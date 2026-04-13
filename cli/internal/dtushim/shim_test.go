package dtushim

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T, state *dtu.State) (*dtu.Store, string) {
	t.Helper()
	stateDir := t.TempDir()
	store, err := dtu.NewStore(stateDir, state.UniverseID)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return store, stateDir
}

func envForStore(store *dtu.Store, stateDir string, universeID string) []string {
	return []string{
		envStatePath + "=" + store.Path(),
		envStateDir + "=" + stateDir,
		envUniverseID + "=" + universeID,
	}
}

func sampleState() *dtu.State {
	return &dtu.State{
		UniverseID: "universe-1",
		Version:    "v1",
		Metadata:   dtu.ManifestMetadata{Name: "sample"},
		Clock:      dtu.ClockState{Now: "2026-01-02T03:04:05Z"},
		Repositories: []dtu.Repository{{
			Owner:         "owner",
			Name:          "repo",
			DefaultBranch: "main",
			Labels:        []dtu.Label{{Name: "bug"}, {Name: "queued"}, {Name: "done"}},
			Issues: []dtu.Issue{{
				Number: 1,
				Title:  "Bug one",
				Body:   "Fix this",
				State:  dtu.IssueStateOpen,
				Labels: []string{"bug"},
			}, {
				Number: 2,
				Title:  "Closed bug",
				State:  dtu.IssueStateClosed,
				Labels: []string{"bug"},
			}},
			PullRequests: []dtu.PullRequest{{
				Number:     10,
				Title:      "Fix PR",
				Body:       "PR body",
				State:      dtu.PullRequestStateOpen,
				BaseBranch: "main",
				HeadBranch: "fix/issue-1-bug-one",
				HeadSHA:    "abc12345deadbeef",
				Labels:     []string{"queued"},
				Reviews:    []dtu.Review{{ID: 41, State: dtu.ReviewStateApproved}},
				Checks:     []dtu.Check{{ID: 51, Name: "ci", State: dtu.CheckStateFailure}},
			}, {
				Number:     11,
				Title:      "Merged PR",
				State:      dtu.PullRequestStateMerged,
				Merged:     true,
				BaseBranch: "main",
				HeadBranch: "fix/issue-2-merged",
				HeadSHA:    "feedfacecafebeef",
			}},
		}},
		Providers: dtu.Providers{Scripts: []dtu.ProviderScript{{
			Name:         "claude-phase",
			Provider:     dtu.ProviderClaude,
			Match:        dtu.ProviderScriptMatch{Phase: "analyze", PromptContains: "Issue 1"},
			Model:        "claude-sonnet",
			AllowedTools: []string{"Read"},
			Stdout:       "analysis complete",
		}, {
			Name:         "copilot-script",
			Provider:     dtu.ProviderCopilot,
			Match:        dtu.ProviderScriptMatch{PromptContains: "HARNESS"},
			Model:        "gpt-5",
			AllowedTools: []string{"Read,Write"},
			Stdout:       "copilot complete",
		}}},
		Counters: dtu.Counters{NextCommentID: 1, NextReviewID: 42, NextCheckID: 52},
	}
}

func TestExecuteGHSearchIssuesFiltersOpenLabelledIssues(t *testing.T) {
	t.Parallel()

	state := sampleState()
	store, stateDir := testStore(t, state)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), "gh", []string{"search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug"}, nil, &stdout, &stderr, envForStore(store, stateDir, state.UniverseID))
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, `"number":1`) || strings.Contains(got, `"number":2`) {
		t.Fatalf("search output = %s, want only open issue 1", got)
	}
}

func TestExecuteGHIssueEditMutatesLabels(t *testing.T) {
	t.Parallel()

	state := sampleState()
	store, stateDir := testStore(t, state)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), "gh", []string{"issue", "edit", "1", "--repo", "owner/repo", "--add-label", "done", "--remove-label", "bug"}, nil, &stdout, &stderr, envForStore(store, stateDir, state.UniverseID))
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issue := loaded.RepositoryBySlug("owner/repo").IssueByNumber(1)
	if issue == nil {
		t.Fatal("IssueByNumber() = nil")
		return
	}
	if strings.Join(issue.Labels, ",") != "done" {
		t.Fatalf("issue labels = %#v, want [done]", issue.Labels)
	}
}

