package source

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateReenqueueBackwardCompatibleWithoutFailureReview(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      "https://github.com/owner/repo/issues/42",
		Workflow: "fix-bug",
		Meta:     map[string]string{"source_input_fingerprint": "source-a"},
	}, queue.StateFailed)

	blocked, err := evaluateReenqueue(q, filepath.Join(dir, ".xylem-state"), prior, "source-a")
	if err != nil {
		t.Fatalf("evaluateReenqueue() error = %v", err)
	}
	if !blocked.Block {
		t.Fatal("Block = false, want true for unchanged legacy failure")
	}

	allowed, err := evaluateReenqueue(q, filepath.Join(dir, ".xylem-state"), prior, "source-b")
	if err != nil {
		t.Fatalf("evaluateReenqueue() error = %v", err)
	}
	if allowed.Block {
		t.Fatal("Block = true, want source unlock")
	}
	if allowed.Meta["recovery_unlocked_by"] != "source" {
		t.Fatalf("recovery_unlocked_by = %q, want source", allowed.Meta["recovery_unlocked_by"])
	}
	if allowed.Meta["retry_of"] != prior.ID {
		t.Fatalf("retry_of = %q, want %q", allowed.Meta["retry_of"], prior.ID)
	}
	if allowed.Meta["retry_count"] != "1" {
		t.Fatalf("retry_count = %q, want 1", allowed.Meta["retry_count"])
	}
}

