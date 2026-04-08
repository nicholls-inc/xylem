package main

import (
	"os"
	"testing"
)

func TestDaemonIsolationOverride(t *testing.T) {
	t.Setenv("XYLEM_DAEMON_ALLOW_MAIN_WORKTREE", "1")
	if !daemonIsolationOverride() {
		t.Error("expected daemonIsolationOverride() = true with env var set to 1")
	}

	t.Setenv("XYLEM_DAEMON_ALLOW_MAIN_WORKTREE", "")
	if daemonIsolationOverride() {
		t.Error("expected daemonIsolationOverride() = false with env var unset")
	}

	t.Setenv("XYLEM_DAEMON_ALLOW_MAIN_WORKTREE", "true")
	if daemonIsolationOverride() {
		t.Error("expected daemonIsolationOverride() = false with non-1 value")
	}
}

func TestEnsureDaemonNotInMainWorktree_NotARepo(t *testing.T) {
	// Use a temp dir that is NOT a git repo — findMainWorktree should fail
	// and ensureDaemonNotInMainWorktree should return nil (permissive).
	dir := t.TempDir()
	oldCwd, _ := os.Getwd()
	defer os.Chdir(oldCwd) //nolint:errcheck
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	if err := ensureDaemonNotInMainWorktree(); err != nil {
		t.Errorf("expected nil for non-git dir, got %v", err)
	}
}
