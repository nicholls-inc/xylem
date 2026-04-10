package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	HarnessGapAnalysisWorkflow       = "harness-gap-analysis"
	harnessGapReportJSONName         = "harness-gap-analysis.json"
	harnessGapReportMarkdownName     = "harness-gap-analysis.md"
	harnessGapIssueStateName         = "harness-gap-analysis-issues.json"
	harnessGapFindingMarkerPrefix    = "<!-- xylem:harness-gap-fingerprint="
	harnessGapMergedPRLookback       = 24 * time.Hour
	harnessGapMergedPRThreshold      = 3
	harnessGapStaleConflictThreshold = 1
	harnessGapRestartThreshold       = 1
	harnessGapIdleBacklogThreshold   = 15 * time.Minute
	harnessGapReleasePRAgeThreshold  = 7 * 24 * time.Hour
	harnessGapReleasePRCommitMinimum = 5
	harnessGapFailedBacklogThreshold = 3
	harnessGapLocalSignalsLookback   = 24 * time.Hour
	harnessGapDaemonLogFileName      = "daemon.log"
)

var harnessGapIssueBranchPattern = regexp.MustCompile(`^(feat|fix|chore)/issue-\d+`)

type HarnessGapOptions struct {
	OutputDir string
	Now       time.Time
}

type HarnessGapResult struct {
	Report       *HarnessGapReport
	JSONPath     string
	MarkdownPath string
	Markdown     string
	Published    []PublishedIssue
}

type HarnessGapReport struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Findings    []HarnessGapFinding `json:"findings,omitempty"`
	Warnings    []string            `json:"warnings,omitempty"`
}

type HarnessGapFinding struct {
	Fingerprint  string   `json:"fingerprint"`
	Category     string   `json:"category"`
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	Observed     int      `json:"observed"`
	Threshold    int      `json:"threshold"`
	Evidence     []string `json:"evidence,omitempty"`
	Remediations []string `json:"remediations,omitempty"`
}

type harnessGapIssueState struct {
	Findings map[string]contextWeightIssueRecord `json:"findings,omitempty"`
}

type harnessGapDaemonEntry struct {
	Time time.Time
	Line string
}

type harnessGapIdleEpisode struct {
	Start   time.Time
	End     time.Time
	Pending int
}

type harnessGapPullRequest struct {
	Number      int               `json:"number"`
	Title       string            `json:"title"`
	URL         string            `json:"url"`
	HeadRefName string            `json:"headRefName"`
	Mergeable   string            `json:"mergeable"`
	MergedAt    time.Time         `json:"mergedAt"`
	CreatedAt   time.Time         `json:"createdAt"`
	MergedBy    *harnessGapUser   `json:"mergedBy"`
	Labels      []harnessGapLabel `json:"labels"`
}

type harnessGapCommit struct {
	OID string `json:"oid"`
}

type harnessGapUser struct {
	Login string `json:"login"`
}

type harnessGapLabel struct {
	Name string `json:"name"`
}

type harnessGapIssue struct {
	Number int               `json:"number"`
	Title  string            `json:"title"`
	URL    string            `json:"url"`
	Body   string            `json:"body"`
	Labels []harnessGapLabel `json:"labels"`
}

func GenerateHarnessGapAnalysis(ctx context.Context, stateDir, repo string, runner issueRunner, opts HarnessGapOptions) (*HarnessGapResult, error) {
	if strings.TrimSpace(opts.OutputDir) == "" {
		opts.OutputDir = defaultOutputDir
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}

	findings, warnings, err := buildHarnessGapFindings(ctx, stateDir, repo, runner, opts.Now)
	if err != nil {
		return nil, fmt.Errorf("generate harness-gap analysis: %w", err)
	}

	report := &HarnessGapReport{
		GeneratedAt: opts.Now,
		Findings:    findings,
		Warnings:    warnings,
	}

	outputDir := filepath.Join(stateDir, opts.OutputDir)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("generate harness-gap analysis: create output dir: %w", err)
	}

	jsonPath := filepath.Join(outputDir, harnessGapReportJSONName)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("generate harness-gap analysis: marshal report: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("generate harness-gap analysis: write json report: %w", err)
	}

	markdown := renderHarnessGapMarkdown(report)
	markdownPath := filepath.Join(outputDir, harnessGapReportMarkdownName)
	if err := os.WriteFile(markdownPath, []byte(markdown), 0o644); err != nil {
		return nil, fmt.Errorf("generate harness-gap analysis: write markdown report: %w", err)
	}

	return &HarnessGapResult{
		Report:       report,
		JSONPath:     jsonPath,
		MarkdownPath: markdownPath,
		Markdown:     markdown,
	}, nil
}

