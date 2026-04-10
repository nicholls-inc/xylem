package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type harnessGapTestRunner struct {
	calls       [][]string
	responses   map[string][]byte
	createCount int
	createBody  string
}

func (r *harnessGapTestRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if name == "gh" && len(args) >= 2 && args[0] == "issue" && args[1] == "create" {
		r.createCount++
		r.createBody = valueAfterFlag(args, "--body")
		return []byte("https://github.com/owner/repo/issues/91"), nil
	}
	if out, ok := r.responses[strings.Join(call, "\x00")]; ok {
		return out, nil
	}
	return []byte("[]"), nil
}

func TestGenerateHarnessGapAnalysisDetectsObservedSignals(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 1, 0, 0, 0, time.UTC)
	writeDaemonLogFixture(t, stateDir, []string{
		daemonLogLine(now.Add(-23*time.Hour), "daemon reconcile recovered orphaned vessels", "recovered=1"),
		daemonLogLine(now.Add(-22*time.Hour), "daemon tick summary", "pending=3", "running=0", "completed=0", "failed=0"),
		daemonLogLine(now.Add(-21*time.Hour-40*time.Minute), "daemon tick summary", "pending=4", "running=0", "completed=0", "failed=0"),
		daemonLogLine(now.Add(-21*time.Hour-20*time.Minute), "daemon tick summary", "pending=0", "running=1", "completed=0", "failed=0"),
	})

	runner := &harnessGapTestRunner{
		responses: map[string][]byte{
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "merged", "--limit", "100", "--json", "number,title,url,headRefName,mergedAt,mergedBy,labels"): mustJSON(t, []map[string]any{
				{"number": 1, "title": "feat", "url": "https://example/pr/1", "headRefName": "feat/issue-201-201", "mergedAt": now.Add(-2 * time.Hour), "mergedBy": map[string]any{"login": "alice"}, "labels": []map[string]any{{"name": "harness-impl"}}},
				{"number": 2, "title": "fix", "url": "https://example/pr/2", "headRefName": "fix/issue-202-202", "mergedAt": now.Add(-3 * time.Hour), "mergedBy": map[string]any{"login": "bob"}, "labels": []map[string]any{{"name": "harness-impl"}}},
				{"number": 3, "title": "chore", "url": "https://example/pr/3", "headRefName": "chore/issue-203-203", "mergedAt": now.Add(-4 * time.Hour), "mergedBy": map[string]any{"login": "carol"}, "labels": []map[string]any{{"name": "harness-impl"}}},
			}),
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "open", "--label", "needs-conflict-resolution", "--limit", "100", "--json", "number,title,url,mergeable,headRefName"): mustJSON(t, []map[string]any{
				{"number": 11, "title": "conflict", "url": "https://example/pr/11", "mergeable": "MERGEABLE", "headRefName": "feat/issue-211-211"},
			}),
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt"): mustJSON(t, []map[string]any{
				{"number": 21, "title": "Release Please", "url": "https://example/pr/21", "headRefName": "release-please--branches--main", "createdAt": now.Add(-10 * 24 * time.Hour)},
			}),
			commandKey("gh", "--repo", "owner/repo", "pr", "view", "21", "--json", "commits"): mustJSON(t, map[string]any{
				"commits": []map[string]any{{"oid": "a"}, {"oid": "b"}, {"oid": "c"}, {"oid": "d"}, {"oid": "e"}, {"oid": "f"}},
			}),
			commandKey("git", "rev-list", "--left-right", "--count", "origin/main...HEAD"): []byte("2\t1\n"),
			commandKey("gh", "--repo", "owner/repo", "issue", "list", "--state", "open", "--label", "xylem-failed", "--limit", "100", "--json", "number,title,url,labels"): mustJSON(t, []map[string]any{
				{"number": 31, "title": "failed one", "url": "https://example/issue/31", "labels": []map[string]any{{"name": "xylem-failed"}}},
				{"number": 32, "title": "failed two", "url": "https://example/issue/32", "labels": []map[string]any{{"name": "xylem-failed"}}},
				{"number": 33, "title": "failed three", "url": "https://example/issue/33", "labels": []map[string]any{{"name": "xylem-failed"}}},
			}),
		},
	}

	result, err := GenerateHarnessGapAnalysis(context.Background(), stateDir, "owner/repo", runner, HarnessGapOptions{
		OutputDir: "reviews",
		Now:       now,
	})
	if err != nil {
		t.Fatalf("GenerateHarnessGapAnalysis() error = %v", err)
	}

	if len(result.Report.Findings) != 7 {
		t.Fatalf("findings = %d, want 7", len(result.Report.Findings))
	}
	gotCategories := make(map[string]bool, len(result.Report.Findings))
	for _, finding := range result.Report.Findings {
		gotCategories[finding.Category] = true
		if finding.Fingerprint == "" {
			t.Fatalf("finding %#v has empty fingerprint", finding)
		}
	}
	for _, category := range []string{
		"config-drift",
		"failed-fingerprint-backlog",
		"idle-with-backlog",
		"merge-click-frequency",
		"release-cadence",
		"stale-label-patterns",
		"daemon-restart-count",
	} {
		if !gotCategories[category] {
			t.Fatalf("missing category %q in %+v", category, result.Report.Findings)
		}
	}

	if _, err := os.Stat(filepath.Join(stateDir, "reviews", harnessGapReportJSONName)); err != nil {
		t.Fatalf("report json missing: %v", err)
	}
	if !strings.Contains(result.Markdown, "Harness gap analysis") {
		t.Fatalf("markdown = %q, want heading", result.Markdown)
	}
}

