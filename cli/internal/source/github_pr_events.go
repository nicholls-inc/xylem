package source

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// PREventsTask defines a task triggered by PR events.
type PREventsTask struct {
	Workflow        string
	Labels          []string
	ReviewSubmitted bool
	ChecksFailed    bool
	Commented       bool
	PROpened        bool
	PRHeadUpdated   bool
	// AuthorAllow restricts authored events (reviews, comments) to the
	// listed GitHub logins. Empty slice = no allowlist.
	AuthorAllow []string
	// AuthorDeny skips authored events from these GitHub logins.
	// AuthorDeny takes precedence over AuthorAllow.
	AuthorDeny []string
	Debounce   time.Duration
}

// GitHubPREvents scans GitHub PRs for specific events and produces vessels.
type GitHubPREvents struct {
	Repo      string
	Tasks     map[string]PREventsTask
	Exclude   []string
	StateDir  string
	Queue     *queue.Queue
	CmdRunner CommandRunner
	Now       func() time.Time

	// selfLogin is the authenticated gh CLI user's login, looked up once
	// per Scan. Used to unconditionally filter out events authored by
	// xylem itself, preventing self-trigger loops even if AuthorAllow /
	// AuthorDeny are misconfigured. Empty string means unresolved or
	// the lookup failed.
	selfLogin         string
	selfLoginResolved bool
}

// ghAuthoredEvent is a minimal shape emitted by gh api --jq for reviews
// and issue comments. Login is empty when the GitHub user is null (rare,
// e.g. a deleted account). Both empty Login and known Login flow through
// the same filter rules.
type ghAuthoredEvent struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	// Type is the GitHub user type ("Bot" or "User"). Not used today;
	// reserved for future filtering (e.g. "all bots" allowlist).
	Type string `json:"type"`
}

// resolveSelfLogin returns the authenticated gh user's login, looked up
// at most once per Scan cycle. On failure it returns "" and disables the
// self-filter for that scan; the mandatory author_allow / author_deny
// config (see config.validateGitHubPREventsSource) is what actually
// prevents loops, so a lookup failure is non-fatal.
func (g *GitHubPREvents) resolveSelfLogin(ctx context.Context) string {
	if g.selfLoginResolved {
		return g.selfLogin
	}
	g.selfLoginResolved = true
	out, err := g.CmdRunner.Run(ctx, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		log.Printf("warn: github-pr-events: gh api user failed, self-filter disabled this scan: %v", err)
		return ""
	}
	g.selfLogin = strings.TrimSpace(string(out))
	return g.selfLogin
}

// shouldSkipAuthoredEvent returns true if a review/comment authored by
// login should not spawn a vessel for this task. Rules (earlier wins):
//
//  1. login == selfLogin (non-empty) → skip. INVARIANT: the self-filter
//     cannot be overridden by AuthorAllow; it is always-on.
//  2. login in task.AuthorDeny → skip. Deny wins over allow.
//  3. len(task.AuthorAllow) > 0 and login not in AuthorAllow → skip.
//  4. Otherwise keep.
//
// An empty login (user: null from the API) passes rule 1 but fails rule 3
// whenever an allowlist is set — which is mandatory for authored-event
// triggers per config validation.
func shouldSkipAuthoredEvent(login, selfLogin string, task PREventsTask) bool {
	if selfLogin != "" && login == selfLogin {
		return true
	}
	for _, deny := range task.AuthorDeny {
		if login == deny {
			return true
		}
	}
	if len(task.AuthorAllow) > 0 {
		for _, allow := range task.AuthorAllow {
			if login == allow {
				return false
			}
		}
		return true
	}
	return false
}

// parseAuthoredEvents decodes the JSON-lines output emitted by
// `gh api ... --jq '.[] | {id, login, type}'`. Malformed lines are
// skipped with a warning (fail-soft so a single bad record does not
// block the entire scan).
func parseAuthoredEvents(raw []byte) []ghAuthoredEvent {
	var out []ghAuthoredEvent
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev ghAuthoredEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			log.Printf("warn: github-pr-events: skipping malformed authored event line: %v", err)
			continue
		}
		out = append(out, ev)
	}
	return out
}

func (g *GitHubPREvents) Name() string { return "github-pr-events" }

