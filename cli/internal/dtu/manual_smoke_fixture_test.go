package dtu_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

func TestCheckedInManualSmokeFixturesAreSeeded(t *testing.T) {
	tests := []struct {
		name          string
		fixtureName   string
		workflowName  string
		requiredFiles []string
	}{
		{
			name:         "ws3 observability cost",
			fixtureName:  filepath.Join("manual-smoke", "ws3-observability-cost"),
			workflowName: "observability-cost",
			requiredFiles: []string{
				".xylem/HARNESS.md",
				".xylem/prompts/observability-cost/plan.md",
				".xylem/prompts/observability-cost/implement.md",
			},
		},
		{
			name:         "ws3 summary artifacts",
			fixtureName:  filepath.Join("manual-smoke", "ws3-summary-artifacts"),
			workflowName: "summary-artifacts",
			requiredFiles: []string{
				".xylem/HARNESS.md",
				".xylem/prompts/summary-artifacts/analyze.md",
				".xylem/prompts/summary-artifacts/implement.md",
			},
		},
		{
			name:         "ws4 evidence model",
			fixtureName:  filepath.Join("manual-smoke", "ws4-evidence-model"),
			workflowName: "evidence-model",
			requiredFiles: []string{
				".xylem/HARNESS.md",
				".xylem/prompts/evidence-model/implement.md",
			},
		},
		{
			name:         "ws5 eval suite",
			fixtureName:  filepath.Join("manual-smoke", "ws5-eval-suite"),
			workflowName: "eval-suite",
			requiredFiles: []string{
				".xylem/HARNESS.md",
				".xylem/prompts/eval-suite/fix.md",
				".xylem/eval/harbor.yaml",
				".xylem/eval/helpers/xylem_verify.py",
				".xylem/eval/helpers/conftest.py",
				".xylem/eval/rubrics/plan_quality.toml",
				".xylem/eval/rubrics/evidence_quality.toml",
				".xylem/eval/scenarios/widget-bug/instruction.md",
				".xylem/eval/scenarios/widget-bug/task.toml",
				".xylem/eval/scenarios/widget-bug/tests/test.sh",
				".xylem/eval/scenarios/widget-bug/tests/test_verification.py",
			},
		},
		{
			name:         "ws6 cross cutting",
			fixtureName:  filepath.Join("manual-smoke", "ws6-cross-cutting"),
			workflowName: "cross-cutting",
			requiredFiles: []string{
				".xylem/HARNESS.md",
				".xylem/prompts/cross-cutting/analyze.md",
				".xylem/prompts/cross-cutting/implement_a.md",
				".xylem/prompts/cross-cutting/implement_b.md",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			fixtureDir := scenarioFixturePath(t, tt.fixtureName)
			defer withWorkingDir(t, fixtureDir)()

			cfg, err := config.Load(".xylem.yml")
			if err != nil {
				t.Fatalf("Load(.xylem.yml): %v", err)
			}
			if cfg.StateDir != ".xylem-state" {
				t.Fatalf("cfg.StateDir = %q, want %q", cfg.StateDir, ".xylem-state")
			}

			for _, required := range tt.requiredFiles {
				if _, err := os.Stat(required); err != nil {
					t.Fatalf("Stat(%q): %v", required, err)
				}
			}

			if _, err := workflow.Load(filepath.Join(".xylem", "workflows", tt.workflowName+".yaml")); err != nil {
				t.Fatalf("Load workflow %q: %v", tt.workflowName, err)
			}
		})
	}
}
