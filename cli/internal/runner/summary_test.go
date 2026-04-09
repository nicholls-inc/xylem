package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var standardLoggerMu sync.Mutex

func TestSmoke_S1_SummaryFileWrittenOnVesselCompletion(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem-state")

	startedAt := time.Date(2026, time.April, 8, 20, 25, 0, 0, time.UTC)
	vessel := runningSmokeVessel("vessel-abc123", "github", "fix-bug", startedAt)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	vrs := newVesselRunState(cfg, vessel, startedAt)
	vrs.addPhase(PhaseSummary{Name: "plan", Status: "completed"})
	vrs.addPhase(PhaseSummary{Name: "implement", Status: "completed"})

	outcome := r.completeVessel(context.Background(), vessel, "", nil, vrs, nil)
	assert.Equal(t, "completed", outcome)

	path := filepath.Join(cfg.StateDir, "phases", vessel.ID, summaryFileName)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Equal(t, vessel.ID, summary.VesselID)
	assert.Equal(t, "completed", summary.State)
}

func TestSmoke_S2_SummaryFileWrittenOnVesselFailurePartialSummary(t *testing.T) {
	stateDir := t.TempDir()
	startedAt := time.Date(2026, time.April, 8, 20, 26, 0, 0, time.UTC)
	vessel := queue.Vessel{
		ID:       "vessel-def456",
		Source:   "github",
		Workflow: "fix-bug",
	}

	vrs := newVesselRunState(nil, vessel, startedAt)
	vrs.addPhase(PhaseSummary{Name: "analyze", Status: "completed"})

	err := SaveVesselSummary(stateDir, vrs.buildSummary("failed", startedAt.Add(2*time.Second)))
	require.NoError(t, err)

	summary := loadSummary(t, stateDir, vessel.ID)
	assert.Equal(t, "failed", summary.State)
	assert.Len(t, summary.Phases, 1)
	assert.Equal(t, "analyze", summary.Phases[0].Name)
	assert.Equal(t, "completed", summary.Phases[0].Status)
}

func TestSmoke_S3_SummaryContainsTheDisclaimerNote(t *testing.T) {
	stateDir := t.TempDir()

	err := SaveVesselSummary(stateDir, &VesselSummary{
		VesselID: "vessel-note",
		Source:   "manual",
		State:    "completed",
		Phases:   []PhaseSummary{},
	})
	require.NoError(t, err)

	summary := loadSummary(t, stateDir, "vessel-note")
	assert.Equal(t, summaryDisclaimer, summary.Note)
}

func TestSmoke_S4_SummaryJSONIsPrettyPrinted(t *testing.T) {
	stateDir := t.TempDir()

	err := SaveVesselSummary(stateDir, &VesselSummary{
		VesselID: "vessel-pretty",
		Source:   "manual",
		State:    "completed",
		Phases:   []PhaseSummary{},
	})
	require.NoError(t, err)

	data := readSummaryBytes(t, stateDir, "vessel-pretty")
	lines := strings.Split(string(data), "\n")
	require.GreaterOrEqual(t, len(lines), 2)

	assert.Contains(t, string(data), "\n  \"")
	assert.Equal(t, "{", lines[0])
	assert.True(t, strings.HasPrefix(lines[1], "  \""))

	var summary VesselSummary
	require.NoError(t, json.Unmarshal(data, &summary))
	assert.Equal(t, summaryDisclaimer, summary.Note)
}

func TestSmoke_S9_BuildSummaryComputesTotalTokensEstAsSumOfPhaseTokenFields(t *testing.T) {
	startedAt := time.Date(2026, time.April, 8, 20, 27, 0, 0, time.UTC)
	vrs := newVesselRunState(nil, queue.Vessel{
		ID:       "vessel-tokens",
		Source:   "manual",
		Workflow: "fix-bug",
	}, startedAt)
	vrs.addPhase(PhaseSummary{Name: "phase-a", InputTokensEst: 100, OutputTokensEst: 50})
	vrs.addPhase(PhaseSummary{Name: "phase-b", InputTokensEst: 200, OutputTokensEst: 80})

	summary := vrs.buildSummary("completed", startedAt.Add(2*time.Second))
	assert.Equal(t, 300, summary.TotalInputTokensEst)
	assert.Equal(t, 130, summary.TotalOutputTokensEst)
	assert.Equal(t, 430, summary.TotalTokensEst)
}

func TestSmoke_S10_BuildSummaryComputesTotalCostUSDEstAsSumOfPhaseCosts(t *testing.T) {
	startedAt := time.Date(2026, time.April, 8, 20, 28, 0, 0, time.UTC)
	vrs := newVesselRunState(nil, queue.Vessel{
		ID:       "vessel-cost",
		Source:   "manual",
		Workflow: "fix-bug",
	}, startedAt)
	vrs.addPhase(PhaseSummary{Name: "phase-a", CostUSDEst: 0.0012})
	vrs.addPhase(PhaseSummary{Name: "phase-b", CostUSDEst: 0.0034})

	summary := vrs.buildSummary("completed", startedAt.Add(2*time.Second))
	assert.InDelta(t, 0.0046, summary.TotalCostUSDEst, 1e-9)
}

func TestSummarizeUsageSourceHandlesMissingUsageScenarios(t *testing.T) {
	t.Run("non llm phases", func(t *testing.T) {
		source, reason := summarizeUsageSource([]PhaseSummary{
			{Name: "verify", Type: "command", UsageSource: cost.UsageSourceNotApplicable, UsageUnavailableReason: "non-llm phase"},
		}, 0, 0)

		assert.Equal(t, cost.UsageSourceNotApplicable, source)
		assert.Equal(t, "run did not execute an llm phase", reason)
	})

	t.Run("mixed phases retains available usage source", func(t *testing.T) {
		source, reason := summarizeUsageSource([]PhaseSummary{
			{Name: "verify", Type: "command", UsageSource: cost.UsageSourceNotApplicable, UsageUnavailableReason: "non-llm phase"},
			{Name: "implement", Type: "prompt", UsageSource: cost.UsageSourceProvider},
		}, 0, 0)

		assert.Equal(t, cost.UsageSourceProvider, source)
		assert.Empty(t, reason)
	})
}

