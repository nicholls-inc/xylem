package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestBuildSourceMapIncludesScheduledSource(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Sources = map[string]config.SourceConfig{
		"sota-gap": {
			Type:     "scheduled",
			Repo:     "owner/repo",
			Schedule: "@weekly",
			Tasks: map[string]config.Task{
				"weekly-self-gap-analysis": {
					Workflow: "sota-gap-analysis",
					Ref:      "sota-gap-analysis",
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	sources := buildSourceMap(cfg, q, newCmdRunner(cfg))
	scheduled, ok := sources["sota-gap"]
	if !ok {
		t.Fatalf("scheduled source missing from source map: %#v", sources)
	}
	scheduledSource, ok := scheduled.(*source.Scheduled)
	if !ok {
		t.Fatalf("scheduled source type = %T, want *source.Scheduled", scheduled)
	}
	if scheduledSource.Repo != "owner/repo" {
		t.Fatalf("scheduled source repo = %q, want owner/repo", scheduledSource.Repo)
	}
	if scheduledSource.Schedule != "@weekly" {
		t.Fatalf("scheduled source schedule = %q, want @weekly", scheduledSource.Schedule)
	}
	task, ok := scheduledSource.Tasks["weekly-self-gap-analysis"]
	if !ok {
		t.Fatalf("scheduled task missing from source map: %#v", scheduledSource.Tasks)
	}
	if task.Workflow != "sota-gap-analysis" {
		t.Fatalf("scheduled task workflow = %q, want sota-gap-analysis", task.Workflow)
	}
	if task.Ref != "sota-gap-analysis" {
		t.Fatalf("scheduled task ref = %q, want sota-gap-analysis", task.Ref)
	}
	if _, ok := sources["scheduled"]; !ok {
		t.Fatalf("scheduled runtime alias missing from source map: %#v", sources)
	}
}

func TestBuildSourceMapPreservesDistinctScheduledConfigNames(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Sources = map[string]config.SourceConfig{
		"sota-gap": {
			Type:     "scheduled",
			Repo:     "owner/repo",
			Schedule: "@weekly",
			Tasks: map[string]config.Task{
				"weekly-self-gap-analysis": {
					Workflow: "sota-gap-analysis",
					Ref:      "sota-gap-analysis",
				},
			},
		},
		"release-gap": {
			Type:     "scheduled",
			Repo:     "owner/other-repo",
			Schedule: "@daily",
			Tasks: map[string]config.Task{
				"daily-self-gap-analysis": {
					Workflow: "sota-gap-analysis",
					Ref:      "release-gap-analysis",
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	sources := buildSourceMap(cfg, q, newCmdRunner(cfg))
	first, ok := sources["sota-gap"]
	if !ok {
		t.Fatalf("sota-gap source missing from source map: %#v", sources)
	}
	second, ok := sources["release-gap"]
	if !ok {
		t.Fatalf("release-gap source missing from source map: %#v", sources)
	}
	firstScheduled, ok := first.(*source.Scheduled)
	if !ok {
		t.Fatalf("sota-gap source type = %T, want *source.Scheduled", first)
	}
	secondScheduled, ok := second.(*source.Scheduled)
	if !ok {
		t.Fatalf("release-gap source type = %T, want *source.Scheduled", second)
	}
	if firstScheduled.Repo != "owner/repo" {
		t.Fatalf("sota-gap repo = %q, want owner/repo", firstScheduled.Repo)
	}
	if secondScheduled.Repo != "owner/other-repo" {
		t.Fatalf("release-gap repo = %q, want owner/other-repo", secondScheduled.Repo)
	}
}

func TestBuildReporterUsesScheduledSourceRepo(t *testing.T) {
	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"sota-gap": {
				Type: "scheduled",
				Repo: "owner/repo",
			},
		},
	}

	reporter := buildReporter(cfg, nil)
	if reporter == nil {
		t.Fatal("buildReporter() = nil, want reporter")
	}
	if reporter.Repo != "owner/repo" {
		t.Fatalf("reporter repo = %q, want owner/repo", reporter.Repo)
	}
}

func TestSmoke_S8_TracerInitializationFailureLogsWarningAndContinuesWithoutTracing(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Claude.Command = "true"
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)
	wt := &configurableWorktreeStub{}
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

	oldNewTracer := newTracer
	defer func() { newTracer = oldNewTracer }()
	newTracer = func(observability.TracerConfig) (*observability.Tracer, error) {
		return nil, errors.New("boom")
	}

	var logBuf bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLogWriter)

	runnerInstance, cleanup := buildDrainRunner(cfg, q, wt, cmdRunner)
	defer cleanup()

	assert.Nil(t, runnerInstance.Tracer)

	result, err := runnerInstance.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	assert.Zero(t, result.Failed)
	assert.Zero(t, result.Skipped)
	assert.Zero(t, result.Waiting)
	assert.Contains(t, logBuf.String(), "warn: failed to initialize tracer: boom")

	vessel, findErr := q.FindByID("prompt-1")
	require.NoError(t, findErr)
	assert.Equal(t, queue.StateCompleted, vessel.State)
	require.Len(t, wt.removedPaths, 1)
	assert.Equal(t, worktreePath, wt.removedPaths[0])
}

func TestSmoke_S30_TracerWiredInDrainAfterConfigLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	r, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	defer cleanup()

	assert.NotNil(t, r.Tracer)
}

func TestSmoke_S32_TracerShutdownDeferredInDrainPath(t *testing.T) {
	oldNewTracer := newTracer
	defer func() { newTracer = oldNewTracer }()

	exporter := &recordingExporter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	newTracer = func(observability.TracerConfig) (*observability.Tracer, error) {
		return observability.NewTracerFromProvider(tp), nil
	}

	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	_, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	cleanup()

	assert.True(t, exporter.shutdownCalled)
}
