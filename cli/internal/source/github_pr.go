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

// GitHubPR scans GitHub pull requests and produces vessels.
type GitHubPR struct {
	Repo      string
	Tasks     map[string]GitHubTask
	Exclude   []string
	Queue     *queue.Queue
	CmdRunner CommandRunner
}

type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	HeadRefName string `json:"headRefName"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (g *GitHubPR) Name() string { return "github-pr" }

func (g *GitHubPR) Scan(ctx context.Context) ([]queue.Vessel, error) {
	excludeSet := make(map[string]bool, len(g.Exclude))
	for _, ex := range g.Exclude {
		excludeSet[ex] = true
	}

	var vessels []queue.Vessel
	seen := make(map[int]bool)

	for _, task := range g.Tasks {
		for _, label := range task.Labels {
			args := []string{
				"pr", "list",
				"--repo", g.Repo,
				"--state", "open",
				"--label", label,
				"--json", "number,title,url,labels,headRefName",
				"--limit", "20",
			}

			out, err := g.CmdRunner.Run(ctx, "gh", args...)
			if err != nil {
				return vessels, fmt.Errorf("gh pr list: %w", err)
			}

			var prs []ghPR
			if err := json.Unmarshal(out, &prs); err != nil {
				return vessels, fmt.Errorf("parse gh pr list output: %w", err)
			}

			for _, pr := range prs {
				if seen[pr.Number] {
					continue
				}
				if g.hasExcludedLabel(pr, excludeSet) ||
					g.Queue.HasRef(pr.URL) ||
					g.hasBranch(ctx, pr.Number) {
					continue
				}
				seen[pr.Number] = true
				vessels = append(vessels, queue.Vessel{
					ID:     fmt.Sprintf("pr-%d", pr.Number),
					Source: "github-pr",
					Ref:    pr.URL,
					Workflow: task.Workflow,
					Meta: map[string]string{
						"pr_num": strconv.Itoa(pr.Number),
					},
					State:     queue.StatePending,
					CreatedAt: time.Now().UTC(),
				})
			}
		}
	}
	return vessels, nil
}

func (g *GitHubPR) OnStart(ctx context.Context, vessel queue.Vessel) error {
	if g.CmdRunner == nil {
		return nil
	}
	prNum := vessel.Meta["pr_num"]
	if prNum == "" {
		return nil
	}
	// Best-effort: add in-progress label
	_, _ = g.CmdRunner.Run(ctx, "gh", "pr", "edit",
		prNum,
		"--repo", g.Repo,
		"--add-label", "in-progress")
	return nil
}

func (g *GitHubPR) BranchName(vessel queue.Vessel) string {
	prNum := vessel.Meta["pr_num"]
	slug := slugify(vessel.Ref)
	return fmt.Sprintf("review/pr-%s-%s", prNum, slug)
}

func (g *GitHubPR) hasExcludedLabel(pr ghPR, excluded map[string]bool) bool {
	for _, l := range pr.Labels {
		if excluded[l.Name] {
			return true
		}
	}
	return false
}

func (g *GitHubPR) hasBranch(ctx context.Context, prNum int) bool {
	pattern := fmt.Sprintf("review/pr-%d-*", prNum)
	out, err := g.CmdRunner.Run(ctx, "git", "ls-remote", "--heads", "origin", pattern)
	if err == nil && strings.Contains(string(out), "\t") {
		return true
	}
	return false
}
