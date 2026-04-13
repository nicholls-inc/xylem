package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ensureDaemonNotInMainWorktree verifies that the current working directory
// is NOT the main git worktree. If it is, the function refuses to continue —
// the daemon must run in an isolated worktree so that vessel subprocesses
// cannot switch branches on the user's primary checkout.
//
// Returns an error with instructions for creating a dedicated daemon
// worktree, or nil if already running in an isolated worktree.
func ensureDaemonNotInMainWorktree() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("absolute working directory: %w", err)
	}

	mainWT, err := findMainWorktree()
	if err != nil {
		// Not a git repo or git unavailable — let the daemon proceed.
		return nil
	}
	absMain, err := filepath.Abs(mainWT)
	if err != nil {
		return nil
	}

	if absCwd != absMain {
		// Already in a secondary worktree — safe.
		return nil
	}

	return fmt.Errorf("daemon must run in an isolated worktree, not the main repo at %s; "+
		"vessel subprocesses may switch branches or modify the working tree, which would corrupt "+
		"your primary checkout. Create a dedicated daemon worktree and start the daemon from there: "+
		"`git worktree add .xylem/worktrees/.daemon-root main && cd .xylem/worktrees/.daemon-root && xylem daemon`. "+
		"Or set XYLEM_DAEMON_ALLOW_MAIN_WORKTREE=1 to bypass this check (not recommended)", absMain)
}

// findMainWorktree returns the absolute path of the main git worktree.
// Parses `git worktree list --porcelain` and returns the first entry.
func findMainWorktree() (string, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			return strings.TrimPrefix(line, "worktree "), nil
		}
	}
	return "", fmt.Errorf("no worktree entries in `git worktree list --porcelain`")
}

// daemonIsolationOverride reports whether the user has set the environment
// variable to bypass the main-worktree check. Used for testing and edge
// cases where the daemon legitimately needs to run in the main worktree.
func daemonIsolationOverride() bool {
	return os.Getenv("XYLEM_DAEMON_ALLOW_MAIN_WORKTREE") == "1"
}

// logDaemonWorktreeCheck runs the isolation check and logs the result.
// Returns true if the daemon should proceed, false if it should exit.
func logDaemonWorktreeCheck() bool {
	if daemonIsolationOverride() {
		slog.Warn("daemon isolation check bypassed", "env", "XYLEM_DAEMON_ALLOW_MAIN_WORKTREE")
		return true
	}
	if err := ensureDaemonNotInMainWorktree(); err != nil {
		slog.Error("daemon isolation check failed", "error", err)
		return false
	}
	return true
}
