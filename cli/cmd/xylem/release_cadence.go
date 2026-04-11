package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/releasecadence"
)

var applyReleaseCadenceReadyLabel = releasecadence.ApplyReadyLabel

func newReleaseCadenceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release-cadence",
		Short: "Deterministic helpers for release-please merge cadence",
	}
	cmd.AddCommand(newReleaseCadenceLabelReadyCmd())
	return cmd
}

func newReleaseCadenceLabelReadyCmd() *cobra.Command {
	var repo string
	var readyLabel string
	var optOutLabel string
	var minAge time.Duration
	var minCommits int
	var nowRaw string

	cmd := &cobra.Command{
		Use:   "label-ready",
		Short: "Label a mature release-please PR as ready for daemon auto-merge",
		RunE: func(cmd *cobra.Command, args []string) error {
			now, err := parseOptionalNow(nowRaw)
			if err != nil {
				return err
			}
			result, err := cmdReleaseCadenceLabelReady(context.Background(), repo, readyLabel, optOutLabel, minAge, minCommits, now, &realCmdRunner{})
			if err != nil {
				return err
			}
			if result.Action == releasecadence.ActionNoop {
				fmt.Printf("XYLEM_NOOP: %s\n", result.Reason)
				return nil
			}
			fmt.Printf("Applied %s to release PR #%d after %s with %d queued commit(s)\n",
				result.ReadyLabel,
				result.PRNumber,
				result.Age.Round(24*time.Hour),
				result.CommitCount,
			)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository slug (owner/name); defaults to repo from config")
	cmd.Flags().StringVar(&readyLabel, "ready-label", releasecadence.DefaultReadyLabel, "Label to apply once the release PR is mature")
	cmd.Flags().StringVar(&optOutLabel, "opt-out-label", releasecadence.DefaultOptOutLabel, "Opt-out label that blocks automatic release labeling")
	cmd.Flags().DurationVar(&minAge, "min-age", releasecadence.DefaultMinAge, "Minimum release PR age before labeling it ready")
	cmd.Flags().IntVar(&minCommits, "min-commits", releasecadence.DefaultMinCommits, "Minimum queued release PR commit count before labeling it ready")
	cmd.Flags().StringVar(&nowRaw, "now", "", "Optional RFC3339 timestamp override for deterministic runs")
	return cmd
}

func cmdReleaseCadenceLabelReady(ctx context.Context, repo, readyLabel, optOutLabel string, minAge time.Duration, minCommits int, now time.Time, runner releasecadence.CommandRunner) (*releasecadence.Result, error) {
	if strings.TrimSpace(repo) == "" {
		if deps == nil || deps.cfg == nil {
			return nil, fmt.Errorf("release-cadence label-ready requires --repo or loaded config")
		}
		repo = detectLessonsRepo(deps.cfg)
	}
	if strings.TrimSpace(repo) == "" {
		return nil, fmt.Errorf("release-cadence label-ready requires --repo or loaded config repo")
	}
	result, err := applyReleaseCadenceReadyLabel(ctx, runner, releasecadence.Options{
		Repo:        repo,
		ReadyLabel:  readyLabel,
		OptOutLabel: optOutLabel,
		MinAge:      minAge,
		MinCommits:  minCommits,
		Now:         now,
	})
	if err != nil {
		return nil, fmt.Errorf("apply release cadence ready label: %w", err)
	}
	return result, nil
}