func TestSmoke_S11_BuildSummarySetsDurationMSFromStartedAtToCallTime(t *testing.T) {
	startedAt := time.Now().UTC().Add(-2 * time.Second)
	vrs := newVesselRunState(nil, queue.Vessel{
		ID:       "vessel-duration",
		Source:   "manual",
		Workflow: "fix-bug",
	}, startedAt)

	summary := vrs.buildSummary("completed", time.Time{})
	assert.Greater(t, summary.DurationMS, int64(0))
	assert.True(t, summary.StartedAt.Equal(startedAt))
	assert.True(t, summary.EndedAt.After(summary.StartedAt))
}

func TestSmoke_S12_BuildSummaryReadsBudgetExceededFromTheCostTracker(t *testing.T) {
	cfg := makeTestConfig(t.TempDir(), 1)
	setPricedModel(cfg)
	setBudget(cfg, 0.0001, 10)

	startedAt := time.Now().Add(-time.Second).UTC()
	vrs := newVesselRunState(cfg, queue.Vessel{
		ID:       "vessel-budget-exceeded-smoke",
		Source:   "manual",
		Workflow: "fix-bug",
	}, startedAt)

	inputTokens, outputTokens, costUSDEst := vrs.recordPhaseTokens(
		workflow.Phase{Name: "implement"},
		"claude-sonnet-4",
		"Implement the requested changes",
		strings.Repeat("output ", 40),
		startedAt.Add(500*time.Millisecond),
	)
	vrs.addPhase(PhaseSummary{
		Name:            "implement",
		Status:          "failed",
		InputTokensEst:  inputTokens,
		OutputTokensEst: outputTokens,
		CostUSDEst:      costUSDEst,
	})

	summary := vrs.buildSummary("completed", startedAt.Add(2*time.Second))
	assert.True(t, summary.BudgetExceeded)
}

func TestSmoke_S13_SaveVesselSummaryCreatesThePhasesVesselIDDirectoryIfAbsent(t *testing.T) {
	stateDir := t.TempDir()
	targetDir := filepath.Join(stateDir, "phases", "vessel-new999")
	_, err := os.Stat(targetDir)
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	err = SaveVesselSummary(stateDir, &VesselSummary{
		VesselID: "vessel-new999",
		Source:   "manual",
		State:    "completed",
		Phases:   []PhaseSummary{},
	})
	require.NoError(t, err)

	_, err = os.Stat(targetDir)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(targetDir, summaryFileName))
	require.NoError(t, err)
}

func TestSmoke_S14_SaveVesselSummaryFailureIsNonFatalCallerContinues(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem-state")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(14, "ws3-s14")
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "ws3-s14", []testPhase{{
		name:          "implement",
		promptContent: "Implement the failing phase",
		maxTurns:      5,
	}})

	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer func() {
		require.NoError(t, os.Chdir(oldWd))
	}()

	summaryAsDir := filepath.Join(cfg.StateDir, "phases", vessel.ID, summaryFileName)
	require.NoError(t, os.MkdirAll(summaryAsDir, 0o755))

	buf := captureStandardLogger(t)
	cmdRunner := &mockCmdRunner{
		phaseErrByPrompt: map[string]error{
			"Implement the failing phase": errors.New("phase execution failed"),
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, result.Failed)
	assert.Contains(t, buf.String(), "warn: save vessel summary:")
	assert.Equal(t, queue.StateFailed, queueVesselByID(t, q, vessel.ID).State)
}

func TestSmoke_S17_CompleteVesselSavesEvidenceManifestWhenClaimsArePresent(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem-state")

	startedAt := time.Date(2026, time.April, 8, 20, 31, 0, 0, time.UTC)
	vessel := runningSmokeVessel("vessel-with-claims", "github", "fix-bug", startedAt)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	vrs := newVesselRunState(cfg, vessel, startedAt)
	vrs.addPhase(PhaseSummary{Name: "implement", Status: "completed"})
	claims := []evidence.Claim{{
		Claim:     "Implement gate passed",
		Level:     evidence.BehaviorallyChecked,
		Checker:   "make test",
		Phase:     "implement",
		Passed:    true,
		Timestamp: startedAt.Add(time.Second),
	}}

	outcome := r.completeVessel(context.Background(), vessel, "", nil, vrs, claims)
	assert.Equal(t, "completed", outcome)

	manifestPath := filepath.Join(cfg.StateDir, "phases", vessel.ID, "evidence-manifest.json")
	_, err = os.Stat(manifestPath)
	require.NoError(t, err)

	manifest, err := evidence.LoadManifest(cfg.StateDir, vessel.ID)
	require.NoError(t, err)
	assert.Len(t, manifest.Claims, 1)
	assert.Equal(t, "implement", manifest.Claims[0].Phase)

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Equal(t, evidenceManifestRelativePath(vessel.ID), summary.EvidenceManifestPath)
}

func TestSmoke_S18_EvidenceManifestPathIsEmptyInSummaryWhenNoClaimsProvided(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem-state")

	startedAt := time.Date(2026, time.April, 8, 20, 32, 0, 0, time.UTC)
	vessel := runningSmokeVessel("vessel-without-claims", "github", "fix-bug", startedAt)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	vrs := newVesselRunState(cfg, vessel, startedAt)
	vrs.addPhase(PhaseSummary{Name: "implement", Status: "completed"})

	outcome := r.completeVessel(context.Background(), vessel, "", nil, vrs, nil)
	assert.Equal(t, "completed", outcome)

	manifestPath := filepath.Join(cfg.StateDir, "phases", vessel.ID, "evidence-manifest.json")
	_, err = os.Stat(manifestPath)
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Empty(t, summary.EvidenceManifestPath)

	raw := loadSummaryJSON(t, cfg.StateDir, vessel.ID)
	_, ok := raw["evidence_manifest_path"]
	assert.False(t, ok)
}

