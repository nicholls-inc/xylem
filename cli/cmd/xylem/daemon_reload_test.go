package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCmdDaemonReloadWritesRequestAndSignals(t *testing.T) {
	cfg := &config.Config{StateDir: t.TempDir()}
	var gotPIDPath string
	var gotSig os.Signal

	err := cmdDaemonReload(cfg, false, func(pidPath string, sig syscall.Signal) (int, bool, error) {
		gotPIDPath = pidPath
		gotSig = sig
		return 123, true, nil
	})
	require.NoError(t, err)

	req, ok, err := readDaemonReloadRequest(cfg)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "cli", req.Trigger)
	assert.False(t, req.Rollback)
	assert.Equal(t, daemonPIDPath(cfg), gotPIDPath)
	assert.Equal(t, syscall.SIGHUP, gotSig)
}

func TestCmdDaemonReloadClearsRequestWhenDaemonMissing(t *testing.T) {
	cfg := &config.Config{StateDir: t.TempDir()}

	err := cmdDaemonReload(cfg, false, func(string, syscall.Signal) (int, bool, error) {
		return 0, false, nil
	})
	require.Error(t, err)

	_, ok, readErr := readDaemonReloadRequest(cfg)
	require.NoError(t, readErr)
	assert.False(t, ok)
}

func TestDaemonRuntimeRejectsInvalidReload(t *testing.T) {
	rootDir, configPath, cfg := writeDaemonReloadRepo(t, "# Harness A\n")
	q := queue.New(filepath.Join(cfg.StateDir, "queue.jsonl"))
	rt, err := newDaemonRuntime(rootDir, configPath, q, worktree.New(rootDir, newCmdRunner(cfg)), cfg)
	require.NoError(t, err)

	beforeDigest := rt.currentHandle().snapshot.Digest
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, ".xylem", "workflows", "fix-bug.yaml"), []byte("name: fix-bug\nphases:\n  - name: bad name\n"), 0o644))

	rt.request(daemonReloadRequest{Trigger: "cli", RequestedAt: daemonNow().Add(-time.Minute)})
	err = rt.processPendingReload(context.Background())
	require.Error(t, err)
	assert.Equal(t, beforeDigest, rt.currentHandle().snapshot.Digest)

	logs := readDaemonReloadLogEntries(t, cfg)
	require.NotEmpty(t, logs)
	assert.Equal(t, "rejected", logs[len(logs)-1].Result)
}

func TestDaemonRuntimeRollbackRestoresPriorSnapshot(t *testing.T) {
	rootDir, configPath, cfg := writeDaemonReloadRepo(t, "# Harness A\n")
	q := queue.New(filepath.Join(cfg.StateDir, "queue.jsonl"))
	rt, err := newDaemonRuntime(rootDir, configPath, q, worktree.New(rootDir, newCmdRunner(cfg)), cfg)
	require.NoError(t, err)

	beforeDigest := rt.currentHandle().snapshot.Digest
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, ".xylem", "HARNESS.md"), []byte("version B"), 0o644))

	rt.request(daemonReloadRequest{Trigger: "cli", RequestedAt: daemonNow().Add(-time.Minute)})
	require.NoError(t, rt.processPendingReload(context.Background()))
	afterDigest := rt.currentHandle().snapshot.Digest
	assert.NotEqual(t, beforeDigest, afterDigest)

	rt.request(daemonReloadRequest{Trigger: "rollback", Rollback: true, RequestedAt: daemonNow().Add(-time.Minute)})
	require.NoError(t, rt.processPendingReload(context.Background()))
	assert.Equal(t, beforeDigest, rt.currentHandle().snapshot.Digest)

	harness, err := os.ReadFile(filepath.Join(rootDir, ".xylem", "HARNESS.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Harness A\n", string(harness))
}

