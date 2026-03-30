package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

func makeDaemonConfig(dir string) *config.Config {
	return &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude:      config.ClaudeConfig{Command: "claude", Template: "{{.Command}} -p \"/{{.Workflow}} {{.Ref}}\" --max-turns {{.MaxTurns}}"},
		Sources: map[string]config.SourceConfig{
			"github": {
				Type:    "github",
				Repo:    "owner/repo",
				Exclude: []string{"wontfix"},
				Tasks:   map[string]config.Task{"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
			},
		},
	}
}

func TestDaemonShutdown(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDaemonConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	wt := worktree.New(dir, &emptyWorktreeRunner{})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, cfg, q, wt, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("expected nil error on shutdown, got: %v", err)
	}
}

func TestParseDaemonIntervals(t *testing.T) {
	tests := []struct {
		name              string
		scanInterval      string
		drainInterval     string
		expectedScan      time.Duration
		expectedDrain     time.Duration
	}{
		{"defaults", "", "", 60 * time.Second, 30 * time.Second},
		{"custom scan", "120s", "", 120 * time.Second, 30 * time.Second},
		{"custom drain", "", "15s", 60 * time.Second, 15 * time.Second},
		{"both custom", "90s", "45s", 90 * time.Second, 45 * time.Second},
		{"invalid scan falls back to default", "not-a-duration", "", 60 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scan, drain := parseDaemonIntervals(config.DaemonConfig{
				ScanInterval:  tt.scanInterval,
				DrainInterval: tt.drainInterval,
			})
			if scan != tt.expectedScan {
				t.Errorf("scan interval: got %s, want %s", scan, tt.expectedScan)
			}
			if drain != tt.expectedDrain {
				t.Errorf("drain interval: got %s, want %s", drain, tt.expectedDrain)
			}
		})
	}
}

func TestDaemonNonBlockingDrain(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDaemonConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	wt := worktree.New(dir, &emptyWorktreeRunner{})

	now := time.Now().UTC()
	q.Enqueue(queue.Vessel{ID: "v1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck

	// drainInterval=1ms ensures the drain fires immediately. The loop should
	// return promptly when the context is cancelled — it must NOT block waiting
	// for the drain to finish.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := daemonLoop(ctx, cfg, q, wt, time.Hour, time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error on shutdown, got: %v", err)
	}
	// If drain were blocking, this would hang for the full vessel timeout.
	// 500ms is generous: the loop should return well within the 200ms ctx timeout.
	if elapsed > 500*time.Millisecond {
		t.Errorf("daemonLoop blocked for %s — drain is not running in background", elapsed)
	}
}

func TestLogTickSummary(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()

	q.Enqueue(queue.Vessel{ID: "v1", Source: "manual", State: queue.StatePending, CreatedAt: now})   //nolint:errcheck
	q.Enqueue(queue.Vessel{ID: "v2", Source: "manual", State: queue.StateCompleted, CreatedAt: now}) //nolint:errcheck

	// logTickSummary should not panic on any queue state
	logTickSummary(q)
}
