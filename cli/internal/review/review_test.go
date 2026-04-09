package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAggregatesReviewRecommendations(t *testing.T) {
	stateDir := t.TempDir()
	base := time.Date(2026, time.April, 8, 12, 0, 0, 0, time.UTC)

	for i := range 6 {
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:  "stable-run-" + string(rune('a'+i)),
			source:    "github-issue",
			workflow:  "stable",
			state:     "completed",
			startedAt: base.Add(time.Duration(i) * time.Minute),
			endedAt:   base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			phases: []runner.PhaseSummary{{
				Name:       "implement",
				Type:       "prompt",
				Status:     "completed",
				CostUSDEst: 0.15,
			}},
			totalCost: 0.15,
			costReport: &cost.CostReport{
				MissionID:    "stable",
				TotalTokens:  100,
				TotalCostUSD: 0.15,
				GeneratedAt:  base,
				ByRole:       map[cost.AgentRole]float64{cost.RoleGenerator: 0.15},
				ByPurpose:    map[cost.Purpose]float64{cost.PurposeReasoning: 0.15},
				ByModel:      map[string]float64{"claude-sonnet-4-6": 0.15},
				RecordCount:  1,
			},
		})
	}

	for i := range 3 {
		claimPhase := "implement"
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:  "broken-run-" + string(rune('a'+i)),
			source:    "github-issue",
			workflow:  "broken",
			state:     "failed",
			startedAt: base.Add(time.Duration(10+i) * time.Minute),
			endedAt:   base.Add(time.Duration(10+i)*time.Minute + 30*time.Second),
			phases: []runner.PhaseSummary{{
				Name:       "implement",
				Type:       "prompt",
				Status:     "failed",
				Error:      "gate failed",
				CostUSDEst: 0.4,
			}},
			totalCost: 0.4,
			manifest: &evidence.Manifest{
				VesselID: "broken-run-" + string(rune('a'+i)),
				Workflow: "broken",
				Claims: []evidence.Claim{{
					Claim:     "implementation gate",
					Phase:     claimPhase,
					Passed:    false,
					Timestamp: base,
				}},
				CreatedAt: base,
			},
			evalReport: &evaluator.LoopResult{
				FinalResult: &evaluator.EvalResult{Pass: false},
				Iterations:  1,
			},
		})
	}

	writeRunArtifacts(t, stateDir, runFixture{
		vesselID:  "costly-run-a",
		source:    "github-issue",
		workflow:  "costly",
		state:     "completed",
		startedAt: base.Add(20 * time.Minute),
		endedAt:   base.Add(21 * time.Minute),
		totalCost: 0.10,
		costReport: &cost.CostReport{
			MissionID:    "costly-a",
			TotalTokens:  100,
			TotalCostUSD: 0.10,
			GeneratedAt:  base,
			ByRole:       map[cost.AgentRole]float64{cost.RoleGenerator: 0.10},
			ByPurpose:    map[cost.Purpose]float64{cost.PurposeReasoning: 0.10},
			ByModel:      map[string]float64{"claude-sonnet-4-6": 0.10},
			RecordCount:  1,
		},
	})
	writeRunArtifacts(t, stateDir, runFixture{
		vesselID:  "costly-run-b",
		source:    "github-issue",
		workflow:  "costly",
		state:     "completed",
		startedAt: base.Add(22 * time.Minute),
		endedAt:   base.Add(23 * time.Minute),
		totalCost: 0.12,
		costReport: &cost.CostReport{
			MissionID:    "costly-b",
			TotalTokens:  120,
			TotalCostUSD: 0.12,
			GeneratedAt:  base,
			ByRole:       map[cost.AgentRole]float64{cost.RoleGenerator: 0.12},
			ByPurpose:    map[cost.Purpose]float64{cost.PurposeReasoning: 0.12},
			ByModel:      map[string]float64{"claude-sonnet-4-6": 0.12},
			RecordCount:  1,
		},
	})
	writeRunArtifacts(t, stateDir, runFixture{
		vesselID:  "costly-run-c",
		source:    "github-issue",
		workflow:  "costly",
		state:     "completed",
		startedAt: base.Add(24 * time.Minute),
		endedAt:   base.Add(25 * time.Minute),
		totalCost: 1.00,
		costReport: &cost.CostReport{
			MissionID:    "costly-c",
			TotalTokens:  1500,
			TotalCostUSD: 1.00,
			GeneratedAt:  base,
			ByRole:       map[cost.AgentRole]float64{cost.RoleGenerator: 1.00},
			ByPurpose:    map[cost.Purpose]float64{cost.PurposeReasoning: 1.00},
			ByModel:      map[string]float64{"claude-sonnet-4-6": 1.00},
			RecordCount:  1,
		},
	})

	result, err := Generate(stateDir, Options{LookbackRuns: 20, MinSamples: 3, OutputDir: "reviews", Now: base.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	stable := findGroup(t, result.Report.Groups, "github-issue", "stable", "implement")
	if stable.Recommendation != RecommendationPruneCandidate {
		t.Fatalf("stable recommendation = %q, want %q", stable.Recommendation, RecommendationPruneCandidate)
	}

	broken := findGroup(t, result.Report.Groups, "github-issue", "broken", "implement")
	if broken.Recommendation != RecommendationInvestigate {
		t.Fatalf("broken recommendation = %q, want %q", broken.Recommendation, RecommendationInvestigate)
	}

	costlyRun := findGroup(t, result.Report.Groups, "github-issue", "costly", runPhaseName)
	if costlyRun.Recommendation != RecommendationInvestigate {
		t.Fatalf("costly run recommendation = %q, want %q", costlyRun.Recommendation, RecommendationInvestigate)
	}

	if len(result.Report.CostAnomalies) == 0 {
		t.Fatal("CostAnomalies = 0, want at least one anomaly")
	}
	if _, err := os.Stat(result.JSONPath); err != nil {
		t.Fatalf("Stat(JSONPath) error = %v", err)
	}
	if _, err := os.Stat(result.MarkdownPath); err != nil {
		t.Fatalf("Stat(MarkdownPath) error = %v", err)
	}
}

func TestGenerateToleratesMissingOptionalArtifacts(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 8, 13, 0, 0, 0, time.UTC)
	requireSummaryOnly(t, stateDir, runner.VesselSummary{
		VesselID:   "summary-only",
		Source:     "manual",
		Workflow:   "ad-hoc",
		State:      "completed",
		StartedAt:  now,
		EndedAt:    now.Add(time.Minute),
		DurationMS: time.Minute.Milliseconds(),
		Phases:     []runner.PhaseSummary{},
		Note:       "test",
	})

	result, err := Generate(stateDir, Options{MinSamples: 2})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	group := findGroup(t, result.Report.Groups, "manual", "ad-hoc", runPhaseName)
	if group.Recommendation != RecommendationInsufficientData {
		t.Fatalf("recommendation = %q, want %q", group.Recommendation, RecommendationInsufficientData)
	}
}

