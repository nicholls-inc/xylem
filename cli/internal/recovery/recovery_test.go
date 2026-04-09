package recovery

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmoke_S1_TransientTimeoutClassifiesToRetry(t *testing.T) {
	artifact := Build(Input{
		VesselID:  "issue-99",
		Workflow:  "fix-bug",
		State:     queue.StateTimedOut,
		Error:     "context deadline exceeded",
		CreatedAt: time.Date(2026, time.April, 9, 18, 5, 0, 0, time.UTC),
		Trace: &observability.TraceContextData{
			TraceID: "trace",
			SpanID:  "span",
		},
	})

	require.NotNil(t, artifact)
	assert.Equal(t, ClassTransient, artifact.RecoveryClass)
	assert.Equal(t, ActionRetry, artifact.RecoveryAction)
	assert.False(t, artifact.RetrySuppressed)
	assert.Equal(t, "not_attempted", artifact.RetryOutcome)
	assert.Equal(t, DecisionSourceDeterministic, artifact.DecisionSource)
	assert.Equal(t, []string{"Operator reviewed the failure context before retrying."}, artifact.RetryPreconditions)
	require.NotNil(t, artifact.Trace)
	assert.Equal(t, "trace", artifact.Trace.TraceID)
}

func TestSmoke_S2_HarnessGapPreservesUnlockDimensionForLessonsRouting(t *testing.T) {
	artifact := Build(Input{
		VesselID:  "issue-214-harness",
		Workflow:  "implement-harness",
		State:     queue.StateFailed,
		Error:     "policy blocked write to .xylem/HARNESS.md",
		CreatedAt: time.Date(2026, time.April, 9, 18, 7, 0, 0, time.UTC),
		Meta: map[string]string{
			MetaUnlockDimension: "workflow",
		},
	})

	require.NotNil(t, artifact)
	assert.Equal(t, ClassHarnessGap, artifact.RecoveryClass)
	assert.Equal(t, ActionLessons, artifact.RecoveryAction)
	assert.Equal(t, "workflow", artifact.UnlockDimension)
	assert.True(t, artifact.RetrySuppressed)
	assert.Equal(t, "suppressed", artifact.RetryOutcome)
	assert.Empty(t, artifact.FollowUpRoute)
}

func TestSmoke_S3_SpecGapRoutesToNeedsRefinement(t *testing.T) {
	artifact := Build(Input{
		VesselID:    "issue-214",
		Source:      "github-issue",
		Workflow:    "implement-harness",
		State:       queue.StateFailed,
		FailedPhase: "analyze",
		Error:       "missing requirement: acceptance criteria are ambiguous",
		CreatedAt:   time.Date(2026, time.April, 9, 18, 0, 0, 0, time.UTC),
	})

	require.NotNil(t, artifact)
	assert.Equal(t, ClassSpecGap, artifact.RecoveryClass)
	assert.Equal(t, ActionRefine, artifact.RecoveryAction)
	assert.Equal(t, "needs-refinement", artifact.FollowUpRoute)
	assert.True(t, artifact.RetrySuppressed)
	assert.Equal(t, "suppressed", artifact.RetryOutcome)
}

func TestSmoke_S4_ScopeGapRoutesToSplitTaskNeedsRefinement(t *testing.T) {
	artifact := Build(Input{
		VesselID:  "issue-214-scope",
		Workflow:  "implement-harness",
		State:     queue.StateFailed,
		Error:     "too broad for one change; split into separate issue",
		CreatedAt: time.Date(2026, time.April, 9, 18, 8, 0, 0, time.UTC),
	})

	require.NotNil(t, artifact)
	assert.Equal(t, ClassScopeGap, artifact.RecoveryClass)
	assert.Equal(t, ActionSplitTask, artifact.RecoveryAction)
	assert.Equal(t, "needs-refinement", artifact.FollowUpRoute)
	assert.True(t, artifact.RetrySuppressed)
	assert.Equal(t, "suppressed", artifact.RetryOutcome)
}

