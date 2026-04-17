package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/nicholls-inc/xylem/cli/internal/profiles"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	return repoDir, store, queue.New(config.RuntimePath(stateDir, "queue.jsonl")), cmdRunner
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

type daemonBacklogRunner struct {
	output []byte
}

func (r daemonBacklogRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return r.output, nil
}

func TestSmoke_S1_LegacyFlatLayoutMigratesAndDrainKeepsWorking(t *testing.T) {
	repoDir := t.TempDir()
	stateDir := filepath.Join(repoDir, ".xylem")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	cfg := makeDrainConfig(stateDir)
	cmdRunner := &smokeCommandRunner{}
	stubCommandRunnerFactory(t, func(*config.Config) drainCommandRunner {
		return cmdRunner
	})

	legacyQueue := queue.New(filepath.Join(stateDir, "queue.jsonl"))
	enqueuePromptVessel(t, legacyQueue, "issue-388", filepath.Join(repoDir, "legacy-worktree"))

	legacyAuditPath := filepath.Join(stateDir, config.DefaultAuditLogPath)
	legacyAudit := intermediary.NewAuditLog(legacyAuditPath)
	require.NoError(t, legacyAudit.Append(intermediary.AuditEntry{
		Intent: intermediary.Intent{
			Action:   "phase_execute",
			Resource: "prompt",
			AgentID:  "issue-388",
		},
		Decision:  intermediary.Allow,
		Timestamp: time.Now().UTC(),
	}))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "daemon.pid"), []byte(fmt.Sprintf("%d", os.Getpid())), 0o644))

	logBuf := withBufferedDefaultLogger(t)
	require.NoError(t, config.MigrateFlatStateToRuntime(stateDir))

	runtimeQueuePath := config.RuntimePath(stateDir, "queue.jsonl")
	q := queue.New(runtimeQueuePath)
	result, err := runDrain(context.Background(), cfg, q, worktree.New(repoDir, cmdRunner), 0)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Completed)
	assert.FileExists(t, runtimeQueuePath)
	assert.FileExists(t, config.RuntimePath(stateDir, config.DefaultAuditLogPath))
	assert.FileExists(t, filepath.Join(stateDir, "queue.jsonl.migrated"))
	assert.FileExists(t, filepath.Join(stateDir, config.DefaultAuditLogPath+".migrated"))
	assert.FileExists(t, filepath.Join(stateDir, "daemon.pid.migrated"))
	assert.Contains(t, logBuf.String(), "migrated legacy runtime file")
	assert.Contains(t, logBuf.String(), "prepared daemon pid migration")

	vessel, err := q.FindByID("issue-388")
	require.NoError(t, err)
	assert.Equal(t, queue.StateCompleted, vessel.State)
}

func TestSmoke_S2_NewLayoutStartupDoesNotRenameAndDrainWorks(t *testing.T) {
	repoDir := t.TempDir()
	stateDir := filepath.Join(repoDir, ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "state"), 0o755))

	cfg := makeDrainConfig(stateDir)
	cmdRunner := &smokeCommandRunner{}
	stubCommandRunnerFactory(t, func(*config.Config) drainCommandRunner {
		return cmdRunner
	})

	runtimeQueuePath := config.RuntimePath(stateDir, "queue.jsonl")
	q := queue.New(runtimeQueuePath)
	enqueuePromptVessel(t, q, "issue-389", filepath.Join(repoDir, "runtime-worktree"))

	logBuf := withBufferedDefaultLogger(t)
	require.NoError(t, config.MigrateFlatStateToRuntime(stateDir))

	result, err := runDrain(context.Background(), cfg, q, worktree.New(repoDir, cmdRunner), 0)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Completed)
	assert.FileExists(t, runtimeQueuePath)
	assert.NoFileExists(t, filepath.Join(stateDir, "queue.jsonl"))
	assert.NoFileExists(t, filepath.Join(stateDir, config.DefaultAuditLogPath+".migrated"))
	assert.NoFileExists(t, filepath.Join(stateDir, "queue.jsonl.migrated"))
	assert.NoFileExists(t, filepath.Join(stateDir, "daemon.pid.migrated"))
	assert.NotContains(t, logBuf.String(), "migrated legacy runtime file")
	assert.NotContains(t, logBuf.String(), "prepared daemon pid migration")

	vessel, err := q.FindByID("issue-389")
	require.NoError(t, err)
	assert.Equal(t, queue.StateCompleted, vessel.State)
}

func TestSmoke_S3_SplitBrainLayoutErrorsLoudly(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "state"), 0o755))

	legacyQueue := queue.New(filepath.Join(stateDir, "queue.jsonl"))
	enqueuePromptVessel(t, legacyQueue, "issue-390", filepath.Join(stateDir, "legacy-worktree"))

	runtimeQueue := queue.New(filepath.Join(stateDir, "state", "queue.jsonl"))
	enqueuePromptVessel(t, runtimeQueue, "issue-391", filepath.Join(stateDir, "runtime-worktree"))

	logBuf := withBufferedDefaultLogger(t)
	err := config.MigrateFlatStateToRuntime(stateDir)
	require.Error(t, err)
	require.ErrorContains(t, err, "both legacy and runtime queue.jsonl exist")
	assert.NoFileExists(t, filepath.Join(stateDir, "queue.jsonl.migrated"))
	assert.NotContains(t, logBuf.String(), "migrated legacy runtime file")
}

