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

// GitHubTask defines a label-based task for the GitHub source.
type GitHubTask struct {
	Labels       []string
	Workflow     string
	StatusLabels *StatusLabels
}

// GitHub scans GitHub issues and produces vessels.
type GitHub struct {
	Repo      string
	Tasks     map[string]GitHubTask
	Exclude   []string
	Queue     *queue.Queue
	CmdRunner CommandRunner
}

type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (g *GitHub) Name() string { return "github-issue" }

func (g *GitHub) Scan(ctx context.Context) ([]queue.Vessel, error) {
	excludeSet := make(map[string]bool, len(g.Exclude))
	for _, ex := range g.Exclude {
		excludeSet[ex] = true
	}

	var vessels []queue.Vessel
	seen := make(map[int]bool)

	for _, task := range g.Tasks {
		for _, label := range task.Labels {
			args := []string{
				"search", "issues",
				"--repo", g.Repo,
				"--state", "open",
				"--json", "number,title,url,labels",
				"--limit", "20",
				"--label", label,
			}

			out, err := g.CmdRunner.Run(ctx, "gh", args...)
			if err != nil {
				return vessels, fmt.Errorf("gh search issues: %w", err)
			}

			var issues []ghIssue
			if err := json.Unmarshal(out, &issues); err != nil {
				return vessels, fmt.Errorf("parse gh search output: %w", err)
			}

			for _, issue := range issues {
				if seen[issue.Number] {
					continue
				}
				if g.hasExcludedLabel(issue, excludeSet) ||
					g.Queue.HasRef(issue.URL) ||
					g.hasBranch(ctx, issue.Number) ||
					g.hasOpenPR(ctx, issue.Number) {
					continue
				}
				seen[issue.Number] = true
				meta := map[string]string{
					"issue_num": strconv.Itoa(issue.Number),
				}
				sl := task.StatusLabels
				if sl != nil {
					meta["status_label_queued"] = sl.Queued
					meta["status_label_running"] = sl.Running
					meta["status_label_completed"] = sl.Completed
					meta["status_label_failed"] = sl.Failed
					meta["status_label_timed_out"] = sl.TimedOut
				}
				vessels = append(vessels, queue.Vessel{
					ID:        fmt.Sprintf("issue-%d", issue.Number),
					Source:    "github-issue",
					Ref:       issue.URL,
					Workflow:  task.Workflow,
					Meta:      meta,
					State:     queue.StatePending,
					CreatedAt: time.Now().UTC(),
				})
			}
		}
	}
	return vessels, nil
}

func (g *GitHub) OnEnqueue(ctx context.Context, vessel queue.Vessel) error {
	g.applyIssueLabel(ctx, vessel.Meta["issue_num"],
		vessel.Meta["status_label_queued"], "")
	return nil
}

func (g *GitHub) OnStart(ctx context.Context, vessel queue.Vessel) error {
	if g.CmdRunner == nil {
		return nil
	}
	issueNum := vessel.Meta["issue_num"]
	if issueNum == "" {
		return nil
	}
	running, hasRunning := vessel.Meta["status_label_running"]
	if !hasRunning {
		running = "in-progress" // backward-compat: preserve old behaviour
	}
	g.applyIssueLabel(ctx, issueNum, running, vessel.Meta["status_label_queued"])
	return nil
}

func (g *GitHub) OnComplete(ctx context.Context, vessel queue.Vessel) error {
	g.applyIssueLabel(ctx, vessel.Meta["issue_num"],
		vessel.Meta["status_label_completed"],
		vessel.Meta["status_label_running"])
	return nil
}

func (g *GitHub) OnFail(ctx context.Context, vessel queue.Vessel) error {
	g.applyIssueLabel(ctx, vessel.Meta["issue_num"],
		vessel.Meta["status_label_failed"],
		vessel.Meta["status_label_running"])
	return nil
}

func (g *GitHub) OnTimedOut(ctx context.Context, vessel queue.Vessel) error {
	g.applyIssueLabel(ctx, vessel.Meta["issue_num"],
		vessel.Meta["status_label_timed_out"],
		vessel.Meta["status_label_running"])
	return nil
}

// applyIssueLabel runs gh issue edit to add and/or remove a label on the issue.
// Both add and remove are optional — empty string means skip that operation.
func (g *GitHub) applyIssueLabel(ctx context.Context, issueNum, add, remove string) {
	if g.CmdRunner == nil || issueNum == "" {
		return
	}
	if add == "" && remove == "" {
		return
	}
	args := []string{"issue", "edit", issueNum, "--repo", g.Repo}
	if add != "" {
		args = append(args, "--add-label", add)
	}
	if remove != "" {
		args = append(args, "--remove-label", remove)
	}
	_, _ = g.CmdRunner.Run(ctx, "gh", args...)
}

func (g *GitHub) BranchName(vessel queue.Vessel) string {
	prefix := "feat"
	if strings.Contains(strings.ToLower(vessel.Workflow), "fix") {
		prefix = "fix"
	}
	issueNum := vessel.Meta["issue_num"]
	slug := slugify(vessel.Ref)
	return fmt.Sprintf("%s/issue-%s-%s", prefix, issueNum, slug)
}

func (g *GitHub) hasExcludedLabel(issue ghIssue, excluded map[string]bool) bool {
	for _, l := range issue.Labels {
		if excluded[l.Name] {
			return true
		}
	}
	return false
}

// branchPrefixes lists the branch name prefixes xylem uses when creating
// worktree branches.
var branchPrefixes = []string{"fix", "feat"}

func (g *GitHub) hasBranch(ctx context.Context, issueNum int) bool {
	for _, prefix := range branchPrefixes {
		pattern := fmt.Sprintf("%s/issue-%d-*", prefix, issueNum)
		out, err := g.CmdRunner.Run(ctx, "git", "ls-remote", "--heads", "origin", pattern)
		if err == nil && strings.Contains(string(out), "\t") {
			return true
		}
	}
	return false
}

func (g *GitHub) hasOpenPR(ctx context.Context, issueNum int) bool {
	for _, prefix := range branchPrefixes {
		search := fmt.Sprintf("head:%s/issue-%d-", prefix, issueNum)
		out, err := g.CmdRunner.Run(ctx, "gh", "pr", "list",
			"--repo", g.Repo,
			"--search", search,
			"--state", "open",
			"--json", "number,headRefName",
			"--limit", "5")
		if err != nil {
			continue
		}
		var prs []struct {
			Number      int    `json:"number"`
			HeadRefName string `json:"headRefName"`
		}
		if err := json.Unmarshal(out, &prs); err != nil {
			continue
		}
		branchPrefix := fmt.Sprintf("%s/issue-%d-", prefix, issueNum)
		for _, pr := range prs {
			if strings.HasPrefix(pr.HeadRefName, branchPrefix) {
				return true
			}
		}
	}
	return false
}