func (g *GitHubPREvents) Scan(ctx context.Context) ([]queue.Vessel, error) {
	excludeSet := make(map[string]bool, len(g.Exclude))
	for _, ex := range g.Exclude {
		excludeSet[ex] = true
	}

	g.selfLogin = ""
	g.selfLoginResolved = false

	// List open PRs
	args := []string{
		"pr", "list",
		"--repo", g.Repo,
		"--state", "open",
		"--json", "number,title,url,labels,headRefName",
		"--limit", "50",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}

	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}

	var vessels []queue.Vessel

	for _, pr := range prs {
		if g.hasExcludedLabel(pr, excludeSet) {
			continue
		}

		for _, task := range g.Tasks {
			// Label triggers
			if len(task.Labels) > 0 {
				prLabels := make(map[string]bool, len(pr.Labels))
				for _, l := range pr.Labels {
					prLabels[l.Name] = true
				}
				for _, triggerLabel := range task.Labels {
					if prLabels[triggerLabel] {
						ref := fmt.Sprintf("%s#label-%s", pr.URL, triggerLabel)
						if !g.Queue.HasRefAny(ref) {
							vessels = append(vessels, queue.Vessel{
								ID:       fmt.Sprintf("pr-%d-label-%s", pr.Number, triggerLabel),
								Source:   "github-pr-events",
								Ref:      ref,
								Workflow: task.Workflow,
								Meta: map[string]string{
									"pr_num":         strconv.Itoa(pr.Number),
									"event_type":     "label",
									"pr_head_branch": pr.HeadRefName,
								},
								State:     queue.StatePending,
								CreatedAt: sourceNow(),
							})
						}
					}
				}
			}

			if task.PROpened {
				v, err := g.scanPROpened(pr, task)
				if err != nil {
					return vessels, err
				}
				vessels = append(vessels, v...)
			}

			if task.PRHeadUpdated {
				v, err := g.scanPRHeadUpdated(ctx, pr, task)
				if err != nil {
					return vessels, err
				}
				vessels = append(vessels, v...)
			}

			// Review submitted trigger
			if task.ReviewSubmitted {
				v, err := g.scanReviews(ctx, pr, task)
				if err != nil {
					return vessels, err
				}
				vessels = append(vessels, v...)
			}

			// Checks failed trigger
			if task.ChecksFailed {
				v, err := g.scanChecksFailed(ctx, pr, task)
				if err != nil {
					return vessels, err
				}
				vessels = append(vessels, v...)
			}

			// Comment trigger
			if task.Commented {
				v, err := g.scanComments(ctx, pr, task)
				if err != nil {
					return vessels, err
				}
				vessels = append(vessels, v...)
			}
		}
	}

	return vessels, nil
}

func (g *GitHubPREvents) scanPROpened(pr ghPR, task PREventsTask) ([]queue.Vessel, error) {
	blocked, err := g.shouldDebounceTrigger(preventPROpened, pr.Number, task)
	if err != nil {
		return nil, fmt.Errorf("debounce pr_opened for PR %d: %w", pr.Number, err)
	}
	if blocked {
		return nil, nil
	}

	ref := fmt.Sprintf("%s#pr-opened", pr.URL)
	if g.Queue.HasRefAny(ref) {
		return nil, nil
	}

	return []queue.Vessel{
		g.newPREventVessel(pr, task, preventPROpened, fmt.Sprintf("pr-%d-pr-opened", pr.Number), ref, nil),
	}, nil
}

func (g *GitHubPREvents) scanPRHeadUpdated(ctx context.Context, pr ghPR, task PREventsTask) ([]queue.Vessel, error) {
	blocked, err := g.shouldDebounceTrigger(preventPRHeadUpdated, pr.Number, task)
	if err != nil {
		return nil, fmt.Errorf("debounce pr_head_updated for PR %d: %w", pr.Number, err)
	}
	if blocked {
		return nil, nil
	}

	headOID, err := g.loadPRHeadOID(ctx, pr.Number)
	if err != nil {
		return nil, nil
	}
	if headOID == "" {
		return nil, nil
	}

	ref := fmt.Sprintf("%s#head-%s", pr.URL, headOID)
	if g.Queue.HasRefAny(ref) {
		return nil, nil
	}

	return []queue.Vessel{
		g.newPREventVessel(pr, task, preventPRHeadUpdated, fmt.Sprintf("pr-%d-head-%s", pr.Number, headOID[:minLen(len(headOID), 8)]), ref, map[string]string{
			"pr_head_sha": headOID,
		}),
	}, nil
}