func TestSmoke_S1_LoadRunsIncludesRecoveryArtifactWhenPresent(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 8, 13, 30, 0, 0, time.UTC)

	writeRunArtifacts(t, stateDir, runFixture{
		vesselID:  "failed-with-recovery",
		source:    "github-issue",
		workflow:  "implement-harness",
		state:     "failed",
		startedAt: now,
		endedAt:   now.Add(time.Minute),
		recoveryArtifact: recovery.Build(recovery.Input{
			VesselID:    "failed-with-recovery",
			Source:      "github-issue",
			Workflow:    "implement-harness",
			State:       queue.StateFailed,
			FailedPhase: "analyze",
			Error:       "missing requirement: acceptance criteria are ambiguous",
			CreatedAt:   now.Add(time.Minute),
		}),
	})

	runs, _, _, err := LoadRuns(stateDir, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.NotNil(t, runs[0].Recovery)
	assert.Equal(t, recovery.ClassSpecGap, runs[0].Recovery.RecoveryClass)
	assert.Equal(t, recovery.ActionRefine, runs[0].Recovery.RecoveryAction)
	assert.Equal(t, "needs-refinement", runs[0].Recovery.FollowUpRoute)
	assert.True(t, runs[0].Recovery.RetrySuppressed)
	assert.Equal(t, "suppressed", runs[0].Recovery.RetryOutcome)
}

type runFixture struct {
	vesselID         string
	source           string
	workflow         string
	state            string
	startedAt        time.Time
	endedAt          time.Time
	phases           []runner.PhaseSummary
	totalInput       int
	totalOutput      int
	totalCost        float64
	manifest         *evidence.Manifest
	costReport       *cost.CostReport
	budgetAlerts     []cost.BudgetAlert
	evalReport       *evaluator.LoopResult
	recoveryArtifact *recovery.Artifact
}

