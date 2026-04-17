package source

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
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
			Number:      20,
			Title:       "merged PR",
			URL:         "https://github.com/owner/repo/pull/20",
			MergeCommit: ghMergeCommit{OID: "abcdef1234567890"},
			HeadRefName: "feature-x",
		},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")

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

func TestMergeScanControlPlaneCallback(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{
			Number:      20,
			Title:       "merged PR",
			URL:         "https://github.com/owner/repo/pull/20",
			MergeCommit: ghMergeCommit{OID: "abcdef1234567890"},
			HeadRefName: "feature-x",
		},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")
	r.set([]byte(`{"files":[{"path":".xylem/workflows/fix-bug.yaml"},{"path":"README.md"}]}`),
		"gh", "pr", "view", "20", "--repo", "owner/repo", "--json", "files")

	var got ControlPlaneMergeEvent
	g := &GitHubMerge{
		Repo:  "owner/repo",
		Tasks: map[string]MergeTask{"deploy": {Workflow: "post-merge"}},
		Queue: q, CmdRunner: r,
		OnControlPlaneMerge: func(event ControlPlaneMergeEvent) {
			got = event
		},
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	want := ControlPlaneMergeEvent{
		PRNumber:       20,
		MergeCommitSHA: "abcdef1234567890",
		Files:          []string{".xylem/workflows/fix-bug.yaml", "README.md"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("callback event = %#v, want %#v", got, want)
	}
}

func TestMergeScanControlPlaneCallbackIgnoresNonControlPlaneChanges(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{
			Number:      20,
			Title:       "merged PR",
			URL:         "https://github.com/owner/repo/pull/20",
			MergeCommit: ghMergeCommit{OID: "abcdef1234567890"},
			HeadRefName: "feature-x",
		},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")
	r.set([]byte(`{"files":[{"path":"README.md"},{"path":"docs/guide.md"}]}`),
		"gh", "pr", "view", "20", "--repo", "owner/repo", "--json", "files")

	called := false
	g := &GitHubMerge{
		Repo:  "owner/repo",
		Tasks: map[string]MergeTask{"deploy": {Workflow: "post-merge"}},
		Queue: q, CmdRunner: r,
		OnControlPlaneMerge: func(ControlPlaneMergeEvent) {
			called = true
		},
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if called {
		t.Fatal("expected non-control-plane merge to skip control-plane callback")
	}
}

func TestMergeScanControlPlaneCallbackSkipsDuplicates(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{
			Number:      20,
			Title:       "merged PR",
			URL:         "https://github.com/owner/repo/pull/20",
			MergeCommit: ghMergeCommit{OID: "abcdef1234567890"},
			HeadRefName: "feature-x",
		},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")

	_, _ = q.Enqueue(queue.Vessel{
		ID:     "merge-pr-20-abcdef12",
		Source: "github-merge",
		Ref:    "https://github.com/owner/repo/pull/20#merge-abcdef1234567890",
		State:  queue.StatePending,
	})

	called := false
	g := &GitHubMerge{
		Repo:  "owner/repo",
		Tasks: map[string]MergeTask{"deploy": {Workflow: "post-merge"}},
		Queue: q, CmdRunner: r,
		OnControlPlaneMerge: func(ControlPlaneMergeEvent) {
			called = true
		},
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels, got %d", len(vessels))
	}
	if called {
		t.Fatal("expected duplicate merge ref to skip control-plane callback")
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
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")

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
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")

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
	vessel := queue.Vessel{
		ID:     "merge-pr-20-abcdef12",
		Source: "github-merge",
		Ref:    "https://github.com/owner/repo/pull/20#merge-abcdef1234567890",
		Meta:   map[string]string{"pr_num": "20"},
	}

	tests := []struct {
		name string
		call func(context.Context, queue.Vessel) error
	}{
		{name: "enqueue", call: g.OnEnqueue},
		{name: "start", call: g.OnStart},
		{name: "wait", call: g.OnWait},
		{name: "resume", call: g.OnResume},
		{name: "complete", call: g.OnComplete},
		{name: "fail", call: g.OnFail},
		{name: "timed_out", call: g.OnTimedOut},
		{name: "remove_running_label", call: g.RemoveRunningLabel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(context.Background(), vessel); err != nil {
				t.Fatalf("%s should be a no-op, got: %v", tt.name, err)
			}
		})
	}
}

func TestMergeScanGHError(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	r.setErr(fmt.Errorf("network error"), "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")

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
	if !strings.Contains(err.Error(), "gh pr list (merged): network error") {
		t.Fatalf("Scan() error = %q, want wrapped gh list failure", err)
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
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")

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

func TestMergeScanExclude(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{
			Number:      40,
			Title:       "autorelease PR",
			URL:         "https://github.com/owner/repo/pull/40",
			MergeCommit: ghMergeCommit{OID: "aabbccdd11223344"},
			HeadRefName: "release-branch",
			Labels:      []ghMergedPRLabel{{Name: "autorelease: tagged"}},
		},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")

	g := &GitHubMerge{
		Repo:    "owner/repo",
		Exclude: []string{"autorelease: tagged"},
		Tasks:   map[string]MergeTask{"unblock": {Workflow: "unblock-wave"}},
		Queue:   q, CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (excluded label), got %d", len(vessels))
	}
}

func TestMergeScanExcludeNotApplied(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghMergedPR{
		{
			Number:      41,
			Title:       "normal PR",
			URL:         "https://github.com/owner/repo/pull/41",
			MergeCommit: ghMergeCommit{OID: "1122334455667788"},
			HeadRefName: "fix-branch",
			Labels:      []ghMergedPRLabel{{Name: "bug"}},
		},
	}
	prBytes, _ := json.Marshal(prs)
	r.set(prBytes, "gh", "pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")

	g := &GitHubMerge{
		Repo:    "owner/repo",
		Exclude: []string{"autorelease: tagged"},
		Tasks:   map[string]MergeTask{"unblock": {Workflow: "unblock-wave"}},
		Queue:   q, CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
}
