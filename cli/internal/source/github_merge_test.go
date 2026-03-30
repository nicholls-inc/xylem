package source

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func mergedPRJSON(prs []ghMergedPR) []byte {
	b, _ := json.Marshal(prs)
	return b
}

func TestGitHubMergeScanFindsMergedPRs(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghMergedPR{
		{Number: 10, Title: "merged fix", URL: "https://github.com/owner/repo/pull/10",
			MergeCommit: struct{ Oid string `json:"oid"` }{Oid: "abc123def456"}},
		{Number: 20, Title: "merged feature", URL: "https://github.com/owner/repo/pull/20",
			MergeCommit: struct{ Oid string `json:"oid"` }{Oid: "789abcdef012"}},
	}
	r.set(mergedPRJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit", "--limit", "20")

	src := &GitHubMerge{
		Repo: "owner/repo",
		Tasks: map[string]MergeTask{
			"post-merge": {Workflow: "post-merge-check"},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("expected 2 vessels, got %d", len(vessels))
	}
	for _, v := range vessels {
		if v.Source != "github-merge" {
			t.Errorf("expected source github-merge, got %q", v.Source)
		}
		if v.Meta["pr_num"] == "" {
			t.Error("expected Meta[pr_num] to be set")
		}
		if v.Meta["merge_commit"] == "" {
			t.Error("expected Meta[merge_commit] to be set")
		}
		if v.Workflow != "post-merge-check" {
			t.Errorf("expected workflow post-merge-check, got %q", v.Workflow)
		}
		if !strings.HasPrefix(v.ID, "merge-") {
			t.Errorf("expected ID to start with merge-, got %q", v.ID)
		}
	}
}

func TestGitHubMergeScanAlreadyProcessed(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")

	// Pre-enqueue a merge event and mark it completed
	ref := "https://github.com/owner/repo/pull/10#merge-abc123def456"
	_, _ = q.Enqueue(queue.Vessel{
		ID: "merge-10-abc123de", Source: "github-merge",
		Ref: ref, Workflow: "post-merge-check",
		State: queue.StatePending,
	})
	_, _ = q.Dequeue()
	_ = q.Update("merge-10-abc123de", queue.StateCompleted, "")

	r := newMock()
	prs := []ghMergedPR{
		{Number: 10, Title: "merged fix", URL: "https://github.com/owner/repo/pull/10",
			MergeCommit: struct{ Oid string `json:"oid"` }{Oid: "abc123def456"}},
	}
	r.set(mergedPRJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit", "--limit", "20")

	src := &GitHubMerge{
		Repo: "owner/repo",
		Tasks: map[string]MergeTask{
			"post-merge": {Workflow: "post-merge-check"},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels (already processed via HasRefAny), got %d", len(vessels))
	}
}

func TestGitHubMergeScanMultipleTasks(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghMergedPR{
		{Number: 10, Title: "merged fix", URL: "https://github.com/owner/repo/pull/10",
			MergeCommit: struct{ Oid string `json:"oid"` }{Oid: "abc123def456"}},
	}
	r.set(mergedPRJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit", "--limit", "20")

	src := &GitHubMerge{
		Repo: "owner/repo",
		Tasks: map[string]MergeTask{
			"deploy":     {Workflow: "deploy"},
			"post-merge": {Workflow: "post-merge-check"},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("expected 2 vessels (one per task), got %d", len(vessels))
	}
	workflows := make(map[string]bool)
	for _, v := range vessels {
		workflows[v.Workflow] = true
	}
	if !workflows["deploy"] || !workflows["post-merge-check"] {
		t.Errorf("expected both workflows, got: %v", workflows)
	}
}

func TestGitHubMergeVesselIDUsesCommitHash(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghMergedPR{
		{Number: 42, Title: "test", URL: "https://github.com/owner/repo/pull/42",
			MergeCommit: struct{ Oid string `json:"oid"` }{Oid: "deadbeefcafe1234"}},
	}
	r.set(mergedPRJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit", "--limit", "20")

	src := &GitHubMerge{
		Repo: "owner/repo",
		Tasks: map[string]MergeTask{
			"task": {Workflow: "wf"},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].ID != "merge-42-deadbeef" {
		t.Errorf("expected ID merge-42-deadbeef, got %q", vessels[0].ID)
	}
}

func TestGitHubMergeBranchName(t *testing.T) {
	src := &GitHubMerge{Repo: "owner/repo"}
	vessel := queue.Vessel{
		ID:   "merge-42-deadbeef",
		Ref:  "https://github.com/owner/repo/pull/42#merge-deadbeefcafe1234",
		Meta: map[string]string{"pr_num": "42", "merge_commit": "deadbeefcafe1234"},
	}
	branch := src.BranchName(vessel)
	if !strings.HasPrefix(branch, "merge/pr-42-") {
		t.Errorf("expected branch to start with merge/pr-42-, got %q", branch)
	}
}

func TestGitHubMergeOnStart(t *testing.T) {
	src := &GitHubMerge{Repo: "owner/repo"}
	vessel := queue.Vessel{
		ID:   "merge-10-abc",
		Meta: map[string]string{"pr_num": "10"},
	}
	err := src.OnStart(context.Background(), vessel)
	if err != nil {
		t.Fatalf("expected OnStart to be a no-op, got: %v", err)
	}
}

func TestGitHubMergeName(t *testing.T) {
	src := &GitHubMerge{}
	if src.Name() != "github-merge" {
		t.Errorf("expected name github-merge, got %q", src.Name())
	}
}

func TestGitHubMergeScanGHFailure(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	r.setErr(errTest, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit", "--limit", "20")

	src := &GitHubMerge{
		Repo: "owner/repo",
		Tasks: map[string]MergeTask{
			"post-merge": {Workflow: "post-merge-check"},
		},
		Queue:     q,
		CmdRunner: r,
	}

	_, err := src.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error from gh failure, got nil")
	}
}

func TestGitHubMergeScanMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	r.set([]byte(`{not valid`), "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit", "--limit", "20")

	src := &GitHubMerge{
		Repo: "owner/repo",
		Tasks: map[string]MergeTask{
			"post-merge": {Workflow: "post-merge-check"},
		},
		Queue:     q,
		CmdRunner: r,
	}

	_, err := src.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestShortMergeHash(t *testing.T) {
	if got := shortMergeHash("deadbeefcafe1234"); got != "deadbeef" {
		t.Errorf("expected deadbeef, got %q", got)
	}
	if got := shortMergeHash("short"); got != "short" {
		t.Errorf("expected short, got %q", got)
	}
	if got := shortMergeHash("12345678"); got != "12345678" {
		t.Errorf("expected 12345678, got %q", got)
	}
}
