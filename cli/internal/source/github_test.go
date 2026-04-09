package source

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func TestHasMergedPRTrue(t *testing.T) {
	r := newMock()

	prs := []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}{
		{Number: 42, HeadRefName: "fix/issue-7-some-slug"},
	}
	out, _ := json.Marshal(prs)
	r.set(out, "gh", "pr", "list",
		"--repo", "owner/repo",
		"--search", "head:fix/issue-7-",
		"--state", "merged",
		"--json", "number,headRefName",
		"--limit", "5")

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if !g.hasMergedPR(context.Background(), 7) {
		t.Error("expected hasMergedPR to return true when a merged PR exists")
	}
}

func TestHasMergedPRFalse(t *testing.T) {
	r := newMock()
	// Default mock returns "[]" for unregistered keys, so no merged PRs.

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if g.hasMergedPR(context.Background(), 99) {
		t.Error("expected hasMergedPR to return false when no merged PR exists")
	}
}

func TestHasMergedPRWrongBranch(t *testing.T) {
	r := newMock()

	// PR exists but branch prefix doesn't match issue number pattern
	prs := []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}{
		{Number: 50, HeadRefName: "unrelated/branch-name"},
	}
	out, _ := json.Marshal(prs)
	r.set(out, "gh", "pr", "list",
		"--repo", "owner/repo",
		"--search", "head:fix/issue-7-",
		"--state", "merged",
		"--json", "number,headRefName",
		"--limit", "5")

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if g.hasMergedPR(context.Background(), 7) {
		t.Error("expected hasMergedPR to return false when branch prefix doesn't match")
	}
}

func TestHasMergedPRGHError(t *testing.T) {
	r := newMock()
	// Simulate gh CLI error for all branch prefixes
	for _, prefix := range branchPrefixes {
		r.setErr(fmt.Errorf("network error"), "gh", "pr", "list",
			"--repo", "owner/repo",
			"--search", fmt.Sprintf("head:%s/issue-5-", prefix),
			"--state", "merged",
			"--json", "number,headRefName",
			"--limit", "5")
	}

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if g.hasMergedPR(context.Background(), 5) {
		t.Error("expected hasMergedPR to return false on gh error")
	}
}

func TestHasMergedPRFeatPrefix(t *testing.T) {
	r := newMock()

	// No fix/ match, but feat/ has a match
	prs := []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}{
		{Number: 99, HeadRefName: "feat/issue-3-add-feature"},
	}
	out, _ := json.Marshal(prs)
	r.set(out, "gh", "pr", "list",
		"--repo", "owner/repo",
		"--search", "head:feat/issue-3-",
		"--state", "merged",
		"--json", "number,headRefName",
		"--limit", "5")

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if !g.hasMergedPR(context.Background(), 3) {
		t.Error("expected hasMergedPR to return true for feat/ prefix match")
	}
}

func TestScanSkipsMergedPR(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{
			Number: 7,
			Title:  "test issue",
			Body:   "body",
			URL:    "https://github.com/owner/repo/issues/7",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "bug"}},
		},
	}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "search", "issues",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	// Set up merged PR match for fix/issue-7-
	prs := []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}{
		{Number: 42, HeadRefName: "fix/issue-7-test-issue"},
	}
	prOut, _ := json.Marshal(prs)
	r.set(prOut, "gh", "pr", "list",
		"--repo", "owner/repo",
		"--search", "head:fix/issue-7-",
		"--state", "merged",
		"--json", "number,headRefName",
		"--limit", "5")

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels (merged PR exists), got %d", len(vessels))
	}
}

func TestOnFailAppliesLabel(t *testing.T) {
	r := newMock()
	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":            "1",
			"status_label_failed":  "xylem-failed",
			"status_label_running": "in-progress",
		},
	}

	err := g.OnFail(context.Background(), vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label xylem-failed") {
		t.Errorf("expected --add-label xylem-failed in call, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label in-progress") {
		t.Errorf("expected --remove-label in-progress in call, got %q", joined)
	}
}

func TestOnFailNoLabelsConfigured(t *testing.T) {
	// When no status labels are configured, OnFail still removes the
	// backward-compat "in-progress" label that OnStart would have added.
	r := newMock()
	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
		Meta:   map[string]string{"issue_num": "1"},
	}

	err := g.OnFail(context.Background(), vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--remove-label in-progress") {
		t.Errorf("expected --remove-label in-progress, got %q", joined)
	}
}

func TestOnFailNilRunner(t *testing.T) {
	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: nil,
	}

	vessel := queue.Vessel{
		ID:   "issue-1",
		Meta: map[string]string{"issue_num": "1", "status_label_failed": "xylem-failed"},
	}

	err := g.OnFail(context.Background(), vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScanSkipsExcludedFailedLabel(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	// Issue has the xylem-failed label
	issues := []ghIssue{
		{
			Number: 10,
			Title:  "failed issue",
			Body:   "body",
			URL:    "https://github.com/owner/repo/issues/10",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "bug"}, {Name: "xylem-failed"}},
		},
	}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "search", "issues",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		Exclude:   []string{"xylem-failed"},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels (xylem-failed excluded), got %d", len(vessels))
	}
}