func TestDaemonRuntimeRetainsBusyRunnerAcrossReload(t *testing.T) {
	rootDir, configPath, cfg := writeDaemonReloadRepo(t, "# Harness A\n")
	q := queue.New(filepath.Join(cfg.StateDir, "queue.jsonl"))
	rt, err := newDaemonRuntime(rootDir, configPath, q, worktree.New(rootDir, newCmdRunner(cfg)), cfg)
	require.NoError(t, err)

	markRunnerInFlight(t, rt.currentHandle().drain, 1)
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, ".xylem", "HARNESS.md"), []byte("version B"), 0o644))

	rt.request(daemonReloadRequest{Trigger: "merge", RequestedAt: daemonNow().Add(-time.Minute)})
	require.NoError(t, rt.processPendingReload(context.Background()))

	rt.mu.Lock()
	retired := len(rt.retired)
	oldHandle := rt.retired[0]
	rt.mu.Unlock()
	require.Equal(t, 1, retired)

	markRunnerInFlight(t, oldHandle.drain, 0)
	rt.cleanupRetired()

	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Empty(t, rt.retired)
}

func TestDaemonRuntimeReloadReappliesDaemonRootEnv(t *testing.T) {
	rootDir, configPath, _ := writeDaemonReloadRepo(t, "# Harness A\n")
	envPath := daemonSupervisorEnvFilePath(rootDir, ".env")
	require.NoError(t, os.MkdirAll(filepath.Dir(envPath), 0o755))
	require.NoError(t, os.WriteFile(envPath, []byte("XYLEM_TEST_DAEMON_TOKEN=first\n"), 0o644))

	configYAML := strings.Join([]string{
		"repo: owner/name",
		"tasks:",
		"  fix-bugs:",
		"    labels: [bug]",
		"    workflow: fix-bug",
		"state_dir: " + filepath.Join(rootDir, ".xylem"),
		"claude:",
		"  command: claude",
		"  default_model: claude-sonnet-4-6",
		"  env:",
		"    API_TOKEN: ${XYLEM_TEST_DAEMON_TOKEN}",
		"daemon:",
		"  scan_interval: 1s",
		"  drain_interval: 1s",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(configPath, []byte(configYAML), 0o644))

	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	require.NoError(t, loadDaemonStartupEnv(rootDir, ".env"))

	q := queue.New(filepath.Join(cfg.StateDir, "queue.jsonl"))
	rt, err := newDaemonRuntime(rootDir, configPath, q, worktree.New(rootDir, newCmdRunner(cfg)), cfg)
	require.NoError(t, err)
	assert.Equal(t, "first", daemonEnvValue(rt.currentHandle().scanCmd.extraEnv, "API_TOKEN"))

	require.NoError(t, os.WriteFile(envPath, []byte("XYLEM_TEST_DAEMON_TOKEN=second\n"), 0o644))

	rt.request(daemonReloadRequest{Trigger: "signal", RequestedAt: daemonNow().Add(-time.Minute)})
	require.NoError(t, rt.processPendingReload(context.Background()))
	assert.Equal(t, "second", daemonEnvValue(rt.currentHandle().scanCmd.extraEnv, "API_TOKEN"))
}

func TestValidateDaemonReloadCandidateRejectsInvalidWorkflow(t *testing.T) {
	rootDir, configPath, _ := writeDaemonReloadRepo(t, "# Harness A\n")
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, ".xylem", "workflows", "fix-bug.yaml"), []byte("name: fix-bug\nphases:\n  - name: Bad\n"), 0o644))

	_, err := validateDaemonReloadCandidate(rootDir, configPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validate workflows")
}

