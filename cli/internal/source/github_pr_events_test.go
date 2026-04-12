package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prEventsListJSON(prs []ghPR) []byte {
	b, _ := json.Marshal(prs)
	return b
}

// authoredEventsLines formats a slice of {id, login, type} triples into
// the JSON-lines shape that `gh api ... --jq '.[] | {id, login, type}'`
// emits. Used by tests that exercise scanReviews / scanComments with the
// new author-aware API.
func authoredEventsLines(events ...struct {
	ID    int64
	Login string
	Type  string
}) []byte {
	var lines []string
	for _, ev := range events {
		b, _ := json.Marshal(map[string]any{
			"id":    ev.ID,
			"login": ev.Login,
			"type":  ev.Type,
		})
		lines = append(lines, string(b))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

const (
	reviewsJQ  = `.[] | {id: .id, login: .user.login, type: .user.type}`
	commentsJQ = `.[] | {id: .id, login: .user.login, type: .user.type}`
)

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
	r.set(authoredEventsLines(
		struct {
			ID    int64
			Login string
			Type  string
		}{1001, "copilot-pull-request-reviewer[bot]", "Bot"},
		struct {
			ID    int64
			Login string
			Type  string
		}{1002, "copilot-pull-request-reviewer[bot]", "Bot"},
	), "gh", "api", "repos/owner/repo/pulls/5/reviews", "--jq", reviewsJQ)

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"reviews": {
				Workflow:        "handle-review",
				ReviewSubmitted: true,
				Debounce:        0,
				AuthorAllow:     []string{"copilot-pull-request-reviewer[bot]"},
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
		if v.Meta["review_author"] != "copilot-pull-request-reviewer[bot]" {
			t.Errorf("Meta[review_author] = %q, want copilot-pull-request-reviewer[bot]", v.Meta["review_author"])
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
	r.set(authoredEventsLines(
		struct {
			ID    int64
			Login string
			Type  string
		}{5001, "alice", "User"},
		struct {
			ID    int64
			Login string
			Type  string
		}{5002, "alice", "User"},
		struct {
			ID    int64
			Login string
			Type  string
		}{5003, "alice", "User"},
	), "gh", "api", "repos/owner/repo/issues/3/comments", "--jq", commentsJQ)

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"comments": {
				Workflow:    "respond-comment",
				Commented:   true,
				Debounce:    0,
				AuthorAllow: []string{"alice"},
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
		if v.Meta["comment_author"] != "alice" {
			t.Errorf("Meta[comment_author] = %q, want alice", v.Meta["comment_author"])
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

func TestPREventsScanPROpenedDedupByPR(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 11, Title: "PR 11", URL: "https://github.com/owner/repo/pull/11", HeadRefName: "branch-11"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow: "review-pr",
				PROpened: true,
			},
		},
		StateDir:  dir,
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
	if vessels[0].Meta["event_type"] != "pr_opened" {
		t.Fatalf("Meta[event_type] = %q, want pr_opened", vessels[0].Meta["event_type"])
	}
	if !strings.Contains(vessels[0].Ref, "#pr-opened") {
		t.Fatalf("Ref = %q, want #pr-opened suffix", vessels[0].Ref)
	}
	if _, err := q.Enqueue(vessels[0]); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := g.OnEnqueue(context.Background(), vessels[0]); err != nil {
		t.Fatalf("OnEnqueue: %v", err)
	}

	vessels, err = g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan again: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels after dedup, got %d", len(vessels))
	}
}

func TestPROpenedDedupSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(dir, "queue.jsonl")

	prs := []ghPR{
		{Number: 11, Title: "PR 11", URL: "https://github.com/owner/repo/pull/11", HeadRefName: "branch-11"},
	}
	listCall := []string{"gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50"}

	// --- First "daemon boot" ---
	q1 := queue.New(queuePath)
	r1 := newMock()
	r1.set(prEventsListJSON(prs), listCall...)

	g1 := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {Workflow: "review-pr", PROpened: true},
		},
		StateDir:  dir,
		Queue:     q1,
		CmdRunner: r1,
	}

	vessels, err := g1.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	_, err = q1.Enqueue(vessels[0])
	require.NoError(t, err)
	require.NoError(t, g1.OnEnqueue(context.Background(), vessels[0]))

	// --- Simulate restart: new queue instance, same backing file ---
	q2 := queue.New(queuePath)
	r2 := newMock()
	r2.set(prEventsListJSON(prs), listCall...)

	g2 := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {Workflow: "review-pr", PROpened: true},
		},
		StateDir:  dir,
		Queue:     q2,
		CmdRunner: r2,
	}

	vessels2, err := g2.Scan(context.Background())
	require.NoError(t, err)
	assert.Empty(t, vessels2, "queue-backed dedup must survive daemon restart")
}

