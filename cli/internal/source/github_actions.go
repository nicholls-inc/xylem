package source

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// ActionsFilter defines which workflow runs match a task.
type ActionsFilter struct {
	// WorkflowName filters by workflow filename or display name. Empty = all.
	WorkflowName string
	// Branches filters by head branch name. Empty = all branches.
	Branches []string
	// Conclusions filters by run conclusion. Defaults to ["failure"] when empty.
	Conclusions []string
}

// ActionsTask defines a task triggered by a GitHub Actions workflow run failure.
type ActionsTask struct {
	Workflow string
	Tier     string
	Filter   ActionsFilter
}

// GitHubActions scans for failed GitHub Actions workflow runs (non-PR-triggered)
// and produces vessels for analysis.
type GitHubActions struct {
	Repo        string
	Tasks       map[string]ActionsTask
	StateDir    string
	ConfigName  string
	DefaultTier string
	Queue       *queue.Queue
	CmdRunner   CommandRunner
}

// ghWorkflowRun is the shape returned by gh run list --json.
type ghWorkflowRun struct {
	DatabaseID   int64  `json:"databaseId"`
	Name         string `json:"name"`
	HeadBranch   string `json:"headBranch"`
	WorkflowName string `json:"workflowName"`
	Conclusion   string `json:"conclusion"`
	URL          string `json:"url"`
	CreatedAt    string `json:"createdAt"`
	Event        string `json:"event"`
}

// actionsSeenState tracks which run IDs have been enqueued to prevent
// re-enqueuing the same failed run across scan cycles.
type actionsSeenState struct {
	SeenRunIDs map[string]bool `json:"seen_run_ids"`
}

func (g *GitHubActions) Name() string { return "github-actions" }

func (g *GitHubActions) Scan(ctx context.Context) ([]queue.Vessel, error) {
	runs, err := g.fetchRuns(ctx)
	if err != nil {
		return nil, err
	}

	seen, err := g.loadSeenState()
	if err != nil {
		return nil, err
	}

	var vessels []queue.Vessel
	for _, run := range runs {
		// Skip PR-triggered runs — this source is for scheduled/manual/push events only.
		if run.Event == "pull_request" || run.Event == "pull_request_target" {
			continue
		}

		runID := fmt.Sprintf("%d", run.DatabaseID)
		for taskName, task := range g.Tasks {
			if !g.runMatchesTask(run, task) {
				continue
			}

			vesselID := fmt.Sprintf("ci-%s-%s", sanitizeActionsComponent(taskName), runID)
			// Scope ref to task so different tasks can independently process the same run.
			ref := fmt.Sprintf("%s#%s", run.URL, taskName)

			if seen.SeenRunIDs[vesselID] {
				continue
			}
			if g.Queue != nil && g.Queue.HasRefAny(ref) {
				continue
			}

			vessels = append(vessels, queue.Vessel{
				ID:       vesselID,
				Source:   g.Name(),
				Ref:      ref,
				Workflow: task.Workflow,
				Tier:     ResolveTaskTier(task.Tier, g.DefaultTier),
				Meta: map[string]string{
					"run_id":        runID,
					"run_name":      run.Name,
					"workflow_name": run.WorkflowName,
					"head_branch":   run.HeadBranch,
					"conclusion":    run.Conclusion,
					"run_url":       run.URL,
					"run_event":     run.Event,
					"task_name":     taskName,
				},
				State:     queue.StatePending,
				CreatedAt: sourceNow(),
			})
		}
	}

	return vessels, nil
}

// fetchRuns calls gh run list once per unique conclusion needed across all tasks
// and merges the results, deduplicating by DatabaseID. This ensures that tasks
// configured with non-failure conclusions (e.g., "timed_out") receive the runs
// they expect, since the GitHub API --status flag only returns runs for that
// specific conclusion value.
func (g *GitHubActions) fetchRuns(ctx context.Context) ([]ghWorkflowRun, error) {
	// Collect unique conclusions needed across all tasks.
	conclusionSet := make(map[string]struct{})
	for _, task := range g.Tasks {
		conclusions := task.Filter.Conclusions
		if len(conclusions) == 0 {
			conclusions = []string{"failure"}
		}
		for _, c := range conclusions {
			conclusionSet[strings.ToLower(c)] = struct{}{}
		}
	}

	var allRuns []ghWorkflowRun
	seen := make(map[int64]bool)
	for conclusion := range conclusionSet {
		batch, err := g.fetchRunsByStatus(ctx, conclusion)
		if err != nil {
			return nil, err
		}
		for _, run := range batch {
			if !seen[run.DatabaseID] {
				seen[run.DatabaseID] = true
				allRuns = append(allRuns, run)
			}
		}
	}
	return allRuns, nil
}

