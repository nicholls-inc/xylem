package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/dtushim"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type daemonDTUCmdRunner struct {
	env []string
}

func (r *daemonDTUCmdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := dtushim.Execute(ctx, name, args, nil, &stdout, &stderr, r.env)
	if code != 0 {
		return stdout.Bytes(), &daemonDTUExitError{code: code, stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

type daemonDTUExitError struct {
	code   int
	stderr string
}

func (e *daemonDTUExitError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return fmt.Sprintf("exit status %d: %s", e.code, msg)
}

func (e *daemonDTUExitError) ExitCode() int {
	return e.code
}

func daemonDTUFixturePath(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "internal", "dtu", "testdata", name)
}

func newDaemonDTUEnv(t *testing.T, fixtureName string) (string, *dtu.Store, *queue.Queue, *daemonDTUCmdRunner) {
	t.Helper()

	repoDir := t.TempDir()
	stateDir := filepath.Join(repoDir, ".xylem")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", stateDir, err)
	}

	manifestPath := daemonDTUFixturePath(t, fixtureName)
	manifest, err := dtu.LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest(%q): %v", manifestPath, err)
	}
	state, err := dtu.NewState(manifest.Metadata.Name, manifest, manifestPath, time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	store, err := dtu.NewStore(stateDir, manifest.Metadata.Name)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	t.Setenv(dtu.EnvStatePath, store.Path())
	t.Setenv(dtu.EnvStateDir, stateDir)
	t.Setenv(dtu.EnvUniverseID, manifest.Metadata.Name)

	cmdRunner := &daemonDTUCmdRunner{
		env: []string{
			dtu.EnvStatePath + "=" + store.Path(),
			dtu.EnvStateDir + "=" + stateDir,
			dtu.EnvUniverseID + "=" + manifest.Metadata.Name,
		},
	}
	return repoDir, store, queue.New(filepath.Join(stateDir, "queue.jsonl")), cmdRunner
}

func loadDaemonDTUState(t *testing.T, store *dtu.Store) *dtu.State {
	t.Helper()

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return state
}

func readDaemonDTULabels(t *testing.T, store *dtu.Store, repoSlug string, issueNum int) []string {
	t.Helper()

	state := loadDaemonDTUState(t, store)
	repo := state.RepositoryBySlug(repoSlug)
	if repo == nil {
		t.Fatalf("RepositoryBySlug(%q) = nil", repoSlug)
	}
	issue := repo.IssueByNumber(issueNum)
	if issue == nil {
		t.Fatalf("IssueByNumber(%d) = nil", issueNum)
		return nil
	}
	return append([]string(nil), issue.Labels...)
}

func readDaemonDTUEvents(t *testing.T, store *dtu.Store) []dtu.Event {
	t.Helper()

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	return events
}

func assertLabelsEqual(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("labels = %v, want %v", got, want)
		}
	}
}

func withDaemonWorkingDir(t *testing.T, dir string) func() {
	t.Helper()

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%q): %v", dir, err)
	}
	return func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}
}

func noopScan(_ context.Context) (scanner.ScanResult, error) {
	return scanner.ScanResult{}, nil
}

func noopDrain(_ context.Context) (runner.DrainResult, error) {
	return runner.DrainResult{}, nil
}

type daemonNoopRunner struct{}

func (daemonNoopRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return []byte("[]"), nil
}