func TestPREventsScanPRHeadUpdatedDedupsPerHead(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 12, Title: "PR 12", URL: "https://github.com/owner/repo/pull/12", HeadRefName: "branch-12"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set([]byte("abc12345def"), "gh", "pr", "view", "12", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow:      "review-pr",
				PRHeadUpdated: true,
				Debounce:      0,
			},
		},
		StateDir:  dir,
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
	if !strings.Contains(vessels[0].Ref, "#head-abc12345def") {
		t.Fatalf("Ref = %q, want #head-abc12345def", vessels[0].Ref)
	}
	if _, err := q.Enqueue(vessels[0]); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := g.OnEnqueue(context.Background(), vessels[0]); err != nil {
		t.Fatalf("OnEnqueue: %v", err)
	}

	vessels, err = g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan same head: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels for repeated head, got %d", len(vessels))
	}

	r.set([]byte("fedcba987654"), "gh", "pr", "view", "12", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")
	vessels, err = g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan new head: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel for new head, got %d", len(vessels))
	}
	if !strings.Contains(vessels[0].Ref, "#head-fedcba987654") {
		t.Fatalf("Ref = %q, want #head-fedcba987654", vessels[0].Ref)
	}
}

func TestPREventDefaultDebounceMatchesSpec(t *testing.T) {
	cases := []struct {
		trigger string
		want    time.Duration
	}{
		{trigger: preventPROpened, want: 0},
		{trigger: preventPRHeadUpdated, want: 10 * time.Minute},
		{trigger: preventReviewSubmitted, want: 0},
		{trigger: preventChecksFailed, want: 0},
		{trigger: preventCommented, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.trigger, func(t *testing.T) {
			got := effectivePREventDebounce(PREventsTask{Debounce: UnsetPREventsDebounce}, tc.trigger)
			if got != tc.want {
				t.Fatalf("effectivePREventDebounce(%q) = %v, want %v", tc.trigger, got, tc.want)
			}
		})
	}
}

func TestPREventsScanPRHeadUpdatedDebouncesAcrossHeads(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 13, Title: "PR 13", URL: "https://github.com/owner/repo/pull/13", HeadRefName: "branch-13"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set([]byte("11111111aaaa"), "gh", "pr", "view", "13", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")

	now := time.Date(2026, 4, 9, 21, 0, 0, 0, time.UTC)
	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow:      "review-pr",
				PRHeadUpdated: true,
				Debounce:      UnsetPREventsDebounce,
			},
		},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
		Now: func() time.Time {
			return now
		},
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if _, err := q.Enqueue(vessels[0]); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := g.OnEnqueue(context.Background(), vessels[0]); err != nil {
		t.Fatalf("OnEnqueue: %v", err)
	}

	now = now.Add(5 * time.Minute)
	r.set([]byte("22222222bbbb"), "gh", "pr", "view", "13", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")
	vessels, err = g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan inside debounce window: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels inside debounce window, got %d", len(vessels))
	}

	now = now.Add(6 * time.Minute)
	vessels, err = g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan after debounce window: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel after debounce window, got %d", len(vessels))
	}
	if !strings.Contains(vessels[0].Ref, "#head-22222222bbbb") {
		t.Fatalf("Ref = %q, want #head-22222222bbbb", vessels[0].Ref)
	}
}

