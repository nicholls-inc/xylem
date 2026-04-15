package source

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasMergedPRTrue(t *testing.T) {
	r := newMock()

	prs := []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}{
		{Number: 42, HeadRefName: "fix/issue-7-some-slug"},
	}
	out, _ := json.Marshal(prs)
	r.set(out, "gh", "pr", "list",
		"--repo", "owner/repo",
		"--search", "head:fix/issue-7-",
		"--state", "merged",
		"--json", "number,headRefName",
		"--limit", "5")

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if !g.hasMergedPR(context.Background(), 7) {
		t.Error("expected hasMergedPR to return true when a merged PR exists")
	}
}

func TestHasMergedPRFalse(t *testing.T) {
	r := newMock()
	// Default mock returns "[]" for unregistered keys, so no merged PRs.

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if g.hasMergedPR(context.Background(), 99) {
		t.Error("expected hasMergedPR to return false when no merged PR exists")
	}
}

func TestHasMergedPRWrongBranch(t *testing.T) {
	r := newMock()

	// PR exists but branch prefix doesn't match issue number pattern
	prs := []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}{
		{Number: 50, HeadRefName: "unrelated/branch-name"},
	}
	out, _ := json.Marshal(prs)
	r.set(out, "gh", "pr", "list",
		"--repo", "owner/repo",
		"--search", "head:fix/issue-7-",
		"--state", "merged",
		"--json", "number,headRefName",
		"--limit", "5")

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if g.hasMergedPR(context.Background(), 7) {
		t.Error("expected hasMergedPR to return false when branch prefix doesn't match")
	}
}

func TestHasMergedPRGHError(t *testing.T) {
	r := newMock()
	// Simulate gh CLI error for all branch prefixes
	for _, prefix := range branchPrefixes {
		r.setErr(fmt.Errorf("network error"), "gh", "pr", "list",
			"--repo", "owner/repo",
			"--search", fmt.Sprintf("head:%s/issue-5-", prefix),
			"--state", "merged",
			"--json", "number,headRefName",
			"--limit", "5")
	}

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if g.hasMergedPR(context.Background(), 5) {
		t.Error("expected hasMergedPR to return false on gh error")
	}
}

func TestHasMergedPRFeatPrefix(t *testing.T) {
	r := newMock()

	// No fix/ match, but feat/ has a match
	prs := []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}{
		{Number: 99, HeadRefName: "feat/issue-3-add-feature"},
	}
	out, _ := json.Marshal(prs)
	r.set(out, "gh", "pr", "list",
		"--repo", "owner/repo",
		"--search", "head:feat/issue-3-",
		"--state", "merged",
		"--json", "number,headRefName",
		"--limit", "5")

	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	if !g.hasMergedPR(context.Background(), 3) {
		t.Error("expected hasMergedPR to return true for feat/ prefix match")
	}
}

func TestScanSkipsMergedPR(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{
			Number: 7,
			Title:  "test issue",
			Body:   "body",
			URL:    "https://github.com/owner/repo/issues/7",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "bug"}},
		},
	}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	// Set up merged PR match for fix/issue-7-
	prs := []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}{
		{Number: 42, HeadRefName: "fix/issue-7-test-issue"},
	}
	prOut, _ := json.Marshal(prs)
	r.set(prOut, "gh", "pr", "list",
		"--repo", "owner/repo",
		"--search", "head:fix/issue-7-",
		"--state", "merged",
		"--json", "number,headRefName",
		"--limit", "5")

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels (merged PR exists), got %d", len(vessels))
	}
}

func TestSmoke_S2_GitHubScanAutoRetriesEligibleTransientFailure(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "xylem-failed"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "temporary failure from upstream 503"))

	artifact := recovery.Build(recovery.Input{
		VesselID:  "issue-42",
		Source:    "github-issue",
		Workflow:  "fix-bug",
		Ref:       issues[0].URL,
		State:     queue.StateFailed,
		Error:     "temporary failure from upstream 503",
		CreatedAt: time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown),
	})
	require.NoError(t, recovery.Save(dir, artifact))

	g := &GitHub{
		Repo: "owner/repo",
		Tasks: map[string]GitHubTask{"fix": {
			Labels:       []string{"bug"},
			Workflow:     "fix-bug",
			StatusLabels: &StatusLabels{Failed: "xylem-failed"},
		}},
		Exclude:   []string{"xylem-failed"},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, "issue-42", vessels[0].RetryOf)
	assert.Equal(t, "cooldown", vessels[0].Meta[recovery.MetaUnlockedBy])
	assert.Equal(t, "1", vessels[0].Meta[recovery.MetaRetryCount])
}

