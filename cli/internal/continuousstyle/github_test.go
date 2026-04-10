package continuousstyle

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
		Title:  "[continuous-style] Existing finding",
		URL:    "https://github.com/owner/repo/issues/7",
	}})
	if err != nil {
		t.Fatalf("Marshal issueSummary: %v", err)
	}
	runner := &mockRunner{
		outputs: map[string][]byte{
			"gh search issues --repo owner/repo --state open --json number,title,url --limit 100 --search [continuous-style]": searchOut,
			"gh issue create --repo owner/repo --title [continuous-style] Route stderr consistently --body summary\n\nCategory: `stderr-routing`\nPriority: `9`\nTarget surface: `cli terminal output`\n\n## Affected paths\n- `cli/cmd/xylem/main.go`\n\n## Current code evidence\n- `cli/cmd/xylem/main.go:30` — evidence\n\n## Suggested direction\nrecommendation --label enhancement --label ready-for-work": []byte("https://github.com/owner/repo/issues/9\n"),
		},
	}
	report := &Report{
		Version:       ReportVersion,
		Repo:          "owner/repo",
		GeneratedAt:   "2026-04-10T00:00:00Z",
		TargetSurface: "cli terminal output",
		Findings: []Finding{
			{
				ID:             "route-stderr",
				Title:          "Route stderr consistently",
				Category:       "stderr-routing",
				Summary:        "summary",
				Recommendation: "recommendation",
				Priority:       9,
				Paths:          []string{"cli/cmd/xylem/main.go"},
				Evidence: []Evidence{{
					Path:      "cli/cmd/xylem/main.go",
					LineStart: 30,
					Summary:   "evidence",
				}},
			},
			{
				ID:             "existing-finding",
				Title:          "Existing finding",
				Category:       "consistency",
				Summary:        "summary",
				Recommendation: "recommendation",
				Priority:       6,
				Paths:          []string{"cli/cmd/xylem/status.go"},
				Evidence: []Evidence{{
					Path:      "cli/cmd/xylem/status.go",
					LineStart: 10,
					Summary:   "evidence",
				}},
			},
		},
	}

	result, err := FileIssues(context.Background(), runner, "owner/repo", report, 3, "[continuous-style]", []string{"enhancement", "ready-for-work"})
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
			fmt.Sprintf("gh search issues --repo owner/repo --state open --json number,title,url --limit 100 --search %s", DefaultTrackingIssueTitle):                                                                                             []byte(string(searchOut)),
			fmt.Sprintf("gh issue create --repo owner/repo --title %s --body Scheduled continuous-style summaries land here so operators can track terminal-output polish opportunities across xylem's CLI surfaces.", DefaultTrackingIssueTitle): []byte("https://github.com/owner/repo/issues/11\n"),
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

func TestFileIssuesDoesNotCountExistingAgainstCreateLimit(t *testing.T) {
	searchOut, err := json.Marshal([]issueSummary{
		{
			Number: 7,
			Title:  "[continuous-style] Existing highest priority",
			URL:    "https://github.com/owner/repo/issues/7",
		},
		{
			Number: 8,
			Title:  "[continuous-style] Existing second priority",
			URL:    "https://github.com/owner/repo/issues/8",
		},
	})
	if err != nil {
		t.Fatalf("Marshal issueSummary: %v", err)
	}
	runner := &mockRunner{
		outputs: map[string][]byte{
			"gh search issues --repo owner/repo --state open --json number,title,url --limit 100 --search [continuous-style]": searchOut,
			"gh issue create --repo owner/repo --title [continuous-style] New third priority --body summary\n\nCategory: `consistency`\nPriority: `7`\nTarget surface: `cli terminal output`\n\n## Affected paths\n- `cli/cmd/xylem/status.go`\n\n## Current code evidence\n- `cli/cmd/xylem/status.go:10` — evidence\n\n## Suggested direction\nrecommendation": []byte("https://github.com/owner/repo/issues/9\n"),
		},
	}
	report := &Report{
		Version:       ReportVersion,
		Repo:          "owner/repo",
		GeneratedAt:   "2026-04-10T00:00:00Z",
		TargetSurface: "cli terminal output",
		Findings: []Finding{
			{
				ID:             "existing-highest-priority",
				Title:          "Existing highest priority",
				Category:       "consistency",
				Summary:        "summary",
				Recommendation: "recommendation",
				Priority:       9,
				Paths:          []string{"cli/cmd/xylem/status.go"},
				Evidence: []Evidence{{
					Path:      "cli/cmd/xylem/status.go",
					LineStart: 10,
					Summary:   "evidence",
				}},
			},
			{
				ID:             "existing-second-priority",
				Title:          "Existing second priority",
				Category:       "consistency",
				Summary:        "summary",
				Recommendation: "recommendation",
				Priority:       8,
				Paths:          []string{"cli/cmd/xylem/status.go"},
				Evidence: []Evidence{{
					Path:      "cli/cmd/xylem/status.go",
					LineStart: 10,
					Summary:   "evidence",
				}},
			},
			{
				ID:             "new-third-priority",
				Title:          "New third priority",
				Category:       "consistency",
				Summary:        "summary",
				Recommendation: "recommendation",
				Priority:       7,
				Paths:          []string{"cli/cmd/xylem/status.go"},
				Evidence: []Evidence{{
					Path:      "cli/cmd/xylem/status.go",
					LineStart: 10,
					Summary:   "evidence",
				}},
			},
		},
	}

	result, err := FileIssues(context.Background(), runner, "owner/repo", report, 1, "[continuous-style]", nil)
	if err != nil {
		t.Fatalf("FileIssues() error = %v", err)
	}
	if len(result.Existing) != 2 {
		t.Fatalf("len(Existing) = %d, want 2", len(result.Existing))
	}
	if len(result.Created) != 1 || result.Created[0].Number != 9 {
		t.Fatalf("Created = %#v, want one created issue #9", result.Created)
	}
}