func TestSmoke_S7_DaemonStartupContinuesWhenAdaptRepoSearchFails(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"issues": {
				Type: "github",
				Repo: "owner/repo",
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	logBuf := withBufferedDefaultLogger(t)
	markerPath := adaptRepoSeedMarkerPath(cfg.StateDir)
	runner := &seedRunnerStub{
		errors: map[string]error{
			adaptRepoListCallForState("owner/repo", "open"): fmt.Errorf("gh unavailable"),
		},
	}

	err := daemonStartup(context.Background(), cfg, q, nil, runner, true)
	require.NoError(t, err)
	_, statErr := os.Stat(markerPath)
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))
	assert.Contains(t, logBuf.String(), "seed adapt-repo issue failed, continuing")
	assert.Contains(t, logBuf.String(), "gh unavailable")
	assert.Len(t, runner.calls, 1)
}

func TestSmoke_S8_DaemonStartupLeavesMarkerAbsentWhenAdaptRepoCreateFails(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"issues": {
				Type: "github",
				Repo: "owner/repo",
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	logBuf := withBufferedDefaultLogger(t)
	runner := &seedRunnerStub{
		outputs: map[string][]byte{
			adaptRepoListCallForState("owner/repo", "open"):   []byte("[]"),
			adaptRepoListCallForState("owner/repo", "closed"): []byte("[]"),
		},
		errors: map[string]error{
			adaptRepoCreateCall("owner/repo"): fmt.Errorf("gh create failed"),
		},
	}
	markerPath := adaptRepoSeedMarkerPath(cfg.StateDir)
	_, statErr := os.Stat(markerPath)
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))

	err := daemonStartup(context.Background(), cfg, q, nil, runner, true)
	require.NoError(t, err)
	_, statErr = os.Stat(markerPath)
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
	assert.Contains(t, logBuf.String(), "seed adapt-repo issue failed, continuing")
	assert.Contains(t, logBuf.String(), "gh create failed")
	assert.Len(t, runner.calls, 3)
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

	if err := daemonLoop(ctx, q, tracker, scan, drain, nil, nil, nil, nil, 10*time.Millisecond, 10*time.Millisecond, 0); err != nil {
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

func TestSmoke_S2_DaemonIdleWithBacklogWarningFires(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"issues": {
				Type:    "github",
				Repo:    "owner/repo",
				Exclude: []string{"wontfix"},
				Tasks: map[string]config.Task{
					"bugs": {
						Labels:   []string{"bug"},
						Workflow: "fix-bug",
					},
				},
			},
		},
	}
	issues := []map[string]any{
		{"number": 1, "title": "one", "body": "", "url": "https://github.com/owner/repo/issues/1", "labels": []map[string]string{{"name": "bug"}}},
		{"number": 2, "title": "two", "body": "", "url": "https://github.com/owner/repo/issues/2", "labels": []map[string]string{{"name": "bug"}}},
		{"number": 3, "title": "three", "body": "", "url": "https://github.com/owner/repo/issues/3", "labels": []map[string]string{{"name": "bug"}}},
	}
	output, err := json.Marshal(issues)
	require.NoError(t, err)

	s := scanner.New(cfg, queue.New(filepath.Join(dir, "queue.jsonl")), daemonBacklogRunner{output: output})
	count, err := s.BacklogCount(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, count)

	check := daemonBacklogHealthCheck(time.Now().UTC(), time.Now().UTC().Add(-10*time.Minute), 5*time.Minute, count, daemonQueueSnapshot{})
	require.NotNil(t, check)
	assert.Equal(t, "idle_with_backlog", check.Code)
	assert.Equal(t, "Daemon idle with 3 backlog items on GitHub", check.Message)
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

type cancellableTrackerStub struct {
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (t *cancellableTrackerStub) Wait() runner.DrainResult {
	return runner.DrainResult{}
}

func (t *cancellableTrackerStub) InFlightCount() int {
	return int(t.inFlight.Load())
}

func (t *cancellableTrackerStub) Start() {
	cur := t.inFlight.Add(1)
	for {
		old := t.maxSeen.Load()
		if cur <= old || t.maxSeen.CompareAndSwap(old, cur) {
			return
		}
	}
}

func (t *cancellableTrackerStub) Cancel() {
	t.inFlight.Add(-1)
}

func TestDaemonShutdown(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, nil, noopScan, noopDrain, nil, nil, nil, nil, time.Hour, time.Hour, 0)
	if err != nil {
		t.Fatalf("expected nil error on shutdown, got: %v", err)
	}
}