func RunHarnessGapAnalysis(ctx context.Context, stateDir, repo string, runner issueRunner, opts HarnessGapOptions) (*HarnessGapResult, error) {
	result, err := GenerateHarnessGapAnalysis(ctx, stateDir, repo, runner, opts)
	if err != nil {
		return nil, err
	}
	published, err := PublishHarnessGapIssues(ctx, stateDir, repo, runner, result.Report, opts.OutputDir, opts.Now)
	if err != nil {
		return nil, err
	}
	result.Published = published
	return result, nil
}

func PublishHarnessGapIssues(ctx context.Context, stateDir, repo string, runner issueRunner, report *HarnessGapReport, outputDir string, now time.Time) ([]PublishedIssue, error) {
	if report == nil {
		return nil, nil
	}
	if strings.TrimSpace(outputDir) == "" {
		outputDir = defaultOutputDir
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	statePath := filepath.Join(stateDir, outputDir, harnessGapIssueStateName)
	state, err := loadHarnessGapIssueState(statePath)
	if err != nil {
		return nil, err
	}

	openByFingerprint := map[string]contextWeightIssue{}
	if runner != nil && strings.TrimSpace(repo) != "" && len(report.Findings) > 0 {
		openByFingerprint, err = loadOpenHarnessGapIssues(ctx, runner, repo)
		if err != nil {
			return nil, err
		}
	}

	published := make([]PublishedIssue, 0, len(report.Findings))
	for _, finding := range report.Findings {
		record, ok := state.Findings[finding.Fingerprint]
		switch {
		case ok:
			record.Title = finding.Title
			record.LastObservedAt = now
			state.Findings[finding.Fingerprint] = record
			published = append(published, PublishedIssue{
				Fingerprint: finding.Fingerprint,
				IssueNumber: record.IssueNumber,
				Title:       record.Title,
				Created:     false,
			})
			continue
		case openByFingerprint[finding.Fingerprint].Number > 0:
			issue := openByFingerprint[finding.Fingerprint]
			state.Findings[finding.Fingerprint] = contextWeightIssueRecord{
				IssueNumber:     issue.Number,
				Title:           issue.Title,
				FirstReportedAt: now,
				LastObservedAt:  now,
			}
			published = append(published, PublishedIssue{
				Fingerprint: finding.Fingerprint,
				IssueNumber: issue.Number,
				Title:       issue.Title,
				Created:     false,
			})
			continue
		}

		if runner == nil || strings.TrimSpace(repo) == "" {
			continue
		}

		body := renderHarnessGapIssueBody(finding)
		out, err := runner.RunOutput(ctx, "gh", "issue", "create", "--repo", repo, "--title", finding.Title, "--body", body)
		if err != nil {
			return nil, fmt.Errorf("publish harness-gap issue for %s: %w", finding.Category, err)
		}
		issueNumber, err := parseIssueNumberFromCreateOutput(string(out))
		if err != nil {
			return nil, fmt.Errorf("publish harness-gap issue for %s: %w", finding.Category, err)
		}
		state.Findings[finding.Fingerprint] = contextWeightIssueRecord{
			IssueNumber:     issueNumber,
			Title:           finding.Title,
			FirstReportedAt: now,
			LastObservedAt:  now,
		}
		published = append(published, PublishedIssue{
			Fingerprint: finding.Fingerprint,
			IssueNumber: issueNumber,
			Title:       finding.Title,
			Created:     true,
		})
	}

	if err := saveHarnessGapIssueState(statePath, state); err != nil {
		return nil, err
	}
	return published, nil
}

func buildHarnessGapFindings(ctx context.Context, stateDir, repo string, runner issueRunner, now time.Time) ([]HarnessGapFinding, []string, error) {
	findings := make([]HarnessGapFinding, 0, 7)
	warnings := make([]string, 0, 1)

	localFindings, localWarnings, err := buildHarnessGapLocalFindings(stateDir, now)
	if err != nil {
		return nil, nil, err
	}
	findings = append(findings, localFindings...)
	warnings = append(warnings, localWarnings...)

	if runner == nil || strings.TrimSpace(repo) == "" {
		sortHarnessGapFindings(findings)
		return findings, warnings, nil
	}

	if finding, err := detectAdminMergeGap(ctx, repo, runner, now); err != nil {
		return nil, nil, err
	} else if finding != nil {
		findings = append(findings, *finding)
	}
	if finding, err := detectStaleConflictLabelGap(ctx, repo, runner); err != nil {
		return nil, nil, err
	} else if finding != nil {
		findings = append(findings, *finding)
	}
	if finding, err := detectReleaseCadenceGap(ctx, repo, runner, now); err != nil {
		return nil, nil, err
	} else if finding != nil {
		findings = append(findings, *finding)
	}
	if finding, err := detectConfigDriftGap(ctx, runner); err != nil {
		return nil, nil, err
	} else if finding != nil {
		findings = append(findings, *finding)
	}
	if finding, err := detectFailedFingerprintBacklogGap(ctx, repo, runner); err != nil {
		return nil, nil, err
	} else if finding != nil {
		findings = append(findings, *finding)
	}

	sortHarnessGapFindings(findings)
	return findings, warnings, nil
}

func buildHarnessGapLocalFindings(stateDir string, now time.Time) ([]HarnessGapFinding, []string, error) {
	path := filepath.Join(stateDir, harnessGapDaemonLogFileName)
	entries, err := loadHarnessGapDaemonLog(path)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 {
		return nil, []string{"daemon log not available; skipped local restart/backlog signals"}, nil
	}

	findings := make([]HarnessGapFinding, 0, 2)
	cutoff := now.Add(-harnessGapLocalSignalsLookback)
	restarts := 0
	restartEvidence := make([]string, 0, 3)
	for _, entry := range entries {
		if entry.Time.Before(cutoff) {
			continue
		}
		if logFieldValue(entry.Line, "msg") != "daemon reconcile recovered orphaned vessels" {
			continue
		}
		recovered, ok := logFieldInt(entry.Line, "recovered")
		if !ok || recovered <= 0 {
			continue
		}
		restarts += recovered
		restartEvidence = append(restartEvidence, fmt.Sprintf("%s recovered %d orphaned vessel(s) after restart", entry.Time.Format(time.RFC3339), recovered))
	}
	if restarts >= harnessGapRestartThreshold {
		finding := newHarnessGapFinding(
			"daemon-restart-count",
			"harness-gap-analysis: daemon restarts orphaned running vessels",
			fmt.Sprintf("the daemon recovered %d orphaned vessel(s) in the last 24h, which means restart handling still depends on timeout recovery", restarts),
			restarts,
			harnessGapRestartThreshold,
			restartEvidence,
			[]string{
				"Track and surface restart causes so the daemon can distinguish operator restarts from harness bugs.",
				"Make restart recovery resumable or cancellable instead of relying on orphaned running vessels timing out later.",
			},
		)
		findings = append(findings, finding)
	}

	episodes := detectIdleBacklogEpisodes(entries, cutoff)
	if len(episodes) == 0 {
		return findings, nil, nil
	}

	longest := idleBacklogLongest(episodes)
	if longest.End.Sub(longest.Start) >= harnessGapIdleBacklogThreshold {
		evidence := make([]string, 0, len(episodes))
		for _, episode := range episodes {
			evidence = append(evidence, fmt.Sprintf(
				"%s to %s: pending=%d while running=0",
				episode.Start.Format(time.RFC3339),
				episode.End.Format(time.RFC3339),
				episode.Pending,
			))
		}
		finding := newHarnessGapFinding(
			"idle-with-backlog",
			"harness-gap-analysis: daemon stayed idle while backlog existed",
			fmt.Sprintf("the daemon spent %s idle with queued backlog, which suggests missing wakeup or dequeue reliability", longest.End.Sub(longest.Start).Round(time.Minute)),
			int(longest.End.Sub(longest.Start).Minutes()),
			int(harnessGapIdleBacklogThreshold.Minutes()),
			evidence,
			[]string{
				"Promote idle-with-backlog episodes to a first-class health signal instead of relying on log inspection.",
				"Teach the daemon to self-heal when backlog remains pending across multiple drain intervals.",
			},
		)
		findings = append(findings, finding)
	}

	return findings, nil, nil
}

func detectAdminMergeGap(ctx context.Context, repo string, runner issueRunner, now time.Time) (*HarnessGapFinding, error) {
	args := append(harnessGapRepoArgs(repo), "pr", "list", "--state", "merged", "--limit", "100", "--json", "number,title,url,headRefName,mergedAt,mergedBy,labels")
	out, err := runner.RunOutput(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("detect admin-merge gap: list merged pull requests: %w", err)
	}
	var prs []harnessGapPullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("detect admin-merge gap: parse gh pr list output: %w", err)
	}

	cutoff := now.Add(-harnessGapMergedPRLookback)
	evidence := make([]string, 0, len(prs))
	count := 0
	for _, pr := range prs {
		if pr.MergedAt.IsZero() || pr.MergedAt.Before(cutoff) {
			continue
		}
		if !harnessGapIssueBranchPattern.MatchString(strings.TrimSpace(pr.HeadRefName)) {
			continue
		}
		if !hasHarnessGapLabel(pr.Labels, "harness-impl") {
			continue
		}
		if pr.MergedBy == nil || strings.TrimSpace(pr.MergedBy.Login) == "" || strings.Contains(pr.MergedBy.Login, "[bot]") {
			continue
		}
		count++
		evidence = append(evidence, fmt.Sprintf("#%d merged by @%s from `%s` at %s", pr.Number, pr.MergedBy.Login, pr.HeadRefName, pr.MergedAt.Format(time.RFC3339)))
	}
	if count < harnessGapMergedPRThreshold {
		return nil, nil
	}

	finding := newHarnessGapFinding(
		"merge-click-frequency",
		"harness-gap-analysis: automate repeated human admin merges",
		fmt.Sprintf("%d harness PRs were merged by humans in the last 24h, which is the manual merge work #244 was meant to remove", count),
		count,
		harnessGapMergedPRThreshold,
		evidence,
		[]string{
			"Route merge-ready harness PRs through the daemon's auto-merge path instead of depending on a human admin merge click.",
			"Emit a first-class merge automation signal when issue-branch PRs keep landing via human merges.",
		},
	)
	return &finding, nil
}