func TestExecuteGHPRSurfacesUsedByXylem(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Repositories[0].PullRequests[0].Mergeable = "MERGEABLE"
	state.Repositories[0].PullRequests[0].ReviewDecision = "APPROVED"
	state.Repositories[0].PullRequests[0].ReviewRequests = []string{"copilot-pull-request-reviewer"}
	state.Repositories[0].PullRequests[0].ReviewThreads = []dtu.ReviewThread{{IsResolved: true}, {IsResolved: false}}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var listOut, listErr bytes.Buffer
	code := Execute(context.Background(), "gh", []string{"pr", "list", "--repo", "owner/repo", "--search", "head:fix/issue-1-", "--state", "open", "--json", "number,headRefName", "--limit", "5"}, nil, &listOut, &listErr, env)
	if code != 0 {
		t.Fatalf("pr list code = %d, stderr = %q", code, listErr.String())
	}
	if !strings.Contains(listOut.String(), `"headRefName":"fix/issue-1-bug-one"`) {
		t.Fatalf("pr list output = %s", listOut.String())
	}

	var viewOut, viewErr bytes.Buffer
	code = Execute(context.Background(), "gh", []string{"pr", "view", "10", "--repo", "owner/repo", "--json", "headRefOid", "--jq", ".headRefOid"}, nil, &viewOut, &viewErr, env)
	if code != 0 {
		t.Fatalf("pr view code = %d, stderr = %q", code, viewErr.String())
	}
	if strings.TrimSpace(viewOut.String()) != "abc12345deadbeef" {
		t.Fatalf("pr view output = %q", viewOut.String())
	}

	var richViewOut, richViewErr bytes.Buffer
	code = Execute(context.Background(), "gh", []string{
		"pr", "view", "10", "--repo", "owner/repo",
		"--json", "state,mergeable,reviewDecision,statusCheckRollup,reviewRequests,reviewThreads",
	}, nil, &richViewOut, &richViewErr, env)
	if code != 0 {
		t.Fatalf("pr view rich code = %d, stderr = %q", code, richViewErr.String())
	}
	rich := richViewOut.String()
	for _, want := range []string{
		`"state":"OPEN"`,
		`"mergeable":"MERGEABLE"`,
		`"reviewDecision":"APPROVED"`,
		`"login":"copilot-pull-request-reviewer"`,
		`"isResolved":false`,
		`"status":"COMPLETED"`,
	} {
		if !strings.Contains(rich, want) {
			t.Fatalf("rich pr view output = %s, want substring %q", rich, want)
		}
	}

	var checksOut, checksErr bytes.Buffer
	code = Execute(context.Background(), "gh", []string{"pr", "checks", "10", "--repo", "owner/repo", "--json", "name,state"}, nil, &checksOut, &checksErr, env)
	if code != 0 {
		t.Fatalf("pr checks code = %d, stderr = %q", code, checksErr.String())
	}
	if !strings.Contains(checksOut.String(), `"state":"FAILURE"`) {
		t.Fatalf("pr checks output = %s", checksOut.String())
	}
}

