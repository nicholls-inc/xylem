package dtu

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeManifestFile(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "universe.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %q", want, err.Error())
	}
}

func TestLoadManifest(t *testing.T) {
	t.Parallel()

	path := writeManifestFile(t, `version: v1
metadata:
  name: sample-universe
  description: Integration fixture
  scenario: issue happy path
  tags: [alpha, smoke]
clock:
  now: 2026-01-02T03:04:05Z
repositories:
  - owner: nicholls-inc
    name: xylem
    default_branch: main
    labels:
      - name: bug
      - name: ready
    branches:
      - name: main
        sha: abc123
    worktrees:
      - path: .xylem/worktrees/issue-1
        branch: issue-1
    issues:
      - number: 1
        title: Fix it
        body: Please fix it
        labels: [bug, ready]
        comments:
          - id: 10
            body: seeded comment
    pull_requests:
      - number: 2
        title: Draft fix
        base_branch: main
        head_branch: fix-2
        head_sha: deadbeef
        labels: [ready]
        reviews:
          - id: 20
            state: APPROVED
        checks:
          - id: 30
            name: test
            state: success
providers:
  scripts:
    - name: analyze-success
      provider: claude
      match:
        phase: analyze
        attempt: 2
      allowed_tools: [Read, Bash]
      stdout: ok
      delay: 1s
shim_faults:
  - name: gh-search-timeout
    command: gh
    match:
      args_prefix: [search, issues]
      attempt: 2
    stderr: rate limited
    exit_code: 1
`)

	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}

	if manifest.Metadata.Name != "sample-universe" {
		t.Fatalf("Metadata.Name = %q, want sample-universe", manifest.Metadata.Name)
	}
	if len(manifest.Repositories) != 1 {
		t.Fatalf("len(Repositories) = %d, want 1", len(manifest.Repositories))
	}
	repo := manifest.Repositories[0]
	if repo.Slug() != "nicholls-inc/xylem" {
		t.Fatalf("Slug() = %q, want nicholls-inc/xylem", repo.Slug())
	}
	if len(repo.Issues) != 1 || repo.Issues[0].Comments[0].ID != 10 {
		t.Fatalf("unexpected issue payload: %#v", repo.Issues)
	}
	if len(manifest.Providers.Scripts) != 1 {
		t.Fatalf("len(Providers.Scripts) = %d, want 1", len(manifest.Providers.Scripts))
	}
	if manifest.Providers.Scripts[0].Provider != ProviderClaude {
		t.Fatalf("Provider = %q, want %q", manifest.Providers.Scripts[0].Provider, ProviderClaude)
	}
	if manifest.Providers.Scripts[0].Match.Attempt != 2 {
		t.Fatalf("Match.Attempt = %d, want 2", manifest.Providers.Scripts[0].Match.Attempt)
	}
	if len(manifest.ShimFaults) != 1 {
		t.Fatalf("len(ShimFaults) = %d, want 1", len(manifest.ShimFaults))
	}
	if manifest.ShimFaults[0].Command != ShimCommandGH {
		t.Fatalf("ShimFault.Command = %q, want %q", manifest.ShimFaults[0].Command, ShimCommandGH)
	}
}

func TestLoadManifestSupportsLegacyProviderScriptsField(t *testing.T) {
	t.Parallel()

	path := writeManifestFile(t, `metadata:
  name: sample-universe
repositories:
  - owner: nicholls-inc
    name: xylem
    default_branch: main
provider_scripts:
  - name: reply
    provider: copilot
    stdout: done
`)

	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(manifest.Providers.Scripts) != 1 {
		t.Fatalf("len(Providers.Scripts) = %d, want 1", len(manifest.Providers.Scripts))
	}
	if manifest.Providers.Scripts[0].Provider != ProviderCopilot {
		t.Fatalf("Provider = %q, want %q", manifest.Providers.Scripts[0].Provider, ProviderCopilot)
	}
}

func TestLoadManifestRejectsInvalidRepositoryState(t *testing.T) {
	t.Parallel()

	path := writeManifestFile(t, `metadata:
  name: bad-universe
repositories:
  - owner: nicholls-inc
    name: xylem
    default_branch: main
    issues:
      - number: 1
        title: Broken
        state: sideways
`)

	_, err := LoadManifest(path)
	requireErrorContains(t, err, `invalid state "sideways"`)
}