func detectStaleConflictLabelGap(ctx context.Context, repo string, runner issueRunner) (*HarnessGapFinding, error) {
	args := append(harnessGapRepoArgs(repo), "pr", "list", "--state", "open", "--label", "needs-conflict-resolution", "--limit", "100", "--json", "number,title,url,mergeable,headRefName")
	out, err := runner.RunOutput(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("detect stale conflict-label gap: list open conflict PRs: %w", err)
	}
	var prs []harnessGapPullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("detect stale conflict-label gap: parse gh pr list output: %w", err)
	}

	evidence := make([]string, 0, len(prs))
	count := 0
	for _, pr := range prs {
		if strings.EqualFold(strings.TrimSpace(pr.Mergeable), "CONFLICTING") {
			continue
		}
		count++
		evidence = append(evidence, fmt.Sprintf("#%d is `%s` but still labeled `needs-conflict-resolution`", pr.Number, strings.TrimSpace(pr.Mergeable)))
	}
	if count < harnessGapStaleConflictThreshold {
		return nil, nil
	}

	finding := newHarnessGapFinding(
		"stale-label-patterns",
		"harness-gap-analysis: clean up stale conflict labels on mergeable PRs",
		fmt.Sprintf("%d open PR(s) still carry `needs-conflict-resolution` even though GitHub no longer reports them as conflicting", count),
		count,
		harnessGapStaleConflictThreshold,
		evidence,
		[]string{
			"Move stale conflict-label cleanup into a deterministic daemon path instead of waiting for a human to strip labels.",
			"Escalate repeated stale conflict labels as resolve-conflicts reliability debt.",
		},
	)
	return &finding, nil
}

