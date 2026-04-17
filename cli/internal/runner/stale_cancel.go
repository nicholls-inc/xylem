package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// ghCallTimeout is the maximum time allowed for a single gh CLI call in the
// daemon's hot paths (scan tick, check tick, stale-cancel tick). Overridable
// in tests. A hung gh process can no longer freeze the daemon indefinitely.
var ghCallTimeout = 60 * time.Second

// prRefPattern matches GitHub PR URLs and extracts the PR number.
var prRefPattern = regexp.MustCompile(`/pull/(\d+)`)

// prSources are the vessel source types that reference pull requests and should
// be cancelled when their PR is already merged or closed. github-merge is
// intentionally excluded: those vessels are triggered *by* a PR merge, so a
// merged PR is their entry condition, not an obsolescence signal.
var prSources = map[string]bool{
	"github-pr":        true,
	"github-pr-events": true,
}

// CancelStalePRVessels checks pending and waiting vessels that reference pull
// requests and cancels those whose PRs are already merged or closed. This
// prevents wasting concurrency slots on work that can never succeed (e.g.,
// merging an already-merged PR, resolving conflicts on a closed PR) and
// releases waiting vessels that are polling for a label on a PR that will
// never receive it. Vessels from the github-merge source are excluded because
// a merged PR is their trigger, not an obsolescence signal.
//
// Returns the number of vessels cancelled.
func (r *Runner) CancelStalePRVessels(ctx context.Context) int {
	pending, err := r.Queue.ListByState(queue.StatePending)
	if err != nil {
		log.Printf("warn: cancel stale PR vessels: list pending: %v", err)
		return 0
	}
	waiting, err := r.Queue.ListByState(queue.StateWaiting)
	if err != nil {
		log.Printf("warn: cancel stale PR vessels: list waiting: %v", err)
		return 0
	}

	candidates := make([]queue.Vessel, 0, len(pending)+len(waiting))
	candidates = append(candidates, pending...)
	candidates = append(candidates, waiting...)

	cancelled := 0
	for _, vessel := range candidates {
		if !prSources[vessel.Source] {
			continue
		}

		prNum := extractPRNumber(vessel)
		if prNum == 0 {
			continue
		}

		repo := r.resolveRepo(vessel)
		if repo == "" {
			continue
		}

		state, err := r.checkPRState(ctx, repo, prNum)
		if err != nil {
			log.Printf("warn: cancel stale PR vessels: check PR %d state: %v", prNum, err)
			continue
		}

		if state == "OPEN" {
			continue
		}

		reason := fmt.Sprintf("PR #%d is %s", prNum, strings.ToLower(state))
		log.Printf("cancel stale vessel %s: %s", vessel.ID, reason)
		if err := r.Queue.Cancel(vessel.ID); err != nil {
			log.Printf("warn: cancel stale vessel %s: %v", vessel.ID, err)
			continue
		}
		cancelled++
	}

	if cancelled > 0 {
		log.Printf("cancelled %d stale PR vessel(s)", cancelled)
	}
	return cancelled
}

// extractPRNumber gets the PR number from a vessel's metadata or ref URL.
func extractPRNumber(v queue.Vessel) int {
	if num, ok := v.Meta["pr_num"]; ok {
		if n, err := strconv.Atoi(num); err == nil {
			return n
		}
	}
	matches := prRefPattern.FindStringSubmatch(v.Ref)
	if len(matches) >= 2 {
		if n, err := strconv.Atoi(matches[1]); err == nil {
			return n
		}
	}
	return 0
}

// checkPRState queries the GitHub API for a PR's state.
// Returns "OPEN", "MERGED", or "CLOSED".
func (r *Runner) checkPRState(ctx context.Context, repo string, prNum int) (string, error) {
	ghCtx, cancel := context.WithTimeout(ctx, ghCallTimeout)
	defer cancel()
	out, err := r.Runner.RunOutput(ghCtx, "gh", "pr", "view",
		strconv.Itoa(prNum),
		"--repo", repo,
		"--json", "state",
	)
	if err != nil {
		return "", fmt.Errorf("gh pr view %d: %w", prNum, err)
	}

	var resp struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("parse PR state: %w", err)
	}
	return resp.State, nil
}
