package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run the eval corpus against the harness",
		Long: `Execute scenario tests from .xylem/eval/scenarios/ and compare results
against stored baselines.

Subcommands:
  run      Run one or all scenario test suites
  baseline Capture a new baseline for one or all scenarios
  compare  Run scenarios and diff results against stored baselines`,
	}
	cmd.AddCommand(
		newEvalRunCmd(),
		newEvalBaselineCmd(),
		newEvalCompareCmd(),
	)
	return cmd
}

func newEvalRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run scenario test suites",
		Long: `Run the pytest-based test suite for one or all scenarios under
.xylem/eval/scenarios/. Requires pytest on PATH.

Set WORK_DIR to the repository root that was exercised by the agent.
Set TASK_DIR to the scenario directory (default: resolved automatically).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			scenario, _ := cmd.Flags().GetString("scenario")
			all, _ := cmd.Flags().GetBool("all")
			evalDir, _ := cmd.Flags().GetString("eval-dir")
			return cmdEvalRun(scenario, all, evalDir, false)
		},
	}
	cmd.Flags().StringP("scenario", "s", "", "Scenario ID to run (e.g. fix-simple-null-pointer)")
	cmd.Flags().Bool("all", false, "Run all scenarios")
	cmd.Flags().String("eval-dir", filepath.Join(".xylem", "eval"), "Directory containing the eval corpus")
	return cmd
}

func newEvalBaselineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Capture a new baseline for one or all scenarios",
		Long: `Run the scenario test suite with XYLEM_EVAL_CAPTURE_BASELINE=1 so that
each test writes its results to .xylem/eval/baselines/<scenario-id>.json.

Only run this intentionally after verifying the agent's output is correct.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			scenario, _ := cmd.Flags().GetString("scenario")
			all, _ := cmd.Flags().GetBool("all")
			evalDir, _ := cmd.Flags().GetString("eval-dir")
			return cmdEvalRun(scenario, all, evalDir, true)
		},
	}
	cmd.Flags().StringP("scenario", "s", "", "Scenario ID to capture baseline for")
	cmd.Flags().Bool("all", false, "Capture baselines for all scenarios")
	cmd.Flags().String("eval-dir", filepath.Join(".xylem", "eval"), "Directory containing the eval corpus")
	return cmd
}

func newEvalCompareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Run scenarios and compare against stored baselines",
		Long: `Run the scenario test suite and compare the resulting reward scores against
stored baselines in .xylem/eval/baselines/.

Exits non-zero if any scenario has a regression (a check that passed in the
baseline now fails, or the reward score dropped by more than the threshold).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			scenario, _ := cmd.Flags().GetString("scenario")
			all, _ := cmd.Flags().GetBool("all")
			evalDir, _ := cmd.Flags().GetString("eval-dir")
			threshold, _ := cmd.Flags().GetFloat64("regression-threshold")
			return cmdEvalCompare(scenario, all, evalDir, threshold)
		},
	}
	cmd.Flags().StringP("scenario", "s", "", "Scenario ID to compare")
	cmd.Flags().Bool("all", false, "Compare all scenarios")
	cmd.Flags().String("eval-dir", filepath.Join(".xylem", "eval"), "Directory containing the eval corpus")
	cmd.Flags().Float64("regression-threshold", 0.05, "Reward drop (0.0-1.0) that counts as a regression")
	return cmd
}

// resolveEvalTargets returns the list of scenario directories to operate on.
// When scenario is non-empty, only that scenario directory is returned.
// When all is true, all subdirectories of scenariosDir are returned.
func resolveEvalTargets(evalDir, scenario string, all bool) ([]string, error) {
	scenariosDir := filepath.Join(evalDir, "scenarios")

	if scenario != "" {
		target := filepath.Join(scenariosDir, scenario)
		if _, err := os.Stat(target); os.IsNotExist(err) {
			return nil, fmt.Errorf("scenario %q not found under %s", scenario, scenariosDir)
		}
		return []string{target}, nil
	}

	if !all {
		return nil, fmt.Errorf("specify --scenario <id> or --all")
	}

	entries, err := os.ReadDir(scenariosDir)
	if err != nil {
		return nil, fmt.Errorf("read scenarios dir %s: %w", scenariosDir, err)
	}

	var targets []string
	for _, e := range entries {
		if e.IsDir() {
			targets = append(targets, filepath.Join(scenariosDir, e.Name()))
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no scenario directories found under %s", scenariosDir)
	}
	return targets, nil
}

// pytestPath returns the pytest executable path or an error if not found.
func pytestPath() (string, error) {
	p, err := exec.LookPath("pytest")
	if err != nil {
		return "", fmt.Errorf("pytest not found on PATH; install it with: pip install pytest")
	}
	return p, nil
}

// cmdEvalRun runs pytest for the selected scenario(s).
// When captureBaseline is true it sets XYLEM_EVAL_CAPTURE_BASELINE=1.
func cmdEvalRun(scenario string, all bool, evalDir string, captureBaseline bool) error {
	pytest, err := pytestPath()
	if err != nil {
		return err
	}

	targets, err := resolveEvalTargets(evalDir, scenario, all)
	if err != nil {
		return err
	}

	workDir, _ := os.Getwd()

	var failed []string
	for _, scenarioDir := range targets {
		testsDir := filepath.Join(scenarioDir, "tests")
		if _, err := os.Stat(testsDir); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "  skip %s (no tests/ dir)\n", filepath.Base(scenarioDir))
			continue
		}

		scenarioID := filepath.Base(scenarioDir)
		fmt.Printf("=== %s ===\n", scenarioID)

		cmdArgs := []string{testsDir, "-v", "--tb=short"}
		c := exec.Command(pytest, cmdArgs...)
		c.Env = append(os.Environ(),
			"WORK_DIR="+workDir,
			"TASK_DIR="+scenarioDir,
		)
		if captureBaseline {
			c.Env = append(c.Env, "XYLEM_EVAL_CAPTURE_BASELINE=1")
		}
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr

		if err := c.Run(); err != nil {
			failed = append(failed, scenarioID)
		}
		fmt.Println()
	}

	if len(failed) > 0 {
		return fmt.Errorf("%d scenario(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// baselineResult is a minimal schema for reading stored baseline JSON files.
type baselineResult struct {
	ScenarioID string  `json:"scenario_id"`
	Version    string  `json:"version"`
	Reward     float64 `json:"reward"`
	Checks     []struct {
		Name   string `json:"name"`
		Passed bool   `json:"passed"`
	} `json:"checks"`
}

// rewardFromFile reads reward.txt written by the pytest run, or returns -1.
func rewardFromFile(scenarioDir string) float64 {
	data, err := os.ReadFile(filepath.Join(scenarioDir, "reward.txt"))
	if err != nil {
		return -1
	}
	var v float64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%f", &v); err != nil {
		return -1
	}
	return v
}

func cmdEvalCompare(scenario string, all bool, evalDir string, regressionThreshold float64) error {
	pytest, err := pytestPath()
	if err != nil {
		return err
	}

	targets, err := resolveEvalTargets(evalDir, scenario, all)
	if err != nil {
		return err
	}

	baselinesDir := filepath.Join(evalDir, "baselines")
	workDir, _ := os.Getwd()

	type scenarioResult struct {
		id              string
		baselineReward  float64
		currentReward   float64
		regressions     []string
		baselineMissing bool
	}

	var results []scenarioResult

	for _, scenarioDir := range targets {
		testsDir := filepath.Join(scenarioDir, "tests")
		if _, err := os.Stat(testsDir); os.IsNotExist(err) {
			continue
		}

		scenarioID := filepath.Base(scenarioDir)

		// Load baseline
		baselinePath := filepath.Join(baselinesDir, scenarioID+".json")
		var baseline *baselineResult
		if data, err := os.ReadFile(baselinePath); err == nil {
			var b baselineResult
			if json.Unmarshal(data, &b) == nil {
				baseline = &b
			}
		}

		// Run pytest (without capturing baseline)
		cmdArgs := []string{testsDir, "-v", "--tb=short"}
		c := exec.Command(pytest, cmdArgs...)
		c.Env = append(os.Environ(),
			"WORK_DIR="+workDir,
			"TASK_DIR="+scenarioDir,
		)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		_ = c.Run() // we compare scores regardless of pytest exit code

		currentReward := rewardFromFile(scenarioDir)

		res := scenarioResult{id: scenarioID, currentReward: currentReward}
		if baseline == nil {
			res.baselineMissing = true
		} else {
			res.baselineReward = baseline.Reward
			// Reward-level regression: per-check detail is only available in Python.
			delta := currentReward - baseline.Reward
			if delta < -regressionThreshold {
				res.regressions = append(res.regressions, fmt.Sprintf("reward_drop(%.3f)", delta))
			}
		}
		results = append(results, res)
	}

	// Render comparison table
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nSCENARIO\tBASELINE\tCURRENT\tDELTA\tREGRESSIONS")
	fmt.Fprintln(tw, "--------\t--------\t-------\t-----\t-----------")

	var regressionCount int
	for _, r := range results {
		baseline := "N/A"
		delta := "N/A"
		regrStr := "none"

		if !r.baselineMissing {
			baseline = fmt.Sprintf("%.4f", r.baselineReward)
			d := r.currentReward - r.baselineReward
			sign := "+"
			if d < 0 {
				sign = ""
			}
			delta = fmt.Sprintf("%s%.4f", sign, d)
		}

		current := "N/A"
		if r.currentReward >= 0 {
			current = fmt.Sprintf("%.4f", r.currentReward)
		}

		if len(r.regressions) > 0 {
			regrStr = strings.Join(r.regressions, ", ")
			regressionCount += len(r.regressions)
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.id, baseline, current, delta, regrStr)
	}
	tw.Flush()

	if regressionCount > 0 {
		return fmt.Errorf("%d regression(s) detected", regressionCount)
	}
	return nil
}
