package source

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

type ScheduledTask struct {
	Workflow string
}

type Scheduled struct {
	Repo       string
	StateDir   string
	ConfigName string
	Schedule   time.Duration
	Tasks      map[string]ScheduledTask
}

type scheduleState struct {
	LastEnqueuedBuckets map[string]int64 `json:"last_enqueued_buckets,omitempty"`
}

var safeScheduledPathComponent = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func (s *Scheduled) Name() string { return "scheduled" }

func (s *Scheduled) Scan(_ context.Context) ([]queue.Vessel, error) {
	if s.Schedule <= 0 {
		return nil, fmt.Errorf("scheduled source %q: schedule must be greater than 0", s.ConfigName)
	}

	state, err := s.loadState()
	if err != nil {
		return nil, err
	}

	now := sourceNow()
	bucket := now.UnixNano() / s.Schedule.Nanoseconds()
	taskNames := make([]string, 0, len(s.Tasks))
	for taskName := range s.Tasks {
		taskNames = append(taskNames, taskName)
	}
	sort.Strings(taskNames)

	vessels := make([]queue.Vessel, 0, len(taskNames))
	for _, taskName := range taskNames {
		if state.LastEnqueuedBuckets[taskName] >= bucket {
			continue
		}
		task := s.Tasks[taskName]
		ref := fmt.Sprintf("scheduled://%s/%s@%d", s.ConfigName, taskName, bucket)
		id := fmt.Sprintf("scheduled-%s-%s-%d", sanitizeScheduledComponent(s.ConfigName), sanitizeScheduledComponent(taskName), bucket)
		vessels = append(vessels, queue.Vessel{
			ID:       id,
			Source:   s.Name(),
			Ref:      ref,
			Workflow: task.Workflow,
			Meta: map[string]string{
				"scheduled_config_name": s.ConfigName,
				"scheduled_task_name":   taskName,
				"scheduled_bucket":      fmt.Sprintf("%d", bucket),
				"scheduled_repo":        s.Repo,
				"scheduled_fingerprint": scheduledFingerprint(s.ConfigName, taskName, task.Workflow),
			},
			State:     queue.StatePending,
			CreatedAt: now,
		})
	}

	return vessels, nil
}

func (s *Scheduled) OnEnqueue(_ context.Context, vessel queue.Vessel) error {
	taskName := strings.TrimSpace(vessel.Meta["scheduled_task_name"])
	bucketRaw := strings.TrimSpace(vessel.Meta["scheduled_bucket"])
	if taskName == "" || bucketRaw == "" {
		return nil
	}
	parsedBucket, parseErr := parseInt64(bucketRaw)
	if parseErr != nil {
		return fmt.Errorf("scheduled source %q: parse bucket %q: %w", s.ConfigName, bucketRaw, parseErr)
	}

	state, err := s.loadState()
	if err != nil {
		return err
	}
	if state.LastEnqueuedBuckets == nil {
		state.LastEnqueuedBuckets = make(map[string]int64)
	}
	state.LastEnqueuedBuckets[taskName] = parsedBucket
	if err := s.saveState(state); err != nil {
		return err
	}
	return nil
}

func (s *Scheduled) OnStart(_ context.Context, _ queue.Vessel) error            { return nil }
func (s *Scheduled) OnWait(_ context.Context, _ queue.Vessel) error             { return nil }
func (s *Scheduled) OnResume(_ context.Context, _ queue.Vessel) error           { return nil }
func (s *Scheduled) OnComplete(_ context.Context, _ queue.Vessel) error         { return nil }
func (s *Scheduled) OnFail(_ context.Context, _ queue.Vessel) error             { return nil }
func (s *Scheduled) OnTimedOut(_ context.Context, _ queue.Vessel) error         { return nil }
func (s *Scheduled) RemoveRunningLabel(_ context.Context, _ queue.Vessel) error { return nil }

func (s *Scheduled) BranchName(vessel queue.Vessel) string {
	taskName := sanitizeScheduledComponent(vessel.Meta["scheduled_task_name"])
	if taskName == "" {
		taskName = "task"
	}
	return fmt.Sprintf("scheduled/%s-%s", taskName, sanitizeScheduledComponent(vessel.ID))
}

func (s *Scheduled) loadState() (*scheduleState, error) {
	path := s.statePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &scheduleState{LastEnqueuedBuckets: make(map[string]int64)}, nil
		}
		return nil, fmt.Errorf("scheduled source %q: read state: %w", s.ConfigName, err)
	}
	var state scheduleState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("scheduled source %q: unmarshal state: %w", s.ConfigName, err)
	}
	if state.LastEnqueuedBuckets == nil {
		state.LastEnqueuedBuckets = make(map[string]int64)
	}
	return &state, nil
}

func (s *Scheduled) saveState(state *scheduleState) error {
	path := s.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("scheduled source %q: create state dir: %w", s.ConfigName, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("scheduled source %q: marshal state: %w", s.ConfigName, err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("scheduled source %q: write temp state: %w", s.ConfigName, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("scheduled source %q: rename temp state: %w", s.ConfigName, err)
	}
	return nil
}

func (s *Scheduled) statePath() string {
	return filepath.Join(s.StateDir, "schedules", sanitizeScheduledComponent(s.ConfigName)+".json")
}

func sanitizeScheduledComponent(s string) string {
	clean := strings.TrimSpace(s)
	if clean == "" {
		return "scheduled"
	}
	clean = safeScheduledPathComponent.ReplaceAllString(clean, "-")
	clean = strings.Trim(clean, "-")
	if clean == "" {
		return "scheduled"
	}
	return clean
}

func scheduledFingerprint(configName, taskName, workflow string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{configName, taskName, workflow}, "\n")))
	return fmt.Sprintf("%x", sum[:8])
}

func parseInt64(raw string) (int64, error) {
	var value int64
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		return 0, err
	}
	return value, nil
}
