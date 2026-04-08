package main

import (
	"fmt"
	"log"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	reviewpkg "github.com/nicholls-inc/xylem/cli/internal/review"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

var generateHarnessReview = func(cfg *config.Config) (*reviewpkg.Result, error) {
	return reviewpkg.Generate(cfg.StateDir, reviewpkg.Options{
		LookbackRuns: cfg.HarnessReviewLookbackRuns(),
		MinSamples:   cfg.HarnessReviewMinSamples(),
		OutputDir:    cfg.HarnessReviewOutputDir(),
	})
}

func newReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "review",
		Short: "Aggregate recurring harness review inputs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdReview(deps.cfg)
		},
	}
}

func cmdReview(cfg *config.Config) error {
	result, err := generateHarnessReview(cfg)
	if err != nil {
		return fmt.Errorf("generate harness review: %w", err)
	}

	fmt.Printf("Wrote %s and %s\n\n%s", result.JSONPath, result.MarkdownPath, result.Markdown)
	return nil
}

func maybeAutoGenerateHarnessReview(cfg *config.Config, result runner.DrainResult) {
	if result.Completed+result.Failed == 0 {
		return
	}

	switch cfg.HarnessReviewCadence() {
	case "manual":
		return
	case "every_drain":
		// Always generate after a drain that processed at least one vessel.
	case "every_n_runs":
		totalRuns, err := reviewpkg.CountAvailableRuns(cfg.StateDir)
		if err != nil {
			log.Printf("warn: count harness review runs: %v", err)
			return
		}
		latest, err := reviewpkg.LoadLatestReport(cfg.StateDir, cfg.HarnessReviewOutputDir())
		if err != nil {
			log.Printf("warn: load latest harness review: %v", err)
			return
		}
		lastReviewed := 0
		if latest != nil {
			lastReviewed = latest.TotalRunsObserved
		}
		if totalRuns-lastReviewed < cfg.HarnessReviewEveryNRuns() {
			return
		}
	default:
		return
	}

	if _, err := generateHarnessReview(cfg); err != nil {
		log.Printf("warn: generate harness review: %v", err)
	}
}
