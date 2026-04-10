package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledScanEnqueuesOnePerWindow(t *testing.T) {
	q := queue.New(filepath.Join(t.TempDir(), "queue.jsonl"))
	source := &Scheduled{
		Repo:     "owner/repo",
		Schedule: "@weekly",
		Queue:    q,
		Tasks: map[string]ScheduledTask{
			"sota": {Workflow: "sota-gap-analysis", Ref: "sota-gap-analysis"},
		},
	}

	vessels, err := source.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("len(vessels) = %d, want 1", len(vessels))
	}
	if vessels[0].Source != "scheduled" {
		t.Fatalf("Source = %q, want scheduled", vessels[0].Source)
	}
	if !strings.HasPrefix(vessels[0].Ref, "scheduled://owner-repo/sota@") {
		t.Fatalf("Ref = %q, want scheduled ref", vessels[0].Ref)
	}
	if _, err := q.Enqueue(vessels[0]); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	second, err := source.Scan(context.Background())
	if err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("len(second) = %d, want 0 after queue already has this schedule window", len(second))
	}
}

func TestParseSchedule(t *testing.T) {
	got, err := parseSchedule("@weekly")
	if err != nil {
		t.Fatalf("parseSchedule(@weekly) error = %v", err)
	}
	if got != 7*24*time.Hour {
		t.Fatalf("parseSchedule(@weekly) = %s, want 168h", got)
	}
}

func TestScheduleWindow(t *testing.T) {
	start, end := scheduleWindow(time.Date(2026, 4, 9, 10, 27, 0, 0, time.UTC), 24*time.Hour)
	if end.Sub(start) != 24*time.Hour {
		t.Fatalf("end-start = %s, want 24h", end.Sub(start))
	}
	if start.After(time.Date(2026, 4, 9, 10, 27, 0, 0, time.UTC)) {
		t.Fatalf("start = %s, want start at or before now", start)
	}
}

func TestSmoke_S1_ContinuousSimplicityScheduledVesselEnqueuesOncePerWindow(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "queue.jsonl")
	q := queue.New(queuePath)
	interval, err := parseSchedule("@weekly")
	require.NoError(t, err)

	src := &Scheduled{
		Repo:       "nicholls-inc/xylem",
		StateDir:   stateDir,
		ConfigName: "continuous-simplicity",
		Schedule:   "@weekly",
		Queue:      q,
		Tasks: map[string]ScheduledTask{
			"weekly-continuous-simplicity": {
				Workflow: "continuous-simplicity",
				Ref:      "continuous-simplicity",
			},
		},
	}

	before := sourceNow()
	first, err := src.Scan(ctx)
	after := sourceNow()
	require.NoError(t, err)
	require.Len(t, first, 1)

	vessel := first[0]
	slotStart, slotEnd := scheduleWindow(vessel.CreatedAt, interval)
	bucket := slotStart.UnixNano() / interval.Nanoseconds()

	assert.Equal(t, "scheduled", vessel.Source)
	assert.Equal(t, queue.StatePending, vessel.State)
	assert.Equal(t, "continuous-simplicity", vessel.Workflow)
	assert.Equal(t, "continuous-simplicity", vessel.Meta["schedule_ref"])
	assert.Equal(t, "weekly-continuous-simplicity", vessel.Meta["schedule_task"])
	assert.Equal(t, "@weekly", vessel.Meta["schedule_spec"])
	assert.Equal(t, "continuous-simplicity", vessel.Meta["scheduled_config_name"])
	assert.Equal(t, "nicholls-inc/xylem", vessel.Meta["scheduled_repo"])
	assert.Equal(t, fmt.Sprintf("%d", bucket), vessel.Meta["scheduled_bucket"])
	assert.Equal(t, slotStart.Format(time.RFC3339), vessel.Meta["schedule_slot_start"])
	assert.Equal(t, slotEnd.Format(time.RFC3339), vessel.Meta["schedule_slot_end"])
	assert.Equal(t, vessel.CreatedAt.UTC().Format(time.RFC3339), vessel.CreatedAt.Format(time.RFC3339))
	assert.True(t, !vessel.CreatedAt.Before(before) && !vessel.CreatedAt.After(after))
	assert.Equal(t,
		"scheduled://continuous-simplicity/weekly-continuous-simplicity@"+vessel.Meta["scheduled_bucket"],
		vessel.Ref,
	)
	assert.Equal(t, "scheduled-continuous-simplicity-weekly-continuous-simplicity-"+vessel.Meta["scheduled_bucket"], vessel.ID)
	assert.Equal(t,
		scheduledFingerprint("continuous-simplicity", "weekly-continuous-simplicity", "continuous-simplicity", "continuous-simplicity"),
		vessel.Meta["scheduled_fingerprint"],
	)

	_, err = q.Enqueue(vessel)
	require.NoError(t, err)

	duplicate, err := src.Scan(ctx)
	require.NoError(t, err)
	assert.Empty(t, duplicate)

	require.NoError(t, src.OnEnqueue(ctx, vessel))

	stateBytes, err := os.ReadFile(filepath.Join(stateDir, "schedules", "continuous-simplicity.json"))
	require.NoError(t, err)
	var persisted scheduleState
	require.NoError(t, json.Unmarshal(stateBytes, &persisted))
	assert.Equal(t, map[string]int64{"weekly-continuous-simplicity": bucket}, persisted.LastEnqueuedBuckets)

	restarted := &Scheduled{
		Repo:       "nicholls-inc/xylem",
		StateDir:   stateDir,
		ConfigName: "continuous-simplicity",
		Schedule:   "@weekly",
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue-restart.jsonl")),
		Tasks: map[string]ScheduledTask{
			"weekly-continuous-simplicity": {
				Workflow: "continuous-simplicity",
				Ref:      "continuous-simplicity",
			},
		},
	}
	suppressed, err := restarted.Scan(ctx)
	require.NoError(t, err)
	assert.Empty(t, suppressed)
}