func TestScanPersistsTriggerLabelInMeta(t *testing.T) {
	// Property: after Scan produces a vessel for an issue, that vessel's
	// Meta["trigger_label"] equals one of the labels configured on the
	// task that matched the issue. OnComplete later uses this to remove
	// the trigger label from the source issue.
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{
			Number: 42,
			Title:  "add feature",
			Body:   "body",
			URL:    "https://github.com/owner/repo/issues/42",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "ready-for-work"}, {Name: "enhancement"}},
		},
	}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "search", "issues",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "ready-for-work")

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"features": {Labels: []string{"ready-for-work"}, Workflow: "implement-feature"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	got := vessels[0].Meta["trigger_label"]
	if got != "ready-for-work" {
		t.Errorf("expected Meta[trigger_label] = ready-for-work, got %q", got)
	}
}

func TestOnCompleteRemovesTriggerLabel(t *testing.T) {
	// Property: after a vessel completes successfully, its trigger
	// label is removed from the source issue via a separate gh call.
	// This prevents duplicate-enqueue risk after PR lifecycle events
	// and keeps the issue's UI state consistent with workflow state.
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:     "issue-156",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":              "156",
			"status_label_completed": "xylem-completed",
			"status_label_running":   "in-progress",
			"trigger_label":          "ready-for-work",
		},
	}
	if err := g.OnComplete(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected 2 gh calls (status transition + trigger-label removal), got %d: %v", len(r.calls), r.calls)
	}
	status := strings.Join(r.calls[0], " ")
	if !strings.Contains(status, "--add-label xylem-completed") {
		t.Errorf("call 0: expected --add-label xylem-completed, got %q", status)
	}
	if !strings.Contains(status, "--remove-label in-progress") {
		t.Errorf("call 0: expected --remove-label in-progress, got %q", status)
	}
	trig := strings.Join(r.calls[1], " ")
	if !strings.Contains(trig, "--remove-label ready-for-work") {
		t.Errorf("call 1: expected --remove-label ready-for-work, got %q", trig)
	}
	if strings.Contains(trig, "--add-label") {
		t.Errorf("call 1: trigger-label removal must not add anything, got %q", trig)
	}
}

func TestOnCompleteBackwardCompatNoTriggerLabel(t *testing.T) {
	// Backward-compat: a vessel enqueued before trigger_label was
	// introduced has no such key. OnComplete must not crash, must not
	// emit a second gh call, and must continue to perform the
	// status-label transition exactly as before.
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:     "issue-100",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":              "100",
			"status_label_completed": "done",
			"status_label_running":   "wip",
		},
	}
	if err := g.OnComplete(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 gh call (no trigger label present), got %d: %v", len(r.calls), r.calls)
	}
}

func TestOnCompleteRemovesTriggerLabelEvenWhenNoStatusLabels(t *testing.T) {
	// When status labels are not configured, OnComplete still emits a
	// gh call to remove the "in-progress" backward-compat running
	// label. The trigger_label removal must still fire in that case
	// as an independent second call.
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:     "issue-117",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":     "117",
			"trigger_label": "needs-refinement",
		},
	}
	if err := g.OnComplete(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(r.calls), r.calls)
	}
	trig := strings.Join(r.calls[1], " ")
	if !strings.Contains(trig, "--remove-label needs-refinement") {
		t.Errorf("expected --remove-label needs-refinement in second call, got %q", trig)
	}
}

func TestGitHubTaskFromConfigCopiesLabelGateLabels(t *testing.T) {
	task := GitHubTaskFromConfig(config.Task{
		Labels:   []string{"bug"},
		Workflow: "fix-bug",
		LabelGateLabels: &config.LabelGateLabels{
			Waiting: "blocked",
			Ready:   "ready-for-implementation",
		},
	})

	if task.LabelGateLabels == nil {
		t.Fatal("LabelGateLabels should not be nil when config block is present")
	}
	if task.LabelGateLabels.Waiting != "blocked" {
		t.Errorf("LabelGateLabels.Waiting = %q, want blocked", task.LabelGateLabels.Waiting)
	}
	if task.LabelGateLabels.Ready != "ready-for-implementation" {
		t.Errorf("LabelGateLabels.Ready = %q, want ready-for-implementation", task.LabelGateLabels.Ready)
	}
}

func TestGitHubOnWaitAppliesWaitingLabel(t *testing.T) {
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID: "issue-1",
		Meta: map[string]string{
			"issue_num":                "1",
			"label_gate_label_waiting": "blocked",
			"label_gate_label_ready":   "ready-for-implementation",
		},
	}

	if err := g.OnWait(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label blocked") {
		t.Errorf("expected --add-label blocked, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label ready-for-implementation") {
		t.Errorf("expected --remove-label ready-for-implementation, got %q", joined)
	}
}

func TestGitHubOnResumeAppliesReadyLabel(t *testing.T) {
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID: "issue-1",
		Meta: map[string]string{
			"issue_num":                "1",
			"label_gate_label_waiting": "blocked",
			"label_gate_label_ready":   "ready-for-implementation",
		},
	}

	if err := g.OnResume(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label ready-for-implementation") {
		t.Errorf("expected --add-label ready-for-implementation, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label blocked") {
		t.Errorf("expected --remove-label blocked, got %q", joined)
	}
}

func TestGitHubOnTimedOutRemovesWaitingLabel(t *testing.T) {
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID: "issue-1",
		Meta: map[string]string{
			"issue_num":                "1",
			"status_label_timed_out":   "timed-out",
			"label_gate_label_waiting": "blocked",
		},
	}

	if err := g.OnTimedOut(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label timed-out") {
		t.Errorf("expected --add-label timed-out, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label blocked") {
		t.Errorf("expected --remove-label blocked, got %q", joined)
	}
}
