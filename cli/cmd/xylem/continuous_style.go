package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/continuousstyle"
)

var newContinuousStyleRunner = func() continuousstyle.CommandRunner {
	return &realCmdRunner{}
}

func newContinuousStyleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "continuous-style",
		Short: "Deterministic helpers for scheduled terminal-style analysis runs",
	}
	cmd.AddCommand(
		newContinuousStyleFileIssuesCmd(),
		newContinuousStylePostSummaryCmd(),
	)
	return cmd
}

func newContinuousStyleFileIssuesCmd() *cobra.Command {
	var reportPath, outputPath, repo, prefix string
	var limit int
	var labels []string
	cmd := &cobra.Command{
		Use:   "file-issues",
		Short: "Create top-ranked continuous-style issues with deduping",
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := continuousstyle.LoadReport(reportPath)
			if err != nil {
				return err
			}
			result, err := continuousstyle.FileIssues(context.Background(), newContinuousStyleRunner(), repo, report, limit, prefix, labels)
			if err != nil {
				return err
			}
			if err := continuousstyle.WriteJSON(outputPath, result); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&reportPath, "report", "", "Path to continuous-style findings JSON")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write filed-issues JSON")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository slug (owner/name)")
	cmd.Flags().StringVar(&prefix, "prefix", "[continuous-style]", "Issue title prefix")
	cmd.Flags().IntVar(&limit, "limit", 3, "Maximum number of issues to create")
	cmd.Flags().StringSliceVar(&labels, "labels", []string{"enhancement", "ready-for-work"}, "Labels to apply to new issues")
	cmd.MarkFlagRequired("report") //nolint:errcheck
	cmd.MarkFlagRequired("output") //nolint:errcheck
	cmd.MarkFlagRequired("repo")   //nolint:errcheck
	return cmd
}

func newContinuousStylePostSummaryCmd() *cobra.Command {
	var reportPath, filedPath, summaryPath, repo, trackingTitle string
	cmd := &cobra.Command{
		Use:   "post-summary",
		Short: "Ensure a tracking issue exists and post a continuous-style summary comment",
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := continuousstyle.LoadReport(reportPath)
			if err != nil {
				return err
			}
			filed, err := loadContinuousStyleFileResult(filedPath)
			if err != nil {
				return err
			}
			summaryBytes, err := os.ReadFile(summaryPath)
			if err != nil {
				return fmt.Errorf("read summary %q: %w", summaryPath, err)
			}
			tracking, err := continuousstyle.EnsureTrackingIssue(context.Background(), newContinuousStyleRunner(), repo, trackingTitle)
			if err != nil {
				return err
			}
			if err := continuousstyle.PostSummary(context.Background(), newContinuousStyleRunner(), repo, tracking.Number, report, filed, string(summaryBytes)); err != nil {
				return err
			}
			fmt.Printf("Posted summary to issue #%d\n", tracking.Number)
			return nil
		},
	}
	cmd.Flags().StringVar(&reportPath, "report", "", "Path to continuous-style findings JSON")
	cmd.Flags().StringVar(&filedPath, "filed", "", "Path to filed-issues JSON")
	cmd.Flags().StringVar(&summaryPath, "summary", "", "Path to markdown summary")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository slug (owner/name)")
	cmd.Flags().StringVar(&trackingTitle, "tracking-title", continuousstyle.DefaultTrackingIssueTitle, "Tracking issue title")
	cmd.MarkFlagRequired("report")  //nolint:errcheck
	cmd.MarkFlagRequired("filed")   //nolint:errcheck
	cmd.MarkFlagRequired("summary") //nolint:errcheck
	cmd.MarkFlagRequired("repo")    //nolint:errcheck
	return cmd
}

func loadContinuousStyleFileResult(path string) (*continuousstyle.FileResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read filed issues %q: %w", path, err)
	}
	var filed continuousstyle.FileResult
	if err := json.Unmarshal(data, &filed); err != nil {
		return nil, fmt.Errorf("parse filed issues %q: %w", path, err)
	}
	return &filed, nil
}
