package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
)

func TestLoadCalibrationSet(t *testing.T) {
	dir := t.TempDir()
	writeCalibrationFixture(t, dir, &CalibrationSet{
		Rubric:        "plan_quality",
		PassThreshold: 0.7,
		Criteria: []evaluator.Criterion{
			{Name: "root_cause_identification", Description: "Find the bug", Weight: 0.4, Threshold: 0.7},
			{Name: "reasoning_chain", Description: "Explain the logic", Weight: 0.3, Threshold: 0.7},
			{Name: "scope_accuracy", Description: "Stay scoped", Weight: 0.3, Threshold: 0.7},
		},
		Examples: []CalibrationExample{
			{
				ID:         "strong-plan",
				Judgment:   "pass",
				OutputFile: "strong-plan.md",
				Criteria: map[string]float64{
					"root_cause_identification": 1.0,
					"reasoning_chain":           0.9,
					"scope_accuracy":            0.8,
				},
			},
			{
				ID:         "scope-drift",
				Judgment:   "fail",
				OutputFile: "scope-drift.md",
				Criteria: map[string]float64{
					"root_cause_identification": 0.4,
					"reasoning_chain":           0.5,
					"scope_accuracy":            0.1,
				},
			},
		},
	}, map[string]string{
		"strong-plan.md": "Root cause is correct and the plan stays narrowly scoped.",
		"scope-drift.md": "Suggests a rewrite unrelated to the reported bug.",
	})

	set, err := LoadCalibrationSet(dir)
	if err != nil {
		t.Fatalf("LoadCalibrationSet() error = %v", err)
	}
	if got, want := set.Summary(), (CalibrationSummary{PassExamples: 1, FailExamples: 1}); got != want {
		t.Fatalf("Summary() = %#v, want %#v", got, want)
	}
	if set.Examples[0].Output == "" {
		t.Fatal("expected example output to be loaded from disk")
	}
}

func TestLoadCalibrationSetFromSeededPlanQualityFixtures(t *testing.T) {
	set, err := LoadCalibrationSet(repoCalibrationPath(t, "plan_quality"))
	if err != nil {
		t.Fatalf("LoadCalibrationSet(seed) error = %v", err)
	}
	if set.Rubric != "plan_quality" {
		t.Fatalf("rubric = %q, want plan_quality", set.Rubric)
	}
	if summary := set.Summary(); summary.PassExamples < 1 || summary.FailExamples < 1 {
		t.Fatalf("Summary() = %#v, want both pass and fail examples", summary)
	}
}

func TestCalibrationSetValidateRejectsJudgmentDrift(t *testing.T) {
	set := &CalibrationSet{
		Rubric:        "plan_quality",
		PassThreshold: 0.7,
		Criteria: []evaluator.Criterion{
			{Name: "root_cause_identification", Description: "Find the bug", Weight: 0.4, Threshold: 0.7},
			{Name: "reasoning_chain", Description: "Explain the logic", Weight: 0.3, Threshold: 0.7},
			{Name: "scope_accuracy", Description: "Stay scoped", Weight: 0.3, Threshold: 0.7},
		},
		Examples: []CalibrationExample{{
			ID:         "mislabeled-pass",
			Judgment:   "pass",
			OutputFile: "mislabeled-pass.md",
			Output:     "This example is intentionally weak.",
			Criteria: map[string]float64{
				"root_cause_identification": 0.2,
				"reasoning_chain":           0.1,
				"scope_accuracy":            0.2,
			},
		}},
	}

	if err := set.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want judgment drift error")
	}
}

func repoCalibrationPath(t *testing.T, rubric string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return filepath.Join(root, ".xylem", "eval", "calibration", rubric)
}

func writeCalibrationFixture(t *testing.T, dir string, set *CalibrationSet, outputs map[string]string) {
	t.Helper()

	for name, content := range outputs {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write output %s: %v", name, err)
		}
	}

	data, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal calibration set: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, calibrationFileName), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write calibration set: %v", err)
	}
}
