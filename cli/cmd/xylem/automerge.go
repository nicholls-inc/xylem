package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// xylemBranchPattern matches branch names created by xylem vessels.
// Examples: feat/issue-42-42, fix/issue-99-99, feat/issue-60-60-runner-context
var xylemBranchPattern = regexp.MustCompile(`^(feat|fix|chore)/issue-\d+`)

// copilotReviewerLogin is the GitHub bot that performs automated code review.
const copilotReviewerLogin = "copilot-pull-request-reviewer"

// conflictResolutionLabels are the labels that trigger the resolve-conflicts
// workflow via the conflict-resolution github-pr source. Auto-merge adds
// these to any CONFLICTING xylem PR so the workflow picks it up.
var conflictResolutionLabels = []string{"needs-conflict-resolution", "harness-impl"}

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

// autoMergeAction describes what the daemon should do with a PR this cycle.
type autoMergeAction int

const (
	actionSkip             autoMergeAction = iota // not a xylem PR, or skip for other reasons
	actionRequestReview                           // no reviewer assigned; request copilot review
	actionWaitForReview                           // review requested but not yet complete
	actionWaitForChecks                           // CI still running
	actionWaitForMergeable                        // unknown mergeable state (github computing)
	actionRouteConflict                           // conflicts — add labels so resolve-conflicts workflow picks it up
	actionAddressReview                           // changes requested; another workflow handles
	actionMerge                                   // approved + green + mergeable
)

// decideAutoMergeAction returns the action to take for a given PR. It does
// not execute anything — it's pure reasoning over the PR state so it can be
// unit-tested.
//
// Decision order:
// 1. Non-xylem branch → skip
// 2. Closed/merged → skip
// 3. Conflicts + missing resolve-conflicts labels → add labels (routeConflict)
// 4. Conflicts + labels already present → wait (workflow handles)
// 5. Unknown mergeable state → wait (github computing)
// 6. CI failing/running → wait (fix-pr-checks handles failures)
// 7. Changes requested → wait (respond-to-pr-review handles)
// 8. No copilot review requested or submitted → request review
// 9. Review pending → wait
// 10. Approved + mergeable + green → merge
func decideAutoMergeAction(pr prSummary) autoMergeAction {
	if !xylemBranchPattern.MatchString(pr.HeadRefName) {
		return actionSkip
	}
	if pr.State != "OPEN" && pr.State != "" {
		return actionSkip
	}
	// Mergeable state: MERGEABLE / CONFLICTING / UNKNOWN
	if pr.Mergeable == "CONFLICTING" {
		// If the PR already has the labels that trigger resolve-conflicts
		// workflow, just wait — the workflow is (or will be) processing it.
		// Otherwise, add the labels so the workflow picks it up.
		if pr.hasLabel("needs-conflict-resolution") && pr.hasLabel("harness-impl") {
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
	if pr.ReviewDecision == "APPROVED" {
		return actionMerge
	}
	// No decision yet (REVIEW_REQUIRED or empty): check if copilot has been
	// asked to review. If not, request it.
	if !copilotReviewRequestedOrSubmitted(pr) {
		return actionRequestReview
	}
	return actionWaitForReview
}

// copilotReviewRequestedOrSubmitted returns true if copilot has either been
// requested as a reviewer or has already submitted a review.
func copilotReviewRequestedOrSubmitted(pr prSummary) bool {
	for _, r := range pr.ReviewRequests {
		if r.Login == copilotReviewerLogin {
			return true
		}
	}
	for _, r := range pr.LatestReviews {
		if r.Author.Login == copilotReviewerLogin {
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
// in-progress review/CI/conflict, or merge.
//
// The existing `respond-to-pr-review`, `fix-pr-checks`, and
// `resolve-conflicts` workflows handle the intermediate steps via the
// `github-pr-events` source, so auto-merge only needs to (1) kick off the
// review cycle and (2) merge when everything is green.
//
// Repo is the GitHub repo slug (e.g., "owner/name"). If empty, gh uses the
// current directory's origin remote.
func autoMergeXylemPRs(ctx context.Context, repo string) {
	prs, err := listOpenPRs(ctx, repo)
	if err != nil {
		log.Printf("daemon: auto-merge: list PRs: %v", err)
		return
	}

	for _, pr := range prs {
		action := decideAutoMergeAction(pr)
		switch action {
		case actionSkip:
			continue
		case actionRequestReview:
			if err := requestCopilotReview(ctx, repo, pr.Number); err != nil {
				if isBenignGhWarning(err) {
					log.Printf("daemon: auto-merge: requested copilot review on PR #%d (gh warning ignored): %s", pr.Number, pr.HeadRefName)
					continue
				}
				log.Printf("daemon: auto-merge: PR #%d request review failed: %v", pr.Number, err)
				continue
			}
			log.Printf("daemon: auto-merge: requested copilot review on PR #%d (%s)", pr.Number, pr.HeadRefName)
		case actionRouteConflict:
			if err := addPRLabels(ctx, repo, pr.Number, conflictResolutionLabels); err != nil {
				if isBenignGhWarning(err) {
					log.Printf("daemon: auto-merge: routed PR #%d to resolve-conflicts workflow (gh warning ignored)", pr.Number)
					continue
				}
				log.Printf("daemon: auto-merge: PR #%d add conflict labels failed: %v", pr.Number, err)
				continue
			}
			log.Printf("daemon: auto-merge: routed PR #%d to resolve-conflicts workflow (%s)", pr.Number, pr.HeadRefName)
		case actionWaitForReview:
			log.Printf("daemon: auto-merge: PR #%d waiting for copilot review to complete", pr.Number)
		case actionWaitForChecks:
			log.Printf("daemon: auto-merge: PR #%d waiting for CI checks", pr.Number)
		case actionWaitForMergeable:
			log.Printf("daemon: auto-merge: PR #%d waiting for mergeable state (conflicts being resolved or computing)", pr.Number)
		case actionAddressReview:
			log.Printf("daemon: auto-merge: PR #%d has changes requested, respond-to-pr-review workflow will handle", pr.Number)
		case actionMerge:
			if err := mergePR(ctx, repo, pr.Number); err != nil {
				log.Printf("daemon: auto-merge: PR #%d merge failed: %v", pr.Number, err)
				continue
			}
			log.Printf("daemon: auto-merge: merged PR #%d (%s)", pr.Number, pr.HeadRefName)
		}
	}
}

func listOpenPRs(ctx context.Context, repo string) ([]prSummary, error) {
	args := []string{"pr", "list", "--state", "open", "--json",
		"number,headRefName,mergeable,state,reviewDecision,statusCheckRollup,reviewRequests,latestReviews,labels",
		"--limit", "50"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var prs []prSummary
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, err
	}
	return prs, nil
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

// requestCopilotReview adds copilot-pull-request-reviewer as a reviewer on
// the given PR. Uses the GitHub REST API directly (not `gh pr edit`) so we
// avoid the GraphQL Projects-deprecation warning that `gh pr edit` emits
// alongside a non-zero exit code even when the underlying operation succeeds.
func requestCopilotReview(ctx context.Context, repo string, number int) error {
	if repo == "" {
		return fmt.Errorf("requestCopilotReview: repo slug required")
	}
	body, err := json.Marshal(map[string][]string{"reviewers": {copilotReviewerLogin}})
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

func mergePR(ctx context.Context, repo string, number int) error {
	args := []string{"pr", "merge", "--squash", "--admin", "--delete-branch"}
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