func TestSmoke_S18a_PersistRunArtifactsWritesCostAndBudgetReviewInputs(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem-state")

	startedAt := time.Date(2026, time.April, 8, 20, 32, 30, 0, time.UTC)
	vessel := runningSmokeVessel("vessel-cost-artifacts", "github", "fix-bug", startedAt)
	vrs := newVesselRunState(cfg, vessel, startedAt)
	require.NotNil(t, vrs.costTracker)

	err := vrs.costTracker.Record(cost.UsageRecord{
		MissionID:    vessel.ID,
		AgentRole:    cost.RoleGenerator,
		Purpose:      cost.PurposeReasoning,
		Model:        "claude-sonnet-4-6",
		InputTokens:  1000,
		OutputTokens: 1000,
		CostUSD:      0.6,
		Timestamp:    startedAt.Add(time.Second),
	})
	require.NoError(t, err)

	r := New(cfg, queue.New(filepath.Join(dir, "queue.jsonl")), &mockWorktree{}, &mockCmdRunner{})
	r.persistRunArtifacts(vessel, string(queue.StateCompleted), vrs, nil, startedAt.Add(2*time.Second))

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Equal(t, costReportRelativePath(vessel.ID), summary.CostReportPath)
	assert.Equal(t, budgetAlertsRelativePath(vessel.ID), summary.BudgetAlertsPath)
	require.NotNil(t, summary.ReviewArtifacts)
	assert.Equal(t, summary.CostReportPath, summary.ReviewArtifacts.CostReport)
	assert.Equal(t, summary.BudgetAlertsPath, summary.ReviewArtifacts.BudgetAlerts)

	report, err := cost.LoadReport(filepath.Join(cfg.StateDir, "phases", vessel.ID, costReportFileName))
	require.NoError(t, err)
	assert.Equal(t, vessel.ID, report.MissionID)
	assert.Equal(t, cost.UsageSourceEstimated, report.UsageSource)

	alertsData, err := os.ReadFile(filepath.Join(cfg.StateDir, "phases", vessel.ID, budgetAlertsFileName))
	require.NoError(t, err)
	assert.JSONEq(t, "[]", string(alertsData))
	assert.Zero(t, summary.BudgetAlertCount)
	assert.False(t, summary.BudgetWarning)
}

func TestPersistRunArtifactsSummarizesBudgetWarnings(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem-state")
	cfg.Cost.Budget = &config.BudgetConfig{MaxCostUSD: 1}

	startedAt := time.Date(2026, time.April, 8, 20, 33, 30, 0, time.UTC)
	vessel := runningSmokeVessel("vessel-budget-warning", "github", "fix-bug", startedAt)
	vrs := newVesselRunState(cfg, vessel, startedAt)
	require.NotNil(t, vrs.costTracker)

	err := vrs.costTracker.Record(cost.UsageRecord{
		MissionID:    vessel.ID,
		AgentRole:    cost.RoleGenerator,
		Purpose:      cost.PurposeReasoning,
		Model:        "claude-sonnet-4-6",
		InputTokens:  1000,
		OutputTokens: 1000,
		CostUSD:      0.85,
		Timestamp:    startedAt.Add(time.Second),
	})
	require.NoError(t, err)

	r := New(cfg, queue.New(filepath.Join(dir, "queue.jsonl")), &mockWorktree{}, &mockCmdRunner{})
	r.persistRunArtifacts(vessel, string(queue.StateCompleted), vrs, nil, startedAt.Add(2*time.Second))

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.True(t, summary.BudgetWarning)
	assert.False(t, summary.BudgetExceeded)
	assert.Equal(t, 1, summary.BudgetAlertCount)

	alertsData, err := os.ReadFile(filepath.Join(cfg.StateDir, "phases", vessel.ID, budgetAlertsFileName))
	require.NoError(t, err)
	assert.Contains(t, string(alertsData), `"type": "warning"`)
}

func TestSmoke_S18b_PersistRunArtifactsLinksExistingEvalReport(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem-state")

	startedAt := time.Date(2026, time.April, 8, 20, 32, 45, 0, time.UTC)
	vessel := runningSmokeVessel("vessel-eval-artifact", "github", "fix-bug", startedAt)
	evalPath := filepath.Join(cfg.StateDir, "phases", vessel.ID, evalReportFileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(evalPath), 0o755))
	require.NoError(t, os.WriteFile(evalPath, []byte(`{"iterations":1}`), 0o644))

	r := New(cfg, queue.New(filepath.Join(dir, "queue.jsonl")), &mockWorktree{}, &mockCmdRunner{})
	r.persistRunArtifacts(vessel, string(queue.StateCompleted), newVesselRunState(cfg, vessel, startedAt), nil, startedAt.Add(time.Second))

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Equal(t, evalReportRelativePath(vessel.ID), summary.EvalReportPath)
	require.NotNil(t, summary.ReviewArtifacts)
	assert.Equal(t, summary.EvalReportPath, summary.ReviewArtifacts.EvalReport)
}

