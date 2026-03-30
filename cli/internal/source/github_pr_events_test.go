package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func TestGitHubPREventsScanLabelTrigger(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "needs review", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug",
			Labels: []struct{ Name string `json:"name"` }{{Name: "needs-response"}}},
		{Number: 20, Title: "no label", URL: "https://github.com/owner/repo/pull/20", HeadRefName: "add-feature",
			Labels: []struct{ Name string `json:"name"` }{{Name: "other"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"respond": {Workflow: "respond-to-pr", Labels: []string{"needs-response"}},
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
	v := vessels[0]
	if v.Source != "github-pr-events" {
		t.Errorf("expected source github-pr-events, got %q", v.Source)
	}
	if v.Meta["event_type"] != "label" {
		t.Errorf("expected event_type label, got %q", v.Meta["event_type"])
	}
	if v.Meta["pr_num"] != "10" {
		t.Errorf("expected pr_num 10, got %q", v.Meta["pr_num"])
	}
	if !strings.Contains(v.Ref, "#label-needs-response") {
		t.Errorf("expected ref to contain #label-needs-response, got %q", v.Ref)
	}
	if v.Workflow != "respond-to-pr" {
		t.Errorf("expected workflow respond-to-pr, got %q", v.Workflow)
	}
}

func TestGitHubPREventsScanReviewTrigger(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "has reviews", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")
	r.set([]byte("111\n222\n"), "gh", "api", "repos/owner/repo/pulls/10/reviews", "--jq", ".[].id")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {Workflow: "handle-review", ReviewSubmitted: true},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("expected 2 vessels (one per review), got %d", len(vessels))
	}
	for _, v := range vessels {
		if v.Meta["event_type"] != "review" {
			t.Errorf("expected event_type review, got %q", v.Meta["event_type"])
		}
	}
}

func TestGitHubPREventsScanChecksFailed(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "failing checks", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")

	checks := []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}{
		{Name: "lint", State: "SUCCESS"},
		{Name: "test", State: "FAILURE"},
	}
	checksJSON, _ := json.Marshal(checks)
	r.set(checksJSON, "gh", "pr", "checks", "10", "--repo", "owner/repo", "--json", "name,state")
	r.set([]byte(`{"headRefOid":"abc123def456"}`), "gh", "pr", "view", "10", "--repo", "owner/repo", "--json", "headRefOid")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"fix-checks": {Workflow: "fix-checks", ChecksFailed: true},
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
	v := vessels[0]
	if v.Meta["event_type"] != "checks_failed" {
		t.Errorf("expected event_type checks_failed, got %q", v.Meta["event_type"])
	}
	if !strings.Contains(v.Ref, "#checks-failed-abc123def456") {
		t.Errorf("expected ref to contain #checks-failed-abc123def456, got %q", v.Ref)
	}
}

func TestGitHubPREventsScanChecksAllPassing(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "passing checks", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")

	checks := []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}{
		{Name: "lint", State: "SUCCESS"},
		{Name: "test", State: "SUCCESS"},
	}
	checksJSON, _ := json.Marshal(checks)
	r.set(checksJSON, "gh", "pr", "checks", "10", "--repo", "owner/repo", "--json", "name,state")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"fix-checks": {Workflow: "fix-checks", ChecksFailed: true},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (checks passing), got %d", len(vessels))
	}
}

func TestGitHubPREventsScanCommentTrigger(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "has comments", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")
	r.set([]byte("501\n502\n"), "gh", "api", "repos/owner/repo/issues/10/comments", "--jq", ".[].id")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"respond": {Workflow: "respond-to-comment", Commented: true},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("expected 2 vessels (one per comment), got %d", len(vessels))
	}
	for _, v := range vessels {
		if v.Meta["event_type"] != "comment" {
			t.Errorf("expected event_type comment, got %q", v.Meta["event_type"])
		}
	}
}

func TestGitHubPREventsScanExcludedLabel(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "excluded", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug",
			Labels: []struct{ Name string `json:"name"` }{{Name: "needs-response"}, {Name: "no-bot"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"respond": {Workflow: "respond-to-pr", Labels: []string{"needs-response"}},
		},
		Exclude:   []string{"no-bot"},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels (excluded), got %d", len(vessels))
	}
}