func TestDaemonLoopScheduledSourceRunsSingleTick(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"lessons": {
				Type:     "schedule",
				Cadence:  "@hourly",
				Workflow: "lessons",
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	tracker := &trackerStub{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scan := func(ctx context.Context) (scanner.ScanResult, error) {
		s := scanner.New(cfg, q, daemonNoopRunner{})
		return s.Scan(ctx)
	}
	drain := func(_ context.Context) (runner.DrainResult, error) {
		vessel, err := q.Dequeue()
		if err != nil {
			return runner.DrainResult{}, err
		}
		if vessel == nil {
			return runner.DrainResult{}, nil
		}
		if vessel.Source != "schedule" {
			t.Fatalf("vessel.Source = %q, want schedule", vessel.Source)
		}
		if err := q.Update(vessel.ID, queue.StateCompleted, ""); err != nil {
			return runner.DrainResult{}, err
		}
		cancel()
		return runner.DrainResult{Launched: 1, Completed: 1}, nil
	}

	if err := daemonLoop(ctx, q, tracker, scan, drain, nil, nil, 10*time.Millisecond, 10*time.Millisecond, 0); err != nil {
		t.Fatalf("daemonLoop() error = %v", err)
	}

	completed, err := q.ListByState(queue.StateCompleted)
	if err != nil {
		t.Fatalf("ListByState(completed) error = %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("len(completed) = %d, want 1", len(completed))
	}
}

type trackerStub struct {
	wg       sync.WaitGroup
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (t *trackerStub) Wait() runner.DrainResult {
	t.wg.Wait()
	return runner.DrainResult{}
}

func (t *trackerStub) InFlightCount() int {
	return int(t.inFlight.Load())
}

func (t *trackerStub) Start(duration time.Duration) {
	cur := t.inFlight.Add(1)
	for {
		old := t.maxSeen.Load()
		if cur <= old || t.maxSeen.CompareAndSwap(old, cur) {
			break
		}
	}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		time.Sleep(duration)
		t.inFlight.Add(-1)
	}()
}

func TestDaemonShutdown(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, nil, noopScan, noopDrain, nil, nil, time.Hour, time.Hour, 0)
	if err != nil {
		t.Fatalf("expected nil error on shutdown, got: %v", err)
	}
}

func TestSmoke_S31_TracerWiredInDaemonRunDrain(t *testing.T) {
	oldNewTracer := newTracer
	defer func() { newTracer = oldNewTracer }()

	exporter := &recordingExporter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	var calls int
	newTracer = func(cfg observability.TracerConfig) (*observability.Tracer, error) {
		calls++
		if cfg.Endpoint != "" {
			t.Fatalf("cfg.Endpoint = %q, want empty endpoint for stdout-mode tracer", cfg.Endpoint)
		}
		return observability.NewTracerFromProvider(tp), nil
	}

	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	result, err := runDrain(context.Background(), cfg, q, worktree.New(dir, newCmdRunner(cfg)), 0)
	require.NoError(t, err)
	assert.Equal(t, runner.DrainResult{}, result)
	assert.Equal(t, 1, calls)
}

func TestSmoke_S32_TracerShutdownDeferredInDaemonPath(t *testing.T) {
	oldNewTracer := newTracer
	defer func() { newTracer = oldNewTracer }()

	exporter := &recordingExporter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	newTracer = func(cfg observability.TracerConfig) (*observability.Tracer, error) {
		return observability.NewTracerFromProvider(tp), nil
	}

	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	_, err := runDrain(context.Background(), cfg, q, worktree.New(dir, newCmdRunner(cfg)), 0)
	require.NoError(t, err)
	assert.True(t, exporter.shutdownCalled)
}
func TestDaemonLoopPeriodicUpgradeFiresAtDrainEnd(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	var upgradeCalls atomic.Int32
	upgrade := func() { upgradeCalls.Add(1) }

	// drainInterval=2ms so drains fire rapidly; upgradeInterval=1ms so every
	// drain end should trigger an upgrade. Over 200ms we expect at least 5
	// upgrade calls, proving the drain-end check fires on every cycle.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, nil, noopScan, noopDrain, nil, upgrade, time.Hour, 2*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("daemonLoop() error = %v", err)
	}

	if got := upgradeCalls.Load(); got < 5 {
		t.Errorf("upgrade called %d times, want at least 5", got)
	}
}

func TestDaemonLoopPeriodicUpgradeRespectsInterval(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	var upgradeCalls atomic.Int32
	upgrade := func() { upgradeCalls.Add(1) }

	// drainInterval=2ms → drains fire rapidly (~50 drains in 100ms), but
	// upgradeInterval=10s → upgrade fires at most ONCE in that window.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, nil, noopScan, noopDrain, nil, upgrade, time.Hour, 2*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatalf("daemonLoop() error = %v", err)
	}

	// With lastUpgrade initialised to daemonNow() at startup and a 10s
	// interval, no upgrade should fire within 100ms.
	if got := upgradeCalls.Load(); got != 0 {
		t.Errorf("upgrade called %d times, want 0 (interval not elapsed)", got)
	}
}

