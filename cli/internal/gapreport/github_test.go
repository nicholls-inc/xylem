package gapreport

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type mockRunner struct {
	calls   [][]string
	outputs map[string][]byte
}

func (m *mockRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	m.calls = append(m.calls, call)
	if m.outputs == nil {
		return nil, nil
	}
	return m.outputs[strings.Join(call, " ")], nil
}

// sequentialMockRunner returns outputs in call order, ignoring the command key.
type sequentialMockRunner struct {
	calls   [][]string
	outputs [][]byte
	errors  []error
	idx     int
}

func (m *sequentialMockRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	m.calls = append(m.calls, call)
	i := m.idx
	m.idx++
	if i < len(m.errors) && m.errors[i] != nil {
		return nil, m.errors[i]
	}
	if i < len(m.outputs) {
		return m.outputs[i], nil
	}
	return nil, nil
}

func TestFileIssuesCreatesAndDedupes(t *testing.T) {
	searchOut, err := json.Marshal([]issueSummary{{
		Number: 7,
		Title:  "[sota-gap] Existing gap",
		URL:    "https://github.com/owner/repo/issues/7",
	}})
	if err != nil {
		t.Fatalf("Marshal issueSummary: %v", err)
	}
	runner := &mockRunner{
		outputs: map[string][]byte{
			"gh search issues --repo owner/repo --state open --json number,title,url --limit 100 --search [sota-gap]": searchOut,
			"gh issue create --repo owner/repo --title [sota-gap] Missing gap --body summary\n\nCurrent status: `not-implemented`\nPriority: `9`\n\n## Spec sections\n- docs/spec.md §1\n\n## Current code evidence\n- `cli/internal/example.go:10` — evidence --label enhancement --label ready-for-work": []byte("https://github.com/owner/repo/issues/9\n"),
		},
	}
	delta := &Delta{
		NewGaps: []CapabilityDelta{
			{Key: "missing", Name: "Missing gap", CurrentStatus: StatusNotImplemented, Priority: 9, Summary: "summary", SpecSections: []string{"docs/spec.md §1"}, CodeEvidence: []CodeEvidence{{Path: "cli/internal/example.go", LineStart: 10, Summary: "evidence"}}},
			{Key: "existing", Name: "Existing gap", CurrentStatus: StatusDormant, Priority: 5, Summary: "summary", SpecSections: []string{"docs/spec.md §1"}, CodeEvidence: []CodeEvidence{{Path: "cli/internal/example.go", LineStart: 10, Summary: "evidence"}}},
		},
	}

	result, err := FileIssues(context.Background(), runner, "owner/repo", delta, 3, "[sota-gap]", []string{"enhancement", "ready-for-work"})
	if err != nil {
		t.Fatalf("FileIssues() error = %v", err)
	}
	if len(result.Created) != 1 || result.Created[0].Number != 9 {
		t.Fatalf("Created = %#v, want one created issue #9", result.Created)
	}
	if len(result.Existing) != 1 || result.Existing[0].Number != 7 {
		t.Fatalf("Existing = %#v, want one deduped issue #7", result.Existing)
	}
}

func TestEnsureTrackingIssueCreatesWhenMissing(t *testing.T) {
	searchOut, err := json.Marshal([]issueSummary{})
	if err != nil {
		t.Fatalf("Marshal empty issueSummary: %v", err)
	}
	runner := &mockRunner{
		outputs: map[string][]byte{
			fmt.Sprintf("gh search issues --repo owner/repo --state open --json number,title,url --limit 100 --search %s", DefaultTrackingIssueTitle):                                                                                         []byte(string(searchOut)),
			fmt.Sprintf("gh issue create --repo owner/repo --title %s --body Weekly SoTA self-gap-analysis summaries land here so operators can track whether xylem is converging on the harness reference docs.", DefaultTrackingIssueTitle): []byte("https://github.com/owner/repo/issues/11\n"),
		},
	}
	item, err := EnsureTrackingIssue(context.Background(), runner, "owner/repo", DefaultTrackingIssueTitle)
	if err != nil {
		t.Fatalf("EnsureTrackingIssue() error = %v", err)
	}
	if !item.Created || item.Number != 11 {
		t.Fatalf("EnsureTrackingIssue() = %#v, want created issue #11", item)
	}
}