func TestGenerateHarnessGapAnalysisNoSignalsWritesNoopReport(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 1, 0, 0, 0, time.UTC)
	runner := &harnessGapTestRunner{
		responses: map[string][]byte{
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "merged", "--limit", "100", "--json", "number,title,url,headRefName,mergedAt,mergedBy,labels"):                        []byte("[]"),
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "open", "--label", "needs-conflict-resolution", "--limit", "100", "--json", "number,title,url,mergeable,headRefName"): []byte("[]"),
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt"):                                         []byte("[]"),
			commandKey("git", "rev-list", "--left-right", "--count", "origin/main...HEAD"):                                                                                                          []byte("0 0\n"),
			commandKey("gh", "--repo", "owner/repo", "issue", "list", "--state", "open", "--label", "xylem-failed", "--limit", "100", "--json", "number,title,url,labels"):                          []byte("[]"),
		},
	}

	result, err := GenerateHarnessGapAnalysis(context.Background(), stateDir, "owner/repo", runner, HarnessGapOptions{
		OutputDir: "reviews",
		Now:       now,
	})
	if err != nil {
		t.Fatalf("GenerateHarnessGapAnalysis() error = %v", err)
	}
	if len(result.Report.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", result.Report.Findings)
	}
	if !strings.Contains(result.Markdown, "No recurring autonomy gaps exceeded") {
		t.Fatalf("markdown = %q, want no-op summary", result.Markdown)
	}
	if len(result.Report.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want daemon-log warning", result.Report.Warnings)
	}
}

func TestDetectFailedFingerprintBacklogGapIgnoresRecordedRecoveryPolicy(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 1, 0, 0, 0, time.UTC)
	issueURL := "https://example/issue/31"
	q := queue.New(filepath.Join(stateDir, "queue.jsonl"))

	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-31",
		Source:   "github-issue",
		Ref:      issueURL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "31",
			"source_input_fingerprint": "src-fp",
		},
		State:     queue.StatePending,
		CreatedAt: now.Add(-15 * time.Minute),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-31", queue.StateFailed, "temporary failure from upstream 503"))

	artifact := recovery.Build(recovery.Input{
		VesselID:  "issue-31",
		Source:    "github-issue",
		Workflow:  "fix-bug",
		Ref:       issueURL,
		State:     queue.StateFailed,
		Error:     "temporary failure from upstream 503",
		CreatedAt: now.Add(-10 * time.Minute),
		Meta: map[string]string{
			"source_input_fingerprint": "src-fp",
		},
	})
	require.NoError(t, recovery.Save(stateDir, artifact))

	runner := &harnessGapTestRunner{
		responses: map[string][]byte{
			commandKey("gh", "--repo", "owner/repo", "issue", "list", "--state", "open", "--label", "xylem-failed", "--limit", "100", "--json", "number,title,url,labels"): mustJSON(t, []map[string]any{
				{"number": 31, "title": "failed one", "url": issueURL, "labels": []map[string]any{{"name": "xylem-failed"}}},
			}),
		},
	}

	finding, err := detectFailedFingerprintBacklogGap(context.Background(), stateDir, "owner/repo", runner)
	require.NoError(t, err)
	assert.Nil(t, finding)
}