func TestDaemonLoopPeriodicUpgradeNilDisables(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Passing nil upgrade should not panic even with a non-zero interval.
	err := daemonLoop(ctx, q, nil, noopScan, noopDrain, nil, nil, time.Hour, 2*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("daemonLoop() error = %v", err)
	}
}

// TestDaemonLoopUpgradeWaitsForDrainCompletion verifies the upgrade callback
// only fires AFTER the drain function returns — i.e., in a guaranteed idle
// window where no vessel subprocesses are alive.
func TestDaemonLoopUpgradeWaitsForDrainCompletion(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	var (
		drainActive atomic.Int32
		drainSeen   atomic.Int32
		upgradeSaw  atomic.Int32
	)
	slowDrain := func(_ context.Context) (runner.DrainResult, error) {
		drainActive.Store(1)
		drainSeen.Add(1)
		defer drainActive.Store(0)
		// Hold drain long enough for the tick loop to observe it.
		time.Sleep(20 * time.Millisecond)
		return runner.DrainResult{}, nil
	}
	upgrade := func() {
		// If the drain goroutine were still running when upgrade fires,
		// drainActive would be 1. Option A guarantees it's 0 (drain has
		// returned, but the defer clearing `draining` hasn't run yet).
		if drainActive.Load() != 0 {
			t.Errorf("upgrade fired while drain active")
		}
		upgradeSaw.Add(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, nil, noopScan, slowDrain, nil, upgrade, time.Hour, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("daemonLoop() error = %v", err)
	}

	if drainSeen.Load() < 2 {
		t.Errorf("expected at least 2 drain invocations, got %d", drainSeen.Load())
	}
	if upgradeSaw.Load() < 2 {
		t.Errorf("expected at least 2 upgrade invocations, got %d", upgradeSaw.Load())
	}
}

func TestSmoke_S35_DaemonLoopAllowsNewDrainTicksWhileVesselsRemainInFlight(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	tracker := &trackerStub{}

	var drainCalls atomic.Int32
	drain := func(_ context.Context) (runner.DrainResult, error) {
		drainCalls.Add(1)
		tracker.Start(80 * time.Millisecond)
		return runner.DrainResult{Launched: 1}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, tracker, noopScan, drain, nil, nil, time.Hour, 10*time.Millisecond, 0)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, drainCalls.Load(), int32(2))
	assert.GreaterOrEqual(t, tracker.maxSeen.Load(), int32(2))
}