func detectReleaseCadenceGap(ctx context.Context, repo string, runner issueRunner, now time.Time) (*HarnessGapFinding, error) {
	args := append(harnessGapRepoArgs(repo), "pr", "list", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt")
	out, err := runner.RunOutput(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("detect release cadence gap: list open pull requests: %w", err)
	}
	var prs []harnessGapPullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("detect release cadence gap: parse gh pr list output: %w", err)
	}

	var releasePR *harnessGapPullRequest
	for i := range prs {
		head := strings.ToLower(strings.TrimSpace(prs[i].HeadRefName))
		title := strings.ToLower(strings.TrimSpace(prs[i].Title))
		if strings.HasPrefix(head, "release-please") || strings.Contains(title, "release please") {
			releasePR = &prs[i]
			break
		}
	}
	if releasePR == nil || releasePR.CreatedAt.IsZero() {
		return nil, nil
	}
	age := now.Sub(releasePR.CreatedAt)
	if age < harnessGapReleasePRAgeThreshold {
		return nil, nil
	}

	viewArgs := append(harnessGapRepoArgs(repo), "pr", "view", strconv.Itoa(releasePR.Number), "--json", "commits")
	commitOut, err := runner.RunOutput(ctx, "gh", viewArgs...)
	if err != nil {
		return nil, fmt.Errorf("detect release cadence gap: view release pull request: %w", err)
	}
	var details struct {
		Commits []harnessGapCommit `json:"commits"`
	}
	if err := json.Unmarshal(commitOut, &details); err != nil {
		return nil, fmt.Errorf("detect release cadence gap: parse gh pr view output: %w", err)
	}
	if len(details.Commits) < harnessGapReleasePRCommitMinimum {
		return nil, nil
	}

	finding := newHarnessGapFinding(
		"release-cadence",
		"harness-gap-analysis: release-please cadence is drifting",
		fmt.Sprintf("release PR #%d has been open for %s with %d queued commit(s)", releasePR.Number, age.Round(24*time.Hour), len(details.Commits)),
		len(details.Commits),
		harnessGapReleasePRCommitMinimum,
		[]string{
			fmt.Sprintf("#%d opened at %s and still accumulating commits on `%s`", releasePR.Number, releasePR.CreatedAt.Format(time.RFC3339), releasePR.HeadRefName),
		},
		[]string{
			"Teach the daemon to notice stale release PRs before they become week-long release debt.",
			"Bound release PR age or commit count with an explicit cadence policy and issue escalation.",
		},
	)
	return &finding, nil
}

