package source

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// ScheduledTask defines a workflow fired on a fixed cadence.
type ScheduledTask struct {
	Workflow string
	Ref      string
}

// Scheduled enqueues recurring vessels keyed by schedule window.
type Scheduled struct {
	Repo     string
	Schedule string
	Tasks    map[string]ScheduledTask
	Queue    *queue.Queue
}

func (s *Scheduled) Name() string { return "scheduled" }

func (s *Scheduled) Scan(_ context.Context) ([]queue.Vessel, error) {
	interval, err := parseSchedule(s.Schedule)
	if err != nil {
		return nil, fmt.Errorf("parse schedule %q: %w", s.Schedule, err)
	}
	slotStart, slotEnd := scheduleWindow(sourceNow(), interval)
	taskNames := make([]string, 0, len(s.Tasks))
	for name := range s.Tasks {
		taskNames = append(taskNames, name)
	}
	sort.Strings(taskNames)

	var vessels []queue.Vessel
	for _, name := range taskNames {
		task := s.Tasks[name]
		ref := scheduledRef(s.Repo, name, slotStart)
		if s.Queue != nil && s.Queue.HasRefAny(ref) {
			continue
		}
		vessels = append(vessels, queue.Vessel{
			ID:       scheduledID(name, slotStart),
			Source:   s.Name(),
			Ref:      ref,
			Workflow: task.Workflow,
			Meta: map[string]string{
				"schedule_task":       name,
				"schedule_ref":        task.Ref,
				"schedule_spec":       s.Schedule,
				"schedule_slot_start": slotStart.Format(time.RFC3339),
				"schedule_slot_end":   slotEnd.Format(time.RFC3339),
				"repo":                s.Repo,
			},
			State:     queue.StatePending,
			CreatedAt: sourceNow(),
		})
	}
	return vessels, nil
}

func (s *Scheduled) OnEnqueue(_ context.Context, _ queue.Vessel) error          { return nil }
func (s *Scheduled) OnStart(_ context.Context, _ queue.Vessel) error            { return nil }
func (s *Scheduled) OnComplete(_ context.Context, _ queue.Vessel) error         { return nil }
func (s *Scheduled) OnFail(_ context.Context, _ queue.Vessel) error             { return nil }
func (s *Scheduled) OnTimedOut(_ context.Context, _ queue.Vessel) error         { return nil }
func (s *Scheduled) RemoveRunningLabel(_ context.Context, _ queue.Vessel) error { return nil }

func (s *Scheduled) BranchName(vessel queue.Vessel) string {
	name := vessel.Meta["schedule_task"]
	if name == "" {
		name = vessel.ID
	}
	return fmt.Sprintf("schedule/%s", slugify(name))
}

func parseSchedule(value string) (time.Duration, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "@hourly":
		return time.Hour, nil
	case "@daily":
		return 24 * time.Hour, nil
	case "@weekly":
		return 7 * 24 * time.Hour, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if interval <= 0 {
		return 0, fmt.Errorf("must be greater than 0")
	}
	return interval, nil
}

func scheduleWindow(now time.Time, interval time.Duration) (time.Time, time.Time) {
	now = now.UTC()
	size := interval.Nanoseconds()
	startUnix := now.UnixNano() / size * size
	start := time.Unix(0, startUnix).UTC()
	return start, start.Add(interval)
}

func scheduledRef(repo, taskName string, slotStart time.Time) string {
	scope := strings.Trim(strings.ReplaceAll(repo, "/", "-"), "-")
	if scope == "" {
		scope = "global"
	}
	return fmt.Sprintf("schedule://%s/%s/%s", scope, taskName, slotStart.UTC().Format(time.RFC3339))
}

func scheduledID(taskName string, slotStart time.Time) string {
	return fmt.Sprintf("scheduled-%s-%s", slugify(taskName), slotStart.UTC().Format("20060102t150405z"))
}
