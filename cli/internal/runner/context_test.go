package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
	"github.com/nicholls-inc/xylem/cli/internal/memory"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareStructuredStateReconstructsResumeArtifacts(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = dir

	vessel := queue.Vessel{ID: "issue-60", Workflow: "fix-bug", CurrentPhase: 1}
	wf := &workflow.Workflow{
		Name: "fix-bug",
		Phases: []workflow.Phase{
			{Name: "plan"},
			{Name: "implement"},
		},
	}

	phasesDir := filepath.Join(dir, "phases", vessel.ID)
	require.NoError(t, os.MkdirAll(phasesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(phasesDir, "plan.output"), []byte("plan output from file"), 0o644))
	progress := structuredProgressFile{
		VesselID:     vessel.ID,
		Workflow:     vessel.Workflow,
		CurrentPhase: 1,
		Plan:         "existing plan",
		Checkpoints:  []string{"plan"},
		Verification: []string{"go test ./..."},
		Approvals: []memory.OperatorApproval{{
			ApprovedBy: "operator",
			Reason:     "resume",
			ApprovedAt: time.Now().UTC(),
		}},
		PhaseOutputs: map[string]string{"plan": "phases/issue-60/plan.output"},
		Phases: []structuredSnapshot{{
			Name:      "plan",
			Index:     0,
			Status:    "completed",
			UpdatedAt: time.Now().UTC(),
		}},
		UpdatedAt: time.Now().UTC(),
	}
	progressData, err := json.MarshalIndent(progress, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(phasesDir, progressFileName(vessel.ID)), progressData, 0o644))

	handoff := memory.NewHandoff(vessel.ID, latestHandoffSessionID)
	handoff.CurrentPhase = "implement"
	handoff.Plan = "existing plan"
	handoff.Checkpoints = []string{"plan"}
	handoff.Unresolved = []string{"implement: still open"}
	handoff.PhaseOutputs = map[string]string{"plan": "plan output from handoff"}
	require.NoError(t, handoff.Save(phasesDir))

	state, err := prepareStructuredState(cfg, vessel, wf)
	require.NoError(t, err)

	assert.Equal(t, "existing plan", state.progress.Plan)
	assert.Equal(t, 1, state.progress.CurrentPhase)
	assert.Contains(t, state.progress.Checkpoints, "plan")
	assert.Contains(t, state.progress.Unresolved, "implement: still open")
	assert.Equal(t, "phases/issue-60/plan.output", state.progress.PhaseOutputs["plan"])
	assert.Equal(t, "plan output from handoff", state.resume.PreviousOutputs["plan"])
	require.Len(t, state.progress.Phases, 2)
	assert.Equal(t, "implement", state.progress.Phases[1].Name)
}

func TestCompilePhaseContextCompactsWorkingOutputsAtThreshold(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = dir
	cfg.CleanupAfter = "24h"

	vessel := queue.Vessel{ID: "issue-60", Workflow: "fix-bug"}
	wf := &workflow.Workflow{
		Name: "fix-bug",
		Phases: []workflow.Phase{
			{Name: "plan"},
		},
	}

	state, err := prepareStructuredState(cfg, vessel, wf)
	require.NoError(t, err)
	state.progress.Plan = "durable plan"

	manifest, err := state.compilePhaseContext(
		workflow.Phase{Name: "plan"},
		0,
		phase.IssueData{},
		map[string]string{"previous": strings.Repeat("x", compiledContextTokenBudget*8)},
		"",
		false,
	)
	require.NoError(t, err)

	assert.Equal(t, ctxmgr.StrategyCompress, manifest.Strategy)
	assert.True(t, manifest.Compaction.Applied)
	assert.Equal(t, manifest.Compaction.DurableTokens, manifest.Compaction.AfterTokens)
	require.Len(t, manifest.Window.Segments, 1)
	assert.Equal(t, "plan", manifest.Window.Segments[0].Name)

	manifestPath := filepath.Join(dir, manifest.ManifestPath)
	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	var persisted PhaseContextManifest
	require.NoError(t, json.Unmarshal(data, &persisted))
	assert.Equal(t, manifest.Phase, persisted.Phase)
	assert.Equal(t, manifest.ManifestPath, persisted.ManifestPath)
}

func TestCleanupExpiredStructuredArtifactsKeepsDurableFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = dir
	cfg.CleanupAfter = "1h"

	vesselID := "issue-60"
	phasesDir := filepath.Join(dir, "phases", vesselID)
	require.NoError(t, os.MkdirAll(phasesDir, 0o755))

	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now()
	oldFiles := []string{
		filepath.Join(phasesDir, "plan.context.json"),
		filepath.Join(phasesDir, "handoff_"+vesselID+"_20260408T202500Z.json"),
	}
	keptFiles := []string{
		filepath.Join(phasesDir, "handoff_"+vesselID+"_"+latestHandoffSessionID+".json"),
		filepath.Join(phasesDir, progressFileName(vesselID)),
		filepath.Join(phasesDir, summaryFileName),
		filepath.Join(phasesDir, "plan.output"),
	}

	for _, path := range oldFiles {
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0o644))
		require.NoError(t, os.Chtimes(path, oldTime, oldTime))
	}
	for _, path := range keptFiles {
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0o644))
		require.NoError(t, os.Chtimes(path, newTime, newTime))
	}

	require.NoError(t, cleanupExpiredStructuredArtifacts(cfg, vesselID, newTime))

	for _, path := range oldFiles {
		_, err := os.Stat(path)
		assert.Error(t, err)
	}
	for _, path := range keptFiles {
		_, err := os.Stat(path)
		assert.NoError(t, err)
	}
}

func TestApplySummaryIncludesStructuredRetention(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = dir

	state := &structuredState{
		cfg:              cfg,
		vessel:           queue.Vessel{ID: "issue-60"},
		latestHandoffRel: latestHandoffRelativePath("issue-60"),
	}
	summary := &VesselSummary{}

	state.applySummary(summary)

	assert.Equal(t, "phases/issue-60/"+progressFileName("issue-60"), summary.ProgressPath)
	assert.Equal(t, latestHandoffRelativePath("issue-60"), summary.HandoffPath)
	require.NotNil(t, summary.Retention)
	assert.Contains(t, summary.Retention.Expirable, "phases/issue-60/*.context.json")
}
