package worktree

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
)

// CommandRunner abstracts shell command execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// WorktreeInfo holds metadata about a git worktree.
type WorktreeInfo struct {
	Path       string
	Branch     string
	HeadCommit string
}

// Manager manages xylem git worktrees.
type Manager struct {
	RepoRoot      string
	Runner        CommandRunner
	DefaultBranch string // if set, skip auto-detection

	protectedSurfaces    []string
	protectedSurfacesSet bool
}

// New creates a Manager for the given repository root.
func New(repoRoot string, runner CommandRunner) *Manager {
	return &Manager{RepoRoot: repoRoot, Runner: runner}
}

var defaultProtectedSurfaces = []string{
	".xylem.yml",
	".xylem/workflows/*.yaml",
	".xylem/prompts/*/*.md",
	".xylem/HARNESS.md",
}

// SetProtectedSurfaces overrides the default read-only worktree protections.
// Passing a nil or empty slice disables the filesystem hardening layer.
func (m *Manager) SetProtectedSurfaces(patterns []string) {
	m.protectedSurfacesSet = true
	m.protectedSurfaces = append([]string(nil), patterns...)
}

func (m *Manager) protectedSurfacePatterns() []string {
	if m.protectedSurfacesSet {
		return append([]string(nil), m.protectedSurfaces...)
	}
	return append([]string(nil), defaultProtectedSurfaces...)
}

// DetectDefaultBranch detects the repository's default branch.
// If Manager.DefaultBranch is set, it returns that immediately (no network calls).
// Otherwise it tries: gh repo view → git symbolic-ref → git remote show origin.
func (m *Manager) DetectDefaultBranch(ctx context.Context) (string, error) {
	if m.DefaultBranch != "" {
		return m.DefaultBranch, nil
	}

	type ghResp struct {
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	out, err := m.Runner.Run(ctx, "gh", "repo", "view", "--json", "defaultBranchRef")
	if err == nil {
		var resp ghResp
		if jsonErr := json.Unmarshal(out, &resp); jsonErr == nil && resp.DefaultBranchRef.Name != "" {
			return resp.DefaultBranchRef.Name, nil
		}
	}

	// Fallback: local symbolic-ref (works offline when origin exists)
	out, err = m.Runner.Run(ctx, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(out))
		// ref looks like "refs/remotes/origin/main"
		if branch := strings.TrimPrefix(ref, "refs/remotes/origin/"); branch != ref && branch != "" {
			return branch, nil
		}
	}

	// Fallback: local HEAD (works without any remote)
	out, err = m.Runner.Run(ctx, "git", "symbolic-ref", "HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(out))
		if branch := strings.TrimPrefix(ref, "refs/heads/"); branch != ref && branch != "" {
			return branch, nil
		}
	}

	// Fallback: git remote show origin (requires network)
	out, err = m.Runner.Run(ctx, "git", "remote", "show", "origin")
	if err != nil {
		return "", fmt.Errorf("detect default branch: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HEAD branch:") {
			branch := strings.TrimSpace(strings.TrimPrefix(line, "HEAD branch:"))
			if branch != "" {
				return branch, nil
			}
		}
	}
	return "", fmt.Errorf("could not detect default branch from remote")
}

