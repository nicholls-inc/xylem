package source

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"pgregory.net/rapid"
)

func TestPropScheduledCronWindowContainsNow(t *testing.T) {
	t.Parallel()

	baseStateDir := t.TempDir()
	baseQueueDir := t.TempDir()

	rapid.Check(t, func(t *rapid.T) {
		dayOffset := rapid.IntRange(0, 13).Draw(t, "day_offset")
		hour := rapid.IntRange(0, 23).Draw(t, "hour")
		minute := rapid.IntRange(0, 59).Draw(t, "minute")
		now := time.Date(2026, time.April, 6+dayOffset, hour, minute, 0, 0, time.UTC)
		caseID := fmt.Sprintf("%02d-%02d-%02d", dayOffset, hour, minute)

		src := &Scheduled{
			Repo:     "owner/repo",
			StateDir: filepath.Join(baseStateDir, caseID),
			Schedule: "0 10 * * 1,4",
			Queue:    queue.New(filepath.Join(baseQueueDir, caseID+".jsonl")),
			Now: func() time.Time {
				return now
			},
			Tasks: map[string]ScheduledTask{
				"cut-release": {Workflow: "cut-release"},
			},
		}

		vessels, err := src.Scan(context.Background())
		if err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		if len(vessels) != 1 {
			t.Fatalf("len(vessels) = %d, want 1", len(vessels))
		}

		slotStart, err := time.Parse(time.RFC3339, vessels[0].Meta["schedule_slot_start"])
		if err != nil {
			t.Fatalf("parse schedule_slot_start: %v", err)
		}
		slotEnd, err := time.Parse(time.RFC3339, vessels[0].Meta["schedule_slot_end"])
		if err != nil {
			t.Fatalf("parse schedule_slot_end: %v", err)
		}
		if slotStart.After(now) {
			t.Fatalf("slotStart = %s, want <= now %s", slotStart, now)
		}
		if !slotEnd.After(now) {
			t.Fatalf("slotEnd = %s, want > now %s", slotEnd, now)
		}
		if !slotEnd.After(slotStart) {
			t.Fatalf("slotEnd = %s, want > slotStart %s", slotEnd, slotStart)
		}
	})
}
