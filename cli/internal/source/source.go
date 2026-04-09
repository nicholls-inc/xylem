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

// LabelGateLabels holds the label names to apply when a vessel enters and exits
// a label-gate wait. Empty strings disable the corresponding operation.
type LabelGateLabels struct {
	Waiting string
	Ready   string
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
	if t.LabelGateLabels != nil {
		task.LabelGateLabels = &LabelGateLabels{
			Waiting: t.LabelGateLabels.Waiting,
			Ready:   t.LabelGateLabels.Ready,
		}
	}
	return task
}

// ResolveRunningLabel returns the running status label for a vessel,
// applying the backward-compat fallback ("in-progress") when the
// status_label_running meta key is absent.
func ResolveRunningLabel(vessel queue.Vessel) string {
	running, has := vessel.Meta["status_label_running"]
	if !has {
		return "in-progress"
	}
	return running
}

func resolveWaitingLabel(vessel queue.Vessel) string {
	return vessel.Meta["label_gate_label_waiting"]
}

func resolveReadyLabel(vessel queue.Vessel) string {
	return vessel.Meta["label_gate_label_ready"]
}

func normalizeLabelOps(add []string, remove []string) ([]string, []string) {
	added := normalizeLabels(add...)
	removed := normalizeLabels(remove...)
	if len(added) == 0 || len(removed) == 0 {
		return added, removed
	}

	addSet := make(map[string]struct{}, len(added))
	for _, label := range added {
		addSet[label] = struct{}{}
	}

	filtered := removed[:0]
	for _, label := range removed {
		if _, ok := addSet[label]; ok {
			continue
		}
		filtered = append(filtered, label)
	}
	return added, filtered
}

func normalizeLabels(labels ...string) []string {
	seen := make(map[string]struct{}, len(labels))
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	return out
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
	// OnWait is called when a vessel enters waiting due to a label gate.
	OnWait(ctx context.Context, vessel queue.Vessel) error
	// OnResume is called when a waiting vessel is unblocked and returned to pending.
	OnResume(ctx context.Context, vessel queue.Vessel) error
	// OnComplete is called when a vessel completes all phases successfully.
	OnComplete(ctx context.Context, vessel queue.Vessel) error
	// OnFail is called when a vessel fails (phase error or gate exhausted).
	OnFail(ctx context.Context, vessel queue.Vessel) error
	// OnTimedOut is called when a vessel's label gate times out.
	OnTimedOut(ctx context.Context, vessel queue.Vessel) error
	// RemoveRunningLabel removes the running status label unconditionally.
	// Called via defer to ensure the label is always removed when a vessel
	// exits the running state, regardless of the exit reason.
	RemoveRunningLabel(ctx context.Context, vessel queue.Vessel) error
	// BranchName generates the git branch name for this vessel.
	BranchName(vessel queue.Vessel) string
}

// BacklogSource reports how many items currently match this source's scan
// criteria without enqueueing them.
type BacklogSource interface {
	BacklogCount(ctx context.Context) (int, error)
}

// CommandRunner abstracts subprocess execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}
