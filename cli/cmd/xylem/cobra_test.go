package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

// setupTestDeps injects test dependencies into the global deps variable,
// bypassing PersistentPreRunE which requires gh/git on PATH and a real config.
func setupTestDeps(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	deps = &appDeps{
		cfg: &config.Config{
			Repo:     "owner/repo",
			StateDir: dir,
			Exclude:  []string{},
			Tasks:    map[string]config.Task{},
			Claude:   config.ClaudeConfig{Command: "claude"},
		},
		q:  queue.New(filepath.Join(dir, "queue.jsonl")),
		wt: worktree.New(dir, &emptyWorktreeRunner{}),
	}
}

func TestCobraSubcommandRegistration(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	hidden := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
		hidden[sub.Name()] = sub.Hidden
	}

	expected := []string{"init", "bootstrap", "continuous-improvement", "dtu", "shim-dispatch", "scan", "drain", "review", "gap-report", "lessons", "status", "pause", "resume", "cancel", "cleanup", "enqueue", "daemon", "retry", "visualize", "version"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected subcommand %q to be registered", name)
		}
	}
	if len(cmd.Commands()) != len(expected) {
		t.Errorf("expected %d subcommands, got %d", len(expected), len(cmd.Commands()))
	}
	if !hidden["shim-dispatch"] {
		t.Errorf("expected shim-dispatch to be hidden")
	}
}

func TestCobraStatusJsonFlag(t *testing.T) {
	setupTestDeps(t)
	cmd := newRootCmd()
	cmd.PersistentPreRunE = nil
	cmd.SetArgs([]string{"status", "--json"})

	out := captureStdout(func() {
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	trimmed := strings.TrimSpace(out)
	if trimmed != "[]" {
		t.Errorf("expected '[]' for --json empty status, got: %q", trimmed)
	}
}
