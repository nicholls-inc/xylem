package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	xeval "github.com/nicholls-inc/xylem/cli/internal/eval"
)

func TestCmdEvalCompareWritesReport(t *testing.T) {
	baselineDir := t.TempDir()
	candidateDir := t.TempDir()
	writeRunReportFixture(t, baselineDir, &xeval.RunReport{
		SchemaVersion: "1",
		JobDir:        baselineDir,
		Aggregate: xeval.AggregateSummary{
			TrialCount:            1,
			SuccessCount:          1,
			SuccessRate:           1,
			AverageReward:         0.8,
			AverageLatencySeconds: 10,
			AverageCostUSDEst:     0.4,
			AverageEvidenceScore:  0.5,
		},
		Trials: []xeval.TrialReport{{
			TaskID:         "fix-simple-null-pointer",
			TrialName:      "trial-fix",
			Reward:         0.8,
			Success:        true,
			LatencySeconds: 10,
			CostUSDEst:     0.4,
			EvidenceScore:  0.5,
		}},
	})
	writeRunReportFixture(t, candidateDir, &xeval.RunReport{
		SchemaVersion: "1",
		JobDir:        candidateDir,
		Aggregate: xeval.AggregateSummary{
			TrialCount:            1,
			SuccessCount:          1,
			SuccessRate:           1,
			AverageReward:         1.0,
			AverageLatencySeconds: 8,
			AverageCostUSDEst:     0.3,
			AverageEvidenceScore:  0.75,
		},
		Trials: []xeval.TrialReport{{
			TaskID:         "fix-simple-null-pointer",
			TrialName:      "trial-fix",
			Reward:         1.0,
			Success:        true,
			LatencySeconds: 8,
			CostUSDEst:     0.3,
			EvidenceScore:  0.75,
		}},
	})

	outputPath := filepath.Join(t.TempDir(), "comparison.json")
	var out bytes.Buffer
	err := cmdEvalCompare(&out, &evalCompareOptions{
		BaselineDir:  baselineDir,
		CandidateDir: candidateDir,
		OutputPath:   outputPath,
	})
	if err != nil {
		t.Fatalf("cmdEvalCompare() error = %v", err)
	}
	if !strings.Contains(out.String(), "candidate_improved") {
		t.Fatalf("stdout = %q, want verdict", out.String())
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("comparison report not written: %v", err)
	}
}

func TestCmdEvalRunInvokesHarborAndWritesReport(t *testing.T) {
	origLookPath := evalLookPath
	origRunProcess := evalRunProcess
	origBuildRunReport := evalBuildRunReport
	origWriteRunReport := evalWriteRunReport
	t.Cleanup(func() {
		evalLookPath = origLookPath
		evalRunProcess = origRunProcess
		evalBuildRunReport = origBuildRunReport
		evalWriteRunReport = origWriteRunReport
	})

	evalLookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	var calls [][]string
	evalRunProcess = func(_ context.Context, _ string, name string, args ...string) error {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return nil
	}
	evalBuildRunReport = func(jobDir string) (*xeval.RunReport, error) {
		return &xeval.RunReport{
			SchemaVersion: "1",
			JobDir:        jobDir,
			Aggregate: xeval.AggregateSummary{
				TrialCount:    2,
				SuccessRate:   1,
				AverageReward: 0.95,
			},
		}, nil
	}
	var writtenReportPath string
	evalWriteRunReport = func(path string, report *xeval.RunReport) error {
		writtenReportPath = path
		return nil
	}

	rubricsDir := filepath.Join(t.TempDir(), "rubrics")
	if err := os.MkdirAll(rubricsDir, 0o755); err != nil {
		t.Fatalf("mkdir rubrics dir: %v", err)
	}
	for _, name := range []string{"plan_quality.toml", "evidence_quality.toml"} {
		if err := os.WriteFile(filepath.Join(rubricsDir, name), []byte("[rubric]\n"), 0o644); err != nil {
			t.Fatalf("write rubric: %v", err)
		}
	}

	outputDir := filepath.Join(t.TempDir(), "jobs", "candidate")
	var out bytes.Buffer
	err := cmdEvalRun(context.Background(), &out, &evalRunOptions{
		HarborConfig: ".xylem/eval/harbor.yaml",
		OutputDir:    outputDir,
		RubricsDir:   rubricsDir,
	})
	if err != nil {
		t.Fatalf("cmdEvalRun() error = %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("harbor calls = %d, want 3", len(calls))
	}
	if calls[0][0] != "harbor" || calls[0][1] != "run" {
		t.Fatalf("first call = %v, want harbor run", calls[0])
	}
	if !strings.Contains(out.String(), "average reward: 0.9500") {
		t.Fatalf("stdout = %q, want report summary", out.String())
	}
	if writtenReportPath != xeval.ReportPath(outputDir) {
		t.Fatalf("written report path = %q, want %q", writtenReportPath, xeval.ReportPath(outputDir))
	}
}

func TestCmdEvalCompareFailsOnRegression(t *testing.T) {
	baselineDir := t.TempDir()
	candidateDir := t.TempDir()
	writeRunReportFixture(t, baselineDir, &xeval.RunReport{
		SchemaVersion: "1",
		JobDir:        baselineDir,
		Aggregate: xeval.AggregateSummary{
			TrialCount:            1,
			SuccessCount:          1,
			SuccessRate:           1,
			AverageReward:         1.0,
			AverageLatencySeconds: 8,
			AverageCostUSDEst:     0.3,
			AverageEvidenceScore:  0.9,
		},
		Trials: []xeval.TrialReport{{
			TaskID:         "fix-simple-null-pointer",
			TrialName:      "trial-fix",
			Reward:         1.0,
			Success:        true,
			LatencySeconds: 8,
			CostUSDEst:     0.3,
			EvidenceScore:  0.9,
		}},
	})
	writeRunReportFixture(t, candidateDir, &xeval.RunReport{
		SchemaVersion: "1",
		JobDir:        candidateDir,
		Aggregate: xeval.AggregateSummary{
			TrialCount:            1,
			SuccessCount:          0,
			SuccessRate:           0,
			AverageReward:         0.2,
			AverageLatencySeconds: 12,
			AverageCostUSDEst:     0.5,
			AverageEvidenceScore:  0.2,
		},
		Trials: []xeval.TrialReport{{
			TaskID:         "fix-simple-null-pointer",
			TrialName:      "trial-fix",
			Reward:         0.2,
			Success:        false,
			LatencySeconds: 12,
			CostUSDEst:     0.5,
			EvidenceScore:  0.2,
		}},
	})

	var out bytes.Buffer
	err := cmdEvalCompare(&out, &evalCompareOptions{
		BaselineDir:      baselineDir,
		CandidateDir:     candidateDir,
		FailOnRegression: true,
	})
	if err == nil {
		t.Fatal("cmdEvalCompare() error = nil, want regression exit error")
	}

	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("cmdEvalCompare() error = %T, want *exitError", err)
	}
	if ee.code != 2 {
		t.Fatalf("exit code = %d, want 2", ee.code)
	}
	if !strings.Contains(out.String(), "candidate_regressed") {
		t.Fatalf("stdout = %q, want regression verdict", out.String())
	}
}

func writeRunReportFixture(t *testing.T, dir string, report *xeval.RunReport) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(xeval.ReportPath(dir), data, 0o644); err != nil {
		t.Fatalf("write report fixture: %v", err)
	}
}
