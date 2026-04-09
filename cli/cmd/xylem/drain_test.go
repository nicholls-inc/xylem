package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
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

type exportedSpanSnapshot struct {
	Name       string
	Attributes map[string]string
}

type recordingSpanExporter struct {
	mu            sync.Mutex
	spans         []exportedSpanSnapshot
	shutdownCalls int
}

type configurableWorktreeStub struct {
	patterns     []string
	removedPaths []string
}

func (w *configurableWorktreeStub) Create(context.Context, string) (string, error) { return "", nil }

func (w *configurableWorktreeStub) Remove(_ context.Context, worktreePath string) error {
	w.removedPaths = append(w.removedPaths, worktreePath)
	return nil
}

func (w *configurableWorktreeStub) SetProtectedSurfaces(patterns []string) {
	w.patterns = append([]string(nil), patterns...)
}

func (e *recordingSpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, span := range spans {
		attrs := make(map[string]string, len(span.Attributes()))
		for _, attr := range span.Attributes() {
			attrs[string(attr.Key)] = attrValueString(attr.Value)
		}
		e.spans = append(e.spans, exportedSpanSnapshot{
			Name:       span.Name(),
			Attributes: attrs,
		})
	}
	return nil
}

func (e *recordingSpanExporter) Shutdown(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shutdownCalls++
	return nil
}

func (e *recordingSpanExporter) snapshots() []exportedSpanSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]exportedSpanSnapshot, len(e.spans))
	for i, span := range e.spans {
		attrs := make(map[string]string, len(span.Attributes))
		for key, value := range span.Attributes {
			attrs[key] = value
		}
		out[i] = exportedSpanSnapshot{
			Name:       span.Name,
			Attributes: attrs,
		}
	}
	return out
}

func (e *recordingSpanExporter) shutdownCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.shutdownCalls
}

func attrValueString(value attribute.Value) string {
	switch value.Type() {
	case attribute.BOOL:
		if value.AsBool() {
			return "true"
		}
		return "false"
	case attribute.INT64:
		return value.Emit()
	case attribute.FLOAT64:
		return value.Emit()
	case attribute.STRING:
		return value.AsString()
	default:
		return value.Emit()
	}
}

func newRecordingTracer() (*observability.Tracer, *recordingSpanExporter) {
	exporter := &recordingSpanExporter{}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	return observability.NewTracerFromProvider(provider), exporter
}

func stubConfiguredTracerFactory(t *testing.T, fn func(observability.TracerConfig) (*observability.Tracer, error)) {
	t.Helper()

	prev := newConfiguredTracer
	newConfiguredTracer = fn
	t.Cleanup(func() {
		newConfiguredTracer = prev
	})
}

func withBufferedDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(newTextLogger(&buf))
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})
	return &buf
}

func requireSpanNamed(t *testing.T, spans []exportedSpanSnapshot, want string) exportedSpanSnapshot {
	t.Helper()

	for _, span := range spans {
		if span.Name == want {
			return span
		}
	}
	t.Fatalf("span %q not found in %#v", want, spans)
	return exportedSpanSnapshot{}
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
	if r.Tracer == nil {
		t.Fatal("r.Tracer = nil, want stdout tracer when observability is enabled")
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

func TestBuildDrainRunnerPropagatesProtectedSurfaces(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Harness.ProtectedSurfaces.Paths = []string{"docs/*.txt"}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	wt := &configurableWorktreeStub{}
	cmdRunner := newCmdRunner(cfg)

	r, cleanup := buildDrainRunner(cfg, q, wt, cmdRunner)
	defer cleanup()

	if r == nil {
		t.Fatal("buildDrainRunner() returned nil runner")
	}
	if got := wt.patterns; len(got) != 1 || got[0] != "docs/*.txt" {
		t.Fatalf("protected surfaces = %#v, want %#v", got, []string{"docs/*.txt"})
	}
}

func TestSmoke_S8_TracerInitializationFailureLogsWarningAndContinuesWithoutTracing(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = "collector:4317"
	cfg.Observability.Insecure = true
	cfg.Claude.Command = "true"
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)
	wt := &configurableWorktreeStub{}
	logs := withBufferedDefaultLogger(t)
	worktreePath := filepath.Join(dir, "prompt-worktree")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", worktreePath, err)
	}
	if _, err := q.Enqueue(queue.Vessel{
		ID:           "prompt-1",
		Source:       "manual",
		Prompt:       "print hello",
		WorktreePath: worktreePath,
		State:        queue.StatePending,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	stubConfiguredTracerFactory(t, func(observability.TracerConfig) (*observability.Tracer, error) {
		return nil, errors.New("bridge down")
	})

	runnerInstance, cleanup := buildDrainRunner(cfg, q, wt, cmdRunner)
	defer cleanup()

	require.Nil(t, runnerInstance.Tracer)
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), "msg=\"initialize tracer\"")
	assert.Contains(t, logs.String(), "error=\"bridge down\"")

	result, err := runnerInstance.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	assert.Zero(t, result.Failed)
	assert.Zero(t, result.Skipped)
	assert.Zero(t, result.Waiting)

	vessel, findErr := q.FindByID("prompt-1")
	require.NoError(t, findErr)
	assert.Equal(t, queue.StateCompleted, vessel.State)
	require.Len(t, wt.removedPaths, 1)
	assert.Equal(t, worktreePath, wt.removedPaths[0])
}

func TestSmoke_S30_TracerWiredInDrainGoAfterConfigLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = "localhost:4317"
	cfg.Observability.Insecure = true
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)
	tracer, exporter := newRecordingTracer()

	stubConfiguredTracerFactory(t, func(observability.TracerConfig) (*observability.Tracer, error) {
		return tracer, nil
	})

	r, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	require.NotNil(t, r.Tracer)
	require.Zero(t, exporter.shutdownCount())

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, runner.DrainResult{}, result)

	spans := exporter.snapshots()
	drainSpan := requireSpanNamed(t, spans, "drain_run")
	assert.Equal(t, "2", drainSpan.Attributes["xylem.drain.concurrency"])
	assert.Equal(t, "30m", drainSpan.Attributes["xylem.drain.timeout"])

	assert.Zero(t, exporter.shutdownCount())
	cleanup()
	assert.Equal(t, 1, exporter.shutdownCount())
}

func TestSmoke_S32_TracerShutdownDeferredInBothDrainAndDaemonPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	var exporters []*recordingSpanExporter
	stubConfiguredTracerFactory(t, func(observability.TracerConfig) (*observability.Tracer, error) {
		tracer, exporter := newRecordingTracer()
		exporters = append(exporters, exporter)
		return tracer, nil
	})

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	drainRunner, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	require.NotNil(t, drainRunner.Tracer)
	result, err := drainRunner.Drain(cancelledCtx)
	require.NoError(t, err)
	assert.Equal(t, runner.DrainResult{}, result)
	require.Len(t, exporters, 1)
	assert.Zero(t, exporters[0].shutdownCount())
	requireSpanNamed(t, exporters[0].snapshots(), "drain_run")

	cleanup()
	assert.Equal(t, 1, exporters[0].shutdownCount())

	daemonResult, err := runDrain(cancelledCtx, cfg, q, worktree.New(dir, cmdRunner), 0)
	require.NoError(t, err)
	assert.Equal(t, runner.DrainResult{}, daemonResult)
	require.Len(t, exporters, 2)
	requireSpanNamed(t, exporters[1].snapshots(), "drain_run")
	assert.Equal(t, 1, exporters[1].shutdownCount())
}
