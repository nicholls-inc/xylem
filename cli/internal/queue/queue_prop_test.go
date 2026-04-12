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

func TestPropQueueRoundTripsWorkflowDigest(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "queue-workflow-digest-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		q := New(filepath.Join(dir, "queue.jsonl"))
		digest := rapid.StringMatching(`wf-[0-9a-f]{8,64}`).Draw(t, "workflow_digest")
		vessel := Vessel{
			ID:             "issue-1",
			Source:         "github-issue",
			Ref:            "https://github.com/example/repo/issues/1",
			Workflow:       "fix-bug",
			WorkflowDigest: digest,
			State:          StatePending,
			CreatedAt:      time.Now().UTC(),
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
		if vessels[0].WorkflowDigest != digest {
			t.Fatalf("WorkflowDigest = %q, want %q", vessels[0].WorkflowDigest, digest)
		}
	})
}

// drawVesselState draws a random VesselState from the full set of known states.
func drawVesselState(t *rapid.T, label string) VesselState {
	states := []VesselState{
		StatePending, StateRunning, StateCompleted,
		StateFailed, StateCancelled, StateTimedOut, StateWaiting,
	}
	idx := rapid.IntRange(0, len(states)-1).Draw(t, label)
	return states[idx]
}

// drawTime draws a random time within ±30 days of now.
func drawTime(t *rapid.T, label string) time.Time {
	offsetDays := rapid.IntRange(-30, 30).Draw(t, label)
	return time.Now().UTC().Add(time.Duration(offsetDays) * 24 * time.Hour)
}

func TestPropCompactOlderThan_NeverRemovesNonTerminal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 10).Draw(t, "n")
		vessels := make([]Vessel, n)
		for i := range vessels {
			st := drawVesselState(t, "state")
			ended := drawTime(t, "ended")
			var endedPtr *time.Time
			if rapid.Bool().Draw(t, "has_ended") {
				endedPtr = &ended
			}
			vessels[i] = Vessel{
				ID:      rapid.StringMatching(`[a-z][a-z0-9]{0,7}`).Draw(t, "id"),
				State:   st,
				EndedAt: endedPtr,
			}
		}

		cutoff := drawTime(t, "cutoff")
		kept, _ := compactVesselsOlderThan(vessels, cutoff)

		// Every non-terminal vessel from the input must appear in kept.
		for _, v := range vessels {
			if !v.State.IsTerminal() {
				found := false
				for _, k := range kept {
					if k.ID == v.ID && k.State == v.State {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("non-terminal vessel %s/%s was removed by compactVesselsOlderThan", v.ID, v.State)
				}
			}
		}
	})
}

func TestPropCompactOlderThan_RetainsByAge(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cutoff := time.Now().UTC()

		n := rapid.IntRange(1, 10).Draw(t, "n")
		type entry struct {
			id     string
			ended  time.Time
			before bool // true if EndedAt < cutoff
		}
		entries := make([]entry, n)
		vessels := make([]Vessel, n)
		for i := range entries {
			offsetHours := rapid.IntRange(-48, 48).Draw(t, "offset_hours")
			ended := cutoff.Add(time.Duration(offsetHours) * time.Hour)
			e := entry{
				id:     rapid.StringMatching(`[a-z][a-z0-9]{0,5}`).Draw(t, "id"),
				ended:  ended,
				before: ended.Before(cutoff),
			}
			entries[i] = e
			vessels[i] = Vessel{
				ID:      e.id,
				State:   StateCompleted, // terminal, with EndedAt set
				EndedAt: &ended,
			}
		}

		kept, _ := compactVesselsOlderThan(vessels, cutoff)

		// All kept vessels must have EndedAt >= cutoff (or nil EndedAt).
		for _, k := range kept {
			if k.EndedAt != nil && k.EndedAt.Before(cutoff) {
				t.Fatalf("vessel %s with EndedAt %v before cutoff %v survived compaction", k.ID, k.EndedAt, cutoff)
			}
		}
	})
}

func TestPropCompactOlderThan_SubsetOfInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 15).Draw(t, "n")
		vessels := make([]Vessel, n)
		for i := range vessels {
			ended := drawTime(t, "ended")
			vessels[i] = Vessel{
				ID:      rapid.StringMatching(`[a-z][a-z0-9]{0,7}`).Draw(t, "id"),
				State:   drawVesselState(t, "state"),
				EndedAt: &ended,
			}
		}

		cutoff := drawTime(t, "cutoff")
		kept, _ := compactVesselsOlderThan(vessels, cutoff)

		// Build a set of input IDs for membership testing.
		inputIDs := make(map[string]bool, len(vessels))
		for _, v := range vessels {
			inputIDs[v.ID] = true
		}

		for _, k := range kept {
			if !inputIDs[k.ID] {
				t.Fatalf("vessel %s in output was not in input", k.ID)
			}
		}

		if len(kept) > len(vessels) {
			t.Fatalf("output len %d > input len %d", len(kept), len(vessels))
		}
	})
}