func detectConfigDriftGap(ctx context.Context, runner issueRunner) (*HarnessGapFinding, error) {
	out, err := runner.RunOutput(ctx, "git", "rev-list", "--left-right", "--count", "origin/main...HEAD")
	if err != nil {
		return nil, fmt.Errorf("detect config drift gap: git rev-list: %w", err)
	}
	behind, ahead, err := parseGitRevListAheadBehind(string(out))
	if err != nil {
		return nil, fmt.Errorf("detect config drift gap: %w", err)
	}
	if behind == 0 && ahead == 0 {
		return nil, nil
	}

	finding := newHarnessGapFinding(
		"config-drift",
		"harness-gap-analysis: daemon worktree drifted from origin/main",
		fmt.Sprintf("the daemon worktree is behind by %d commit(s) and ahead by %d commit(s) versus origin/main", behind, ahead),
		behind+ahead,
		1,
		[]string{fmt.Sprintf("git rev-list --left-right --count origin/main...HEAD => behind=%d ahead=%d", behind, ahead)},
		[]string{
			"Surface daemon worktree drift before scheduled scans keep running old control-plane code.",
			"Turn drift detection into an explicit self-heal or issue-filing path instead of relying on operator discovery.",
		},
	)
	return &finding, nil
}

func detectFailedFingerprintBacklogGap(ctx context.Context, repo string, runner issueRunner) (*HarnessGapFinding, error) {
	args := append(harnessGapRepoArgs(repo), "issue", "list", "--state", "open", "--label", "xylem-failed", "--limit", "100", "--json", "number,title,url,labels")
	out, err := runner.RunOutput(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("detect failed-fingerprint backlog gap: list failed issues: %w", err)
	}
	var issues []harnessGapIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("detect failed-fingerprint backlog gap: parse gh issue list output: %w", err)
	}

	evidence := make([]string, 0, len(issues))
	count := 0
	for _, issue := range issues {
		if hasHarnessGapLabel(issue.Labels, "ready-for-work") {
			continue
		}
		count++
		evidence = append(evidence, fmt.Sprintf("#%d `%s` is still labeled `xylem-failed` without `ready-for-work`", issue.Number, issue.Title))
	}
	if count < harnessGapFailedBacklogThreshold {
		return nil, nil
	}

	finding := newHarnessGapFinding(
		"failed-fingerprint-backlog",
		"harness-gap-analysis: failed backlog is missing automatic retry routing",
		fmt.Sprintf("%d failed issue(s) remain parked behind `xylem-failed` without a ready-for-work route", count),
		count,
		harnessGapFailedBacklogThreshold,
		evidence,
		[]string{
			"Escalate parked failed issues when no retry cooldown or unlock path returns them to the backlog.",
			"Pair failure fingerprints with deterministic requeue policies so failed work does not silently accumulate.",
		},
	)
	return &finding, nil
}

