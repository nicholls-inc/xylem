package main

import (
	"encoding/json"
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
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}

	expected := []string{"init", "scan", "drain", "status", "pause", "resume", "cancel", "cleanup", "enqueue", "daemon", "retry"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected subcommand %q to be registered", name)
		}
	}
	if len(cmd.Commands()) != len(expected) {
		t.Errorf("expected %d subcommands, got %d", len(expected), len(cmd.Commands()))
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

	// --json with empty queue should output a valid statusOutput with empty arrays
	trimmed := strings.TrimSpace(out)
	var result statusOutput
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		t.Fatalf("expected valid JSON statusOutput, got: %q\nerr: %v", trimmed, err)
	}
	if len(result.Vessels) != 0 {
		t.Errorf("expected empty vessels in JSON, got %d", len(result.Vessels))
	}
	if result.Anomalies == nil {
		t.Error("expected anomalies field to be present (not null)")
	}
}

