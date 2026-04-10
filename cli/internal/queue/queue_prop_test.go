package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestPropQueueRoundTripsTier(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "queue-tier-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		q := New(filepath.Join(dir, "queue.jsonl"))
		tier := rapid.StringMatching(`[a-z][a-z0-9-]{0,7}`).Draw(t, "tier")
		vessel := Vessel{
			ID:        "issue-1",
			Source:    "github-issue",
			Ref:       "https://github.com/example/repo/issues/1",
			Workflow:  "fix-bug",
			Tier:      tier,
			State:     StatePending,
			CreatedAt: time.Now().UTC(),
		}

		enqueued, err := q.Enqueue(vessel)
		if err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
		if !enqueued {
			t.Fatal("expected vessel to be enqueued")
		}

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(vessels) != 1 {
			t.Fatalf("len(vessels) = %d, want 1", len(vessels))
		}
		if vessels[0].Tier != tier {
			t.Fatalf("Tier = %q, want %q", vessels[0].Tier, tier)
		}
	})
}