func TestEvaluateReenqueueUnlockDimensions(t *testing.T) {
	now := time.Date(2026, time.April, 9, 18, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)

	tests := []struct {
		name         string
		mutate       func(t *testing.T, controlPlane controlPlanePaths, stateDir, vesselID string)
		retryAfter   *time.Time
		wantUnlockBy string
		wantBlock    bool
	}{
		{
			name: "harness",
			mutate: func(t *testing.T, paths controlPlanePaths, _ string, _ string) {
				t.Helper()
				if err := os.WriteFile(paths.harness, []byte("updated harness"), 0o644); err != nil {
					t.Fatalf("WriteFile(harness) error = %v", err)
				}
			},
			wantUnlockBy: "harness",
		},
		{
			name: "workflow",
			mutate: func(t *testing.T, paths controlPlanePaths, _ string, _ string) {
				t.Helper()
				if err := os.WriteFile(paths.workflow, []byte("name: fix-bug\nupdated: true\n"), 0o644); err != nil {
					t.Fatalf("WriteFile(workflow) error = %v", err)
				}
			},
			wantUnlockBy: "workflow",
		},
		{
			name: "decision",
			mutate: func(t *testing.T, _ controlPlanePaths, stateDir, vesselID string) {
				t.Helper()
				path := recovery.FailureReviewPath(stateDir, vesselID)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile(failure-review) error = %v", err)
				}
				var reviewDoc recovery.FailureReview
				if err := json.Unmarshal(data, &reviewDoc); err != nil {
					t.Fatalf("Unmarshal(failure-review) error = %v", err)
				}
				reviewDoc.Hypothesis = "decision changed"
				updated, err := json.MarshalIndent(&reviewDoc, "", "  ")
				if err != nil {
					t.Fatalf("MarshalIndent(failure-review) error = %v", err)
				}
				if err := os.WriteFile(path, updated, 0o644); err != nil {
					t.Fatalf("WriteFile(failure-review) error = %v", err)
				}
			},
			wantUnlockBy: "decision",
		},
		{
			name:         "cooldown",
			retryAfter:   &past,
			wantUnlockBy: "cooldown",
		},
		{
			name: "cooldown pending",
			retryAfter: func() *time.Time {
				future := now.Add(time.Hour)
				return &future
			}(),
			wantBlock: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			stateDir := filepath.Join(dir, ".xylem-state")
			paths := writeControlPlane(t, dir, "fix-bug", "base harness", "name: fix-bug\n")
			q := queue.New(filepath.Join(dir, "queue.jsonl"))
			prior := seedTerminalVessel(t, q, queue.Vessel{
				ID:       "issue-42",
				Source:   "github-issue",
				Ref:      "https://github.com/owner/repo/issues/42",
				Workflow: "fix-bug",
				Meta:     map[string]string{"source_input_fingerprint": "source-a"},
			}, queue.StateFailed)
			reviewDoc := &recovery.FailureReview{
				VesselID:           prior.ID,
				SourceRef:          prior.Ref,
				Workflow:           prior.Workflow,
				Class:              "unknown",
				RecommendedAction:  "retry",
				FailureFingerprint: "fail-123",
				RetryCap:           2,
				RetryAfter:         tt.retryAfter,
				Unlock: recovery.UnlockFingerprint{
					SourceInputFingerprint: "source-a",
					HarnessDigest:          recovery.FileDigest(paths.harness),
					WorkflowDigest:         recovery.FileDigest(paths.workflow),
				},
			}
			if err := recovery.SaveFailureReview(stateDir, reviewDoc); err != nil {
				t.Fatalf("SaveFailureReview() error = %v", err)
			}
			if tt.mutate != nil {
				tt.mutate(t, paths, stateDir, prior.ID)
			}

			decision, err := evaluateReenqueue(q, stateDir, prior, "source-a")
			if err != nil {
				t.Fatalf("evaluateReenqueue() error = %v", err)
			}
			if decision.Block != tt.wantBlock {
				t.Fatalf("Block = %v, want %v", decision.Block, tt.wantBlock)
			}
			if !tt.wantBlock {
				if decision.Meta["recovery_unlocked_by"] != tt.wantUnlockBy {
					t.Fatalf("recovery_unlocked_by = %q, want %q", decision.Meta["recovery_unlocked_by"], tt.wantUnlockBy)
				}
				if decision.Meta["recovery_action"] != "retry" {
					t.Fatalf("recovery_action = %q, want retry", decision.Meta["recovery_action"])
				}
				if decision.Meta["recovery_class"] != "unknown" {
					t.Fatalf("recovery_class = %q, want unknown", decision.Meta["recovery_class"])
				}
				if decision.Meta["failure_fingerprint"] != "fail-123" {
					t.Fatalf("failure_fingerprint = %q, want fail-123", decision.Meta["failure_fingerprint"])
				}
				if decision.Meta["remediation_fingerprint"] == "" {
					t.Fatal("remediation_fingerprint = empty, want propagated fingerprint")
				}
				if decision.Meta["retry_of"] != prior.ID {
					t.Fatalf("retry_of = %q, want %q", decision.Meta["retry_of"], prior.ID)
				}
				if decision.Meta["retry_count"] != "1" {
					t.Fatalf("retry_count = %q, want 1", decision.Meta["retry_count"])
				}
				if decision.Parent == nil || decision.Parent.ID != prior.ID {
					t.Fatalf("Parent = %#v, want %s", decision.Parent, prior.ID)
				}
				if decision.NextID != "issue-42-retry-1" {
					t.Fatalf("NextID = %q, want issue-42-retry-1", decision.NextID)
				}
			}
		})
	}
}