func TestSmoke_S19_FailurePathBuildsSummaryWithStateFailedAndCallsSaveVesselSummary(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem-state")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(19, "ws3-s19")
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "ws3-s19", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the issue",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make analyze\"",
		},
		{
			name:          "implement",
			promptContent: "Implement the fix",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make implement\"\n      retries: 0",
		},
	})

	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer func() {
		require.NoError(t, os.Chdir(oldWd))
	}()

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the issue": []byte("analysis output"),
			"Implement the fix": []byte("implementation output"),
		},
		gateCallResults: []gateCallResult{
			{output: []byte("analyze gate ok"), err: nil},
			{output: []byte("implement gate failed"), err: &mockExitError{code: 1}},
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, result.Failed)
	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Equal(t, "failed", summary.State)
	assert.Len(t, summary.Phases, 2)
	assert.Equal(t, "completed", summary.Phases[0].Status)
	assert.Equal(t, "failed", summary.Phases[1].Status)
	require.NotEmpty(t, summary.FailureReviewPath)
	require.NotNil(t, summary.Recovery)
	artifact, err := recovery.Load(filepath.Join(cfg.StateDir, summary.FailureReviewPath))
	require.NoError(t, err)
	assert.Equal(t, summary.Recovery.Class, string(artifact.RecoveryClass))
	assert.Equal(t, summary.Recovery.Action, string(artifact.RecoveryAction))
	assert.Equal(t, summary.Recovery.FollowUpRoute, artifact.FollowUpRoute)
	assert.Equal(t, summary.Recovery.RetrySuppressed, artifact.RetrySuppressed)
	assert.Equal(t, summary.Recovery.RetryOutcome, artifact.RetryOutcome)
	assert.Equal(t, summary.Recovery.UnlockDimension, artifact.UnlockDimension)
	require.NotNil(t, summary.ReviewArtifacts)
	assert.Equal(t, summary.FailureReviewPath, summary.ReviewArtifacts.FailureReview)
}

func TestSmoke_S20_BudgetMaxCostUSDAndBudgetMaxTokensAppearInSummaryWhenBudgetIsConfigured(t *testing.T) {
	stateDir := t.TempDir()
	startedAt := time.Date(2026, time.April, 8, 20, 33, 0, 0, time.UTC)
	cfg := &config.Config{
		Cost: config.CostConfig{
			Budget: &config.BudgetConfig{
				MaxCostUSD: 1.0,
				MaxTokens:  50000,
			},
		},
	}
	vessel := queue.Vessel{
		ID:       "vessel-budget-limits",
		Source:   "manual",
		Workflow: "fix-bug",
	}

	vrs := newVesselRunState(cfg, vessel, startedAt)
	vrs.addPhase(PhaseSummary{Name: "implement", Status: "completed"})

	err := SaveVesselSummary(stateDir, vrs.buildSummary("completed", startedAt.Add(30*time.Second)))
	require.NoError(t, err)

	summary := loadSummary(t, stateDir, vessel.ID)
	require.NotNil(t, summary.BudgetMaxCostUSD)
	require.NotNil(t, summary.BudgetMaxTokens)
	assert.Equal(t, 1.0, *summary.BudgetMaxCostUSD)
	assert.Equal(t, 50000, *summary.BudgetMaxTokens)
}