func TestSmoke_S3_DaemonTickDrainsScheduledVessel(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    filepath.Join(dir, ".xylem"),
		Claude:      config.ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]config.SourceConfig{
			"doctor": {
				Type:     "schedule",
				Cadence:  "1h",
				Workflow: "doctor",
			},
		},
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", cfg.StateDir, err)
	}
	q := queue.New(config.RuntimePath(cfg.StateDir, "queue.jsonl"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	drain := func(ctx context.Context) (runner.DrainResult, error) {
		vessel, err := q.Dequeue()
		if err != nil {
			return runner.DrainResult{}, err
		}
		if vessel == nil {
			return runner.DrainResult{}, nil
		}
		if err := q.Update(vessel.ID, queue.StateCompleted, ""); err != nil {
			return runner.DrainResult{}, err
		}
		cancel()
		return runner.DrainResult{Launched: 1, Completed: 1}, nil
	}

	err := daemonLoop(ctx, q, nil, func(ctx context.Context) (scanner.ScanResult, error) {
		return runScan(ctx, cfg, q)
	}, drain, nil, nil, nil, nil, time.Millisecond, time.Millisecond, 0)
	require.NoError(t, err)

	vessels, err := q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "schedule", vessels[0].Source)
	assert.Equal(t, queue.StateCompleted, vessels[0].State)
	assert.Equal(t, "doctor", vessels[0].Workflow)
	assert.Equal(t, "1h", vessels[0].Meta["schedule.cadence"])
	assert.Equal(t, "doctor", vessels[0].Meta["schedule.source_name"])
	assert.NotEmpty(t, vessels[0].Meta["schedule.fired_at"])
}

func TestSmoke_S31_TracerWiredInDaemonRunDrain(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = ""
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := &smokeCommandRunner{}
	stubCommandRunnerFactory(t, func(*config.Config) drainCommandRunner {
		return cmdRunner
	})

	var tracers []*observability.Tracer
	stubConfiguredTracerFactory(t, func(cfg observability.TracerConfig) (*observability.Tracer, error) {
		tracer, err := observability.NewTracer(cfg)
		if err == nil {
			tracers = append(tracers, tracer)
		}
		return tracer, err
	})

	enqueuePromptVessel(t, q, "daemon-1", filepath.Join(dir, "daemon-one"))
	firstOut := captureStdout(func() {
		result, err := runDrain(context.Background(), cfg, q, worktree.New(dir, cmdRunner), 0)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Completed)
	})

	enqueuePromptVessel(t, q, "daemon-2", filepath.Join(dir, "daemon-two"))
	secondOut := captureStdout(func() {
		result, err := runDrain(context.Background(), cfg, q, worktree.New(dir, cmdRunner), 0)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Completed)
	})

	require.Len(t, tracers, 2)
	assert.NotSame(t, tracers[0], tracers[1])
	assert.Contains(t, firstOut, `"Name":"drain_run"`)
	assert.Contains(t, firstOut, `"Name":"vessel:daemon-1"`)
	assert.NotContains(t, firstOut, `"Name":"vessel:daemon-2"`)
	assert.Contains(t, secondOut, `"Name":"drain_run"`)
	assert.Contains(t, secondOut, `"Name":"vessel:daemon-2"`)
	assert.NotContains(t, secondOut, `"Name":"vessel:daemon-1"`)
}

func TestSmoke_S32_TracerShutdownDeferredInDaemonPath(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = "localhost:4317"
	cfg.Observability.Insecure = true
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	var exporter *recordingSpanExporter
	stubConfiguredTracerFactory(t, func(observability.TracerConfig) (*observability.Tracer, error) {
		tracer, spanExporter := newRecordingTracer()
		exporter = spanExporter
		return tracer, nil
	})

	_, err := runDrain(context.Background(), cfg, q, worktree.New(dir, newCmdRunner(cfg)), 0)
	require.NoError(t, err)
	require.NotNil(t, exporter)
	assert.Equal(t, 1, exporter.shutdownCount())
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

	err := daemonLoop(ctx, q, nil, noopScan, noopDrain, nil, upgrade, nil, nil, time.Hour, 2*time.Millisecond, time.Millisecond)
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

	err := daemonLoop(ctx, q, nil, noopScan, noopDrain, nil, upgrade, nil, nil, time.Hour, 2*time.Millisecond, 10*time.Second)
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
	err := daemonLoop(ctx, q, nil, noopScan, noopDrain, nil, nil, nil, nil, time.Hour, 2*time.Millisecond, 10*time.Millisecond)
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

	err := daemonLoop(ctx, q, nil, noopScan, slowDrain, nil, upgrade, nil, nil, time.Hour, time.Millisecond, time.Millisecond)
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

	err := daemonLoop(ctx, q, tracker, noopScan, drain, nil, nil, nil, nil, time.Hour, 10*time.Millisecond, 0)
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

	err := daemonLoop(ctx, q, tracker, noopScan, drain, nil, upgrade, nil, nil, time.Hour, 10*time.Millisecond, time.Millisecond)
	require.NoError(t, err)

	assert.NotZero(t, upgradeSeen.Load())
	launchTime := time.Unix(0, launchedAt.Load())
	upgradeTime := time.Unix(0, upgradedAt.Load())
	assert.GreaterOrEqual(t, upgradeTime.Sub(launchTime), 60*time.Millisecond)
}