func TestNewStateAssignsMissingIDsAndNormalizesOrder(t *testing.T) {
	t.Parallel()

	manifest := &Manifest{
		Metadata: ManifestMetadata{Name: "sample", Tags: []string{"zeta", "alpha", "alpha"}},
		Clock:    ClockState{Now: "2026-01-02T03:04:05+02:00"},
		Repositories: []Repository{
			{
				Owner:         "nicholls-inc",
				Name:          "xylem",
				DefaultBranch: "main",
				Labels:        []Label{{Name: "ready"}, {Name: "bug"}},
				Issues: []Issue{{
					Number:   2,
					Title:    "Issue",
					Labels:   []string{"ready", "bug", "ready"},
					Comments: []Comment{{Body: "auto issue comment"}},
				}},
				PullRequests: []PullRequest{{
					Number:     5,
					Title:      "PR",
					BaseBranch: "main",
					HeadBranch: "feature",
					HeadSHA:    "abc123",
					Labels:     []string{"ready", "ready"},
					Comments:   []Comment{{Body: "auto pr comment"}},
					Reviews:    []Review{{Body: "looks fine"}},
					Checks:     []Check{{Name: "test"}},
				}},
			},
		},
		Providers: Providers{Scripts: []ProviderScript{
			{Name: "b", Provider: ProviderCopilot},
			{Name: "a", Provider: ProviderClaude, Match: ProviderScriptMatch{Attempt: 2}, AllowedTools: []string{"Bash", "Read", "Bash"}},
			{Name: "a", Provider: ProviderClaude, AllowedTools: []string{"Edit", "Read"}},
		}},
		ShimFaults: []ShimFault{
			{
				Name:    "z",
				Command: ShimCommandGH,
				Match:   ShimFaultMatch{ArgsPrefix: []string{" search", "issues ", ""}},
			},
			{
				Name:     "a",
				Command:  ShimCommandGit,
				Match:    ShimFaultMatch{Phase: " analyze ", Script: " mutate ", Attempt: 2, ArgsExact: []string{" status ", " --short "}},
				ExitCode: 0,
			},
		},
	}

	state, err := NewState("universe-1", manifest, "fixtures/universe.yaml", time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}

	repo := state.Repository("nicholls-inc", "xylem")
	if repo == nil {
		t.Fatal("Repository() = nil, want repo")
	}
	if got := repo.Issues[0].Comments[0].ID; got != 1 {
		t.Fatalf("issue comment ID = %d, want 1", got)
	}
	if got := repo.PullRequests[0].Comments[0].ID; got != 2 {
		t.Fatalf("pull request comment ID = %d, want 2", got)
	}
	if got := repo.PullRequests[0].Reviews[0].ID; got != 1 {
		t.Fatalf("review ID = %d, want 1", got)
	}
	if got := repo.PullRequests[0].Checks[0].ID; got != 1 {
		t.Fatalf("check ID = %d, want 1", got)
	}
	if got := repo.Issues[0].State; got != IssueStateOpen {
		t.Fatalf("Issue.State = %q, want %q", got, IssueStateOpen)
	}
	if got := repo.PullRequests[0].State; got != PullRequestStateOpen {
		t.Fatalf("PullRequest.State = %q, want %q", got, PullRequestStateOpen)
	}
	if got := repo.PullRequests[0].Reviews[0].State; got != ReviewStateCommented {
		t.Fatalf("Review.State = %q, want %q", got, ReviewStateCommented)
	}
	if got := repo.PullRequests[0].Checks[0].State; got != CheckStatePending {
		t.Fatalf("Check.State = %q, want %q", got, CheckStatePending)
	}
	if !reflect.DeepEqual(state.Metadata.Tags, []string{"alpha", "zeta"}) {
		t.Fatalf("Metadata.Tags = %#v, want [alpha zeta]", state.Metadata.Tags)
	}
	if !reflect.DeepEqual(repo.Labels, []Label{{Name: "bug"}, {Name: "ready"}}) {
		t.Fatalf("Labels = %#v, want sorted labels", repo.Labels)
	}
	if !reflect.DeepEqual(repo.Issues[0].Labels, []string{"bug", "ready"}) {
		t.Fatalf("Issue labels = %#v, want sorted unique labels", repo.Issues[0].Labels)
	}
	if !reflect.DeepEqual(state.Providers.Scripts[0].AllowedTools, []string{"Edit", "Read"}) {
		t.Fatalf("AllowedTools = %#v, want sorted unique tools", state.Providers.Scripts[0].AllowedTools)
	}
	if state.Providers.Scripts[1].Match.Attempt != 2 {
		t.Fatalf("Providers.Scripts[1].Match.Attempt = %d, want 2", state.Providers.Scripts[1].Match.Attempt)
	}
	if !reflect.DeepEqual(state.Providers.Scripts[1].AllowedTools, []string{"Bash", "Read"}) {
		t.Fatalf("AllowedTools = %#v, want sorted unique tools", state.Providers.Scripts[1].AllowedTools)
	}
	if len(state.ShimFaults) != 2 {
		t.Fatalf("len(ShimFaults) = %d, want 2", len(state.ShimFaults))
	}
	if state.ShimFaults[0].Name != "z" || state.ShimFaults[0].Command != ShimCommandGH {
		t.Fatalf("ShimFaults[0] = %#v, want gh fault first", state.ShimFaults[0])
	}
	if !reflect.DeepEqual(state.ShimFaults[0].Match.ArgsPrefix, []string{"search", "issues"}) {
		t.Fatalf("ShimFaults[0].Match.ArgsPrefix = %#v, want normalized prefix", state.ShimFaults[0].Match.ArgsPrefix)
	}
	if state.ShimFaults[0].ExitCode != 1 {
		t.Fatalf("ShimFaults[0].ExitCode = %d, want default 1", state.ShimFaults[0].ExitCode)
	}
	if state.ShimFaults[1].Name != "a" || state.ShimFaults[1].Command != ShimCommandGit {
		t.Fatalf("ShimFaults[1] = %#v, want git fault second", state.ShimFaults[1])
	}
	if !reflect.DeepEqual(state.ShimFaults[1].Match.ArgsExact, []string{"status", "--short"}) {
		t.Fatalf("ShimFaults[1].Match.ArgsExact = %#v, want normalized args", state.ShimFaults[1].Match.ArgsExact)
	}
	if state.ShimFaults[1].Match.Phase != "analyze" || state.ShimFaults[1].Match.Script != "mutate" {
		t.Fatalf("ShimFaults[1].Match = %#v, want trimmed phase/script", state.ShimFaults[1].Match)
	}
	if state.ShimFaults[1].ExitCode != 1 {
		t.Fatalf("ShimFaults[1].ExitCode = %d, want default 1", state.ShimFaults[1].ExitCode)
	}
	if state.Clock.Now != "2026-01-02T01:04:05Z" {
		t.Fatalf("Clock.Now = %q, want 2026-01-02T01:04:05Z", state.Clock.Now)
	}
	if state.Counters.NextCommentID != 3 || state.Counters.NextReviewID != 2 || state.Counters.NextCheckID != 2 {
		t.Fatalf("Counters = %#v, want comment=3 review=2 check=2", state.Counters)
	}
}