func TestSmoke_S36_DaemonLoopUpgradeWaitsForTrackerIdle(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	tracker := &trackerStub{}

	var (
		launchedAt  atomic.Int64
		upgradedAt  atomic.Int64
		drainCalls  atomic.Int32
		upgradeSeen atomic.Int32
	)
	drain := func(_ context.Context) (runner.DrainResult, error) {
		if drainCalls.Add(1) == 1 {
			launchedAt.Store(time.Now().UnixNano())
			tracker.Start(60 * time.Millisecond)
			return runner.DrainResult{Launched: 1}, nil
		}
		return runner.DrainResult{}, nil
	}
	upgrade := func() {
		upgradedAt.Store(time.Now().UnixNano())
		upgradeSeen.Add(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, tracker, noopScan, drain, nil, upgrade, time.Hour, 10*time.Millisecond, time.Millisecond)
	require.NoError(t, err)

	assert.NotZero(t, upgradeSeen.Load())
	launchTime := time.Unix(0, launchedAt.Load())
	upgradeTime := time.Unix(0, upgradedAt.Load())
	assert.GreaterOrEqual(t, upgradeTime.Sub(launchTime), 60*time.Millisecond)
}

func TestParseUpgradeInterval(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Duration
	}{
		{"empty uses default", "", defaultUpgradeInterval},
		{"valid 10m", "10m", 10 * time.Minute},
		{"valid 30s", "30s", 30 * time.Second},
		{"invalid falls back to default", "not-a-duration", defaultUpgradeInterval},
		{"zero falls back to default", "0s", defaultUpgradeInterval},
		{"negative falls back to default", "-5m", defaultUpgradeInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUpgradeInterval(config.DaemonConfig{UpgradeInterval: tt.input})
			if got != tt.want {
				t.Errorf("parseUpgradeInterval(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseDaemonIntervals(t *testing.T) {
	tests := []struct {
		name          string
		scanInterval  string
		drainInterval string
		expectedScan  time.Duration
		expectedDrain time.Duration
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
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	now := time.Now().UTC()
	q.Enqueue(queue.Vessel{ID: "v1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck

	// slowDrain simulates a drain that takes 5 seconds. The context should
	// cancel well before that, and the daemon should wait for the in-flight
	// drain to finish (or timeout) rather than abandoning it.
	var drainStarted int32
	slowDrain := func(ctx context.Context) (runner.DrainResult, error) {
		atomic.StoreInt32(&drainStarted, 1)
		select {
		case <-ctx.Done():
			return runner.DrainResult{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return runner.DrainResult{Completed: 1}, nil
		}
	}

	// drainInterval=1ms ensures the drain fires immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := daemonLoop(ctx, q, nil, noopScan, slowDrain, nil, nil, time.Hour, time.Millisecond, 0)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error on shutdown, got: %v", err)
	}

	// The drain goroutine should have been started.
	if atomic.LoadInt32(&drainStarted) == 0 {
		t.Error("drain goroutine was never started")
	}

	// The loop should return after the context cancels and the drain
	// goroutine observes the cancellation — well under 2 seconds.
	if elapsed > 2*time.Second {
		t.Errorf("daemonLoop took %s — drain shutdown wait may be broken", elapsed)
	}
}

func TestLogTickSummary(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()

	q.Enqueue(queue.Vessel{ID: "v1", Source: "manual", State: queue.StatePending, CreatedAt: now})   //nolint:errcheck
	q.Enqueue(queue.Vessel{ID: "v2", Source: "manual", State: queue.StateRunning, CreatedAt: now})   //nolint:errcheck
	q.Enqueue(queue.Vessel{ID: "v3", Source: "manual", State: queue.StateCompleted, CreatedAt: now}) //nolint:errcheck
	q.Enqueue(queue.Vessel{ID: "v4", Source: "manual", State: queue.StateFailed, CreatedAt: now})    //nolint:errcheck

	var logBuf bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLogWriter)

	logTickSummary(q)

	got := logBuf.String()
	if !strings.Contains(got, "daemon: tick summary") {
		t.Fatalf("logTickSummary() log = %q, want daemon tick summary prefix", got)
	}
	for _, want := range []string{"pending=1", "running=1", "completed=1", "failed=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("logTickSummary() log = %q, want substring %q", got, want)
		}
	}
}

// TestWS1S28DaemonPathWiresScaffolding verifies that the daemon drain path
// produces a Runner with Intermediary and AuditLog wired. runDrain delegates
// directly to buildDrainRunner, so constructing the runner here exercises the
// same daemon-side wiring.
//
// Covers: WS1 S28.
func TestWS1S28DaemonPathWiresScaffolding(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	r, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	defer cleanup()

	if r.Intermediary == nil {
		t.Fatal("r.Intermediary = nil: daemon runDrain must wire Intermediary")
	}
	if r.AuditLog == nil {
		t.Fatal("r.AuditLog = nil: daemon runDrain must wire AuditLog")
	}

	result := r.Intermediary.Evaluate(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/HARNESS.md",
		AgentID:  "issue-1",
	})
	if result.Effect != intermediary.Deny {
		t.Fatalf("default protected-surface effect = %q, want %q", result.Effect, intermediary.Deny)
	}
}

func TestReconcileStaleVessels(t *testing.T) {
	t.Run("orphaned running vessel transitions to timed_out", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "stale-1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		if v == nil {
			t.Fatal("expected vessel from dequeue")
			return
		}

		reconcileStaleVessels(q, nil)

		updated, err := q.FindByID("stale-1")
		if err != nil {
			t.Fatalf("failed to find vessel: %v", err)
		}
		if updated.State != queue.StateTimedOut {
			t.Errorf("expected state %s, got %s", queue.StateTimedOut, updated.State)
		}
		if updated.Error != "orphaned by daemon restart" {
			t.Errorf("expected error 'orphaned by daemon restart', got %q", updated.Error)
		}
	})

	t.Run("recently started running vessel is also recovered", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "recent-1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		if v == nil {
			t.Fatal("expected vessel from dequeue")
			return
		}
		// StartedAt is set to now by Dequeue — singleton lock means it's still orphaned.

		reconcileStaleVessels(q, nil)

		updated, err := q.FindByID("recent-1")
		if err != nil {
			t.Fatalf("failed to find vessel: %v", err)
		}
		if updated.State != queue.StateTimedOut {
			t.Errorf("expected state %s, got %s", queue.StateTimedOut, updated.State)
		}
	})

	t.Run("pending and completed vessels are not affected", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "pending-1", Source: "manual", State: queue.StatePending, CreatedAt: now})    //nolint:errcheck
		q.Enqueue(queue.Vessel{ID: "complete-1", Source: "manual", State: queue.StateCompleted, CreatedAt: now}) //nolint:errcheck

		reconcileStaleVessels(q, nil)

		pending, _ := q.FindByID("pending-1")
		if pending.State != queue.StatePending {
			t.Errorf("expected pending-1 to remain pending, got %s", pending.State)
		}
		complete, _ := q.FindByID("complete-1")
		if complete.State != queue.StateCompleted {
			t.Errorf("expected complete-1 to remain completed, got %s", complete.State)
		}
	})

	t.Run("running vessel with nil StartedAt is recovered", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "nil-start", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		v.StartedAt = nil
		q.UpdateVessel(*v) //nolint:errcheck

		reconcileStaleVessels(q, nil)

		updated, _ := q.FindByID("nil-start")
		if updated.State != queue.StateTimedOut {
			t.Errorf("expected state timed_out for nil StartedAt, got %s", updated.State)
		}
	})

	t.Run("dtu fixture-backed stale running vessel keeps DTU evidence", func(t *testing.T) {
		repoDir, store, q, cmdRunner := newDaemonDTUEnv(t, "issue-daemon-recovery.yaml")
		defer withDaemonWorkingDir(t, repoDir)()

		cfg := &config.Config{
			Concurrency: 1,
			MaxTurns:    5,
			Timeout:     "45m",
			StateDir:    filepath.Join(repoDir, ".xylem"),
			Claude: config.ClaudeConfig{
				Command: "claude",
			},
			Sources: map[string]config.SourceConfig{
				"issues": {
					Type: "github",
					Repo: "owner/repo",
					Tasks: map[string]config.Task{
						"bugs": {
							Labels:   []string{"bug"},
							Workflow: "fix-bug",
							StatusLabels: &config.StatusLabels{
								Queued:  "queued",
								Running: "in-progress",
								Failed:  "failed",
							},
						},
					},
				},
			},
		}

		scan := scanner.New(cfg, q, cmdRunner)
		scanResult, err := scan.Scan(context.Background())
		if err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		if scanResult.Added != 1 {
			t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
		}
		assertLabelsEqual(t, readDaemonDTULabels(t, store, "owner/repo", 4), []string{"bug", "queued"})

		src := &source.GitHub{
			Repo:      "owner/repo",
			CmdRunner: cmdRunner,
		}
		wt := worktree.New(repoDir, cmdRunner)

		vessel, err := q.Dequeue()
		if err != nil {
			t.Fatalf("Dequeue() error = %v", err)
		}
		if vessel == nil {
			t.Fatal("Dequeue() = nil, want vessel")
			return
		}

		worktreePath, err := wt.Create(context.Background(), src.BranchName(*vessel))
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		vessel.WorktreePath = worktreePath
		if err := q.UpdateVessel(*vessel); err != nil {
			t.Fatalf("UpdateVessel() error = %v", err)
		}
		if err := src.OnStart(context.Background(), *vessel); err != nil {
			t.Fatalf("OnStart() error = %v", err)
		}
		assertLabelsEqual(t, readDaemonDTULabels(t, store, "owner/repo", 4), []string{"bug", "in-progress"})

		absWorktree := filepath.Join(repoDir, worktreePath)
		if _, err := os.Stat(absWorktree); err != nil {
			t.Fatalf("Stat(%q): %v", absWorktree, err)
		}

		reconcileStaleVessels(q, nil)

		updated, err := q.FindByID("issue-4")
		if err != nil {
			t.Fatalf("FindByID(issue-4) error = %v", err)
		}
		if updated.State != queue.StateTimedOut {
			t.Fatalf("updated.State = %q, want %q", updated.State, queue.StateTimedOut)
		}
		if updated.Error != "orphaned by daemon restart" {
			t.Fatalf("updated.Error = %q, want %q", updated.Error, "orphaned by daemon restart")
		}
		if updated.EndedAt == nil {
			t.Fatal("updated.EndedAt = nil, want end timestamp")
		}
		if updated.StartedAt == nil {
			t.Fatal("updated.StartedAt = nil, want start timestamp")
		}

		assertLabelsEqual(t, readDaemonDTULabels(t, store, "owner/repo", 4), []string{"bug", "in-progress"})
		if _, err := os.Stat(absWorktree); err != nil {
			t.Fatalf("Stat(%q) after reconcile: %v", absWorktree, err)
		}

		state := loadDaemonDTUState(t, store)
		repo := state.RepositoryBySlug("owner/repo")
		if repo == nil {
			t.Fatal("RepositoryBySlug(owner/repo) = nil")
			return
		}
		if len(repo.Worktrees) != 1 {
			t.Fatalf("len(repo.Worktrees) = %d, want 1", len(repo.Worktrees))
		}
		if repo.Worktrees[0].Branch != src.BranchName(*vessel) {
			t.Fatalf("worktree branch = %q, want %q", repo.Worktrees[0].Branch, src.BranchName(*vessel))
		}

		events := readDaemonDTUEvents(t, store)
		var commands [][]string
		for _, event := range events {
			if event.Kind != dtu.EventKindShimInvocation || event.Shim == nil || event.Shim.Command != "git" {
				continue
			}
			commands = append(commands, append([]string(nil), event.Shim.Args...))
		}
		wantBranch := src.BranchName(*vessel)
		wantCommands := [][]string{
			{"remote", "get-url", "origin"},
			{"fetch", "origin", "main"},
			{"worktree", "list", "--porcelain"},
			{"worktree", "add", ".claude/worktrees/" + wantBranch, "-B", wantBranch, "origin/main"},
		}
		for _, want := range wantCommands {
			found := false
			for _, got := range commands {
				if reflect.DeepEqual(got, want) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing git shim invocation %v in %v", want, commands)
			}
		}
		for _, event := range events {
			if event.Kind != dtu.EventKindShimInvocation || event.Shim == nil || event.Shim.Command != "git" {
				continue
			}
			if reflect.DeepEqual(event.Shim.Args, []string{"fetch", "origin", "main"}) && event.Shim.Attempt != 1 {
				t.Fatalf("fetch attempt = %d, want 1", event.Shim.Attempt)
			}
			if reflect.DeepEqual(event.Shim.Args, []string{"worktree", "add", ".claude/worktrees/" + wantBranch, "-B", wantBranch, "origin/main"}) && event.Shim.Attempt != 1 {
				t.Fatalf("worktree add attempt = %d, want 1", event.Shim.Attempt)
			}
		}
	})
}