func TestGitHubScanCreatesRetryVesselWithRecoveryLineage(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stateDir := filepath.Join(dir, ".xylem-state")
	paths := writeControlPlane(t, dir, "fix-bug", "base harness", "name: fix-bug\n")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "retry me",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, err := json.Marshal(issues)
	if err != nil {
		t.Fatalf("Marshal(issues) error = %v", err)
	}
	r.set(issueBytes, "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": githubSourceFingerprint("retry me", "body", []string{"bug"}),
		},
	}, queue.StateFailed)
	reviewDoc := &recovery.FailureReview{
		VesselID:           prior.ID,
		SourceRef:          prior.Ref,
		Workflow:           prior.Workflow,
		Class:              "unknown",
		RecommendedAction:  "retry",
		FailureFingerprint: "fail-123",
		RetryCap:           2,
		Unlock: recovery.UnlockFingerprint{
			SourceInputFingerprint: prior.Meta["source_input_fingerprint"],
			HarnessDigest:          recovery.FileDigest(paths.harness),
			WorkflowDigest:         recovery.FileDigest(paths.workflow),
		},
	}
	if err := recovery.SaveFailureReview(stateDir, reviewDoc); err != nil {
		t.Fatalf("SaveFailureReview() error = %v", err)
	}
	if err := os.WriteFile(paths.harness, []byte("updated harness"), 0o644); err != nil {
		t.Fatalf("WriteFile(harness) error = %v", err)
	}

	src := &GitHub{
		Repo:      "owner/repo",
		StateDir:  stateDir,
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("len(vessels) = %d, want 1", len(vessels))
	}
	if vessels[0].RetryOf != "issue-42" {
		t.Fatalf("RetryOf = %q, want issue-42", vessels[0].RetryOf)
	}
	if vessels[0].Meta["retry_of"] != "issue-42" {
		t.Fatalf("retry_of = %q, want issue-42", vessels[0].Meta["retry_of"])
	}
	if vessels[0].Meta["retry_count"] != "1" {
		t.Fatalf("retry_count = %q, want 1", vessels[0].Meta["retry_count"])
	}
	if vessels[0].Meta["recovery_unlocked_by"] != "harness" {
		t.Fatalf("recovery_unlocked_by = %q, want harness", vessels[0].Meta["recovery_unlocked_by"])
	}
	if vessels[0].Meta["recovery_action"] != "retry" {
		t.Fatalf("recovery_action = %q, want retry", vessels[0].Meta["recovery_action"])
	}
	if vessels[0].Meta["recovery_class"] != "unknown" {
		t.Fatalf("recovery_class = %q, want unknown", vessels[0].Meta["recovery_class"])
	}
	if vessels[0].Meta["failure_fingerprint"] != "fail-123" {
		t.Fatalf("failure_fingerprint = %q, want fail-123", vessels[0].Meta["failure_fingerprint"])
	}
	if vessels[0].Meta["remediation_fingerprint"] == "" {
		t.Fatal("remediation_fingerprint = empty, want populated")
	}
}

func TestGitHubPRScanCreatesRetryVesselWithRecoveryLineage(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stateDir := filepath.Join(dir, ".xylem-state")
	paths := writeControlPlane(t, dir, "review-pr", "base harness", "name: review-pr\n")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{{
		Number: 42,
		Title:  "review me",
		Body:   "body",
		URL:    "https://github.com/owner/repo/pull/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "review-me"}},
	}}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName,mergeable", "--limit", "20")

	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "pr-42-review-pr",
		Source:   "github-pr",
		Ref:      prWorkflowRef(prs[0].URL, "review-pr"),
		Workflow: "review-pr",
		Meta: map[string]string{
			"pr_num":                   "42",
			"source_input_fingerprint": githubSourceFingerprint("review me", "body", []string{"review-me"}),
		},
	}, queue.StateFailed)
	reviewDoc := &recovery.FailureReview{
		VesselID:           prior.ID,
		SourceRef:          prior.Ref,
		Workflow:           prior.Workflow,
		Class:              "unknown",
		RecommendedAction:  "retry",
		FailureFingerprint: "fail-123",
		RetryCap:           2,
		Unlock: recovery.UnlockFingerprint{
			SourceInputFingerprint: prior.Meta["source_input_fingerprint"],
			HarnessDigest:          recovery.FileDigest(paths.harness),
			WorkflowDigest:         recovery.FileDigest(paths.workflow),
		},
	}
	if err := recovery.SaveFailureReview(stateDir, reviewDoc); err != nil {
		t.Fatalf("SaveFailureReview() error = %v", err)
	}
	if err := os.WriteFile(paths.harness, []byte("updated harness"), 0o644); err != nil {
		t.Fatalf("WriteFile(harness) error = %v", err)
	}

	src := &GitHubPR{
		Repo:      "owner/repo",
		StateDir:  stateDir,
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("len(vessels) = %d, want 1", len(vessels))
	}
	if vessels[0].RetryOf != "pr-42-review-pr" {
		t.Fatalf("RetryOf = %q, want pr-42-review-pr", vessels[0].RetryOf)
	}
	if vessels[0].Meta["retry_of"] != "pr-42-review-pr" {
		t.Fatalf("retry_of = %q, want pr-42-review-pr", vessels[0].Meta["retry_of"])
	}
	if vessels[0].Meta["retry_count"] != "1" {
		t.Fatalf("retry_count = %q, want 1", vessels[0].Meta["retry_count"])
	}
	if vessels[0].Meta["recovery_unlocked_by"] != "harness" {
		t.Fatalf("recovery_unlocked_by = %q, want harness", vessels[0].Meta["recovery_unlocked_by"])
	}
	if vessels[0].Meta["recovery_action"] != "retry" {
		t.Fatalf("recovery_action = %q, want retry", vessels[0].Meta["recovery_action"])
	}
	if vessels[0].Meta["recovery_class"] != "unknown" {
		t.Fatalf("recovery_class = %q, want unknown", vessels[0].Meta["recovery_class"])
	}
	if vessels[0].Meta["failure_fingerprint"] != "fail-123" {
		t.Fatalf("failure_fingerprint = %q, want fail-123", vessels[0].Meta["failure_fingerprint"])
	}
	if vessels[0].Meta["remediation_fingerprint"] == "" {
		t.Fatal("remediation_fingerprint = empty, want populated")
	}
}

