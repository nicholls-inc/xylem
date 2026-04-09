package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/nicholls-inc/xylem/cli/internal/cadence"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// Schedule emits recurring synthetic vessels based on a configured cadence.
type Schedule struct {
	ConfigName string
	Cadence    string
	Workflow   string
	StateDir   string
	Queue      *queue.Queue
	Now        func() time.Time
}

type scheduleStateRecord struct {
	LastFiredAt string `json:"last_fired_at"`
}

type scheduleStateFile struct {
	Sources map[string]scheduleStateRecord `json:"sources"`
}

func (s *Schedule) Name() string { return "schedule" }

func (s *Schedule) Scan(_ context.Context) ([]queue.Vessel, error) {
	spec, err := cadence.Parse(s.Cadence)
	if err != nil {
		return nil, fmt.Errorf("schedule source %q: %w", s.ConfigName, err)
	}

	lastFiredAt, err := s.loadLastFiredAt()
	if err != nil {
		return nil, fmt.Errorf("schedule source %q: load last fired state: %w", s.ConfigName, err)
	}

	firedAt, due := spec.FireTime(lastFiredAt, s.now())
	if !due {
		return nil, nil
	}

	active, err := s.hasActiveVessel()
	if err != nil {
		return nil, fmt.Errorf("schedule source %q: inspect active vessels: %w", s.ConfigName, err)
	}
	if active {
		return nil, nil
	}

	vessel := s.buildVessel(firedAt, spec.Raw())
	if s.Queue != nil && s.Queue.HasRefAny(vessel.Ref) {
		return nil, nil
	}
	return []queue.Vessel{vessel}, nil
}

func (s *Schedule) OnEnqueue(_ context.Context, vessel queue.Vessel) error {
	firedAtRaw := vessel.Meta["schedule.fired_at"]
	if firedAtRaw == "" {
		return nil
	}
	firedAt, err := time.Parse(time.RFC3339, firedAtRaw)
	if err != nil {
		return fmt.Errorf("parse schedule fired_at for %s: %w", vessel.ID, err)
	}
	if err := s.storeLastFiredAt(firedAt); err != nil {
		return fmt.Errorf("persist schedule fired_at for %s: %w", vessel.ID, err)
	}
	return nil
}

func (s *Schedule) OnStart(_ context.Context, _ queue.Vessel) error            { return nil }
func (s *Schedule) OnWait(_ context.Context, _ queue.Vessel) error             { return nil }
func (s *Schedule) OnResume(_ context.Context, _ queue.Vessel) error           { return nil }
func (s *Schedule) OnComplete(_ context.Context, _ queue.Vessel) error         { return nil }
func (s *Schedule) OnFail(_ context.Context, _ queue.Vessel) error             { return nil }
func (s *Schedule) OnTimedOut(_ context.Context, _ queue.Vessel) error         { return nil }
func (s *Schedule) RemoveRunningLabel(_ context.Context, _ queue.Vessel) error { return nil }

func (s *Schedule) BranchName(vessel queue.Vessel) string {
	sourceName := scheduleSlug(s.ConfigName)
	firedAt := scheduleFiredAtKey(vessel.Meta["schedule.fired_at"])
	return fmt.Sprintf("chore/schedule-%s-%s", sourceName, firedAt)
}

func (s *Schedule) buildVessel(firedAt time.Time, rawCadence string) queue.Vessel {
	firedAt = firedAt.UTC().Truncate(time.Second)
	idTime := firedAt.Format("20060102t150405z")
	refTime := firedAt.Format(time.RFC3339)
	configName := s.ConfigName
	if configName == "" {
		configName = "schedule"
	}
	return queue.Vessel{
		ID:       fmt.Sprintf("schedule-%s-%s", scheduleSlug(configName), idTime),
		Source:   s.Name(),
		Ref:      fmt.Sprintf("schedule://%s/%s", configName, refTime),
		Workflow: s.Workflow,
		Meta: map[string]string{
			"schedule.cadence":     rawCadence,
			"schedule.fired_at":    refTime,
			"schedule.source_name": configName,
		},
		State:     queue.StatePending,
		CreatedAt: firedAt,
	}
}

func (s *Schedule) loadLastFiredAt() (*time.Time, error) {
	state, err := s.readState()
	if err != nil {
		return nil, err
	}
	record, ok := state.Sources[s.ConfigName]
	if !ok || strings.TrimSpace(record.LastFiredAt) == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339, record.LastFiredAt)
	if err != nil {
		return nil, fmt.Errorf("parse last_fired_at %q: %w", record.LastFiredAt, err)
	}
	ts = ts.UTC()
	return &ts, nil
}

func (s *Schedule) storeLastFiredAt(firedAt time.Time) error {
	if err := os.MkdirAll(filepath.Dir(s.statePath()), 0o755); err != nil {
		return fmt.Errorf("create schedule state directory: %w", err)
	}
	lock := flock.New(s.statePath() + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock schedule state: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()

	state, err := s.readStateUnlocked()
	if err != nil {
		return err
	}
	if state.Sources == nil {
		state.Sources = make(map[string]scheduleStateRecord)
	}
	state.Sources[s.ConfigName] = scheduleStateRecord{
		LastFiredAt: firedAt.UTC().Truncate(time.Second).Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal schedule state: %w", err)
	}
	if err := os.WriteFile(s.statePath(), data, 0o644); err != nil {
		return fmt.Errorf("write schedule state: %w", err)
	}
	return nil
}

func (s *Schedule) readState() (scheduleStateFile, error) {
	if err := os.MkdirAll(filepath.Dir(s.statePath()), 0o755); err != nil {
		return scheduleStateFile{}, fmt.Errorf("create schedule state directory: %w", err)
	}
	lock := flock.New(s.statePath() + ".lock")
	if err := lock.RLock(); err != nil {
		return scheduleStateFile{}, fmt.Errorf("lock schedule state for read: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()
	return s.readStateUnlocked()
}

func (s *Schedule) readStateUnlocked() (scheduleStateFile, error) {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return scheduleStateFile{Sources: map[string]scheduleStateRecord{}}, nil
		}
		return scheduleStateFile{}, fmt.Errorf("read schedule state: %w", err)
	}
	var state scheduleStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return scheduleStateFile{}, fmt.Errorf("parse schedule state: %w", err)
	}
	if state.Sources == nil {
		state.Sources = map[string]scheduleStateRecord{}
	}
	return state, nil
}

func (s *Schedule) statePath() string {
	return filepath.Join(s.StateDir, "state", "schedule.json")
}

func (s *Schedule) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return sourceNow()
}

func (s *Schedule) hasActiveVessel() (bool, error) {
	if s.Queue == nil {
		return false, nil
	}

	vessels, err := s.Queue.List()
	if err != nil {
		return false, fmt.Errorf("list queue: %w", err)
	}
	for _, vessel := range vessels {
		if vessel.State.IsTerminal() {
			continue
		}
		if vessel.Meta["schedule.source_name"] == s.ConfigName {
			return true, nil
		}
	}
	return false, nil
}

func scheduleSlug(name string) string {
	slug := nonAlphaNum.ReplaceAllString(strings.ToLower(name), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "schedule"
	}
	return slug
}

func scheduleFiredAtKey(raw string) string {
	firedAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "tick"
	}
	return firedAt.UTC().Format("20060102t150405z")
}