func TestPREventsDebounceStatePathMatchesSpec(t *testing.T) {
	g := &GitHubPREvents{StateDir: ".xylem"}
	if got := g.debounceStatePath(); got != filepath.Join(".xylem", "state", "pr-events", "debounce.json") {
		t.Fatalf("debounceStatePath() = %q", got)
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

func TestPREventsLifecycleHooksWithoutDebounceMetaLeaveStateUntouched(t *testing.T) {
	dir := t.TempDir()
	g := &GitHubPREvents{StateDir: dir}
	if err := g.storeDebounceEmittedAt(preventPRHeadUpdated, 42, time.Date(2026, 4, 9, 21, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed debounce state: %v", err)
	}
	want, err := os.ReadFile(g.debounceStatePath())
	if err != nil {
		t.Fatalf("read seeded debounce state: %v", err)
	}

	tests := []struct {
		name string
		run  func(context.Context, queue.Vessel) error
	}{
		{name: "on enqueue", run: g.OnEnqueue},
		{name: "on start", run: g.OnStart},
		{name: "on wait", run: g.OnWait},
		{name: "on resume", run: g.OnResume},
		{name: "on complete", run: g.OnComplete},
		{name: "on fail", run: g.OnFail},
		{name: "on timed out", run: g.OnTimedOut},
		{name: "remove running label", run: g.RemoveRunningLabel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(context.Background(), queue.Vessel{}); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			got, err := os.ReadFile(g.debounceStatePath())
			if err != nil {
				t.Fatalf("read debounce state after %s: %v", tt.name, err)
			}
			if string(got) != string(want) {
				t.Fatalf("%s mutated debounce state:\nwant %s\ngot  %s", tt.name, want, got)
			}
		})
	}
}

func TestSmoke_S44_PROpenedScansExactlyOncePerPR(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 21, Title: "PR 21", URL: "https://github.com/owner/repo/pull/21", HeadRefName: "feature/pr-opened"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow: "review-pr",
				PROpened: true,
			},
		},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)

	v := vessels[0]
	assert.Equal(t, "pr_opened", v.Meta["event_type"])
	assert.Equal(t, "21", v.Meta["pr_num"])
	assert.Equal(t, "feature/pr-opened", v.Meta["pr_head_branch"])
	assert.Contains(t, v.Ref, "#pr-opened")

	_, err = q.Enqueue(v)
	require.NoError(t, err)
	require.NoError(t, g.OnEnqueue(context.Background(), v))

	vessels, err = g.Scan(context.Background())
	require.NoError(t, err)
	assert.Empty(t, vessels)
}

func TestSmoke_S45_PRHeadUpdatedScansPerHeadSHA(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 22, Title: "PR 22", URL: "https://github.com/owner/repo/pull/22", HeadRefName: "feature/head-updated"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set([]byte("aaaabbbbcccc"), "gh", "pr", "view", "22", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow:      "review-pr",
				PRHeadUpdated: true,
				Debounce:      0,
			},
		},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "pr_head_updated", vessels[0].Meta["event_type"])
	assert.Contains(t, vessels[0].Ref, "#head-aaaabbbbcccc")

	_, err = q.Enqueue(vessels[0])
	require.NoError(t, err)
	require.NoError(t, g.OnEnqueue(context.Background(), vessels[0]))

	vessels, err = g.Scan(context.Background())
	require.NoError(t, err)
	assert.Empty(t, vessels)

	r.set([]byte("dddd1111eeee"), "gh", "pr", "view", "22", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")
	vessels, err = g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Contains(t, vessels[0].Ref, "#head-dddd1111eeee")
}