func TestSmoke_S2b_GitHubScanRetriesAfterRecoveryRefreshChangesDecisionDigest(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 158,
		Title:  "missing worktree panic",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/158",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("missing worktree panic", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-158-fresh-retry-1",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "158",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-158-fresh-retry-1", queue.StateFailed, "panic: chdir missing worktree"))

	blocked := &recovery.Artifact{
		SchemaVersion:           "v1",
		VesselID:                "issue-158-fresh-retry-1",
		State:                   string(queue.StateFailed),
		FailureFingerprint:      "fail-worktree-missing",
		RecoveryClass:           recovery.ClassUnknown,
		Confidence:              0.79,
		RecoveryAction:          recovery.ActionHumanEscalation,
		DecisionSource:          recovery.DecisionSourceDiagnosis,
		Rationale:               "needs human review",
		EvidencePaths:           []string{"phases/issue-158-fresh-retry-1/summary.json"},
		RetryPreconditions:      []string{"Refresh the recovery decision after a human reviews the cited artifacts."},
		RetrySuppressed:         true,
		RetryOutcome:            "suppressed",
		RequiresDecisionRefresh: true,
		SourceInputFP:           fingerprint,
		HarnessDigest:           "har-same",
		WorkflowDigest:          "wf-same",
		RemediationEpoch:        "1",
		CreatedAt:               time.Now().UTC().Add(-time.Hour),
	}
	blocked.DecisionDigest = recovery.DecisionDigest(blocked)
	blocked.RemediationFP = recovery.ComputeRemediationFingerprint(recovery.RemediationState{
		SourceInputFP:    blocked.SourceInputFP,
		HarnessDigest:    blocked.HarnessDigest,
		WorkflowDigest:   blocked.WorkflowDigest,
		DecisionDigest:   blocked.DecisionDigest,
		RemediationEpoch: blocked.RemediationEpoch,
	})
	require.NoError(t, recovery.Save(dir, blocked))

	failed, err := q.FindByID(blocked.VesselID)
	require.NoError(t, err)
	require.NotNil(t, failed)
	failed.Meta = recovery.ApplyToMeta(failed.Meta, blocked)
	require.NoError(t, q.UpdateVessel(*failed))

	_, err = recovery.RefreshRetryDecisionForVessel(dir, blocked.VesselID, recovery.RefreshOptions{
		ReviewedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	g := &GitHub{
		Repo: "owner/repo",
		Tasks: map[string]GitHubTask{"fix": {
			Labels:   []string{"bug"},
			Workflow: "fix-bug",
		}},
		StateDir:              dir,
		Queue:                 q,
		CmdRunner:             r,
		HarnessDigestResolver: func() string { return "har-same" },
		WorkflowDigestResolver: func(string) string {
			return "wf-same"
		},
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "issue-158-fresh-retry-1-retry-1", vessels[0].ID)
	assert.Equal(t, blocked.VesselID, vessels[0].RetryOf)
	assert.Equal(t, "decision", vessels[0].Meta[recovery.MetaUnlockedBy])
	assert.Equal(t, string(recovery.ActionRetry), vessels[0].Meta[recovery.MetaAction])
}

func TestSmoke_S2_GitHubScanAutoRetriesEligibleTransientFailureIgnoresStaleBranchWithoutPR(t *testing.T) {
	t.Parallel()

	type repoState struct {
		retrying         bool
		priorState       queue.VesselState
		hasBranch        bool
		hasOpenPR        bool
		hasMergedPR      bool
		wantRetry        bool
		wantBranchLookup bool
	}

	issue := ghIssue{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}

	tests := []struct {
		name  string
		state repoState
	}{
		{
			name: "failed retry ignores stale branch without pr",
			state: repoState{
				retrying:   true,
				priorState: queue.StateFailed,
				hasBranch:  true,
				wantRetry:  true,
			},
		},
		{
			name: "timed out retry ignores stale branch without pr",
			state: repoState{
				retrying:   true,
				priorState: queue.StateTimedOut,
				hasBranch:  true,
				wantRetry:  true,
			},
		},
		{
			name: "retry remains blocked by open pr",
			state: repoState{
				retrying:   true,
				priorState: queue.StateFailed,
				hasBranch:  true,
				hasOpenPR:  true,
			},
		},
		{
			name: "retry remains blocked by merged pr",
			state: repoState{
				retrying:    true,
				priorState:  queue.StateTimedOut,
				hasBranch:   true,
				hasMergedPR: true,
			},
		},
		{
			name: "fresh issue stays blocked by existing branch",
			state: repoState{
				hasBranch:        true,
				wantBranchLookup: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			q := queue.New(filepath.Join(dir, "queue.jsonl"))
			r := newMock()

			issueBytes, _ := json.Marshal([]ghIssue{issue})
			r.set(issueBytes, "gh", "issue", "list",
				"--repo", "owner/repo",
				"--state", "open",
				"--json", "number,title,body,url,labels",
				"--limit", "20",
				"--label", "bug")

			if tt.state.hasBranch {
				r.set([]byte("abc123\trefs/heads/fix/issue-42-flaky-dependency\n"),
					"git", "ls-remote", "--heads", "origin", "fix/issue-42-*")
			}
			if tt.state.hasOpenPR {
				openPRs, _ := json.Marshal([]struct {
					Number      int    `json:"number"`
					HeadRefName string `json:"headRefName"`
				}{
					{Number: 43, HeadRefName: "fix/issue-42-flaky-dependency"},
				})
				r.set(openPRs, "gh", "pr", "list",
					"--repo", "owner/repo",
					"--search", "head:fix/issue-42-",
					"--state", "open",
					"--json", "number,headRefName",
					"--limit", "5")
			}
			if tt.state.hasMergedPR {
				mergedPRs, _ := json.Marshal([]struct {
					Number      int    `json:"number"`
					HeadRefName string `json:"headRefName"`
				}{
					{Number: 44, HeadRefName: "fix/issue-42-flaky-dependency"},
				})
				r.set(mergedPRs, "gh", "pr", "list",
					"--repo", "owner/repo",
					"--search", "head:fix/issue-42-",
					"--state", "merged",
					"--json", "number,headRefName",
					"--limit", "5")
			}

			g := &GitHub{
				Repo:      "owner/repo",
				Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
				Queue:     q,
				CmdRunner: r,
			}

			if tt.state.retrying {
				fingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
				_, err := q.Enqueue(queue.Vessel{
					ID:       "issue-42",
					Source:   "github-issue",
					Ref:      issue.URL,
					Workflow: "fix-bug",
					Meta: map[string]string{
						"issue_num":                "42",
						"source_input_fingerprint": fingerprint,
					},
					State:     queue.StatePending,
					CreatedAt: time.Now().UTC(),
				})
				require.NoError(t, err)
				_, err = q.Dequeue()
				require.NoError(t, err)
				require.NoError(t, q.Update("issue-42", tt.state.priorState, "temporary failure from upstream 503"))

				artifact := recovery.Build(recovery.Input{
					VesselID:  "issue-42",
					Source:    "github-issue",
					Workflow:  "fix-bug",
					Ref:       issue.URL,
					State:     tt.state.priorState,
					Error:     "temporary failure from upstream 503",
					CreatedAt: time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown),
				})
				require.NoError(t, recovery.Save(dir, artifact))
				g.StateDir = dir
			}

			vessels, err := g.Scan(context.Background())
			require.NoError(t, err)
			branchLookups := 0
			for _, call := range r.calls {
				if len(call) >= 3 && call[0] == "git" && call[1] == "ls-remote" && call[2] == "--heads" {
					branchLookups++
				}
			}
			assert.Equal(t, boolToInt(tt.state.wantBranchLookup), branchLookups)
			if tt.state.wantRetry {
				require.Len(t, vessels, 1)
				assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
				assert.Equal(t, "issue-42", vessels[0].RetryOf)
				assert.Equal(t, "cooldown", vessels[0].Meta[recovery.MetaUnlockedBy])
				return
			}
			assert.Empty(t, vessels)
		})
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestSmoke_S3_GitHubScanBlocksNonTransientRecoveryClasses(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "ambiguous acceptance criteria",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("ambiguous acceptance criteria", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "missing requirement: acceptance criteria are ambiguous"))

	artifact := recovery.Build(recovery.Input{
		VesselID:  "issue-42",
		Source:    "github-issue",
		Workflow:  "fix-bug",
		Ref:       issues[0].URL,
		State:     queue.StateFailed,
		Error:     "missing requirement: acceptance criteria are ambiguous",
		CreatedAt: time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown),
	})
	require.NoError(t, recovery.Save(dir, artifact))
	require.Equal(t, recovery.ActionRefine, artifact.RecoveryAction)

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	assert.Empty(t, vessels)
}

func TestSmoke_S4_GitHubScanRetriesAfterCooldownWhenRecoveryArtifactMissing(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "temporary failure from upstream 503"))

	failed, err := q.FindByID("issue-42")
	require.NoError(t, err)
	endedAt := time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown)
	failed.EndedAt = &endedAt
	require.NoError(t, q.UpdateVessel(*failed))

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, "issue-42", vessels[0].RetryOf)
	assert.Equal(t, "cooldown", vessels[0].Meta[recovery.MetaUnlockedBy])
}

