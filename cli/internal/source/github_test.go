package source

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

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

	if len(r.calls) != 0 {
		t.Errorf("expected no calls when no labels configured, got %v", r.calls)
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
