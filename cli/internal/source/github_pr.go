package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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
	Body        string `json:"body"`
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
				"--json", "number,title,body,url,labels,headRefName",
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
				fingerprint := githubSourceFingerprint(pr.Title, pr.Body, issueLabelNames(pr.Labels))
				if g.hasExcludedLabel(pr, excludeSet) ||
					g.Queue.HasRef(pr.URL) ||
					g.hasMatchingFailedFingerprint(pr.URL, fingerprint) ||
					g.hasBranch(ctx, pr.Number) {
					continue
				}
				seen[pr.Number] = true
				meta := map[string]string{
					"pr_num":                   strconv.Itoa(pr.Number),
					"pr_title":                 pr.Title,
					"pr_body":                  pr.Body,
					"pr_labels":                strings.Join(issueLabelNames(pr.Labels), ","),
					"source_input_fingerprint": fingerprint,
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
					ID:        fmt.Sprintf("pr-%d", pr.Number),
					Source:    "github-pr",
					Ref:       pr.URL,
					Workflow:  task.Workflow,
					Meta:      meta,
					State:     queue.StatePending,
					CreatedAt: sourceNow(),
				})
			}
		}
	}
	return vessels, nil
}

func (g *GitHubPR) OnEnqueue(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabel(ctx, vessel.Meta["pr_num"],
		vessel.Meta["status_label_queued"], "")
	return nil
}

func (g *GitHubPR) OnStart(ctx context.Context, vessel queue.Vessel) error {
	if g.CmdRunner == nil {
		return nil
	}
	prNum := vessel.Meta["pr_num"]
	if prNum == "" {
		return nil
	}
	g.applyPRLabel(ctx, prNum, ResolveRunningLabel(vessel), vessel.Meta["status_label_queued"])
	return nil
}

func (g *GitHubPR) OnComplete(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabel(ctx, vessel.Meta["pr_num"],
		vessel.Meta["status_label_completed"],
		ResolveRunningLabel(vessel))
	return nil
}

func (g *GitHubPR) OnFail(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabel(ctx, vessel.Meta["pr_num"],
		vessel.Meta["status_label_failed"],
		ResolveRunningLabel(vessel))
	return nil
}

func (g *GitHubPR) OnTimedOut(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabel(ctx, vessel.Meta["pr_num"],
		vessel.Meta["status_label_timed_out"],
		ResolveRunningLabel(vessel))
	return nil
}

func (g *GitHubPR) RemoveRunningLabel(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabel(ctx, vessel.Meta["pr_num"], "", ResolveRunningLabel(vessel))
	return nil
}

// applyPRLabel runs gh pr edit to add and/or remove a label on the PR.
// Both add and remove are optional — empty string means skip that operation.
func (g *GitHubPR) applyPRLabel(ctx context.Context, prNum, add, remove string) {
	if g.CmdRunner == nil || prNum == "" {
		return
	}
	if add == "" && remove == "" {
		return
	}
	args := []string{"pr", "edit", prNum, "--repo", g.Repo}
	if add != "" {
		args = append(args, "--add-label", add)
	}
	if remove != "" {
		args = append(args, "--remove-label", remove)
	}
	_, _ = g.CmdRunner.Run(ctx, "gh", args...)
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

func (g *GitHubPR) hasMatchingFailedFingerprint(ref, fingerprint string) bool {
	latest, err := g.Queue.FindLatestByRef(ref)
	if err != nil || latest == nil {
		return false
	}
	return latest.State == queue.StateFailed && latest.Meta["source_input_fingerprint"] == fingerprint
}