func TestGitHubScanRetriesAfterCooldownWithoutArtifactFallsBackToStartedAt(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "temporary failure from upstream 503"))

	failed, err := q.FindByID("issue-42")
	require.NoError(t, err)
	startedAt := time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown)
	failed.StartedAt = &startedAt
	failed.EndedAt = nil
	require.NoError(t, q.UpdateVessel(*failed))

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, "cooldown", vessels[0].Meta[recovery.MetaUnlockedBy])
}

func TestGitHubScanLegacyRunningLabelDoesNotChangeRetryFingerprint(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "in-progress"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "temporary failure from upstream 503"))

	failed, err := q.FindByID("issue-42")
	require.NoError(t, err)
	endedAt := time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown)
	failed.EndedAt = &endedAt
	require.NoError(t, q.UpdateVessel(*failed))

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, "issue-42", vessels[0].RetryOf)
	assert.Equal(t, "cooldown", vessels[0].Meta[recovery.MetaUnlockedBy])
}

func TestGitHubScanRetriesWhenOnlySourceFingerprintChanges(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "updated body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	oldFingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": oldFingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "temporary failure from upstream 503"))

	artifact := recovery.Build(recovery.Input{
		VesselID: "issue-42",
		Source:   "github-issue",
		Workflow: "fix-bug",
		Ref:      issues[0].URL,
		State:    queue.StateFailed,
		Error:    "temporary failure from upstream 503",
		Meta: map[string]string{
			"source_input_fingerprint": oldFingerprint,
		},
		CreatedAt: time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown),
	})
	artifact.HarnessDigest = "har-same"
	artifact.WorkflowDigest = "wf-same"
	artifact.DecisionDigest = recovery.DecisionDigest(artifact)
	artifact.RemediationEpoch = "0"
	artifact.RemediationFP = recovery.ComputeRemediationFingerprint(recovery.RemediationState{
		SourceInputFP:    oldFingerprint,
		HarnessDigest:    artifact.HarnessDigest,
		WorkflowDigest:   artifact.WorkflowDigest,
		DecisionDigest:   artifact.DecisionDigest,
		RemediationEpoch: artifact.RemediationEpoch,
	})
	require.NoError(t, recovery.Save(dir, artifact))

	g := &GitHub{
		Repo:                   "owner/repo",
		Tasks:                  map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		StateDir:               dir,
		Queue:                  q,
		CmdRunner:              r,
		HarnessDigestResolver:  func() string { return "har-same" },
		WorkflowDigestResolver: func(string) string { return "wf-same" },
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, "issue-42", vessels[0].RetryOf)
	assert.Equal(t, "source", vessels[0].Meta[recovery.MetaUnlockedBy])
}