func (g *GitHubPREvents) scanReviews(ctx context.Context, pr ghPR, task PREventsTask) ([]queue.Vessel, error) {
	blocked, err := g.shouldDebounceTrigger(preventReviewSubmitted, pr.Number, task)
	if err != nil {
		return nil, fmt.Errorf("debounce review_submitted for PR %d: %w", pr.Number, err)
	}
	if blocked {
		return nil, nil
	}

	args := []string{
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", g.Repo, pr.Number),
		"--jq", `.[] | {id: .id, login: .user.login, type: .user.type}`,
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, nil // non-fatal: skip reviews on error
	}

	selfLogin := g.resolveSelfLogin(ctx)
	events := parseAuthoredEvents(out)
	if debounceCollapsesEvents(task, preventReviewSubmitted) {
		events = newestEligibleAuthoredEvents(events, func(ev ghAuthoredEvent) bool {
			if ev.ID == 0 || shouldSkipAuthoredEvent(ev.Login, selfLogin, task) {
				return false
			}
			ref := fmt.Sprintf("%s#review-%d", pr.URL, ev.ID)
			return !g.Queue.HasRefAny(ref)
		})
	}

	var vessels []queue.Vessel
	for _, ev := range events {
		if ev.ID == 0 {
			continue
		}
		if shouldSkipAuthoredEvent(ev.Login, selfLogin, task) {
			continue
		}
		reviewID := strconv.FormatInt(ev.ID, 10)
		ref := fmt.Sprintf("%s#review-%s", pr.URL, reviewID)
		if g.Queue.HasRefAny(ref) {
			continue
		}
		vessels = append(vessels, g.newPREventVessel(pr, task, preventReviewSubmitted, fmt.Sprintf("pr-%d-review-%s", pr.Number, reviewID), ref, map[string]string{
			"review_author": ev.Login,
		}))
	}
	return vessels, nil
}

type ghCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func (g *GitHubPREvents) scanChecksFailed(ctx context.Context, pr ghPR, task PREventsTask) ([]queue.Vessel, error) {
	blocked, err := g.shouldDebounceTrigger(preventChecksFailed, pr.Number, task)
	if err != nil {
		return nil, fmt.Errorf("debounce checks_failed for PR %d: %w", pr.Number, err)
	}
	if blocked {
		return nil, nil
	}

	args := []string{
		"pr", "checks", strconv.Itoa(pr.Number),
		"--repo", g.Repo,
		"--json", "name,state",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, nil // non-fatal
	}

	var checks []ghCheck
	if err := json.Unmarshal(out, &checks); err != nil {
		return nil, nil // non-fatal
	}

	hasFailed := false
	for _, c := range checks {
		if c.State == "FAILURE" || c.State == "ERROR" {
			hasFailed = true
			break
		}
	}

	if !hasFailed {
		return nil, nil
	}

	sha, err := g.loadPRHeadOID(ctx, pr.Number)
	if err != nil {
		return nil, nil // non-fatal
	}
	if sha == "" {
		return nil, nil
	}

	ref := fmt.Sprintf("%s#checks-failed-%s", pr.URL, sha)
	if g.Queue.HasRefAny(ref) {
		return nil, nil
	}

	return []queue.Vessel{
		g.newPREventVessel(pr, task, preventChecksFailed, fmt.Sprintf("pr-%d-checks-failed-%s", pr.Number, sha[:minLen(len(sha), 8)]), ref, map[string]string{
			"pr_head_sha": sha,
		}),
	}, nil
}