func TestSmoke_S1_HarnessGapAnalysisFilesAutoAdminMergeIssue(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 1, 0, 0, 0, time.UTC)
	runner := &harnessGapTestRunner{
		responses: map[string][]byte{
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "merged", "--limit", "100", "--json", "number,title,url,headRefName,mergedAt,mergedBy,labels"): mustJSON(t, []map[string]any{
				{"number": 1, "title": "feat", "url": "https://example/pr/1", "headRefName": "feat/issue-201-201", "mergedAt": now.Add(-2 * time.Hour), "mergedBy": map[string]any{"login": "alice"}, "labels": []map[string]any{{"name": "harness-impl"}}},
				{"number": 2, "title": "fix", "url": "https://example/pr/2", "headRefName": "fix/issue-202-202", "mergedAt": now.Add(-3 * time.Hour), "mergedBy": map[string]any{"login": "bob"}, "labels": []map[string]any{{"name": "harness-impl"}}},
				{"number": 3, "title": "chore", "url": "https://example/pr/3", "headRefName": "chore/issue-203-203", "mergedAt": now.Add(-4 * time.Hour), "mergedBy": map[string]any{"login": "carol"}, "labels": []map[string]any{{"name": "harness-impl"}}},
			}),
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "open", "--label", "needs-conflict-resolution", "--limit", "100", "--json", "number,title,url,mergeable,headRefName"): []byte("[]"),
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt"):                                         []byte("[]"),
			commandKey("git", "rev-list", "--left-right", "--count", "origin/main...HEAD"):                                                                                                          []byte("0\t0\n"),
			commandKey("gh", "--repo", "owner/repo", "issue", "list", "--state", "open", "--label", "xylem-failed", "--limit", "100", "--json", "number,title,url,labels"):                          []byte("[]"),
			commandKey("gh", "issue", "list", "--repo", "owner/repo", "--state", "open", "--limit", "100", "--json", "number,title,body"):                                                           []byte("[]"),
		},
	}

	result, err := RunHarnessGapAnalysis(context.Background(), stateDir, "owner/repo", runner, HarnessGapOptions{
		OutputDir: "reviews",
		Now:       now,
	})
	require.NoError(t, err)

	require.Len(t, result.Report.Findings, 1)
	finding := result.Report.Findings[0]
	assert.Equal(t, "merge-click-frequency", finding.Category)
	assert.Equal(t, "harness-gap-analysis: automate repeated human admin merges", finding.Title)
	assert.Equal(t, harnessGapMergedPRThreshold, finding.Observed)
	assert.Len(t, finding.Evidence, harnessGapMergedPRThreshold)

	require.Len(t, result.Published, 1)
	assert.True(t, result.Published[0].Created)
	assert.Equal(t, 91, result.Published[0].IssueNumber)
	assert.Equal(t, finding.Title, result.Published[0].Title)
	assert.Equal(t, 1, runner.createCount)
	assert.Contains(t, runner.createBody, "## Harness gap finding")
	assert.Contains(t, runner.createBody, harnessGapFindingMarkerPrefix+finding.Fingerprint)
	assert.Contains(t, runner.createBody, "#1 merged by @alice")

	_, err = os.Stat(filepath.Join(stateDir, "reviews", harnessGapReportJSONName))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(stateDir, "reviews", harnessGapReportMarkdownName))
	require.NoError(t, err)
}

func TestSmoke_S2_HarnessGapAnalysisNoopsWhenNoGapsDetected(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 1, 0, 0, 0, time.UTC)
	runner := &harnessGapTestRunner{
		responses: map[string][]byte{
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "merged", "--limit", "100", "--json", "number,title,url,headRefName,mergedAt,mergedBy,labels"):                        []byte("[]"),
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "open", "--label", "needs-conflict-resolution", "--limit", "100", "--json", "number,title,url,mergeable,headRefName"): []byte("[]"),
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt"):                                         []byte("[]"),
			commandKey("git", "rev-list", "--left-right", "--count", "origin/main...HEAD"):                                                                                                          []byte("0\t0\n"),
			commandKey("gh", "--repo", "owner/repo", "issue", "list", "--state", "open", "--label", "xylem-failed", "--limit", "100", "--json", "number,title,url,labels"):                          []byte("[]"),
		},
	}

	result, err := RunHarnessGapAnalysis(context.Background(), stateDir, "owner/repo", runner, HarnessGapOptions{
		OutputDir: "reviews",
		Now:       now,
	})
	require.NoError(t, err)

	assert.Empty(t, result.Report.Findings)
	assert.Empty(t, result.Published)
	assert.Equal(t, 0, runner.createCount)
	assert.Contains(t, result.Markdown, "No recurring autonomy gaps exceeded the built-in thresholds.")
	assert.Equal(t, []string{"daemon log not available; skipped local restart/backlog signals"}, result.Report.Warnings)

	reportJSON, err := os.ReadFile(filepath.Join(stateDir, "reviews", harnessGapReportJSONName))
	require.NoError(t, err)
	assert.Contains(t, string(reportJSON), "\"warnings\"")
	assert.Contains(t, string(reportJSON), "daemon log not available")
}