// Create creates a git worktree at .claude/worktrees/<branchName> branched from origin/<defaultBranch>.
// It also copies .claude/ config files (settings.json, settings.local.json, rules/) into the worktree.
func (m *Manager) Create(ctx context.Context, branchName string) (string, error) {
	span := observability.StartGlobalSpan(ctx, "worktree:create", observability.WorktreeSpanAttributes(observability.WorktreeSpanData{
		Action: "create",
		Branch: branchName,
	}))
	defer span.End()

	defaultBranch, err := m.DetectDefaultBranch(ctx)
	if err != nil {
		span.RecordError(err)
		return "", fmt.Errorf("create worktree: %w", err)
	}

	// Check if origin remote exists
	hasOrigin := m.hasRemote(ctx, "origin")

	// Fetch the default branch (only if origin exists)
	if hasOrigin {
		if _, err := retryGitCmd(ctx, m.Runner, 3, "fetch", "origin", defaultBranch); err != nil {
			span.RecordError(err)
			return "", fmt.Errorf("git fetch origin %s: %w", defaultBranch, err)
		}
	}

	// Create the worktree
	worktreePath := filepath.Join(".claude", "worktrees", branchName)
	startPoint := defaultBranch
	if hasOrigin {
		startPoint = "origin/" + defaultBranch
	}

	// Remove stale worktree at this path from a previous run (e.g., re-enqueued vessel).
	// The -B flag handles branch name collisions, but git worktree add still fails
	// if the worktree PATH is already registered or exists on disk.
	if m.branchForWorktree(ctx, worktreePath) != "" {
		if _, removeErr := m.Runner.Run(ctx, "git", "worktree", "remove", worktreePath, "--force"); removeErr != nil {
			span.RecordError(removeErr)
			return "", fmt.Errorf("remove stale worktree %s: %w", worktreePath, removeErr)
		}
	} else if _, err := os.Stat(worktreePath); err == nil {
		// Directory exists on disk but isn't registered as a git worktree (orphaned).
		if removeErr := os.RemoveAll(worktreePath); removeErr != nil {
			span.RecordError(removeErr)
			return "", fmt.Errorf("remove orphaned worktree dir %s: %w", worktreePath, removeErr)
		}
	}

	// Also check if the BRANCH is already checked out at a different path.
	// This happens when a prior vessel's worktree was registered under a
	// different path (e.g., main-repo vs daemon-root) or when two concurrent
	// vessels target the same branch. Without this guard, `git worktree add
	// -B` fails with exit 128: "branch '<x>' is already used by worktree at
	// '<other path>'".
	if existingPath := m.pathForBranch(ctx, branchName); existingPath != "" {
		if _, removeErr := m.Runner.Run(ctx, "git", "worktree", "remove", existingPath, "--force"); removeErr != nil {
			// Prune may succeed even when remove fails (e.g., path already deleted).
			if _, pruneErr := m.Runner.Run(ctx, "git", "worktree", "prune"); pruneErr != nil {
				span.RecordError(removeErr)
				return "", fmt.Errorf("remove cross-path worktree %s for branch %s: %w", existingPath, branchName, removeErr)
			}
		}
	}

	if _, err := retryGitCmd(ctx, m.Runner, 3, "worktree", "add", worktreePath, "-B", branchName, startPoint); err != nil {
		span.RecordError(err)
		return "", fmt.Errorf("git worktree add: %w", err)
	}

	// Copy .claude/ config files from repo root into worktree (non-fatal)
	if err := m.copyClaudeConfig(worktreePath); err != nil {
		fmt.Fprintf(os.Stderr, "warn: copy .claude/ config: %v\n", err)
	}

	// Mark protected control surfaces as read-only to prevent agents from
	// accidentally deleting or modifying workflow/prompt definitions.
	if err := protectXylemSurfaces(worktreePath, m.protectedSurfacePatterns()); err != nil {
		fmt.Fprintf(os.Stderr, "warn: protect .xylem/ surfaces: %v\n", err)
	}
	span.AddAttributes(observability.WorktreeSpanAttributes(observability.WorktreeSpanData{
		Action: "create",
		Branch: branchName,
		Path:   worktreePath,
	}))

	return worktreePath, nil
}

// hasRemote checks whether a named remote exists (local-only, no network).
func (m *Manager) hasRemote(ctx context.Context, name string) bool {
	_, err := m.Runner.Run(ctx, "git", "remote", "get-url", name)
	return err == nil
}

// retryGitCmd runs a git command with exponential backoff on transient errors.
// It retries up to maxAttempts times, waiting 1s, 2s, 4s, ... between attempts.
// Only errors with retryable exit codes (1, 128, 255) are retried.
func retryGitCmd(ctx context.Context, runner CommandRunner, maxAttempts int, args ...string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, err := runner.Run(ctx, "git", appendDTUGitArgs(args, attempt)...)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if attempt < maxAttempts && isRetryableGitError(err) {
			backoff := func() time.Duration {
				const maxDuration = time.Duration(1<<63 - 1)
				if attempt <= 1 {
					return time.Second
				}
				delay := time.Second
				for i := 1; i < attempt; i++ {
					if delay >= maxDuration/2 {
						return maxDuration
					}
					delay = delay * 2
				}
				return delay
			}()
			log.Printf("worktree: retrying git %s (attempt %d/%d after %v): %v",
				args[0], attempt, maxAttempts, backoff, err)
			if err := dtu.RuntimeSleep(ctx, backoff); err != nil {
				return nil, err
			}
			continue
		}
		break
	}
	return nil, lastErr
}

func appendDTUGitArgs(args []string, attempt int) []string {
	out := append([]string(nil), args...)
	if !isDTUGitRun() {
		return out
	}
	return append(out, "--dtu-attempt", strconv.Itoa(attempt))
}

func isDTUGitRun() bool {
	return strings.TrimSpace(os.Getenv(dtu.EnvStatePath)) != ""
}

// exitCoder is satisfied by exec.ExitError and test doubles.
type exitCoder interface {
	ExitCode() int
}

// isRetryableGitError returns true if the error indicates a transient git failure
// that may succeed on retry (index lock contention, network errors, etc.).
func isRetryableGitError(err error) bool {
	if ec, ok := err.(exitCoder); ok {
		switch ec.ExitCode() {
		case 1, 128, 255:
			return true
		}
	}
	return false
}

