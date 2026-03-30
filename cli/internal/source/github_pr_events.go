package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// PREventsTask defines triggers for the PR events source.
type PREventsTask struct {
	Workflow        string
	Labels          []string
	ReviewSubmitted bool
	ChecksFailed    bool
	Commented       bool
}

// GitHubPREvents scans GitHub pull requests for events and produces vessels.
type GitHubPREvents struct {
	Repo      string
	Tasks     map[string]PREventsTask
	Exclude   []string
	Queue     *queue.Queue
	CmdRunner CommandRunner
}

func (g *GitHubPREvents) Name() string { return "github-pr-events" }

func (g *GitHubPREvents) Scan(ctx context.Context) ([]queue.Vessel, error) {
	// Get open PRs (recently updated, limit 20)
	out, err := g.CmdRunner.Run(ctx, "gh", "pr", "list",
		"--repo", g.Repo,
		"--state", "open",
		"--json", "number,title,url,labels,headRefName",
		"--limit", "20",
	)
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}

	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}

	excludeSet := make(map[string]bool, len(g.Exclude))
	for _, ex := range g.Exclude {
		excludeSet[ex] = true
	}

	var vessels []queue.Vessel

	for _, pr := range prs {
		if g.hasExcludedLabel(pr, excludeSet) {
			continue
		}

		for _, task := range g.Tasks {
			// Check label triggers
			if len(task.Labels) > 0 {
				for _, triggerLabel := range task.Labels {
					if prHasLabel(pr, triggerLabel) {
						ref := fmt.Sprintf("%s#label-%s", pr.URL, triggerLabel)
						if !g.Queue.HasRefAny(ref) {
							vessels = append(vessels, g.makeVessel(pr, task.Workflow, ref, "label"))
						}
					}
				}
			}

			// Check review_submitted trigger
			if task.ReviewSubmitted {
				reviewVessels, err := g.scanReviews(ctx, pr, task.Workflow)
				if err != nil {
					// Log and continue — don't block other PRs
					continue
				}
				vessels = append(vessels, reviewVessels...)
			}

			// Check checks_failed trigger
			if task.ChecksFailed {
				checksVessels, err := g.scanCheckFailures(ctx, pr, task.Workflow)
				if err != nil {
					continue
				}
				vessels = append(vessels, checksVessels...)
			}

			// Check commented trigger
			if task.Commented {
				commentVessels, err := g.scanComments(ctx, pr, task.Workflow)
				if err != nil {
					continue
				}
				vessels = append(vessels, commentVessels...)
			}
		}
	}

	return vessels, nil
}

func (g *GitHubPREvents) scanReviews(ctx context.Context, pr ghPR, workflow string) ([]queue.Vessel, error) {
	out, err := g.CmdRunner.Run(ctx, "gh", "api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", g.Repo, pr.Number),
		"--jq", ".[].id",
	)
	if err != nil {
		return nil, err
	}

	var vessels []queue.Vessel
	for _, idStr := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		idStr = strings.TrimSpace(idStr)
		if idStr == "" {
			continue
		}
		ref := fmt.Sprintf("%s#review-%s", pr.URL, idStr)
		if !g.Queue.HasRefAny(ref) {
			vessels = append(vessels, g.makeVessel(pr, workflow, ref, "review"))
		}
	}
	return vessels, nil
}

func (g *GitHubPREvents) scanCheckFailures(ctx context.Context, pr ghPR, workflow string) ([]queue.Vessel, error) {
	out, err := g.CmdRunner.Run(ctx, "gh", "pr", "checks",
		strconv.Itoa(pr.Number),
		"--repo", g.Repo,
		"--json", "name,state",
	)
	if err != nil {
		return nil, err
	}

	var checks []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &checks); err != nil {
		return nil, err
	}

	// Only trigger if there are failed checks
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

	// Use PR URL + head SHA as ref to deduplicate per commit
	// Get head SHA
	headOut, err := g.CmdRunner.Run(ctx, "gh", "pr", "view",
		strconv.Itoa(pr.Number),
		"--repo", g.Repo,
		"--json", "headRefOid",
	)
	if err != nil {
		return nil, err
	}
	var headInfo struct {
		HeadRefOid string `json:"headRefOid"`
	}
	if err := json.Unmarshal(headOut, &headInfo); err != nil {
		return nil, err
	}

	ref := fmt.Sprintf("%s#checks-failed-%s", pr.URL, headInfo.HeadRefOid)
	if g.Queue.HasRefAny(ref) {
		return nil, nil
	}

	return []queue.Vessel{g.makeVessel(pr, workflow, ref, "checks_failed")}, nil
}

func (g *GitHubPREvents) scanComments(ctx context.Context, pr ghPR, workflow string) ([]queue.Vessel, error) {
	out, err := g.CmdRunner.Run(ctx, "gh", "api",
		fmt.Sprintf("repos/%s/issues/%d/comments", g.Repo, pr.Number),
		"--jq", ".[].id",
	)
	if err != nil {
		return nil, err
	}

	var vessels []queue.Vessel
	for _, idStr := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		idStr = strings.TrimSpace(idStr)
		if idStr == "" {
			continue
		}
		ref := fmt.Sprintf("%s#comment-%s", pr.URL, idStr)
		if !g.Queue.HasRefAny(ref) {
			vessels = append(vessels, g.makeVessel(pr, workflow, ref, "comment"))
		}
	}
	return vessels, nil
}

func (g *GitHubPREvents) makeVessel(pr ghPR, workflow, ref, eventType string) queue.Vessel {
	return queue.Vessel{
		ID:       fmt.Sprintf("pr-%d-%s-%s", pr.Number, eventType, shortHash(ref)),
		Source:   "github-pr-events",
		Ref:      ref,
		Workflow: workflow,
		Meta: map[string]string{
			"pr_num":         strconv.Itoa(pr.Number),
			"event_type":     eventType,
			"pr_head_branch": pr.HeadRefName,
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	}
}

func (g *GitHubPREvents) OnStart(ctx context.Context, vessel queue.Vessel) error {
	// No default label changes for PR event vessels
	return nil
}

func (g *GitHubPREvents) BranchName(vessel queue.Vessel) string {
	prNum := vessel.Meta["pr_num"]
	eventType := vessel.Meta["event_type"]
	slug := slugify(vessel.Ref)
	return fmt.Sprintf("pr-event/%s-%s-%s", prNum, eventType, slug)
}

func (g *GitHubPREvents) hasExcludedLabel(pr ghPR, excluded map[string]bool) bool {
	for _, l := range pr.Labels {
		if excluded[l.Name] {
			return true
		}
	}
	return false
}

func prHasLabel(pr ghPR, label string) bool {
	for _, l := range pr.Labels {
		if l.Name == label {
			return true
		}
	}
	return false
}

// shortHash returns first 8 chars of a simple hash of s for vessel ID uniqueness.
func shortHash(s string) string {
	// Simple deterministic hash using FNV
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}
