package scanner

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/nicholls-inc/xylem/cli/internal/source"
)

// CommandRunner abstracts shell calls for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Scanner scans configured sources for actionable tasks and enqueues vessels.
type Scanner struct {
	Config              *config.Config
	Queue               *queue.Queue
	CmdRunner           CommandRunner
	RunHooks            bool
	BudgetGate          budgetGate
	OnControlPlaneMerge func(source.ControlPlaneMergeEvent)
}

type budgetGate interface {
	Check(class string) cost.Decision
}

// ScanResult summarises a scan run.
type ScanResult struct {
	Added   int
	Skipped int
	Paused  bool
}

// New creates a Scanner.
func New(cfg *config.Config, q *queue.Queue, runner CommandRunner) *Scanner {
	return &Scanner{
		Config:     cfg,
		Queue:      q,
		CmdRunner:  runner,
		RunHooks:   true,
		BudgetGate: cost.NewBudgetGate(gateConfigFrom(cfg), cfg.StateDir),
	}
}

// gateConfigFrom converts the cost section of a Config into a cost.GateConfig.
func gateConfigFrom(cfg *config.Config) cost.GateConfig {
	return cost.GateConfig{
		DailyBudgetUSD: cfg.Cost.DailyBudgetUSD,
		PerClassLimit:  cfg.Cost.PerClassLimit,
		OnExceeded:     cfg.CostOnExceeded(),
	}
}

// Scan queries configured sources, filters candidates, and enqueues new vessels.
func (s *Scanner) Scan(ctx context.Context) (ScanResult, error) {
	pauseMarker := config.RuntimePath(s.Config.StateDir, "paused")
	if _, err := os.Stat(pauseMarker); err == nil {
		return ScanResult{Paused: true}, nil
	}

	var result ScanResult
	var scanErrs []error
	entries := s.buildSources()

	for _, entry := range entries {
		vessels, err := entry.src.Scan(ctx)
		if err != nil {
			// S4: one source's failure must not block intake from others.
			// Collect the error and continue iterating over remaining sources.
			log.Printf("warn: scanner: source scan failed: %v", err)
			scanErrs = append(scanErrs, err)
			continue
		}
		for _, vessel := range vessels {
			// Propagate source config name so the runner can look up
			// source-level LLM/Model overrides at execution time.
			if entry.configName != "" {
				if vessel.Meta == nil {
					vessel.Meta = make(map[string]string)
				}
				vessel.Meta["config_source"] = entry.configName
			}

			class := vessel.ConcurrencyClass()
			decision := s.budgetGate().Check(class)
			if !decision.Allowed {
				log.Printf("audit: budget.skipped class=%s vessel_source=%s reason=%s remaining_usd=%.4f", class, vessel.Source, decision.Reason, decision.RemainingUSD)
				result.Skipped++
				continue
			}

			enqueued, err := s.Queue.Enqueue(vessel)
			if errors.Is(err, queue.ErrDuplicateID) {
				// Changed-fingerprint retry path currently reuses the original
				// ID, which collides with the failed record (I9). Skip rather
				// than crash the scan; the correct fix (route through Update
				// or use RetryID for a new-ID retry) is scanner-side.
				log.Printf("warn: scanner: duplicate vessel ID %q, skipping", vessel.ID)
				result.Skipped++
				continue
			}
			if err != nil {
				// S5: a non-dedup Enqueue error (e.g. disk I/O, malformed
				// queue file) must not block later vessels from this source
				// or any subsequent source. Log + accumulate + continue,
				// matching the ErrDuplicateID-class tolerance above.
				log.Printf("warn: scanner: enqueue failed for vessel %q: %v", vessel.ID, err)
				scanErrs = append(scanErrs, err)
				result.Skipped++
				continue
			}
			if enqueued {
				if vessel.RetryOf != "" {
					if err := recovery.UpdateRetryOutcome(s.Config.StateDir, vessel.RetryOf, "enqueued"); err != nil {
						// S5 (extension): UpdateRetryOutcome is a
						// recovery-side bookkeeping call; a failure here
						// must not abort the tick mid-source. Log +
						// accumulate and fall through to count the vessel
						// as added (it IS in the queue).
						log.Printf("warn: scanner: UpdateRetryOutcome failed for retry_of=%q: %v", vessel.RetryOf, err)
						scanErrs = append(scanErrs, err)
					}
				}
				result.Added++
				if s.RunHooks {
					if err := entry.src.OnEnqueue(ctx, vessel); err != nil {
						log.Printf("warn: OnEnqueue hook for vessel %s failed: %v", vessel.ID, err)
					}
				}
			} else {
				result.Skipped++
			}
		}
	}
	return result, errors.Join(scanErrs...)
}

