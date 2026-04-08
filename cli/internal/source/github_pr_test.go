package source

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

type mockCmdRunner struct {
	calls   [][]string
	outputs map[string][]byte
	errs    map[string]error
}

func newMock() *mockCmdRunner {
	return &mockCmdRunner{
		outputs: make(map[string][]byte),
		errs:    make(map[string]error),
	}
}

func (m *mockCmdRunner) set(out []byte, args ...string) {
	m.outputs[strings.Join(args, " ")] = out
}

func (m *mockCmdRunner) setErr(err error, args ...string) {
	m.errs[strings.Join(args, " ")] = err
}

func (m *mockCmdRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	parts := append([]string{name}, args...)
	m.calls = append(m.calls, parts)
	key := strings.Join(parts, " ")
	if err, ok := m.errs[key]; ok {
		return nil, err
	}
	if out, ok := m.outputs[key]; ok {
		return out, nil
	}
	return []byte("[]"), nil
}

func prJSON(prs []ghPR) []byte {
	b, _ := json.Marshal(prs)
	return b
}

func TestGitHubPRScanFindsPRs(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "fix readme", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-readme",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "review-me"}}},
		{Number: 20, Title: "add tests", URL: "https://github.com/owner/repo/pull/20", HeadRefName: "add-tests",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "review-me"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")

	src := &GitHubPR{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Exclude:   nil,
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
		if v.Source != "github-pr" {
			t.Errorf("expected source github-pr, got %q", v.Source)
		}
		if v.Meta["pr_num"] == "" {
			t.Error("expected Meta[pr_num] to be set")
		}
		if !strings.HasPrefix(v.ID, "pr-") {
			t.Errorf("expected ID to start with pr-, got %q", v.ID)
		}
		if v.Workflow != "review-pr" {
			t.Errorf("expected workflow review-pr, got %q", v.Workflow)
		}
	}
}

func TestGitHubPRScanExcludedLabel(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "excluded pr", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "excluded",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "review-me"}, {Name: "wontfix"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")

	src := &GitHubPR{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Exclude:   []string{"wontfix"},
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

func TestGitHubPRScanAlreadyQueued(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	_, _ = q.Enqueue(queue.Vessel{
		ID: "pr-10", Source: "github-pr",
		Ref: "https://github.com/owner/repo/pull/10", Workflow: "review-pr",
		Meta:  map[string]string{"pr_num": "10"},
		State: queue.StatePending,
	})
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "already queued", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "review-me"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")

	src := &GitHubPR{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels (already queued), got %d", len(vessels))
	}
}

func TestGitHubPRScanExistingBranch(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 42, Title: "has branch", URL: "https://github.com/owner/repo/pull/42", HeadRefName: "fix",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "review-me"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")
	r.set([]byte("abc123\trefs/heads/review/pr-42-something"), "git", "ls-remote", "--heads", "origin", "review/pr-42-*")

	src := &GitHubPR{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels (existing branch), got %d", len(vessels))
	}
}

func TestGitHubPRScanDeduplicates(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "dup pr", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "review-me"}, {Name: "urgent"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "urgent", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")

	src := &GitHubPR{
		Repo: "owner/repo",
		Tasks: map[string]GitHubTask{
			"review":  {Labels: []string{"review-me"}, Workflow: "review-pr"},
			"urgents": {Labels: []string{"urgent"}, Workflow: "review-pr"},
		},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 1 {
		t.Errorf("expected 1 vessel (dedup), got %d", len(vessels))
	}
}

func TestGitHubPRScanGHFailure(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	r.setErr(errTest, "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")

	src := &GitHubPR{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Queue:     q,
		CmdRunner: r,
	}

	_, err := src.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error from gh failure, got nil")
	}
}

func TestGitHubPRScanMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	r.set([]byte(`{not valid`), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")

	src := &GitHubPR{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Queue:     q,
		CmdRunner: r,
	}

	_, err := src.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestGitHubPRScanSkipsUnchangedFailedVessel(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "same title", Body: "same body", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "review-me"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")

	fingerprint := githubSourceFingerprint("same title", "same body", []string{"review-me"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "pr-10",
		Source:   "github-pr",
		Ref:      "https://github.com/owner/repo/pull/10",
		Workflow: "review-pr",
		Meta: map[string]string{
			"pr_num":                   "10",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("enqueue failed vessel seed: %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update("pr-10", queue.StateFailed, "boom"); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	src := &GitHubPR{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("expected unchanged failed PR to be skipped, got %d vessels", len(vessels))
	}
}

func TestGitHubPRScanReenqueuesChangedFailedVessel(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(dir + "/queue.jsonl")
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "same title", Body: "updated body", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "review-me"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName", "--limit", "20")

	oldFingerprint := githubSourceFingerprint("same title", "same body", []string{"review-me"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "pr-10",
		Source:   "github-pr",
		Ref:      "https://github.com/owner/repo/pull/10",
		Workflow: "review-pr",
		Meta: map[string]string{
			"pr_num":                   "10",
			"source_input_fingerprint": oldFingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("enqueue failed vessel seed: %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update("pr-10", queue.StateFailed, "boom"); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	src := &GitHubPR{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected changed failed PR to be re-enqueued, got %d vessels", len(vessels))
	}
	if vessels[0].Meta["source_input_fingerprint"] == oldFingerprint {
		t.Fatal("expected updated fingerprint for changed PR input")
	}
}

func TestGitHubPROnStart(t *testing.T) {
	r := newMock()
	src := &GitHubPR{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	vessel := queue.Vessel{
		ID:     "pr-10",
		Source: "github-pr",
		Meta:   map[string]string{"pr_num": "10"},
	}
	err := src.OnStart(context.Background(), vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	call := strings.Join(r.calls[0], " ")
	if !strings.Contains(call, "pr edit") {
		t.Errorf("expected 'pr edit' in call, got %q", call)
	}
	if !strings.Contains(call, "in-progress") {
		t.Errorf("expected 'in-progress' label in call, got %q", call)
	}
}

func TestGitHubPROnStartNilRunner(t *testing.T) {
	src := &GitHubPR{
		Repo:      "owner/repo",
		CmdRunner: nil,
	}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10"},
	}
	err := src.OnStart(context.Background(), vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGitHubPROnStartMissingMeta(t *testing.T) {
	r := newMock()
	src := &GitHubPR{
		Repo:      "owner/repo",
		CmdRunner: r,
	}
	vessel := queue.Vessel{ID: "pr-10", Meta: map[string]string{}}
	err := src.OnStart(context.Background(), vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no calls for missing pr_num, got %d", len(r.calls))
	}
}

func TestGitHubPRBranchName(t *testing.T) {
	src := &GitHubPR{Repo: "owner/repo"}
	vessel := queue.Vessel{
		ID:   "pr-42",
		Ref:  "https://github.com/owner/repo/pull/42",
		Meta: map[string]string{"pr_num": "42"},
	}
	branch := src.BranchName(vessel)
	if !strings.HasPrefix(branch, "review/pr-42-") {
		t.Errorf("expected branch to start with review/pr-42-, got %q", branch)
	}
}

func TestGitHubPRName(t *testing.T) {
	src := &GitHubPR{}
	if src.Name() != "github-pr" {
		t.Errorf("expected name github-pr, got %q", src.Name())
	}
}

func TestGitHubPROnEnqueue(t *testing.T) {
	r := newMock()
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10", "status_label_queued": "queued"},
	}
	if err := src.OnEnqueue(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, call := range r.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "pr edit") && strings.Contains(joined, "--add-label") && strings.Contains(joined, "queued") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gh pr edit --add-label queued, calls: %v", r.calls)
	}
}

func TestGitHubPROnEnqueueNoLabelConfigured(t *testing.T) {
	r := newMock()
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{ID: "pr-10", Meta: map[string]string{"pr_num": "10"}}
	if err := src.OnEnqueue(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no calls when queued label not configured, got %v", r.calls)
	}
}

func TestGitHubPROnStartConfiguredLabel(t *testing.T) {
	r := newMock()
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10", "status_label_running": "wip", "status_label_queued": "queued"},
	}
	if err := src.OnStart(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label wip") {
		t.Errorf("expected --add-label wip in call, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label queued") {
		t.Errorf("expected --remove-label queued in call, got %q", joined)
	}
}

func TestGitHubPROnComplete(t *testing.T) {
	r := newMock()
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10", "status_label_completed": "done", "status_label_running": "wip"},
	}
	if err := src.OnComplete(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label done") {
		t.Errorf("expected --add-label done in call, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label wip") {
		t.Errorf("expected --remove-label wip in call, got %q", joined)
	}
}

func TestGitHubPROnFail(t *testing.T) {
	r := newMock()
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10", "status_label_failed": "failed", "status_label_running": "wip"},
	}
	if err := src.OnFail(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label failed") {
		t.Errorf("expected --add-label failed in call, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label wip") {
		t.Errorf("expected --remove-label wip in call, got %q", joined)
	}
}

func TestGitHubPROnTimedOut(t *testing.T) {
	r := newMock()
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10", "status_label_timed_out": "timed-out", "status_label_running": "wip"},
	}
	if err := src.OnTimedOut(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label timed-out") {
		t.Errorf("expected --add-label timed-out, got %q", joined)
	}
}

func TestGitHubPRLifecycleNilRunner(t *testing.T) {
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: nil}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10", "status_label_queued": "queued", "status_label_running": "wip", "status_label_failed": "failed"},
	}
	ctx := context.Background()
	if err := src.OnEnqueue(ctx, vessel); err != nil {
		t.Errorf("OnEnqueue with nil runner: %v", err)
	}
	if err := src.OnComplete(ctx, vessel); err != nil {
		t.Errorf("OnComplete with nil runner: %v", err)
	}
	if err := src.OnFail(ctx, vessel); err != nil {
		t.Errorf("OnFail with nil runner: %v", err)
	}
	if err := src.OnTimedOut(ctx, vessel); err != nil {
		t.Errorf("OnTimedOut with nil runner: %v", err)
	}
}

func TestGitHubIssueOnStartBackwardCompat(t *testing.T) {
	// When status_label_running is absent (old vessel), OnStart should add "in-progress"
	r := newMock()
	src := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "issue-1",
		Meta: map[string]string{"issue_num": "1"},
	}
	if err := src.OnStart(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "in-progress") {
		t.Errorf("expected in-progress fallback label, got %q", joined)
	}
}

func TestGitHubIssueOnStartConfiguredLabel(t *testing.T) {
	r := newMock()
	src := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "issue-1",
		Meta: map[string]string{"issue_num": "1", "status_label_running": "active", "status_label_queued": "queued"},
	}
	if err := src.OnStart(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label active") {
		t.Errorf("expected --add-label active, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label queued") {
		t.Errorf("expected --remove-label queued, got %q", joined)
	}
}

func TestGitHubIssueOnCompleteNoLabels(t *testing.T) {
	// When no status labels are configured, OnComplete still removes the
	// backward-compat "in-progress" label that OnStart would have added.
	r := newMock()
	src := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "issue-1",
		Meta: map[string]string{"issue_num": "1"},
	}
	if err := src.OnComplete(context.Background(), vessel); err != nil {
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

func TestResolveRunningLabel(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]string
		want string
	}{
		{"no meta key falls back to in-progress", map[string]string{}, "in-progress"},
		{"configured label", map[string]string{"status_label_running": "wip"}, "wip"},
		{"empty configured label", map[string]string{"status_label_running": ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := queue.Vessel{Meta: tt.meta}
			got := ResolveRunningLabel(v)
			if got != tt.want {
				t.Errorf("ResolveRunningLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGitHubPRRemoveRunningLabel(t *testing.T) {
	r := newMock()
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10", "status_label_running": "wip"},
	}
	if err := src.RemoveRunningLabel(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--remove-label wip") {
		t.Errorf("expected --remove-label wip, got %q", joined)
	}
	if strings.Contains(joined, "--add-label") {
		t.Errorf("expected no --add-label, got %q", joined)
	}
}

func TestGitHubPRRemoveRunningLabelBackwardCompat(t *testing.T) {
	// Legacy vessel without status_label_running should remove "in-progress"
	r := newMock()
	src := &GitHubPR{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "pr-10",
		Meta: map[string]string{"pr_num": "10"},
	}
	if err := src.RemoveRunningLabel(context.Background(), vessel); err != nil {
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

func TestGitHubIssueRemoveRunningLabel(t *testing.T) {
	r := newMock()
	src := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:   "issue-1",
		Meta: map[string]string{"issue_num": "1", "status_label_running": "wip"},
	}
	if err := src.RemoveRunningLabel(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--remove-label wip") {
		t.Errorf("expected --remove-label wip, got %q", joined)
	}
}

var errTest = &testError{"test error"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