func TestSaveVesselSummaryWritesEmptyPhasesArray(t *testing.T) {
	stateDir := t.TempDir()
	summary := &VesselSummary{
		VesselID: "vessel-empty-phases",
		Source:   "manual",
		State:    "completed",
	}

	if err := SaveVesselSummary(stateDir, summary); err != nil {
		t.Fatalf("SaveVesselSummary() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "phases", "vessel-empty-phases", summaryFileName))
	if err != nil {
		t.Fatalf("read summary file: %v", err)
	}
	if !strings.Contains(string(data), "\"phases\": []") {
		t.Fatalf("summary.json = %s, want empty phases array", string(data))
	}
}

func TestSaveVesselSummaryRejectsInvalidInput(t *testing.T) {
	t.Run("nil summary", func(t *testing.T) {
		err := SaveVesselSummary(t.TempDir(), nil)
		if err == nil {
			t.Fatal("SaveVesselSummary() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "summary must not be nil") {
			t.Fatalf("SaveVesselSummary() error = %v, want nil-summary message", err)
		}
	})

	t.Run("unsafe vessel id", func(t *testing.T) {
		err := SaveVesselSummary(t.TempDir(), &VesselSummary{
			VesselID: "../escape",
			Source:   "manual",
			State:    "failed",
		})
		if err == nil {
			t.Fatal("SaveVesselSummary() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "invalid vessel ID") {
			t.Fatalf("SaveVesselSummary() error = %v, want invalid vessel ID", err)
		}
	})
}

func TestVesselRunStateBuildSummaryIncludesBudgetLimits(t *testing.T) {
	startedAt := time.Now().Add(-2 * time.Minute).UTC()
	cfg := &config.Config{
		Cost: config.CostConfig{
			Budget: &config.BudgetConfig{
				MaxCostUSD: 1.0,
				MaxTokens:  50000,
			},
		},
	}
	vrs := newVesselRunState(cfg, queue.Vessel{
		ID:       "vessel-budget",
		Source:   "manual",
		Workflow: "fix-bug",
	}, startedAt)
	vrs.addPhase(PhaseSummary{
		Name:            "implement",
		Status:          "completed",
		InputTokensEst:  100,
		OutputTokensEst: 50,
		CostUSDEst:      0.25,
	})

	summary := vrs.buildSummary("completed", startedAt.Add(30*time.Second))
	if got, want := summary.TotalInputTokensEst, 100; got != want {
		t.Fatalf("TotalInputTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalOutputTokensEst, 50; got != want {
		t.Fatalf("TotalOutputTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalTokensEst, 150; got != want {
		t.Fatalf("TotalTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalCostUSDEst, 0.25; got != want {
		t.Fatalf("TotalCostUSDEst = %v, want %v", got, want)
	}
	if summary.BudgetMaxCostUSD == nil || *summary.BudgetMaxCostUSD != 1.0 {
		t.Fatalf("BudgetMaxCostUSD = %#v, want 1.0", summary.BudgetMaxCostUSD)
	}
	if summary.BudgetMaxTokens == nil || *summary.BudgetMaxTokens != 50000 {
		t.Fatalf("BudgetMaxTokens = %#v, want 50000", summary.BudgetMaxTokens)
	}
}

func TestVesselRunStateBuildSummaryCopiesPhasesInInsertionOrder(t *testing.T) {
	vrs := newVesselRunState(nil, queue.Vessel{
		ID:       "vessel-order",
		Source:   "manual",
		Workflow: "fix-bug",
	}, time.Now().UTC())

	vrs.addPhase(PhaseSummary{Name: "plan", Status: "completed"})
	vrs.addPhase(PhaseSummary{Name: "implement", Status: "failed", Error: "exit status 1"})
	vrs.addPhase(PhaseSummary{Name: "test", Status: "no-op"})

	summary := vrs.buildSummary("failed", time.Now().UTC())

	if got, want := len(summary.Phases), 3; got != want {
		t.Fatalf("len(summary.Phases) = %d, want %d", got, want)
	}
	if got := summary.Phases[0].Name; got != "plan" {
		t.Fatalf("summary.Phases[0].Name = %q, want plan", got)
	}
	if got := summary.Phases[1].Name; got != "implement" {
		t.Fatalf("summary.Phases[1].Name = %q, want implement", got)
	}
	if got := summary.Phases[1].Error; got != "exit status 1" {
		t.Fatalf("summary.Phases[1].Error = %q, want exit status 1", got)
	}
	if got := summary.Phases[2].Status; got != "no-op" {
		t.Fatalf("summary.Phases[2].Status = %q, want no-op", got)
	}

	summary.Phases[0].Name = "mutated"
	rebuilt := vrs.buildSummary("failed", time.Now().UTC())
	if got := rebuilt.Phases[0].Name; got != "plan" {
		t.Fatalf("rebuilt.Phases[0].Name = %q, want plan after mutating prior summary", got)
	}
}

func TestVesselRunStateBuildSummaryAggregatesTotalsAndStatus(t *testing.T) {
	startedAt := time.Date(2026, time.April, 8, 20, 0, 0, 0, time.UTC)
	vrs := newVesselRunState(nil, queue.Vessel{
		ID:       "vessel-summary",
		Source:   "manual",
		Workflow: "fix-bug",
	}, startedAt)
	vrs.addPhase(PhaseSummary{
		Name:            "plan",
		Status:          "completed",
		InputTokensEst:  100,
		OutputTokensEst: 50,
		CostUSDEst:      0.0012,
	})
	vrs.addPhase(PhaseSummary{
		Name:            "test",
		Status:          "failed",
		InputTokensEst:  200,
		OutputTokensEst: 80,
		CostUSDEst:      0.0034,
		Error:           "exit status 1",
	})

	summary := vrs.buildSummary("failed", startedAt.Add(3*time.Second))
	if got, want := summary.TotalInputTokensEst, 300; got != want {
		t.Fatalf("TotalInputTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalOutputTokensEst, 130; got != want {
		t.Fatalf("TotalOutputTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalTokensEst, 430; got != want {
		t.Fatalf("TotalTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalCostUSDEst, 0.0046; got != want {
		t.Fatalf("TotalCostUSDEst = %v, want %v", got, want)
	}
	if got, want := summary.DurationMS, int64(3000); got != want {
		t.Fatalf("DurationMS = %d, want %d", got, want)
	}
	if got := summary.Phases[0].Status; got != "completed" {
		t.Fatalf("Phases[0].Status = %q, want completed", got)
	}
	if got := summary.Phases[1].Status; got != "failed" {
		t.Fatalf("Phases[1].Status = %q, want failed", got)
	}
	if got := summary.Phases[1].Error; got != "exit status 1" {
		t.Fatalf("Phases[1].Error = %q, want exit status 1", got)
	}
}

func TestSmoke_S15_BuildGateClaimWithEvidenceMetadataProducesATypedClaim(t *testing.T) {
	recordedAt := time.Date(2026, time.March, 31, 12, 0, 0, 0, time.UTC)
	artifactPath := phaseArtifactRelativePath("vessel-1", "implement")
	claim := buildGateClaim(workflow.Phase{
		Name: "implement",
		Gate: &workflow.Gate{
			Run: "go test ./...",
			Evidence: &workflow.GateEvidence{
				Claim:         "All tests pass",
				Level:         "behaviorally_checked",
				Checker:       "go test",
				TrustBoundary: "Package-level only",
			},
		},
	}, true, artifactPath, recordedAt)

	assert.Equal(t, "All tests pass", claim.Claim)
	assert.Equal(t, evidence.BehaviorallyChecked, claim.Level)
	assert.Equal(t, "go test", claim.Checker)
	assert.Equal(t, "Package-level only", claim.TrustBoundary)
	assert.True(t, claim.Passed)
	assert.Equal(t, artifactPath, claim.ArtifactPath)
	assert.Equal(t, "implement", claim.Phase)
	assert.True(t, claim.Timestamp.Equal(recordedAt))
}

func TestSmoke_S16_BuildGateClaimWithoutEvidenceMetadataProducesAnUntypedClaim(t *testing.T) {
	recordedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	artifactPath := phaseArtifactRelativePath("vessel-1", "implement")
	claim := buildGateClaim(workflow.Phase{
		Name: "implement",
		Gate: &workflow.Gate{Run: "cd cli && go test ./..."},
	}, true, artifactPath, recordedAt)

	assert.Equal(t, evidence.Untyped, claim.Level)
	assert.Equal(t, "No trust boundary declared", claim.TrustBoundary)
	assert.Contains(t, claim.Claim, "implement")
	assert.True(t, claim.Passed)
	assert.Equal(t, artifactPath, claim.ArtifactPath)
	assert.Equal(t, "implement", claim.Phase)
	assert.True(t, claim.Timestamp.Equal(recordedAt))
}

func TestBuildGateClaimDefaultsLiveGatesToObservedInSitu(t *testing.T) {
	recordedAt := time.Date(2026, time.April, 2, 8, 30, 0, 0, time.UTC)
	artifactPath := "phases/vessel-1/evidence/implement/live-gate.json"
	claim := buildGateClaim(workflow.Phase{
		Name: "implement",
		Gate: &workflow.Gate{
			Type: "live",
			Live: &workflow.LiveGate{Mode: "http"},
		},
	}, true, artifactPath, recordedAt)

	if claim.Level != evidence.ObservedInSitu {
		t.Fatalf("Level = %q, want %q", claim.Level, evidence.ObservedInSitu)
	}
	if claim.Checker != "live/http" {
		t.Fatalf("Checker = %q, want %q", claim.Checker, "live/http")
	}
	if claim.TrustBoundary != "Running system observation" {
		t.Fatalf("TrustBoundary = %q, want %q", claim.TrustBoundary, "Running system observation")
	}
	if claim.ArtifactPath != artifactPath {
		t.Fatalf("ArtifactPath = %q, want %q", claim.ArtifactPath, artifactPath)
	}
}

func TestSmoke_S17_BuildGateClaimSetsCheckerFromGateRunCommandWhenNoEvidence(t *testing.T) {
	claim := buildGateClaim(workflow.Phase{
		Name: "implement",
		Gate: &workflow.Gate{Run: "cd cli && go test ./..."},
	}, true, phaseArtifactRelativePath("vessel-1", "implement"), time.Date(2026, time.April, 1, 8, 31, 0, 0, time.UTC))

	assert.Equal(t, "cd cli && go test ./...", claim.Checker)
}

func TestSmoke_S6_EvidenceCollectionFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	now := time.Date(2026, time.April, 2, 9, 0, 0, 0, time.UTC)
	vessel := runningSmokeVessel("vessel-ws6-s6", "github", "test-workflow", now)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	buf := captureStandardLogger(t)

	vrs := newVesselRunState(cfg, vessel, now)
	vrs.addPhase(PhaseSummary{Name: "implement", Status: "completed"})
	claims := []evidence.Claim{{
		Claim:     "gate check",
		Level:     evidence.Level("bogus-level"),
		Phase:     "implement",
		Passed:    true,
		Timestamp: now,
	}}

	outcome := r.completeVessel(context.Background(), vessel, "", nil, vrs, claims)

	assert.Equal(t, "completed", outcome)
	assert.Equal(t, queue.StateCompleted, queueVesselByID(t, q, vessel.ID).State)
	assert.Contains(t, buf.String(), "warn: save evidence manifest:")

	manifestPath := filepath.Join(cfg.StateDir, "phases", vessel.ID, "evidence-manifest.json")
	assert.NoFileExists(t, manifestPath)

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Equal(t, "completed", summary.State)
	assert.Empty(t, summary.EvidenceManifestPath)
}

func TestSmoke_S7_SummaryWriteFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	now := time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC)
	vessel := runningSmokeVessel("vessel-ws6-s7", "github", "test-workflow", now)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	summaryAsDir := filepath.Join(cfg.StateDir, "phases", vessel.ID, summaryFileName)
	require.NoError(t, os.MkdirAll(summaryAsDir, 0o755))

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	buf := captureStandardLogger(t)

	vrs := newVesselRunState(cfg, vessel, now)
	vrs.addPhase(PhaseSummary{Name: "implement", Status: "completed"})

	outcome := r.completeVessel(context.Background(), vessel, "", nil, vrs, nil)

	assert.Equal(t, "completed", outcome)
	assert.Equal(t, queue.StateCompleted, queueVesselByID(t, q, vessel.ID).State)
	assert.Contains(t, buf.String(), "warn: save vessel summary:")
	assert.DirExists(t, summaryAsDir)
}

func TestDrainPromptOnlyWritesSummaryArtifact(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "Prompt-only workflow summary smoke"))

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Prompt-only workflow summary smoke": []byte("Prompt-only completion output for summary telemetry"),
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	summary := loadSummary(t, cfg.StateDir, "prompt-1")
	if summary.State != "completed" {
		t.Fatalf("State = %q, want completed", summary.State)
	}
	if len(summary.Phases) != 1 {
		t.Fatalf("len(Phases) = %d, want 1", len(summary.Phases))
	}
	if summary.Phases[0].Name != "prompt" {
		t.Fatalf("Phases[0].Name = %q, want prompt", summary.Phases[0].Name)
	}
	if summary.Phases[0].Type != "prompt" {
		t.Fatalf("Phases[0].Type = %q, want prompt", summary.Phases[0].Type)
	}
	if summary.Phases[0].UsageSource != cost.UsageSourceEstimated {
		t.Fatalf("Phases[0].UsageSource = %q, want %q", summary.Phases[0].UsageSource, cost.UsageSourceEstimated)
	}
	if summary.TotalTokensEst <= 0 {
		t.Fatalf("TotalTokensEst = %d, want > 0", summary.TotalTokensEst)
	}
	if summary.EvidenceManifestPath != "" {
		t.Fatalf("EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
	}

	manifestPath := filepath.Join(cfg.StateDir, "phases", "prompt-1", "evidence-manifest.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("expected no evidence manifest, got err=%v", err)
	}
}

func TestDrainWritesFailureSummaryAndEvidenceManifest(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "artifact-failure"))

	writeWorkflowFile(t, dir, "artifact-failure", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the issue",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make analyze\"\n      evidence:\n        claim: \"Analyze gate passed\"\n        level: behaviorally_checked\n        checker: \"make analyze\"\n        trust_boundary: \"Analysis scope only\"",
		},
		{
			name:          "implement",
			promptContent: "Implement the fix",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make implement\"\n      retries: 0",
		},
	})

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the issue": []byte("analysis output"),
			"Implement the fix": []byte("implementation output"),
		},
		gateCallResults: []gateCallResult{
			{output: []byte("analyze gate ok"), err: nil},
			{output: []byte("implement gate failed"), err: &mockExitError{code: 1}},
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", result.Failed)
	}

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	if summary.State != "failed" {
		t.Fatalf("State = %q, want failed", summary.State)
	}
	if len(summary.Phases) != 2 {
		t.Fatalf("len(Phases) = %d, want 2", len(summary.Phases))
	}
	if summary.Phases[0].Status != "completed" {
		t.Fatalf("Phases[0].Status = %q, want completed", summary.Phases[0].Status)
	}
	if summary.Phases[1].Status != "failed" {
		t.Fatalf("Phases[1].Status = %q, want failed", summary.Phases[1].Status)
	}
	if summary.EvidenceManifestPath != evidenceManifestRelativePath("issue-1") {
		t.Fatalf("EvidenceManifestPath = %q, want %q", summary.EvidenceManifestPath, evidenceManifestRelativePath("issue-1"))
	}

	manifest, err := evidence.LoadManifest(cfg.StateDir, "issue-1")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(manifest.Claims) != 1 {
		t.Fatalf("len(Claims) = %d, want 1", len(manifest.Claims))
	}
	if manifest.Claims[0].Phase != "analyze" {
		t.Fatalf("Claims[0].Phase = %q, want analyze", manifest.Claims[0].Phase)
	}
	for _, claim := range manifest.Claims {
		if claim.Phase == "implement" {
			t.Fatalf("unexpected claim for failed phase: %+v", claim)
		}
	}
}

func TestSmoke_S18_EvidenceClaimsAreAccumulatedAcrossMultiplePhases(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(18, "ws4-s18")
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "ws4-s18", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the issue",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make analyze\"\n      evidence:\n        claim: \"Analyze gate passed\"\n        level: behaviorally_checked\n        checker: \"make analyze\"\n        trust_boundary: \"Analysis scope only\"",
		},
		{
			name:          "implement",
			promptContent: "Implement the fix",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make implement\"\n      evidence:\n        claim: \"Implement gate passed\"\n        level: mechanically_checked\n        checker: \"make implement\"\n        trust_boundary: \"Implementation scope only\"",
		},
	})

	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the issue": []byte("analysis output"),
			"Implement the fix": []byte("implementation output"),
		},
		gateCallResults: []gateCallResult{
			{output: []byte("analyze gate ok"), err: nil},
			{output: []byte("implement gate ok"), err: nil},
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	manifestPath := filepath.Join(cfg.StateDir, "phases", vessel.ID, "evidence-manifest.json")
	assert.FileExists(t, manifestPath)

	manifest, err := evidence.LoadManifest(cfg.StateDir, vessel.ID)
	require.NoError(t, err)
	require.Len(t, manifest.Claims, 2)
	assert.Equal(t, "analyze", manifest.Claims[0].Phase)
	assert.Equal(t, "implement", manifest.Claims[1].Phase)
	assert.Equal(t, "Analyze gate passed", manifest.Claims[0].Claim)
	assert.Equal(t, "Implement gate passed", manifest.Claims[1].Claim)
}