func TestEvaluateReenqueueContinuesRetrySequenceFromOriginalRoot(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "issue-42-retry-1",
		Source:   "github-issue",
		Ref:      "https://github.com/owner/repo/issues/42",
		Workflow: "fix-bug",
		RetryOf:  "issue-42",
		Meta: map[string]string{
			"retry_of":                 "issue-42",
			"retry_count":              "1",
			"source_input_fingerprint": "source-a",
		},
	}, queue.StateFailed)

	decision, err := evaluateReenqueue(q, filepath.Join(dir, ".xylem-state"), prior, "source-b")
	if err != nil {
		t.Fatalf("evaluateReenqueue() error = %v", err)
	}
	if decision.NextID != "issue-42-retry-2" {
		t.Fatalf("NextID = %q, want issue-42-retry-2", decision.NextID)
	}
	if decision.Meta["retry_of"] != "issue-42" {
		t.Fatalf("retry_of = %q, want issue-42", decision.Meta["retry_of"])
	}
	if decision.Meta["retry_count"] != "2" {
		t.Fatalf("retry_count = %q, want 2", decision.Meta["retry_count"])
	}
}

func TestSmoke_S1_TransientFailureRetriesOnceCooldownExpires(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	stateDir := filepath.Join(dir, ".xylem-state")
	paths := writeControlPlane(t, dir, "fix-bug", "base harness", "name: fix-bug\n")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      "https://github.com/owner/repo/issues/42",
		Workflow: "fix-bug",
		Meta:     map[string]string{"source_input_fingerprint": "source-a"},
	}, queue.StateFailed)

	retryAfter := time.Now().UTC().Add(-time.Hour)
	reviewDoc := &recovery.FailureReview{
		VesselID:           prior.ID,
		SourceRef:          prior.Ref,
		Workflow:           prior.Workflow,
		Class:              "transient",
		RecommendedAction:  "retry",
		FailureFingerprint: "fail-123",
		RetryCap:           2,
		RetryAfter:         &retryAfter,
		Unlock: recovery.UnlockFingerprint{
			SourceInputFingerprint: "source-a",
			HarnessDigest:          recovery.FileDigest(paths.harness),
			WorkflowDigest:         recovery.FileDigest(paths.workflow),
		},
	}
	require.NoError(t, recovery.SaveFailureReview(stateDir, reviewDoc))

	decision, err := evaluateReenqueue(q, stateDir, prior, "source-a")
	require.NoError(t, err)

	assert.False(t, decision.Block)
	require.NotNil(t, decision.Parent)
	assert.Equal(t, prior.ID, decision.Parent.ID)
	assert.Equal(t, "issue-42-retry-1", decision.NextID)
	assert.Equal(t, "retry", decision.Meta["recovery_action"])
	assert.Equal(t, "transient", decision.Meta["recovery_class"])
	assert.Equal(t, "cooldown", decision.Meta["recovery_unlocked_by"])
	assert.Equal(t, "fail-123", decision.Meta["failure_fingerprint"])
	assert.Equal(t, prior.ID, decision.Meta["retry_of"])
	assert.Equal(t, "1", decision.Meta["retry_count"])
	assert.NotEmpty(t, decision.Meta["remediation_fingerprint"])
}