func TestSmoke_S5_AmbiguousFailureTriggersDiagnosisWorkflow(t *testing.T) {
	artifact := Build(Input{
		VesselID:             "issue-214-ambiguous",
		Workflow:             "implement-harness",
		State:                queue.StateFailed,
		Error:                "panic: invariant violated during phase execution",
		RepeatedFailureCount: 2,
		EvidencePaths: []string{
			"phases/issue-214-ambiguous/summary.json",
			"phases/issue-214-ambiguous/evidence-manifest.json",
		},
		CreatedAt: time.Date(2026, time.April, 9, 18, 9, 0, 0, time.UTC),
	})

	require.NotNil(t, artifact)
	assert.Equal(t, ClassUnknown, artifact.RecoveryClass)
	assert.Equal(t, ActionDiagnose, artifact.RecoveryAction)
	assert.True(t, ShouldDiagnose(artifact))

	diagnosed, invoked, err := RunDiagnosisWorkflow(DiagnosisInput{Artifact: artifact})
	require.NoError(t, err)
	assert.True(t, invoked)
	require.NotNil(t, diagnosed)
	assert.Equal(t, DecisionSourceDiagnosis, diagnosed.DecisionSource)
	assert.Equal(t, ActionHumanEscalation, diagnosed.RecoveryAction)
	assert.Equal(t, artifact.EvidencePaths, diagnosed.EvidencePaths)
	assert.True(t, diagnosed.RequiresDecisionRefresh)
	assert.True(t, diagnosed.RetrySuppressed)
	assert.NotEmpty(t, diagnosed.RetryPreconditions)
	assert.Contains(t, diagnosed.Rationale, "phases/issue-214-ambiguous/summary.json")
}

func TestShouldDiagnoseTriggersForLowConfidenceSingleFailure(t *testing.T) {
	artifact := &Artifact{
		SchemaVersion:        schemaVersion,
		VesselID:             "issue-214-low-confidence",
		State:                string(queue.StateFailed),
		RecoveryClass:        ClassTransient,
		RecoveryAction:       ActionRetry,
		Confidence:           diagnosisConfidenceThreshold - 0.01,
		RetryOutcome:         "not_attempted",
		RetryPreconditions:   []string{"Operator reviewed the failure context before retrying."},
		RepeatedFailureCount: 1,
		CreatedAt:            time.Date(2026, time.April, 9, 18, 9, 30, 0, time.UTC),
	}

	require.NotNil(t, artifact)
	assert.Equal(t, ActionRetry, artifact.RecoveryAction)
	assert.Less(t, artifact.Confidence, diagnosisConfidenceThreshold)
	assert.Equal(t, 1, artifact.RepeatedFailureCount)
	assert.True(t, ShouldDiagnose(artifact))
}

func TestRunDiagnosisWorkflowSkipsStableConcreteDecisions(t *testing.T) {
	base := Build(Input{
		VesselID:      "issue-214-transient",
		Workflow:      "fix-bug",
		State:         queue.StateFailed,
		Error:         "temporary failure from upstream 503",
		EvidencePaths: []string{"phases/issue-214-transient/summary.json"},
		CreatedAt:     time.Date(2026, time.April, 9, 18, 9, 45, 0, time.UTC),
	})

	require.NotNil(t, base)
	require.False(t, ShouldDiagnose(base))

	updated, invoked, err := RunDiagnosisWorkflow(DiagnosisInput{Artifact: base})
	require.NoError(t, err)
	assert.False(t, invoked)
	require.NotNil(t, updated)
	assert.NotSame(t, base, updated)
	assert.Equal(t, base.RecoveryAction, updated.RecoveryAction)
	assert.Equal(t, base.DecisionSource, updated.DecisionSource)
	assert.Equal(t, base.RetryPreconditions, updated.RetryPreconditions)

	updated.RetryPreconditions[0] = "mutated"
	assert.Equal(t, "Operator reviewed the failure context before retrying.", base.RetryPreconditions[0])
}