func TestSmoke_S46_PRHeadUpdatedDefaultDebounceCapsRapidHeadAdvances(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 23, Title: "PR 23", URL: "https://github.com/owner/repo/pull/23", HeadRefName: "feature/debounce"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set([]byte("111122223333"), "gh", "pr", "view", "23", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")

	now := time.Date(2026, 4, 9, 21, 0, 0, 0, time.UTC)
	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"review": {
				Workflow:      "review-pr",
				PRHeadUpdated: true,
				Debounce:      UnsetPREventsDebounce,
			},
		},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
		Now: func() time.Time {
			return now
		},
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)

	_, err = q.Enqueue(vessels[0])
	require.NoError(t, err)
	require.NoError(t, g.OnEnqueue(context.Background(), vessels[0]))

	now = now.Add(5 * time.Minute)
	r.set([]byte("444455556666"), "gh", "pr", "view", "23", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid")
	vessels, err = g.Scan(context.Background())
	require.NoError(t, err)
	assert.Empty(t, vessels)

	now = now.Add(6 * time.Minute)
	vessels, err = g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Contains(t, vessels[0].Ref, "#head-444455556666")
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

// Helper shorthands for author-filter tests.
type evt = struct {
	ID    int64
	Login string
	Type  string
}

// newReviewEventsSource wires up a PR with the given review events and
// configures the source for a single review_submitted task with the given
// filter. Returns the mock so tests can stub gh api user if needed.
func newReviewEventsSource(t *testing.T, task PREventsTask, events ...evt) (*GitHubPREvents, *queue.Queue, *mockCmdRunner) {
	t.Helper()
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 42, Title: "test", URL: "https://github.com/owner/repo/pull/42", HeadRefName: "b"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set(authoredEventsLines(events...), "gh", "api", "repos/owner/repo/pulls/42/reviews", "--jq", reviewsJQ)

	g := &GitHubPREvents{
		Repo:      "owner/repo",
		Tasks:     map[string]PREventsTask{"reviews": task},
		Queue:     q,
		CmdRunner: r,
	}
	return g, q, r
}

// TestPREventsScanReviewAuthorAllowFiltersMixed is the canonical loop-fix
// regression test: given a PR with reviews from a Copilot bot, xylem's
// self login, and an unrelated human, and an AuthorAllow set to just the
// Copilot bot login, exactly ONE vessel should be created (the Copilot
// one). If this ever stops holding, the feedback loop on PR #114 can
// recur.
func TestPREventsScanReviewAuthorAllowFiltersMixed(t *testing.T) {
	task := PREventsTask{
		Workflow:        "handle-review",
		ReviewSubmitted: true,
		AuthorAllow:     []string{"copilot-pull-request-reviewer[bot]"},
	}
	g, _, r := newReviewEventsSource(t, task,
		evt{1001, "copilot-pull-request-reviewer[bot]", "Bot"},
		evt{1002, "hnipps", "User"},
		evt{1003, "other-human", "User"},
	)
	r.set([]byte("hnipps"), "gh", "api", "user", "--jq", ".login")

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected exactly 1 vessel (Copilot only), got %d", len(vessels))
	}
	if vessels[0].Meta["review_author"] != "copilot-pull-request-reviewer[bot]" {
		t.Errorf("review_author = %q, want copilot-pull-request-reviewer[bot]", vessels[0].Meta["review_author"])
	}
}

func TestPREventsScanReviewAuthorDenyBlocks(t *testing.T) {
	task := PREventsTask{
		Workflow:        "handle-review",
		ReviewSubmitted: true,
		AuthorDeny:      []string{"copilot-pull-request-reviewer[bot]"},
	}
	g, _, r := newReviewEventsSource(t, task,
		evt{1001, "copilot-pull-request-reviewer[bot]", "Bot"},
		evt{1002, "alice", "User"},
	)
	r.set([]byte(""), "gh", "api", "user", "--jq", ".login")

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel (alice only), got %d", len(vessels))
	}
	if vessels[0].Meta["review_author"] != "alice" {
		t.Errorf("review_author = %q, want alice", vessels[0].Meta["review_author"])
	}
}

// TestPREventsScanReviewSelfFilterInvariant asserts the self-filter cannot
// be overridden by AuthorAllow. If xylem's own login appears in the
// allowlist (via a misconfigured .xylem.yml), the self-filter still skips
// it. This is the INVARIANT guaranteed by shouldSkipAuthoredEvent rule 1.
func TestPREventsScanReviewSelfFilterInvariant(t *testing.T) {
	task := PREventsTask{
		Workflow:        "handle-review",
		ReviewSubmitted: true,
		AuthorAllow:     []string{"hnipps"}, // misconfigured: allows self
	}
	g, _, r := newReviewEventsSource(t, task,
		evt{1001, "hnipps", "User"},
	)
	r.set([]byte("hnipps"), "gh", "api", "user", "--jq", ".login")

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (self-filter is always-on), got %d", len(vessels))
	}
}