func TestSmoke_S5_DaemonReloadPreservesFrozenWorkflowSnapshot(t *testing.T) {
	rootDir, configPath, cfg := writeDaemonReloadRepo(t, "# Harness A\n")
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(rootDir))
	defer func() {
		require.NoError(t, os.Chdir(oldWd))
	}()

	planPromptPath := filepath.Join(rootDir, ".xylem", "prompts", "fix-bug-plan.md")
	implementPromptPath := filepath.Join(rootDir, ".xylem", "prompts", "fix-bug-implement.md")
	mutatedPromptPath := filepath.Join(rootDir, ".xylem", "prompts", "fix-bug-mutated.md")
	require.NoError(t, os.WriteFile(planPromptPath, []byte("Create plan\n"), 0o644))
	require.NoError(t, os.WriteFile(implementPromptPath, []byte("Implement after approval\n"), 0o644))
	require.NoError(t, os.WriteFile(mutatedPromptPath, []byte("Mutated after reload\n"), 0o644))

	workflowPath := filepath.Join(rootDir, ".xylem", "workflows", "fix-bug.yaml")
	originalWorkflow := []byte(strings.Join([]string{
		"name: fix-bug",
		"phases:",
		"  - name: plan",
		"    prompt_file: " + planPromptPath,
		"    max_turns: 1",
		"    gate:",
		"      type: label",
		"      wait_for: plan-approved",
		"      timeout: 24h",
		"  - name: implement",
		"    prompt_file: " + implementPromptPath,
		"    max_turns: 1",
		"",
	}, "\n"))
	require.NoError(t, os.WriteFile(workflowPath, originalWorkflow, 0o644))

	q := queue.New(filepath.Join(cfg.StateDir, "queue.jsonl"))
	rt, err := newDaemonRuntime(rootDir, configPath, q, worktree.New(rootDir, newCmdRunner(cfg)), cfg)
	require.NoError(t, err)
	beforeDigest := rt.currentHandle().snapshot.Digest

	cmdRunner := newSmokeReloadCmdRunner()
	cmdRunner.set([]byte(`{"labels":[{"name":"plan-approved"}]}`),
		"gh", "issue", "view", "1", "--repo", "owner/name", "--json", "labels")
	wireSmokeReloadHandle(rt.currentHandle(), cmdRunner, rootDir)

	_, err = q.Enqueue(queue.Vessel{
		ID:       "issue-1",
		Source:   "github-issue",
		Ref:      "https://github.com/owner/name/issues/1",
		Workflow: "fix-bug",
		Meta: map[string]string{
			"issue_num":    "1",
			"source_repo":  "owner/name",
			"issue_title":  "Bug title",
			"issue_body":   "Bug body",
			"issue_labels": "bug",
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	first, err := rt.currentHandle().drain.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, first.Launched)

	waiting, err := q.FindByID("issue-1")
	require.NoError(t, err)
	require.NotNil(t, waiting)
	require.Equal(t, queue.StateWaiting, waiting.State)
	require.NotEmpty(t, waiting.WorkflowDigest)

	snapshotPath := config.RuntimePath(cfg.StateDir, "phases", waiting.ID, "workflow", waiting.Workflow+".yaml")
	snapshotBytes, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	assert.Equal(t, originalWorkflow, snapshotBytes)

	configBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)
	updatedConfig := strings.Replace(string(configBytes), "drain_interval: 1s", "drain_interval: 2s", 1)
	require.NoError(t, os.WriteFile(configPath, []byte(updatedConfig), 0o644))

	mutatedWorkflow := []byte(strings.Join([]string{
		"name: fix-bug",
		"phases:",
		"  - name: plan",
		"    prompt_file: " + planPromptPath,
		"    max_turns: 1",
		"    gate:",
		"      type: label",
		"      wait_for: plan-approved",
		"      timeout: 24h",
		"  - name: mutated",
		"    prompt_file: " + mutatedPromptPath,
		"    max_turns: 1",
		"",
	}, "\n"))
	require.NoError(t, os.WriteFile(workflowPath, mutatedWorkflow, 0o644))

	cmdRunner.set([]byte(`[{"number":42,"title":"reload control plane","url":"https://github.com/owner/name/pull/42","mergeCommit":{"oid":"abcdef1234567890"},"headRefName":"control-plane"}]`),
		"gh", "pr", "list", "--repo", "owner/name", "--state", "merged", "--json", "number,title,url,mergeCommit,headRefName,labels", "--limit", "20")
	cmdRunner.set([]byte(`{"files":[{"path":".xylem.yml"},{"path":".xylem/workflows/fix-bug.yaml"}]}`),
		"gh", "pr", "view", "42", "--repo", "owner/name", "--json", "files")

	mergeSource := &source.GitHubMerge{
		Repo:                "owner/name",
		Tasks:               map[string]source.MergeTask{},
		Queue:               q,
		CmdRunner:           cmdRunner,
		OnControlPlaneMerge: rt.requestControlPlaneMerge,
	}
	_, err = mergeSource.Scan(context.Background())
	require.NoError(t, err)

	rt.mergeDebounce = 0
	require.NoError(t, rt.processPendingReload(context.Background()))
	assert.Equal(t, "2s", rt.currentConfig().Daemon.DrainInterval)
	assert.NotEqual(t, beforeDigest, rt.currentHandle().snapshot.Digest)

	wireSmokeReloadHandle(rt.currentHandle(), cmdRunner, rootDir)

	logs := readDaemonReloadLogEntries(t, cfg)
	require.NotEmpty(t, logs)
	lastLog := logs[len(logs)-1]
	assert.Equal(t, "merge", lastLog.Trigger)
	assert.Equal(t, "applied", lastLog.Result)
	assert.Equal(t, beforeDigest, lastLog.BeforeDigest)
	assert.Equal(t, rt.currentHandle().snapshot.Digest, lastLog.AfterDigest)
	assert.Equal(t, 42, lastLog.PRNumber)
	assert.Equal(t, "abcdef1234567890", lastLog.MergeCommitSHA)

	rt.runChecks(context.Background(), false)

	resumed, err := q.FindByID("issue-1")
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.Equal(t, queue.StatePending, resumed.State)

	second, err := rt.currentHandle().drain.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, second.Completed)

	final, err := q.FindByID("issue-1")
	require.NoError(t, err)
	require.NotNil(t, final)
	assert.Equal(t, queue.StateCompleted, final.State)
	assert.Equal(t, waiting.WorkflowDigest, final.WorkflowDigest)

	_, snapshotDigest, err := workflow.LoadWithDigest(snapshotPath)
	require.NoError(t, err)
	assert.Equal(t, waiting.WorkflowDigest, snapshotDigest)

	require.Len(t, cmdRunner.phasePrompts(), 2)
	assert.Contains(t, cmdRunner.phasePrompts()[0], "Create plan")
	assert.Contains(t, cmdRunner.phasePrompts()[1], "Implement after approval")
	assert.NotContains(t, cmdRunner.phasePrompts()[1], "Mutated after reload")
}