func loadHarnessGapDaemonLog(path string) ([]harnessGapDaemonEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load harness-gap daemon log: read %q: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	entries := make([]harnessGapDaemonEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rawTime := logFieldValue(line, "time")
		if rawTime == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, rawTime)
		if err != nil {
			continue
		}
		entries = append(entries, harnessGapDaemonEntry{Time: ts.UTC(), Line: line})
	}
	return entries, nil
}

func detectIdleBacklogEpisodes(entries []harnessGapDaemonEntry, cutoff time.Time) []harnessGapIdleEpisode {
	episodes := make([]harnessGapIdleEpisode, 0)
	var current *harnessGapIdleEpisode
	for _, entry := range entries {
		if entry.Time.Before(cutoff) {
			continue
		}
		if logFieldValue(entry.Line, "msg") != "daemon tick summary" {
			continue
		}
		pending, okPending := logFieldInt(entry.Line, "pending")
		running, okRunning := logFieldInt(entry.Line, "running")
		if !okPending || !okRunning {
			continue
		}
		if pending > 0 && running == 0 {
			if current == nil {
				current = &harnessGapIdleEpisode{Start: entry.Time, End: entry.Time, Pending: pending}
				continue
			}
			current.End = entry.Time
			if pending > current.Pending {
				current.Pending = pending
			}
			continue
		}
		if current != nil {
			episodes = append(episodes, *current)
			current = nil
		}
	}
	if current != nil {
		episodes = append(episodes, *current)
	}
	return episodes
}

func idleBacklogLongest(episodes []harnessGapIdleEpisode) harnessGapIdleEpisode {
	longest := episodes[0]
	for _, episode := range episodes[1:] {
		if episode.End.Sub(episode.Start) > longest.End.Sub(longest.Start) {
			longest = episode
		}
	}
	return longest
}