func TestSmoke_S19_GateFailureProducesNoClaimButPreservesClaimsFromPriorPhases(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(19, "ws4-s19")
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "ws4-s19", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the bug",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make analyze\"\n      evidence:\n        claim: \"Analyze gate passed\"\n        level: behaviorally_checked\n        checker: \"make analyze\"",
		},
		{
			name:          "implement",
			promptContent: "Implement the bugfix",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make implement\"\n      evidence:\n        claim: \"Implement gate passed\"\n        level: mechanically_checked\n        checker: \"make implement\"",
		},
		{
			name:          "verify",
			promptContent: "Verify the rollout",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make verify\"\n      retries: 0\n      evidence:\n        claim: \"Verify gate passed\"\n        level: observed_in_situ\n        checker: \"make verify\"",
		},
	})

	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the bug":      []byte("analysis output"),
			"Implement the bugfix": []byte("implementation output"),
			"Verify the rollout":   []byte("verification output"),
		},
		gateCallResults: []gateCallResult{
			{output: []byte("phase-1 gate ok"), err: nil},
			{output: []byte("phase-2 gate ok"), err: nil},
			{output: []byte("phase-3 gate failed"), err: &mockExitError{code: 1}},
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, queue.StateFailed, queueVesselByID(t, q, vessel.ID).State)

	manifest, err := evidence.LoadManifest(cfg.StateDir, vessel.ID)
	require.NoError(t, err)
	require.Len(t, manifest.Claims, 2)
	assert.Equal(t, "analyze", manifest.Claims[0].Phase)
	assert.Equal(t, "implement", manifest.Claims[1].Phase)
	assert.NotContains(t, []string{manifest.Claims[0].Phase, manifest.Claims[1].Phase}, "verify")
}

