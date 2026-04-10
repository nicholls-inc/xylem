package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

type autoMergeSettings struct {
	repo                     string
	labels                   []string
	branchPattern            *regexp.Regexp
	branchPatternRaw         string
	reviewer                 string
	optOutLabel              string
	conflictResolutionLabels []string
}

func newAutoMergeSettings(dc config.DaemonConfig) (autoMergeSettings, error) {
	pattern := dc.EffectiveAutoMergeBranchPattern()
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return autoMergeSettings{}, fmt.Errorf("compile auto-merge branch pattern %q: %w", pattern, err)
	}
	labels := dc.EffectiveAutoMergeLabels()
	return autoMergeSettings{
		repo:                     strings.TrimSpace(dc.AutoMergeRepo),
		labels:                   labels,
		branchPattern:            compiled,
		branchPatternRaw:         pattern,
		reviewer:                 dc.EffectiveAutoMergeReviewer(),
		optOutLabel:              dc.EffectiveAutoAdminMergeOptOutLabel(),
		conflictResolutionLabels: append([]string{"needs-conflict-resolution"}, labels...),
	}, nil
}

// isBenignGhWarning reports whether a gh CLI error is a non-fatal warning
// that should not block the intended operation. The most common case is the
// "Projects (classic) is being deprecated" GraphQL warning, which gh prints
// alongside an exit code of 1 even though the underlying operation (edit,
// add-label, etc.) actually succeeded.
func isBenignGhWarning(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	benignFragments := []string{
		"Projects (classic) is being deprecated",
		"projectCards",
	}
	for _, f := range benignFragments {
		if strings.Contains(msg, f) {
			return true
		}
	}
	return false
}

// isReviewerNotCollaborator reports whether a gh api error from the
// requested_reviewers endpoint is the GitHub 422 "not a collaborator"
// response. This condition is terminal for a given (repo, reviewer)
// pair: the reviewer will not spontaneously become a collaborator, so
// retrying on every drain tick only spams the log. Callers should treat
// this as "review cannot be requested from this bot; continue with
// admin-merge safety checks instead".
func isReviewerNotCollaborator(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Reviews may only be requested from collaborators")
}

