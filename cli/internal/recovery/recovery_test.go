package recovery

import (
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

func TestSmoke_S5_AmbiguousFailureRoutesToDiagnose(t *testing.T) {
	artifact := Build(Input{
		VesselID:  "issue-214-ambiguous",
		Workflow:  "implement-harness",
		State:     queue.StateFailed,
		Error:     "panic: invariant violated during phase execution",
		CreatedAt: time.Date(2026, time.April, 9, 18, 9, 0, 0, time.UTC),
	})

	require.NotNil(t, artifact)
	assert.Equal(t, ClassUnknown, artifact.RecoveryClass)
	assert.Equal(t, ActionDiagnose, artifact.RecoveryAction)
	assert.Empty(t, artifact.FollowUpRoute)
	assert.True(t, artifact.RetrySuppressed)
	assert.Equal(t, "suppressed", artifact.RetryOutcome)
}

func TestSmoke_S6_SaveLoadAndUpdateRetryOutcomePersistsEnqueuedState(t *testing.T) {
	stateDir := t.TempDir()
	artifact := Build(Input{
		VesselID:  "issue-101",
		Workflow:  "fix-bug",
		State:     queue.StateFailed,
		Error:     "temporary failure from upstream 503",
		CreatedAt: time.Date(2026, time.April, 9, 18, 10, 0, 0, time.UTC),
	})

	require.NoError(t, Save(stateDir, artifact))
	require.NoError(t, UpdateRetryOutcome(stateDir, artifact.VesselID, "enqueued"))

	loaded, err := Load(filepath.Join(stateDir, RelativePath(artifact.VesselID)))
	require.NoError(t, err)
	assert.Equal(t, "enqueued", loaded.RetryOutcome)
}

func TestSaveRejectsUnsafeVesselID(t *testing.T) {
	artifact := Build(Input{
		VesselID:  "../escape",
		Workflow:  "fix-bug",
		State:     queue.StateFailed,
		Error:     "temporary failure from upstream 503",
		CreatedAt: time.Date(2026, time.April, 9, 18, 10, 0, 0, time.UTC),
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