func TestExecuteGHPRMergeUpdatesStateAndGitVisibility(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Repositories[0].PullRequests[0].Checks[0].State = dtu.CheckStateSuccess
	state.Repositories[0].Branches = []dtu.Branch{
		{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		{Name: "fix/issue-1-bug-one", SHA: "abc12345deadbeef"},
		{Name: "keep-me", SHA: "2222222222222222222222222222222222222222"},
	}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var mergeOut, mergeErr bytes.Buffer
	code := Execute(
		context.Background(),
		"gh",
		[]string{"pr", "merge", "10", "--repo", "owner/repo", "--delete-branch", "--squash", "--auto"},
		nil,
		&mergeOut,
		&mergeErr,
		env,
	)
	if code != 0 {
		t.Fatalf("pr merge code = %d, stderr = %q", code, mergeErr.String())
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	repo := loaded.RepositoryBySlug("owner/repo")
	if repo == nil {
		t.Fatal("RepositoryBySlug() = nil")
	}
	pr := repo.PullRequestByNumber(10)
	if pr == nil {
		t.Fatal("PullRequestByNumber() = nil")
		return
	}
	if pr.State != dtu.PullRequestStateMerged || !pr.Merged {
		t.Fatalf("merged pr = %#v, want merged state", pr)
	}
	main := repo.BranchByName("main")
	if main == nil || main.SHA != pr.HeadSHA {
		t.Fatalf("main branch = %#v, want SHA %q", main, pr.HeadSHA)
	}
	if repo.BranchByName(pr.HeadBranch) != nil {
		t.Fatalf("head branch %q still visible after delete-branch", pr.HeadBranch)
	}
	if repo.BranchByName("keep-me") == nil {
		t.Fatal("unrelated branch was removed")
	}

	var mergedOut, mergedErr bytes.Buffer
	code = Execute(
		context.Background(),
		"gh",
		[]string{"pr", "list", "--repo", "owner/repo", "--state", "merged", "--json", "number,mergeCommit,headRefName", "--limit", "5"},
		nil,
		&mergedOut,
		&mergedErr,
		env,
	)
	if code != 0 {
		t.Fatalf("pr list merged code = %d, stderr = %q", code, mergedErr.String())
	}
	if !strings.Contains(mergedOut.String(), `"number":10`) || !strings.Contains(mergedOut.String(), `"oid":"abc12345deadbeef"`) {
		t.Fatalf("merged pr list output = %s", mergedOut.String())
	}

	var mainOut, mainErr bytes.Buffer
	code = Execute(context.Background(), "git", []string{"ls-remote", "--heads", "origin", "main"}, nil, &mainOut, &mainErr, env)
	if code != 0 {
		t.Fatalf("git ls-remote main code = %d, stderr = %q", code, mainErr.String())
	}
	if strings.TrimSpace(mainOut.String()) != "abc12345deadbeef\trefs/heads/main" {
		t.Fatalf("git ls-remote main output = %q", mainOut.String())
	}

	var headOut, headErr bytes.Buffer
	code = Execute(context.Background(), "git", []string{"ls-remote", "--heads", "origin", "fix/issue-1-*"}, nil, &headOut, &headErr, env)
	if code != 0 {
		t.Fatalf("git ls-remote head code = %d, stderr = %q", code, headErr.String())
	}
	if strings.Contains(headOut.String(), "fix/issue-1-bug-one") {
		t.Fatalf("git ls-remote head output = %q, want deleted head branch to disappear", headOut.String())
	}
}

func TestExecuteGHPRAdminMergeBypassesPendingChecks(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Repositories[0].Branches = []dtu.Branch{
		{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		{Name: "fix/issue-1-bug-one", SHA: "abc12345deadbeef"},
	}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var mergeOut, mergeErr bytes.Buffer
	code := Execute(
		context.Background(),
		"gh",
		[]string{"pr", "merge", "10", "--repo", "owner/repo", "--delete-branch", "--squash", "--admin"},
		nil,
		&mergeOut,
		&mergeErr,
		env,
	)
	require.Zero(t, code, "stderr = %q", mergeErr.String())

	loaded, err := store.Load()
	require.NoError(t, err)
	repo := loaded.RepositoryBySlug("owner/repo")
	require.NotNil(t, repo)
	pr := repo.PullRequestByNumber(10)
	require.NotNil(t, pr)
	assert.Equal(t, dtu.PullRequestStateMerged, pr.State)
	assert.True(t, pr.Merged)
	assert.False(t, pr.AutoMergeEnabled)
	assert.False(t, pr.AutoMergeDeleteBranch)
	main := repo.BranchByName("main")
	require.NotNil(t, main)
	assert.Equal(t, pr.HeadSHA, main.SHA)
	assert.Nil(t, repo.BranchByName(pr.HeadBranch))
}

func TestSmoke_S2_GHPRMergeWithAutoQueuesUntilChecksPass(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Repositories[0].PullRequests[0].Checks[0].State = dtu.CheckStatePending
	state.Repositories[0].Branches = []dtu.Branch{
		{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		{Name: "fix/issue-1-bug-one", SHA: "abc12345deadbeef"},
	}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var mergeOut, mergeErr bytes.Buffer
	code := Execute(
		context.Background(),
		"gh",
		[]string{"pr", "merge", "10", "--repo", "owner/repo", "--delete-branch", "--squash", "--auto"},
		nil,
		&mergeOut,
		&mergeErr,
		env,
	)
	require.Zero(t, code, "stderr = %q", mergeErr.String())

	loaded, err := store.Load()
	require.NoError(t, err)
	repo := loaded.RepositoryBySlug("owner/repo")
	require.NotNil(t, repo)
	pr := repo.PullRequestByNumber(10)
	require.NotNil(t, pr)
	assert.Equal(t, dtu.PullRequestStateOpen, pr.State)
	assert.False(t, pr.Merged)
	assert.True(t, pr.AutoMergeEnabled)
	assert.True(t, pr.AutoMergeDeleteBranch)
	main := repo.BranchByName("main")
	require.NotNil(t, main)
	assert.Equal(t, "1111111111111111111111111111111111111111", main.SHA)
	assert.NotNil(t, repo.BranchByName(pr.HeadBranch))
}

func TestExecuteGHAPIAndIssueCommentUseStateStore(t *testing.T) {
	t.Parallel()

	state := sampleState()
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var commentErr bytes.Buffer
	code := Execute(context.Background(), "gh", []string{"issue", "comment", "1", "--repo", "owner/repo", "--body", "hello from reporter"}, nil, ioDiscard{}, &commentErr, env)
	if code != 0 {
		t.Fatalf("issue comment code = %d, stderr = %q", code, commentErr.String())
	}

	var apiOut, apiErr bytes.Buffer
	code = Execute(context.Background(), "gh", []string{"api", "repos/owner/repo/issues/1/comments", "--jq", ".[].id"}, nil, &apiOut, &apiErr, env)
	if code != 0 {
		t.Fatalf("api code = %d, stderr = %q", code, apiErr.String())
	}
	if strings.TrimSpace(apiOut.String()) != "1" {
		t.Fatalf("comment ids = %q, want 1", apiOut.String())
	}
}

func TestExecuteGHIssueCreateAndSearch(t *testing.T) {
	t.Parallel()

	state := sampleState()
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var createOut, createErr bytes.Buffer
	code := Execute(context.Background(), "gh", []string{
		"issue", "create",
		"--repo", "owner/repo",
		"--title", "[sota-gap] Missing entropy management",
		"--body", "details",
		"--label", "enhancement",
		"--label", "ready-for-work",
	}, nil, &createOut, &createErr, env)
	if code != 0 {
		t.Fatalf("issue create code = %d, stderr = %q", code, createErr.String())
	}
	if strings.TrimSpace(createOut.String()) != "https://github.com/owner/repo/issues/3" {
		t.Fatalf("create output = %q, want created issue URL", createOut.String())
	}

	var searchOut, searchErr bytes.Buffer
	code = Execute(context.Background(), "gh", []string{
		"search", "issues",
		"--repo", "owner/repo",
		"--state", "open",
		"--search", "missing entropy management",
		"--json", "number,title,url",
	}, nil, &searchOut, &searchErr, env)
	if code != 0 {
		t.Fatalf("search issues code = %d, stderr = %q", code, searchErr.String())
	}
	if !strings.Contains(searchOut.String(), `"number":3`) {
		t.Fatalf("search output = %s, want new issue in results", searchOut.String())
	}
}

func TestExecuteClaudeUsesStdinAndPhaseMatching(t *testing.T) {
	t.Parallel()

	state := sampleState()
	store, stateDir := testStore(t, state)
	env := append(envForStore(store, stateDir, state.UniverseID), envPhase+"=analyze")

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("Please analyze Issue 1")
	code := Execute(context.Background(), "claude", []string{"-p", "--max-turns", "3", "--model", "claude-sonnet", "--allowedTools", "Read", "--append-system-prompt", "HARNESS"}, stdin, &stdout, &stderr, env)
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "analysis complete" {
		t.Fatalf("stdout = %q, want analysis complete", stdout.String())
	}
}

func TestExecuteCopilotUsesPromptFlagAndExplicitStatePath(t *testing.T) {
	t.Parallel()

	state := sampleState()
	store, _ := testStore(t, state)

	var stdout, stderr bytes.Buffer
	args := []string{"--dtu-state-path", store.Path(), "-p", "HARNESS\n\nReview this change", "-s", "--model", "gpt-5", "--available-tools", "Read,Write", "--allow-all-tools"}
	code := Execute(context.Background(), "copilot", args, nil, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "copilot complete" {
		t.Fatalf("stdout = %q, want copilot complete", stdout.String())
	}
}

func TestExecuteClaudeHangHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Providers.Scripts = append(state.Providers.Scripts, dtu.ProviderScript{
		Name:     "hang",
		Provider: dtu.ProviderClaude,
		Match:    dtu.ProviderScriptMatch{PromptExact: "hang forever"},
		Hang:     true,
	})
	store, stateDir := testStore(t, state)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := Execute(ctx, "claude", []string{"-p", "hang forever"}, nil, &stdout, &stderr, envForStore(store, stateDir, state.UniverseID))
	if code == 0 {
		t.Fatal("expected non-zero exit code for canceled hang")
	}
	if !strings.Contains(stderr.String(), "context deadline exceeded") {
		t.Fatalf("stderr = %q, want context deadline exceeded", stderr.String())
	}
}

func TestExecuteGitWorktreeCommandsMaterializeDirectories(t *testing.T) {
	// Cannot run in parallel: uses os.Chdir which mutates global CWD state.
	state := sampleState()
	state.Repositories[0].Branches = []dtu.Branch{{Name: "main", SHA: "abc12345deadbeef"}}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	workdir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	target := filepath.Join(".xylem", "worktrees", "fix-issue-1")

	var addOut, addErr bytes.Buffer
	code := Execute(context.Background(), "git", []string{"worktree", "add", target, "-B", "fix/issue-1-bug-one", "origin/main"}, nil, &addOut, &addErr, env)
	if code != 0 {
		t.Fatalf("worktree add code = %d, stderr = %q", code, addErr.String())
	}
	absTarget := filepath.Join(workdir, target)
	if info, err := os.Stat(absTarget); err != nil || !info.IsDir() {
		t.Fatalf("expected worktree directory %q to exist, err = %v", absTarget, err)
	}

	var removeOut, removeErr bytes.Buffer
	code = Execute(context.Background(), "git", []string{"worktree", "remove", target, "--force"}, nil, &removeOut, &removeErr, env)
	if code != 0 {
		t.Fatalf("worktree remove code = %d, stderr = %q", code, removeErr.String())
	}
	if _, err := os.Stat(absTarget); !os.IsNotExist(err) {
		t.Fatalf("expected worktree directory %q to be removed, err = %v", absTarget, err)
	}
}

func TestExecuteGitBranchForceDeleteRemovesBranch(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Repositories[0].Branches = []dtu.Branch{{Name: "fix/issue-1-bug-one", SHA: "abc123"}}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), "git", []string{"branch", "-D", "fix/issue-1-bug-one"}, nil, &stdout, &stderr, env)
	if code != 0 {
		t.Fatalf("git branch -D code = %d, stderr = %q", code, stderr.String())
	}

	got, err := store.Load()
	require.NoError(t, err)
	require.Empty(t, got.Repositories[0].Branches)
}

func TestExecuteGitFetchSupportsMultipleBranches(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Repositories[0].Branches = []dtu.Branch{
		{Name: "main", SHA: "1111111"},
		{Name: "fix/issue-1-bug-one", SHA: "abc1234"},
	}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), "git", []string{"fetch", "origin", "fix/issue-1-bug-one", "main"}, nil, &stdout, &stderr, env)
	require.Zero(t, code, "stderr = %q", stderr.String())
	assert.Empty(t, stdout.String())

	loaded, err := store.Load()
	require.NoError(t, err)
	repo := loaded.RepositoryBySlug("owner/repo")
	require.NotNil(t, repo)
	require.NotNil(t, repo.BranchByName("fix/issue-1-bug-one"))
	require.NotNil(t, repo.BranchByName("main"))

	events, err := store.ReadEvents()
	require.NoError(t, err)
	var invocation, result *dtu.Event
	for i := range events {
		if events[i].Shim == nil || events[i].Shim.Command != "git" || len(events[i].Shim.Args) == 0 || events[i].Shim.Args[0] != "fetch" {
			continue
		}
		switch events[i].Kind {
		case dtu.EventKindShimInvocation:
			invocation = &events[i]
		case dtu.EventKindShimResult:
			result = &events[i]
		}
	}
	require.NotNil(t, invocation)
	require.NotNil(t, result)
	assert.Equal(t, []string{"fetch", "origin", "fix/issue-1-bug-one", "main"}, invocation.Shim.Args)
	assert.Equal(t, []string{"fetch", "origin", "fix/issue-1-bug-one", "main"}, result.Shim.Args)
	require.NotNil(t, result.Shim.ExitCode)
	assert.Equal(t, 0, *result.Shim.ExitCode)
}

