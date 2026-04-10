package source

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func TestScheduledScanEnqueuesOnePerWindow(t *testing.T) {
	now := time.Date(2026, time.April, 9, 10, 27, 0, 0, time.UTC)
	q := queue.New(filepath.Join(t.TempDir(), "queue.jsonl"))
	source := &Scheduled{
		Repo:     "owner/repo",
		Schedule: "@weekly",
		Queue:    q,
		Now: func() time.Time {
			return now
		},
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

func TestScheduledScanSupportsCronCadence(t *testing.T) {
	stateDir := t.TempDir()
	q := queue.New(filepath.Join(t.TempDir(), "queue.jsonl"))
	now := time.Date(2026, time.April, 6, 10, 0, 0, 0, time.UTC) // Monday
	source := &Scheduled{
		Repo:     "owner/repo",
		StateDir: stateDir,
		Schedule: "0 10 * * 1,4",
		Queue:    q,
		Now: func() time.Time {
			return now
		},
		Tasks: map[string]ScheduledTask{
			"cut-release": {Workflow: "cut-release", Ref: "release-please-cut"},
		},
	}

	first, err := source.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("len(first) = %d, want 1", len(first))
	}
	if got, want := first[0].Meta["schedule_slot_start"], "2026-04-06T10:00:00Z"; got != want {
		t.Fatalf("schedule_slot_start = %q, want %q", got, want)
	}
	if got, want := first[0].Meta["schedule_slot_end"], "2026-04-09T10:00:00Z"; got != want {
		t.Fatalf("schedule_slot_end = %q, want %q", got, want)
	}
	if err := source.OnEnqueue(context.Background(), first[0]); err != nil {
		t.Fatalf("OnEnqueue() error = %v", err)
	}

	now = time.Date(2026, time.April, 8, 12, 0, 0, 0, time.UTC) // Wednesday, same slot
	suppressed, err := source.Scan(context.Background())
	if err != nil {
		t.Fatalf("suppressed Scan() error = %v", err)
	}
	if len(suppressed) != 0 {
		t.Fatalf("len(suppressed) = %d, want 0 within the same cron slot", len(suppressed))
	}

	now = time.Date(2026, time.April, 9, 10, 0, 0, 0, time.UTC) // Thursday
	second, err := source.Scan(context.Background())
	if err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("len(second) = %d, want 1 after the next cron slot begins", len(second))
	}
	if got, want := second[0].Meta["schedule_slot_start"], "2026-04-09T10:00:00Z"; got != want {
		t.Fatalf("second schedule_slot_start = %q, want %q", got, want)
	}
}

func TestScheduledScanRejectsMalformedSchedule(t *testing.T) {
	source := &Scheduled{
		Repo:     "owner/repo",
		Schedule: "weeklyish",
		Queue:    queue.New(filepath.Join(t.TempDir(), "queue.jsonl")),
		Tasks: map[string]ScheduledTask{
			"sota": {Workflow: "sota-gap-analysis"},
		},
	}

	if _, err := source.Scan(context.Background()); err == nil {
		t.Fatal("Scan() error = nil, want malformed schedule error")
	}
}