func TestSmoke_S2_HarnessChangeUnlocksRetryWithoutSourceEdit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	stateDir := filepath.Join(dir, ".xylem-state")
	paths := writeControlPlane(t, dir, "fix-bug", "base harness", "name: fix-bug\n")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "retry me",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, err := json.Marshal(issues)
	require.NoError(t, err)
	r.set(issueBytes, "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	sourceFingerprint := githubSourceFingerprint("retry me", "body", []string{"bug"})
	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": sourceFingerprint,
		},
	}, queue.StateFailed)
	reviewDoc := &recovery.FailureReview{
		VesselID:           prior.ID,
		SourceRef:          prior.Ref,
		Workflow:           prior.Workflow,
		Class:              "harness_gap",
		RecommendedAction:  "retry",
		FailureFingerprint: "fail-123",
		RetryCap:           2,
		Unlock: recovery.UnlockFingerprint{
			SourceInputFingerprint: sourceFingerprint,
			HarnessDigest:          recovery.FileDigest(paths.harness),
			WorkflowDigest:         recovery.FileDigest(paths.workflow),
		},
	}
	require.NoError(t, recovery.SaveFailureReview(stateDir, reviewDoc))
	require.NoError(t, os.WriteFile(paths.harness, []byte("updated harness"), 0o644))

	src := &GitHub{
		Repo:      "owner/repo",
		StateDir:  stateDir,
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)

	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, queue.StatePending, vessels[0].State)
	assert.Equal(t, prior.ID, vessels[0].RetryOf)
	assert.Equal(t, sourceFingerprint, vessels[0].Meta["source_input_fingerprint"])
	assert.Equal(t, "retry", vessels[0].Meta["recovery_action"])
	assert.Equal(t, "harness_gap", vessels[0].Meta["recovery_class"])
	assert.Equal(t, "harness", vessels[0].Meta["recovery_unlocked_by"])
	assert.Equal(t, "fail-123", vessels[0].Meta["failure_fingerprint"])
	assert.Equal(t, prior.ID, vessels[0].Meta["retry_of"])
	assert.Equal(t, "1", vessels[0].Meta["retry_count"])
	assert.NotEmpty(t, vessels[0].Meta["remediation_fingerprint"])
}

func TestSmoke_S3_WorkflowChangeUnlocksRetryWithoutSourceEdit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	stateDir := filepath.Join(dir, ".xylem-state")
	paths := writeControlPlane(t, dir, "fix-bug", "base harness", "name: fix-bug\n")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "retry me",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, err := json.Marshal(issues)
	require.NoError(t, err)
	r.set(issueBytes, "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	sourceFingerprint := githubSourceFingerprint("retry me", "body", []string{"bug"})
	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": sourceFingerprint,
		},
	}, queue.StateFailed)
	reviewDoc := &recovery.FailureReview{
		VesselID:           prior.ID,
		SourceRef:          prior.Ref,
		Workflow:           prior.Workflow,
		Class:              "workflow_gap",
		RecommendedAction:  "retry",
		FailureFingerprint: "fail-123",
		RetryCap:           2,
		Unlock: recovery.UnlockFingerprint{
			SourceInputFingerprint: sourceFingerprint,
			HarnessDigest:          recovery.FileDigest(paths.harness),
			WorkflowDigest:         recovery.FileDigest(paths.workflow),
		},
	}
	require.NoError(t, recovery.SaveFailureReview(stateDir, reviewDoc))
	require.NoError(t, os.WriteFile(paths.workflow, []byte("name: fix-bug\nupdated: true\n"), 0o644))

	src := &GitHub{
		Repo:      "owner/repo",
		StateDir:  stateDir,
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)

	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, prior.ID, vessels[0].RetryOf)
	assert.Equal(t, sourceFingerprint, vessels[0].Meta["source_input_fingerprint"])
	assert.Equal(t, "retry", vessels[0].Meta["recovery_action"])
	assert.Equal(t, "workflow_gap", vessels[0].Meta["recovery_class"])
	assert.Equal(t, "workflow", vessels[0].Meta["recovery_unlocked_by"])
	assert.Equal(t, "fail-123", vessels[0].Meta["failure_fingerprint"])
	assert.Equal(t, prior.ID, vessels[0].Meta["retry_of"])
	assert.Equal(t, "1", vessels[0].Meta["retry_count"])
	assert.NotEmpty(t, vessels[0].Meta["remediation_fingerprint"])
}