func renderHarnessGapMarkdown(report *HarnessGapReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Harness gap analysis\n\n")
	fmt.Fprintf(&b, "- Generated: %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Findings above threshold: %d\n\n", len(report.Findings))

	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "## Warnings\n\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
		fmt.Fprintf(&b, "\n")
	}

	if len(report.Findings) == 0 {
		fmt.Fprintf(&b, "No recurring autonomy gaps exceeded the built-in thresholds.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "## Findings\n\n")
	for _, finding := range report.Findings {
		fmt.Fprintf(&b, "### %s\n\n", finding.Title)
		fmt.Fprintf(&b, "- Category: `%s`\n", finding.Category)
		fmt.Fprintf(&b, "- Observed: %d (threshold %d)\n", finding.Observed, finding.Threshold)
		fmt.Fprintf(&b, "- Summary: %s\n", finding.Summary)
		if len(finding.Evidence) > 0 {
			fmt.Fprintf(&b, "- Evidence:\n")
			for _, evidence := range finding.Evidence {
				fmt.Fprintf(&b, "  - %s\n", evidence)
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

func renderHarnessGapIssueBody(finding HarnessGapFinding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s -->\n", harnessGapFindingMarkerPrefix, finding.Fingerprint)
	fmt.Fprintf(&b, "## Harness gap finding\n\n")
	fmt.Fprintf(&b, "- Category: `%s`\n", finding.Category)
	fmt.Fprintf(&b, "- Observed: %d (threshold %d)\n", finding.Observed, finding.Threshold)
	fmt.Fprintf(&b, "- Summary: %s\n\n", finding.Summary)
	fmt.Fprintf(&b, "This issue was generated from deterministic harness telemetry already persisted by xylem (daemon logs, GitHub state, git state, and phase summaries where available).\n\n")
	if len(finding.Evidence) > 0 {
		fmt.Fprintf(&b, "## Evidence\n\n")
		for _, evidence := range finding.Evidence {
			fmt.Fprintf(&b, "- %s\n", evidence)
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, "## Suggested follow-up\n\n")
	for _, remediation := range finding.Remediations {
		fmt.Fprintf(&b, "- %s\n", remediation)
	}
	return b.String()
}

func loadOpenHarnessGapIssues(ctx context.Context, runner issueRunner, repo string) (map[string]contextWeightIssue, error) {
	out, err := runner.RunOutput(ctx, "gh", "issue", "list", "--repo", repo, "--state", "open", "--limit", "100", "--json", "number,title,body")
	if err != nil {
		return nil, fmt.Errorf("load open harness-gap issues: %w", err)
	}
	var issues []contextWeightIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("load open harness-gap issues: parse gh issue list output: %w", err)
	}
	byFingerprint := make(map[string]contextWeightIssue, len(issues))
	for _, issue := range issues {
		fingerprint := parseHarnessGapMarker(issue.Body)
		if fingerprint == "" {
			continue
		}
		byFingerprint[fingerprint] = issue
	}
	return byFingerprint, nil
}

func parseHarnessGapMarker(body string) string {
	start := strings.Index(body, harnessGapFindingMarkerPrefix)
	if start == -1 {
		return ""
	}
	start += len(harnessGapFindingMarkerPrefix)
	end := strings.Index(body[start:], " -->")
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(body[start : start+end])
}

func loadHarnessGapIssueState(path string) (*harnessGapIssueState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &harnessGapIssueState{Findings: make(map[string]contextWeightIssueRecord)}, nil
		}
		return nil, fmt.Errorf("load harness-gap issue state: %w", err)
	}
	var state harnessGapIssueState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("load harness-gap issue state: unmarshal: %w", err)
	}
	if state.Findings == nil {
		state.Findings = make(map[string]contextWeightIssueRecord)
	}
	return &state, nil
}

func saveHarnessGapIssueState(path string, state *harnessGapIssueState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save harness-gap issue state: create dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("save harness-gap issue state: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save harness-gap issue state: write: %w", err)
	}
	return nil
}

func newHarnessGapFinding(category, title, summary string, observed, threshold int, evidence, remediations []string) HarnessGapFinding {
	finding := HarnessGapFinding{
		Category:     category,
		Title:        title,
		Summary:      summary,
		Observed:     observed,
		Threshold:    threshold,
		Evidence:     append([]string(nil), evidence...),
		Remediations: append([]string(nil), remediations...),
	}
	finding.Fingerprint = harnessGapFindingFingerprint(finding)
	return finding
}

func harnessGapFindingFingerprint(finding HarnessGapFinding) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		finding.Category,
		finding.Title,
	}, "\n")))
	return fmt.Sprintf("%x", sum[:8])
}

func sortHarnessGapFindings(findings []HarnessGapFinding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Category != findings[j].Category {
			return findings[i].Category < findings[j].Category
		}
		return findings[i].Title < findings[j].Title
	})
}

func logFieldValue(line, key string) string {
	pattern := regexp.MustCompile(`(?:^| )` + regexp.QuoteMeta(key) + `=("[^"]*"|[^ ]+)`)
	match := pattern.FindStringSubmatch(line)
	if len(match) < 2 {
		return ""
	}
	return strings.Trim(match[1], `"`)
}

func logFieldInt(line, key string) (int, bool) {
	raw := strings.TrimSpace(logFieldValue(line, key))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseGitRevListAheadBehind(raw string) (behind int, ahead int, err error) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("parse git rev-list counts %q: want two integers", strings.TrimSpace(raw))
	}
	behind, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse git rev-list behind count %q: %w", fields[0], err)
	}
	ahead, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse git rev-list ahead count %q: %w", fields[1], err)
	}
	return behind, ahead, nil
}

func hasHarnessGapLabel(labels []harnessGapLabel, want string) bool {
	for _, label := range labels {
		if label.Name == want {
			return true
		}
	}
	return false
}

func harnessGapRepoArgs(repo string) []string {
	if strings.TrimSpace(repo) == "" {
		return nil
	}
	return []string{"--repo", repo}
}
