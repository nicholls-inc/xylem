package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildRunReport(t *testing.T) {
	jobDir := t.TempDir()
	writeTrial(t, jobDir, "trial-fix", trialFixture{
		TaskName:   "fix-simple-null-pointer",
		TrialName:  "trial-fix",
		Reward:     1.0,
		Success:    true,
		Latency:    12.5,
		Cost:       0.42,
		RetryCount: 1,
		Evidence:   0.75,
	})
	writeTrial(t, jobDir, "trial-harness", trialFixture{
		TaskName:         "modify-harness-md",
		TrialName:        "trial-harness",
		Reward:           0.5,
		Success:          false,
		Latency:          7.5,
		Cost:             0.11,
		ToolFailures:     1,
		PolicyViolations: 1,
		Evidence:         0.0,
		Error:            "policy denied",
	})
	writeAnalysis(t, jobDir, "plan_quality", harborAnalysis{
		JobSummary: "baseline summary",
		Trials: []struct {
			TrialName string `json:"trial_name"`
			Summary   string `json:"summary"`
			Checks    map[string]struct {
				Outcome string `json:"outcome"`
			} `json:"checks"`
		}{
			{
				TrialName: "trial-fix",
				Summary:   "good plan",
				Checks: map[string]struct {
					Outcome string `json:"outcome"`
				}{
					"root_cause_identification": {Outcome: "pass"},
				},
			},
		},
	})

	report, err := BuildRunReport(jobDir)
	if err != nil {
		t.Fatalf("BuildRunReport() error = %v", err)
	}

	if report.Aggregate.TrialCount != 2 {
		t.Fatalf("trial_count = %d, want 2", report.Aggregate.TrialCount)
	}
	if report.Aggregate.SuccessCount != 1 {
		t.Fatalf("success_count = %d, want 1", report.Aggregate.SuccessCount)
	}
	if report.Aggregate.SuccessRate != 0.5 {
		t.Fatalf("success_rate = %v, want 0.5", report.Aggregate.SuccessRate)
	}
	if len(report.Rubrics) != 1 {
		t.Fatalf("rubrics len = %d, want 1", len(report.Rubrics))
	}
	if report.Rubrics[0].Criteria[0].PassRate != 1 {
		t.Fatalf("criterion pass rate = %v, want 1", report.Rubrics[0].Criteria[0].PassRate)
	}
}

func TestCompareReports(t *testing.T) {
	baseline := &RunReport{
		JobDir: "/baseline",
		Aggregate: AggregateSummary{
			TrialCount:              1,
			SuccessCount:            1,
			SuccessRate:             1,
			AverageReward:           0.8,
			AverageLatencySeconds:   10,
			AverageCostUSDEst:       0.5,
			AverageRetryCount:       1,
			AverageToolFailureCount: 1,
			AveragePolicyViolations: 0,
			AverageEvidenceScore:    0.5,
		},
		Trials: []TrialReport{{
			TaskID:           "fix-simple-null-pointer",
			TrialName:        "trial-fix",
			Reward:           0.8,
			Success:          true,
			LatencySeconds:   10,
			CostUSDEst:       0.5,
			RetryCount:       1,
			ToolFailureCount: 1,
			PolicyViolations: 0,
			EvidenceScore:    0.5,
		}},
		Rubrics: []RubricReport{{
			Name: "plan_quality",
			Criteria: []RubricCriterionSummary{{
				Name:     "root_cause_identification",
				PassRate: 0.5,
			}},
		}},
	}
	candidate := &RunReport{
		JobDir: "/candidate",
		Aggregate: AggregateSummary{
			TrialCount:              1,
			SuccessCount:            1,
			SuccessRate:             1,
			AverageReward:           1,
			AverageLatencySeconds:   8,
			AverageCostUSDEst:       0.4,
			AverageRetryCount:       0,
			AverageToolFailureCount: 0,
			AveragePolicyViolations: 0,
			AverageEvidenceScore:    0.75,
		},
		Trials: []TrialReport{{
			TaskID:           "fix-simple-null-pointer",
			TrialName:        "trial-fix",
			Reward:           1,
			Success:          true,
			LatencySeconds:   8,
			CostUSDEst:       0.4,
			RetryCount:       0,
			ToolFailureCount: 0,
			PolicyViolations: 0,
			EvidenceScore:    0.75,
		}},
		Rubrics: []RubricReport{{
			Name: "plan_quality",
			Criteria: []RubricCriterionSummary{{
				Name:     "root_cause_identification",
				PassRate: 1,
			}},
		}},
	}

	comparison := CompareReports(baseline, candidate)
	if comparison.Verdict != "candidate_improved" {
		t.Fatalf("verdict = %q, want candidate_improved", comparison.Verdict)
	}
	if len(comparison.Regressions) != 0 {
		t.Fatalf("regressions = %v, want none", comparison.Regressions)
	}
	if len(comparison.Improvements) == 0 {
		t.Fatal("expected at least one improvement")
	}
	if comparison.Trials[0].Delta.Reward <= 0 {
		t.Fatalf("reward delta = %v, want positive", comparison.Trials[0].Delta.Reward)
	}
	if comparison.Rubrics[0].CriterionDeltas[0].Delta <= 0 {
		t.Fatalf("criterion delta = %v, want positive", comparison.Rubrics[0].CriterionDeltas[0].Delta)
	}
}

