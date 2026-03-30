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

func TestMergeName(t *testing.T) {
	g := &GitHubMerge{}
	if g.Name() != "github-merge" {
		t.Fatalf("Name() = %q, want github-merge", g.Name())
	}
}

func TestMergeScan(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{
			Number:         20,
			Title:          "merged PR",
			URL:            "https://github.com/owner/repo/pull/20",
			MergeCommit: ghMergeCommit{OID: "abcdef1234567890"},
			HeadRefName:    "feature-x",
		},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName", "--limit", "20")

	g := &GitHubMerge{
		Repo: "owner/repo",
		Tasks: map[string]MergeTask{
			"deploy": {Workflow: "post-merge"},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	v := vessels[0]
	if v.Source != "github-merge" {
		t.Errorf("Source = %q, want github-merge", v.Source)
	}
	if !strings.Contains(v.Ref, "#merge-abcdef1234567890") {
		t.Errorf("Ref = %q, want to contain #merge-abcdef1234567890", v.Ref)
	}
	if v.Meta["pr_num"] != "20" {
		t.Errorf("Meta[pr_num] = %q, want 20", v.Meta["pr_num"])
	}
	if v.Meta["event_type"] != "merge" {
		t.Errorf("Meta[event_type] = %q, want merge", v.Meta["event_type"])
	}
	if v.Meta["pr_head_branch"] != "feature-x" {
		t.Errorf("Meta[pr_head_branch] = %q, want feature-x", v.Meta["pr_head_branch"])
	}
	if v.Workflow != "post-merge" {
		t.Errorf("Workflow = %q, want post-merge", v.Workflow)
	}
}

func TestMergeScanDedup(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{Number: 20, Title: "merged PR", URL: "https://github.com/owner/repo/pull/20", MergeCommit: ghMergeCommit{OID: "abcdef1234567890"}, HeadRefName: "feature-x"},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName", "--limit", "20")

	// Pre-enqueue the same merge ref
	_, _ = q.Enqueue(queue.Vessel{
		ID:     "merge-pr-20-abcdef12",
		Source: "github-merge",
		Ref:    "https://github.com/owner/repo/pull/20#merge-abcdef1234567890",
		State:  queue.StatePending,
	})

	g := &GitHubMerge{
		Repo:  "owner/repo",
		Tasks: map[string]MergeTask{"deploy": {Workflow: "post-merge"}},
		Queue: q, CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (already queued), got %d", len(vessels))
	}
}

func TestMergeScanDedupCompleted(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{Number: 20, Title: "merged PR", URL: "https://github.com/owner/repo/pull/20", MergeCommit: ghMergeCommit{OID: "abcdef1234567890"}, HeadRefName: "feature-x"},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName", "--limit", "20")

	// Pre-enqueue and complete the same merge ref
	_, _ = q.Enqueue(queue.Vessel{
		ID:     "merge-pr-20-abcdef12",
		Source: "github-merge",
		Ref:    "https://github.com/owner/repo/pull/20#merge-abcdef1234567890",
		State:  queue.StatePending,
	})
	_, _ = q.Dequeue()
	_ = q.Update("merge-pr-20-abcdef12", queue.StateCompleted, "")

	g := &GitHubMerge{
		Repo:  "owner/repo",
		Tasks: map[string]MergeTask{"deploy": {Workflow: "post-merge"}},
		Queue: q, CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (completed ref blocks re-processing), got %d", len(vessels))
	}
}

func TestMergeBranchName(t *testing.T) {
	g := &GitHubMerge{}
	vessel := queue.Vessel{
		Ref: "https://github.com/owner/repo/pull/20#merge-abcdef1234567890",
		Meta: map[string]string{
			"pr_num": "20",
		},
	}
	got := g.BranchName(vessel)
	if !strings.HasPrefix(got, "merge/pr-20-") {
		t.Errorf("BranchName = %q, want prefix merge/pr-20-", got)
	}
}

func TestMergeOnStartNoOp(t *testing.T) {
	g := &GitHubMerge{}
	if err := g.OnStart(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnStart should be no-op, got: %v", err)
	}
}

func TestMergeOnEnqueueNoOp(t *testing.T) {
	g := &GitHubMerge{}
	if err := g.OnEnqueue(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnEnqueue should be no-op, got: %v", err)
	}
}

func TestMergeOnCompleteNoOp(t *testing.T) {
	g := &GitHubMerge{}
	if err := g.OnComplete(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnComplete should be no-op, got: %v", err)
	}
}

func TestMergeOnFailNoOp(t *testing.T) {
	g := &GitHubMerge{}
	if err := g.OnFail(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnFail should be no-op, got: %v", err)
	}
}

func TestMergeOnTimedOutNoOp(t *testing.T) {
	g := &GitHubMerge{}
	if err := g.OnTimedOut(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnTimedOut should be no-op, got: %v", err)
	}
}

func TestMergeScanGHError(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	r.setErr(fmt.Errorf("network error"), "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName", "--limit", "20")

	g := &GitHubMerge{
		Repo:      "owner/repo",
		Tasks:     map[string]MergeTask{"deploy": {Workflow: "post-merge"}},
		Queue:     q,
		CmdRunner: r,
	}

	_, err := g.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error from gh failure, got nil")
	}
}

func TestMergeScanEmptyOIDSkipped(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{Number: 30, Title: "no OID", URL: "https://github.com/owner/repo/pull/30", MergeCommit: ghMergeCommit{OID: ""}, HeadRefName: "branch-30"},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName", "--limit", "20")

	g := &GitHubMerge{
		Repo:      "owner/repo",
		Tasks:     map[string]MergeTask{"deploy": {Workflow: "post-merge"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (empty OID), got %d", len(vessels))
	}
}
