package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func makeEpisodicEntry(vesselID, phase, outcome string) EpisodicEntry {
	return EpisodicEntry{
		VesselID:   vesselID,
		PhaseName:  phase,
		RecordedAt: time.Now().UTC(),
		Outcome:    outcome,
		Summary:    "summary for " + phase,
	}
}

// ---------- TestEpisodicStore_AppendAndAll ----------

func TestEpisodicStore_AppendAndAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episodic.jsonl")
	s := NewEpisodicStore(path)

	entries := []EpisodicEntry{
		makeEpisodicEntry("v1", "phase-a", "completed"),
		makeEpisodicEntry("v1", "phase-b", "completed"),
		makeEpisodicEntry("v2", "phase-a", "failed"),
	}
	for _, e := range entries {
		if err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := s.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("All: got %d entries, want %d", len(got), len(entries))
	}
	for i, e := range entries {
		if got[i].VesselID != e.VesselID || got[i].PhaseName != e.PhaseName || got[i].Outcome != e.Outcome {
			t.Errorf("entry[%d]: got %+v, want %+v", i, got[i], e)
		}
	}
}

// ---------- TestEpisodicStore_AllEmptyFileNotExist ----------

func TestEpisodicStore_AllEmptyFileNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-file.jsonl")
	s := NewEpisodicStore(path)

	got, err := s.All()
	if err != nil {
		t.Fatalf("All on missing file: %v", err)
	}
	if got == nil {
		t.Fatal("All: expected non-nil slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("All: got %d entries, want 0", len(got))
	}
}

// ---------- TestEpisodicStore_RecentForVessel_Filter ----------

func TestEpisodicStore_RecentForVessel_Filter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episodic.jsonl")
	s := NewEpisodicStore(path)

	for _, e := range []EpisodicEntry{
		makeEpisodicEntry("vessel-A", "phase-a", "completed"),
		makeEpisodicEntry("vessel-B", "phase-a", "completed"),
		makeEpisodicEntry("vessel-A", "phase-b", "completed"),
		makeEpisodicEntry("vessel-B", "phase-b", "failed"),
	} {
		if err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := s.RecentForVessel("vessel-A", 0)
	if err != nil {
		t.Fatalf("RecentForVessel: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries for vessel-A, want 2", len(got))
	}
	for _, e := range got {
		if e.VesselID != "vessel-A" {
			t.Errorf("unexpected vessel ID %q in result", e.VesselID)
		}
	}
}

// ---------- TestEpisodicStore_RecentForVessel_Limit ----------

func TestEpisodicStore_RecentForVessel_Limit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episodic.jsonl")
	s := NewEpisodicStore(path)

	for i := range 20 {
		e := EpisodicEntry{
			VesselID:  "v1",
			PhaseName: "phase",
			Outcome:   "completed",
			Summary:   strings.Repeat("x", i+1), // unique so we can identify
		}
		if err := s.Append(e); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	got, err := s.RecentForVessel("v1", 5)
	if err != nil {
		t.Fatalf("RecentForVessel: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d entries, want 5", len(got))
	}
	// The last 5 entries (i=15..19) have summaries of lengths 16..20, in that
	// order. Verify both selection and append-order preservation.
	for j, e := range got {
		wantLen := 16 + j
		if len(e.Summary) != wantLen {
			t.Errorf("got[%d].Summary len = %d, want %d (entry %d of last-5)",
				j, len(e.Summary), wantLen, j)
		}
	}
}

// ---------- TestEpisodicStore_RecentForVessel_ZeroN ----------

func TestEpisodicStore_RecentForVessel_ZeroN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episodic.jsonl")
	s := NewEpisodicStore(path)

	for range 10 {
		if err := s.Append(makeEpisodicEntry("v1", "p", "completed")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	for _, n := range []int{0, -1} {
		got, err := s.RecentForVessel("v1", n)
		if err != nil {
			t.Fatalf("RecentForVessel(n=%d): %v", n, err)
		}
		if len(got) != 10 {
			t.Errorf("RecentForVessel(n=%d): got %d entries, want 10", n, len(got))
		}
	}
}

// ---------- TestEpisodicStore_AppendConcurrent ----------

func TestEpisodicStore_AppendConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episodic.jsonl")
	s := NewEpisodicStore(path)

	const goroutines = 20
	const perGoroutine = 5
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range perGoroutine {
				e := EpisodicEntry{
					VesselID:  "v1",
					PhaseName: "phase",
					Outcome:   "completed",
					Summary:   strings.Repeat("x", g*perGoroutine+i),
				}
				if err := s.Append(e); err != nil {
					t.Errorf("Append goroutine %d entry %d: %v", g, i, err)
				}
			}
		}(g)
	}
	wg.Wait()

	all, err := s.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != goroutines*perGoroutine {
		t.Fatalf("got %d entries, want %d", len(all), goroutines*perGoroutine)
	}

	// Verify no JSONL corruption by confirming each line is valid JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e EpisodicEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d is corrupt JSON: %v", i, err)
		}
	}
}

// ---------- TestEpisodicStore_BlankLinesTolerated ----------

func TestEpisodicStore_BlankLinesTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episodic.jsonl")

	// Manually write a file with blank lines interspersed.
	e := makeEpisodicEntry("v1", "phase-a", "completed")
	b, _ := json.Marshal(e)
	content := "\n" + string(b) + "\n\n" + string(b) + "\n\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := NewEpisodicStore(path)
	all, err := s.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d entries, want 2", len(all))
	}
}
