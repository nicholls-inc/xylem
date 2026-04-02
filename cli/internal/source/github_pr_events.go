package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// PREventsTask defines a task triggered by PR events.
type PREventsTask struct {
	Workflow        string
	Labels          []string
	ReviewSubmitted bool
	ChecksFailed    bool
	Commented       bool
}

// GitHubPREvents scans GitHub PRs for specific events and produces vessels.
type GitHubPREvents struct {
	Repo      string
	Tasks     map[string]PREventsTask
	Exclude   []string
	Queue     *queue.Queue
	CmdRunner CommandRunner
}

func (g *GitHubPREvents) Name() string { return "github-pr-events" }

func (g *GitHubPREvents) Scan(ctx context.Context) ([]queue.Vessel, error) {
	excludeSet := make(map[string]bool, len(g.Exclude))
	for _, ex := range g.Exclude {
		excludeSet[ex] = true
	}

	// List open PRs
	args := []string{
		"pr", "list",
		"--repo", g.Repo,
		"--state", "open",
		"--json", "number,title,url,labels,headRefName",
		"--limit", "50",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}

	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}

	var vessels []queue.Vessel

	for _, pr := range prs {
		if g.hasExcludedLabel(pr, excludeSet) {
			continue
		}

		for _, task := range g.Tasks {
			// Label triggers
			if len(task.Labels) > 0 {
				prLabels := make(map[string]bool, len(pr.Labels))
				for _, l := range pr.Labels {
					prLabels[l.Name] = true
				}
				for _, triggerLabel := range task.Labels {
					if prLabels[triggerLabel] {
						ref := fmt.Sprintf("%s#label-%s", pr.URL, triggerLabel)
						if !g.Queue.HasRefAny(ref) {
							vessels = append(vessels, queue.Vessel{
								ID:       fmt.Sprintf("pr-%d-label-%s", pr.Number, triggerLabel),
								Source:   "github-pr-events",
								Ref:      ref,
								Workflow: task.Workflow,
								Meta: map[string]string{
									"pr_num":         strconv.Itoa(pr.Number),
									"event_type":     "label",
									"pr_head_branch": pr.HeadRefName,
								},
								State:     queue.StatePending,
								CreatedAt: sourceNow(),
							})
						}
					}
				}
			}

			// Review submitted trigger
			if task.ReviewSubmitted {
				v, err := g.scanReviews(ctx, pr, task)
				if err != nil {
					return vessels, err
				}
				vessels = append(vessels, v...)
			}

			// Checks failed trigger
			if task.ChecksFailed {
				v, err := g.scanChecksFailed(ctx, pr, task)
				if err != nil {
					return vessels, err
				}
				vessels = append(vessels, v...)
			}

			// Comment trigger
			if task.Commented {
				v, err := g.scanComments(ctx, pr, task)
				if err != nil {
					return vessels, err
				}
				vessels = append(vessels, v...)
			}
		}
	}

	return vessels, nil
}

func (g *GitHubPREvents) scanReviews(ctx context.Context, pr ghPR, task PREventsTask) ([]queue.Vessel, error) {
	args := []string{
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", g.Repo, pr.Number),
		"--jq", ".[].id",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, nil // non-fatal: skip reviews on error
	}

	var vessels []queue.Vessel
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		reviewID := strings.TrimSpace(line)
		if reviewID == "" {
			continue
		}
		ref := fmt.Sprintf("%s#review-%s", pr.URL, reviewID)
		if !g.Queue.HasRefAny(ref) {
			vessels = append(vessels, queue.Vessel{
				ID:       fmt.Sprintf("pr-%d-review-%s", pr.Number, reviewID),
				Source:   "github-pr-events",
				Ref:      ref,
				Workflow: task.Workflow,
				Meta: map[string]string{
					"pr_num":         strconv.Itoa(pr.Number),
					"event_type":     "review_submitted",
					"pr_head_branch": pr.HeadRefName,
				},
				State:     queue.StatePending,
				CreatedAt: sourceNow(),
			})
		}
	}
	return vessels, nil
}

type ghCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func (g *GitHubPREvents) scanChecksFailed(ctx context.Context, pr ghPR, task PREventsTask) ([]queue.Vessel, error) {
	args := []string{
		"pr", "checks", strconv.Itoa(pr.Number),
		"--repo", g.Repo,
		"--json", "name,state",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, nil // non-fatal
	}

	var checks []ghCheck
	if err := json.Unmarshal(out, &checks); err != nil {
		return nil, nil // non-fatal
	}

	hasFailed := false
	for _, c := range checks {
		if c.State == "FAILURE" || c.State == "ERROR" {
			hasFailed = true
			break
		}
	}

	if !hasFailed {
		return nil, nil
	}

	// Use head SHA via gh pr view for dedup
	shaArgs := []string{
		"pr", "view", strconv.Itoa(pr.Number),
		"--repo", g.Repo,
		"--json", "headRefOid",
		"--jq", ".headRefOid",
	}
	shaOut, err := g.CmdRunner.Run(ctx, "gh", shaArgs...)
	if err != nil {
		return nil, nil // non-fatal
	}

	sha := strings.TrimSpace(string(shaOut))
	if sha == "" {
		return nil, nil
	}

	ref := fmt.Sprintf("%s#checks-failed-%s", pr.URL, sha)
	if g.Queue.HasRefAny(ref) {
		return nil, nil
	}

	return []queue.Vessel{
		{
			ID:       fmt.Sprintf("pr-%d-checks-failed-%s", pr.Number, sha[:minLen(len(sha), 8)]),
			Source:   "github-pr-events",
			Ref:      ref,
			Workflow: task.Workflow,
			Meta: map[string]string{
				"pr_num":         strconv.Itoa(pr.Number),
				"event_type":     "checks_failed",
				"pr_head_branch": pr.HeadRefName,
			},
			State:     queue.StatePending,
			CreatedAt: sourceNow(),
		},
	}, nil
}

func (g *GitHubPREvents) scanComments(ctx context.Context, pr ghPR, task PREventsTask) ([]queue.Vessel, error) {
	args := []string{
		"api",
		fmt.Sprintf("repos/%s/issues/%d/comments", g.Repo, pr.Number),
		"--jq", ".[].id",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, nil // non-fatal
	}

	var vessels []queue.Vessel
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		commentID := strings.TrimSpace(line)
		if commentID == "" {
			continue
		}
		ref := fmt.Sprintf("%s#comment-%s", pr.URL, commentID)
		if !g.Queue.HasRefAny(ref) {
			vessels = append(vessels, queue.Vessel{
				ID:       fmt.Sprintf("pr-%d-comment-%s", pr.Number, commentID),
				Source:   "github-pr-events",
				Ref:      ref,
				Workflow: task.Workflow,
				Meta: map[string]string{
					"pr_num":         strconv.Itoa(pr.Number),
					"event_type":     "commented",
					"pr_head_branch": pr.HeadRefName,
				},
				State:     queue.StatePending,
				CreatedAt: sourceNow(),
			})
		}
	}
	return vessels, nil
}

func (g *GitHubPREvents) OnEnqueue(_ context.Context, _ queue.Vessel) error  { return nil }
func (g *GitHubPREvents) OnStart(_ context.Context, _ queue.Vessel) error    { return nil }
func (g *GitHubPREvents) OnComplete(_ context.Context, _ queue.Vessel) error { return nil }
func (g *GitHubPREvents) OnFail(_ context.Context, _ queue.Vessel) error     { return nil }
func (g *GitHubPREvents) OnTimedOut(_ context.Context, _ queue.Vessel) error { return nil }

func (g *GitHubPREvents) BranchName(vessel queue.Vessel) string {
	prNum := vessel.Meta["pr_num"]
	eventType := vessel.Meta["event_type"]
	slug := slugify(vessel.Ref)
	return fmt.Sprintf("event/pr-%s-%s-%s", prNum, eventType, slug)
}

func (g *GitHubPREvents) hasExcludedLabel(pr ghPR, excluded map[string]bool) bool {
	for _, l := range pr.Labels {
		if excluded[l.Name] {
			return true
		}
	}
	return false
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}
