package source

import (
	"context"
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

func TestScheduledScanEncodesTaskParamsIntoVesselMeta(t *testing.T) {
	t.Parallel()

	src := &Scheduled{
		Repo:     "owner/repo",
		Schedule: "@weekly",
		Queue:    queue.New(filepath.Join(t.TempDir(), "queue.jsonl")),
		Tasks: map[string]ScheduledTask{
			"semantic": {
				Workflow: "continuous-refactoring",
				Params: map[string]any{
					"mode":               "semantic_refactor",
					"max_issues_per_run": 3,
				},
			},
		},
	}

	vessels, err := src.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)

	decoded, err := DecodeTaskParams(vessels[0].Meta[TaskParamsMetaKey])
	require.NoError(t, err)
	assert.Equal(t, "semantic_refactor", decoded["mode"])
	assert.Equal(t, float64(3), decoded["max_issues_per_run"])
}