func TestSmoke_S39_DaemonAutoUpgradeProceedsAfterCancelledVesselDropsInFlight(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	tracker := &cancellableTrackerStub{}

	var (
		drainCalls  atomic.Int32
		upgradeSeen atomic.Int32
	)
	cancelIssued := make(chan struct{})

	drain := func(_ context.Context) (runner.DrainResult, error) {
		if drainCalls.Add(1) == 1 {
			tracker.Start()
			go func() {
				time.Sleep(40 * time.Millisecond)
				tracker.Cancel()
				close(cancelIssued)
			}()
			return runner.DrainResult{Launched: 1}, nil
		}
		return runner.DrainResult{}, nil
	}
	upgrade := func() {
		if tracker.InFlightCount() != 0 {
			t.Errorf("upgrade fired before cancelled vessel drained: in_flight=%d", tracker.InFlightCount())
		}
		upgradeSeen.Add(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, tracker, noopScan, drain, nil, upgrade, nil, nil, time.Hour, 10*time.Millisecond, time.Millisecond)
	require.NoError(t, err)

	select {
	case <-cancelIssued:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for cancelled vessel to drop in-flight count")
	}

	assert.GreaterOrEqual(t, drainCalls.Load(), int32(2))
	assert.NotZero(t, upgradeSeen.Load())
	assert.Zero(t, tracker.InFlightCount())
}

func TestSmoke_S48_DaemonHealthTickPrunesOnlyStaleXylemWorktrees(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := filepath.Join(repoRoot, ".xylem")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	q := queue.New(filepath.Join(stateDir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:           "issue-1",
		Source:       "manual",
		Workflow:     "fix-bug",
		State:        queue.StatePending,
		CreatedAt:    time.Now().UTC(),
		WorktreePath: filepath.Join(".xylem", "worktrees", "fix", "issue-1"),
	})
	require.NoError(t, err)

	activePath := filepath.Join(repoRoot, ".xylem", "worktrees", "fix", "issue-1")
	stalePath := filepath.Join(repoRoot, ".xylem", "worktrees", "fix", "issue-2")
	daemonRootPath := filepath.Join(repoRoot, ".daemon-root", "issue-3")
	porcelain := strings.Join([]string{
		"worktree " + repoRoot,
		"HEAD aaa",
		"branch refs/heads/main",
		"",
		"worktree " + activePath,
		"HEAD bbb",
		"branch refs/heads/fix/issue-1",
		"",
		"worktree " + stalePath,
		"HEAD ccc",
		"branch refs/heads/fix/issue-2",
		"",
		"worktree " + daemonRootPath,
		"HEAD ddd",
		"branch refs/heads/daemon/issue-3",
		"",
	}, "\n")

	cmdRunner := &mockCleanupRunner{porcelain: porcelain}
	pruneRunner := runner.New(nil, q, worktree.New(repoRoot, cmdRunner), nil)
	var checkCalls atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = daemonLoop(ctx, q, nil, noopScan, noopDrain, func(ctx context.Context) {
		checkCalls.Add(1)
		if pruneRunner.PruneStaleWorktrees(ctx) > 0 {
			cancel()
		}
	}, nil, nil, nil, time.Hour, time.Hour, 0)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, checkCalls.Load(), int32(1))
	require.Len(t, cmdRunner.removeCalls, 1)
	assert.Contains(t, cmdRunner.removeCalls[0], stalePath)
	assert.NotContains(t, cmdRunner.removeCalls[0], activePath)
	assert.NotContains(t, cmdRunner.removeCalls[0], daemonRootPath)

	vessel, err := q.FindByID("issue-1")
	require.NoError(t, err)
	assert.Equal(t, queue.StatePending, vessel.State)
	assert.Equal(t, filepath.Join(".xylem", "worktrees", "fix", "issue-1"), vessel.WorktreePath)
}

