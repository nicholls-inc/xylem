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

func prEventsListJSON(prs []ghPR) []byte {
	b, _ := json.Marshal(prs)
	return b
}

func TestPREventsName(t *testing.T) {
	g := &GitHubPREvents{}
	if g.Name() != "github-pr-events" {
		t.Fatalf("Name() = %q, want github-pr-events", g.Name())
	}
}

func TestPREventsScanLabels(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{
			Number:      10,
			Title:       "test PR",
			URL:         "https://github.com/owner/repo/pull/10",
			HeadRefName: "feature-branch",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "needs-review"}},
		},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow: "handle-review",
				Labels:   []string{"needs-review"},
			},
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
	if v.Source != "github-pr-events" {
		t.Errorf("Source = %q, want github-pr-events", v.Source)
	}
	if !strings.Contains(v.Ref, "#label-needs-review") {
		t.Errorf("Ref = %q, want to contain #label-needs-review", v.Ref)
	}
	if v.Meta["pr_num"] != "10" {
		t.Errorf("Meta[pr_num] = %q, want 10", v.Meta["pr_num"])
	}
	if v.Meta["event_type"] != "label" {
		t.Errorf("Meta[event_type] = %q, want label", v.Meta["event_type"])
	}
	if v.Meta["pr_head_branch"] != "feature-branch" {
		t.Errorf("Meta[pr_head_branch] = %q, want feature-branch", v.Meta["pr_head_branch"])
	}
}

func TestPREventsScanReviewSubmitted(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 5, Title: "PR 5", URL: "https://github.com/owner/repo/pull/5", HeadRefName: "branch-5"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set([]byte("1001\n1002\n"), "gh", "api", "repos/owner/repo/pulls/5/reviews", "--jq", ".[].id")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"reviews": {
				Workflow:        "handle-review",
				ReviewSubmitted: true,
			},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("expected 2 vessels (one per review), got %d", len(vessels))
	}
	for _, v := range vessels {
		if v.Meta["event_type"] != "review_submitted" {
			t.Errorf("Meta[event_type] = %q, want review_submitted", v.Meta["event_type"])
		}
	}
}

func TestPREventsScanChecksFailed(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 7, Title: "PR 7", URL: "https://github.com/owner/repo/pull/7", HeadRefName: "branch-7"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	checks := []ghCheck{
		{Name: "lint", State: "SUCCESS"},
		{Name: "test", State: "FAILURE"},
	}
	checksJSON, _ := json.Marshal(checks)
	r.set(checksJSON, "gh", "pr", "checks", "7", "--repo", "owner/repo", "--json", "name,state")
	r.set([]byte("abc12345def"), "gh", "pr", "view", "7", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"fix-checks": {
				Workflow:     "fix-ci",
				ChecksFailed: true,
			},
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
	if v.Meta["event_type"] != "checks_failed" {
		t.Errorf("Meta[event_type] = %q, want checks_failed", v.Meta["event_type"])
	}
	if !strings.Contains(v.Ref, "#checks-failed-abc12345def") {
		t.Errorf("Ref = %q, want to contain #checks-failed-abc12345def", v.Ref)
	}
}

func TestPREventsScanChecksNoFailure(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 7, Title: "PR 7", URL: "https://github.com/owner/repo/pull/7", HeadRefName: "branch-7"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	checks := []ghCheck{
		{Name: "lint", State: "SUCCESS"},
		{Name: "test", State: "SUCCESS"},
	}
	checksJSON, _ := json.Marshal(checks)
	r.set(checksJSON, "gh", "pr", "checks", "7", "--repo", "owner/repo", "--json", "name,state")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"fix-checks": {
				Workflow:     "fix-ci",
				ChecksFailed: true,
			},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (no failures), got %d", len(vessels))
	}
}

func TestPREventsScanCommented(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 3, Title: "PR 3", URL: "https://github.com/owner/repo/pull/3", HeadRefName: "branch-3"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set([]byte("5001\n5002\n5003\n"), "gh", "api", "repos/owner/repo/issues/3/comments", "--jq", ".[].id")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"comments": {
				Workflow:  "respond-comment",
				Commented: true,
			},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 3 {
		t.Fatalf("expected 3 vessels (one per comment), got %d", len(vessels))
	}
	for _, v := range vessels {
		if v.Meta["event_type"] != "commented" {
			t.Errorf("Meta[event_type] = %q, want commented", v.Meta["event_type"])
		}
	}
}

