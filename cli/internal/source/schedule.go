package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/robfig/cron/v3"
)

const scheduleStateFileName = "schedule-state.json"

var scheduleBranchSafe = regexp.MustCompile(`[^a-z0-9]+`)

type Schedule struct {
	ConfigName string
	Cadence    string
	Workflow   string
	StateDir   string
	Queue      *queue.Queue
}

type scheduleStateEntry struct {
	Cadence     string     `json:"cadence"`
	LastFiredAt *time.Time `json:"last_fired_at,omitempty"`
	NextDueAt   *time.Time `json:"next_due_at,omitempty"`
	LastVessel  string     `json:"last_vessel,omitempty"`
}

func (s *Schedule) Name() string { return "schedule" }

func (s *Schedule) Scan(_ context.Context) ([]queue.Vessel, error) {
	if strings.TrimSpace(s.ConfigName) == "" {
		return nil, fmt.Errorf("schedule source: config name is required")
	}
	sched, err := parseScheduleCadence(s.Cadence)
	if err != nil {
		return nil, fmt.Errorf("schedule source %q: parse cadence: %w", s.ConfigName, err)
	}

	now := sourceNow().UTC()
	state, err := s.loadState()
	if err != nil {
		return nil, fmt.Errorf("schedule source %q: load state: %w", s.ConfigName, err)
	}
	entry := state[s.ConfigName]
	due, firedAt, nextDue := scheduledTick(sched, s.Cadence, entry, now)
	if !due {
		return nil, nil
	}

	ref := scheduleRef(s.ConfigName, firedAt)
	if s.Queue != nil && s.Queue.HasRefAny(ref) {
		return nil, nil
	}

	return []queue.Vessel{{
		ID:       scheduleVesselID(s.ConfigName, firedAt),
		Source:   "schedule",
		Ref:      ref,
		Workflow: s.Workflow,
		Meta: map[string]string{
			"schedule_name":        s.ConfigName,
			"schedule_cadence":     s.Cadence,
			"schedule_fired_at":    firedAt.Format(time.RFC3339),
			"schedule_next_due_at": nextDue.Format(time.RFC3339),
		},
		State:     queue.StatePending,
		CreatedAt: now,
	}}, nil
}

func (s *Schedule) OnEnqueue(_ context.Context, vessel queue.Vessel) error {
	state, err := s.loadState()
	if err != nil {
		return fmt.Errorf("schedule source %q: load state: %w", s.ConfigName, err)
	}

	firedAt, err := time.Parse(time.RFC3339, vessel.Meta["schedule_fired_at"])
	if err != nil {
		return fmt.Errorf("schedule source %q: parse fired_at: %w", s.ConfigName, err)
	}
	nextDue, err := time.Parse(time.RFC3339, vessel.Meta["schedule_next_due_at"])
	if err != nil {
		return fmt.Errorf("schedule source %q: parse next_due_at: %w", s.ConfigName, err)
	}
	firedAt = firedAt.UTC()
	nextDue = nextDue.UTC()

	state[s.ConfigName] = scheduleStateEntry{
		Cadence:     s.Cadence,
		LastFiredAt: &firedAt,
		NextDueAt:   &nextDue,
		LastVessel:  vessel.ID,
	}
	if err := s.saveState(state); err != nil {
		return fmt.Errorf("schedule source %q: save state: %w", s.ConfigName, err)
	}
	return nil
}

func (s *Schedule) OnStart(_ context.Context, _ queue.Vessel) error            { return nil }
func (s *Schedule) OnComplete(_ context.Context, _ queue.Vessel) error         { return nil }
func (s *Schedule) OnFail(_ context.Context, _ queue.Vessel) error             { return nil }
func (s *Schedule) OnTimedOut(_ context.Context, _ queue.Vessel) error         { return nil }
func (s *Schedule) RemoveRunningLabel(_ context.Context, _ queue.Vessel) error { return nil }

func (s *Schedule) BranchName(vessel queue.Vessel) string {
	name := scheduleBranchComponent(vessel.Meta["schedule_name"])
	if name == "" {
		name = "schedule"
	}
	stamp := scheduleStamp(vessel.Meta["schedule_fired_at"])
	if stamp == "" {
		stamp = "tick"
	}
	return fmt.Sprintf("chore/%s-%s", name, stamp)
}

func ValidateCadence(expr string) error {
	_, err := parseScheduleCadence(expr)
	return err
}

func parseScheduleCadence(expr string) (cron.Schedule, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, fmt.Errorf("cadence must not be empty")
	}
	if d, err := time.ParseDuration(trimmed); err == nil {
		if d <= 0 {
			return nil, fmt.Errorf("cadence duration must be greater than 0")
		}
		return everyDuration(d), nil
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid cadence %q: %w", trimmed, err)
	}
	return sched, nil
}

func scheduledTick(sched cron.Schedule, cadence string, entry scheduleStateEntry, now time.Time) (bool, time.Time, time.Time) {
	now = now.UTC()
	cadence = strings.TrimSpace(cadence)
	if entry.Cadence != cadence || entry.NextDueAt == nil {
		nextDue := sched.Next(now)
		return true, now, nextDue.UTC()
	}
	if now.Before(entry.NextDueAt.UTC()) {
		return false, time.Time{}, time.Time{}
	}

	firedAt := entry.NextDueAt.UTC()
	nextDue := sched.Next(firedAt)
	for !nextDue.After(now) {
		nextDue = sched.Next(nextDue)
	}
	return true, firedAt, nextDue.UTC()
}

func (s *Schedule) statePath() string {
	return filepath.Join(s.StateDir, scheduleStateFileName)
}

func (s *Schedule) withStateLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.statePath()), 0o755); err != nil {
		return err
	}
	lock := flock.New(s.statePath() + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock() //nolint:errcheck
	return fn()
}

func (s *Schedule) loadState() (map[string]scheduleStateEntry, error) {
	state := make(map[string]scheduleStateEntry)
	err := s.withStateLock(func() error {
		data, err := os.ReadFile(s.statePath())
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("unmarshal state: %w", err)
		}
		return nil
	})
	return state, err
}

func (s *Schedule) saveState(state map[string]scheduleStateEntry) error {
	return s.withStateLock(func() error {
		if err := os.MkdirAll(filepath.Dir(s.statePath()), 0o755); err != nil {
			return err
		}
		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(s.statePath(), data, 0o644)
	})
}

func scheduleRef(name string, firedAt time.Time) string {
	return fmt.Sprintf("schedule://%s/%s", name, firedAt.UTC().Format("20060102T150405Z"))
}

func scheduleVesselID(name string, firedAt time.Time) string {
	return fmt.Sprintf("schedule-%s-%s", scheduleBranchComponent(name), firedAt.UTC().Format("20060102t150405z"))
}

func scheduleBranchComponent(name string) string {
	clean := scheduleBranchSafe.ReplaceAllString(strings.ToLower(name), "-")
	clean = strings.Trim(clean, "-")
	if clean == "" {
		return "schedule"
	}
	return clean
}

func scheduleStamp(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format("20060102t150405z")
}

type durationSchedule time.Duration

func everyDuration(d time.Duration) cron.Schedule {
	return durationSchedule(d)
}

func (d durationSchedule) Next(t time.Time) time.Time {
	next := t.Add(time.Duration(d))
	return next.UTC()
}