func TestGitHubScanRetriesWhenOnlyHarnessDigestChanges(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": fingerprint,
			recovery.MetaHarnessDigest: "har-old",
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "temporary failure from upstream 503"))

	artifact := recovery.Build(recovery.Input{
		VesselID: "issue-42",
		Source:   "github-issue",
		Workflow: "fix-bug",
		Ref:      issues[0].URL,
		State:    queue.StateFailed,
		Error:    "temporary failure from upstream 503",
		Meta: map[string]string{
			"source_input_fingerprint":  fingerprint,
			recovery.MetaHarnessDigest:  "har-old",
			recovery.MetaWorkflowDigest: "wf-same",
		},
		CreatedAt: time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown),
	})
	require.NoError(t, recovery.Save(dir, artifact))

	g := &GitHub{
		Repo:                   "owner/repo",
		Tasks:                  map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		StateDir:               dir,
		Queue:                  q,
		CmdRunner:              r,
		HarnessDigestResolver:  func() string { return "har-new" },
		WorkflowDigestResolver: func(string) string { return "wf-same" },
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, "harness", vessels[0].Meta[recovery.MetaUnlockedBy])
}

func TestGitHubScanRetriesWhenOnlyDecisionDigestChanges(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
	storedDecision := "dec-old"
	storedState := recovery.RemediationState{
		SourceInputFP:    fingerprint,
		HarnessDigest:    "har-same",
		WorkflowDigest:   "wf-same",
		DecisionDigest:   storedDecision,
		RemediationEpoch: "0",
	}
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                         "42",
			"source_input_fingerprint":          fingerprint,
			recovery.MetaHarnessDigest:          storedState.HarnessDigest,
			recovery.MetaWorkflowDigest:         storedState.WorkflowDigest,
			recovery.MetaDecisionDigest:         storedState.DecisionDigest,
			recovery.MetaRemediationEpoch:       storedState.RemediationEpoch,
			recovery.MetaRemediationFingerprint: recovery.ComputeRemediationFingerprint(storedState),
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "temporary failure from upstream 503"))

	artifact := recovery.Build(recovery.Input{
		VesselID: "issue-42",
		Source:   "github-issue",
		Workflow: "fix-bug",
		Ref:      issues[0].URL,
		State:    queue.StateFailed,
		Error:    "temporary failure from upstream 503",
		Meta: map[string]string{
			"source_input_fingerprint":  fingerprint,
			recovery.MetaHarnessDigest:  storedState.HarnessDigest,
			recovery.MetaWorkflowDigest: storedState.WorkflowDigest,
		},
		CreatedAt: time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown),
	})
	require.NoError(t, recovery.Save(dir, artifact))
	require.NotEqual(t, storedDecision, recovery.DecisionDigest(artifact))

	g := &GitHub{
		Repo:                   "owner/repo",
		Tasks:                  map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		StateDir:               dir,
		Queue:                  q,
		CmdRunner:              r,
		HarnessDigestResolver:  func() string { return storedState.HarnessDigest },
		WorkflowDigestResolver: func(string) string { return storedState.WorkflowDigest },
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, "decision", vessels[0].Meta[recovery.MetaUnlockedBy])
}

