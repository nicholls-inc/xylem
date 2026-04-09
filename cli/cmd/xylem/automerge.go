package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
)

// xylemBranchPattern matches branch names created by xylem vessels.
// Examples: feat/issue-42-42, fix/issue-99-99, feat/issue-60-60-runner-context
var xylemBranchPattern = regexp.MustCompile(`^(feat|fix|chore)/issue-\d+`)

// copilotReviewerLogin is the GitHub bot that performs automated code review.
const copilotReviewerLogin = "copilot-pull-request-reviewer"

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
}

// autoMergeAction describes what the daemon should do with a PR this cycle.
type autoMergeAction int

const (
	actionSkip             autoMergeAction = iota // not a xylem PR, or skip for other reasons
	actionRequestReview                           // no reviewer assigned; request copilot review
	actionWaitForReview                           // review requested but not yet complete
	actionWaitForChecks                           // CI still running
	actionWaitForMergeable                        // conflicts or unknown mergeable state
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
// 3. Mergeable conflicts → wait (resolve-conflicts workflow handles)
// 4. CI failing/running → wait (fix-pr-checks handles failures)
// 5. Changes requested → wait (respond-to-pr-review handles)
// 6. No copilot review requested or submitted → request review
// 7. Review pending → wait
// 8. Approved + mergeable + green → merge
func decideAutoMergeAction(pr prSummary) autoMergeAction {
	if !xylemBranchPattern.MatchString(pr.HeadRefName) {
		return actionSkip
	}
	if pr.State != "OPEN" && pr.State != "" {
		return actionSkip
	}
	// Mergeable state: MERGEABLE / CONFLICTING / UNKNOWN
	if pr.Mergeable == "CONFLICTING" {
		return actionWaitForMergeable
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
		slog.Error("daemon auto-merge failed to list PRs", "repo", repo, "error", err)
		return
	}

	for _, pr := range prs {
		action := decideAutoMergeAction(pr)
		switch action {
		case actionSkip:
			continue
		case actionRequestReview:
			if err := requestCopilotReview(ctx, repo, pr.Number); err != nil {
				slog.Error("daemon auto-merge failed to request review", "repo", repo, "pr", pr.Number, "error", err)
				continue
			}
			slog.Info("daemon auto-merge requested copilot review", "repo", repo, "pr", pr.Number, "head_ref", pr.HeadRefName)
		case actionWaitForReview:
			slog.Info("daemon auto-merge waiting for copilot review", "repo", repo, "pr", pr.Number)
		case actionWaitForChecks:
			slog.Info("daemon auto-merge waiting for CI checks", "repo", repo, "pr", pr.Number)
		case actionWaitForMergeable:
			slog.Info("daemon auto-merge waiting for mergeable state", "repo", repo, "pr", pr.Number)
		case actionAddressReview:
			slog.Info("daemon auto-merge waiting for review follow-up", "repo", repo, "pr", pr.Number)
		case actionMerge:
			if err := mergePR(ctx, repo, pr.Number); err != nil {
				slog.Error("daemon auto-merge failed to merge PR", "repo", repo, "pr", pr.Number, "error", err)
				continue
			}
			slog.Info("daemon auto-merge merged PR", "repo", repo, "pr", pr.Number, "head_ref", pr.HeadRefName)
		}
	}
}

func listOpenPRs(ctx context.Context, repo string) ([]prSummary, error) {
	args := []string{"pr", "list", "--state", "open", "--json",
		"number,headRefName,mergeable,state,reviewDecision,statusCheckRollup,reviewRequests,latestReviews",
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

// requestCopilotReview adds copilot-pull-request-reviewer as a reviewer on
// the given PR. Uses the gh api to POST to the requested_reviewers endpoint.
func requestCopilotReview(ctx context.Context, repo string, number int) error {
	// gh pr edit --add-reviewer copilot-pull-request-reviewer
	args := []string{"pr", "edit", strconv.Itoa(number), "--add-reviewer", copilotReviewerLogin}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
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