func TestGitHubPREventsScanAlreadyProcessed(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")

	// Pre-enqueue a label event and mark it completed
	ref := "https://github.com/owner/repo/pull/10#label-needs-response"
	_, _ = q.Enqueue(queue.Vessel{
		ID: "pr-10-label-abc", Source: "github-pr-events",
		Ref: ref, Workflow: "respond-to-pr",
		State: queue.StatePending,
	})
	// Transition to completed
	_, _ = q.Dequeue()
	_ = q.Update("pr-10-label-abc", queue.StateCompleted, "")

	r := newMock()
	prs := []ghPR{
		{Number: 10, Title: "needs review", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug",
			Labels: []struct{ Name string `json:"name"` }{{Name: "needs-response"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"respond": {Workflow: "respond-to-pr", Labels: []string{"needs-response"}},
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

func TestGitHubPREventsVesselID(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 42, Title: "test", URL: "https://github.com/owner/repo/pull/42", HeadRefName: "fix",
			Labels: []struct{ Name string `json:"name"` }{{Name: "trigger"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"task": {Workflow: "wf", Labels: []string{"trigger"}},
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
	if !strings.HasPrefix(vessels[0].ID, "pr-42-label-") {
		t.Errorf("expected ID to start with pr-42-label-, got %q", vessels[0].ID)
	}
}

func TestGitHubPREventsBranchName(t *testing.T) {
	src := &GitHubPREvents{Repo: "owner/repo"}
	vessel := queue.Vessel{
		ID:   "pr-42-label-abc",
		Ref:  "https://github.com/owner/repo/pull/42#label-needs-response",
		Meta: map[string]string{"pr_num": "42", "event_type": "label"},
	}
	branch := src.BranchName(vessel)
	if !strings.HasPrefix(branch, "pr-event/42-label-") {
		t.Errorf("expected branch to start with pr-event/42-label-, got %q", branch)
	}
}

func TestGitHubPREventsOnStart(t *testing.T) {
	src := &GitHubPREvents{Repo: "owner/repo"}
	vessel := queue.Vessel{
		ID:   "pr-10-label-abc",
		Meta: map[string]string{"pr_num": "10"},
	}
	err := src.OnStart(context.Background(), vessel)
	if err != nil {
		t.Fatalf("expected OnStart to be a no-op, got: %v", err)
	}
}

func TestGitHubPREventsName(t *testing.T) {
	src := &GitHubPREvents{}
	if src.Name() != "github-pr-events" {
		t.Errorf("expected name github-pr-events, got %q", src.Name())
	}
}

func TestGitHubPREventsScanGHFailure(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	r.setErr(errTest, "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"respond": {Workflow: "respond-to-pr", Labels: []string{"trigger"}},
		},
		Queue:     q,
		CmdRunner: r,
	}

	_, err := src.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error from gh failure, got nil")
	}
}

func TestShortHash(t *testing.T) {
	// shortHash should be deterministic
	h1 := shortHash("test-input")
	h2 := shortHash("test-input")
	if h1 != h2 {
		t.Fatalf("shortHash is not deterministic: %q != %q", h1, h2)
	}
	if len(h1) != 8 {
		t.Fatalf("expected 8 char hash, got %d: %q", len(h1), h1)
	}

	// Different inputs should produce different hashes
	h3 := shortHash("different-input")
	if h1 == h3 {
		t.Fatal("expected different inputs to produce different hashes")
	}
}

func TestPrHasLabel(t *testing.T) {
	pr := ghPR{
		Labels: []struct{ Name string `json:"name"` }{
			{Name: "bug"}, {Name: "review-me"},
		},
	}
	if !prHasLabel(pr, "bug") {
		t.Error("expected prHasLabel to find 'bug'")
	}
	if prHasLabel(pr, "missing") {
		t.Error("expected prHasLabel to not find 'missing'")
	}
}

func TestGitHubPREventsScanReviewAPIError(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "test", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix"},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "20")
	r.setErr(fmt.Errorf("api error"), "gh", "api", "repos/owner/repo/pulls/10/reviews", "--jq", ".[].id")

	src := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {Workflow: "handle-review", ReviewSubmitted: true},
		},
		Queue:     q,
		CmdRunner: r,
	}

	// Should not error — review API errors are swallowed per PR
	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels when review API fails, got %d", len(vessels))
	}
}