func TestNewStateAllowsScenarioScopedProviderScripts(t *testing.T) {
	t.Parallel()

	manifest := &Manifest{
		Metadata: ManifestMetadata{
			Name:     "sample",
			Scenario: "issue workflow",
		},
		Providers: Providers{Scripts: []ProviderScript{
			{Name: "reply", Provider: ProviderClaude, Match: ProviderScriptMatch{Scenario: "issue workflow"}},
			{Name: "reply", Provider: ProviderClaude, Match: ProviderScriptMatch{Scenario: "merge workflow"}},
		}},
	}

	state, err := NewState("universe-1", manifest, "fixtures/universe.yaml", time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	if len(state.Providers.Scripts) != 2 {
		t.Fatalf("len(Providers.Scripts) = %d, want 2", len(state.Providers.Scripts))
	}
}

func TestStoreSaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()

	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample", Tags: []string{"beta", "alpha"}},
		Clock:      ClockState{Now: "2026-01-02T03:04:05Z"},
		Repositories: []Repository{{
			Owner:         "nicholls-inc",
			Name:          "xylem",
			DefaultBranch: "main",
			Labels:        []Label{{Name: "ready"}, {Name: "bug"}},
			Issues: []Issue{{
				Number:   1,
				Title:    "Issue",
				Labels:   []string{"ready", "bug"},
				Comments: []Comment{{ID: 5, Body: "hi"}},
			}},
			PullRequests: []PullRequest{{
				Number:     2,
				Title:      "PR",
				BaseBranch: "main",
				HeadBranch: "feature",
				HeadSHA:    "abc123",
				Checks:     []Check{{ID: 9, Name: "ci", State: CheckStateSuccess}},
			}},
		}},
		Providers: Providers{Scripts: []ProviderScript{{Name: "reply", Provider: ProviderClaude, Match: ProviderScriptMatch{Attempt: 2}, AllowedTools: []string{"Read", "Bash"}}}},
		ShimFaults: []ShimFault{{
			Name:     "gh-bad-gateway",
			Command:  ShimCommandGH,
			Match:    ShimFaultMatch{ArgsPrefix: []string{"api"}, Attempt: 2},
			Stderr:   "bad gateway",
			ExitCode: 1,
		}},
		Counters: Counters{NextCommentID: 6, NextReviewID: 1, NextCheckID: 10},
	}

	stateDir := t.TempDir()
	store, err := NewStore(stateDir, "universe-1")
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.UniverseID != state.UniverseID {
		t.Fatalf("UniverseID = %q, want %q", loaded.UniverseID, state.UniverseID)
	}
	if loaded.Repository("nicholls-inc", "xylem") == nil {
		t.Fatal("Repository() = nil, want repo")
	}
	if loaded.ProviderScript(ProviderClaude, "reply") == nil {
		t.Fatal("ProviderScript() = nil, want script")
	}
	if loaded.ShimFault(ShimCommandGH, "gh-bad-gateway") == nil {
		t.Fatal("ShimFault() = nil, want fault")
	}
	if loaded.Counters.NextCheckID != 10 {
		t.Fatalf("NextCheckID = %d, want 10", loaded.Counters.NextCheckID)
	}

	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(data)
	if strings.Index(text, `"bug"`) > strings.Index(text, `"ready"`) {
		t.Fatalf("state file labels are not normalized: %s", text)
	}
}

