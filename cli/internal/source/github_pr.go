package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
)

// GitHubPR scans GitHub pull requests and produces vessels.
type GitHubPR struct {
	Repo                   string
	Tasks                  map[string]GitHubTask
	Exclude                []string
	StateDir               string
	DefaultTier            string
	Queue                  *queue.Queue
	CmdRunner              CommandRunner
	HarnessDigestResolver  func() string
	WorkflowDigestResolver func(string) string
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
	HeadOID     string `json:"headRefOid"`
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
				baseVessel := queue.Vessel{
					ID:       fmt.Sprintf("pr-%d-%s", pr.Number, task.Workflow),
					Source:   "github-pr",
					Ref:      prWorkflowRef(pr.URL, task.Workflow),
					Workflow: task.Workflow,
					Tier:     ResolveTaskTier(task.Tier, g.DefaultTier),
					Meta: map[string]string{
						"pr_num":                   strconv.Itoa(pr.Number),
						"pr_title":                 pr.Title,
						"pr_body":                  pr.Body,
						"pr_labels":                strings.Join(issueLabelNames(pr.Labels), ","),
						"source_input_fingerprint": fingerprint,
					},
					State:     queue.StatePending,
					CreatedAt: sourceNow(),
				}
				baseVessel.Meta = applyCurrentRemediationMeta(baseVessel.Meta, nil, g.currentHarnessDigest(), g.currentWorkflowDigest(task.Workflow))
				sl := task.StatusLabels
				if sl != nil {
					baseVessel.Meta["status_label_queued"] = sl.Queued
					baseVessel.Meta["status_label_running"] = sl.Running
					baseVessel.Meta["status_label_completed"] = sl.Completed
					baseVessel.Meta["status_label_failed"] = sl.Failed
					baseVessel.Meta["status_label_timed_out"] = sl.TimedOut
				}
				lgl := task.LabelGateLabels
				if lgl != nil {
					baseVessel.Meta["label_gate_label_waiting"] = lgl.Waiting
					baseVessel.Meta["label_gate_label_ready"] = lgl.Ready
				}
				if g.hasExcludedLabel(pr, excludeSet) {
					continue
				}
				if task.Workflow == resolveConflictsWorkflow && pr.Mergeable != ghMergeableConflicting {
					if pr.Mergeable == ghMergeableMergeable {
						g.stripTaskLabels(ctx, pr.Number, task.Labels)
					}
					continue
				}
				retryVessel, blocked, err := g.retryCandidate(baseVessel, pr.URL, fingerprint, task.Workflow)
				if err != nil {
					return vessels, err
				}
				if blocked {
					continue
				}
				if g.hasBranch(ctx, pr.Number) {
					continue
				}
				seen[key] = true
				if retryVessel != nil {
					vessels = append(vessels, *retryVessel)
					continue
				}
				vessels = append(vessels, baseVessel)
			}
		}
	}
	return vessels, nil
}

func (g *GitHubPR) BacklogCount(ctx context.Context) (int, error) {
	excludeSet := make(map[string]bool, len(g.Exclude))
	for _, ex := range g.Exclude {
		excludeSet[ex] = true
	}

	seen := make(map[prWorkflowSeenKey]struct{})
	count := 0
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
				return 0, fmt.Errorf("gh pr list: %w", err)
			}

			var prs []ghPR
			if err := json.Unmarshal(out, &prs); err != nil {
				return 0, fmt.Errorf("parse gh pr list output: %w", err)
			}

			for _, pr := range prs {
				key := prWorkflowSeenKey{prNum: pr.Number, workflow: task.Workflow}
				if _, ok := seen[key]; ok || g.hasExcludedLabel(pr, excludeSet) {
					continue
				}
				if task.Workflow == resolveConflictsWorkflow && pr.Mergeable != ghMergeableConflicting {
					continue
				}
				seen[key] = struct{}{}
				count++
			}
		}
	}
	return count, nil
}

func (g *GitHubPR) OnEnqueue(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabels(ctx, vessel.Meta["pr_num"],
		[]string{vessel.Meta["status_label_queued"]}, nil)
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
	g.applyPRLabels(ctx, prNum,
		[]string{ResolveRunningLabel(vessel)},
		[]string{vessel.Meta["status_label_queued"], resolveReadyLabel(vessel)})
	return nil
}

func (g *GitHubPR) OnWait(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabels(ctx, vessel.Meta["pr_num"],
		[]string{resolveWaitingLabel(vessel)},
		[]string{resolveReadyLabel(vessel)})
	return nil
}

func (g *GitHubPR) OnResume(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabels(ctx, vessel.Meta["pr_num"],
		[]string{resolveReadyLabel(vessel)},
		[]string{resolveWaitingLabel(vessel)})
	return nil
}

