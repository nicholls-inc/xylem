package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	"github.com/nicholls-inc/xylem/cli/internal/source"
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

type smokeCommandRunner struct {
	runHook       func(context.Context, string, ...string) ([]byte, error)
	runOutputHook func(context.Context, string, ...string) ([]byte, error)
	processHook   func(context.Context, string, string, ...string) error
	phaseHook     func(context.Context, string, string, string, ...string) ([]byte, error)
}

func (w *configurableWorktreeStub) Create(context.Context, string) (string, error) { return "", nil }

func (w *configurableWorktreeStub) Remove(_ context.Context, worktreePath string) error {
	w.removedPaths = append(w.removedPaths, worktreePath)
	return nil
}

func (w *configurableWorktreeStub) SetProtectedSurfaces(patterns []string) {
	w.patterns = append([]string(nil), patterns...)
}

func (r *smokeCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.runHook != nil {
		return r.runHook(ctx, name, args...)
	}
	return []byte("[]"), nil
}

func (r *smokeCommandRunner) RunOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.runOutputHook != nil {
		return r.runOutputHook(ctx, name, args...)
	}
	return []byte("[]"), nil
}

func (r *smokeCommandRunner) RunProcess(ctx context.Context, dir string, name string, args ...string) error {
	if r.processHook != nil {
		return r.processHook(ctx, dir, name, args...)
	}
	return nil
}

func (r *smokeCommandRunner) RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.RunPhaseWithEnv(ctx, dir, nil, stdin, name, args...)
}