func writeRunArtifacts(t *testing.T, stateDir string, fixture runFixture) {
	t.Helper()
	totalInput := fixture.totalInput
	totalOutput := fixture.totalOutput
	if totalInput == 0 && totalOutput == 0 {
		for _, phase := range fixture.phases {
			totalInput += phase.InputTokensEst
			totalOutput += phase.OutputTokensEst
		}
	}
	summary := runner.VesselSummary{
		VesselID:             fixture.vesselID,
		Source:               fixture.source,
		Workflow:             fixture.workflow,
		State:                fixture.state,
		StartedAt:            fixture.startedAt,
		EndedAt:              fixture.endedAt,
		DurationMS:           fixture.endedAt.Sub(fixture.startedAt).Milliseconds(),
		Phases:               fixture.phases,
		TotalInputTokensEst:  totalInput,
		TotalOutputTokensEst: totalOutput,
		TotalTokensEst:       totalInput + totalOutput,
		TotalCostUSDEst:      fixture.totalCost,
		Note:                 "fixture",
	}
	artifacts := &runner.ReviewArtifacts{}
	if fixture.manifest != nil {
		if err := evidence.SaveManifest(stateDir, fixture.vesselID, fixture.manifest); err != nil {
			t.Fatalf("SaveManifest() error = %v", err)
		}
		summary.EvidenceManifestPath = filepath.ToSlash(filepath.Join("phases", fixture.vesselID, "evidence-manifest.json"))
		artifacts.EvidenceManifest = summary.EvidenceManifestPath
	}
	if fixture.costReport != nil {
		reportPath := filepath.Join(stateDir, "phases", fixture.vesselID, "cost-report.json")
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
			t.Fatalf("MkdirAll(reportPath) error = %v", err)
		}
		if err := cost.SaveReport(reportPath, fixture.costReport); err != nil {
			t.Fatalf("SaveReport() error = %v", err)
		}
		summary.CostReportPath = filepath.ToSlash(filepath.Join("phases", fixture.vesselID, "cost-report.json"))
		artifacts.CostReport = summary.CostReportPath
	}
	if fixture.evalReport != nil {
		evalDir := filepath.Join(stateDir, "phases", fixture.vesselID)
		if err := os.MkdirAll(evalDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(evalDir) error = %v", err)
		}
		if err := evaluator.SaveReport(evalDir, fixture.evalReport); err != nil {
			t.Fatalf("SaveReport(eval) error = %v", err)
		}
		summary.EvalReportPath = filepath.ToSlash(filepath.Join("phases", fixture.vesselID, "quality-report.json"))
		artifacts.EvalReport = summary.EvalReportPath
	}
	if len(fixture.budgetAlerts) > 0 {
		alertPath := filepath.Join(stateDir, "phases", fixture.vesselID, "budget-alerts.json")
		if err := os.MkdirAll(filepath.Dir(alertPath), 0o755); err != nil {
			t.Fatalf("MkdirAll(alertPath) error = %v", err)
		}
		data, err := json.MarshalIndent(fixture.budgetAlerts, "", "  ")
		if err != nil {
			t.Fatalf("MarshalIndent() error = %v", err)
		}
		if err := os.WriteFile(alertPath, data, 0o644); err != nil {
			t.Fatalf("WriteFile(alertPath) error = %v", err)
		}
		summary.BudgetAlertsPath = filepath.ToSlash(filepath.Join("phases", fixture.vesselID, "budget-alerts.json"))
		artifacts.BudgetAlerts = summary.BudgetAlertsPath
	}
	if fixture.recoveryArtifact != nil {
		if err := recovery.Save(stateDir, fixture.recoveryArtifact); err != nil {
			t.Fatalf("recovery.Save() error = %v", err)
		}
		summary.FailureReviewPath = filepath.ToSlash(filepath.Join("phases", fixture.vesselID, "failure-review.json"))
		summary.Recovery = &runner.RecoverySummary{
			Class:           string(fixture.recoveryArtifact.RecoveryClass),
			Action:          string(fixture.recoveryArtifact.RecoveryAction),
			FollowUpRoute:   fixture.recoveryArtifact.FollowUpRoute,
			RetrySuppressed: fixture.recoveryArtifact.RetrySuppressed,
			RetryOutcome:    fixture.recoveryArtifact.RetryOutcome,
			UnlockDimension: fixture.recoveryArtifact.UnlockDimension,
		}
		artifacts.FailureReview = summary.FailureReviewPath
	}
	if artifacts.EvidenceManifest != "" || artifacts.CostReport != "" || artifacts.BudgetAlerts != "" || artifacts.EvalReport != "" || artifacts.FailureReview != "" {
		summary.ReviewArtifacts = artifacts
	}
	requireSummaryOnly(t, stateDir, summary)
}

func requireSummaryOnly(t *testing.T, stateDir string, summary runner.VesselSummary) {
	t.Helper()
	if err := runner.SaveVesselSummary(stateDir, &summary); err != nil {
		t.Fatalf("SaveVesselSummary() error = %v", err)
	}
}

func findGroup(t *testing.T, groups []GroupReview, source, workflow, phase string) GroupReview {
	t.Helper()
	for _, group := range groups {
		if group.Source == source && group.Workflow == workflow && group.Phase == phase {
			return group
		}
	}
	t.Fatalf("group %s/%s/%s not found", source, workflow, phase)
	return GroupReview{}
}
