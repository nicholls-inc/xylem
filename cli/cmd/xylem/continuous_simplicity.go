package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/simplicity"
)

var simplicityNow = func() time.Time {
	return time.Now().UTC()
}

var newSimplicityRunner = func() simplicity.CommandRunner {
	return &realCmdRunner{}
}

func newContinuousSimplicityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "continuous-simplicity",
		Short: "Deterministic helpers for the continuous-simplicity workflow",
	}
	cmd.AddCommand(
		newContinuousSimplicityScanChangesCmd(),
		newContinuousSimplicityPlanPRsCmd(),
		newContinuousSimplicityOpenPRsCmd(),
	)
	return cmd
}

func newContinuousSimplicityScanChangesCmd() *cobra.Command {
	var repoRoot, outputPath string
	var windowDays int
	cmd := &cobra.Command{
		Use:   "scan-changes",
		Short: "Write a durable manifest of recently changed files",
		RunE: func(cmd *cobra.Command, args []string) error {
			manifest, err := simplicity.ScanRecentChanges(context.Background(), newSimplicityRunner(), repoRoot, simplicityNow(), windowDays)
			if err != nil {
				return err
			}
			if err := simplicity.WriteJSON(outputPath, manifest); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&repoRoot, "repo-root", ".", "Repository root to inspect")
	cmd.Flags().IntVar(&windowDays, "days", simplicity.DefaultWindowDays, "Number of days of git history to inspect")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write the changed-file manifest JSON")
	cmd.MarkFlagRequired("output") //nolint:errcheck
	return cmd
}

func newContinuousSimplicityPlanPRsCmd() *cobra.Command {
	var simplificationsPath, duplicationsPath, outputPath string
	var repo, baseBranch string
	var maxPRs, minDuplicateLines, minDuplicateLocations int
	var minConfidence float64
	var excludeGlobs []string
	cmd := &cobra.Command{
		Use:   "plan-prs",
		Short: "Filter and cap simplification candidates into a PR plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			simplifications, err := simplicity.LoadFindings(simplificationsPath)
			if err != nil {
				return err
			}
			duplications, err := simplicity.LoadFindings(duplicationsPath)
			if err != nil {
				return err
			}
			plan, err := simplicity.BuildPlan(effectiveRepo(repo), effectiveBaseBranch(baseBranch), simplicity.PlanOptions{
				MaxPRs:                maxPRs,
				MinConfidence:         minConfidence,
				MinDuplicateLines:     minDuplicateLines,
				MinDuplicateLocations: minDuplicateLocations,
				ExcludeGlobs:          append([]string(nil), excludeGlobs...),
			}, simplifications, duplications, simplicityNow())
			if err != nil {
				return err
			}
			if err := simplicity.WriteJSON(outputPath, plan); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&simplificationsPath, "simplifications", "", "Path to simplification findings JSON")
	cmd.Flags().StringVar(&duplicationsPath, "duplications", "", "Path to duplication findings JSON")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write the PR plan JSON")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository slug (defaults to config repo)")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", "Base branch for generated PRs (defaults to config/default main)")
	cmd.Flags().IntVar(&maxPRs, "max-prs", simplicity.DefaultMaxPRs, "Maximum PRs to select for this run")
	cmd.Flags().Float64Var(&minConfidence, "min-confidence", simplicity.DefaultMinConfidence, "Minimum confidence required to keep a finding")
	cmd.Flags().IntVar(&minDuplicateLines, "min-duplicate-lines", simplicity.DefaultMinDuplicateLines, "Minimum duplicated lines for duplication findings")
	cmd.Flags().IntVar(&minDuplicateLocations, "min-duplicate-locations", simplicity.DefaultMinDuplicateTargets, "Minimum duplicated locations for duplication findings")
	cmd.Flags().StringSliceVar(&excludeGlobs, "exclude-glob", append([]string(nil), simplicity.DefaultExcludeGlobs...), "Low-value path globs to exclude from duplication planning")
	cmd.MarkFlagRequired("simplifications") //nolint:errcheck
	cmd.MarkFlagRequired("duplications")    //nolint:errcheck
	cmd.MarkFlagRequired("output")          //nolint:errcheck
	return cmd
}

func newContinuousSimplicityOpenPRsCmd() *cobra.Command {
	var planPath, outputPath string
	cmd := &cobra.Command{
		Use:   "open-prs",
		Short: "Create PRs for branches listed in a continuous-simplicity plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := simplicity.LoadPlan(planPath)
			if err != nil {
				return err
			}
			result, err := simplicity.OpenPRs(context.Background(), newSimplicityRunner(), plan, simplicityNow())
			if err != nil {
				return err
			}
			if err := simplicity.WriteJSON(outputPath, result); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&planPath, "plan", "", "Path to the PR plan JSON")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write the PR creation result JSON")
	cmd.MarkFlagRequired("plan")   //nolint:errcheck
	cmd.MarkFlagRequired("output") //nolint:errcheck
	return cmd
}

func effectiveRepo(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if deps != nil && deps.cfg != nil {
		return deps.cfg.Repo
	}
	return ""
}

func effectiveBaseBranch(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if deps != nil && deps.cfg != nil && deps.cfg.DefaultBranch != "" {
		return deps.cfg.DefaultBranch
	}
	return "main"
}