func (r *smokeCommandRunner) RunPhaseWithEnv(ctx context.Context, dir string, _ []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	prompt, err := io.ReadAll(stdin)
	if err != nil {
		return nil, err
	}
	if r.phaseHook != nil {
		return r.phaseHook(ctx, dir, string(prompt), name, args...)
	}
	return []byte("mock output"), nil
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

func stubCommandRunnerFactory(t *testing.T, fn func(*config.Config) drainCommandRunner) {
	t.Helper()

	prev := newCommandRunner
	newCommandRunner = fn
	t.Cleanup(func() {
		newCommandRunner = prev
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

func enqueuePromptVessel(t *testing.T, q *queue.Queue, id string, worktreePath string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(worktreePath, 0o755))
	_, err := q.Enqueue(queue.Vessel{
		ID:           id,
		Source:       "manual",
		Prompt:       "print hello",
		WorktreePath: worktreePath,
		State:        queue.StatePending,
		CreatedAt:    time.Now().UTC(),
	})
	require.NoError(t, err)
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

func TestDrainDryRunStartsCommandSpan(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = "localhost:4317"
	cfg.Observability.Insecure = true
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	wt := worktree.New(dir, newCmdRunner(cfg))

	now := time.Now().UTC()
	_, err := q.Enqueue(queue.Vessel{
		ID:        "issue-1",
		Source:    "github-issue",
		Ref:       "https://github.com/owner/repo/issues/1",
		Workflow:  "fix-bug",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	require.NoError(t, err)

	tracer, exporter := newRecordingTracer()
	stubConfiguredTracerFactory(t, func(observability.TracerConfig) (*observability.Tracer, error) {
		return tracer, nil
	})

	out := captureStdout(func() {
		require.NoError(t, cmdDrain(cfg, q, wt, true))
	})

	assert.Contains(t, out, "dry-run")
	span := requireSpanNamed(t, exporter.snapshots(), "command:drain")
	assert.Equal(t, "drain", span.Attributes["xylem.command.name"])
	assert.Equal(t, "true", span.Attributes["xylem.command.dry_run"])
	assert.Equal(t, dir, span.Attributes["xylem.command.state_dir"])
	assert.Equal(t, 1, exporter.shutdownCount())
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
		t.Fatal("r.Tracer = nil, want stdout tracer without an observability endpoint")
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

func TestBuildDrainRunnerRegistersBuiltinLessonsWorkflow(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	r, cleanup := buildDrainRunner(cfg, q, &configurableWorktreeStub{}, cmdRunner)
	defer cleanup()

	if r == nil {
		t.Fatal("buildDrainRunner() returned nil runner")
	}
	if _, ok := r.BuiltinWorkflows["lessons"]; !ok {
		t.Fatal("expected lessons builtin workflow to be registered")
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

func TestBuildConfiguredTracerWithoutEndpointUsesStdoutExporter(t *testing.T) {
	cfg := makeDrainConfig(t.TempDir())
	tracer := buildConfiguredTracer(cfg)
	require.NotNil(t, tracer)
	assert.NoError(t, tracer.Shutdown(context.Background()))
}

func TestSmoke_S30_TracerWiredInDrainGoAfterConfigLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = ""
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := &smokeCommandRunner{}
	stubCommandRunnerFactory(t, func(*config.Config) drainCommandRunner {
		return cmdRunner
	})
	wt := worktree.New(dir, cmdRunner)
	enqueuePromptVessel(t, q, "prompt-1", filepath.Join(dir, "prompt-worktree"))

	r, cleanup := buildDrainRunner(cfg, q, wt, cmdRunner)
	require.NotNil(t, r.Tracer)
	cleanup()

	out := captureStdout(func() {
		require.NoError(t, cmdDrain(cfg, q, wt, false))
	})

	assert.Contains(t, out, "Completed 1, failed 0, skipped 0, waiting 0")
	assert.Contains(t, out, `"Name":"drain_run"`)
}

func TestSmoke_S32_TracerShutdownDeferredInBothDrainAndDaemonPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = ""
	cfg.Concurrency = 2
	cmdRunner := &smokeCommandRunner{}
	stubCommandRunnerFactory(t, func(*config.Config) drainCommandRunner {
		return cmdRunner
	})

	makePhaseGate := func() (chan string, chan struct{}) {
		started := make(chan string, 2)
		release := make(chan struct{})
		cmdRunner.phaseHook = func(_ context.Context, _ string, _ string, _ string, _ ...string) ([]byte, error) {
			started <- "started"
			<-release
			return []byte("ok"), nil
		}
		return started, release
	}

	runInterruptedDrain := func(t *testing.T, started chan string, release chan struct{}, invoke func(context.Context) (runner.DrainResult, error)) string {
		t.Helper()

		ctx, cancel := context.WithCancel(context.Background())
		return captureStdout(func() {
			ready := make(chan struct{})
			go func() {
				defer close(ready)
				<-started
				<-started
				cancel()
				close(release)
			}()

			result, err := invoke(ctx)
			require.NoError(t, err)
			assert.Equal(t, 2, result.Completed)
			assert.Zero(t, result.Failed)
			assert.Zero(t, result.Waiting)
			<-ready
		})
	}

	drainQueue := queue.New(filepath.Join(dir, "drain-queue.jsonl"))
	enqueuePromptVessel(t, drainQueue, "drain-1", filepath.Join(dir, "drain-one"))
	enqueuePromptVessel(t, drainQueue, "drain-2", filepath.Join(dir, "drain-two"))
	drainStarted, drainRelease := makePhaseGate()
	drainOut := runInterruptedDrain(t, drainStarted, drainRelease, func(ctx context.Context) (runner.DrainResult, error) {
		drainRunner, cleanup := buildDrainRunner(cfg, drainQueue, &configurableWorktreeStub{}, cmdRunner)
		result, err := drainRunner.DrainAndWait(ctx)
		cleanup()
		return result, err
	})
	assert.Contains(t, drainOut, `"Name":"drain_run"`)
	assert.Equal(t, 1, strings.Count(drainOut, `"Name":"vessel:drain-1"`))
	assert.Equal(t, 1, strings.Count(drainOut, `"Name":"vessel:drain-2"`))

	daemonQueue := queue.New(filepath.Join(dir, "daemon-queue.jsonl"))
	enqueuePromptVessel(t, daemonQueue, "daemon-1", filepath.Join(dir, "daemon-one"))
	enqueuePromptVessel(t, daemonQueue, "daemon-2", filepath.Join(dir, "daemon-two"))
	daemonStarted, daemonRelease := makePhaseGate()
	daemonOut := runInterruptedDrain(t, daemonStarted, daemonRelease, func(ctx context.Context) (runner.DrainResult, error) {
		return runDrain(ctx, cfg, daemonQueue, worktree.New(dir, cmdRunner), 0)
	})
	assert.Contains(t, daemonOut, `"Name":"drain_run"`)
	assert.Equal(t, 1, strings.Count(daemonOut, `"Name":"vessel:daemon-1"`))
	assert.Equal(t, 1, strings.Count(daemonOut, `"Name":"vessel:daemon-2"`))
}
