package source

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
)

// GitHubTask defines a label-based task for the GitHub source.
type GitHubTask struct {
	Labels          []string
	Workflow        string
	Tier            string
	StatusLabels    *StatusLabels
	LabelGateLabels *LabelGateLabels
}

// GitHub scans GitHub issues and produces vessels.
type GitHub struct {
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

type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
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
				"--json", "number,title,body,url,labels",
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
				fingerprint := githubSourceFingerprint(issue.Title, issue.Body, issueLabelNames(issue.Labels))
				baseMeta := map[string]string{
					"issue_num":                strconv.Itoa(issue.Number),
					"issue_title":              issue.Title,
					"issue_body":               issue.Body,
					"issue_labels":             strings.Join(issueLabelNames(issue.Labels), ","),
					"source_input_fingerprint": fingerprint,
					// trigger_label records which of this task's configured
					// labels matched on the source issue during scan. On
					// vessel completion this label is removed so the issue
					// no longer appears in the scanner's candidate set,
					// preventing duplicate enqueue after PR lifecycle events
					// (close/merge) and keeping the issue's UI state
					// consistent with its workflow state.
					"trigger_label": label,
				}
				baseMeta = applyCurrentRemediationMeta(baseMeta, nil, g.currentHarnessDigest(), g.currentWorkflowDigest(task.Workflow))
				sl := task.StatusLabels
				if sl != nil {
					baseMeta["status_label_queued"] = sl.Queued
					baseMeta["status_label_running"] = sl.Running
					baseMeta["status_label_completed"] = sl.Completed
					baseMeta["status_label_failed"] = sl.Failed
					baseMeta["status_label_timed_out"] = sl.TimedOut
				}
				lgl := task.LabelGateLabels
				if lgl != nil {
					baseMeta["label_gate_label_waiting"] = lgl.Waiting
					baseMeta["label_gate_label_ready"] = lgl.Ready
				}
				baseVessel := queue.Vessel{
					ID:        fmt.Sprintf("issue-%d", issue.Number),
					Source:    "github-issue",
					Ref:       issue.URL,
					Workflow:  task.Workflow,
					Tier:      ResolveTaskTier(task.Tier, g.DefaultTier),
					Meta:      baseMeta,
					State:     queue.StatePending,
					CreatedAt: sourceNow(),
				}
				if g.hasExcludedLabel(issue, excludeSet) {
					continue
				}

				retryVessel, blocked, err := g.retryCandidate(baseVessel)
				if err != nil {
					return vessels, err
				}
				if blocked {
					continue
				}
				if g.scanBlockedByRepoState(ctx, issue.Number, retryVessel != nil) {
					continue
				}
				seen[issue.Number] = true
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

func (g *GitHub) BacklogCount(ctx context.Context) (int, error) {
	excludeSet := make(map[string]bool, len(g.Exclude))
	for _, ex := range g.Exclude {
		excludeSet[ex] = true
	}

	seen := make(map[int]struct{})
	count := 0
	for _, task := range g.Tasks {
		for _, label := range task.Labels {
			args := []string{
				"search", "issues",
				"--repo", g.Repo,
				"--state", "open",
				"--json", "number,title,body,url,labels",
				"--limit", "20",
				"--label", label,
			}

			out, err := g.CmdRunner.Run(ctx, "gh", args...)
			if err != nil {
				return 0, fmt.Errorf("gh search issues: %w", err)
			}

			var issues []ghIssue
			if err := json.Unmarshal(out, &issues); err != nil {
				return 0, fmt.Errorf("parse gh search output: %w", err)
			}

			for _, issue := range issues {
				if _, ok := seen[issue.Number]; ok || g.hasExcludedLabel(issue, excludeSet) {
					continue
				}
				seen[issue.Number] = struct{}{}
				count++
			}
		}
	}
	return count, nil
}

func (g *GitHub) OnEnqueue(ctx context.Context, vessel queue.Vessel) error {
	g.applyIssueLabels(ctx, vessel.Meta["issue_num"],
		[]string{vessel.Meta["status_label_queued"]}, nil)
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
	g.applyIssueLabels(ctx, issueNum,
		[]string{ResolveRunningLabel(vessel)},
		[]string{vessel.Meta["status_label_queued"], resolveReadyLabel(vessel)})
	return nil
}

func (g *GitHub) OnWait(ctx context.Context, vessel queue.Vessel) error {
	g.applyIssueLabels(ctx, vessel.Meta["issue_num"],
		[]string{resolveWaitingLabel(vessel)},
		[]string{resolveReadyLabel(vessel)})
	return nil
}

func (g *GitHub) OnResume(ctx context.Context, vessel queue.Vessel) error {
	g.applyIssueLabels(ctx, vessel.Meta["issue_num"],
		[]string{resolveReadyLabel(vessel)},
		[]string{resolveWaitingLabel(vessel)})
	return nil
}

func (g *GitHub) OnComplete(ctx context.Context, vessel queue.Vessel) error {
	issueNum := vessel.Meta["issue_num"]
	g.applyIssueLabels(ctx, issueNum,
		[]string{vessel.Meta["status_label_completed"]},
		[]string{ResolveRunningLabel(vessel), resolveWaitingLabel(vessel), resolveReadyLabel(vessel)})
	// Remove the label that triggered this vessel's enqueue. Without this,
	// completed vessels leave their trigger label (e.g. "ready-for-work")
	// on the source issue, which is a cosmetic state bug *and* a latent
	// duplicate-enqueue hazard once the PR is closed/merged and branch
	// dedup checks fall through. Done as a separate applyIssueLabel call
	// so it doesn't interfere with the status_label_* transition above.
	// Backward-compat: vessels queued before this field was introduced
	// have an empty trigger_label and skip this step.
	if trig := vessel.Meta["trigger_label"]; trig != "" {
		g.applyIssueLabels(ctx, issueNum, nil, []string{trig})
	}
	return nil
}

func (g *GitHub) OnFail(ctx context.Context, vessel queue.Vessel) error {
	latest := g.recoveryAwareVessel(vessel)
	g.applyIssueLabels(ctx, latest.Meta["issue_num"],
		[]string{latest.Meta["status_label_failed"]},
		[]string{ResolveRunningLabel(vessel), resolveWaitingLabel(vessel), resolveReadyLabel(vessel)})
	if shouldRouteToRefinement(latest) {
		remove := []string{}
		if trig := latest.Meta["trigger_label"]; trig != "" {
			remove = append(remove, trig)
		}
		g.applyIssueLabels(ctx, latest.Meta["issue_num"], []string{"needs-refinement"}, remove)
	}
	return nil
}

func (g *GitHub) OnTimedOut(ctx context.Context, vessel queue.Vessel) error {
	latest := g.recoveryAwareVessel(vessel)
	g.applyIssueLabels(ctx, latest.Meta["issue_num"],
		[]string{latest.Meta["status_label_timed_out"]},
		[]string{ResolveRunningLabel(vessel), resolveWaitingLabel(vessel), resolveReadyLabel(vessel)})
	if shouldRouteToRefinement(latest) {
		remove := []string{}
		if trig := latest.Meta["trigger_label"]; trig != "" {
			remove = append(remove, trig)
		}
		g.applyIssueLabels(ctx, latest.Meta["issue_num"], []string{"needs-refinement"}, remove)
	}
	return nil
}

func (g *GitHub) RemoveRunningLabel(ctx context.Context, vessel queue.Vessel) error {
	g.applyIssueLabels(ctx, vessel.Meta["issue_num"], nil, []string{ResolveRunningLabel(vessel)})
	return nil
}

// applyIssueLabels runs gh issue edit to add and/or remove labels on the issue.
// Empty labels are ignored, and conflicting add/remove operations prefer add.
func (g *GitHub) applyIssueLabels(ctx context.Context, issueNum string, add []string, remove []string) {
	if g.CmdRunner == nil || issueNum == "" {
		return
	}
	add, remove = normalizeLabelOps(add, remove)
	if len(add) == 0 && len(remove) == 0 {
		return
	}
	args := []string{"issue", "edit", issueNum, "--repo", g.Repo}
	for _, label := range add {
		args = append(args, "--add-label", label)
	}
	for _, label := range remove {
		args = append(args, "--remove-label", label)
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

func sourceNow() time.Time {
	now, err := dtu.RuntimeNow()
	if err != nil {
		log.Printf("warn: source: resolve runtime clock: %v", err)
		return time.Now().UTC()
	}
	return now.UTC()
}

func (g *GitHub) retryCandidate(base queue.Vessel) (*queue.Vessel, bool, error) {
	if g == nil || g.Queue == nil {
		return nil, false, nil
	}
	latest, err := g.Queue.FindLatestByRef(base.Ref)
	if err != nil || latest == nil {
		return nil, false, nil
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
			retry, blocked := retryCandidateWithoutArtifact(base, *latest, g.Queue, sourceNow())
			return retry, blocked, nil
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

func (g *GitHub) loadRetryArtifact(vessel queue.Vessel) (*recovery.Artifact, bool, error) {
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

func (g *GitHub) recoveryAwareVessel(vessel queue.Vessel) queue.Vessel {
	if g == nil || g.Queue == nil {
		return vessel
	}
	latest, err := g.Queue.FindByID(vessel.ID)
	if err != nil || latest == nil {
		return vessel
	}
	return *latest
}

func shouldRouteToRefinement(vessel queue.Vessel) bool {
	action := vessel.Meta[recovery.MetaAction]
	class := vessel.Meta[recovery.MetaClass]
	return action == string(recovery.ActionRefine) ||
		action == string(recovery.ActionSplitTask) ||
		action == string(recovery.ActionRequestInfo) ||
		action == string(recovery.ActionSplitIssue) ||
		class == string(recovery.ClassSpecGap) ||
		class == string(recovery.ClassScopeGap)
}

func (g *GitHub) currentHarnessDigest() string {
	if g != nil && g.HarnessDigestResolver != nil {
		return strings.TrimSpace(g.HarnessDigestResolver())
	}
	return defaultHarnessDigest()
}

func (g *GitHub) currentWorkflowDigest(workflow string) string {
	if g != nil && g.WorkflowDigestResolver != nil {
		return strings.TrimSpace(g.WorkflowDigestResolver(workflow))
	}
	return defaultWorkflowDigest(workflow)
}

func issueLabelNames(labels []struct {
	Name string `json:"name"`
}) []string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names
}

func githubSourceFingerprint(title, body string, labels []string) string {
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		title,
		body,
		strings.Join(sorted, ","),
	}, "\n")))
	return fmt.Sprintf("%x", sum)
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

func (g *GitHub) hasMergedPR(ctx context.Context, issueNum int) bool {
	for _, prefix := range branchPrefixes {
		search := fmt.Sprintf("head:%s/issue-%d-", prefix, issueNum)
		out, err := g.CmdRunner.Run(ctx, "gh", "pr", "list",
			"--repo", g.Repo,
			"--search", search,
			"--state", "merged",
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

func (g *GitHub) scanBlockedByRepoState(ctx context.Context, issueNum int, retrying bool) bool {
	if retrying {
		if g.hasOpenPR(ctx, issueNum) {
			return true
		}
		return g.hasMergedPR(ctx, issueNum)
	}
	if g.hasBranch(ctx, issueNum) {
		return true
	}
	if g.hasOpenPR(ctx, issueNum) {
		return true
	}
	return g.hasMergedPR(ctx, issueNum)
}