func TestOnFailAppliesLabel(t *testing.T) {
	r := newMock()
	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":            "1",
			"status_label_failed":  "xylem-failed",
			"status_label_running": "in-progress",
		},
	}

	err := g.OnFail(context.Background(), vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label xylem-failed") {
		t.Errorf("expected --add-label xylem-failed in call, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label in-progress") {
		t.Errorf("expected --remove-label in-progress in call, got %q", joined)
	}
}

func TestSmoke_S1_OnFailRoutesSpecGapToNeedsRefinement(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := queue.Vessel{
		ID:     "issue-214",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":            "214",
			"status_label_failed":  "xylem-failed",
			"status_label_running": "in-progress",
			"trigger_label":        "ready-for-work",
			recovery.MetaAction:    string(recovery.ActionRefine),
		},
		State: queue.StateFailed,
	}
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	r := newMock()
	g := &GitHub{
		Repo:      "owner/repo",
		Queue:     q,
		CmdRunner: r,
	}

	require.NoError(t, g.OnFail(context.Background(), queue.Vessel{ID: vessel.ID, Meta: map[string]string{"issue_num": "214"}}))
	require.Len(t, r.calls, 2)

	refine := strings.Join(r.calls[1], " ")
	assert.Contains(t, refine, "--add-label needs-refinement")
	assert.Contains(t, refine, "--remove-label ready-for-work")
}

func TestShouldRouteToRefinementIncludesDiagnosisFollowUps(t *testing.T) {
	assert.True(t, shouldRouteToRefinement(queue.Vessel{
		Meta: map[string]string{recovery.MetaAction: string(recovery.ActionRequestInfo)},
	}))
	assert.True(t, shouldRouteToRefinement(queue.Vessel{
		Meta: map[string]string{recovery.MetaAction: string(recovery.ActionSplitIssue)},
	}))
	assert.False(t, shouldRouteToRefinement(queue.Vessel{
		Meta: map[string]string{recovery.MetaAction: string(recovery.ActionHarnessPatch)},
	}))
}