func (g *GitHubPREvents) scanComments(ctx context.Context, pr ghPR, task PREventsTask) ([]queue.Vessel, error) {
	blocked, err := g.shouldDebounceTrigger(preventCommented, pr.Number, task)
	if err != nil {
		return nil, fmt.Errorf("debounce commented for PR %d: %w", pr.Number, err)
	}
	if blocked {
		return nil, nil
	}

	args := []string{
		"api",
		fmt.Sprintf("repos/%s/issues/%d/comments", g.Repo, pr.Number),
		"--jq", `.[] | {id: .id, login: .user.login, type: .user.type}`,
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, nil // non-fatal
	}

	selfLogin := g.resolveSelfLogin(ctx)
	events := parseAuthoredEvents(out)
	if debounceCollapsesEvents(task, preventCommented) {
		events = newestEligibleAuthoredEvents(events, func(ev ghAuthoredEvent) bool {
			if ev.ID == 0 || shouldSkipAuthoredEvent(ev.Login, selfLogin, task) {
				return false
			}
			ref := fmt.Sprintf("%s#comment-%d", pr.URL, ev.ID)
			return !g.Queue.HasRefAny(ref)
		})
	}

	var vessels []queue.Vessel
	for _, ev := range events {
		if ev.ID == 0 {
			continue
		}
		if shouldSkipAuthoredEvent(ev.Login, selfLogin, task) {
			continue
		}
		commentID := strconv.FormatInt(ev.ID, 10)
		ref := fmt.Sprintf("%s#comment-%s", pr.URL, commentID)
		if g.Queue.HasRefAny(ref) {
			continue
		}
		vessels = append(vessels, g.newPREventVessel(pr, task, preventCommented, fmt.Sprintf("pr-%d-comment-%s", pr.Number, commentID), ref, map[string]string{
			"comment_author": ev.Login,
		}))
	}
	return vessels, nil
}

func (g *GitHubPREvents) OnEnqueue(_ context.Context, vessel queue.Vessel) error {
	return g.persistDebounce(vessel)
}
func (g *GitHubPREvents) OnStart(_ context.Context, _ queue.Vessel) error            { return nil }
func (g *GitHubPREvents) OnWait(_ context.Context, _ queue.Vessel) error             { return nil }
func (g *GitHubPREvents) OnResume(_ context.Context, _ queue.Vessel) error           { return nil }
func (g *GitHubPREvents) OnComplete(_ context.Context, _ queue.Vessel) error         { return nil }
func (g *GitHubPREvents) OnFail(_ context.Context, _ queue.Vessel) error             { return nil }
func (g *GitHubPREvents) OnTimedOut(_ context.Context, _ queue.Vessel) error         { return nil }
func (g *GitHubPREvents) RemoveRunningLabel(_ context.Context, _ queue.Vessel) error { return nil }

func (g *GitHubPREvents) BranchName(vessel queue.Vessel) string {
	prNum := vessel.Meta["pr_num"]
	eventType := vessel.Meta["event_type"]
	slug := slugify(vessel.Ref)
	return fmt.Sprintf("event/pr-%s-%s-%s", prNum, eventType, slug)
}

func (g *GitHubPREvents) hasExcludedLabel(pr ghPR, excluded map[string]bool) bool {
	for _, l := range pr.Labels {
		if excluded[l.Name] {
			return true
		}
	}
	return false
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (g *GitHubPREvents) loadPRHeadOID(ctx context.Context, prNumber int) (string, error) {
	args := []string{
		"pr", "view", strconv.Itoa(prNumber),
		"--repo", g.Repo,
		"--json", "headRefOid",
		"--jq", ".headRefOid",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr view headRefOid for PR %d: %w", prNumber, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *GitHubPREvents) newPREventVessel(pr ghPR, task PREventsTask, eventType, id, ref string, extraMeta map[string]string) queue.Vessel {
	createdAt := g.now()
	meta := map[string]string{
		"pr_num":         strconv.Itoa(pr.Number),
		"event_type":     eventType,
		"pr_head_branch": pr.HeadRefName,
	}
	for key, value := range extraMeta {
		meta[key] = value
	}
	g.markDebounceMeta(meta, eventType, pr.Number, task, createdAt)
	return queue.Vessel{
		ID:        id,
		Source:    g.Name(),
		Ref:       ref,
		Workflow:  task.Workflow,
		Meta:      meta,
		State:     queue.StatePending,
		CreatedAt: createdAt,
	}
}

func newestEligibleAuthoredEvents(events []ghAuthoredEvent, keep func(ghAuthoredEvent) bool) []ghAuthoredEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if keep(events[i]) {
			return []ghAuthoredEvent{events[i]}
		}
	}
	return nil
}

func (g *GitHubPREvents) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC().Truncate(time.Second)
	}
	return sourceNow().UTC().Truncate(time.Second)
}