func TestExecuteGHIssueViewAppliesScheduledMutationAfterObservationThreshold(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Repositories[0].Labels = append(state.Repositories[0].Labels, dtu.Label{Name: "ready"})
	state.ScheduledMutations = []dtu.ScheduledMutation{{
		Name: "add-ready-after-second-view",
		Trigger: dtu.MutationTrigger{
			Command:    dtu.ShimCommandGH,
			ArgsPrefix: []string{"issue", "view", "1"},
			After:      2,
		},
		Operations: []dtu.MutationOperation{{
			Type:   dtu.MutationOperationIssueAddLabel,
			Repo:   "owner/repo",
			Number: 1,
			Label:  "ready",
		}},
	}}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	runView := func() string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), "gh", []string{"issue", "view", "1", "--repo", "owner/repo", "--json", "labels"}, nil, &stdout, &stderr, env)
		if code != 0 {
			t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
		}
		return stdout.String()
	}

	first := runView()
	if strings.Contains(first, `"ready"`) {
		t.Fatalf("first view output = %s, want no ready label yet", first)
	}

	second := runView()
	if strings.Contains(second, `"ready"`) {
		t.Fatalf("second view output = %s, want no ready label before threshold is exceeded", second)
	}

	third := runView()
	if !strings.Contains(third, `"ready"`) {
		t.Fatalf("third view output = %s, want ready label after scheduled mutation", third)
	}
}