func TestDetectAdminMergeGapIgnoresNonHarnessPullRequests(t *testing.T) {
	now := time.Date(2026, time.April, 10, 1, 0, 0, 0, time.UTC)
	runner := &harnessGapTestRunner{
		responses: map[string][]byte{
			commandKey("gh", "--repo", "owner/repo", "pr", "list", "--state", "merged", "--limit", "100", "--json", "number,title,url,headRefName,mergedAt,mergedBy,labels"): mustJSON(t, []map[string]any{
				{"number": 1, "title": "bug fix", "url": "https://example/pr/1", "headRefName": "fix/issue-201-201", "mergedAt": now.Add(-2 * time.Hour), "mergedBy": map[string]any{"login": "alice"}, "labels": []map[string]any{{"name": "bug"}}},
				{"number": 2, "title": "feature", "url": "https://example/pr/2", "headRefName": "feat/issue-202-202", "mergedAt": now.Add(-3 * time.Hour), "mergedBy": map[string]any{"login": "bob"}, "labels": []map[string]any{{"name": "enhancement"}}},
				{"number": 3, "title": "docs", "url": "https://example/pr/3", "headRefName": "chore/issue-203-203", "mergedAt": now.Add(-4 * time.Hour), "mergedBy": map[string]any{"login": "carol"}, "labels": []map[string]any{{"name": "ready-to-merge"}}},
			}),
		},
	}

	finding, err := detectAdminMergeGap(context.Background(), "owner/repo", runner, now)
	if err != nil {
		t.Fatalf("detectAdminMergeGap() error = %v", err)
	}
	if finding != nil {
		t.Fatalf("detectAdminMergeGap() = %+v, want nil", finding)
	}
}

func TestPublishHarnessGapIssuesDedupsRepeatedFindings(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 1, 0, 0, 0, time.UTC)
	report := &HarnessGapReport{
		GeneratedAt: now,
		Findings: []HarnessGapFinding{
			newHarnessGapFinding(
				"config-drift",
				"harness-gap-analysis: daemon worktree drifted from origin/main",
				"drift detected",
				2,
				1,
				[]string{"behind=1 ahead=1"},
				[]string{"sync daemon worktree"},
			),
		},
	}
	runner := &harnessGapTestRunner{
		responses: map[string][]byte{
			commandKey("gh", "issue", "list", "--repo", "owner/repo", "--state", "open", "--limit", "100", "--json", "number,title,body"): []byte("[]"),
		},
	}

	first, err := PublishHarnessGapIssues(context.Background(), stateDir, "owner/repo", runner, report, "reviews", now)
	if err != nil {
		t.Fatalf("first PublishHarnessGapIssues() error = %v", err)
	}
	if runner.createCount != 1 {
		t.Fatalf("createCount after first publish = %d, want 1", runner.createCount)
	}
	if len(first) != 1 || !first[0].Created {
		t.Fatalf("first publish = %+v, want created issue", first)
	}

	second, err := PublishHarnessGapIssues(context.Background(), stateDir, "owner/repo", runner, report, "reviews", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second PublishHarnessGapIssues() error = %v", err)
	}
	if runner.createCount != 1 {
		t.Fatalf("createCount after second publish = %d, want 1", runner.createCount)
	}
	if len(second) != 1 || second[0].Created {
		t.Fatalf("second publish = %+v, want deduped existing issue", second)
	}
	if !strings.Contains(runner.createBody, harnessGapFindingMarkerPrefix+report.Findings[0].Fingerprint) {
		t.Fatalf("create body missing fingerprint marker: %q", runner.createBody)
	}
}

func writeDaemonLogFixture(t *testing.T, stateDir string, lines []string) {
	t.Helper()
	path := filepath.Join(stateDir, harnessGapDaemonLogFileName)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func daemonLogLine(ts time.Time, msg string, extra ...string) string {
	fields := []string{
		"time=" + ts.UTC().Format(time.RFC3339),
		"level=INFO",
		`msg="` + msg + `"`,
	}
	fields = append(fields, extra...)
	return strings.Join(fields, " ")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func valueAfterFlag(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