func writeDaemonReloadRepo(t *testing.T, harnessBody string) (string, string, *config.Config) {
	t.Helper()

	rootDir := t.TempDir()
	stateDir := filepath.Join(rootDir, ".xylem")
	promptPath := filepath.Join(rootDir, ".xylem", "prompts", "fix-bug.md")
	workflowPath := filepath.Join(rootDir, ".xylem", "workflows", "fix-bug.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(promptPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(promptPath, []byte("Fix the bug.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, ".xylem", "HARNESS.md"), []byte(harnessBody), 0o644))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: fix-bug\nphases:\n  - name: analyze\n    prompt_file: "+promptPath+"\n    max_turns: 1\n"), 0o644))

	configPath := filepath.Join(rootDir, ".xylem.yml")
	configYAML := strings.Join([]string{
		"repo: owner/name",
		"tasks:",
		"  fix-bugs:",
		"    labels: [bug]",
		"    workflow: fix-bug",
		"state_dir: " + stateDir,
		"claude:",
		"  command: claude",
		"  default_model: claude-sonnet-4-6",
		"daemon:",
		"  scan_interval: 1s",
		"  drain_interval: 1s",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(configPath, []byte(configYAML), 0o644))
	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	return rootDir, configPath, cfg
}

func readDaemonReloadLogEntries(t *testing.T, cfg *config.Config) []daemonReloadLogEntry {
	t.Helper()

	data, err := os.ReadFile(daemonReloadLogPath(cfg))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	entries := make([]daemonReloadLogEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry daemonReloadLogEntry
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		entries = append(entries, entry)
	}
	return entries
}

