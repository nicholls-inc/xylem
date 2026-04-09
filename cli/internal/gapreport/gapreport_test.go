package gapreport

import "testing"

func TestDiffClassifiesChanges(t *testing.T) {
	previous := &Snapshot{
		Version:     1,
		Repo:        "owner/repo",
		GeneratedAt: "2026-04-01T00:00:00Z",
		Capabilities: []Capability{
			testCapability("context", "Context isolation", "context", StatusDormant, 5),
			testCapability("tools", "Tool catalog", "tools", StatusWired, 4),
		},
	}
	current := &Snapshot{
		Version:     1,
		Repo:        "owner/repo",
		GeneratedAt: "2026-04-08T00:00:00Z",
		Capabilities: []Capability{
			testCapability("context", "Context isolation", "context", StatusWired, 5),
			testCapability("tools", "Tool catalog", "tools", StatusDormant, 4),
			testCapability("entropy", "Entropy management", "entropy", StatusNotImplemented, 9),
		},
	}

	delta, err := Diff(previous, current)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(delta.Improvements) != 1 || delta.Improvements[0].Key != "context" {
		t.Fatalf("Improvements = %#v, want context improvement", delta.Improvements)
	}
	if len(delta.Regressions) != 1 || delta.Regressions[0].Key != "tools" {
		t.Fatalf("Regressions = %#v, want tools regression", delta.Regressions)
	}
	if len(delta.NewGaps) != 2 {
		t.Fatalf("NewGaps = %#v, want 2 entries", delta.NewGaps)
	}
	if delta.NewGaps[0].Key != "entropy" {
		t.Fatalf("NewGaps[0] = %#v, want entropy first by priority", delta.NewGaps[0])
	}
}

func TestSnapshotValidateRejectsDuplicateKeys(t *testing.T) {
	snapshot := &Snapshot{
		Version:     1,
		Repo:        "owner/repo",
		GeneratedAt: "2026-04-08T00:00:00Z",
		Capabilities: []Capability{
			testCapability("context", "Context isolation", "context", StatusWired, 1),
			testCapability("context", "Context isolation duplicate", "context", StatusDormant, 1),
		},
	}

	if err := snapshot.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want duplicate capability key error")
	}
}

func TestShouldFail(t *testing.T) {
	delta := &Delta{}
	if !ShouldFail(delta, &FileResult{}) {
		t.Fatal("ShouldFail() = false, want true when no issues filed and no improvement")
	}
	delta.Improvements = append(delta.Improvements, CapabilityDelta{Key: "context"})
	if ShouldFail(delta, &FileResult{}) {
		t.Fatal("ShouldFail() = true, want false when improvement exists")
	}
}

func testCapability(key, name, layer, status string, priority int) Capability {
	return Capability{
		Key:          key,
		Name:         name,
		Layer:        layer,
		Status:       status,
		Summary:      "summary",
		Priority:     priority,
		SpecSections: []string{"docs/spec.md §1"},
		CodeEvidence: []CodeEvidence{{
			Path:      "cli/internal/example.go",
			LineStart: 10,
			Summary:   "evidence",
		}},
	}
}
