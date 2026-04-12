package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/discussion"
	"github.com/nicholls-inc/xylem/cli/internal/notify"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

func newReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Collect and display daemon status report",
		Long: `Collect vessel metrics, daemon health, and fleet status into a report.

By default, prints to stdout. Use --post to also post to the configured
GitHub Discussion. Use --json for machine-readable output.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			post, _ := cmd.Flags().GetBool("post")
			jsonMode, _ := cmd.Flags().GetBool("json")
			root, _ := cmd.Flags().GetString("root")

			scopedDeps, err := doctorDepsForRoot(deps, root)
			if err != nil {
				return err
			}
			return cmdReport(scopedDeps.cfg, scopedDeps.q, post, jsonMode)
		},
	}
	cmd.Flags().Bool("post", false, "Post report to configured GitHub Discussion")
	cmd.Flags().Bool("json", false, "Output as JSON")
	cmd.Flags().String("root", "", "Collect status from the xylem state rooted at this directory")
	return cmd
}

func cmdReport(cfg *config.Config, q *queue.Queue, post, jsonMode bool) error {
	now := time.Now()
	report := reporter.CollectStatus(context.Background(), reporter.StatusDeps{
		StateDir:      cfg.StateDir,
		Queue:         q,
		FleetAnalyzer: fleetAnalyzerFromRunner,
	}, now)

	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	if !post {
		fmt.Print(reporter.FormatPlainText(report))
		return nil
	}

	// Post to GitHub Discussion.
	fmt.Print(reporter.FormatPlainText(report))

	dcfg := cfg.Notifications.GitHubDiscussion
	if !dcfg.Enabled {
		return fmt.Errorf("notifications.github_discussion.enabled is false; enable it in .xylem.yml to post")
	}
	repoSlug := dcfg.Repo
	if repoSlug == "" {
		repoSlug = resolveDefaultRepo(cfg)
	}
	if repoSlug == "" {
		return fmt.Errorf("notifications.github_discussion.repo not set and no source repo found")
	}

	cmdRunner := newCmdRunner(cfg)
	pub := &discussion.Publisher{Runner: cmdRunner}
	disc, err := notify.NewDiscussion(pub, repoSlug, dcfg.Category, dcfg.Title)
	if err != nil {
		return fmt.Errorf("create discussion notifier: %w", err)
	}

	md := reporter.FormatMarkdown(report)
	if err := disc.PostStatus(context.Background(), notify.StatusReport{Markdown: md}); err != nil {
		return fmt.Errorf("post to discussion: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Posted status to GitHub Discussion\n")
	return nil
}

// fleetAnalyzerFromRunner bridges the reporter's FleetAnalyzer callback to the
// runner package, resolving the import cycle.
func fleetAnalyzerFromRunner(stateDir string, vessels []queue.Vessel) reporter.FleetHealth {
	ids := make([]string, len(vessels))
	for i, v := range vessels {
		ids[i] = v.ID
	}
	summaries, _ := runner.LoadVesselSummaries(stateDir, ids)
	fleet := runner.AnalyzeFleetStatus(vessels, summaries)
	patterns := make([]reporter.FleetPattern, len(fleet.Patterns))
	for i, p := range fleet.Patterns {
		patterns[i] = reporter.FleetPattern{Code: p.Code, Count: p.Count}
	}
	return reporter.FleetHealth{
		Healthy:   fleet.Healthy,
		Degraded:  fleet.Degraded,
		Unhealthy: fleet.Unhealthy,
		Patterns:  patterns,
	}
}

// resolveDefaultRepo finds the first GitHub source's repo slug from config.
func resolveDefaultRepo(cfg *config.Config) string {
	if cfg.Repo != "" {
		return cfg.Repo
	}
	for _, src := range cfg.Sources {
		if src.Repo != "" {
			return src.Repo
		}
	}
	return ""
}