func TestCompareReportsAggregatesTrialsPerTask(t *testing.T) {
	baseline := &RunReport{
		JobDir: "/baseline",
		Aggregate: AggregateSummary{
			TrialCount:    2,
			SuccessCount:  1,
			SuccessRate:   0.5,
			AverageReward: 0.5,
		},
		Trials: []TrialReport{
			{
				TaskID:    "fix-simple-null-pointer",
				TrialName: "trial-a",
				Reward:    1,
				Success:   true,
			},
			{
				TaskID:    "fix-simple-null-pointer",
				TrialName: "trial-b",
				Reward:    0,
				Success:   false,
			},
		},
	}
	candidate := &RunReport{
		JobDir: "/candidate",
		Aggregate: AggregateSummary{
			TrialCount:    2,
			SuccessCount:  1,
			SuccessRate:   0.5,
			AverageReward: 0.5,
		},
		Trials: []TrialReport{
			{
				TaskID:    "fix-simple-null-pointer",
				TrialName: "trial-a",
				Reward:    0,
				Success:   false,
			},
			{
				TaskID:    "fix-simple-null-pointer",
				TrialName: "trial-b",
				Reward:    1,
				Success:   true,
			},
		},
	}

	comparison := CompareReports(baseline, candidate)
	if len(comparison.Trials) != 1 {
		t.Fatalf("trial comparisons = %d, want 1", len(comparison.Trials))
	}
	if comparison.Trials[0].Regression {
		t.Fatalf("regression = true, want false")
	}
	if comparison.Trials[0].Improved {
		t.Fatalf("improved = true, want false")
	}
	if comparison.Trials[0].Delta.Reward != 0 {
		t.Fatalf("reward delta = %v, want 0", comparison.Trials[0].Delta.Reward)
	}
	if comparison.Trials[0].Delta.SuccessRate != 0 {
		t.Fatalf("success rate delta = %v, want 0", comparison.Trials[0].Delta.SuccessRate)
	}
	if comparison.Trials[0].Baseline == nil || comparison.Trials[0].Baseline.TrialCount != 2 {
		t.Fatalf("baseline trial count = %#v, want 2", comparison.Trials[0].Baseline)
	}
	if comparison.Trials[0].Candidate == nil || comparison.Trials[0].Candidate.TrialCount != 2 {
		t.Fatalf("candidate trial count = %#v, want 2", comparison.Trials[0].Candidate)
	}
}

type trialFixture struct {
	TaskName         string
	TrialName        string
	Reward           float64
	Success          bool
	Latency          float64
	Cost             float64
	RetryCount       int
	ToolFailures     int
	PolicyViolations int
	Evidence         float64
	Error            string
}

func writeTrial(t *testing.T, jobDir, trialName string, fixture trialFixture) {
	t.Helper()

	startedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	finishedAt := startedAt.Add(time.Duration(fixture.Latency * float64(time.Second)))

	trialDir := filepath.Join(jobDir, trialName)
	if err := os.MkdirAll(filepath.Join(trialDir, "verifier"), 0o755); err != nil {
		t.Fatalf("mkdir trial dir: %v", err)
	}

	result := harborTrialResult{
		TaskName:   fixture.TaskName,
		TrialName:  fixture.TrialName,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		AgentResult: &struct {
			CostUSD *float64 `json:"cost_usd"`
		}{CostUSD: &fixture.Cost},
		VerifierResult: &struct {
			Rewards map[string]float64 `json:"rewards"`
		}{Rewards: map[string]float64{"reward": fixture.Reward}},
	}
	if fixture.Error != "" {
		result.ExceptionInfo = &struct {
			ExceptionType    string `json:"exception_type"`
			ExceptionMessage string `json:"exception_message"`
		}{
			ExceptionType:    "RuntimeError",
			ExceptionMessage: fixture.Error,
		}
	}
	writeJSON(t, filepath.Join(trialDir, "result.json"), result)
	writeJSON(t, filepath.Join(trialDir, "verifier", "reward.json"), TrialMetrics{
		SchemaVersion:        metricsSchemaVersion,
		TaskID:               fixture.TaskName,
		Reward:               fixture.Reward,
		Success:              fixture.Success,
		LatencySeconds:       fixture.Latency,
		CostUSDEst:           fixture.Cost,
		RetryCount:           fixture.RetryCount,
		ToolFailureCount:     fixture.ToolFailures,
		PolicyViolationCount: fixture.PolicyViolations,
		EvidenceScore:        fixture.Evidence,
	})
}

func writeAnalysis(t *testing.T, jobDir, name string, analysis harborAnalysis) {
	t.Helper()
	dir := filepath.Join(jobDir, "analysis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir analysis dir: %v", err)
	}
	writeJSON(t, filepath.Join(dir, name+".json"), analysis)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
