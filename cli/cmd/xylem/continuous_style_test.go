package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/continuousstyle"
)

type continuousStyleRunnerStub struct {
	outputs map[string][]byte
	calls   [][]string
}

func (r *continuousStyleRunnerStub) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	key := strings.Join(call, "\x00")
	out, ok := r.outputs[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return out, nil
}

func TestContinuousStyleFileIssuesCommandWritesResult(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	outputPath := filepath.Join(dir, "filed.json")
	writeContinuousStyleReport(t, reportPath, continuousstyle.Report{
		Version:       continuousstyle.ReportVersion,
		Repo:          "owner/repo",
		GeneratedAt:   "2026-04-10T00:00:00Z",
		TargetSurface: "cli terminal output",
		Findings: []continuousstyle.Finding{{
			ID:             "route-stderr",
			Title:          "Route stderr consistently",
			Category:       "stderr-routing",
			Summary:        "summary",
			Recommendation: "recommendation",
			Priority:       9,
			Paths:          []string{"cli/cmd/xylem/main.go"},
			Evidence: []continuousstyle.Evidence{{
				Path:      "cli/cmd/xylem/main.go",
				LineStart: 30,
				Summary:   "evidence",
			}},
		}},
	})
	runner := &continuousStyleRunnerStub{
		outputs: map[string][]byte{
			strings.Join([]string{"gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url", "--limit", "100", "--search", "[continuous-style]"}, "\x00"): []byte("[]"),
			strings.Join([]string{"gh", "issue", "create", "--repo", "owner/repo", "--title", "[continuous-style] Route stderr consistently", "--body", "summary\n\nCategory: `stderr-routing`\nPriority: `9`\nTarget surface: `cli terminal output`\n\n## Affected paths\n- `cli/cmd/xylem/main.go`\n\n## Current code evidence\n- `cli/cmd/xylem/main.go:30` — evidence\n\n## Suggested direction\nrecommendation", "--label", "enhancement", "--label", "ready-for-work"}, "\x00"): []byte("https://github.com/owner/repo/issues/9\n"),
		},
	}

	original := newContinuousStyleRunner
	newContinuousStyleRunner = func() continuousstyle.CommandRunner { return runner }
	t.Cleanup(func() { newContinuousStyleRunner = original })

	cmd := newContinuousStyleCmd()
	cmd.SetArgs([]string{"file-issues", "--report", reportPath, "--output", outputPath, "--repo", "owner/repo"})

	output := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if strings.TrimSpace(output) != "Wrote "+outputPath {
		t.Fatalf("stdout = %q, want %q", output, "Wrote "+outputPath)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", outputPath, err)
	}
	var result continuousstyle.FileResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(result.Created) != 1 || result.Created[0].Number != 9 {
		t.Fatalf("Created = %#v, want one created issue #9", result.Created)
	}
}

func TestContinuousStylePostSummaryCommandPostsTrackingComment(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	filedPath := filepath.Join(dir, "filed.json")
	summaryPath := filepath.Join(dir, "summary.md")
	writeContinuousStyleReport(t, reportPath, continuousstyle.Report{
		Version:       continuousstyle.ReportVersion,
		Repo:          "owner/repo",
		GeneratedAt:   "2026-04-10T00:00:00Z",
		TargetSurface: "cli terminal output",
		Findings: []continuousstyle.Finding{{
			ID:             "route-stderr",
			Title:          "Route stderr consistently",
			Category:       "stderr-routing",
			Summary:        "summary",
			Recommendation: "recommendation",
			Priority:       9,
			Paths:          []string{"cli/cmd/xylem/main.go"},
			Evidence: []continuousstyle.Evidence{{
				Path:      "cli/cmd/xylem/main.go",
				LineStart: 30,
				Summary:   "evidence",
			}},
		}},
	})
	writeContinuousStyleFileResult(t, filedPath, continuousstyle.FileResult{
		Created: []continuousstyle.FiledIssue{{
			ID:      "route-stderr",
			Title:   "[continuous-style] Route stderr consistently",
			Number:  9,
			URL:     "https://github.com/owner/repo/issues/9",
			Created: true,
		}},
	})
	if err := os.WriteFile(summaryPath, []byte("# Continuous style report\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", summaryPath, err)
	}
	runner := &continuousStyleRunnerStub{
		outputs: map[string][]byte{
			strings.Join([]string{"gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url", "--limit", "100", "--search", continuousstyle.DefaultTrackingIssueTitle}, "\x00"):                                                                                                                                                                                                                                                                 []byte(`[]`),
			strings.Join([]string{"gh", "issue", "create", "--repo", "owner/repo", "--title", continuousstyle.DefaultTrackingIssueTitle, "--body", "Scheduled continuous-style summaries land here so operators can track terminal-output polish opportunities across xylem's CLI surfaces."}, "\x00"):                                                                                                                                                                                 []byte("https://github.com/owner/repo/issues/11\n"),
			strings.Join([]string{"gh", "issue", "comment", "11", "--repo", "owner/repo", "--body", "**xylem — continuous style analysis**\n\n- Target surface: cli terminal output\n- Findings this run: 1\n- Highest-priority findings:\n  - Route stderr consistently (`stderr-routing`, priority=9)\n- Filed issues:\n  - [continuous-style] Route stderr consistently (#9)\n\n<details>\n<summary>Analysis report</summary>\n\n# Continuous style report\n\n</details>"}, "\x00"): []byte(""),
		},
	}

	original := newContinuousStyleRunner
	newContinuousStyleRunner = func() continuousstyle.CommandRunner { return runner }
	t.Cleanup(func() { newContinuousStyleRunner = original })

	cmd := newContinuousStyleCmd()
	cmd.SetArgs([]string{"post-summary", "--report", reportPath, "--filed", filedPath, "--summary", summaryPath, "--repo", "owner/repo"})

	output := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if strings.TrimSpace(output) != "Posted summary to issue #11" {
		t.Fatalf("stdout = %q, want tracking issue confirmation", output)
	}

	wantCalls := [][]string{
		{"gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url", "--limit", "100", "--search", continuousstyle.DefaultTrackingIssueTitle},
		{"gh", "issue", "create", "--repo", "owner/repo", "--title", continuousstyle.DefaultTrackingIssueTitle, "--body", "Scheduled continuous-style summaries land here so operators can track terminal-output polish opportunities across xylem's CLI surfaces."},
		{"gh", "issue", "comment", "11", "--repo", "owner/repo", "--body", "**xylem — continuous style analysis**\n\n- Target surface: cli terminal output\n- Findings this run: 1\n- Highest-priority findings:\n  - Route stderr consistently (`stderr-routing`, priority=9)\n- Filed issues:\n  - [continuous-style] Route stderr consistently (#9)\n\n<details>\n<summary>Analysis report</summary>\n\n# Continuous style report\n\n</details>"},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("runner calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func writeContinuousStyleReport(t *testing.T, path string, report continuousstyle.Report) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func writeContinuousStyleFileResult(t *testing.T, path string, result continuousstyle.FileResult) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