func TestOnFailNoLabelsConfigured(t *testing.T) {
	// When no status labels are configured, OnFail still removes the
	// backward-compat "in-progress" label that OnStart would have added.
	r := newMock()
	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: r,
	}

	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
		Meta:   map[string]string{"issue_num": "1"},
	}

	err := g.OnFail(context.Background(), vessel)
	if err != nil {
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

func TestOnFailNilRunner(t *testing.T) {
	g := &GitHub{
		Repo:      "owner/repo",
		CmdRunner: nil,
	}

	vessel := queue.Vessel{
		ID:   "issue-1",
		Meta: map[string]string{"issue_num": "1", "status_label_failed": "xylem-failed"},
	}

	err := g.OnFail(context.Background(), vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScanSkipsFailedStatusLabelWithoutEligibleRetry(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	// Issue has the xylem-failed label
	issues := []ghIssue{
		{
			Number: 10,
			Title:  "failed issue",
			Body:   "body",
			URL:    "https://github.com/owner/repo/issues/10",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "bug"}, {Name: "xylem-failed"}},
		},
	}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	g := &GitHub{
		Repo: "owner/repo",
		Tasks: map[string]GitHubTask{"fix": {
			Labels:       []string{"bug"},
			Workflow:     "fix-bug",
			StatusLabels: &StatusLabels{Failed: "xylem-failed"},
		}},
		Exclude:   []string{"xylem-failed"},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 0 {
		t.Errorf("expected 0 vessels (failed status labels require retry eligibility), got %d", len(vessels))
	}
}

func TestBacklogCountIncludesEligibleRetryableFailedIssue(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "flaky dependency",
		Body:   "same body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "xylem-failed"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	fingerprint := githubSourceFingerprint("flaky dependency", "same body", []string{"bug"})
	_, err := q.Enqueue(queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": fingerprint,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.Update("issue-42", queue.StateFailed, "temporary failure from upstream 503"))

	artifact := recovery.Build(recovery.Input{
		VesselID:  "issue-42",
		Source:    "github-issue",
		Workflow:  "fix-bug",
		Ref:       issues[0].URL,
		State:     queue.StateFailed,
		Error:     "temporary failure from upstream 503",
		CreatedAt: time.Now().UTC().Add(-2 * recovery.DefaultRetryCooldown),
	})
	require.NoError(t, recovery.Save(dir, artifact))

	g := &GitHub{
		Repo: "owner/repo",
		Tasks: map[string]GitHubTask{"fix": {
			Labels:       []string{"bug"},
			Workflow:     "fix-bug",
			StatusLabels: &StatusLabels{Failed: "xylem-failed"},
		}},
		Exclude:   []string{"xylem-failed"},
		StateDir:  dir,
		Queue:     q,
		CmdRunner: r,
	}

	count, err := g.BacklogCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestScanPersistsTriggerLabelInMeta(t *testing.T) {
	// Property: after Scan produces a vessel for an issue, that vessel's
	// Meta["trigger_label"] equals one of the labels configured on the
	// task that matched the issue. OnComplete later uses this to remove
	// the trigger label from the source issue.
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{
		{
			Number: 42,
			Title:  "add feature",
			Body:   "body",
			URL:    "https://github.com/owner/repo/issues/42",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "ready-for-work"}, {Name: "enhancement"}},
		},
	}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "ready-for-work")

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"features": {Labels: []string{"ready-for-work"}, Workflow: "implement-feature"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	got := vessels[0].Meta["trigger_label"]
	if got != "ready-for-work" {
		t.Errorf("expected Meta[trigger_label] = ready-for-work, got %q", got)
	}
}

func TestOnCompleteRemovesTriggerLabel(t *testing.T) {
	// Property: after a vessel completes successfully, its trigger
	// label is removed from the source issue via a separate gh call.
	// This prevents duplicate-enqueue risk after PR lifecycle events
	// and keeps the issue's UI state consistent with workflow state.
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:     "issue-156",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":              "156",
			"status_label_completed": "xylem-completed",
			"status_label_running":   "in-progress",
			"trigger_label":          "ready-for-work",
		},
	}
	if err := g.OnComplete(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected 2 gh calls (status transition + trigger-label removal), got %d: %v", len(r.calls), r.calls)
	}
	status := strings.Join(r.calls[0], " ")
	if !strings.Contains(status, "--add-label xylem-completed") {
		t.Errorf("call 0: expected --add-label xylem-completed, got %q", status)
	}
	if !strings.Contains(status, "--remove-label in-progress") {
		t.Errorf("call 0: expected --remove-label in-progress, got %q", status)
	}
	trig := strings.Join(r.calls[1], " ")
	if !strings.Contains(trig, "--remove-label ready-for-work") {
		t.Errorf("call 1: expected --remove-label ready-for-work, got %q", trig)
	}
	if strings.Contains(trig, "--add-label") {
		t.Errorf("call 1: trigger-label removal must not add anything, got %q", trig)
	}
}

