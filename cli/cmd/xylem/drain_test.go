package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func makeDrainConfig(dir string) *config.Config {
	return &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Claude: config.ClaudeConfig{
			Command: "claude",
		},
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

type recordingExporter struct {
	shutdownCalled bool
}

func (e *recordingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return nil
}

func (e *recordingExporter) Shutdown(context.Context) error {
	e.shutdownCalled = true
	return nil
}

func TestDrainDryRun(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	now := time.Now().UTC()
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-1", Source: "github-issue",
		Ref:      "https://github.com/owner/repo/issues/1",
		Workflow: "fix-bug", State: queue.StatePending, CreatedAt: now,
	})
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-2", Source: "github-issue",
		Ref:      "https://github.com/owner/repo/issues/2",
		Workflow: "fix-bug", State: queue.StatePending, CreatedAt: now,
	})

	out := captureStdout(func() {
		err := dryRunDrain(cfg, q)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	vessels, _ := q.ListByState(queue.StatePending)
	if len(vessels) != 2 {
		t.Errorf("dry-run should not drain queue, got %d pending", len(vessels))
	}
	if !strings.Contains(out, "issue-1") {
		t.Errorf("expected vessel in dry-run output, got: %s", out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run notice in output, got: %s", out)
	}
}

func TestDrainDryRunCommandFormat(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	now := time.Now().UTC()
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-1", Source: "github-issue",
		Ref:      "https://github.com/owner/repo/issues/1",
		Workflow: "fix-bug", State: queue.StatePending, CreatedAt: now,
	})

	out := captureStdout(func() {
		err := dryRunDrain(cfg, q)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	expectedCmd := `claude -p "/fix-bug https://github.com/owner/repo/issues/1" --max-turns 50`
	if !strings.Contains(out, expectedCmd) {
		t.Errorf("expected command %q in output, got: %s", expectedCmd, out)
	}
	if !strings.Contains(out, "ID") || !strings.Contains(out, "Source") ||
		!strings.Contains(out, "Workflow") || !strings.Contains(out, "Command") {
		t.Errorf("expected table headers (ID, Source, Workflow, Command), got: %s", out)
	}
	if !strings.Contains(out, "1 vessel(s) would be drained") {
		t.Errorf("expected count message, got: %s", out)
	}
}

func TestDrainDryRunEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	out := captureStdout(func() {
		err := dryRunDrain(cfg, q)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(out, "No pending") {
		t.Errorf("expected empty message, got: %s", out)
	}
}

func TestDrainDryRunQueueReadError(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(dir)

	err := dryRunDrain(cfg, q)
	if err == nil {
		t.Fatal("expected error from dryRunDrain with bad queue path")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exitError, got %T: %v", err, err)
	}
	if ee.code != 2 {
		t.Errorf("expected exit code 2, got %d", ee.code)
	}
}

// Covers: WS1 S27.
func TestBuildDrainRunnerWiresSharedScaffolding(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	r, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	defer cleanup()

	if r.Intermediary == nil {
		t.Fatal("r.Intermediary = nil, want intermediary")
	}
	if r.AuditLog == nil {
		t.Fatal("r.AuditLog = nil, want audit log")
	}
	// Tracer is nil when no OTLP endpoint is configured.
	if r.Tracer != nil {
		t.Fatal("r.Tracer != nil, want nil when no OTLP endpoint configured")
	}

	result := r.Intermediary.Evaluate(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/HARNESS.md",
		AgentID:  "issue-1",
	})
	if result.Effect != intermediary.Deny {
		t.Fatalf("default protected-surface effect = %q, want %q", result.Effect, intermediary.Deny)
	}

	entry := intermediary.AuditEntry{
		Intent: intermediary.Intent{
			Action:   "phase_execute",
			Resource: "fix",
			AgentID:  "issue-1",
		},
		Decision:  intermediary.Allow,
		Timestamp: time.Now().UTC(),
	}
	if err := r.AuditLog.Append(entry); err != nil {
		t.Fatalf("AuditLog.Append() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.jsonl")); err != nil {
		t.Fatalf("Stat(audit.jsonl) error = %v", err)
	}
}

func TestSmoke_S8_TracerInitFailureRunnerContinues(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	runnerInstance, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	defer cleanup()
	runnerInstance.Tracer = nil

	result, err := runnerInstance.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result != (runner.DrainResult{}) {
		t.Fatalf("DrainResult = %+v, want zero value", result)
	}
}

func TestSmoke_S30_TracerWiredWhenEndpointConfigured(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = "localhost:4317"
	cfg.Observability.Insecure = true
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	r, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	defer cleanup()

	if r.Tracer == nil {
		t.Fatal("r.Tracer = nil, want tracer when endpoint configured")
	}
}

func TestSmoke_S32_TracerShutdownDeferred(t *testing.T) {
	exporter := &recordingExporter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := observability.NewTracerFromProvider(tp)

	shutdownConfiguredTracer(tracer)

	if !exporter.shutdownCalled {
		t.Fatal("exporter shutdown was not called")
	}
}