func TestSmoke_S4_AmbiguousRepeatedFailureStaysBlockedUntilDecisionChange(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	stateDir := filepath.Join(dir, ".xylem-state")
	paths := writeControlPlane(t, dir, "fix-bug", "base harness", "name: fix-bug\n")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	issues := []ghIssue{{
		Number: 42,
		Title:  "retry me",
		Body:   "body",
		URL:    "https://github.com/owner/repo/issues/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	issueBytes, err := json.Marshal(issues)
	require.NoError(t, err)
	r.set(issueBytes, "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", "bug")

	sourceFingerprint := githubSourceFingerprint("retry me", "body", []string{"bug"})
	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "issue-42",
		Source:   "github-issue",
		Ref:      issues[0].URL,
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":                "42",
			"source_input_fingerprint": sourceFingerprint,
		},
	}, queue.StateFailed)
	reviewDoc := &recovery.FailureReview{
		VesselID:                prior.ID,
		SourceRef:               prior.Ref,
		Workflow:                prior.Workflow,
		Class:                   "unknown",
		RecommendedAction:       "retry",
		FailureFingerprint:      "fail-123",
		RetryCap:                2,
		RequiresDecisionRefresh: true,
		Hypothesis:              "initial hypothesis",
		Unlock: recovery.UnlockFingerprint{
			SourceInputFingerprint: sourceFingerprint,
			HarnessDigest:          recovery.FileDigest(paths.harness),
			WorkflowDigest:         recovery.FileDigest(paths.workflow),
		},
	}
	require.NoError(t, recovery.SaveFailureReview(stateDir, reviewDoc))

	src := &GitHub{
		Repo:      "owner/repo",
		StateDir:  stateDir,
		Tasks:     map[string]GitHubTask{"fix": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	require.NoError(t, err)
	assert.Empty(t, vessels)

	reviewPath := recovery.FailureReviewPath(stateDir, prior.ID)
	data, err := os.ReadFile(reviewPath)
	require.NoError(t, err)

	var updated recovery.FailureReview
	require.NoError(t, json.Unmarshal(data, &updated))
	updated.Hypothesis = "refined hypothesis"

	rewritten, err := json.MarshalIndent(&updated, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(reviewPath, rewritten, 0o644))

	vessels, err = src.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)

	assert.Equal(t, "issue-42-retry-1", vessels[0].ID)
	assert.Equal(t, prior.ID, vessels[0].RetryOf)
	assert.Equal(t, "decision", vessels[0].Meta["recovery_unlocked_by"])
	assert.Equal(t, "retry", vessels[0].Meta["recovery_action"])
	assert.Equal(t, "unknown", vessels[0].Meta["recovery_class"])
	assert.Equal(t, prior.ID, vessels[0].Meta["retry_of"])
	assert.Equal(t, "1", vessels[0].Meta["retry_count"])
	assert.NotEmpty(t, vessels[0].Meta["remediation_fingerprint"])
}

