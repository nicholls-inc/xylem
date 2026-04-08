package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	reviewpkg "github.com/nicholls-inc/xylem/cli/internal/review"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

func TestCmdReviewPrintsGeneratedReport(t *testing.T) {
	cfg := &config.Config{StateDir: t.TempDir()}
	original := generateHarnessReview
	t.Cleanup(func() { generateHarnessReview = original })

	generateHarnessReview = func(cfg *config.Config) (*reviewpkg.Result, error) {
		return &reviewpkg.Result{
			JSONPath:     cfg.StateDir + "/reviews/harness-review.json",
			MarkdownPath: cfg.StateDir + "/reviews/harness-review.md",
			Markdown:     "# Harness review\n",
		}, nil
	}

	out := captureStdout(func() {
		if err := cmdReview(cfg); err != nil {
			t.Fatalf("cmdReview() error = %v", err)
		}
	})

	if !strings.Contains(out, "harness-review.json") {
		t.Fatalf("output = %q, want json path", out)
	}
	if !strings.Contains(out, "# Harness review") {
		t.Fatalf("output = %q, want markdown body", out)
	}
}

func TestReviewCommandSkipsToolingChecks(t *testing.T) {
	t.Setenv("PATH", "")
	stateDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := fmt.Sprintf(`state_dir: %q
repo: owner/repo
tasks:
  review:
    labels: [bug]
    workflow: fix-bug
claude:
  default_model: "claude-sonnet-4-6"
`, stateDir)
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--config", configPath, "review"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestMaybeAutoGenerateHarnessReviewRespectsCadence(t *testing.T) {
	cfg := &config.Config{
		StateDir: t.TempDir(),
		Harness: config.HarnessConfig{
			Review: config.HarnessReviewConfig{
				Enabled:    true,
				Cadence:    "every_n_runs",
				EveryNRuns: 2,
			},
		},
	}

	result1, err := reviewpkg.Generate(cfg.StateDir, reviewpkg.Options{
		OutputDir: cfg.HarnessReviewOutputDir(),
		Now:       reviewTestTime(0),
	})
	if err != nil {
		t.Fatalf("Generate() baseline error = %v", err)
	}
	if result1.Report.TotalRunsObserved != 0 {
		t.Fatalf("initial total runs = %d, want 0", result1.Report.TotalRunsObserved)
	}

	writeReviewSummaryFixture(t, cfg.StateDir, "run-1", reviewTestTime(1))

	calls := 0
	original := generateHarnessReview
	t.Cleanup(func() { generateHarnessReview = original })
	generateHarnessReview = func(cfg *config.Config) (*reviewpkg.Result, error) {
		calls++
		return &reviewpkg.Result{}, nil
	}

	maybeAutoGenerateHarnessReview(cfg, runner.DrainResult{Completed: 1})
	if calls != 0 {
		t.Fatalf("generateHarnessReview calls = %d, want 0 before threshold", calls)
	}

	writeReviewSummaryFixture(t, cfg.StateDir, "run-2", reviewTestTime(2))
	maybeAutoGenerateHarnessReview(cfg, runner.DrainResult{Completed: 1})
	if calls != 1 {
		t.Fatalf("generateHarnessReview calls = %d, want 1 at threshold", calls)
	}
}

func writeReviewSummaryFixture(t *testing.T, stateDir, vesselID string, endedAt time.Time) {
	t.Helper()
	err := runner.SaveVesselSummary(stateDir, &runner.VesselSummary{
		VesselID:   vesselID,
		Source:     "manual",
		Workflow:   "ad-hoc",
		State:      "completed",
		StartedAt:  endedAt.Add(-time.Minute),
		EndedAt:    endedAt,
		DurationMS: time.Minute.Milliseconds(),
		Phases:     []runner.PhaseSummary{},
		Note:       fmt.Sprintf("fixture %s", vesselID),
	})
	if err != nil {
		t.Fatalf("SaveVesselSummary() error = %v", err)
	}
}

func reviewTestTime(offsetMinutes int) time.Time {
	return time.Date(2026, time.April, 8, 15, offsetMinutes, 0, 0, time.UTC)
}