func TestSmoke_S8_ClaimsFromPriorCompletedPhasesPreservedWhenALaterPhaseFails(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(8, "ws6-s8"))

	writeWorkflowFile(t, dir, "ws6-s8", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the bug",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make analyze\"\n      evidence:\n        claim: \"Analyze gate passed\"\n        level: behaviorally_checked\n        checker: \"make analyze\"",
		},
		{
			name:          "implement",
			promptContent: "Implement the fix",
			maxTurns:      5,
		},
	})

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the bug": []byte("analysis done"),
		},
		phaseErrByPrompt: map[string]error{
			"Implement the fix": errors.New("phase execution failed"),
		},
		gateCallResults: []gateCallResult{
			{output: []byte("analyze gate ok"), err: nil},
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", result.Failed)
	}

	manifest, err := evidence.LoadManifest(cfg.StateDir, "issue-8")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(manifest.Claims) != 1 {
		t.Fatalf("len(Claims) = %d, want 1", len(manifest.Claims))
	}
	if manifest.Claims[0].Phase != "analyze" {
		t.Fatalf("Claims[0].Phase = %q, want analyze", manifest.Claims[0].Phase)
	}
	for _, claim := range manifest.Claims {
		if claim.Phase == "implement" {
			t.Fatalf("unexpected claim for failed phase: %+v", claim)
		}
	}
}

func TestSmoke_S9_ClaimsFromAFailedPhaseAreDiscarded(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(9, "ws6-s9"))

	writeWorkflowFile(t, dir, "ws6-s9", []testPhase{
		{
			name:          "implement",
			promptContent: "Implement the feature",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make test\"\n      evidence:\n        claim: \"Tests pass\"\n        level: behaviorally_checked",
		},
	})

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseErrByPrompt: map[string]error{
			"Implement the feature": errors.New("phase execution failed"),
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, queue.StateFailed, queueVesselByID(t, q, "issue-9").State)

	manifestPath := filepath.Join(cfg.StateDir, "phases", "issue-9", "evidence-manifest.json")
	if _, err := os.Stat(manifestPath); err == nil {
		manifest, loadErr := evidence.LoadManifest(cfg.StateDir, "issue-9")
		require.NoError(t, loadErr)
		assert.Empty(t, manifest.Claims)
	} else {
		require.True(t, os.IsNotExist(err))
	}
}