func TestEnsureTrackingDiscussionFindsExisting(t *testing.T) {
	resolveResp, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"id": "R_abc123",
				"discussionCategories": map[string]any{
					"nodes": []map[string]any{
						{"id": "DIC_reports", "name": "Reports"},
						{"id": "DIC_general", "name": "General"},
					},
				},
			},
		},
	})
	searchResp, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"node": map[string]any{
				"discussions": map[string]any{
					"nodes": []map[string]any{
						{"id": "D_existing", "title": "[sota-gap] Weekly tracking", "url": "https://github.com/owner/repo/discussions/5"},
					},
				},
			},
		},
	})

	runner := &sequentialMockRunner{
		outputs: [][]byte{resolveResp, searchResp},
	}

	ref, err := EnsureTrackingDiscussion(context.Background(), runner, "owner", "repo", "Reports", "[sota-gap] Weekly tracking")
	if err != nil {
		t.Fatalf("EnsureTrackingDiscussion() error = %v", err)
	}
	if ref.Created {
		t.Fatal("Expected Created=false for existing discussion")
	}
	if ref.NodeID != "D_existing" {
		t.Fatalf("NodeID = %q, want D_existing", ref.NodeID)
	}
	if ref.URL != "https://github.com/owner/repo/discussions/5" {
		t.Fatalf("URL = %q, want discussions/5", ref.URL)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("Expected 2 gh calls (resolve + search), got %d", len(runner.calls))
	}
}

func TestEnsureTrackingDiscussionCreatesWhenMissing(t *testing.T) {
	resolveResp, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"id": "R_abc123",
				"discussionCategories": map[string]any{
					"nodes": []map[string]any{
						{"id": "DIC_reports", "name": "Reports"},
					},
				},
			},
		},
	})
	searchResp, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"node": map[string]any{
				"discussions": map[string]any{
					"nodes": []map[string]any{},
				},
			},
		},
	})
	createResp, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"createDiscussion": map[string]any{
				"discussion": map[string]any{
					"id":    "D_new",
					"title": "[sota-gap] Weekly tracking",
					"url":   "https://github.com/owner/repo/discussions/10",
				},
			},
		},
	})

	runner := &sequentialMockRunner{
		outputs: [][]byte{resolveResp, searchResp, createResp},
	}

	ref, err := EnsureTrackingDiscussion(context.Background(), runner, "owner", "repo", "Reports", "[sota-gap] Weekly tracking")
	if err != nil {
		t.Fatalf("EnsureTrackingDiscussion() error = %v", err)
	}
	if !ref.Created {
		t.Fatal("Expected Created=true for new discussion")
	}
	if ref.NodeID != "D_new" {
		t.Fatalf("NodeID = %q, want D_new", ref.NodeID)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("Expected 3 gh calls (resolve + search + create), got %d", len(runner.calls))
	}
}

func TestEnsureTrackingDiscussionCategoryNotFound(t *testing.T) {
	resolveResp, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"id": "R_abc123",
				"discussionCategories": map[string]any{
					"nodes": []map[string]any{
						{"id": "DIC_general", "name": "General"},
					},
				},
			},
		},
	})

	runner := &sequentialMockRunner{
		outputs: [][]byte{resolveResp},
	}

	_, err := EnsureTrackingDiscussion(context.Background(), runner, "owner", "repo", "Reports", "[sota-gap] Weekly tracking")
	if err == nil {
		t.Fatal("Expected error for missing category")
	}
	if !strings.Contains(err.Error(), "discussion category \"Reports\" not found") {
		t.Fatalf("Error = %q, want category not found message", err.Error())
	}
}

func TestPostDiscussionSummary(t *testing.T) {
	commentResp, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"addDiscussionComment": map[string]any{
				"comment": map[string]any{
					"url": "https://github.com/owner/repo/discussions/5#discussioncomment-1",
				},
			},
		},
	})

	runner := &sequentialMockRunner{
		outputs: [][]byte{commentResp},
	}

	delta := &Delta{
		Current: SnapshotSummary{
			Counts: map[string]int{StatusWired: 10, StatusDormant: 5, StatusNotImplemented: 3},
		},
		Improvements: []CapabilityDelta{{Key: "a"}},
		NewGaps:      []CapabilityDelta{{Key: "b"}},
	}
	filed := &FileResult{
		Created: []FiledIssue{{Title: "New gap", Number: 42}},
	}

	err := PostDiscussionSummary(context.Background(), runner, "D_existing", delta, filed, "detailed report")
	if err != nil {
		t.Fatalf("PostDiscussionSummary() error = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("Expected 1 gh call, got %d", len(runner.calls))
	}

	// Verify the body contains expected content.
	callArgs := strings.Join(runner.calls[0], " ")
	if !strings.Contains(callArgs, "wired=10") {
		t.Fatalf("Expected body to contain wired=10, got: %s", callArgs)
	}
	if !strings.Contains(callArgs, "New gap (#42)") {
		t.Fatalf("Expected body to contain filed issue reference, got: %s", callArgs)
	}
}