// TestPREventsScanReviewNullUser verifies that a review with user: null
// (JSON null, from a deleted GitHub account) is handled safely: the login
// parses as empty string, and the allowlist rejects it.
func TestPREventsScanReviewNullUser(t *testing.T) {
	task := PREventsTask{
		Workflow:        "handle-review",
		ReviewSubmitted: true,
		AuthorAllow:     []string{"copilot-pull-request-reviewer[bot]"},
	}
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 42, Title: "t", URL: "https://github.com/owner/repo/pull/42", HeadRefName: "b"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	// Emulate what gh emits when user is null: "login" becomes literal null
	// which jq preserves; the .login field then arrives as JSON null and
	// Go's json.Unmarshal leaves the string field empty.
	r.set([]byte(`{"id":2001,"login":null,"type":null}`+"\n"), "gh", "api", "repos/owner/repo/pulls/42/reviews", "--jq", reviewsJQ)
	r.set([]byte("hnipps"), "gh", "api", "user", "--jq", ".login")

	g := &GitHubPREvents{
		Repo:      "owner/repo",
		Tasks:     map[string]PREventsTask{"reviews": task},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected 0 vessels (null user fails allowlist), got %d", len(vessels))
	}
}

func TestPREventsScanReviewMalformedLineSkipped(t *testing.T) {
	task := PREventsTask{
		Workflow:        "handle-review",
		ReviewSubmitted: true,
		AuthorAllow:     []string{"alice"},
	}
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 42, Title: "t", URL: "https://github.com/owner/repo/pull/42", HeadRefName: "b"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	// First line is garbage, second line is a valid event. Expect the garbage
	// to be skipped and the second event to still produce a vessel.
	r.set([]byte("not-json\n"+`{"id":9001,"login":"alice","type":"User"}`+"\n"),
		"gh", "api", "repos/owner/repo/pulls/42/reviews", "--jq", reviewsJQ)
	r.set([]byte("hnipps"), "gh", "api", "user", "--jq", ".login")

	g := &GitHubPREvents{
		Repo:      "owner/repo",
		Tasks:     map[string]PREventsTask{"reviews": task},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel (valid line), got %d", len(vessels))
	}
}

// TestPREventsScanReviewSelfLoginLookupFailsAllowlistStillEnforced verifies
// that if `gh api user` fails, scanning continues but the allowlist is
// still enforced (self-filter is best-effort, allowlist is the primary
// guard — enforced statically by config validation).
func TestPREventsScanReviewSelfLoginLookupFailsAllowlistStillEnforced(t *testing.T) {
	task := PREventsTask{
		Workflow:        "handle-review",
		ReviewSubmitted: true,
		AuthorAllow:     []string{"copilot-pull-request-reviewer[bot]"},
	}
	g, _, r := newReviewEventsSource(t, task,
		evt{1001, "copilot-pull-request-reviewer[bot]", "Bot"},
		evt{1002, "hnipps", "User"},
	)
	r.setErr(fmt.Errorf("auth failed"), "gh", "api", "user", "--jq", ".login")

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel (allowlist still blocks hnipps), got %d", len(vessels))
	}
	if vessels[0].Meta["review_author"] != "copilot-pull-request-reviewer[bot]" {
		t.Errorf("review_author = %q, want copilot-pull-request-reviewer[bot]", vessels[0].Meta["review_author"])
	}
}