func (s *Scanner) budgetGate() budgetGate {
	if s.BudgetGate != nil {
		return s.BudgetGate
	}
	return cost.NewBudgetGate(gateConfigFrom(s.Config), s.Config.StateDir)
}

// BacklogCount reports how many items currently match backlog-aware sources.
func (s *Scanner) BacklogCount(ctx context.Context) (int, error) {
	total := 0
	for _, entry := range s.buildSources() {
		backlogSource, ok := entry.src.(source.BacklogSource)
		if !ok {
			continue
		}
		count, err := backlogSource.BacklogCount(ctx)
		if err != nil {
			return total, err
		}
		total += count
	}
	return total, nil
}

type sourceEntry struct {
	src        source.Source
	configName string
}

// buildSources constructs Source implementations from the config.
func (s *Scanner) buildSources() []sourceEntry {
	var entries []sourceEntry
	for name, srcCfg := range s.Config.Sources {
		switch srcCfg.Type {
		case "github":
			tasks := convertTasks(srcCfg.Tasks)
			entries = append(entries, sourceEntry{
				src: &source.GitHub{
					Repo:        srcCfg.Repo,
					Tasks:       tasks,
					Exclude:     srcCfg.Exclude,
					StateDir:    s.Config.StateDir,
					DefaultTier: s.Config.LLMRouting.DefaultTier,
					Queue:       s.Queue,
					CmdRunner:   s.CmdRunner,
				},
				configName: name,
			})
		case "github-pr":
			tasks := convertTasks(srcCfg.Tasks)
			entries = append(entries, sourceEntry{
				src: &source.GitHubPR{
					Repo:        srcCfg.Repo,
					Tasks:       tasks,
					Exclude:     srcCfg.Exclude,
					StateDir:    s.Config.StateDir,
					DefaultTier: s.Config.LLMRouting.DefaultTier,
					Queue:       s.Queue,
					CmdRunner:   s.CmdRunner,
				},
				configName: name,
			})
		case "github-pr-events":
			prEventsTasks := convertPREventsTasks(srcCfg.Tasks)
			entries = append(entries, sourceEntry{
				src: &source.GitHubPREvents{
					Repo:        srcCfg.Repo,
					Tasks:       prEventsTasks,
					Exclude:     srcCfg.Exclude,
					StateDir:    s.Config.StateDir,
					DefaultTier: s.Config.LLMRouting.DefaultTier,
					Queue:       s.Queue,
					CmdRunner:   s.CmdRunner,
				},
				configName: name,
			})
		case "github-merge":
			mergeTasks := convertMergeTasks(srcCfg.Tasks)
			entries = append(entries, sourceEntry{
				src: &source.GitHubMerge{
					Repo:                srcCfg.Repo,
					Tasks:               mergeTasks,
					DefaultTier:         s.Config.LLMRouting.DefaultTier,
					Queue:               s.Queue,
					CmdRunner:           s.CmdRunner,
					OnControlPlaneMerge: s.OnControlPlaneMerge,
				},
				configName: name,
			})
		case "scheduled":
			scheduledTasks := convertScheduledTasks(srcCfg.Tasks)
			entries = append(entries, sourceEntry{
				src: &source.Scheduled{
					Repo:        srcCfg.Repo,
					StateDir:    s.Config.StateDir,
					ConfigName:  name,
					Schedule:    srcCfg.Schedule,
					Tasks:       scheduledTasks,
					DefaultTier: s.Config.LLMRouting.DefaultTier,
					Queue:       s.Queue,
				},
				configName: name,
			})
		case "schedule":
			entries = append(entries, sourceEntry{
				src: &source.Schedule{
					ConfigName:   name,
					Cadence:      srcCfg.Cadence,
					Workflow:     srcCfg.Workflow,
					StateDir:     s.Config.StateDir,
					Queue:        s.Queue,
					SkipFirstRun: srcCfg.SkipFirstRun,
				},
				configName: name,
			})
		case "github-actions":
			actionsTasks := convertActionsTasks(srcCfg.Tasks)
			entries = append(entries, sourceEntry{
				src: &source.GitHubActions{
					Repo:        srcCfg.Repo,
					Tasks:       actionsTasks,
					StateDir:    s.Config.StateDir,
					ConfigName:  name,
					DefaultTier: s.Config.LLMRouting.DefaultTier,
					Queue:       s.Queue,
					CmdRunner:   s.CmdRunner,
				},
				configName: name,
			})
		}
	}
	return entries
}