func TestNewStateUsesManifestClockWhenProvided(t *testing.T) {
	t.Parallel()

	manifest := &Manifest{
		Metadata: ManifestMetadata{Name: "sample"},
		Clock:    ClockState{Now: "2026-01-02T03:04:05+02:00"},
		Repositories: []Repository{{
			Owner:         "nicholls-inc",
			Name:          "xylem",
			DefaultBranch: "main",
		}},
	}

	state, err := NewState("universe-1", manifest, "fixtures/universe.yaml", time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	if got, want := state.Clock.Now, "2026-01-02T01:04:05Z"; got != want {
		t.Fatalf("Clock.Now = %q, want %q", got, want)
	}
}

func TestRepositoryMergePullRequestRetainsHeadBranchWhenNotDeleting(t *testing.T) {
	t.Parallel()

	repo := &Repository{
		Owner:         "owner",
		Name:          "repo",
		DefaultBranch: "main",
		Branches: []Branch{
			{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		},
		PullRequests: []PullRequest{{
			Number:     7,
			Title:      "Merge me",
			BaseBranch: "main",
			HeadBranch: "feature/merge-me",
			HeadSHA:    "deadbeefcafebabe",
		}},
	}

	if err := repo.MergePullRequest(7, MergePullRequestOptions{}); err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}

	pr := repo.PullRequestByNumber(7)
	if pr == nil {
		t.Fatal("PullRequestByNumber() = nil")
	}
	if pr.State != PullRequestStateMerged || !pr.Merged {
		t.Fatalf("pull request = %#v, want merged state", pr)
	}
	if branch := repo.BranchByName("main"); branch == nil || branch.SHA != pr.HeadSHA {
		t.Fatalf("main branch = %#v, want SHA %q", branch, pr.HeadSHA)
	}
	if branch := repo.BranchByName(pr.HeadBranch); branch == nil || branch.SHA != pr.HeadSHA {
		t.Fatalf("head branch = %#v, want SHA %q", branch, pr.HeadSHA)
	}
}

func TestRepositoryMergePullRequestDeletesHeadBranchAndUpsertsBase(t *testing.T) {
	t.Parallel()

	repo := &Repository{
		Owner:         "owner",
		Name:          "repo",
		DefaultBranch: "main",
		Branches: []Branch{
			{Name: "feature/merge-me", SHA: "deadbeefcafebabe"},
			{Name: "keep-me", SHA: "2222222222222222222222222222222222222222"},
		},
		PullRequests: []PullRequest{{
			Number:     8,
			Title:      "Delete source branch",
			BaseBranch: "main",
			HeadBranch: "feature/merge-me",
			HeadSHA:    "feedfacecafebeef",
		}},
	}

	if err := repo.MergePullRequest(8, MergePullRequestOptions{DeleteHeadBranch: true}); err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}

	pr := repo.PullRequestByNumber(8)
	if pr == nil {
		t.Fatal("PullRequestByNumber() = nil")
	}
	if pr.State != PullRequestStateMerged || !pr.Merged {
		t.Fatalf("pull request = %#v, want merged state", pr)
	}
	if branch := repo.BranchByName("main"); branch == nil || branch.SHA != pr.HeadSHA {
		t.Fatalf("main branch = %#v, want SHA %q", branch, pr.HeadSHA)
	}
	if branch := repo.BranchByName(pr.HeadBranch); branch != nil {
		t.Fatalf("head branch = %#v, want deleted", branch)
	}
	if branch := repo.BranchByName("keep-me"); branch == nil {
		t.Fatal("unrelated branch was removed")
	}
}

