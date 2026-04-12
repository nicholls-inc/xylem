package memory

import (
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// TestPropEpisodicAppendPreservesOrder asserts that All() returns entries in
// the exact order they were appended, with all fields preserved.
func TestPropEpisodicAppendPreservesOrder(t *testing.T) {
	dir := tempDirForRapid(t)

	rapid.Check(t, func(rt *rapid.T) {
		// Fresh file per iteration.
		path := filepath.Join(dir, rapid.StringMatching(`[a-z]{8}`).Draw(rt, "filename")+".jsonl")

		n := rapid.IntRange(0, 20).Draw(rt, "n")
		outcomes := []string{"completed", "failed", "no-op"}
		vessels := []string{"v1", "v2", "v3"}

		entries := make([]EpisodicEntry, n)
		for i := range n {
			entries[i] = EpisodicEntry{
				VesselID:   rapid.SampledFrom(vessels).Draw(rt, "vessel"),
				PhaseName:  rapid.StringMatching(`[a-z][a-z0-9-]{0,9}`).Draw(rt, "phase"),
				RecordedAt: time.Now().UTC(),
				Outcome:    rapid.SampledFrom(outcomes).Draw(rt, "outcome"),
				Summary:    rapid.StringMatching(`[a-z ]{1,30}`).Draw(rt, "summary"),
			}
		}

		s := NewEpisodicStore(path)
		for _, e := range entries {
			if err := s.Append(e); err != nil {
				rt.Fatalf("Append: %v", err)
			}
		}

		got, err := s.All()
		if err != nil {
			rt.Fatalf("All: %v", err)
		}
		if len(got) != len(entries) {
			rt.Fatalf("All: got %d entries, want %d", len(got), len(entries))
		}
		for i := range entries {
			if got[i].VesselID != entries[i].VesselID ||
				got[i].PhaseName != entries[i].PhaseName ||
				got[i].Outcome != entries[i].Outcome ||
				got[i].Summary != entries[i].Summary {
				rt.Fatalf("entry[%d] mismatch: got %+v, want %+v", i, got[i], entries[i])
			}
		}
	})
}

// TestPropEpisodicRecentForVesselSubset asserts that RecentForVessel returns
// a suffix of the vessel-filtered All() slice, with len ≤ min(n, total).
func TestPropEpisodicRecentForVesselSubset(t *testing.T) {
	dir := tempDirForRapid(t)

	rapid.Check(t, func(rt *rapid.T) {
		path := filepath.Join(dir, rapid.StringMatching(`[a-z]{8}`).Draw(rt, "filename")+".jsonl")

		vessels := []string{"alpha", "beta", "gamma"}
		total := rapid.IntRange(0, 30).Draw(rt, "total")
		n := rapid.IntRange(1, 15).Draw(rt, "n")

		s := NewEpisodicStore(path)
		for range total {
			e := EpisodicEntry{
				VesselID:  rapid.SampledFrom(vessels).Draw(rt, "vessel"),
				PhaseName: "phase",
				Outcome:   "completed",
				Summary:   "s",
			}
			if err := s.Append(e); err != nil {
				rt.Fatalf("Append: %v", err)
			}
		}

		target := rapid.SampledFrom(vessels).Draw(rt, "target")

		all, err := s.All()
		if err != nil {
			rt.Fatalf("All: %v", err)
		}

		// Build expected: all filtered to target.
		var filtered []EpisodicEntry
		for _, e := range all {
			if e.VesselID == target {
				filtered = append(filtered, e)
			}
		}

		got, err := s.RecentForVessel(target, n)
		if err != nil {
			rt.Fatalf("RecentForVessel: %v", err)
		}

		// len(got) <= min(n, len(filtered))
		maxExpected := n
		if len(filtered) < maxExpected {
			maxExpected = len(filtered)
		}
		if len(got) != maxExpected {
			rt.Fatalf("RecentForVessel len: got %d, want %d (filtered=%d, n=%d)", len(got), maxExpected, len(filtered), n)
		}

		// got must be a suffix of filtered.
		offset := len(filtered) - len(got)
		for i, e := range got {
			if e.VesselID != filtered[offset+i].VesselID ||
				e.PhaseName != filtered[offset+i].PhaseName {
				rt.Fatalf("entry[%d] is not a suffix of filtered slice", i)
			}
		}
	})
}