func (g *GitHubActions) fetchRunsByStatus(ctx context.Context, status string) ([]ghWorkflowRun, error) {
	args := []string{
		"run", "list",
		"--repo", g.Repo,
		"--status", status,
		"--json", "databaseId,name,headBranch,workflowName,conclusion,url,createdAt,event",
		"--limit", "20",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh run list --status %s: %w", status, err)
	}
	var runs []ghWorkflowRun
	if err := json.Unmarshal(out, &runs); err != nil {
		return nil, fmt.Errorf("parse gh run list output: %w", err)
	}
	return runs, nil
}

func (g *GitHubActions) runMatchesTask(run ghWorkflowRun, task ActionsTask) bool {
	filter := task.Filter

	// Workflow name filter (empty = all).
	if filter.WorkflowName != "" &&
		!strings.EqualFold(run.WorkflowName, filter.WorkflowName) &&
		!strings.EqualFold(run.Name, filter.WorkflowName) {
		return false
	}

	// Branch filter (empty = all).
	if len(filter.Branches) > 0 {
		matched := false
		for _, b := range filter.Branches {
			if run.HeadBranch == b {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Conclusion filter (default: ["failure"]).
	conclusions := filter.Conclusions
	if len(conclusions) == 0 {
		conclusions = []string{"failure"}
	}
	conclusionMatched := false
	for _, c := range conclusions {
		if strings.EqualFold(run.Conclusion, c) {
			conclusionMatched = true
			break
		}
	}
	return conclusionMatched
}

func (g *GitHubActions) OnEnqueue(_ context.Context, vessel queue.Vessel) error {
	taskName := vessel.Meta["task_name"]
	runID := vessel.Meta["run_id"]
	if taskName == "" || runID == "" {
		return nil
	}
	vesselID := vessel.ID
	seen, err := g.loadSeenState()
	if err != nil {
		return err
	}
	if seen.SeenRunIDs == nil {
		seen.SeenRunIDs = make(map[string]bool)
	}
	seen.SeenRunIDs[vesselID] = true
	return g.saveSeenState(seen)
}

func (g *GitHubActions) OnStart(_ context.Context, _ queue.Vessel) error            { return nil }
func (g *GitHubActions) OnWait(_ context.Context, _ queue.Vessel) error             { return nil }
func (g *GitHubActions) OnResume(_ context.Context, _ queue.Vessel) error           { return nil }
func (g *GitHubActions) OnComplete(_ context.Context, _ queue.Vessel) error         { return nil }
func (g *GitHubActions) OnFail(_ context.Context, _ queue.Vessel) error             { return nil }
func (g *GitHubActions) OnTimedOut(_ context.Context, _ queue.Vessel) error         { return nil }
func (g *GitHubActions) RemoveRunningLabel(_ context.Context, _ queue.Vessel) error { return nil }

func (g *GitHubActions) BranchName(vessel queue.Vessel) string {
	workflowSlug := slugify(vessel.Meta["workflow_name"])
	if workflowSlug == "" {
		workflowSlug = "ci"
	}
	runID := vessel.Meta["run_id"]
	return fmt.Sprintf("ci/%s-%s", workflowSlug, runID)
}

func (g *GitHubActions) loadSeenState() (*actionsSeenState, error) {
	path := g.statePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &actionsSeenState{SeenRunIDs: make(map[string]bool)}, nil
		}
		return nil, fmt.Errorf("github-actions source %q: read seen state: %w", g.ConfigName, err)
	}
	var state actionsSeenState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("warn: github-actions source %q: unmarshal seen state (resetting): %v", g.ConfigName, err)
		return &actionsSeenState{SeenRunIDs: make(map[string]bool)}, nil
	}
	if state.SeenRunIDs == nil {
		state.SeenRunIDs = make(map[string]bool)
	}
	return &state, nil
}

func (g *GitHubActions) saveSeenState(state *actionsSeenState) error {
	path := g.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("github-actions source %q: create state dir: %w", g.ConfigName, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("github-actions source %q: marshal seen state: %w", g.ConfigName, err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("github-actions source %q: write temp seen state: %w", g.ConfigName, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("github-actions source %q: rename temp seen state: %w", g.ConfigName, err)
	}
	return nil
}

func (g *GitHubActions) statePath() string {
	scope := sanitizeActionsComponent(g.ConfigName)
	if scope == "" {
		scope = sanitizeActionsComponent(g.Repo)
	}
	return filepath.Join(g.StateDir, "github-actions-seen-"+scope+".json")
}

var nonAlphaNumActions = nonAlphaNum

func sanitizeActionsComponent(s string) string {
	clean := strings.ToLower(strings.TrimSpace(s))
	clean = strings.ReplaceAll(clean, "/", "-")
	clean = nonAlphaNumActions.ReplaceAllString(clean, "-")
	clean = strings.Trim(clean, "-")
	return clean
}