func TestRepositoryMergePullRequestQueuesAutoMergeUntilChecksPass(t *testing.T) {
	t.Parallel()

	repo := &Repository{
		Owner:         "owner",
		Name:          "repo",
		DefaultBranch: "main",
		Branches: []Branch{
			{Name: "main", SHA: "1111111111111111111111111111111111111111"},
			{Name: "feature/merge-me", SHA: "deadbeefcafebabe"},
		},
		PullRequests: []PullRequest{{
			Number:     9,
			Title:      "Queue me",
			State:      PullRequestStateOpen,
			BaseBranch: "main",
			HeadBranch: "feature/merge-me",
			HeadSHA:    "feedfacecafebeef",
			Checks:     []Check{{ID: 1, Name: "ci", State: CheckStatePending}},
		}},
	}

	if err := repo.MergePullRequest(9, MergePullRequestOptions{DeleteHeadBranch: true, AutoMerge: true}); err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}

	pr := repo.PullRequestByNumber(9)
	if pr == nil {
		t.Fatal("PullRequestByNumber() = nil")
	}
	if pr.State != PullRequestStateOpen || pr.Merged {
		t.Fatalf("pull request = %#v, want open queued auto-merge", pr)
	}
	if !pr.AutoMergeEnabled || !pr.AutoMergeDeleteBranch {
		t.Fatalf("pull request auto-merge flags = %#v, want enabled delete-branch queue", pr)
	}
	if branch := repo.BranchByName("main"); branch == nil || branch.SHA != "1111111111111111111111111111111111111111" {
		t.Fatalf("main branch = %#v, want original SHA preserved", branch)
	}
	if branch := repo.BranchByName(pr.HeadBranch); branch == nil {
		t.Fatalf("head branch = %#v, want branch retained while auto-merge is queued", branch)
	}

	pr.Checks[0].State = CheckStateSuccess
	if err := repo.ApplyQueuedAutoMerge(9); err != nil {
		t.Fatalf("ApplyQueuedAutoMerge() error = %v", err)
	}
	if pr.State != PullRequestStateMerged || !pr.Merged {
		t.Fatalf("pull request after check success = %#v, want merged state", pr)
	}
	if pr.AutoMergeEnabled || pr.AutoMergeDeleteBranch {
		t.Fatalf("pull request auto-merge flags after merge = %#v, want cleared", pr)
	}
	if branch := repo.BranchByName("main"); branch == nil || branch.SHA != pr.HeadSHA {
		t.Fatalf("main branch = %#v, want SHA %q", branch, pr.HeadSHA)
	}
	if branch := repo.BranchByName(pr.HeadBranch); branch != nil {
		t.Fatalf("head branch = %#v, want deleted after queued auto-merge", branch)
	}
}

