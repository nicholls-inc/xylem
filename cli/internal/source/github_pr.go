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

// resolveConflictsWorkflow is the de facto workflow identifier whose task
// labels are only meaningful when a PR is actually in a CONFLICTING merge
// state. When a PR bearing one of this workflow's labels reports any other
// mergeable state, the source skips it and proactively removes the label
// so the loop does not re-enqueue the same no-op work every scan.
const resolveConflictsWorkflow = "resolve-conflicts"

// ghMergeableConflicting / ghMergeableMergeable mirror the string values
// returned by `gh pr list --json mergeable`. UNKNOWN (empty or literal) is
// treated as "not definitively mergeable" — the source skips the vessel
// but does NOT strip labels, so a subsequent scan can re-evaluate once
// GitHub finishes computing the merge state.
const (
	ghMergeableConflicting = "CONFLICTING"
	ghMergeableMergeable   = "MERGEABLE"
)

type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	HeadRefName string `json:"headRefName"`
	Mergeable   string `json:"mergeable"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (g *GitHubPR) Name() string { return "github-pr" }

// prWorkflowSeenKey identifies a (PR number, workflow) pair so that one
// Scan() call can produce vessels for the same PR under multiple distinct
// workflows (e.g., merge-pr AND resolve-conflicts) without false-positive
// intra-scan dedup.
type prWorkflowSeenKey struct {
	prNum    int
	workflow string
}

// prWorkflowRef qualifies a PR URL with its target workflow. Two sources
// may scan the same PR for different workflows (e.g., harness-merge runs
// merge-pr, conflict-resolution runs resolve-conflicts); without this
// qualifier they would share a dedup namespace and a failed vessel for
// one workflow would block enqueue of the other.
func prWorkflowRef(prURL, workflow string) string {
	return fmt.Sprintf("%s#workflow=%s", prURL, workflow)
}

func (g *GitHubPR) Scan(ctx context.Context) ([]queue.Vessel, error) {
	excludeSet := make(map[string]bool, len(g.Exclude))
	for _, ex := range g.Exclude {
		excludeSet[ex] = true
	}

	var vessels []queue.Vessel
	seen := make(map[prWorkflowSeenKey]bool)

	for _, task := range g.Tasks {
		for _, label := range task.Labels {
			args := []string{
				"pr", "list",
				"--repo", g.Repo,
				"--state", "open",
				"--label", label,
				"--json", "number,title,body,url,labels,headRefName,mergeable",
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
				key := prWorkflowSeenKey{prNum: pr.Number, workflow: task.Workflow}
				if seen[key] {
					continue
				}
				fingerprint := githubSourceFingerprint(pr.Title, pr.Body, issueLabelNames(pr.Labels))
				if g.hasExcludedLabel(pr, excludeSet) ||
					g.isBlockedByPriorVessel(pr.URL, fingerprint, task.Workflow) ||
					g.hasBranch(ctx, pr.Number) {
					continue
				}
				// resolve-conflicts workflow is only meaningful for PRs in a
				// CONFLICTING merge state. When GitHub reports MERGEABLE, the
				// label is stale (conflicts were resolved outside this
				// workflow, e.g., via manual push or rebase) — strip it so
				// the next scan does not re-match, breaking what would
				// otherwise be an infinite enqueue loop. When GitHub reports
				// UNKNOWN (empty or literal), skip the vessel but preserve
				// the label so a subsequent scan can re-evaluate once the
				// merge state has been computed.
				if task.Workflow == resolveConflictsWorkflow && pr.Mergeable != ghMergeableConflicting {
					if pr.Mergeable == ghMergeableMergeable {
						g.stripTaskLabels(ctx, pr.Number, task.Labels)
					}
					continue
				}
				seen[key] = true
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
					ID:        fmt.Sprintf("pr-%d-%s", pr.Number, task.Workflow),
					Source:    "github-pr",
					Ref:       prWorkflowRef(pr.URL, task.Workflow),
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

// stripTaskLabels removes the given task-selector labels from a PR. Used
// to break the resolve-conflicts enqueue loop when a PR carries a
// needs-conflict-resolution-style label but is no longer in a CONFLICTING
// merge state. Each label is removed via its own gh pr edit call so a
// single failure does not cascade across the remaining labels.
func (g *GitHubPR) stripTaskLabels(ctx context.Context, prNum int, labels []string) {
	if g.CmdRunner == nil || prNum == 0 {
		return
	}
	num := strconv.Itoa(prNum)
	for _, label := range labels {
		if label == "" {
			continue
		}
		g.applyPRLabel(ctx, num, "", label)
	}
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

// isBlockedByPriorVessel reports whether a prior vessel already occupies
// the dedup slot for this (PR URL, workflow) pair and so the scanner
// should not enqueue a new vessel. It checks the new workflow-qualified
// ref (`<url>#workflow=<name>`) first; then for backward-compat with
// queue entries written before refs were qualified, it falls back to
// the legacy bare-URL ref and only treats a legacy vessel as blocking
// when it belongs to the SAME workflow as the current task.
//
// Blocking conditions:
//   - A pending/running/waiting vessel at the qualified ref (via HasRef).
//   - A failed vessel at the qualified ref whose fingerprint equals the
//     current PR input fingerprint (hasMatchingFailedFingerprint).
//   - A legacy bare-URL vessel whose Workflow matches and is either
//     active (pending/running/waiting) or terminally failed with a
//     matching fingerprint.
//
// This preserves the dedup guarantees of the pre-qualification scheme
// for in-flight workflows while allowing distinct workflows over the
// same PR (e.g., merge-pr and resolve-conflicts) to coexist.
func (g *GitHubPR) isBlockedByPriorVessel(prURL, fingerprint, workflow string) bool {
	qualifiedRef := prWorkflowRef(prURL, workflow)
	if g.Queue.HasRef(qualifiedRef) {
		return true
	}
	if g.hasMatchingFailedFingerprint(qualifiedRef, fingerprint) {
		return true
	}
	// Backward-compat: legacy queue entries were written with ref = prURL.
	latest, err := g.Queue.FindLatestByRef(prURL)
	if err != nil || latest == nil {
		return false
	}
	// Only a legacy vessel belonging to the SAME workflow is blocking.
	// Otherwise a failed merge-pr vessel would block resolve-conflicts
	// enqueue for the same PR — the exact regression this fix addresses.
	if latest.Workflow != workflow {
		return false
	}
	switch latest.State {
	case queue.StatePending, queue.StateRunning, queue.StateWaiting:
		return true
	case queue.StateFailed:
		return latest.Meta["source_input_fingerprint"] == fingerprint
	}
	return false
}
