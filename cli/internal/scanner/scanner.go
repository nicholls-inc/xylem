package scanner

import (
	"context"
	"os"
	"path/filepath"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
)

// CommandRunner abstracts shell calls for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Scanner scans configured sources for actionable tasks and enqueues vessels.
type Scanner struct {
	Config    *config.Config
	Queue     *queue.Queue
	CmdRunner CommandRunner
}

// ScanResult summarises a scan run.
type ScanResult struct {
	Added   int
	Skipped int
	Paused  bool
}

// New creates a Scanner.
func New(cfg *config.Config, q *queue.Queue, runner CommandRunner) *Scanner {
	return &Scanner{Config: cfg, Queue: q, CmdRunner: runner}
}

// Scan queries configured sources, filters candidates, and enqueues new vessels.
func (s *Scanner) Scan(ctx context.Context) (ScanResult, error) {
	pauseMarker := filepath.Join(s.Config.StateDir, "paused")
	if _, err := os.Stat(pauseMarker); err == nil {
		return ScanResult{Paused: true}, nil
	}

	var result ScanResult
	sources := s.buildSources()

	for _, src := range sources {
		vessels, err := src.Scan(ctx)
		if err != nil {
			return result, err
		}
		for _, vessel := range vessels {
			enqueued, err := s.Queue.Enqueue(vessel)
			if err != nil {
				return result, err
			}
			if enqueued {
				result.Added++
			} else {
				result.Skipped++
			}
		}
	}
	return result, nil
}

// buildSources constructs Source implementations from the config.
func (s *Scanner) buildSources() []source.Source {
	var sources []source.Source
	for _, srcCfg := range s.Config.Sources {
		tasks := convertTasks(srcCfg.Tasks)
		switch srcCfg.Type {
		case "github":
			sources = append(sources, &source.GitHub{
				Repo:      srcCfg.Repo,
				Tasks:     tasks,
				Exclude:   srcCfg.Exclude,
				Queue:     s.Queue,
				CmdRunner: s.CmdRunner,
			})
		case "github-pr":
			sources = append(sources, &source.GitHubPR{
				Repo:      srcCfg.Repo,
				Tasks:     tasks,
				Exclude:   srcCfg.Exclude,
				Queue:     s.Queue,
				CmdRunner: s.CmdRunner,
			})
		case "github-pr-events":
			prEventsTasks := convertPREventsTasks(srcCfg.Tasks)
			sources = append(sources, &source.GitHubPREvents{
				Repo:      srcCfg.Repo,
				Tasks:     prEventsTasks,
				Exclude:   srcCfg.Exclude,
				Queue:     s.Queue,
				CmdRunner: s.CmdRunner,
			})
		case "github-merge":
			mergeTasks := convertMergeTasks(srcCfg.Tasks)
			sources = append(sources, &source.GitHubMerge{
				Repo:      srcCfg.Repo,
				Tasks:     mergeTasks,
				Queue:     s.Queue,
				CmdRunner: s.CmdRunner,
			})
		}
	}
	return sources
}

func convertTasks(cfgTasks map[string]config.Task) map[string]source.GitHubTask {
	tasks := make(map[string]source.GitHubTask, len(cfgTasks))
	for name, t := range cfgTasks {
		tasks[name] = source.GitHubTask{
			Labels:   t.Labels,
			Workflow: t.Workflow,
		}
	}
	return tasks
}

func convertPREventsTasks(cfgTasks map[string]config.Task) map[string]source.PREventsTask {
	tasks := make(map[string]source.PREventsTask, len(cfgTasks))
	for name, t := range cfgTasks {
		pet := source.PREventsTask{
			Workflow: t.Workflow,
		}
		if t.On != nil {
			pet.Labels = t.On.Labels
			pet.ReviewSubmitted = t.On.ReviewSubmitted
			pet.ChecksFailed = t.On.ChecksFailed
			pet.Commented = t.On.Commented
		}
		tasks[name] = pet
	}
	return tasks
}

func convertMergeTasks(cfgTasks map[string]config.Task) map[string]source.MergeTask {
	tasks := make(map[string]source.MergeTask, len(cfgTasks))
	for name, t := range cfgTasks {
		tasks[name] = source.MergeTask{
			Workflow: t.Workflow,
		}
	}
	return tasks
}
