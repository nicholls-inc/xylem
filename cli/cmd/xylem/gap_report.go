package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/gapreport"
)

func newGapReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gap-report",
		Short: "Deterministic helpers for SoTA gap-analysis workflows",
	}
	cmd.AddCommand(
		newGapReportDiffCmd(),
		newGapReportFileIssuesCmd(),
		newGapReportPostSummaryCmd(),
		newGapReportGuardCmd(),
	)
	return cmd
}

func newGapReportDiffCmd() *cobra.Command {
	var previousPath, currentPath, outputPath, writeCurrentPath string
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Compare two gap snapshots deterministically",
		RunE: func(cmd *cobra.Command, args []string) error {
			previous, err := gapreport.LoadSnapshot(previousPath)
			if err != nil {
				return err
			}
			current, err := gapreport.LoadSnapshot(currentPath)
			if err != nil {
				return err
			}
			delta, err := gapreport.Diff(previous, current)
			if err != nil {
				return fmt.Errorf("diff snapshots: %w", err)
			}
			if err := gapreport.WriteJSON(outputPath, delta); err != nil {
				return err
			}
			if strings.TrimSpace(writeCurrentPath) != "" {
				if err := gapreport.CopySnapshot(writeCurrentPath, current); err != nil {
					return err
				}
			}
			fmt.Printf("Wrote %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&previousPath, "previous", "", "Path to previous snapshot JSON")
	cmd.Flags().StringVar(&currentPath, "current", "", "Path to current snapshot JSON")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write delta JSON")
	cmd.Flags().StringVar(&writeCurrentPath, "write-current", "", "Optional canonical snapshot path to overwrite with the current snapshot")
	cmd.MarkFlagRequired("previous") //nolint:errcheck
	cmd.MarkFlagRequired("current")  //nolint:errcheck
	cmd.MarkFlagRequired("output")   //nolint:errcheck
	return cmd
}

func newGapReportFileIssuesCmd() *cobra.Command {
	var deltaPath, outputPath, repo, prefix string
	var limit int
	var labels []string
	cmd := &cobra.Command{
		Use:   "file-issues",
		Short: "Create top-ranked gap issues with deduping",
		RunE: func(cmd *cobra.Command, args []string) error {
			delta, err := loadDelta(deltaPath)
			if err != nil {
				return err
			}
			result, err := gapreport.FileIssues(context.Background(), &realCmdRunner{}, repo, delta, limit, prefix, labels)
			if err != nil {
				return err
			}
			if err := gapreport.WriteJSON(outputPath, result); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&deltaPath, "delta", "", "Path to delta JSON")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write filed-issues JSON")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository slug (owner/name)")
	cmd.Flags().StringVar(&prefix, "prefix", "[sota-gap]", "Issue title prefix")
	cmd.Flags().IntVar(&limit, "limit", 3, "Maximum number of issues to create")
	cmd.Flags().StringSliceVar(&labels, "labels", []string{"enhancement", "ready-for-work"}, "Labels to apply to new issues")
	cmd.MarkFlagRequired("delta")  //nolint:errcheck
	cmd.MarkFlagRequired("output") //nolint:errcheck
	cmd.MarkFlagRequired("repo")   //nolint:errcheck
	return cmd
}

func newGapReportPostSummaryCmd() *cobra.Command {
	var deltaPath, filedPath, reportPath, repo, trackingTitle string
	cmd := &cobra.Command{
		Use:   "post-summary",
		Short: "Ensure a tracking issue exists and post a weekly summary comment",
		RunE: func(cmd *cobra.Command, args []string) error {
			delta, err := loadDelta(deltaPath)
			if err != nil {
				return err
			}
			filed, err := loadFileResult(filedPath)
			if err != nil {
				return err
			}
			reportBytes, err := os.ReadFile(reportPath)
			if err != nil {
				return fmt.Errorf("read report %q: %w", reportPath, err)
			}
			tracking, err := gapreport.EnsureTrackingIssue(context.Background(), &realCmdRunner{}, repo, trackingTitle)
			if err != nil {
				return err
			}
			if err := gapreport.PostSummary(context.Background(), &realCmdRunner{}, repo, tracking.Number, delta, filed, string(reportBytes)); err != nil {
				return err
			}
			fmt.Printf("Posted summary to issue #%d\n", tracking.Number)
			return nil
		},
	}
	cmd.Flags().StringVar(&deltaPath, "delta", "", "Path to delta JSON")
	cmd.Flags().StringVar(&filedPath, "filed", "", "Path to filed-issues JSON")
	cmd.Flags().StringVar(&reportPath, "report", "", "Path to markdown gap report")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository slug (owner/name)")
	cmd.Flags().StringVar(&trackingTitle, "tracking-title", gapreport.DefaultTrackingIssueTitle, "Tracking issue title")
	cmd.MarkFlagRequired("delta")  //nolint:errcheck
	cmd.MarkFlagRequired("filed")  //nolint:errcheck
	cmd.MarkFlagRequired("report") //nolint:errcheck
	cmd.MarkFlagRequired("repo")   //nolint:errcheck
	return cmd
}

func newGapReportGuardCmd() *cobra.Command {
	var deltaPath, filedPath string
	cmd := &cobra.Command{
		Use:   "guard",
		Short: "Fail when a run produced no new issues and no improvement",
		RunE: func(cmd *cobra.Command, args []string) error {
			delta, err := loadDelta(deltaPath)
			if err != nil {
				return err
			}
			filed, err := loadFileResult(filedPath)
			if err != nil {
				return err
			}
			if gapreport.ShouldFail(delta, filed) {
				return fmt.Errorf("gap-analysis produced zero new issues and no snapshot improvement")
			}
			fmt.Println("Gap-analysis guard passed")
			return nil
		},
	}
	cmd.Flags().StringVar(&deltaPath, "delta", "", "Path to delta JSON")
	cmd.Flags().StringVar(&filedPath, "filed", "", "Path to filed-issues JSON")
	cmd.MarkFlagRequired("delta") //nolint:errcheck
	cmd.MarkFlagRequired("filed") //nolint:errcheck
	return cmd
}

func loadDelta(path string) (*gapreport.Delta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read delta %q: %w", path, err)
	}
	var delta gapreport.Delta
	if err := json.Unmarshal(data, &delta); err != nil {
		return nil, fmt.Errorf("parse delta %q: %w", path, err)
	}
	return &delta, nil
}

func loadFileResult(path string) (*gapreport.FileResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read filed issues %q: %w", path, err)
	}
	var filed gapreport.FileResult
	if err := json.Unmarshal(data, &filed); err != nil {
		return nil, fmt.Errorf("parse filed issues %q: %w", path, err)
	}
	return &filed, nil
}