func TestExecuteClaudeRecordsDeterministicDuration(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Clock = dtu.ClockState{Now: "2026-01-02T03:04:05Z"}
	store, stateDir := testStore(t, state)
	env := append(envForStore(store, stateDir, state.UniverseID), envPhase+"=analyze")

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("Please analyze Issue 1")
	code := Execute(context.Background(), "claude", []string{"-p", "--max-turns", "3", "--model", "claude-sonnet", "--allowedTools", "Read", "--append-system-prompt", "HARNESS"}, stdin, &stdout, &stderr, env)
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var result *dtu.Event
	for i := range events {
		if events[i].Kind == dtu.EventKindShimResult && events[i].Shim != nil && events[i].Shim.Command == "claude" {
			result = &events[i]
			break
		}
	}
	if result == nil {
		t.Fatalf("missing claude shim result event: %#v", events)
		return
	}
	if got, want := result.RecordedAt, "2026-01-02T03:04:05Z"; got != want {
		t.Fatalf("RecordedAt = %q, want %q", got, want)
	}
	if got, want := result.Shim.Duration, "0s"; got != want {
		t.Fatalf("Duration = %q, want %q", got, want)
	}
}

func TestExecuteClaudeRecordsRichShimMetadata(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Providers.Scripts = []dtu.ProviderScript{{
		Name:     "stdin-script",
		Provider: dtu.ProviderClaude,
		Match:    dtu.ProviderScriptMatch{PromptExact: "Please analyze via stdin"},
		Stdout:   "analysis complete",
		Stderr:   "warning\n",
	}}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	binaryPath := filepath.Join(workingDir, "claude")
	prompt := "Please analyze via stdin"
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), binaryPath, []string{"-p", "--max-turns", "3", "--model", "claude-sonnet", "--allowedTools", "Read"}, strings.NewReader(prompt), &stdout, &stderr, env)
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var invocation, result *dtu.Event
	for i := range events {
		if events[i].Shim == nil || events[i].Shim.Command != "claude" {
			continue
		}
		switch events[i].Kind {
		case dtu.EventKindShimInvocation:
			invocation = &events[i]
		case dtu.EventKindShimResult:
			result = &events[i]
		}
	}
	if invocation == nil || result == nil {
		t.Fatalf("missing claude shim events: %#v", events)
		return
	}
	if got, want := invocation.Shim.BinaryPath, binaryPath; got != want {
		t.Fatalf("BinaryPath = %q, want %q", got, want)
	}
	if got, want := invocation.Shim.BinaryName, "claude"; got != want {
		t.Fatalf("BinaryName = %q, want %q", got, want)
	}
	if got, want := invocation.Shim.WorkingDir, workingDir; got != want {
		t.Fatalf("WorkingDir = %q, want %q", got, want)
	}
	if got, want := invocation.Shim.StdinDigest, hashPrompt(prompt); got != want {
		t.Fatalf("StdinDigest = %q, want %q", got, want)
	}
	if got, want := result.Shim.StdoutBytes, len(stdout.String()); got != want {
		t.Fatalf("StdoutBytes = %d, want %d", got, want)
	}
	if got, want := result.Shim.StderrBytes, len(stderr.String()); got != want {
		t.Fatalf("StderrBytes = %d, want %d", got, want)
	}
}

