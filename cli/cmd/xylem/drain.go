package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

var newTracer = observability.NewTracer

func newDrainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drain",
		Short: "Dequeue pending vessels and launch Claude sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			return cmdDrain(deps.cfg, deps.q, deps.wt, dryRun)
		},
	}
	cmd.Flags().Bool("dry-run", false, "Preview what would be drained")
	return cmd
}

func cmdDrain(cfg *config.Config, q *queue.Queue, wt *worktree.Manager, dryRun bool) error {
	if dryRun {
		return dryRunDrain(cfg, q)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cmdRunner := newCmdRunner(cfg)
	r, cleanup := buildDrainRunner(cfg, q, wt, cmdRunner)
	defer cleanup()
	r.Reporter = buildReporter(cfg, cmdRunner)

	// Check waiting vessels before draining pending ones
	r.CheckWaitingVessels(ctx)

	result, err := r.DrainAndWait(ctx)
	if err != nil {
		return &exitError{code: 2, err: fmt.Errorf("drain error: %w", err)}
	}
	maybeAutoGenerateHarnessReview(cfg, result)
	fmt.Printf("Completed %d, failed %d, skipped %d, waiting %d\n", result.Completed, result.Failed, result.Skipped, result.Waiting)
	if result.Failed > 0 {
		return &exitError{code: 1}
	}
	return nil
}

func buildDrainRunner(cfg *config.Config, q *queue.Queue, wt runner.WorktreeManager, cmdRunner *realCmdRunner) (*runner.Runner, func()) {
	tracer := buildConfiguredTracer(cfg)
	if configurable, ok := wt.(interface{ SetProtectedSurfaces([]string) }); ok {
		configurable.SetProtectedSurfaces(cfg.EffectiveProtectedSurfaces())
	}

	r := runner.New(cfg, q, wt, cmdRunner)
	r.Sources = buildSourceMap(cfg, q, cmdRunner)
	wireRunnerScaffolding(cfg, r, tracer)

	return r, func() {
		shutdownConfiguredTracer(tracer)
	}
}

func wireRunnerScaffolding(cfg *config.Config, r *runner.Runner, tracer *observability.Tracer) {
	auditLogPath := filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath())
	auditLog := intermediary.NewAuditLog(auditLogPath)

	r.Intermediary = intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)
	r.AuditLog = auditLog
	r.Tracer = tracer
}

func buildConfiguredTracer(cfg *config.Config) *observability.Tracer {
	if !cfg.ObservabilityEnabled() {
		return nil
	}

	tracer, err := newTracer(observability.TracerConfig{
		ServiceName:    "xylem",
		ServiceVersion: "",
		Endpoint:       cfg.Observability.Endpoint,
		Insecure:       cfg.Observability.Insecure,
		SampleRate:     cfg.ObservabilitySampleRate(),
	})
	if err != nil {
		log.Printf("warn: failed to initialize tracer: %v", err)
		return nil
	}

	return tracer
}

func shutdownConfiguredTracer(tracer *observability.Tracer) {
	if tracer == nil {
		return
	}

	if err := tracer.Shutdown(context.Background()); err != nil {
		log.Printf("warn: %v", fmt.Errorf("shutdown tracer: %w", err))
	}
}

func buildSourceMap(cfg *config.Config, q *queue.Queue, cmdRunner source.CommandRunner) map[string]source.Source {
	sources := make(map[string]source.Source)
	for _, srcCfg := range cfg.Sources {
		switch srcCfg.Type {
		case "github":
			tasks := make(map[string]source.GitHubTask, len(srcCfg.Tasks))
			for name, t := range srcCfg.Tasks {
				tasks[name] = source.GitHubTaskFromConfig(t)
			}
			gh := &source.GitHub{
				Repo:      srcCfg.Repo,
				Tasks:     tasks,
				Exclude:   srcCfg.Exclude,
				Queue:     q,
				CmdRunner: cmdRunner,
			}
			sources[gh.Name()] = gh
		case "github-pr":
			tasks := make(map[string]source.GitHubTask, len(srcCfg.Tasks))
			for name, t := range srcCfg.Tasks {
				tasks[name] = source.GitHubTaskFromConfig(t)
			}
			pr := &source.GitHubPR{
				Repo:      srcCfg.Repo,
				Tasks:     tasks,
				Exclude:   srcCfg.Exclude,
				Queue:     q,
				CmdRunner: cmdRunner,
			}
			sources[pr.Name()] = pr
		case "github-pr-events":
			prEventsTasks := make(map[string]source.PREventsTask, len(srcCfg.Tasks))
			for name, t := range srcCfg.Tasks {
				task := source.PREventsTask{
					Workflow: t.Workflow,
				}
				if t.On != nil {
					task.Labels = t.On.Labels
					task.ReviewSubmitted = t.On.ReviewSubmitted
					task.ChecksFailed = t.On.ChecksFailed
					task.Commented = t.On.Commented
				}
				prEventsTasks[name] = task
			}
			pre := &source.GitHubPREvents{
				Repo:      srcCfg.Repo,
				Tasks:     prEventsTasks,
				Exclude:   srcCfg.Exclude,
				Queue:     q,
				CmdRunner: cmdRunner,
			}
			sources[pre.Name()] = pre
		case "github-merge":
			mergeTasks := make(map[string]source.MergeTask, len(srcCfg.Tasks))
			for name, t := range srcCfg.Tasks {
				mergeTasks[name] = source.MergeTask{
					Workflow: t.Workflow,
				}
			}
			gm := &source.GitHubMerge{
				Repo:      srcCfg.Repo,
				Tasks:     mergeTasks,
				Queue:     q,
				CmdRunner: cmdRunner,
			}
			sources[gm.Name()] = gm
		}
	}
	return sources
}

func buildReporter(cfg *config.Config, cmdRunner reporter.Runner) *reporter.Reporter {
	// Find the first GitHub-based source repo for reporting
	for _, srcCfg := range cfg.Sources {
		switch srcCfg.Type {
		case "github", "github-pr", "github-pr-events", "github-merge":
			if srcCfg.Repo != "" {
				return &reporter.Reporter{Runner: cmdRunner, Repo: srcCfg.Repo}
			}
		}
	}
	return nil
}

func dryRunDrain(cfg *config.Config, q *queue.Queue) error {
	vessels, err := q.ListByState(queue.StatePending)
	if err != nil {
		return &exitError{code: 2, err: fmt.Errorf("error reading queue: %w", err)}
	}
	if len(vessels) == 0 {
		fmt.Println("No pending vessels.")
		return nil
	}
	fmt.Printf("%-14s  %-14s  %-20s  %s\n", "ID", "Source", "Workflow", "Command")
	fmt.Printf("%-14s  %-14s  %-20s  %s\n", "----", "------", "-----", "-------")
	for _, j := range vessels {
		wf := j.Workflow
		if wf == "" {
			wf = "(prompt)"
		}
		var cmd string
		if j.Prompt != "" {
			cmd = fmt.Sprintf("%s -p %q --max-turns %d", cfg.Claude.Command, truncate(j.Prompt, 40), cfg.MaxTurns)
		} else {
			cmd = fmt.Sprintf("%s -p \"/%s %s\" --max-turns %d", cfg.Claude.Command, j.Workflow, j.Ref, cfg.MaxTurns)
		}
		fmt.Printf("%-14s  %-14s  %-20s  %s\n", j.ID, j.Source, wf, cmd)
	}
	fmt.Printf("\n%d vessel(s) would be drained (dry-run — no sessions launched)\n", len(vessels))
	return nil
}