func TestPREventsScanExcluded(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{
			Number: 1, Title: "excluded PR",
			URL:         "https://github.com/owner/repo/pull/1",
			HeadRefName: "branch-1",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "needs-review"}, {Name: "wontfix"}},
		},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	g := &GitHubPREvents{
		Repo:    "owner/repo",
		Exclude: []string{"wontfix"},
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow: "handle-review",
				Labels:   []string{"needs-review"},
			},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (excluded), got %d", len(vessels))
	}
}

func TestPREventsScanDedup(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{
			Number: 10, Title: "test PR",
			URL:         "https://github.com/owner/repo/pull/10",
			HeadRefName: "feature-branch",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "needs-review"}},
		},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	// Pre-enqueue a vessel with matching ref
	_, _ = q.Enqueue(queue.Vessel{
		ID:     "pr-10-label-needs-review",
		Source: "github-pr-events",
		Ref:    "https://github.com/owner/repo/pull/10#label-needs-review",
		State:  queue.StatePending,
	})

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow: "handle-review",
				Labels:   []string{"needs-review"},
			},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (already queued), got %d", len(vessels))
	}
}

func TestPREventsScanDedupCompletedRef(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{
			Number: 10, Title: "test PR",
			URL:         "https://github.com/owner/repo/pull/10",
			HeadRefName: "feature-branch",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "needs-review"}},
		},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	// Pre-enqueue and complete a vessel with matching ref
	_, _ = q.Enqueue(queue.Vessel{
		ID:     "pr-10-label-needs-review",
		Source: "github-pr-events",
		Ref:    "https://github.com/owner/repo/pull/10#label-needs-review",
		State:  queue.StatePending,
	})
	_, _ = q.Dequeue()
	_ = q.Update("pr-10-label-needs-review", queue.StateCompleted, "")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow: "handle-review",
				Labels:   []string{"needs-review"},
			},
		},
		Queue:     q,
		CmdRunner: r,
	}

	// HasRefAny should still find completed vessels, so no new vessel is created
	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (completed ref blocks re-processing), got %d", len(vessels))
	}
}

func TestPREventsBranchName(t *testing.T) {
	g := &GitHubPREvents{}
	vessel := queue.Vessel{
		Ref: "https://github.com/owner/repo/pull/10#label-needs-review",
		Meta: map[string]string{
			"pr_num":     "10",
			"event_type": "label",
		},
	}
	got := g.BranchName(vessel)
	if !strings.HasPrefix(got, "event/pr-10-label-") {
		t.Errorf("BranchName = %q, want prefix event/pr-10-label-", got)
	}
}

func TestPREventsOnStartNoOp(t *testing.T) {
	g := &GitHubPREvents{}
	err := g.OnStart(context.Background(), queue.Vessel{})
	if err != nil {
		t.Fatalf("OnStart should be no-op, got: %v", err)
	}
}

func TestPREventsOnEnqueueNoOp(t *testing.T) {
	g := &GitHubPREvents{}
	if err := g.OnEnqueue(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnEnqueue should be no-op, got: %v", err)
	}
}

func TestPREventsOnCompleteNoOp(t *testing.T) {
	g := &GitHubPREvents{}
	if err := g.OnComplete(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnComplete should be no-op, got: %v", err)
	}
}

func TestPREventsOnFailNoOp(t *testing.T) {
	g := &GitHubPREvents{}
	if err := g.OnFail(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnFail should be no-op, got: %v", err)
	}
}

func TestPREventsOnTimedOutNoOp(t *testing.T) {
	g := &GitHubPREvents{}
	if err := g.OnTimedOut(context.Background(), queue.Vessel{}); err != nil {
		t.Fatalf("OnTimedOut should be no-op, got: %v", err)
	}
}

func TestPREventsScanGHError(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	r.setErr(fmt.Errorf("network error"), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {Workflow: "handle-review", Labels: []string{"needs-review"}},
		},
		Queue:     q,
		CmdRunner: r,
	}

	_, err := g.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error from gh failure, got nil")
	}
}