// prSummary is a minimal projection of `gh pr list` / `gh pr view` output.
type prSummary struct {
	Number            int    `json:"number"`
	HeadRefName       string `json:"headRefName"`
	Mergeable         string `json:"mergeable"`
	State             string `json:"state"`
	ReviewDecision    string `json:"reviewDecision"`
	StatusCheckRollup []struct {
		Conclusion string `json:"conclusion"`
		Status     string `json:"status"`
	} `json:"statusCheckRollup"`
	ReviewRequests []struct {
		Login string `json:"login"`
	} `json:"reviewRequests"`
	LatestReviews []struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State string `json:"state"`
	} `json:"latestReviews"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// hasLabel reports whether the PR already carries the given label.
func (p prSummary) hasLabel(name string) bool {
	for _, l := range p.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

func (p prSummary) hasLabels(names ...string) bool {
	for _, name := range names {
		if !p.hasLabel(name) {
			return false
		}
	}
	return true
}

// autoMergeAction describes what the daemon should do with a PR this cycle.
type autoMergeAction int

const (
	actionSkip             autoMergeAction = iota // not a merge-ready xylem PR, or skip for other reasons
	actionRequestReview                           // request copilot review, then continue with admin merge
	actionWaitForChecks                           // CI still running
	actionWaitForMergeable                        // unknown mergeable state (github computing)
	actionRouteConflict                           // conflicts — add labels so resolve-conflicts workflow picks it up
	actionAddressReview                           // changes requested; another workflow handles
	actionBlockedOptOut                           // explicit no-auto-admin-merge label
	actionAdminMerge                              // merge immediately with admin bypass
)

// decideAutoMergeAction returns the action to take for a given PR. It does
// not execute anything — it's pure reasoning over the PR state so it can be
// unit-tested.
//
// Decision order:
// 1. Non-xylem branch or not merge-ready → skip
// 2. Closed/merged → skip
// 3. Explicit no-auto-admin-merge label → stay blocked
// 4. Conflicts + missing resolve-conflicts labels → add labels (routeConflict)
// 5. Conflicts + labels already present → wait (workflow handles)
// 6. Unknown mergeable state → wait (github computing)
// 7. CI failing/running → wait (fix-pr-checks handles failures)
// 8. Changes requested → wait (respond-to-pr-review handles)
// 9. No copilot review requested or submitted → request review, then admin-merge
// 10. Otherwise admin-merge immediately
func decideAutoMergeAction(pr prSummary, settings autoMergeSettings) autoMergeAction {
	if !isMergeReadyPR(pr, settings) {
		return actionSkip
	}
	if pr.State != "OPEN" && pr.State != "" {
		return actionSkip
	}
	if pr.hasLabel(settings.optOutLabel) {
		return actionBlockedOptOut
	}
	// Mergeable state: MERGEABLE / CONFLICTING / UNKNOWN
	if pr.Mergeable == "CONFLICTING" {
		// If the PR already has the labels that trigger resolve-conflicts
		// workflow, just wait — the workflow is (or will be) processing it.
		// Otherwise, add the labels so the workflow picks it up.
		if pr.hasLabels(settings.conflictResolutionLabels...) {
			return actionWaitForMergeable
		}
		return actionRouteConflict
	}
	if pr.Mergeable != "MERGEABLE" {
		// UNKNOWN: GitHub hasn't computed yet — wait.
		return actionWaitForMergeable
	}
	if !allChecksGreen(pr) {
		return actionWaitForChecks
	}
	if pr.ReviewDecision == "CHANGES_REQUESTED" {
		return actionAddressReview
	}
	if settings.reviewer != "" && !reviewRequestedOrSubmitted(pr, settings.reviewer) {
		return actionRequestReview
	}
	return actionAdminMerge
}

func isMergeReadyPR(pr prSummary, settings autoMergeSettings) bool {
	return settings.branchPattern.MatchString(pr.HeadRefName) &&
		pr.hasLabels(settings.labels...)
}

// reviewRequestedOrSubmitted returns true if the configured reviewer has either
// been requested as a reviewer or has already submitted a review.
func reviewRequestedOrSubmitted(pr prSummary, reviewer string) bool {
	for _, r := range pr.ReviewRequests {
		if r.Login == reviewer {
			return true
		}
	}
	for _, r := range pr.LatestReviews {
		if r.Author.Login == reviewer {
			return true
		}
	}
	return false
}

// allChecksGreen returns true if every check in the rollup has completed with
// SUCCESS, NEUTRAL, or SKIPPED. If there are zero checks, returns true.
// Returns false if any check is failing or still running.
func allChecksGreen(pr prSummary) bool {
	for _, c := range pr.StatusCheckRollup {
		if c.Status != "COMPLETED" {
			return false
		}
		if c.Conclusion != "SUCCESS" && c.Conclusion != "NEUTRAL" && c.Conclusion != "SKIPPED" {
			return false
		}
	}
	return true
}

// autoMergeXylemPRs runs one cycle of the auto-merge loop. For each open PR
// it decides the appropriate action: request copilot review, wait for an
// in-progress review/CI/conflict, or enable GitHub auto-merge.
//
// The existing `respond-to-pr-review`, `fix-pr-checks`, and
// `resolve-conflicts` workflows handle the intermediate steps via the
// `github-pr-events` source, so auto-merge only needs to (1) kick off the
// review cycle and (2) admin-merge trusted vessel PRs once deterministic
// merge safety checks pass.
//
// The repo slug comes from daemon.auto_merge_repo. If empty, gh uses the
// current directory's origin remote.
func autoMergeXylemPRs(ctx context.Context, dc config.DaemonConfig) {
	settings, err := newAutoMergeSettings(dc)
	if err != nil {
		slog.Error("daemon auto-merge disabled by invalid configuration", "error", err)
		return
	}
	repo := settings.repo
	prs, err := listOpenPRsFn(ctx, repo)
	if err != nil {
		slog.Error("daemon auto-merge failed to list PRs", "repo", repo, "error", err)
		return
	}

	for _, pr := range prs {
		if !isMergeReadyPR(pr, settings) {
			continue
		}
		detailedPR, err := getPRSummaryFn(ctx, repo, pr.Number)
		if err != nil {
			slog.Error("daemon auto-merge failed to inspect PR",
				"repo", repo,
				"pr", pr.Number,
				"error", err)
			continue
		}
		pr = detailedPR
		action := decideAutoMergeAction(pr, settings)
		switch action {
		case actionSkip:
			continue
		case actionRequestReview:
			if err := requestCopilotReviewFn(ctx, repo, pr.Number, settings.reviewer); err != nil {
				if isBenignGhWarning(err) {
					slog.Info("daemon auto-merge requested copilot review with gh warning ignored",
						"repo", repo,
						"pr", pr.Number,
						"head_ref", pr.HeadRefName,
						"error", err)
				} else if isReviewerNotCollaborator(err) {
					slog.Warn("daemon auto-merge skipping copilot review request; reviewer is not a collaborator",
						"repo", repo,
						"pr", pr.Number,
						"reviewer", settings.reviewer,
						"error", err)
				} else {
					slog.Warn("daemon auto-merge request review failed; continuing with admin-merge attempt",
						"repo", repo,
						"pr", pr.Number,
						"error", err)
				}
			} else {
				slog.Info("daemon auto-merge requested copilot review",
					"repo", repo,
					"pr", pr.Number,
					"head_ref", pr.HeadRefName)
			}
			if err := adminMergePRFn(ctx, repo, pr.Number); err != nil {
				slog.Error("daemon auto-merge failed to admin-merge PR",
					"repo", repo,
					"pr", pr.Number,
					"error", err)
				continue
			}
			slog.Info("daemon auto-merge merged PR with admin bypass",
				"repo", repo,
				"pr", pr.Number,
				"head_ref", pr.HeadRefName)
		case actionRouteConflict:
			if err := addPRLabelsFn(ctx, repo, pr.Number, settings.conflictResolutionLabels); err != nil {
				if isBenignGhWarning(err) {
					slog.Info("daemon auto-merge routed PR to resolve-conflicts workflow with gh warning ignored",
						"repo", repo,
						"pr", pr.Number,
						"error", err)
					continue
				}
				slog.Error("daemon auto-merge failed to add conflict labels",
					"repo", repo,
					"pr", pr.Number,
					"error", err)
				continue
			}
			slog.Info("daemon auto-merge routed PR to resolve-conflicts workflow",
				"repo", repo,
				"pr", pr.Number,
				"head_ref", pr.HeadRefName)
		case actionWaitForChecks:
			slog.Info("daemon auto-merge waiting for CI checks", "repo", repo, "pr", pr.Number)
		case actionWaitForMergeable:
			slog.Info("daemon auto-merge waiting for mergeable state",
				"repo", repo,
				"pr", pr.Number)
		case actionAddressReview:
			slog.Info("daemon auto-merge waiting for review follow-up",
				"repo", repo,
				"pr", pr.Number)
		case actionBlockedOptOut:
			slog.Info("daemon auto-merge blocked by opt-out label",
				"repo", repo,
				"pr", pr.Number,
				"label", settings.optOutLabel)
		case actionAdminMerge:
			if err := adminMergePRFn(ctx, repo, pr.Number); err != nil {
				slog.Error("daemon auto-merge failed to admin-merge PR",
					"repo", repo,
					"pr", pr.Number,
					"error", err)
				continue
			}
			slog.Info("daemon auto-merge merged PR with admin bypass",
				"repo", repo,
				"pr", pr.Number,
				"head_ref", pr.HeadRefName)
		}
	}
}

var (
	listOpenPRsFn          = listOpenPRs
	getPRSummaryFn         = getPRSummary
	requestCopilotReviewFn = requestCopilotReview
	addPRLabelsFn          = addPRLabels
	adminMergePRFn         = adminMergePR
)

func listOpenPRs(ctx context.Context, repo string) ([]prSummary, error) {
	args := []string{"pr", "list", "--state", "open", "--json", "number,headRefName,state,labels", "--limit", "50"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var prs []prSummary
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("decode gh pr list output: %w", err)
	}
	return prs, nil
}

func getPRSummary(ctx context.Context, repo string, number int) (prSummary, error) {
	args := []string{
		"pr", "view", strconv.Itoa(number),
		"--json", "number,headRefName,mergeable,state,reviewDecision,statusCheckRollup,reviewRequests,latestReviews,labels",
	}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.Output()
	if err != nil {
		return prSummary{}, fmt.Errorf("gh pr view %d: %w", number, err)
	}
	var pr prSummary
	if err := json.Unmarshal(out, &pr); err != nil {
		return prSummary{}, fmt.Errorf("decode gh pr view output for %d: %w", number, err)
	}
	return pr, nil
}

// addPRLabels adds the given labels to a PR via the GitHub REST API.
// Uses `gh api` directly (not `gh pr edit`) to avoid the GraphQL Projects
// deprecation warning that `gh pr edit` emits with a non-zero exit code.
func addPRLabels(ctx context.Context, repo string, number int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	slug := repo
	if slug == "" {
		return fmt.Errorf("addPRLabels: repo slug required")
	}
	body, err := json.Marshal(map[string][]string{"labels": labels})
	if err != nil {
		return fmt.Errorf("marshal label payload: %w", err)
	}
	args := []string{
		"api",
		"--method", "POST",
		fmt.Sprintf("repos/%s/issues/%d/labels", slug, number),
		"--input", "-",
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = strings.NewReader(string(body))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// requestCopilotReview adds the configured reviewer as a reviewer on the given
// PR. Uses the GitHub REST API directly (not `gh pr edit`) so we
// avoid the GraphQL Projects-deprecation warning that `gh pr edit` emits
// alongside a non-zero exit code even when the underlying operation succeeds.
func requestCopilotReview(ctx context.Context, repo string, number int, reviewer string) error {
	if repo == "" {
		return fmt.Errorf("requestCopilotReview: repo slug required")
	}
	if strings.TrimSpace(reviewer) == "" {
		return fmt.Errorf("requestCopilotReview: reviewer required")
	}
	body, err := json.Marshal(map[string][]string{"reviewers": {reviewer}})
	if err != nil {
		return fmt.Errorf("marshal reviewer payload: %w", err)
	}
	args := []string{
		"api",
		"--method", "POST",
		fmt.Sprintf("repos/%s/pulls/%d/requested_reviewers", repo, number),
		"--input", "-",
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = strings.NewReader(string(body))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func adminMergePR(ctx context.Context, repo string, number int) error {
	args := []string{"pr", "merge", "--admin", "--squash", "--delete-branch"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	args = append(args, strconv.Itoa(number))
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}