func TestStoreRecordObservationPRCheckMutationAppliesQueuedAutoMerge(t *testing.T) {
	t.Parallel()

	appliedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample"},
		Clock:      ClockState{Now: appliedAt.Format(time.RFC3339)},
		Repositories: []Repository{{
			Owner:         "owner",
			Name:          "repo",
			DefaultBranch: "main",
			Branches: []Branch{
				{Name: "main", SHA: "1111111111111111111111111111111111111111"},
				{Name: "feature/merge-me", SHA: "deadbeefcafebabe"},
			},
			PullRequests: []PullRequest{{
				Number:                12,
				Title:                 "Queue me",
				State:                 PullRequestStateOpen,
				BaseBranch:            "main",
				HeadBranch:            "feature/merge-me",
				HeadSHA:               "feedfacecafebeef",
				AutoMergeEnabled:      true,
				AutoMergeDeleteBranch: true,
				Checks:                []Check{{ID: 1, Name: "ci", State: CheckStatePending}},
			}},
		}},
		ScheduledMutations: []ScheduledMutation{{
			Name: "complete-check-and-merge",
			Trigger: MutationTrigger{
				Command:    ShimCommandGH,
				ArgsPrefix: []string{"pr", "checks", "12"},
			},
			Operations: []MutationOperation{{
				Type:   MutationOperationPRSetCheckState,
				Repo:   "owner/repo",
				Number: 12,
				Check:  "ci",
				State:  string(CheckStateSuccess),
			}},
		}},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 2},
	}

	store, err := NewStoreWithClock(t.TempDir(), state.UniverseID, NewFixedClock(appliedAt))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	result, err := store.RecordObservation(ShimInvocation{
		Command: ShimCommandGH,
		Args:    []string{"pr", "checks", "12", "--repo", "owner/repo", "--json", "name,state"},
	})
	if err != nil {
		t.Fatalf("RecordObservation() error = %v", err)
	}
	if len(result.Applied) != 1 || result.Applied[0].Name != "complete-check-and-merge" {
		t.Fatalf("Applied = %#v, want queued auto-merge mutation", result.Applied)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loadedRepo := loaded.Repository("owner", "repo")
	if loadedRepo == nil {
		t.Fatal("Repository() = nil")
	}
	pr := loadedRepo.PullRequestByNumber(12)
	if pr == nil {
		t.Fatal("PullRequestByNumber() = nil")
	}
	if pr.State != PullRequestStateMerged || !pr.Merged {
		t.Fatalf("pull request = %#v, want merged state", pr)
	}
	if pr.AutoMergeEnabled || pr.AutoMergeDeleteBranch {
		t.Fatalf("pull request auto-merge flags = %#v, want cleared after merge", pr)
	}
	if branch := loadedRepo.BranchByName("main"); branch == nil || branch.SHA != pr.HeadSHA {
		t.Fatalf("main branch = %#v, want SHA %q", branch, pr.HeadSHA)
	}
	if branch := loadedRepo.BranchByName(pr.HeadBranch); branch != nil {
		t.Fatalf("head branch = %#v, want deleted after queued auto-merge", branch)
	}
}

func TestStoreRejectsUnsafeUniverseID(t *testing.T) {
	t.Parallel()

	_, err := NewStore(t.TempDir(), "../escape")
	requireErrorContains(t, err, "invalid universe ID")
}

func TestSplitRepoSlug(t *testing.T) {
	t.Parallel()

	owner, name, err := SplitRepoSlug("nicholls-inc/xylem")
	if err != nil {
		t.Fatalf("SplitRepoSlug() error = %v", err)
	}
	if owner != "nicholls-inc" || name != "xylem" {
		t.Fatalf("SplitRepoSlug() = (%q, %q), want (nicholls-inc, xylem)", owner, name)
	}
}