func TestSmoke_S5_PRScannerAppliesRemediationAwareRetryUnlocks(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	stateDir := filepath.Join(dir, ".xylem-state")
	paths := writeControlPlane(t, dir, "review-pr", "base harness", "name: review-pr\n")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	r := newMock()

	prs := []ghPR{{
		Number: 42,
		Title:  "review me",
		Body:   "body",
		URL:    "https://github.com/owner/repo/pull/42",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "review-me"}},
	}}
	r.set(prJSON(prs), "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--label", "review-me", "--json", "number,title,body,url,labels,headRefName,mergeable", "--limit", "20")

	sourceFingerprint := githubSourceFingerprint("review me", "body", []string{"review-me"})
	prior := seedTerminalVessel(t, q, queue.Vessel{
		ID:       "pr-42-review-pr",
		Source:   "github-pr",
		Ref:      prWorkflowRef(prs[0].URL, "review-pr"),
		Workflow: "review-pr",
		Meta: map[string]string{
			"pr_num":                   "42",
			"source_input_fingerprint": sourceFingerprint,
		},
	}, queue.StateFailed)
	reviewDoc := &recovery.FailureReview{
		VesselID:           prior.ID,
		SourceRef:          prior.Ref,
		Workflow:           prior.Workflow,
		Class:              "harness_gap",
		RecommendedAction:  "retry",
		FailureFingerprint: "fail-123",
		RetryCap:           2,
		Unlock: recovery.UnlockFingerprint{
			SourceInputFingerprint: sourceFingerprint,
			HarnessDigest:          recovery.FileDigest(paths.harness),
			WorkflowDigest:         recovery.FileDigest(paths.workflow),
		},
	}
	require.NoError(t, recovery.SaveFailureReview(stateDir, reviewDoc))
	require.NoError(t, os.WriteFile(paths.harness, []byte("updated harness"), 0o644))

	src := &GitHubPR{
		Repo:      "owner/repo",
		StateDir:  stateDir,
		Tasks:     map[string]GitHubTask{"review": {Labels: []string{"review-me"}, Workflow: "review-pr"}},
		Queue:     q,
		CmdRunner: r,
	}

	vessels, err := src.Scan(context.Background())
	require.NoError(t, err)
	require.Len(t, vessels, 1)

	assert.Equal(t, "pr-42-review-pr-retry-1", vessels[0].ID)
	assert.Equal(t, queue.StatePending, vessels[0].State)
	assert.Equal(t, prior.ID, vessels[0].RetryOf)
	assert.Equal(t, sourceFingerprint, vessels[0].Meta["source_input_fingerprint"])
	assert.Equal(t, "retry", vessels[0].Meta["recovery_action"])
	assert.Equal(t, "harness_gap", vessels[0].Meta["recovery_class"])
	assert.Equal(t, "harness", vessels[0].Meta["recovery_unlocked_by"])
	assert.Equal(t, "fail-123", vessels[0].Meta["failure_fingerprint"])
	assert.Equal(t, prior.ID, vessels[0].Meta["retry_of"])
	assert.Equal(t, "1", vessels[0].Meta["retry_count"])
	assert.NotEmpty(t, vessels[0].Meta["remediation_fingerprint"])
}

type controlPlanePaths struct {
	harness  string
	workflow string
}

func writeControlPlane(t *testing.T, dir, workflowName, harnessContent, workflowContent string) controlPlanePaths {
	t.Helper()
	harnessPath := filepath.Join(dir, ".xylem", "HARNESS.md")
	workflowPath := filepath.Join(dir, ".xylem", "workflows", workflowName+".yaml")
	if err := os.MkdirAll(filepath.Dir(workflowPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(workflow) error = %v", err)
	}
	if err := os.WriteFile(harnessPath, []byte(harnessContent), 0o644); err != nil {
		t.Fatalf("WriteFile(harness) error = %v", err)
	}
	if err := os.WriteFile(workflowPath, []byte(workflowContent), 0o644); err != nil {
		t.Fatalf("WriteFile(workflow) error = %v", err)
	}
	return controlPlanePaths{harness: harnessPath, workflow: workflowPath}
}

func seedTerminalVessel(t *testing.T, q *queue.Queue, vessel queue.Vessel, state queue.VesselState) *queue.Vessel {
	t.Helper()
	vessel.State = queue.StatePending
	vessel.CreatedAt = time.Now().UTC()
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if err := q.Update(vessel.ID, state, "boom"); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	found, err := q.FindByID(vessel.ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	return found
}
