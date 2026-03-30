package source

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
			Labels: []struct{ Name string `json:"name"` }{{Name: "review-me"}}},
		{Number: 20, Title: "add tests", URL: "https://github.com/owner/repo/pull/20", HeadRefName: "add-tests",
			Labels: []struct{ Name string `json:"name"` }{{Name: "review-me"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,url,labels,headRefName", "--limit", "20")

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
			Labels: []struct{ Name string `json:"name"` }{{Name: "review-me"}, {Name: "wontfix"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,url,labels,headRefName", "--limit", "20")

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
		Meta: map[string]string{"pr_num": "10"},
		State: queue.StatePending,
	})
	r := newMock()

	prs := []ghPR{
		{Number: 10, Title: "already queued", URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix",
			Labels: []struct{ Name string `json:"name"` }{{Name: "review-me"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,url,labels,headRefName", "--limit", "20")

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
			Labels: []struct{ Name string `json:"name"` }{{Name: "review-me"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,url,labels,headRefName", "--limit", "20")
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
			Labels: []struct{ Name string `json:"name"` }{{Name: "review-me"}, {Name: "urgent"}}},
	}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,url,labels,headRefName", "--limit", "20")
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "urgent", "--json", "number,title,url,labels,headRefName", "--limit", "20")

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

	r.setErr(errTest, "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,url,labels,headRefName", "--limit", "20")

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

	r.set([]byte(`{not valid`), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,url,labels,headRefName", "--limit", "20")

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

var errTest = &testError{"test error"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
