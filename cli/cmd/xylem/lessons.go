package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	lessonspkg "github.com/nicholls-inc/xylem/cli/internal/lessons"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	reviewpkg "github.com/nicholls-inc/xylem/cli/internal/review"
	runnerpkg "github.com/nicholls-inc/xylem/cli/internal/runner"
)

type lessonsRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
	RunProcess(ctx context.Context, dir string, name string, args ...string) error
	RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error)
}

type lessonsWorktree interface {
	Create(ctx context.Context, branchName string) (string, error)
	Remove(ctx context.Context, worktreePath string) error
}

var generateLessons = func(ctx context.Context, cfg *config.Config, runner lessonsRunner) (*lessonspkg.Result, error) {
	return lessonspkg.Generate(ctx, cfg.StateDir, lessonspkg.Options{
		Repo:        detectLessonsRepo(cfg),
		HarnessPath: ".xylem/HARNESS.md",
		OutputDir:   "reviews",
	}, &ghLessonsPRClient{runner: runner})
}

type ghLessonsPRClient struct {
	runner lessonsRunner
}

func (c *ghLessonsPRClient) ListOpenPullRequests(ctx context.Context, repo string) ([]lessonspkg.OpenPullRequest, error) {
	if c == nil || c.runner == nil || strings.TrimSpace(repo) == "" {
		return nil, nil
	}
	out, err := c.runner.RunOutput(ctx, "gh", "pr", "list", "--repo", repo, "--state", "open", "--json", "number,title,body,headRefName,labels", "--limit", "100")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		HeadRefName string `json:"headRefName"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	prs := make([]lessonspkg.OpenPullRequest, 0, len(raw))
	for _, pr := range raw {
		labels := make([]string, 0, len(pr.Labels))
		for _, label := range pr.Labels {
			labels = append(labels, label.Name)
		}
		prs = append(prs, lessonspkg.OpenPullRequest{
			Number:     pr.Number,
			Title:      pr.Title,
			Body:       pr.Body,
			HeadBranch: pr.HeadRefName,
			Labels:     labels,
		})
	}
	return prs, nil
}

func newLessonsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lessons",
		Short: "Synthesize institutional-memory lessons from failed vessels",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdLessons(deps.cfg, deps.wt, newCmdRunner(deps.cfg))
		},
	}
}

func cmdLessons(cfg *config.Config, wt lessonsWorktree, runner lessonsRunner) error {
	result, err := runLessons(context.Background(), cfg, wt, runner)
	if err != nil {
		return fmt.Errorf("generate lessons: %w", err)
	}
	fmt.Printf("Wrote %s and %s\n\n%s", result.JSONPath, result.MarkdownPath, result.Markdown)
	return nil
}

func buildBuiltinWorkflowHandlers(cfg *config.Config, wt lessonsWorktree, runner lessonsRunner) map[string]runnerpkg.BuiltinWorkflowHandler {
	handlers := map[string]runnerpkg.BuiltinWorkflowHandler{
		"lessons": func(ctx context.Context, _ queue.Vessel) error {
			_, err := runLessons(ctx, cfg, wt, runner)
			return err
		},
	}
	// Register built-in scheduled audit workflows so the daemon's drain
	// loop handles them directly instead of trying to load a YAML file.
	for _, name := range []string{
		reviewpkg.ContextWeightAuditWorkflow,
		reviewpkg.HarnessGapAnalysisWorkflow,
		reviewpkg.WorkflowHealthReportWorkflow,
	} {
		workflowName := name // capture loop variable
		handlers[workflowName] = func(ctx context.Context, vessel queue.Vessel) error {
			repo := resolveScheduledAuditRepo(cfg, vessel)
			if repo == "" {
				return fmt.Errorf("%s requires a source repo", workflowName)
			}
			return runBuiltInScheduledWorkflow(ctx, cfg, workflowName, repo, runner)
		}
	}
	return handlers
}

func runLessons(ctx context.Context, cfg *config.Config, wt lessonsWorktree, runner lessonsRunner) (*lessonspkg.Result, error) {
	result, err := generateLessons(ctx, cfg, runner)
	if err != nil {
		return nil, err
	}
	createErr := createLessonsPullRequests(ctx, cfg, wt, runner, result)
	writeErr := lessonspkg.WriteResult(result)
	switch {
	case createErr != nil:
		return nil, createErr
	case writeErr != nil:
		return nil, writeErr
	default:
		return result, nil
	}
}

func createLessonsPullRequests(ctx context.Context, cfg *config.Config, wt lessonsWorktree, runner lessonsRunner, result *lessonspkg.Result) error {
	if result == nil || result.Report == nil || len(result.Report.Proposals) == 0 {
		return nil
	}
	if wt == nil {
		return fmt.Errorf("create lessons pull requests: worktree manager is required")
	}
	repo := detectLessonsRepo(cfg)
	for i := range result.Report.Proposals {
		if err := createLessonsPullRequest(ctx, wt, runner, repo, &result.Report.Proposals[i]); err != nil {
			return err
		}
	}
	return nil
}

func createLessonsPullRequest(ctx context.Context, wt lessonsWorktree, runner lessonsRunner, repo string, proposal *lessonspkg.Proposal) error {
	worktreePath, err := wt.Create(ctx, proposal.Branch)
	if err != nil {
		return fmt.Errorf("create lessons pull request %q: create worktree: %w", proposal.Branch, err)
	}
	defer wt.Remove(context.Background(), worktreePath) //nolint:errcheck

	changed, err := appendLessonsHarnessPatch(worktreePath, proposal.HarnessPath, proposal.HarnessPatch)
	if err != nil {
		return fmt.Errorf("create lessons pull request %q: update harness: %w", proposal.Branch, err)
	}
	if !changed {
		proposal.Status = "skipped"
		return nil
	}
	if err := runner.RunProcess(ctx, worktreePath, "git", "add", proposal.HarnessPath); err != nil {
		return fmt.Errorf("create lessons pull request %q: git add: %w", proposal.Branch, err)
	}
	if err := runner.RunProcess(ctx, worktreePath, "git", "-c", "user.name=xylem", "-c", "user.email=xylem@localhost", "commit", "-m", proposal.Title); err != nil {
		return fmt.Errorf("create lessons pull request %q: git commit: %w", proposal.Branch, err)
	}
	if err := runner.RunProcess(ctx, worktreePath, "git", "push", "--set-upstream", "origin", proposal.Branch); err != nil {
		return fmt.Errorf("create lessons pull request %q: git push: %w", proposal.Branch, err)
	}
	createArgs := append(repoArgs(repo), "pr", "create", "--title", proposal.Title, "--body", proposal.Body, "--head", proposal.Branch, "--label", "harness-impl")
	out, err := runner.RunPhase(ctx, worktreePath, nil, "gh", createArgs...)
	if err != nil {
		return fmt.Errorf("create lessons pull request %q: gh pr create: %w", proposal.Branch, err)
	}
	proposal.Status = "created"
	proposal.PRURL = extractFirstURL(string(out))
	if details, err := lookupLessonsPR(ctx, runner, worktreePath, repo, proposal.Branch); err == nil {
		proposal.PRNumber = details.Number
		if proposal.PRURL == "" {
			proposal.PRURL = details.URL
		}
	}
	return nil
}

func appendLessonsHarnessPatch(worktreePath, harnessPath, harnessPatch string) (bool, error) {
	fullPath := filepath.Join(worktreePath, filepath.FromSlash(harnessPath))
	info, err := os.Stat(fullPath)
	if err != nil {
		return false, fmt.Errorf("stat harness: %w", err)
	}
	if err := os.Chmod(fullPath, info.Mode()|0o200); err != nil {
		return false, fmt.Errorf("chmod harness writable: %w", err)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, fmt.Errorf("read harness: %w", err)
	}
	patch := strings.TrimSpace(harnessPatch)
	if patch == "" || strings.Contains(string(data), patch) {
		return false, nil
	}
	updated := strings.TrimRight(string(data), "\n")
	if updated != "" {
		updated += "\n\n"
	}
	updated += patch + "\n"
	if err := os.WriteFile(fullPath, []byte(updated), info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("write harness: %w", err)
	}
	return true, nil
}

type lessonsPRDetails struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func lookupLessonsPR(ctx context.Context, runner lessonsRunner, worktreePath, repo, branch string) (*lessonsPRDetails, error) {
	args := append(repoArgs(repo), "pr", "list", "--head", branch, "--state", "open", "--json", "number,url", "--limit", "1")
	out, err := runner.RunPhase(ctx, worktreePath, nil, "gh", args...)
	if err != nil {
		return nil, err
	}
	var prs []lessonsPRDetails
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

func repoArgs(repo string) []string {
	if strings.TrimSpace(repo) == "" {
		return nil
	}
	return []string{"--repo", repo}
}

func extractFirstURL(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}

func detectLessonsRepo(cfg *config.Config) string {
	if strings.TrimSpace(cfg.Repo) != "" {
		return cfg.Repo
	}
	for _, src := range cfg.Sources {
		if strings.TrimSpace(src.Repo) != "" {
			return src.Repo
		}
	}
	return ""
}
