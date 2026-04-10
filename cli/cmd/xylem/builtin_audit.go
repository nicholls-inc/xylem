package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	reviewpkg "github.com/nicholls-inc/xylem/cli/internal/review"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

type auditCommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

func runBuiltInScheduledVessels(ctx context.Context, cfg *config.Config, q *queue.Queue, cmdRunner auditCommandRunner) (runner.DrainResult, error) {
	pending, err := q.ListByState(queue.StatePending)
	if err != nil {
		return runner.DrainResult{}, fmt.Errorf("list pending vessels: %w", err)
	}

	var result runner.DrainResult
	for _, vessel := range pending {
		if !isBuiltInScheduledVessel(cfg, vessel) {
			continue
		}
		result.Launched++

		if err := q.Update(vessel.ID, queue.StateRunning, ""); err != nil {
			return result, fmt.Errorf("mark built-in vessel %s running: %w", vessel.ID, err)
		}

		updated, err := q.FindByID(vessel.ID)
		if err != nil {
			return result, fmt.Errorf("load built-in vessel %s after start: %w", vessel.ID, err)
		}

		state := queue.StateCompleted
		errMsg := ""
		repo := resolveScheduledAuditRepo(cfg, *updated)
		if strings.TrimSpace(repo) == "" {
			state = queue.StateFailed
			errMsg = fmt.Sprintf("%s requires a source repo for GitHub issue publication", vessel.Workflow)
		} else if err := runBuiltInScheduledWorkflow(ctx, cfg, vessel.Workflow, repo, cmdRunner); err != nil {
			state = queue.StateFailed
			errMsg = err.Error()
		}

		if err := q.Update(vessel.ID, state, errMsg); err != nil {
			return result, fmt.Errorf("finish built-in vessel %s: %w", vessel.ID, err)
		}
		finalVessel, err := q.FindByID(vessel.ID)
		if err != nil {
			return result, fmt.Errorf("load built-in vessel %s after completion: %w", vessel.ID, err)
		}
		if err := persistBuiltInAuditSummary(cfg, *finalVessel); err != nil {
			return result, err
		}

		if state == queue.StateCompleted {
			result.Completed++
		} else {
			result.Failed++
			log.Printf("warn: built-in vessel %s failed: %s", vessel.ID, errMsg)
		}
	}
	return result, nil
}

func isBuiltInScheduledWorkflow(workflow string) bool {
	switch workflow {
	case reviewpkg.ContextWeightAuditWorkflow, reviewpkg.HarnessGapAnalysisWorkflow, reviewpkg.WorkflowHealthReportWorkflow:
		return true
	default:
		return false
	}
}

func isBuiltInScheduledVessel(cfg *config.Config, vessel queue.Vessel) bool {
	if !isBuiltInScheduledWorkflow(vessel.Workflow) {
		return false
	}
	switch vessel.Source {
	case "scheduled", "schedule":
		return true
	}
	if cfg == nil || vessel.Meta == nil {
		return false
	}
	name := strings.TrimSpace(vessel.Meta["config_source"])
	if name == "" {
		return false
	}
	srcCfg, ok := cfg.Sources[name]
	if !ok {
		return false
	}
	return srcCfg.Type == "scheduled" || srcCfg.Type == "schedule"
}

func runBuiltInScheduledWorkflow(ctx context.Context, cfg *config.Config, workflowName, repo string, cmdRunner auditCommandRunner) error {
	now := time.Now().UTC()
	switch workflowName {
	case reviewpkg.ContextWeightAuditWorkflow:
		_, err := reviewpkg.RunContextWeightAudit(ctx, cfg.StateDir, repo, cmdRunner, reviewpkg.ContextWeightOptions{
			LookbackRuns: cfg.HarnessReviewLookbackRuns(),
			MinSamples:   cfg.HarnessReviewMinSamples(),
			OutputDir:    cfg.HarnessReviewOutputDir(),
			Now:          now,
		})
		return err
	case reviewpkg.HarnessGapAnalysisWorkflow:
		_, err := reviewpkg.RunHarnessGapAnalysis(ctx, cfg.StateDir, repo, cmdRunner, reviewpkg.HarnessGapOptions{
			OutputDir: cfg.HarnessReviewOutputDir(),
			Now:       now,
		})
		return err
	case reviewpkg.WorkflowHealthReportWorkflow:
		_, err := reviewpkg.RunWorkflowHealthReport(ctx, cfg.StateDir, repo, cmdRunner, reviewpkg.WorkflowHealthOptions{
			LookbackRuns:        cfg.HarnessReviewLookbackRuns(),
			OutputDir:           cfg.HarnessReviewOutputDir(),
			Now:                 now,
			EscalationThreshold: 2,
		})
		return err
	default:
		return fmt.Errorf("unsupported built-in scheduled workflow %q", workflowName)
	}
}

func resolveScheduledAuditRepo(cfg *config.Config, vessel queue.Vessel) string {
	if cfg == nil {
		return ""
	}
	if vessel.Meta != nil {
		if name := strings.TrimSpace(vessel.Meta["config_source"]); name != "" {
			if srcCfg, ok := cfg.Sources[name]; ok {
				if repo := strings.TrimSpace(srcCfg.Repo); repo != "" {
					return repo
				}
			}
		}
		if repo := strings.TrimSpace(vessel.Meta["scheduled_repo"]); repo != "" {
			return repo
		}
	}
	return detectLessonsRepo(cfg)
}

func persistBuiltInAuditSummary(cfg *config.Config, vessel queue.Vessel) error {
	if cfg == nil {
		return nil
	}
	startedAt := time.Now().UTC()
	if vessel.StartedAt != nil {
		startedAt = vessel.StartedAt.UTC()
	}
	endedAt := startedAt
	if vessel.EndedAt != nil {
		endedAt = vessel.EndedAt.UTC()
	}
	summary := &runner.VesselSummary{
		VesselID:   vessel.ID,
		Source:     vessel.Source,
		Workflow:   vessel.Workflow,
		Ref:        vessel.Ref,
		State:      string(vessel.State),
		StartedAt:  startedAt,
		EndedAt:    endedAt,
		DurationMS: endedAt.Sub(startedAt).Milliseconds(),
		Note:       builtInScheduledWorkflowSummaryNote(vessel.Workflow),
	}
	if vessel.State == queue.StateFailed {
		summary.Note = fmt.Sprintf("%s Failure: %s", summary.Note, vessel.Error)
	}
	if err := runner.SaveVesselSummary(cfg.StateDir, summary); err != nil {
		return fmt.Errorf("save built-in audit summary for %s: %w", vessel.ID, err)
	}
	return nil
}

func builtInScheduledWorkflowSummaryNote(workflow string) string {
	switch workflow {
	case reviewpkg.HarnessGapAnalysisWorkflow:
		return "Built-in harness-gap analysis uses persisted daemon/GitHub/git telemetry and may open de-duplicated GitHub issues."
	case reviewpkg.WorkflowHealthReportWorkflow:
		return "Built-in workflow-health reporting summarizes recent vessel health and may open weekly de-duplicated GitHub issues."
	default:
		return "Built-in context-weight audit uses persisted summary artifacts and may open de-duplicated GitHub issues."
	}
}

func addDrainResults(dst *runner.DrainResult, src runner.DrainResult) {
	dst.Launched += src.Launched
	dst.Completed += src.Completed
	dst.Failed += src.Failed
	dst.Skipped += src.Skipped
	dst.Waiting += src.Waiting
}