func TestManifestValidationRejectsInvalidProviderAttempt(t *testing.T) {
	t.Parallel()

	manifest := &Manifest{
		Metadata: ManifestMetadata{Name: "sample"},
		Repositories: []Repository{{
			Owner:         "nicholls-inc",
			Name:          "xylem",
			DefaultBranch: "main",
		}},
		Providers: Providers{Scripts: []ProviderScript{{
			Name:     "reply",
			Provider: ProviderClaude,
			Match:    ProviderScriptMatch{Attempt: -1},
		}}},
	}

	err := manifest.Validate()
	requireErrorContains(t, err, "attempt must be greater than or equal to 0")
}

func TestManifestValidationRejectsInvalidShimFault(t *testing.T) {
	t.Parallel()

	manifest := &Manifest{
		Metadata: ManifestMetadata{Name: "sample"},
		Repositories: []Repository{{
			Owner:         "nicholls-inc",
			Name:          "xylem",
			DefaultBranch: "main",
		}},
		ShimFaults: []ShimFault{{
			Name:    "gh-bad",
			Command: ShimCommandGH,
			Match: ShimFaultMatch{
				ArgsExact:  []string{"search", "issues"},
				ArgsPrefix: []string{"search"},
			},
		}},
	}

	err := manifest.Validate()
	requireErrorContains(t, err, "args_exact and args_prefix are mutually exclusive")
}

func TestLoadManifestRejectsInvalidScheduledMutation(t *testing.T) {
	t.Parallel()

	path := writeManifestFile(t, `metadata:
  name: bad-mutation
repositories:
  - owner: nicholls-inc
    name: xylem
    default_branch: main
scheduled_mutations:
  - name: bad-trigger
    trigger:
      command: gh
      args_exact: [issue, view, "1"]
      args_prefix: [issue, view]
    operations:
      - type: issue_add_label
        repo: nicholls-inc/xylem
        number: 1
        label: ready
`)

	_, err := LoadManifest(path)
	requireErrorContains(t, err, "trigger args_exact and args_prefix are mutually exclusive")
}