func TestAcquireDaemonLock(t *testing.T) {
	t.Run("acquires lock successfully", func(t *testing.T) {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "daemon.pid")

		unlock, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		defer unlock()

		// PID file should exist and contain our PID.
		data, err := os.ReadFile(pidPath)
		if err != nil {
			t.Fatalf("failed to read PID file: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("PID file is empty")
		}
	})

	t.Run("second lock fails with already running error", func(t *testing.T) {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "daemon.pid")

		unlock1, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("first lock failed: %v", err)
		}
		defer unlock1()

		_, err = acquireDaemonLock(pidPath)
		if err == nil {
			t.Fatal("expected error from second lock, got nil")
		}
		if !strings.Contains(err.Error(), "daemon already running") {
			t.Errorf("expected 'daemon already running' error, got: %v", err)
		}
	})

	t.Run("lock is released on unlock", func(t *testing.T) {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "daemon.pid")

		unlock1, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("first lock failed: %v", err)
		}
		unlock1()

		// Should be able to acquire again after unlock.
		unlock2, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("second lock after unlock failed: %v", err)
		}
		defer unlock2()
	})

	t.Run("creates parent directory if needed", func(t *testing.T) {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "nested", "subdir", "daemon.pid")

		unlock, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		defer unlock()
	})
}