// TestDaemonLoopUpgradeOverduePausesDrainToCreateIdleWindow verifies the
// overdue path: when upgrade has been pending for upgradeInterval*3 without
// firing (because in-flight vessels kept in_flight > 0 continuously), new
// drain ticks are paused so in-flight vessels can drain naturally. Once
// in_flight reaches zero, the normal upgrade path fires.
//
// This is the regression test for the loop 8 diagnosis: scheduled sources
// that continuously fill concurrency slots previously locked the daemon on
// its current binary forever because the normal upgrade path required
// in_flight == 0, which the scheduled source prevented.
func TestDaemonLoopUpgradeOverduePausesDrainToCreateIdleWindow(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	tracker := &trackerStub{}

	var (
		drainCalls  atomic.Int32
		upgradeSeen atomic.Int32
	)

	// Saturating drain: every drain call starts a "long" in-flight vessel
	// (relative to upgradeInterval) and returns Launched=1. Without the
	// overdue pause, this would keep in_flight > 0 indefinitely and the
	// normal upgrade path would never fire.
	drain := func(_ context.Context) (runner.DrainResult, error) {
		drainCalls.Add(1)
		// Each vessel lasts ~50ms. Drain interval is 5ms, so without pause
		// the tracker stays above zero for the entire test window.
		tracker.Start(50 * time.Millisecond)
		return runner.DrainResult{Launched: 1}, nil
	}

	upgrade := func() {
		upgradeSeen.Add(1)
	}

	// upgradeInterval=5ms → overdue threshold = 15ms. Drain interval=5ms so
	// drain is triggered frequently. The tracker starts a 50ms vessel on
	// each drain call, but once upgrade is overdue (>15ms elapsed), drain
	// should be paused — no new tracker.Start calls — allowing the existing
	// in-flight vessels to complete, which then lets upgrade fire.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, tracker, noopScan, drain, nil, upgrade, nil, nil, time.Hour, 5*time.Millisecond, 5*time.Millisecond)
	require.NoError(t, err)

	// Upgrade MUST have fired at least once despite the continuously saturating drain.
	// Previously this would have been 0 because in_flight never reached 0 naturally.
	assert.GreaterOrEqual(t, upgradeSeen.Load(), int32(1),
		"upgrade must fire via overdue-pause path even when drain is saturating")
	// Drain must have been invoked multiple times before the pause kicked in.
	assert.GreaterOrEqual(t, drainCalls.Load(), int32(1),
		"drain must have been called at least once")
}

// TestDaemonLoopUpgradeOverdueDoesNotFireUnderNormalConditions verifies that
// the overdue path does NOT trigger under normal conditions where upgrade
// fires naturally on the idle path.
func TestDaemonLoopUpgradeOverdueDoesNotFireUnderNormalConditions(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	tracker := &trackerStub{}

	var (
		upgradeSeen atomic.Int32
	)

	// Idle drain: no vessels ever started, so in_flight stays at 0 and
	// the normal upgrade path fires on every cycle.
	drain := func(_ context.Context) (runner.DrainResult, error) {
		return runner.DrainResult{}, nil
	}
	upgrade := func() {
		upgradeSeen.Add(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// upgradeInterval=2ms; over 100ms, normal path should fire many times.
	err := daemonLoop(ctx, q, tracker, noopScan, drain, nil, upgrade, nil, nil, time.Hour, 2*time.Millisecond, 2*time.Millisecond)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, upgradeSeen.Load(), int32(5),
		"normal-path upgrade should fire multiple times under idle conditions")
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
	err := daemonLoop(ctx, q, nil, noopScan, slowDrain, nil, nil, nil, nil, time.Hour, time.Millisecond, 0)
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

	logBuf := withBufferedDefaultLogger(t)

	logTickSummary(q)

	got := logBuf.String()
	if !strings.Contains(got, "msg=\"daemon tick summary\"") {
		t.Fatalf("logTickSummary() log = %q, want daemon tick summary prefix", got)
	}
	for _, want := range []string{"pending=1", "running=1", "completed=1", "failed=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("logTickSummary() log = %q, want substring %q", got, want)
		}
	}
}

// TestSmoke_S28_CLIWiringInDaemonGoCreatesIntermediaryFromConfig verifies that
// the daemon drain path produces a Runner with Intermediary and AuditLog wired.
// runDrain delegates directly to buildDrainRunner, so constructing the runner
// here exercises the same daemon-side wiring.
func TestSmoke_S28_CLIWiringInDaemonGoCreatesIntermediaryFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	cmdRunner := newCmdRunner(cfg)

	r, cleanup := buildDrainRunner(cfg, q, worktree.New(dir, cmdRunner), cmdRunner)
	defer cleanup()

	require.NotNil(t, r)
	require.NotNil(t, r.Intermediary)
	require.NotNil(t, r.AuditLog)

	result := r.Intermediary.Evaluate(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/HARNESS.md",
		AgentID:  "issue-1",
	})
	assert.Equal(t, intermediary.Deny, result.Effect)

	entry := intermediary.AuditEntry{
		Intent: intermediary.Intent{
			Action:   "phase_execute",
			Resource: "fix",
			AgentID:  "issue-1",
		},
		Decision:  intermediary.Allow,
		Timestamp: time.Now().UTC(),
	}
	require.NoError(t, r.AuditLog.Append(entry))

	auditLogPath := config.RuntimePath(cfg.StateDir, cfg.EffectiveAuditLogPath())
	entries, err := intermediary.NewAuditLog(auditLogPath).Entries()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, entry.Intent, entries[0].Intent)
	assert.Equal(t, entry.Decision, entries[0].Decision)
}