func markRunnerInFlight(t *testing.T, r *runner.Runner, value int32) {
	t.Helper()

	field := reflect.ValueOf(r).Elem().FieldByName("inFlight")
	ptr := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	store := ptr.Addr().MethodByName("Store")
	store.Call([]reflect.Value{reflect.ValueOf(value)})
}

type smokeReloadCmdRunner struct {
	mu      sync.Mutex
	outputs map[string][]byte
	prompts []string
}

func newSmokeReloadCmdRunner() *smokeReloadCmdRunner {
	return &smokeReloadCmdRunner{
		outputs: make(map[string][]byte),
	}
}

func (m *smokeReloadCmdRunner) set(output []byte, name string, args ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outputs[m.key(name, args...)] = append([]byte(nil), output...)
}

func (m *smokeReloadCmdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return m.RunOutput(ctx, name, args...)
}

func (m *smokeReloadCmdRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out, ok := m.outputs[m.key(name, args...)]
	if !ok {
		return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}
	return append([]byte(nil), out...), nil
}

func (m *smokeReloadCmdRunner) RunProcess(_ context.Context, _ string, name string, args ...string) error {
	_, err := m.RunOutput(context.Background(), name, args...)
	return err
}

func (m *smokeReloadCmdRunner) RunPhase(_ context.Context, _ string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	prompt, err := io.ReadAll(stdin)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.prompts = append(m.prompts, string(prompt))
	m.mu.Unlock()
	return []byte("ok"), nil
}

func (m *smokeReloadCmdRunner) RunPhaseWithEnv(_ context.Context, _ string, _ []string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	return m.RunPhase(context.Background(), "", stdin, "")
}

func (m *smokeReloadCmdRunner) phasePrompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

func (m *smokeReloadCmdRunner) key(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

type smokeReloadWorktree struct {
	path string
}

func (w *smokeReloadWorktree) Create(context.Context, string) (string, error) {
	return w.path, nil
}

func (w *smokeReloadWorktree) Remove(context.Context, string) error {
	return nil
}

type smokeReloadSource struct{}

func (s *smokeReloadSource) Name() string                                   { return "github-issue" }
func (s *smokeReloadSource) Scan(context.Context) ([]queue.Vessel, error)   { return nil, nil }
func (s *smokeReloadSource) OnEnqueue(context.Context, queue.Vessel) error  { return nil }
func (s *smokeReloadSource) OnStart(context.Context, queue.Vessel) error    { return nil }
func (s *smokeReloadSource) OnWait(context.Context, queue.Vessel) error     { return nil }
func (s *smokeReloadSource) OnResume(context.Context, queue.Vessel) error   { return nil }
func (s *smokeReloadSource) OnComplete(context.Context, queue.Vessel) error { return nil }
func (s *smokeReloadSource) OnFail(context.Context, queue.Vessel) error     { return nil }
func (s *smokeReloadSource) OnTimedOut(context.Context, queue.Vessel) error { return nil }
func (s *smokeReloadSource) RemoveRunningLabel(context.Context, queue.Vessel) error {
	return nil
}
func (s *smokeReloadSource) BranchName(v queue.Vessel) string {
	return "issue/" + v.ID
}

func wireSmokeReloadHandle(handle *daemonRunnerHandle, cmdRunner *smokeReloadCmdRunner, worktreePath string) {
	handle.drain.Runner = cmdRunner
	handle.drain.Worktree = &smokeReloadWorktree{path: worktreePath}
	handle.drain.Sources = map[string]source.Source{
		"github-issue": &smokeReloadSource{},
	}
}
