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