// TestSmoke_S49_DaemonStartupResyncsProfileAssetsWhenDigestDrifts verifies
// that daemonStartup triggers a resync when the embedded digest differs from
// the runtime digest: stale files are overwritten and missing files are created.
func TestSmoke_S49_DaemonStartupResyncsProfileAssetsWhenDigestDrifts(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "workflows"), 0o755))

	// Write stale content to one workflow file so the runtime digest differs.
	staleContent := []byte("stale: true")
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "workflows", "fix-bug.yaml"),
		staleContent,
		0o644,
	))

	cfg := &config.Config{
		Profiles: []string{"core"},
		StateDir: stateDir,
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	logs := withBufferedDefaultLogger(t)

	err := daemonStartup(context.Background(), cfg, q, nil, &seedRunnerStub{}, false)
	require.NoError(t, err)

	// The resync log message must appear — digest drift was detected.
	assert.Contains(t, logs.String(), "daemon profile assets stale; re-syncing")

	// The stale workflow must be overwritten with the embedded version.
	composed, composeErr := profiles.Compose("core")
	require.NoError(t, composeErr)
	data, readErr := os.ReadFile(filepath.Join(stateDir, "workflows", "fix-bug.yaml"))
	require.NoError(t, readErr)
	assert.Equal(t, composed.Workflows["fix-bug"], data, "stale workflow must be overwritten by resync")

	// A workflow that was absent before resync must be created.
	for name, embeddedBytes := range composed.Workflows {
		if name == "fix-bug" {
			continue // already verified above
		}
		wfPath := filepath.Join(stateDir, "workflows", name+".yaml")
		wfData, wfErr := os.ReadFile(wfPath)
		require.NoError(t, wfErr, "missing workflow %q should have been created by resync", name)
		assert.Equal(t, embeddedBytes, wfData, "newly created workflow %q should match embedded content", name)
		break // one is enough to confirm the create path works
	}
}

// TestSmoke_S50_DaemonStartupSkipsResyncWhenDigestsMatch verifies that
// daemonStartup does not re-write files when the runtime digest already
// matches the embedded digest.
func TestSmoke_S50_DaemonStartupSkipsResyncWhenDigestsMatch(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".xylem")

	// Run a full init-style sync first so the runtime dir is current.
	cfg := &config.Config{
		Profiles: []string{"core"},
		StateDir: stateDir,
	}
	composed, err := profiles.Compose("core")
	require.NoError(t, err)
	require.NoError(t, syncProfileAssets(stateDir, composed, true))

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	logs := withBufferedDefaultLogger(t)

	err = daemonStartup(context.Background(), cfg, q, nil, &seedRunnerStub{}, false)
	require.NoError(t, err)

	assert.NotContains(t, logs.String(), "daemon profile assets stale; re-syncing",
		"no re-sync expected when digests already match")
}

// TestSmoke_S51_DaemonStartupNoProfileSyncFlagSkipsResync verifies that
// noProfileSync=true prevents re-sync even when the runtime dir is stale.
func TestSmoke_S51_DaemonStartupNoProfileSyncFlagSkipsResync(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "workflows"), 0o755))
	staleContent := []byte("stale: true")
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "workflows", "fix-bug.yaml"),
		staleContent,
		0o644,
	))

	cfg := &config.Config{
		Profiles: []string{"core"},
		StateDir: stateDir,
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	logs := withBufferedDefaultLogger(t)

	err := daemonStartup(context.Background(), cfg, q, nil, &seedRunnerStub{}, true)
	require.NoError(t, err)

	// File must NOT have been overwritten.
	data, readErr := os.ReadFile(filepath.Join(stateDir, "workflows", "fix-bug.yaml"))
	require.NoError(t, readErr)
	assert.Equal(t, staleContent, data, "no-profile-sync should leave stale file untouched")
	assert.NotContains(t, logs.String(), "daemon profile assets stale; re-syncing")
}

// TestSmoke_S52_DaemonStartupContinuesWhenProfileComposeFails verifies that
// a profile compose error is logged as a warning and daemonStartup returns nil.
func TestSmoke_S52_DaemonStartupContinuesWhenProfileComposeFails(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Profiles: []string{"nonexistent-profile"},
		StateDir: filepath.Join(dir, ".xylem"),
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	logs := withBufferedDefaultLogger(t)

	err := daemonStartup(context.Background(), cfg, q, nil, &seedRunnerStub{}, false)
	require.NoError(t, err, "daemonStartup must continue even when profile compose fails")
	assert.Contains(t, logs.String(), "daemon profile re-sync failed, continuing")
}

// TestSmoke_S53_DaemonStartupSkipsResyncWhenProfilesEmpty verifies that
// daemonStartup does not attempt any profile sync when cfg.Profiles is empty.
func TestSmoke_S53_DaemonStartupSkipsResyncWhenProfilesEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Profiles: nil,
		StateDir: filepath.Join(dir, ".xylem"),
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	logs := withBufferedDefaultLogger(t)

	err := daemonStartup(context.Background(), cfg, q, nil, &seedRunnerStub{}, false)
	require.NoError(t, err)
	assert.NotContains(t, logs.String(), "re-sync")
	assert.NotContains(t, logs.String(), "compose profiles")
}

