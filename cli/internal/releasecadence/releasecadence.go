package releasecadence

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultReadyLabel  = "ready-to-merge"
	DefaultOptOutLabel = "no-auto-admin-merge"
	DefaultMinAge      = 7 * 24 * time.Hour
	DefaultMinCommits  = 5
)

type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

type Action string

const (
	ActionNoop    Action = "noop"
	ActionLabeled Action = "labeled"
)

type Options struct {
	Repo        string
	ReadyLabel  string
	OptOutLabel string
	MinAge      time.Duration
	MinCommits  int
	Now         time.Time
}

type Result struct {
	Action      Action
	PRNumber    int
	URL         string
	HeadRefName string
	Age         time.Duration
	CommitCount int
	ReadyLabel  string
	Reason      string
}

type prLabel struct {
	Name string `json:"name"`
}

type pullRequest struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	HeadRefName string    `json:"headRefName"`
	CreatedAt   time.Time `json:"createdAt"`
	Labels      []prLabel `json:"labels"`
}

type prCommitView struct {
	Commits []struct {
		OID string `json:"oid"`
	} `json:"commits"`
}

func MatchesReleasePR(headRefName, title string) bool {
	head := strings.ToLower(strings.TrimSpace(headRefName))
	name := strings.ToLower(strings.TrimSpace(title))
	return strings.HasPrefix(head, "release-please") || strings.Contains(name, "release please")
}

func ApplyReadyLabel(ctx context.Context, runner CommandRunner, opts Options) (*Result, error) {
	if runner == nil {
		return nil, fmt.Errorf("release cadence requires a command runner")
	}
	opts = withDefaults(opts)
	if opts.Repo == "" {
		return nil, fmt.Errorf("release cadence requires a repo slug")
	}

	pr, err := findReleasePR(ctx, runner, opts.Repo)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return &Result{Action: ActionNoop, Reason: "no open release-please PR found", ReadyLabel: opts.ReadyLabel}, nil
	}

	if hasLabel(pr.Labels, opts.OptOutLabel) {
		return &Result{
			Action:      ActionNoop,
			PRNumber:    pr.Number,
			URL:         pr.URL,
			HeadRefName: pr.HeadRefName,
			ReadyLabel:  opts.ReadyLabel,
			Reason:      fmt.Sprintf("release PR #%d opted out via %q", pr.Number, opts.OptOutLabel),
		}, nil
	}
	if hasLabel(pr.Labels, opts.ReadyLabel) {
		return &Result{
			Action:      ActionNoop,
			PRNumber:    pr.Number,
			URL:         pr.URL,
			HeadRefName: pr.HeadRefName,
			ReadyLabel:  opts.ReadyLabel,
			Reason:      fmt.Sprintf("release PR #%d already has %q", pr.Number, opts.ReadyLabel),
		}, nil
	}
	if pr.CreatedAt.IsZero() {
		return &Result{
			Action:      ActionNoop,
			PRNumber:    pr.Number,
			URL:         pr.URL,
			HeadRefName: pr.HeadRefName,
			ReadyLabel:  opts.ReadyLabel,
			Reason:      fmt.Sprintf("release PR #%d is missing createdAt metadata", pr.Number),
		}, nil
	}

	age := opts.Now.Sub(pr.CreatedAt)
	if age < opts.MinAge {
		return &Result{
			Action:      ActionNoop,
			PRNumber:    pr.Number,
			URL:         pr.URL,
			HeadRefName: pr.HeadRefName,
			Age:         age,
			ReadyLabel:  opts.ReadyLabel,
			Reason:      fmt.Sprintf("release PR #%d is only %s old", pr.Number, roundDurationDay(age)),
		}, nil
	}

	commitCount, err := releaseCommitCount(ctx, runner, opts.Repo, pr.Number)
	if err != nil {
		return nil, err
	}
	if commitCount < opts.MinCommits {
		return &Result{
			Action:      ActionNoop,
			PRNumber:    pr.Number,
			URL:         pr.URL,
			HeadRefName: pr.HeadRefName,
			Age:         age,
			CommitCount: commitCount,
			ReadyLabel:  opts.ReadyLabel,
			Reason:      fmt.Sprintf("release PR #%d has only %d commit(s) queued", pr.Number, commitCount),
		}, nil
	}

	if err := addReadyLabel(ctx, runner, opts.Repo, pr.Number, opts.ReadyLabel); err != nil {
		return nil, err
	}

	return &Result{
		Action:      ActionLabeled,
		PRNumber:    pr.Number,
		URL:         pr.URL,
		HeadRefName: pr.HeadRefName,
		Age:         age,
		CommitCount: commitCount,
		ReadyLabel:  opts.ReadyLabel,
		Reason:      fmt.Sprintf("applied %q to release PR #%d", opts.ReadyLabel, pr.Number),
	}, nil
}

func withDefaults(opts Options) Options {
	opts.Repo = strings.TrimSpace(opts.Repo)
	opts.ReadyLabel = strings.TrimSpace(opts.ReadyLabel)
	if opts.ReadyLabel == "" {
		opts.ReadyLabel = DefaultReadyLabel
	}
	opts.OptOutLabel = strings.TrimSpace(opts.OptOutLabel)
	if opts.OptOutLabel == "" {
		opts.OptOutLabel = DefaultOptOutLabel
	}
	if opts.MinAge <= 0 {
		opts.MinAge = DefaultMinAge
	}
	if opts.MinCommits <= 0 {
		opts.MinCommits = DefaultMinCommits
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	return opts
}

func findReleasePR(ctx context.Context, runner CommandRunner, repo string) (*pullRequest, error) {
	out, err := runner.RunOutput(ctx, "gh", "pr", "list", "--repo", repo, "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt,labels")
	if err != nil {
		return nil, fmt.Errorf("list open pull requests: %w", err)
	}

	var prs []pullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}

	candidates := make([]pullRequest, 0, len(prs))
	for _, pr := range prs {
		if MatchesReleasePR(pr.HeadRefName, pr.Title) {
			candidates = append(candidates, pr)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})
	return &candidates[0], nil
}

func releaseCommitCount(ctx context.Context, runner CommandRunner, repo string, prNumber int) (int, error) {
	out, err := runner.RunOutput(ctx, "gh", "pr", "view", fmt.Sprintf("%d", prNumber), "--repo", repo, "--json", "commits")
	if err != nil {
		return 0, fmt.Errorf("view release pull request #%d: %w", prNumber, err)
	}

	var view prCommitView
	if err := json.Unmarshal(out, &view); err != nil {
		return 0, fmt.Errorf("parse gh pr view output for #%d: %w", prNumber, err)
	}
	return len(view.Commits), nil
}

func addReadyLabel(ctx context.Context, runner CommandRunner, repo string, prNumber int, label string) error {
	_, err := runner.RunOutput(ctx, "gh", "pr", "edit", fmt.Sprintf("%d", prNumber), "--repo", repo, "--add-label", label)
	if err != nil {
		return fmt.Errorf("add %q to release pull request #%d: %w", label, prNumber, err)
	}
	return nil
}

func hasLabel(labels []prLabel, want string) bool {
	for _, label := range labels {
		if strings.TrimSpace(label.Name) == want {
			return true
		}
	}
	return false
}

func roundDurationDay(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d.Round(24 * time.Hour)
}