func (g *GitHubPR) OnComplete(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabels(ctx, vessel.Meta["pr_num"],
		[]string{vessel.Meta["status_label_completed"]},
		[]string{ResolveRunningLabel(vessel), resolveWaitingLabel(vessel), resolveReadyLabel(vessel)})
	return nil
}

func (g *GitHubPR) OnFail(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabels(ctx, vessel.Meta["pr_num"],
		[]string{vessel.Meta["status_label_failed"]},
		[]string{ResolveRunningLabel(vessel), resolveWaitingLabel(vessel), resolveReadyLabel(vessel)})
	return nil
}

func (g *GitHubPR) OnTimedOut(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabels(ctx, vessel.Meta["pr_num"],
		[]string{vessel.Meta["status_label_timed_out"]},
		[]string{ResolveRunningLabel(vessel), resolveWaitingLabel(vessel), resolveReadyLabel(vessel)})
	return nil
}

func (g *GitHubPR) RemoveRunningLabel(ctx context.Context, vessel queue.Vessel) error {
	g.applyPRLabels(ctx, vessel.Meta["pr_num"], nil, []string{ResolveRunningLabel(vessel)})
	return nil
}

// applyPRLabels runs gh pr edit to add and/or remove labels on the PR.
// Empty labels are ignored, and conflicting add/remove operations prefer add.
func (g *GitHubPR) applyPRLabels(ctx context.Context, prNum string, add []string, remove []string) {
	if g.CmdRunner == nil || prNum == "" {
		return
	}
	add, remove = normalizeLabelOps(add, remove)
	if len(add) == 0 && len(remove) == 0 {
		return
	}
	args := []string{"pr", "edit", prNum, "--repo", g.Repo}
	for _, label := range add {
		args = append(args, "--add-label", label)
	}
	for _, label := range remove {
		args = append(args, "--remove-label", label)
	}
	_, _ = g.CmdRunner.Run(ctx, "gh", args...)
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
	g.applyPRLabels(ctx, prNum, []string{add}, []string{remove})
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

func priorVesselBlocksReenqueue(v *queue.Vessel, fingerprint string) bool {
	if v == nil {
		return false
	}
	switch v.State {
	case queue.StatePending, queue.StateRunning, queue.StateWaiting:
		return true
	case queue.StateFailed, queue.StateTimedOut:
		return v.Meta["source_input_fingerprint"] == fingerprint
	default:
		return false
	}
}

func (g *GitHubPR) retryCandidate(base queue.Vessel, prURL, fingerprint, workflow string) (*queue.Vessel, bool, error) {
	if g == nil || g.Queue == nil {
		return nil, false, nil
	}
	latest, err := g.Queue.FindLatestByRef(prWorkflowRef(prURL, workflow))
	if err != nil || latest == nil {
		latest, err = g.Queue.FindLatestByRef(prURL)
		if err != nil || latest == nil || latest.Workflow != workflow {
			return nil, false, nil
		}
	}
	switch latest.State {
	case queue.StatePending, queue.StateRunning, queue.StateWaiting:
		return nil, true, nil
	case queue.StateFailed, queue.StateTimedOut:
		artifact, found, loadErr := g.loadRetryArtifact(*latest)
		if loadErr != nil {
			return nil, false, loadErr
		}
		if !found {
			if latest.Meta["source_input_fingerprint"] != fingerprint {
				return nil, false, nil
			}
			return nil, true, nil
		}
		base.Meta = applyCurrentRemediationMeta(base.Meta, artifact, g.currentHarnessDigest(), g.currentWorkflowDigest(base.Workflow))
		decision := retryDecision(artifact, latest.Meta, base.Meta, sourceNow())
		if !decision.Eligible {
			return nil, true, nil
		}
		retry := recovery.NextRetryVessel(base, *latest, artifact, g.Queue, sourceNow(), decision.UnlockDimension)
		return &retry, false, nil
	default:
		return nil, false, nil
	}
}

func (g *GitHubPR) loadRetryArtifact(vessel queue.Vessel) (*recovery.Artifact, bool, error) {
	if g.StateDir == "" {
		return nil, false, nil
	}
	artifact, err := recovery.LoadForVessel(g.StateDir, vessel.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("load recovery artifact for %s: %w", vessel.ID, err)
	}
	return recovery.HydrateArtifact(artifact, vessel.Meta), true, nil
}

func (g *GitHubPR) currentHarnessDigest() string {
	if g != nil && g.HarnessDigestResolver != nil {
		return strings.TrimSpace(g.HarnessDigestResolver())
	}
	return defaultHarnessDigest()
}

func (g *GitHubPR) currentWorkflowDigest(workflow string) string {
	if g != nil && g.WorkflowDigestResolver != nil {
		return strings.TrimSpace(g.WorkflowDigestResolver(workflow))
	}
	return defaultWorkflowDigest(workflow)
}