func TestOnCompleteBackwardCompatNoTriggerLabel(t *testing.T) {
	// Backward-compat: a vessel enqueued before trigger_label was
	// introduced has no such key. OnComplete must not crash, must not
	// emit a second gh call, and must continue to perform the
	// status-label transition exactly as before.
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:     "issue-100",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":              "100",
			"status_label_completed": "done",
			"status_label_running":   "wip",
		},
	}
	if err := g.OnComplete(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 gh call (no trigger label present), got %d: %v", len(r.calls), r.calls)
	}
}

func TestOnCompleteRemovesTriggerLabelEvenWhenNoStatusLabels(t *testing.T) {
	// When status labels are not configured, OnComplete still emits a
	// gh call to remove the "in-progress" backward-compat running
	// label. The trigger_label removal must still fire in that case
	// as an independent second call.
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID:     "issue-117",
		Source: "github-issue",
		Meta: map[string]string{
			"issue_num":     "117",
			"trigger_label": "needs-refinement",
		},
	}
	if err := g.OnComplete(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(r.calls), r.calls)
	}
	trig := strings.Join(r.calls[1], " ")
	if !strings.Contains(trig, "--remove-label needs-refinement") {
		t.Errorf("expected --remove-label needs-refinement in second call, got %q", trig)
	}
}

func TestGitHubTaskFromConfigCopiesLabelGateLabels(t *testing.T) {
	task := GitHubTaskFromConfig(config.Task{
		Labels:   []string{"bug"},
		Workflow: "fix-bug",
		Tier:     "  high  ",
		LabelGateLabels: &config.LabelGateLabels{
			Waiting: "blocked",
			Ready:   "ready-for-implementation",
		},
	})

	if task.LabelGateLabels == nil {
		t.Fatal("LabelGateLabels should not be nil when config block is present")
	}
	if task.LabelGateLabels.Waiting != "blocked" {
		t.Errorf("LabelGateLabels.Waiting = %q, want blocked", task.LabelGateLabels.Waiting)
	}
	if task.LabelGateLabels.Ready != "ready-for-implementation" {
		t.Errorf("LabelGateLabels.Ready = %q, want ready-for-implementation", task.LabelGateLabels.Ready)
	}
	if task.Tier != "high" {
		t.Errorf("Tier = %q, want high", task.Tier)
	}
}

func TestGitHubOnWaitAppliesWaitingLabel(t *testing.T) {
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID: "issue-1",
		Meta: map[string]string{
			"issue_num":                "1",
			"label_gate_label_waiting": "blocked",
			"label_gate_label_ready":   "ready-for-implementation",
		},
	}

	if err := g.OnWait(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label blocked") {
		t.Errorf("expected --add-label blocked, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label ready-for-implementation") {
		t.Errorf("expected --remove-label ready-for-implementation, got %q", joined)
	}
}

func TestGitHubOnResumeAppliesReadyLabel(t *testing.T) {
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID: "issue-1",
		Meta: map[string]string{
			"issue_num":                "1",
			"label_gate_label_waiting": "blocked",
			"label_gate_label_ready":   "ready-for-implementation",
		},
	}

	if err := g.OnResume(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label ready-for-implementation") {
		t.Errorf("expected --add-label ready-for-implementation, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label blocked") {
		t.Errorf("expected --remove-label blocked, got %q", joined)
	}
}