func convertTasks(cfgTasks map[string]config.Task) map[string]source.GitHubTask {
	tasks := make(map[string]source.GitHubTask, len(cfgTasks))
	for name, t := range cfgTasks {
		tasks[name] = source.GitHubTaskFromConfig(t)
	}
	return tasks
}

func convertPREventsTasks(cfgTasks map[string]config.Task) map[string]source.PREventsTask {
	tasks := make(map[string]source.PREventsTask, len(cfgTasks))
	for name, t := range cfgTasks {
		task := source.PREventsTask{
			Workflow: t.Workflow,
			Tier:     t.Tier,
		}
		if t.On != nil {
			task.Labels = t.On.Labels
			task.ReviewSubmitted = t.On.ReviewSubmitted
			task.ChecksFailed = t.On.ChecksFailed
			task.Commented = t.On.Commented
			task.PROpened = t.On.PROpened
			task.PRHeadUpdated = t.On.PRHeadUpdated
			task.AuthorAllow = t.On.AuthorAllow
			task.AuthorDeny = t.On.AuthorDeny
			task.Debounce = source.UnsetPREventsDebounce
			if t.On.Debounce != "" {
				task.Debounce, _ = time.ParseDuration(t.On.Debounce)
			}
		}
		tasks[name] = task
	}
	return tasks
}

func convertMergeTasks(cfgTasks map[string]config.Task) map[string]source.MergeTask {
	tasks := make(map[string]source.MergeTask, len(cfgTasks))
	for name, t := range cfgTasks {
		tasks[name] = source.MergeTask{
			Workflow: t.Workflow,
			Tier:     t.Tier,
		}
	}
	return tasks
}

func convertScheduledTasks(cfgTasks map[string]config.Task) map[string]source.ScheduledTask {
	tasks := make(map[string]source.ScheduledTask, len(cfgTasks))
	for name, t := range cfgTasks {
		tasks[name] = source.ScheduledTask{
			Workflow: t.Workflow,
			Ref:      t.Ref,
			Tier:     t.Tier,
		}
	}
	return tasks
}

func convertActionsTasks(cfgTasks map[string]config.Task) map[string]source.ActionsTask {
	tasks := make(map[string]source.ActionsTask, len(cfgTasks))
	for name, t := range cfgTasks {
		filter := source.ActionsFilter{}
		if t.Actions != nil {
			filter.WorkflowName = t.Actions.Workflow
			filter.Branches = t.Actions.Branches
			filter.Conclusions = t.Actions.Conclusions
		}
		tasks[name] = source.ActionsTask{
			Workflow: t.Workflow,
			Tier:     t.Tier,
			Filter:   filter,
		}
	}
	return tasks
}