func TestApplyDiagnosisOutputRejectsSchemaViolations(t *testing.T) {
	base := Build(Input{
		VesselID:             "issue-333",
		Workflow:             "fix-bug",
		State:                queue.StateFailed,
		Error:                "panic: invariant violated",
		RepeatedFailureCount: 2,
		EvidencePaths:        []string{"phases/issue-333/summary.json"},
		CreatedAt:            time.Date(2026, time.April, 9, 18, 11, 0, 0, time.UTC),
	})

	raw, err := json.Marshal(&Artifact{
		RecoveryClass:  ClassTransient,
		RecoveryAction: ActionRetry,
		Confidence:     0.8,
		DecisionSource: DecisionSourceDiagnosis,
		Rationale:      "trust me",
		EvidencePaths:  []string{"phases/issue-333/summary.json"},
		RetryOutcome:   "not_attempted",
	})
	require.NoError(t, err)

	_, err = ApplyDiagnosisOutput(base, raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retry decisions require explicit retry preconditions")
}

func TestValidateRetryAuthorizationBlocksUntilDecisionChanges(t *testing.T) {
	artifact := &Artifact{
		SchemaVersion:           schemaVersion,
		VesselID:                "issue-444",
		State:                   string(queue.StateFailed),
		FailureFingerprint:      "abc",
		RecoveryClass:           ClassUnknown,
		Confidence:              0.8,
		RecoveryAction:          ActionHumanEscalation,
		DecisionSource:          DecisionSourceDiagnosis,
		Rationale:               "needs human review",
		EvidencePaths:           []string{"phases/issue-444/summary.json"},
		RetryPreconditions:      []string{"Refresh the recovery decision after a human reviews the cited artifacts."},
		RetrySuppressed:         true,
		RetryOutcome:            "suppressed",
		RequiresDecisionRefresh: true,
		CreatedAt:               time.Date(2026, time.April, 9, 18, 12, 0, 0, time.UTC),
	}

	err := ValidateRetryAuthorization(artifact)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decision changes")
}

func TestValidateRetryAuthorizationAllowsExplicitRetryPreconditions(t *testing.T) {
	artifact := &Artifact{
		SchemaVersion:      schemaVersion,
		VesselID:           "issue-445",
		State:              string(queue.StateFailed),
		FailureFingerprint: "abc",
		RecoveryClass:      ClassTransient,
		Confidence:         0.72,
		RecoveryAction:     ActionRetry,
		DecisionSource:     DecisionSourceDiagnosis,
		Rationale:          "bounded retry approved",
		EvidencePaths:      []string{"phases/issue-445/summary.json"},
		RetryPreconditions: []string{"Review the cited artifacts and confirm the retry budget before retrying."},
		RetrySuppressed:    false,
		RetryOutcome:       "not_attempted",
		CreatedAt:          time.Date(2026, time.April, 9, 18, 12, 5, 0, time.UTC),
	}

	require.NoError(t, Validate(artifact))
	require.NoError(t, ValidateRetryAuthorization(artifact))
}

func TestSmoke_S6_SaveLoadAndUpdateRetryOutcomePersistsEnqueuedState(t *testing.T) {
	stateDir := t.TempDir()
	artifact := Build(Input{
		VesselID:      "issue-101",
		Workflow:      "fix-bug",
		State:         queue.StateFailed,
		Error:         "temporary failure from upstream 503",
		EvidencePaths: []string{"phases/issue-101/summary.json"},
		CreatedAt:     time.Date(2026, time.April, 9, 18, 10, 0, 0, time.UTC),
	})

	require.NoError(t, Save(stateDir, artifact))
	require.NoError(t, UpdateRetryOutcome(stateDir, artifact.VesselID, "enqueued"))

	loaded, err := Load(filepath.Join(stateDir, RelativePath(artifact.VesselID)))
	require.NoError(t, err)
	assert.Equal(t, "enqueued", loaded.RetryOutcome)
}

func TestCountMatchingFailuresCountsTerminalMatchesOnly(t *testing.T) {
	current := queue.Vessel{
		ID:       "issue-50-retry-2",
		Ref:      "https://github.com/owner/repo/issues/50",
		Workflow: "fix-bug",
		State:    queue.StateFailed,
		Error:    "panic: invariant violated",
	}
	fingerprint := fingerprintForVessel(current)
	vessels := []queue.Vessel{
		current,
		{ID: "issue-50", Ref: current.Ref, Workflow: current.Workflow, State: queue.StateFailed, Error: current.Error},
		{ID: "issue-50-retry-1", Ref: current.Ref, Workflow: current.Workflow, State: queue.StateTimedOut, Error: current.Error},
		{ID: "issue-50-running", Ref: current.Ref, Workflow: current.Workflow, State: queue.StateRunning, Error: current.Error},
		{ID: "issue-99", Ref: "https://github.com/owner/repo/issues/99", Workflow: current.Workflow, State: queue.StateFailed, Error: current.Error},
	}

	assert.Equal(t, 3, CountMatchingFailures(vessels, current, fingerprint))
}

func TestSaveRejectsUnsafeVesselID(t *testing.T) {
	artifact := Build(Input{
		VesselID:      "../escape",
		Workflow:      "fix-bug",
		State:         queue.StateFailed,
		Error:         "temporary failure from upstream 503",
		EvidencePaths: []string{"phases/../escape/summary.json"},
		CreatedAt:     time.Date(2026, time.April, 9, 18, 10, 0, 0, time.UTC),
	})

	err := Save(t.TempDir(), artifact)
	if err == nil {
		t.Fatal("Save() error = nil, want invalid vessel ID error")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "invalid vessel ID") {
		t.Fatalf("Save() error = %q, want invalid vessel ID", got)
	}
}

func TestUpdateRetryOutcomeRejectsUnsafeVesselID(t *testing.T) {
	err := UpdateRetryOutcome(t.TempDir(), "../escape", "enqueued")
	if err == nil {
		t.Fatal("UpdateRetryOutcome() error = nil, want invalid vessel ID error")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "invalid vessel ID") {
		t.Fatalf("UpdateRetryOutcome() error = %q, want invalid vessel ID", got)
	}
}