// copyClaudeConfig copies selected .claude/ files from the repo root into the worktree.
// Copies: settings.json, settings.local.json, rules/
// Skips: worktrees/, conversations/, projects/ (session-specific)
func (m *Manager) copyClaudeConfig(worktreePath string) error {
	srcClaudeDir := filepath.Join(m.RepoRoot, ".claude")
	dstClaudeDir := filepath.Join(worktreePath, ".claude")

	if _, err := os.Stat(srcClaudeDir); os.IsNotExist(err) {
		return nil
	}

	allowlist := map[string]bool{
		"settings.json":       true,
		"settings.local.json": true,
		"rules":               true,
	}

	entries, err := os.ReadDir(srcClaudeDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !allowlist[entry.Name()] {
			continue
		}
		src := filepath.Join(srcClaudeDir, entry.Name())
		dst := filepath.Join(dstClaudeDir, entry.Name())
		if entry.IsDir() {
			if err := copyDir(src, dst); err != nil {
				return err
			}
		} else {
			if err := copyFile(src, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

// protectXylemSurfaces marks configured protected files as read-only in the
// worktree so agents cannot accidentally delete or modify them. It also writes
// a .claude/rules/ file instructing agents to leave them alone.
func protectXylemSurfaces(worktreePath string, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	for _, pattern := range patterns {
		worktreePattern := filepath.Join(worktreePath, filepath.FromSlash(pattern))
		matches, err := filepath.Glob(worktreePattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			os.Chmod(match, 0o444) //nolint:errcheck
		}
	}

	// Write a rule file so Claude agents know not to touch .xylem/ files.
	rulesDir := filepath.Join(worktreePath, ".claude", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		return err
	}
	rule := "Do not modify, delete, or move the configured protected control surfaces managed by the xylem control plane:\n"
	for _, pattern := range patterns {
		rule += "- " + pattern + "\n"
	}
	return os.WriteFile(filepath.Join(rulesDir, "protected-surfaces.md"), []byte(rule), 0o644)
}

// Remove removes a git worktree and its branch.
// It looks up the branch name from `git worktree list --porcelain` before removal
// to correctly handle branch names containing path separators (e.g., "fix/issue-42").
func (m *Manager) Remove(ctx context.Context, worktreePath string) error {
	span := observability.StartGlobalSpan(ctx, "worktree:remove", observability.WorktreeSpanAttributes(observability.WorktreeSpanData{
		Action: "remove",
		Path:   worktreePath,
	}))
	defer span.End()

	// Look up the branch name before removing the worktree, because
	// filepath.Base() would incorrectly truncate "fix/issue-42" to "issue-42".
	branchName := m.branchForWorktree(ctx, worktreePath)

	if _, err := m.Runner.Run(ctx, "git", "worktree", "remove", worktreePath, "--force"); err != nil {
		span.RecordError(err)
		return fmt.Errorf("git worktree remove: %w", err)
	}
	// Best-effort branch deletion using the real branch name
	if branchName != "" {
		m.Runner.Run(ctx, "git", "branch", "-d", branchName) //nolint:errcheck
	}
	span.AddAttributes(observability.WorktreeSpanAttributes(observability.WorktreeSpanData{
		Action: "remove",
		Branch: branchName,
		Path:   worktreePath,
	}))
	return nil
}

// branchForWorktree looks up the branch name for a worktree path by parsing
// `git worktree list --porcelain`. Returns empty string if not found.
func (m *Manager) branchForWorktree(ctx context.Context, worktreePath string) string {
	out, err := m.Runner.Run(ctx, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	// Normalize the target path for comparison
	absTarget := worktreePath
	if !filepath.IsAbs(worktreePath) {
		absTarget = filepath.Join(m.RepoRoot, worktreePath)
	}
	// Resolve to absolute path (handles relative RepoRoot like ".")
	absTarget, err = filepath.Abs(absTarget)
	if err != nil {
		return ""
	}
	absTarget = filepath.Clean(absTarget)

	for _, wt := range parsePorcelain(string(out)) {
		candidate := filepath.Clean(wt.Path)
		if candidate == absTarget {
			return wt.Branch
		}
	}
	return ""
}

// pathForBranch returns the path of the worktree currently holding the given
// branch, or empty string if no worktree holds it. Used to detect and clean
// up cross-path branch collisions before `git worktree add -B` would fail.
func (m *Manager) pathForBranch(ctx context.Context, branchName string) string {
	out, err := m.Runner.Run(ctx, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	for _, wt := range parsePorcelain(string(out)) {
		if wt.Branch == branchName {
			return wt.Path
		}
	}
	return ""
}

// List returns all git worktrees by parsing `git worktree list --porcelain`.
func (m *Manager) List(ctx context.Context) ([]WorktreeInfo, error) {
	out, err := m.Runner.Run(ctx, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parsePorcelain(string(out)), nil
}

// ListXylem returns only worktrees under .claude/worktrees/.
func (m *Manager) ListXylem(ctx context.Context) ([]WorktreeInfo, error) {
	all, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []WorktreeInfo
	for _, wt := range all {
		if strings.Contains(filepath.ToSlash(wt.Path), ".claude/worktrees/") {
			out = append(out, wt)
		}
	}
	return out, nil
}

// parsePorcelain parses `git worktree list --porcelain` output.
func parsePorcelain(output string) []WorktreeInfo {
	var results []WorktreeInfo
	var current WorktreeInfo
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				results = append(results, current)
			}
			current = WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			current.HeadCommit = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			current.Branch = strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	if current.Path != "" {
		results = append(results, current)
	}
	return results
}

// copyFile copies a single file src → dst, creating parent dirs as needed.
// It preserves the source file's permissions.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// copyDir recursively copies a directory src → dst.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		return copyFile(path, dstPath)
	})
}
