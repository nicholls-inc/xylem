package source

import (
	"context"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// StatusLabels holds the label names to apply at each vessel state transition.
// An empty string means "no label operation" for that transition.
type StatusLabels struct {
	Queued    string
	Running   string
	Completed string
	Failed    string
	TimedOut  string
}

// GitHubTaskFromConfig converts a config.Task to a GitHubTask.
// When t.StatusLabels is nil (block omitted from config), the returned task's
// StatusLabels is also nil, preserving the legacy "in-progress" fallback in
// OnStart. When t.StatusLabels is non-nil (block present, even if all values
// are empty), all status_label_* meta keys are populated so tasks can
// explicitly opt out of any label operation.
func GitHubTaskFromConfig(t config.Task) GitHubTask {
	task := GitHubTask{
		Labels:   t.Labels,
		Workflow: t.Workflow,
	}
	if t.StatusLabels != nil {
		task.StatusLabels = &StatusLabels{
			Queued:    t.StatusLabels.Queued,
			Running:   t.StatusLabels.Running,
			Completed: t.StatusLabels.Completed,
			Failed:    t.StatusLabels.Failed,
			TimedOut:  t.StatusLabels.TimedOut,
		}
	}
	return task
}

// Source discovers tasks and produces Vessels.
type Source interface {
	// Name returns the source identifier (e.g., "github-issue").
	Name() string
	// Scan discovers new tasks and returns vessels to enqueue.
	Scan(ctx context.Context) ([]queue.Vessel, error)
	// OnEnqueue is called after a vessel is successfully enqueued.
	// Used for side effects like adding a "queued" label.
	OnEnqueue(ctx context.Context, vessel queue.Vessel) error
	// OnStart is called when a vessel from this source begins running.
	// Used for side effects like adding an "in-progress" label.
	OnStart(ctx context.Context, vessel queue.Vessel) error
	// OnComplete is called when a vessel completes all phases successfully.
	OnComplete(ctx context.Context, vessel queue.Vessel) error
	// OnFail is called when a vessel fails (phase error or gate exhausted).
	OnFail(ctx context.Context, vessel queue.Vessel) error
	// OnTimedOut is called when a vessel's label gate times out.
	OnTimedOut(ctx context.Context, vessel queue.Vessel) error
	// BranchName generates the git branch name for this vessel.
	BranchName(vessel queue.Vessel) string
}

// CommandRunner abstracts subprocess execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}