func TestDrainOrchestratedWritesSummaryManifestAndReporterEvidence(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(42, "artifact-orchestrated"))

	writeWorkflowFile(t, dir, "artifact-orchestrated", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the bug",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make analyze\"\n      evidence:\n        claim: \"Analyze gate passed\"\n        level: behaviorally_checked\n        checker: \"make analyze\"\n        trust_boundary: \"Analysis wave only\"",
		},
		{
			name:          "implement",
			promptContent: "Implement the bugfix",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make implement\"\n      evidence:\n        claim: \"Implement gate passed\"\n        level: mechanically_checked\n        checker: \"make implement\"\n        trust_boundary: \"Implementation wave only\"",
		},
		{
			name:          "finalize",
			promptContent: "Finalize the work",
			maxTurns:      3,
			dependsOn:     []string{"analyze", "implement"},
		},
	})

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the bug":      []byte("analysis output"),
			"Implement the bugfix": []byte("implementation output"),
			"Finalize the work":    []byte("final output"),
		},
		gateCallResults: []gateCallResult{
			{output: []byte("analyze gate ok"), err: nil},
			{output: []byte("implement gate ok"), err: nil},
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	r.Reporter = &reporter.Reporter{Runner: cmdRunner, Repo: "owner/repo"}

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	summary := loadSummary(t, cfg.StateDir, "issue-42")
	if summary.State != "completed" {
		t.Fatalf("State = %q, want completed", summary.State)
	}
	if len(summary.Phases) != 3 {
		t.Fatalf("len(Phases) = %d, want 3", len(summary.Phases))
	}
	if summary.EvidenceManifestPath != evidenceManifestRelativePath("issue-42") {
		t.Fatalf("EvidenceManifestPath = %q, want %q", summary.EvidenceManifestPath, evidenceManifestRelativePath("issue-42"))
	}

	manifest, err := evidence.LoadManifest(cfg.StateDir, "issue-42")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(manifest.Claims) != 2 {
		t.Fatalf("len(Claims) = %d, want 2", len(manifest.Claims))
	}
	if got := manifest.Claims[0].Phase; got != "analyze" {
		t.Fatalf("Claims[0].Phase = %q, want analyze", got)
	}
	if got := manifest.Claims[1].Phase; got != "implement" {
		t.Fatalf("Claims[1].Phase = %q, want implement", got)
	}

	if !strings.Contains(cmdRunner.lastBody, "### Verification evidence") {
		t.Fatalf("expected evidence section in completion comment, got: %s", cmdRunner.lastBody)
	}
	if !strings.Contains(cmdRunner.lastBody, "Analyze gate passed") {
		t.Fatalf("expected analyze claim in completion comment, got: %s", cmdRunner.lastBody)
	}
}

func TestDrainWorkflowWithoutGateOmitsEvidenceFromCompletionComment(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(7, "artifact-no-evidence"))

	writeWorkflowFile(t, dir, "artifact-no-evidence", []testPhase{
		{name: "analyze", promptContent: "Analyze the issue", maxTurns: 5},
		{name: "implement", promptContent: "Implement the fix", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the issue": []byte("analysis output"),
			"Implement the fix": []byte("implementation output"),
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	r.Reporter = &reporter.Reporter{Runner: cmdRunner, Repo: "owner/repo"}

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	summary := loadSummary(t, cfg.StateDir, "issue-7")
	if summary.State != "completed" {
		t.Fatalf("State = %q, want completed", summary.State)
	}
	if len(summary.Phases) != 2 {
		t.Fatalf("len(Phases) = %d, want 2", len(summary.Phases))
	}
	if summary.EvidenceManifestPath != "" {
		t.Fatalf("EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
	}

	manifestPath := filepath.Join(cfg.StateDir, "phases", "issue-7", "evidence-manifest.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("expected no evidence manifest, got err=%v", err)
	}

	if !strings.Contains(cmdRunner.lastBody, "**xylem — all phases completed**") {
		t.Fatalf("expected completion header in comment, got: %s", cmdRunner.lastBody)
	}
	if strings.Contains(cmdRunner.lastBody, "### Verification evidence") {
		t.Fatalf("expected no evidence section in completion comment, got: %s", cmdRunner.lastBody)
	}
}

func loadSummary(t *testing.T, stateDir, vesselID string) VesselSummary {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(stateDir, "phases", vesselID, summaryFileName))
	require.NoError(t, err)

	var summary VesselSummary
	require.NoError(t, json.Unmarshal(data, &summary))

	return summary
}

func loadSummaryJSON(t *testing.T, stateDir, vesselID string) map[string]any {
	t.Helper()

	data := readSummaryBytes(t, stateDir, vesselID)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	return raw
}

func readSummaryBytes(t *testing.T, stateDir, vesselID string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(stateDir, "phases", vesselID, summaryFileName))
	require.NoError(t, err)

	return data
}

func queueVesselByID(t *testing.T, q *queue.Queue, vesselID string) queue.Vessel {
	t.Helper()

	vessels, err := q.List()
	require.NoError(t, err)

	for _, vessel := range vessels {
		if vessel.ID == vesselID {
			return vessel
		}
	}

	t.Fatalf("vessel %q not found in queue", vesselID)
	return queue.Vessel{}
}

func runningSmokeVessel(id, sourceName, workflowName string, startedAt time.Time) queue.Vessel {
	return queue.Vessel{
		ID:        id,
		Source:    sourceName,
		Workflow:  workflowName,
		State:     queue.StateRunning,
		CreatedAt: startedAt.Add(-time.Minute),
		StartedAt: &startedAt,
	}
}

func captureStandardLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	standardLoggerMu.Lock()
	buf := &bytes.Buffer{}
	prevWriter := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		standardLoggerMu.Unlock()
	})

	return buf
}