func TestExecuteClaudeFallsBackWhenNamedScriptExistsOnlyInOtherScenario(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Metadata.Scenario = "issue workflow"
	state.Providers.Scripts = []dtu.ProviderScript{{
		Name:     "review",
		Provider: dtu.ProviderClaude,
		Match:    dtu.ProviderScriptMatch{Scenario: "merge workflow"},
		Stdout:   "wrong scenario",
	}, {
		Name:     "review-response",
		Provider: dtu.ProviderClaude,
		Match:    dtu.ProviderScriptMatch{Scenario: "issue workflow", Phase: "review"},
		Stdout:   "issue scenario",
	}}
	store, stateDir := testStore(t, state)
	env := append(envForStore(store, stateDir, state.UniverseID), envPhase+"=review", envScript+"=review")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), "claude", []string{"-p", "Review this issue"}, nil, &stdout, &stderr, env)
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "issue scenario"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestExecuteClaudeProviderNoOpMarkerAppendsDeterministically(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Providers.Scripts = []dtu.ProviderScript{{
		Name:       "noop-script",
		Provider:   dtu.ProviderClaude,
		Match:      dtu.ProviderScriptMatch{PromptExact: "already fixed"},
		Stdout:     "Already fixed.\n",
		NoOpMarker: "XYLEM_NOOP",
	}}
	store, stateDir := testStore(t, state)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), "claude", []string{"-p", "already fixed"}, nil, &stdout, &stderr, envForStore(store, stateDir, state.UniverseID))
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "Already fixed.\n\nXYLEM_NOOP\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestExecuteClaudeDelayAdvancesDTURuntimeClock(t *testing.T) {
	t.Parallel()

	state := sampleState()
	state.Providers.Scripts = []dtu.ProviderScript{{
		Name:     "delayed",
		Provider: dtu.ProviderClaude,
		Match:    dtu.ProviderScriptMatch{PromptExact: "delay please"},
		Delay:    "2s",
		Stdout:   "done",
	}}
	store, stateDir := testStore(t, state)
	env := envForStore(store, stateDir, state.UniverseID)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), "claude", []string{"-p", "delay please"}, nil, &stdout, &stderr, env)
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "done" {
		t.Fatalf("stdout = %q, want done", stdout.String())
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := loaded.Clock.Now, "2026-01-02T03:04:07Z"; got != want {
		t.Fatalf("Clock.Now = %q, want %q", got, want)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var result *dtu.Event
	for i := range events {
		if events[i].Kind == dtu.EventKindShimResult && events[i].Shim != nil && events[i].Shim.Command == "claude" {
			result = &events[i]
			break
		}
	}
	if result == nil {
		t.Fatalf("missing claude shim result event: %#v", events)
		return
	}
	if got, want := result.Shim.Duration, "2s"; got != want {
		t.Fatalf("Duration = %q, want %q", got, want)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func TestDeriveStoreLocation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(string(filepath.Separator), "repo", "state", "dtu", "universe-1", "state.json")
	stateDir, universeID, err := deriveStoreLocation(path)
	if err != nil {
		t.Fatalf("deriveStoreLocation() error = %v", err)
	}
	if stateDir != filepath.Join(string(filepath.Separator), "repo", "state") || universeID != "universe-1" {
		t.Fatalf("deriveStoreLocation() = (%q, %q)", stateDir, universeID)
	}
}

func TestMainPackagesCompile(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	baseDir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	for _, relPath := range []string{"cmd/gh/main.go", "cmd/claude/main.go", "cmd/copilot/main.go"} {
		path := filepath.Join(baseDir, relPath)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}
