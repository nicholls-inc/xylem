package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
)

const calibrationFileName = "calibration.json"

type CalibrationSet struct {
	Rubric        string                `json:"rubric"`
	PassThreshold float64               `json:"pass_threshold"`
	Criteria      []evaluator.Criterion `json:"criteria"`
	Examples      []CalibrationExample  `json:"examples"`
}

type CalibrationExample struct {
	ID         string             `json:"id"`
	Judgment   string             `json:"judgment"`
	OutputFile string             `json:"output_file"`
	Criteria   map[string]float64 `json:"criteria"`
	Notes      string             `json:"notes,omitempty"`
	Output     string             `json:"-"`
}

type CalibrationSummary struct {
	PassExamples int `json:"pass_examples"`
	FailExamples int `json:"fail_examples"`
}

func LoadCalibrationSet(dir string) (*CalibrationSet, error) {
	path := filepath.Join(dir, calibrationFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load calibration set %q: %w", path, err)
	}

	var set CalibrationSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("parse calibration set %q: %w", path, err)
	}

	for i := range set.Examples {
		outputPath := filepath.Join(dir, set.Examples[i].OutputFile)
		output, err := os.ReadFile(outputPath)
		if err != nil {
			return nil, fmt.Errorf("load calibration example %q: %w", outputPath, err)
		}
		set.Examples[i].Output = string(output)
	}

	if err := set.Validate(); err != nil {
		return nil, err
	}
	return &set, nil
}

func (s *CalibrationSet) Validate() error {
	if s == nil {
		return fmt.Errorf("validate calibration set: nil set")
	}
	if strings.TrimSpace(s.Rubric) == "" {
		return fmt.Errorf("validate calibration set: rubric is required")
	}
	if len(s.Examples) == 0 {
		return fmt.Errorf("validate calibration set %q: at least one example is required", s.Rubric)
	}

	cfg := evaluator.EvalConfig{
		Criteria:      s.Criteria,
		PassThreshold: s.passThreshold(),
		MaxIterations: 1,
	}
	if err := evaluator.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("validate calibration set %q: %w", s.Rubric, err)
	}

	criteriaByName := make(map[string]evaluator.Criterion, len(s.Criteria))
	for _, criterion := range s.Criteria {
		criteriaByName[criterion.Name] = criterion
	}

	for _, example := range s.Examples {
		if strings.TrimSpace(example.ID) == "" {
			return fmt.Errorf("validate calibration set %q: example id is required", s.Rubric)
		}
		switch example.Judgment {
		case "pass", "fail":
		default:
			return fmt.Errorf("validate calibration set %q: example %q judgment must be \"pass\" or \"fail\"", s.Rubric, example.ID)
		}
		if strings.TrimSpace(example.OutputFile) == "" {
			return fmt.Errorf("validate calibration set %q: example %q output_file is required", s.Rubric, example.ID)
		}
		if strings.TrimSpace(example.Output) == "" {
			return fmt.Errorf("validate calibration set %q: example %q output is empty", s.Rubric, example.ID)
		}
		for _, criterion := range s.Criteria {
			score, ok := example.Criteria[criterion.Name]
			if !ok {
				return fmt.Errorf("validate calibration set %q: example %q missing score for %q", s.Rubric, example.ID, criterion.Name)
			}
			if score < 0 || score > 1 {
				return fmt.Errorf("validate calibration set %q: example %q score for %q must be in [0,1], got %f", s.Rubric, example.ID, criterion.Name, score)
			}
		}
		for name := range example.Criteria {
			if _, ok := criteriaByName[name]; !ok {
				return fmt.Errorf("validate calibration set %q: example %q references unknown criterion %q", s.Rubric, example.ID, name)
			}
		}

		overall := evaluator.QualityScore{Criteria: example.Criteria}.WeightedScore(s.Criteria)
		passed := overall >= s.passThreshold()
		if example.Judgment == "pass" && !passed {
			return fmt.Errorf("validate calibration set %q: example %q scored %.3f below threshold %.3f", s.Rubric, example.ID, overall, s.passThreshold())
		}
		if example.Judgment == "fail" && passed {
			return fmt.Errorf("validate calibration set %q: example %q scored %.3f above threshold %.3f", s.Rubric, example.ID, overall, s.passThreshold())
		}
	}

	return nil
}

func (s *CalibrationSet) Summary() CalibrationSummary {
	var summary CalibrationSummary
	if s == nil {
		return summary
	}
	for _, example := range s.Examples {
		switch example.Judgment {
		case "pass":
			summary.PassExamples++
		case "fail":
			summary.FailExamples++
		}
	}
	return summary
}

func (s *CalibrationSet) passThreshold() float64 {
	if s == nil || s.PassThreshold == 0 {
		return evaluator.DefaultPassThreshold
	}
	return s.PassThreshold
}
