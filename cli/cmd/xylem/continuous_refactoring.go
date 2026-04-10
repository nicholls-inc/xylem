package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/continuousrefactoring"
)

var continuousRefactoringNow = func() time.Time {
	return time.Now().UTC()
}

var newContinuousRefactoringRunner = func() continuousrefactoring.CommandRunner {
	return &realCmdRunner{}
}

func newContinuousRefactoringCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "continuous-refactoring",
		Short: "Deterministic helpers for the continuous-refactoring workflow",
	}
	cmd.AddCommand(
		newContinuousRefactoringInspectCmd(),
		newContinuousRefactoringOpenIssuesCmd(),
	)
	return cmd
}

func newContinuousRefactoringInspectCmd() *cobra.Command {
	var repoRoot, sourceName, outputPath string
	var nowRaw string
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Scan configured source directories for refactor and file-diet candidates",
		RunE: func(cmd *cobra.Command, args []string) error {
			if deps == nil || deps.cfg == nil {
				return fmt.Errorf("continuous-refactoring inspect requires loaded config")
			}
			now, err := parseContinuousRefactoringNow(nowRaw)
			if err != nil {
				return err
			}
			manifest, err := continuousrefactoring.Inspect(deps.cfg, repoRoot, sourceName, now)
			if err != nil {
				return err
			}
			if err := continuousrefactoring.WriteJSON(outputPath, manifest); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&repoRoot, "repo-root", ".", "Repository root to inspect")
	cmd.Flags().StringVar(&sourceName, "source", "", "Config source name to inspect")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write the inspection manifest JSON")
	cmd.Flags().StringVar(&nowRaw, "now", "", "Optional RFC3339 timestamp override for deterministic runs")
	cmd.MarkFlagRequired("source") //nolint:errcheck
	cmd.MarkFlagRequired("output") //nolint:errcheck
	return cmd
}

func newContinuousRefactoringOpenIssuesCmd() *cobra.Command {
	var manifestPath, outputPath, sourceName, modeRaw string
	var nowRaw string
	cmd := &cobra.Command{
		Use:   "open-issues",
		Short: "Create deduplicated refactor issues from a continuous-refactoring manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			now, err := parseContinuousRefactoringNow(nowRaw)
			if err != nil {
				return err
			}
			mode, err := parseContinuousRefactoringMode(modeRaw)
			if err != nil {
				return err
			}
			manifest, err := continuousrefactoring.LoadManifest(manifestPath)
			if err != nil {
				return err
			}
			if sourceName != "" {
				manifest.SourceName = sourceName
			}
			result, err := continuousrefactoring.OpenIssues(context.Background(), newContinuousRefactoringRunner(), manifest, mode, now)
			if err != nil {
				return err
			}
			if err := continuousrefactoring.WriteJSON(outputPath, result); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to the continuous-refactoring manifest JSON")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write the issue creation result JSON")
	cmd.Flags().StringVar(&sourceName, "source", "", "Optional source name override for result metadata")
	cmd.Flags().StringVar(&modeRaw, "mode", string(continuousrefactoring.ModeSemantic), "Issue mode to open: semantic or file-diet")
	cmd.Flags().StringVar(&nowRaw, "now", "", "Optional RFC3339 timestamp override for deterministic runs")
	cmd.MarkFlagRequired("manifest") //nolint:errcheck
	cmd.MarkFlagRequired("output")   //nolint:errcheck
	return cmd
}

func parseContinuousRefactoringNow(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return continuousRefactoringNow(), nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse --now: %w", err)
	}
	return parsed.UTC(), nil
}

func parseContinuousRefactoringMode(raw string) (continuousrefactoring.Mode, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case string(continuousrefactoring.ModeSemantic):
		return continuousrefactoring.ModeSemantic, nil
	case string(continuousrefactoring.ModeFileDiet):
		return continuousrefactoring.ModeFileDiet, nil
	default:
		return "", fmt.Errorf("parse --mode: unsupported mode %q", raw)
	}
}