func TestGitHubOnTimedOutRemovesWaitingLabel(t *testing.T) {
	r := newMock()
	g := &GitHub{Repo: "owner/repo", CmdRunner: r}
	vessel := queue.Vessel{
		ID: "issue-1",
		Meta: map[string]string{
			"issue_num":                "1",
			"status_label_timed_out":   "timed-out",
			"label_gate_label_waiting": "blocked",
		},
	}

	if err := g.OnTimedOut(context.Background(), vessel); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "--add-label timed-out") {
		t.Errorf("expected --add-label timed-out, got %q", joined)
	}
	if !strings.Contains(joined, "--remove-label blocked") {
		t.Errorf("expected --remove-label blocked, got %q", joined)
	}
}

// TestScanANDMatchesLabels verifies that Scan uses a single gh issue list call
// with all labels as AND-matched --label flags, not one call per label (OR-match).
// Regression test for issue #541.
func TestScanANDMatchesLabels(t *testing.T) {
	r := newMock()

	// Issue #522 has both "bug" and "ready-for-work"
	issues522 := []ghIssue{{
		Number: 522,
		Title:  "scanner dispatches wrong workflow",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/522",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "ready-for-work"}},
	}}
	issueBytes, _ := json.Marshal(issues522)

	// Only the AND-match key (both --label flags) should match.
	// The unregistered enhancement+ready-for-work key returns "[]" by default.
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug",
		"--label", "ready-for-work")

	g := &GitHub{
		Repo: "owner/repo",
		Tasks: map[string]GitHubTask{
			"fix": {
				Labels:   []string{"bug", "ready-for-work"},
				Workflow: "fix-bug",
			},
		},
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "fix-bug", vessels[0].Workflow)
	assert.Equal(t, "issue-522", vessels[0].ID)
}

// TestScanTwoTasksANDMatchDispatchesCorrectWorkflow verifies that two tasks with
// overlapping labels (e.g. both require "ready-for-work") dispatch correctly: an
// issue matching only one task's full label set produces exactly one vessel for
// that task's workflow and none for the other.
func TestScanTwoTasksANDMatchDispatchesCorrectWorkflow(t *testing.T) {
	r := newMock()

	// Issue #522 has "bug" and "ready-for-work" — matches fix task, not feat task
	bugIssues := []ghIssue{{
		Number: 522,
		Title:  "scanner bug",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/522",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "ready-for-work"}},
	}}
	bugBytes, _ := json.Marshal(bugIssues)
	r.set(bugBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug",
		"--label", "ready-for-work")
	// enhancement+ready-for-work returns empty (unregistered → default "[]")

	g := &GitHub{
		Repo: "owner/repo",
		Tasks: map[string]GitHubTask{
			"fix": {
				Labels:   []string{"bug", "ready-for-work"},
				Workflow: "fix-bug",
			},
			"feat": {
				Labels:   []string{"enhancement", "ready-for-work"},
				Workflow: "implement-feature",
			},
		},
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "fix-bug", vessels[0].Workflow)
}

// TestScanSingleLabelTaskUnchanged ensures that tasks with a single label still
// work correctly after the AND-match fix.
func TestScanSingleLabelTaskUnchanged(t *testing.T) {
	r := newMock()

	issues := []ghIssue{{
		Number: 10,
		Title:  "single label issue",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/10",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug")

	g := &GitHub{
		Repo:      "owner/repo",
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		CmdRunner: r,
	}

	vessels, err := g.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "fix-bug", vessels[0].Workflow)
	assert.Equal(t, "issue-10", vessels[0].ID)
}

// TestBacklogCountANDMatchesLabels verifies that BacklogCount uses the same
// single multi-label call as Scan. Regression test for issue #541.
func TestBacklogCountANDMatchesLabels(t *testing.T) {
	r := newMock()

	issues := []ghIssue{{
		Number: 522,
		Title:  "scanner bug",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/522",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}, {Name: "ready-for-work"}},
	}}
	issueBytes, _ := json.Marshal(issues)
	r.set(issueBytes, "gh", "issue", "list",
		"--repo", "owner/repo",
		"--state", "open",
		"--json", "number,title,body,url,labels",
		"--limit", "20",
		"--label", "bug",
		"--label", "ready-for-work")

	g := &GitHub{
		Repo: "owner/repo",
		Tasks: map[string]GitHubTask{
			"fix": {Labels: []string{"bug", "ready-for-work"}, Workflow: "fix-bug"},
		},
		CmdRunner: r,
	}

	count, err := g.BacklogCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
