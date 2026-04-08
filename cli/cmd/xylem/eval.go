package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	xeval "github.com/nicholls-inc/xylem/cli/internal/eval"
)

var (
	evalLookPath   = exec.LookPath
	evalRunProcess = func(ctx context.Context, dir, name string, args ...string) error {
		return (&realCmdRunner{}).RunProcess(ctx, dir, name, args...)
	}
	evalBuildRunReport = xeval.BuildRunReport
	evalLoadRunReport  = xeval.LoadOrBuildRunReport
	evalWriteRunReport = xeval.WriteRunReport
)

type evalRunOptions struct {
	HarborConfig string
	OutputDir    string
	Task         string
	Model        string
	Attempts     int
	EnvFile      string
	RubricsDir   string
}

type evalCompareOptions struct {
	BaselineDir      string
	CandidateDir     string
	OutputPath       string
	JSON             bool
	FailOnRegression bool
}

func newEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run and compare Harbor-backed harness evaluations",
	}

	cmd.AddCommand(
		newEvalRunCmd(),
		newEvalCompareCmd(),
	)

	return cmd
}

func newEvalRunCmd() *cobra.Command {
	opts := &evalRunOptions{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the seeded Harbor eval corpus and write a xylem eval report",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("run does not accept positional arguments")
			}
			return cmdEvalRun(cmd.Context(), cmd.OutOrStdout(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.HarborConfig, "harbor-config", filepath.Join(".xylem", "eval", "harbor.yaml"), "Path to harbor.yaml")
	cmd.Flags().StringVar(&opts.OutputDir, "output", filepath.Join("jobs", "candidate"), "Directory where Harbor should write the job")
	cmd.Flags().StringVar(&opts.Task, "task", "", "Optional Harbor task/scenario filter")
	cmd.Flags().StringVar(&opts.Model, "model", "", "Override the model passed to harbor run")
	cmd.Flags().IntVar(&opts.Attempts, "attempts", 0, "Override Harbor attempts (-k)")
	cmd.Flags().StringVar(&opts.EnvFile, "env-file", "", "Optional env file passed to harbor run")
	cmd.Flags().StringVar(&opts.RubricsDir, "rubrics-dir", filepath.Join(".xylem", "eval", "rubrics"), "Directory containing Harbor rubric files")
	return cmd
}

func newEvalCompareCmd() *cobra.Command {
	opts := &evalCompareOptions{}
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare a baseline Harbor job against a candidate Harbor job",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("compare does not accept positional arguments")
			}
			return cmdEvalCompare(cmd.OutOrStdout(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.BaselineDir, "baseline", filepath.Join("jobs", "baseline"), "Baseline Harbor job directory")
	cmd.Flags().StringVar(&opts.CandidateDir, "candidate", filepath.Join("jobs", "candidate"), "Candidate Harbor job directory")
	cmd.Flags().StringVar(&opts.OutputPath, "output", "", "Optional path to write the JSON comparison report")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Print the JSON comparison report to stdout")
	cmd.Flags().BoolVar(&opts.FailOnRegression, "fail-on-regression", false, "Exit non-zero when the candidate regresses against the baseline")
	return cmd
}

func cmdEvalRun(ctx context.Context, out io.Writer, opts *evalRunOptions) error {
	if _, err := evalLookPath("harbor"); err != nil {
		return fmt.Errorf("harbor not found on PATH")
	}

	harborConfig := filepath.Clean(strings.TrimSpace(opts.HarborConfig))
	outputDir := filepath.Clean(strings.TrimSpace(opts.OutputDir))
	if outputDir == "." || outputDir == "" {
		return fmt.Errorf("output directory must not be empty")
	}
	runArgs := []string{"run", "-c", harborConfig, "-o", outputDir}
	if task := strings.TrimSpace(opts.Task); task != "" {
		runArgs = append(runArgs, "-t", task)
	}
	if model := strings.TrimSpace(opts.Model); model != "" {
		runArgs = append(runArgs, "-m", model)
	}
	if opts.Attempts > 0 {
		runArgs = append(runArgs, "-k", fmt.Sprintf("%d", opts.Attempts))
	}
	if envFile := strings.TrimSpace(opts.EnvFile); envFile != "" {
		runArgs = append(runArgs, "--env-file", envFile)
	}

	if err := evalRunProcess(ctx, ".", "harbor", runArgs...); err != nil {
		return fmt.Errorf("run harbor eval corpus: %w", err)
	}

	rubricFiles, err := rubricFiles(opts.RubricsDir)
	if err != nil {
		return err
	}
	if len(rubricFiles) == 0 {
		return fmt.Errorf("no rubric files found in %s", opts.RubricsDir)
	}

	analysisDir := filepath.Join(outputDir, "analysis")
	if err := os.MkdirAll(analysisDir, 0o755); err != nil {
		return fmt.Errorf("create analysis directory: %w", err)
	}

	for _, rubric := range rubricFiles {
		name := strings.TrimSuffix(filepath.Base(rubric), filepath.Ext(rubric))
		analysisPath := filepath.Join(analysisDir, name+".json")
		analyzeArgs := []string{"analyze", outputDir, "-r", rubric, "-o", analysisPath}
		if err := evalRunProcess(ctx, ".", "harbor", analyzeArgs...); err != nil {
			return fmt.Errorf("analyze harbor job with rubric %s: %w", rubric, err)
		}
	}

	report, err := evalBuildRunReport(outputDir)
	if err != nil {
		return fmt.Errorf("build eval report: %w", err)
	}
	reportPath := xeval.ReportPath(outputDir)
	if err := evalWriteRunReport(reportPath, report); err != nil {
		return fmt.Errorf("write eval report: %w", err)
	}

	fmt.Fprintf(out, "Wrote Harbor job to %s\n", outputDir)
	fmt.Fprintf(out, "Wrote xylem eval report to %s\n", reportPath)
	fmt.Fprintf(out, "Trials: %d, success rate: %.2f, average reward: %.4f\n",
		report.Aggregate.TrialCount,
		report.Aggregate.SuccessRate,
		report.Aggregate.AverageReward,
	)
	return nil
}

func cmdEvalCompare(out io.Writer, opts *evalCompareOptions) error {
	baselineDir := filepath.Clean(strings.TrimSpace(opts.BaselineDir))
	candidateDir := filepath.Clean(strings.TrimSpace(opts.CandidateDir))
	if baselineDir == "" || candidateDir == "" {
		return fmt.Errorf("baseline and candidate directories are required")
	}

	baseline, err := evalLoadRunReport(baselineDir)
	if err != nil {
		return fmt.Errorf("load baseline report: %w", err)
	}
	candidate, err := evalLoadRunReport(candidateDir)
	if err != nil {
		return fmt.Errorf("load candidate report: %w", err)
	}

	comparison := xeval.CompareReports(baseline, candidate)
	data, err := json.MarshalIndent(comparison, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal comparison report: %w", err)
	}

	if outputPath := strings.TrimSpace(opts.OutputPath); outputPath != "" {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return fmt.Errorf("create comparison report directory: %w", err)
		}
		if err := os.WriteFile(outputPath, append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("write comparison report: %w", err)
		}
	}

	if opts.JSON {
		if _, err := out.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("write comparison json: %w", err)
		}
		return nil
	}

	fmt.Fprintf(out, "Baseline:  %s\n", baselineDir)
	fmt.Fprintf(out, "Candidate: %s\n", candidateDir)
	fmt.Fprintf(out, "Verdict:   %s\n", comparison.Verdict)
	fmt.Fprintf(out, "Success:   %.2f -> %.2f (%+.2f)\n",
		comparison.Baseline.SuccessRate,
		comparison.Candidate.SuccessRate,
		comparison.Delta.SuccessRate,
	)
	fmt.Fprintf(out, "Reward:    %.4f -> %.4f (%+.4f)\n",
		comparison.Baseline.AverageReward,
		comparison.Candidate.AverageReward,
		comparison.Delta.AverageReward,
	)
	fmt.Fprintf(out, "Latency:   %.2fs -> %.2fs (%+.2fs)\n",
		comparison.Baseline.AverageLatencySeconds,
		comparison.Candidate.AverageLatencySeconds,
		comparison.Delta.AverageLatencySeconds,
	)
	fmt.Fprintf(out, "Cost:      $%.4f -> $%.4f (%+.4f)\n",
		comparison.Baseline.AverageCostUSDEst,
		comparison.Candidate.AverageCostUSDEst,
		comparison.Delta.AverageCostUSDEst,
	)
	if len(comparison.Regressions) > 0 {
		fmt.Fprintln(out, "Regressions:")
		for _, regression := range comparison.Regressions {
			fmt.Fprintf(out, "  - %s\n", regression)
		}
	}
	if len(comparison.Improvements) > 0 {
		fmt.Fprintln(out, "Improvements:")
		for _, improvement := range comparison.Improvements {
			fmt.Fprintf(out, "  - %s\n", improvement)
		}
	}
	if outputPath := strings.TrimSpace(opts.OutputPath); outputPath != "" {
		fmt.Fprintf(out, "Comparison report: %s\n", outputPath)
	}
	if opts.FailOnRegression && comparison.Verdict == "candidate_regressed" {
		return &exitError{code: 2, err: fmt.Errorf("eval comparison detected regressions")}
	}
	return nil
}

func rubricFiles(dir string) ([]string, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("rubrics directory %s does not exist", dir)
		}
		return nil, fmt.Errorf("read rubrics directory %s: %w", dir, err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch filepath.Ext(entry.Name()) {
		case ".toml", ".yaml", ".yml", ".json":
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}