func TestStoreRecordObservationAppliesScheduledMutationAfterThreshold(t *testing.T) {
	t.Parallel()

	appliedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	state := &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample"},
		Clock:      ClockState{Now: appliedAt.Format(time.RFC3339)},
		Repositories: []Repository{{
			Owner:         "owner",
			Name:          "repo",
			DefaultBranch: "main",
			Labels:        []Label{{Name: "bug"}, {Name: "ready"}},
			Issues: []Issue{{
				Number: 1,
				Title:  "Bug",
				State:  IssueStateOpen,
				Labels: []string{"bug"},
			}},
		}},
		ScheduledMutations: []ScheduledMutation{{
			Name: "label-ready-after-second-view",
			Trigger: MutationTrigger{
				Command:    ShimCommandGH,
				ArgsPrefix: []string{"issue", "view", "1"},
				After:      2,
			},
			Operations: []MutationOperation{{
				Type:   MutationOperationIssueAddLabel,
				Repo:   "owner/repo",
				Number: 1,
				Label:  "ready",
			}},
		}},
		Counters: Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}

	store, err := NewStoreWithClock(t.TempDir(), state.UniverseID, NewFixedClock(appliedAt))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	inv := ShimInvocation{
		Command: ShimCommandGH,
		Args:    []string{"issue", "view", "1", "--repo", "owner/repo", "--json", "labels"},
	}

	first, err := store.RecordObservation(inv)
	if err != nil {
		t.Fatalf("RecordObservation(first) error = %v", err)
	}
	if first.ObservationCount != 1 {
		t.Fatalf("first ObservationCount = %d, want 1", first.ObservationCount)
	}
	if len(first.Applied) != 0 {
		t.Fatalf("first Applied = %#v, want none", first.Applied)
	}

	second, err := store.RecordObservation(inv)
	if err != nil {
		t.Fatalf("RecordObservation(second) error = %v", err)
	}
	if second.ObservationCount != 2 {
		t.Fatalf("second ObservationCount = %d, want 2", second.ObservationCount)
	}
	if len(second.Applied) != 0 {
		t.Fatalf("second Applied = %#v, want no mutation before threshold is exceeded", second.Applied)
	}

	third, err := store.RecordObservation(inv)
	if err != nil {
		t.Fatalf("RecordObservation(third) error = %v", err)
	}
	if third.ObservationCount != 3 {
		t.Fatalf("third ObservationCount = %d, want 3", third.ObservationCount)
	}
	if len(third.Applied) != 1 || third.Applied[0].Name != "label-ready-after-second-view" {
		t.Fatalf("third Applied = %#v, want scheduled mutation", third.Applied)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issue := loaded.Repository("owner", "repo").IssueByNumber(1)
	if issue == nil {
		t.Fatal("IssueByNumber() = nil")
	}
	if !reflect.DeepEqual(issue.Labels, []string{"bug", "ready"}) {
		t.Fatalf("issue.Labels = %#v, want [bug ready]", issue.Labels)
	}
	if len(loaded.Runtime.AppliedMutations) != 1 {
		t.Fatalf("AppliedMutations = %#v, want one entry", loaded.Runtime.AppliedMutations)
	}
	if got, want := loaded.Runtime.AppliedMutations[0].AppliedAt, appliedAt.Format(time.RFC3339Nano); got != want {
		t.Fatalf("AppliedAt = %q, want %q", got, want)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var (
		observedCount int
		appliedEvent  *Event
	)
	for i := range events {
		switch events[i].Kind {
		case EventKindSchedulerObserved:
			observedCount++
			if events[i].Scheduler == nil {
				t.Fatalf("events[%d].Scheduler = nil", i)
			}
			if observedCount == 3 {
				if got, want := events[i].Scheduler.ObservationCount, 3; got != want {
					t.Fatalf("events[%d].Scheduler.ObservationCount = %d, want %d", i, got, want)
				}
				if !reflect.DeepEqual(events[i].Scheduler.AppliedMutations, []string{"label-ready-after-second-view"}) {
					t.Fatalf("events[%d].Scheduler.AppliedMutations = %#v, want applied mutation name", i, events[i].Scheduler.AppliedMutations)
				}
			}
		case EventKindSchedulerMutationApplied:
			appliedEvent = &events[i]
		}
	}
	if observedCount != 3 {
		t.Fatalf("scheduler observed events = %d, want 3", observedCount)
	}
	if appliedEvent == nil || appliedEvent.Scheduler == nil {
		t.Fatalf("mutation applied event missing from %#v", events)
	}
	if got, want := appliedEvent.Scheduler.MutationName, "label-ready-after-second-view"; got != want {
		t.Fatalf("Scheduler.MutationName = %q, want %q", got, want)
	}
	if got, want := appliedEvent.Scheduler.TriggerAfter, 2; got != want {
		t.Fatalf("Scheduler.TriggerAfter = %d, want %d", got, want)
	}
	if got, want := appliedEvent.Scheduler.AppliedAt, appliedAt.Format(time.RFC3339Nano); got != want {
		t.Fatalf("Scheduler.AppliedAt = %q, want %q", got, want)
	}
	if len(appliedEvent.Scheduler.Operations) != 1 || appliedEvent.Scheduler.Operations[0].Label != "ready" {
		t.Fatalf("Scheduler.Operations = %#v, want ready label operation", appliedEvent.Scheduler.Operations)
	}
}

func TestStoreRecordObservationPreservesArgumentOrderInObservationKey(t *testing.T) {
	t.Parallel()

	first := ObservationKey(ShimInvocation{
		Command: ShimCommandGH,
		Args:    []string{"pr", "list", "--repo", "owner/repo", "--search", "head:fix/1"},
	})
	second := ObservationKey(ShimInvocation{
		Command: ShimCommandGH,
		Args:    []string{"pr", "list", "--search", "head:fix/1", "--repo", "owner/repo"},
	})

	if first == second {
		t.Fatalf("ObservationKey() should preserve arg order, got identical keys %q", first)
	}
}