func TestPREventsScanSelfLoginResolvedOncePerScan(t *testing.T) {
	task := PREventsTask{
		Workflow:        "handle-review",
		ReviewSubmitted: true,
		AuthorAllow:     []string{"copilot-pull-request-reviewer[bot]"},
	}
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	// Two PRs to force two iterations, each calling scanReviews.
	prs := []ghPR{
		{Number: 1, Title: "a", URL: "https://github.com/owner/repo/pull/1", HeadRefName: "a"},
		{Number: 2, Title: "b", URL: "https://github.com/owner/repo/pull/2", HeadRefName: "b"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set(authoredEventsLines(evt{1, "copilot-pull-request-reviewer[bot]", "Bot"}),
		"gh", "api", "repos/owner/repo/pulls/1/reviews", "--jq", reviewsJQ)
	r.set(authoredEventsLines(evt{2, "copilot-pull-request-reviewer[bot]", "Bot"}),
		"gh", "api", "repos/owner/repo/pulls/2/reviews", "--jq", reviewsJQ)
	r.set([]byte("hnipps"), "gh", "api", "user", "--jq", ".login")

	g := &GitHubPREvents{
		Repo:      "owner/repo",
		Tasks:     map[string]PREventsTask{"reviews": task},
		Queue:     q,
		CmdRunner: r,
	}

	if _, err := g.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Count gh api user calls — must be exactly 1 for the whole Scan.
	ghUserCalls := 0
	for _, call := range r.calls {
		if len(call) >= 4 && call[0] == "gh" && call[1] == "api" && call[2] == "user" {
			ghUserCalls++
		}
	}
	if ghUserCalls != 1 {
		t.Errorf("expected 1 gh api user call per Scan, got %d", ghUserCalls)
	}
}

func TestPREventsScanCommentSelfFilter(t *testing.T) {
	// Mirror of the review self-filter test for comments.
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{
		{Number: 9, Title: "t", URL: "https://github.com/owner/repo/pull/9", HeadRefName: "b"},
	}
	r.set(prEventsListJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,url,labels,headRefName", "--limit", "50")
	r.set(authoredEventsLines(
		evt{9001, "hnipps", "User"},
		evt{9002, "alice", "User"},
	), "gh", "api", "repos/owner/repo/issues/9/comments", "--jq", commentsJQ)
	r.set([]byte("hnipps"), "gh", "api", "user", "--jq", ".login")

	g := &GitHubPREvents{
		Repo: "owner/repo",
		Tasks: map[string]PREventsTask{
			"comments": {
				Workflow:    "respond",
				Commented:   true,
				AuthorAllow: []string{"alice", "hnipps"}, // hnipps allowed but self-filtered
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
		t.Fatalf("expected 1 vessel (alice only, hnipps self-filtered), got %d", len(vessels))
	}
	if vessels[0].Meta["comment_author"] != "alice" {
		t.Errorf("comment_author = %q, want alice", vessels[0].Meta["comment_author"])
	}
}

// TestShouldSkipAuthoredEvent covers the filter precedence rules in
// isolation — cheap unit test that documents the INVARIANT: deny > allow,
// self-filter > everything.
func TestShouldSkipAuthoredEvent(t *testing.T) {
	tests := []struct {
		name      string
		login     string
		selfLogin string
		task      PREventsTask
		wantSkip  bool
	}{
		{
			name:      "self always skipped",
			login:     "hnipps",
			selfLogin: "hnipps",
			task:      PREventsTask{AuthorAllow: []string{"hnipps"}}, // even allowlisted
			wantSkip:  true,
		},
		{
			name:      "deny wins over allow",
			login:     "bot",
			selfLogin: "",
			task: PREventsTask{
				AuthorAllow: []string{"bot"},
				AuthorDeny:  []string{"bot"},
			},
			wantSkip: true,
		},
		{
			name:      "allowlisted passes",
			login:     "bot",
			selfLogin: "",
			task:      PREventsTask{AuthorAllow: []string{"bot"}},
			wantSkip:  false,
		},
		{
			name:      "not in allowlist skipped",
			login:     "alice",
			selfLogin: "",
			task:      PREventsTask{AuthorAllow: []string{"bot"}},
			wantSkip:  true,
		},
		{
			name:      "empty allow + empty deny passes",
			login:     "anyone",
			selfLogin: "",
			task:      PREventsTask{},
			wantSkip:  false,
		},
		{
			name:      "empty login fails allowlist",
			login:     "",
			selfLogin: "hnipps",
			task:      PREventsTask{AuthorAllow: []string{"bot"}},
			wantSkip:  true,
		},
		{
			name:      "empty login passes when no allowlist",
			login:     "",
			selfLogin: "hnipps",
			task:      PREventsTask{},
			wantSkip:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipAuthoredEvent(tt.login, tt.selfLogin, tt.task)
			if got != tt.wantSkip {
				t.Errorf("shouldSkipAuthoredEvent(%q, %q, %+v) = %v, want %v",
					tt.login, tt.selfLogin, tt.task, got, tt.wantSkip)
			}
		})
	}
}