// TestSmoke_S54_ResyncOverwritesStaleProfileWorkflow verifies that resyncProfileAssets
// overwrites a profile-owned workflow file that exists on disk with stale content.
func TestSmoke_S54_ResyncOverwritesStaleProfileWorkflow(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	wfPath := filepath.Join(stateDir, "workflows", "fix-bug.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(wfPath), 0o755))
	require.NoError(t, os.WriteFile(wfPath, []byte("stale: content\n"), 0o644))

	composed := &profiles.ComposedProfile{
		Workflows: map[string][]byte{
			"fix-bug": []byte("version: 2\n"),
		},
	}
	logs := withBufferedDefaultLogger(t)
	require.NoError(t, resyncProfileAssets(stateDir, composed))

	got, err := os.ReadFile(wfPath)
	require.NoError(t, err)
	assert.Equal(t, composed.Workflows["fix-bug"], got, "stale workflow must be overwritten")
	assert.Contains(t, logs.String(), "updated file")
}

// TestSmoke_S55_ResyncCreatesMissingProfileWorkflow verifies that resyncProfileAssets
// creates a profile-owned workflow file that does not yet exist on disk.
func TestSmoke_S55_ResyncCreatesMissingProfileWorkflow(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")

	composed := &profiles.ComposedProfile{
		Workflows: map[string][]byte{
			"fix-bug": []byte("workflow: fix\n"),
		},
	}
	logs := withBufferedDefaultLogger(t)
	require.NoError(t, resyncProfileAssets(stateDir, composed))

	got, err := os.ReadFile(filepath.Join(stateDir, "workflows", "fix-bug.yaml"))
	require.NoError(t, err)
	assert.Equal(t, composed.Workflows["fix-bug"], got)
	assert.Contains(t, logs.String(), "added file")
}

// TestSmoke_S56_ResyncPreservesDaemonOnlyWorkflow verifies that resyncProfileAssets
// does not touch workflow files that are not part of the composed profile.
func TestSmoke_S56_ResyncPreservesDaemonOnlyWorkflow(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	daemonOnlyPath := filepath.Join(stateDir, "workflows", "custom-daemon.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(daemonOnlyPath), 0o755))
	daemonContent := []byte("daemon: only\n")
	require.NoError(t, os.WriteFile(daemonOnlyPath, daemonContent, 0o644))

	// Profile only contains "fix-bug", not "custom-daemon".
	composed := &profiles.ComposedProfile{
		Workflows: map[string][]byte{
			"fix-bug": []byte("workflow: fix\n"),
		},
	}
	require.NoError(t, resyncProfileAssets(stateDir, composed))

	got, err := os.ReadFile(daemonOnlyPath)
	require.NoError(t, err)
	assert.Equal(t, daemonContent, got, "daemon-only workflow must not be touched")
}

// TestSmoke_S57_UpgradeTickCallsResyncWhenBinaryUnchanged verifies that
// runUpgradeTick calls maybeResyncProfileAssets (via the injectable
// daemonResyncProfileAssets) even when selfUpgrade returns without exec()-ing
// (binary hash unchanged path). If someone removes the resync call from
// runUpgradeTick, this test fails.
func TestSmoke_S57_UpgradeTickCallsResyncWhenBinaryUnchanged(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".xylem")

	cfg := &config.Config{
		Profiles: []string{"core"},
		StateDir: stateDir,
	}

	// Stub selfUpgrade so exec() is not called (binary unchanged path).
	stubDaemonUpgradeDependencies(t,
		func(string) error { return nil },
		func(string, string) error { return nil },
		func(string, []string, []string) error { return nil },
	)

	// Stub daemonResyncProfileAssets and record whether it was called with the
	// correct config. This is the only assertion that matters: the real production
	// function runUpgradeTick must invoke it.
	resyncCalled := false
	prev := daemonResyncProfileAssets
	daemonResyncProfileAssets = func(c *config.Config) error {
		assert.Equal(t, cfg, c, "resync must receive the daemon cfg")
		resyncCalled = true
		return nil
	}
	t.Cleanup(func() { daemonResyncProfileAssets = prev })

	runUpgradeTick(dir, filepath.Join(dir, "xylem"), cfg)

	assert.True(t, resyncCalled, "runUpgradeTick must call maybeResyncProfileAssets when profiles are configured")
}

// TestSmoke_S58_ResyncLogsAddedAndUpdatedFiles verifies that resyncProfileAssets
// emits slog.Info messages for both added (new) and updated (stale) files.
func TestSmoke_S58_ResyncLogsAddedAndUpdatedFiles(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")

	// Pre-create one file with stale content (will be updated).
	wfPath := filepath.Join(stateDir, "workflows", "fix-bug.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(wfPath), 0o755))
	require.NoError(t, os.WriteFile(wfPath, []byte("old: content\n"), 0o644))

	composed := &profiles.ComposedProfile{
		Workflows: map[string][]byte{
			"fix-bug":           []byte("new: content\n"),
			"implement-feature": []byte("new workflow\n"),
		},
	}
	logs := withBufferedDefaultLogger(t)
	require.NoError(t, resyncProfileAssets(stateDir, composed))

	logStr := logs.String()
	assert.Contains(t, logStr, "updated file", "updated file must be logged")
	assert.Contains(t, logStr, "added file", "added file must be logged")
}

func TestReconcileStaleVessels(t *testing.T) {
	t.Run("orphaned running vessel is requeued as pending", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "stale-1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		if v == nil {
			t.Fatal("expected vessel from dequeue")
			return
		}
		v.CurrentPhase = 2
		v.PhaseOutputs = map[string]string{"plan": "done"}
		v.WorktreePath = "/tmp/worktree-stale-1"
		if err := q.UpdateVessel(*v); err != nil {
			t.Fatalf("UpdateVessel() error = %v", err)
		}

		reconcileStaleVessels(q, nil)

		updated, err := q.FindByID("stale-1")
		if err != nil {
			t.Fatalf("failed to find vessel: %v", err)
		}
		if updated.State != queue.StatePending {
			t.Errorf("expected state %s, got %s", queue.StatePending, updated.State)
		}
		if updated.Error != "" {
			t.Errorf("expected cleared error, got %q", updated.Error)
		}
		if updated.StartedAt != nil {
			t.Fatal("expected StartedAt to be cleared")
		}
		// Spec I3 (docs/invariants/queue.md) extended: running→pending
		// (orphan reconcile) must reset CurrentPhase/PhaseOutputs/WorktreePath
		// so the requeued vessel restarts fresh at phase 0 without inheriting
		// a stale worktree path (loop 202 chdir cascade).
		if updated.CurrentPhase != 0 {
			t.Fatalf("updated.CurrentPhase = %d, want 0 (reset on orphan reconcile)", updated.CurrentPhase)
		}
		if len(updated.PhaseOutputs) != 0 {
			t.Fatalf("updated.PhaseOutputs = %v, want empty (reset on orphan reconcile)", updated.PhaseOutputs)
		}
		if updated.WorktreePath != "" {
			t.Fatalf("updated.WorktreePath = %q, want empty", updated.WorktreePath)
		}
	})

	t.Run("recently started running vessel is also requeued", func(t *testing.T) {
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
		if updated.State != queue.StatePending {
			t.Errorf("expected state %s, got %s", queue.StatePending, updated.State)
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

	t.Run("running vessel with nil StartedAt is requeued", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "nil-start", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		v.StartedAt = nil
		q.UpdateVessel(*v) //nolint:errcheck

		reconcileStaleVessels(q, nil)

		updated, _ := q.FindByID("nil-start")
		if updated.State != queue.StatePending {
			t.Errorf("expected state pending for nil StartedAt, got %s", updated.State)
		}
	})

	t.Run("dtu fixture-backed stale running vessel is requeued without timeout side effects", func(t *testing.T) {
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

		reconcileStaleVessels(q, wt)

		updated, err := q.FindByID("issue-4")
		if err != nil {
			t.Fatalf("FindByID(issue-4) error = %v", err)
		}
		if updated.State != queue.StatePending {
			t.Fatalf("updated.State = %q, want %q", updated.State, queue.StatePending)
		}
		if updated.Error != "" {
			t.Fatalf("updated.Error = %q, want empty", updated.Error)
		}
		if updated.EndedAt != nil {
			t.Fatal("updated.EndedAt != nil, want cleared end timestamp")
		}
		if updated.StartedAt != nil {
			t.Fatal("updated.StartedAt != nil, want cleared start timestamp")
		}
		if updated.WorktreePath != "" {
			t.Fatalf("updated.WorktreePath = %q, want empty", updated.WorktreePath)
		}

		assertLabelsEqual(t, readDaemonDTULabels(t, store, "owner/repo", 4), []string{"bug", "in-progress"})
		if _, err := os.Stat(absWorktree); !os.IsNotExist(err) {
			t.Fatalf("Stat(%q) after reconcile error = %v, want not exist", absWorktree, err)
		}

		state := loadDaemonDTUState(t, store)
		repo := state.RepositoryBySlug("owner/repo")
		if repo == nil {
			t.Fatal("RepositoryBySlug(owner/repo) = nil")
			return
		}
		if len(repo.Worktrees) != 0 {
			t.Fatalf("len(repo.Worktrees) = %d, want 0", len(repo.Worktrees))
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
			{"worktree", "add", ".xylem/worktrees/" + wantBranch, "-B", wantBranch, "origin/main"},
			{"worktree", "remove", worktreePath, "--force"},
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
			if reflect.DeepEqual(event.Shim.Args, []string{"worktree", "add", ".xylem/worktrees/" + wantBranch, "-B", wantBranch, "origin/main"}) && event.Shim.Attempt != 1 {
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
