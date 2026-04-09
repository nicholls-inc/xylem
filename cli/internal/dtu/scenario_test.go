package dtu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	dtu "github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/dtushim"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	runnerpkg "github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

type scenarioPhase struct {
	name           string
	phaseType      string
	run            string
	prompt         string
	allowedTools   string
	gateType       string
	gateRun        string
	gateRetries    int
	gateRetryDelay string
	gateWaitFor    string
}

type dtuScenarioEnv struct {
	repoDir      string
	stateDir     string
	manifestPath string
	store        *dtu.Store
	queue        *queue.Queue
	cmdRunner    *dtuScenarioCmdRunner
}

type dtuScenarioCmdRunner struct {
	env []string
}

type dtuExitError struct {
	code   int
	stderr string
}

func (e *dtuExitError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return fmt.Sprintf("exit status %d: %s", e.code, msg)
}

func (e *dtuExitError) ExitCode() int {
	return e.code
}

func scenarioFixturePath(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

func copyScenarioRepoFixture(t *testing.T, name, dst string) {
	t.Helper()

	src := scenarioFixturePath(t, name)
	info, err := os.Stat(src)
	if err != nil {
		t.Fatalf("Stat(%q): %v", src, err)
	}
	if !info.IsDir() {
		t.Fatalf("scenario fixture %q is not a directory", src)
	}

	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dstPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	}); err != nil {
		t.Fatalf("copy scenario repo fixture %q to %q: %v", src, dst, err)
	}
}

func newScenarioEnv(t *testing.T, fixtureName string) *dtuScenarioEnv {
	t.Helper()

	repoDir := t.TempDir()
	stateDir := filepath.Join(repoDir, ".xylem")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", stateDir, err)
	}

	manifestPath := scenarioFixturePath(t, fixtureName)
	manifest, err := dtu.LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest(%q): %v", manifestPath, err)
	}
	universeID := manifest.Metadata.Name
	state, err := dtu.NewState(universeID, manifest, manifestPath, time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewState(%q): %v", universeID, err)
	}
	store, err := dtu.NewStore(stateDir, universeID)
	if err != nil {
		t.Fatalf("NewStore(%q, %q): %v", stateDir, universeID, err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save(%q): %v", store.Path(), err)
	}

	t.Setenv(dtu.EnvStatePath, store.Path())
	t.Setenv(dtu.EnvStateDir, stateDir)
	t.Setenv(dtu.EnvUniverseID, universeID)

	cmdRunner := &dtuScenarioCmdRunner{
		env: []string{
			dtu.EnvStatePath + "=" + store.Path(),
			dtu.EnvStateDir + "=" + stateDir,
			dtu.EnvUniverseID + "=" + universeID,
		},
	}

	return &dtuScenarioEnv{
		repoDir:      repoDir,
		stateDir:     stateDir,
		manifestPath: manifestPath,
		store:        store,
		queue:        queue.New(filepath.Join(stateDir, "queue.jsonl")),
		cmdRunner:    cmdRunner,
	}
}

func (r *dtuScenarioCmdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.execute(ctx, nil, name, args...)
}

func (r *dtuScenarioCmdRunner) RunOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if filepath.Base(name) == "sh" {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), r.env...)
		return cmd.CombinedOutput()
	}
	return r.execute(ctx, nil, name, args...)
}

func (r *dtuScenarioCmdRunner) RunProcess(ctx context.Context, _ string, name string, args ...string) error {
	_, err := r.execute(ctx, nil, name, args...)
	return err
}

func (r *dtuScenarioCmdRunner) RunPhase(ctx context.Context, _ string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.execute(ctx, stdin, name, args...)
}

func (r *dtuScenarioCmdRunner) execute(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	code := dtushim.Execute(ctx, name, args, stdin, &stdout, &stderr, r.env)
	if code != 0 {
		return stdout.Bytes(), &dtuExitError{code: code, stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

func withWorkingDir(t *testing.T, dir string) func() {
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

func writeScenarioHarness(t *testing.T, repoDir, content string) {
	t.Helper()

	if strings.TrimSpace(content) == "" {
		return
	}
	harnessPath := filepath.Join(repoDir, ".xylem", "HARNESS.md")
	if err := os.MkdirAll(filepath.Dir(harnessPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(harnessPath), err)
	}
	if err := os.WriteFile(harnessPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", harnessPath, err)
	}
}

func writeScenarioWorkflow(t *testing.T, repoDir, workflowName string, phases []scenarioPhase) {
	t.Helper()

	workflowDir := filepath.Join(repoDir, ".xylem", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", workflowDir, err)
	}

	var phaseYAML strings.Builder
	for _, phase := range phases {
		fmt.Fprintf(&phaseYAML, "  - name: %s\n", phase.name)
		if phase.phaseType == "command" {
			phaseYAML.WriteString("    type: command\n")
			fmt.Fprintf(&phaseYAML, "    run: %q\n", phase.run)
		} else {
			promptPath := filepath.Join(repoDir, ".xylem", "prompts", workflowName, phase.name+".md")
			if err := os.MkdirAll(filepath.Dir(promptPath), 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", filepath.Dir(promptPath), err)
			}
			if err := os.WriteFile(promptPath, []byte(phase.prompt), 0o644); err != nil {
				t.Fatalf("WriteFile(%q): %v", promptPath, err)
			}

			fmt.Fprintf(&phaseYAML, "    prompt_file: %s\n", promptPath)
			phaseYAML.WriteString("    max_turns: 3\n")
			if phase.allowedTools != "" {
				fmt.Fprintf(&phaseYAML, "    allowed_tools: %q\n", phase.allowedTools)
			}
		}

		switch {
		case phase.gateType == "command" || phase.gateRun != "":
			phaseYAML.WriteString("    gate:\n")
			phaseYAML.WriteString("      type: command\n")
			fmt.Fprintf(&phaseYAML, "      run: %q\n", phase.gateRun)
			if phase.gateRetries > 0 {
				fmt.Fprintf(&phaseYAML, "      retries: %d\n", phase.gateRetries)
			}
			if phase.gateRetryDelay != "" {
				fmt.Fprintf(&phaseYAML, "      retry_delay: %q\n", phase.gateRetryDelay)
			}
		case phase.gateWaitFor != "" || phase.gateType == "label":
			phaseYAML.WriteString("    gate:\n")
			phaseYAML.WriteString("      type: label\n")
			fmt.Fprintf(&phaseYAML, "      wait_for: %s\n", phase.gateWaitFor)
			phaseYAML.WriteString("      timeout: 1h\n")
		}
	}

	workflowContent := fmt.Sprintf("name: %s\nphases:\n%s", workflowName, phaseYAML.String())
	workflowPath := filepath.Join(workflowDir, workflowName+".yaml")
	if err := os.WriteFile(workflowPath, []byte(workflowContent), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", workflowPath, err)
	}
}

func baseScenarioConfig(stateDir string) *config.Config {
	return &config.Config{
		Concurrency: 1,
		MaxTurns:    5,
		Timeout:     "1m",
		StateDir:    stateDir,
		Claude: config.ClaudeConfig{
			Command: "claude",
		},
		Copilot: config.CopilotConfig{
			Command: "copilot",
		},
	}
}

func newDrainRunner(t *testing.T, cfg *config.Config, q *queue.Queue, cmdRunner *dtuScenarioCmdRunner, repoDir string, src source.Source) *runnerpkg.Runner {
	t.Helper()

	drainer := runnerpkg.New(cfg, q, worktree.New(repoDir, cmdRunner), cmdRunner)
	drainer.Sources = map[string]source.Source{
		src.Name(): src,
	}
	drainer.AuditLog = intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	drainer.Intermediary = intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), drainer.AuditLog, nil)

	if cfg.ObservabilityEnabled() && cfg.Observability.Endpoint != "" {
		tracer, err := observability.NewTracer(observability.TracerConfig{
			ServiceName:    "xylem",
			ServiceVersion: "",
			Endpoint:       cfg.Observability.Endpoint,
			Insecure:       cfg.Observability.Insecure,
			SampleRate:     cfg.ObservabilitySampleRate(),
		})
		if err != nil {
			t.Fatalf("NewTracer() error = %v", err)
		}
		drainer.Tracer = tracer
		t.Cleanup(func() {
			if err := tracer.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown() error = %v", err)
			}
		})
	}

	return drainer
}

func loadState(t *testing.T, store *dtu.Store) *dtu.State {
	t.Helper()

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load(%q): %v", store.Path(), err)
	}
	return state
}

func loadVesselSummary(t *testing.T, stateDir, vesselID string) runnerpkg.VesselSummary {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(stateDir, "phases", vesselID, "summary.json"))
	if err != nil {
		t.Fatalf("read summary.json for %q: %v", vesselID, err)
	}

	var summary runnerpkg.VesselSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshal summary.json for %q: %v", vesselID, err)
	}

	return summary
}

func readIssueLabels(t *testing.T, store *dtu.Store, repoSlug string, number int) []string {
	t.Helper()

	state := loadState(t, store)
	repo := state.RepositoryBySlug(repoSlug)
	if repo == nil {
		t.Fatalf("RepositoryBySlug(%q) = nil", repoSlug)
	}
	issue := repo.IssueByNumber(number)
	if issue == nil {
		t.Fatalf("IssueByNumber(%d) = nil", number)
		return nil
	}
	return append([]string(nil), issue.Labels...)
}

func readPRLabels(t *testing.T, store *dtu.Store, repoSlug string, number int) []string {
	t.Helper()

	state := loadState(t, store)
	repo := state.RepositoryBySlug(repoSlug)
	if repo == nil {
		t.Fatalf("RepositoryBySlug(%q) = nil", repoSlug)
	}
	pr := repo.PullRequestByNumber(number)
	if pr == nil {
		t.Fatalf("PullRequestByNumber(%d) = nil", number)
		return nil
	}
	return append([]string(nil), pr.Labels...)
}

func assertStringSliceEqual(t *testing.T, got []string, want []string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func readEvents(t *testing.T, store *dtu.Store) []dtu.Event {
	t.Helper()

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents(%q): %v", store.EventLogPath(), err)
	}
	return events
}

func filterShimEvents(events []dtu.Event, kind dtu.EventKind, command string, argsPrefix []string) []dtu.Event {
	filtered := make([]dtu.Event, 0, len(events))
	for _, event := range events {
		if event.Kind != kind || event.Shim == nil || event.Shim.Command != command {
			continue
		}
		if len(argsPrefix) > 0 && !hasStringPrefix(event.Shim.Args, argsPrefix) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func filterVesselEvents(events []dtu.Event, vesselID string) []dtu.Event {
	filtered := make([]dtu.Event, 0, len(events))
	for _, event := range events {
		if event.Kind != dtu.EventKindVesselUpdated || event.Vessel == nil {
			continue
		}
		if vesselID != "" && event.Vessel.VesselID != vesselID {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func hasStringPrefix(values, prefix []string) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(values) < len(prefix) {
		return false
	}
	for i := range prefix {
		if values[i] != prefix[i] {
			return false
		}
	}
	return true
}

func TestScenarioIssueLabelGateWaitsThenResumes(t *testing.T) {
	env := newScenarioEnv(t, "issue-label-gate.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "plan", prompt: "Plan issue {{.Issue.Number}}", gateWaitFor: "plan-approved"},
		{name: "implement", prompt: "Implement issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:    "queued",
						Running:   "in-progress",
						Completed: "done",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 1), []string{"bug", "queued"})

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)

	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Waiting != 1 {
		t.Fatalf("DrainResult.Waiting = %d, want 1", drainResult.Waiting)
	}

	vessel, err := env.queue.FindByID("issue-1")
	if err != nil {
		t.Fatalf("FindByID(issue-1) error = %v", err)
	}
	if vessel.State != queue.StateWaiting {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateWaiting)
	}
	if vessel.CurrentPhase != 1 {
		t.Fatalf("vessel.CurrentPhase = %d, want 1", vessel.CurrentPhase)
	}
	if vessel.WorktreePath == "" {
		t.Fatal("vessel.WorktreePath = empty, want persisted worktree")
	}
	worktreePath := filepath.Join(env.repoDir, vessel.WorktreePath)
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("Stat(%q): %v", worktreePath, err)
	}
	state := loadState(t, env.store)
	if got := len(state.RepositoryBySlug("owner/repo").Worktrees); got != 1 {
		t.Fatalf("len(Worktrees) = %d, want 1 while waiting", got)
	}

	for i := 0; i < 2; i++ {
		drainer.CheckWaitingVessels(context.Background())
		vessel, err = env.queue.FindByID("issue-1")
		if err != nil {
			t.Fatalf("FindByID(issue-1) after poll %d: %v", i+1, err)
		}
		if vessel.State != queue.StateWaiting {
			t.Fatalf("poll %d: vessel.State = %q, want waiting", i+1, vessel.State)
		}
	}

	drainer.CheckWaitingVessels(context.Background())
	vessel, err = env.queue.FindByID("issue-1")
	if err != nil {
		t.Fatalf("FindByID(issue-1) after third poll: %v", err)
	}
	if vessel.State != queue.StatePending {
		t.Fatalf("vessel.State after third poll = %q, want %q", vessel.State, queue.StatePending)
	}
	if vessel.WaitingFor != "" || vessel.WaitingSince != nil {
		t.Fatalf("waiting metadata not cleared on resume: %+v", vessel)
	}
	// The "in-progress" label was removed when the vessel entered the waiting
	// state (runVessel defer). It will be re-added when the vessel resumes.
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 1), []string{"bug", "plan-approved"})

	drainResult, err = drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("second Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("second DrainResult.Completed = %d, want 1", drainResult.Completed)
	}
	// Trigger label "bug" is removed by OnComplete to prevent duplicate
	// enqueue on the next scan. Only "done" and mid-workflow labels remain.
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 1), []string{"done", "plan-approved"})

	vessel, err = env.queue.FindByID("issue-1")
	if err != nil {
		t.Fatalf("FindByID(issue-1) final: %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("final vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree %q still exists, err = %v", worktreePath, err)
	}
	state = loadState(t, env.store)
	if got := len(state.RepositoryBySlug("owner/repo").Worktrees); got != 0 {
		t.Fatalf("len(Worktrees) = %d, want 0 after completion", got)
	}

	events := readEvents(t, env.store)
	labelPolls := filterShimEvents(events, dtu.EventKindShimResult, "gh", []string{"issue", "view", "1", "--repo", "owner/repo", "--json", "labels"})
	if len(labelPolls) != 3 {
		t.Fatalf("len(label poll results) = %d, want 3", len(labelPolls))
	}
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 2 {
		t.Fatalf("len(claude invocations) = %d, want 2", len(claudeInvocations))
	}
	if claudeInvocations[0].Shim.Phase != "plan" || claudeInvocations[1].Shim.Phase != "implement" {
		t.Fatalf("unexpected claude phases: %#v", claudeInvocations)
	}
	vesselEvents := filterVesselEvents(events, "issue-1")
	if len(vesselEvents) == 0 {
		t.Fatalf("missing vessel events in %#v", events)
	}
	var (
		sawWaiting  bool
		sawResume   bool
		sawComplete bool
	)
	for _, event := range vesselEvents {
		if event.Vessel == nil || event.Vessel.Current == nil {
			continue
		}
		switch {
		case event.Vessel.OldState == string(queue.StateRunning) && event.Vessel.NewState == string(queue.StateWaiting):
			sawWaiting = event.Vessel.Current.WaitingFor == "plan-approved" && event.Vessel.Current.CurrentPhase == 1 && event.Vessel.Current.FailedPhase == "plan"
		case event.Vessel.OldState == string(queue.StateWaiting) && event.Vessel.NewState == string(queue.StatePending):
			sawResume = event.Vessel.Current.WaitingFor == "" && event.Vessel.Current.GateRetries == 0
		case event.Vessel.NewState == string(queue.StateCompleted):
			sawComplete = true
		}
	}
	if !sawWaiting {
		t.Fatalf("missing waiting vessel event in %#v", vesselEvents)
	}
	if !sawResume {
		t.Fatalf("missing resume vessel event in %#v", vesselEvents)
	}
	if !sawComplete {
		t.Fatalf("missing completion vessel event in %#v", vesselEvents)
	}
}

func TestScenarioGitHubPROfflineCopilot(t *testing.T) {
	env := newScenarioEnv(t, "github-pr-copilot.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioHarness(t, env.repoDir, "HARNESS HEADER")
	writeScenarioWorkflow(t, env.repoDir, "review-pr", []scenarioPhase{
		{name: "review", prompt: "Review PR {{.Issue.Number}}", allowedTools: "Read,Write"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"pull-requests": {
			Type: "github-pr",
			Repo: "owner/repo",
			LLM:  "copilot",
			Tasks: map[string]config.Task{
				"ready-prs": {
					Labels:   []string{"ready"},
					Workflow: "review-pr",
					StatusLabels: &config.StatusLabels{
						Completed: "reviewed",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHubPR{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}
	assertStringSliceEqual(t, readPRLabels(t, env.store, "owner/repo", 10), []string{"ready", "reviewed"})

	events := readEvents(t, env.store)
	copilotInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "copilot", nil)
	if len(copilotInvocations) != 1 {
		t.Fatalf("len(copilot invocations) = %d, want 1", len(copilotInvocations))
	}
	invocation := copilotInvocations[0].Shim
	if invocation.Provider != dtu.ProviderCopilot {
		t.Fatalf("Provider = %q, want %q", invocation.Provider, dtu.ProviderCopilot)
	}
	if invocation.Phase != "review" || invocation.Script != "review" {
		t.Fatalf("unexpected copilot phase/script: %#v", invocation)
	}
	if !strings.Contains(invocation.Prompt, "HARNESS HEADER") || !strings.Contains(invocation.Prompt, "Review PR 10") {
		t.Fatalf("copilot prompt = %q, want harness + rendered prompt", invocation.Prompt)
	}
	if invocation.PromptHash == "" {
		t.Fatal("PromptHash = empty, want hashed prompt")
	}
	if !hasStringPrefix(invocation.Args, []string{"-p", invocation.Prompt, "-s"}) {
		t.Fatalf("unexpected copilot args prefix: %v", invocation.Args)
	}
	if !hasStringPrefix(invocation.Args[3:], []string{"--available-tools", "Read,Write", "--allow-all-tools"}) {
		t.Fatalf("missing copilot allowed-tools args: %v", invocation.Args)
	}
}

func TestScenarioGitHubPREventsChecksFailed(t *testing.T) {
	env := newScenarioEnv(t, "github-pr-events-checks-failed.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-ci", []scenarioPhase{
		{name: "respond", prompt: "Respond to failed checks for PR {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"pr-events": {
			Type: "github-pr-events",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"checks": {
					Workflow: "fix-ci",
					On: &config.PREventsConfig{
						ChecksFailed: true,
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	vessels, err := env.queue.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 1 || vessels[0].Source != "github-pr-events" || vessels[0].Meta["event_type"] != "checks_failed" {
		t.Fatalf("unexpected queued vessels: %#v", vessels)
	}

	src := &source.GitHubPREvents{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	events := readEvents(t, env.store)
	checkResults := filterShimEvents(events, dtu.EventKindShimResult, "gh", []string{"pr", "checks", "21"})
	if len(checkResults) != 1 {
		t.Fatalf("len(pr check results) = %d, want 1", len(checkResults))
	}
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) == 0 || claudeInvocations[len(claudeInvocations)-1].Shim.Phase != "respond" {
		t.Fatalf("missing claude respond invocation: %#v", claudeInvocations)
	}
}

func TestScenarioGithubPREventsProviderFailure(t *testing.T) {
	env := newScenarioEnv(t, "github-pr-events-provider-failure.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "pr-review", []scenarioPhase{
		{name: "review", prompt: "Review PR {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"pr-events": {
			Type: "github-pr-events",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"review": {
					Workflow: "pr-review",
					On: &config.PREventsConfig{
						Labels: []string{"review-me"},
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	vessels, err := env.queue.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vessels) != 1 || vessels[0].Source != "github-pr-events" || vessels[0].Meta["event_type"] != "label" {
		t.Fatalf("unexpected queued vessels: %#v", vessels)
	}

	src := &source.GitHubPREvents{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", drainResult.Failed)
	}

	vessel, err := env.queue.FindByID(vessels[0].ID)
	if err != nil {
		t.Fatalf("FindByID(%q) error = %v", vessels[0].ID, err)
	}
	if vessel.State != queue.StateFailed {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateFailed)
	}
	if !strings.Contains(vessel.Error, "exit status 1") {
		t.Fatalf("vessel.Error = %q, want substring %q", vessel.Error, "exit status 1")
	}

	events := readEvents(t, env.store)
	var reviewResult *dtu.ShimEvent
	for _, event := range events {
		if event.Kind == dtu.EventKindShimResult && event.Shim != nil && event.Shim.Command == "claude" && event.Shim.Phase == "review" {
			reviewResult = event.Shim
			break
		}
	}
	if reviewResult == nil || reviewResult.ExitCode == nil || *reviewResult.ExitCode != 1 {
		t.Fatalf("missing review result with exit code 1: %#v", events)
	}
}

func TestScenarioGitHubMergeHappyPath(t *testing.T) {
	env := newScenarioEnv(t, "github-merge-happy.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "post-merge", []scenarioPhase{
		{name: "postmerge", prompt: "Handle merged PR {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"merged-prs": {
			Type: "github-merge",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"merged": {Workflow: "post-merge"},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHubMerge{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err := env.queue.FindByID("merge-pr-30-cafebabe")
	if err != nil {
		t.Fatalf("FindByID(merge-pr-30-cafebabe) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}

	events := readEvents(t, env.store)
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 1 || claudeInvocations[0].Shim.Phase != "postmerge" {
		t.Fatalf("unexpected merge claude invocations: %#v", claudeInvocations)
	}
}

func TestScenarioIssueProviderFailureMarksFailed(t *testing.T) {
	env := newScenarioEnv(t, "issue-provider-failure.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "plan", prompt: "Plan issue {{.Issue.Number}}"},
		{name: "implement", prompt: "Implement issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Failed: "failed",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", drainResult.Failed)
	}

	vessel, err := env.queue.FindByID("issue-2")
	if err != nil {
		t.Fatalf("FindByID(issue-2) error = %v", err)
	}
	if vessel.State != queue.StateFailed {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateFailed)
	}
	if !strings.Contains(vessel.Error, "exit status 2") {
		t.Fatalf("vessel.Error = %q, want exit status 2", vessel.Error)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 2), []string{"bug", "failed"})

	events := readEvents(t, env.store)
	var implementResult *dtu.ShimEvent
	for _, event := range events {
		if event.Kind == dtu.EventKindShimResult && event.Shim != nil && event.Shim.Command == "claude" && event.Shim.Phase == "implement" {
			implementResult = event.Shim
			break
		}
	}
	if implementResult == nil || implementResult.ExitCode == nil || *implementResult.ExitCode != 2 {
		t.Fatalf("missing implement result with exit code 2: %#v", events)
	}
}

func TestScenarioIssueGitFetchRetrySucceeds(t *testing.T) {
	env := newScenarioEnv(t, "issue-git-fetch-retry.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "fix", prompt: "Fix issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	events := readEvents(t, env.store)
	fetchResults := filterShimEvents(events, dtu.EventKindShimResult, "git", []string{"fetch", "origin", "main"})
	if len(fetchResults) != 3 {
		t.Fatalf("len(fetch results) = %d, want 3", len(fetchResults))
	}
	gotAttempts := make([]int, 0, len(fetchResults))
	gotCodes := make([]int, 0, len(fetchResults))
	for _, event := range fetchResults {
		gotAttempts = append(gotAttempts, event.Shim.Attempt)
		if event.Shim.ExitCode == nil {
			gotCodes = append(gotCodes, 0)
			continue
		}
		gotCodes = append(gotCodes, *event.Shim.ExitCode)
	}
	if !reflect.DeepEqual(gotAttempts, []int{1, 2, 3}) {
		t.Fatalf("fetch attempts = %v, want [1 2 3]", gotAttempts)
	}
	if !reflect.DeepEqual(gotCodes, []int{128, 128, 0}) {
		t.Fatalf("fetch exit codes = %v, want [128 128 0]", gotCodes)
	}
}

func TestScenarioIssueGitFetchRetryExitCode1Succeeds(t *testing.T) {
	env := newScenarioEnv(t, "issue-git-fetch-retry-exit-1.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "fix", prompt: "Fix issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:    "queued",
						Running:   "in-progress",
						Completed: "done",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 7), []string{"bug", "queued"})

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err := env.queue.FindByID("issue-7")
	if err != nil {
		t.Fatalf("FindByID(issue-7) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}
	// Trigger label "bug" removed by OnComplete; only terminal status remains.
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 7), []string{"done"})

	state := loadState(t, env.store)
	if got := len(state.RepositoryBySlug("owner/repo").Worktrees); got != 0 {
		t.Fatalf("len(Worktrees) = %d, want 0 after completion", got)
	}

	events := readEvents(t, env.store)
	fetchResults := filterShimEvents(events, dtu.EventKindShimResult, "git", []string{"fetch", "origin", "main"})
	if len(fetchResults) != 2 {
		t.Fatalf("len(fetch results) = %d, want 2", len(fetchResults))
	}
	gotAttempts := make([]int, 0, len(fetchResults))
	gotCodes := make([]int, 0, len(fetchResults))
	for _, event := range fetchResults {
		gotAttempts = append(gotAttempts, event.Shim.Attempt)
		if event.Shim.ExitCode == nil {
			gotCodes = append(gotCodes, 0)
			continue
		}
		gotCodes = append(gotCodes, *event.Shim.ExitCode)
	}
	if !reflect.DeepEqual(gotAttempts, []int{1, 2}) {
		t.Fatalf("fetch attempts = %v, want [1 2]", gotAttempts)
	}
	if !reflect.DeepEqual(gotCodes, []int{1, 0}) {
		t.Fatalf("fetch exit codes = %v, want [1 0]", gotCodes)
	}
}

func TestScenarioIssueGitWorktreeAddRetryExitCode255Succeeds(t *testing.T) {
	env := newScenarioEnv(t, "issue-git-worktree-add-retry-exit-255.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "fix", prompt: "Fix issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:    "queued",
						Running:   "in-progress",
						Completed: "done",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 8), []string{"bug", "queued"})

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err := env.queue.FindByID("issue-8")
	if err != nil {
		t.Fatalf("FindByID(issue-8) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}
	// Trigger label "bug" removed by OnComplete; only terminal status remains.
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 8), []string{"done"})

	state := loadState(t, env.store)
	if got := len(state.RepositoryBySlug("owner/repo").Worktrees); got != 0 {
		t.Fatalf("len(Worktrees) = %d, want 0 after completion", got)
	}

	events := readEvents(t, env.store)
	fetchResults := filterShimEvents(events, dtu.EventKindShimResult, "git", []string{"fetch", "origin", "main"})
	if len(fetchResults) != 1 {
		t.Fatalf("len(fetch results) = %d, want 1", len(fetchResults))
	}
	worktreeResults := filterShimEvents(events, dtu.EventKindShimResult, "git", []string{"worktree", "add"})
	if len(worktreeResults) != 2 {
		t.Fatalf("len(worktree add results) = %d, want 2", len(worktreeResults))
	}
	gotAttempts := make([]int, 0, len(worktreeResults))
	gotCodes := make([]int, 0, len(worktreeResults))
	for _, event := range worktreeResults {
		gotAttempts = append(gotAttempts, event.Shim.Attempt)
		if event.Shim.ExitCode == nil {
			gotCodes = append(gotCodes, 0)
			continue
		}
		gotCodes = append(gotCodes, *event.Shim.ExitCode)
	}
	if !reflect.DeepEqual(gotAttempts, []int{1, 2}) {
		t.Fatalf("worktree add attempts = %v, want [1 2]", gotAttempts)
	}
	if !reflect.DeepEqual(gotCodes, []int{255, 0}) {
		t.Fatalf("worktree add exit codes = %v, want [255 0]", gotCodes)
	}
}

func TestScenarioIssueGitWorktreeAddRetryExitCode128Succeeds(t *testing.T) {
	env := newScenarioEnv(t, "issue-git-worktree-add-retry-exit-128.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "fix", prompt: "Fix issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:    "queued",
						Running:   "in-progress",
						Completed: "done",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err := env.queue.FindByID("issue-9")
	if err != nil {
		t.Fatalf("FindByID(issue-9) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}

	events := readEvents(t, env.store)
	worktreeResults := filterShimEvents(events, dtu.EventKindShimResult, "git", []string{"worktree", "add"})
	if len(worktreeResults) != 2 {
		t.Fatalf("len(worktree add results) = %d, want 2", len(worktreeResults))
	}
	gotAttempts := make([]int, 0, len(worktreeResults))
	gotCodes := make([]int, 0, len(worktreeResults))
	for _, event := range worktreeResults {
		gotAttempts = append(gotAttempts, event.Shim.Attempt)
		if event.Shim.ExitCode == nil {
			gotCodes = append(gotCodes, 0)
			continue
		}
		gotCodes = append(gotCodes, *event.Shim.ExitCode)
	}
	if !reflect.DeepEqual(gotAttempts, []int{1, 2}) {
		t.Fatalf("worktree add attempts = %v, want [1 2]", gotAttempts)
	}
	if !reflect.DeepEqual(gotCodes, []int{128, 0}) {
		t.Fatalf("worktree add exit codes = %v, want [128 0]", gotCodes)
	}
}

func TestScenarioIssueGitWorktreeAddExhaustRetriesExitCode128Fails(t *testing.T) {
	env := newScenarioEnv(t, "issue-git-worktree-add-exhaust-exit-128.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "fix", prompt: "Fix issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
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
						Failed:  "xylem-failed",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", drainResult.Failed)
	}

	vessel, err := env.queue.FindByID("issue-10")
	if err != nil {
		t.Fatalf("FindByID(issue-10) error = %v", err)
	}
	if vessel.State != queue.StateFailed {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateFailed)
	}
	if !strings.Contains(vessel.Error, "git worktree add") {
		t.Fatalf("vessel.Error = %q, want it to contain 'git worktree add'", vessel.Error)
	}

	// Verify the failed label was applied
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 10), []string{"bug", "xylem-failed"})

	// All 3 retry attempts should have exit code 128
	events := readEvents(t, env.store)
	worktreeResults := filterShimEvents(events, dtu.EventKindShimResult, "git", []string{"worktree", "add"})
	if len(worktreeResults) != 3 {
		t.Fatalf("len(worktree add results) = %d, want 3 (all retries exhausted)", len(worktreeResults))
	}
	for i, event := range worktreeResults {
		if event.Shim.ExitCode == nil || *event.Shim.ExitCode != 128 {
			code := 0
			if event.Shim.ExitCode != nil {
				code = *event.Shim.ExitCode
			}
			t.Fatalf("worktree add attempt %d exit code = %d, want 128", i+1, code)
		}
	}
}

func TestScenarioIssueMalformedGHOutputFailsScan(t *testing.T) {
	env := newScenarioEnv(t, "issue-gh-malformed.yaml")
	defer withWorkingDir(t, env.repoDir)()

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	_, err := scan.Scan(context.Background())
	if err == nil {
		t.Fatal("Scan() error = nil, want malformed gh parse error")
	}
	if !strings.Contains(err.Error(), "parse gh search output") {
		t.Fatalf("Scan() error = %v, want parse gh search output", err)
	}

	vessels, listErr := env.queue.List()
	if listErr != nil {
		t.Fatalf("List() error = %v", listErr)
	}
	if len(vessels) != 0 {
		t.Fatalf("len(vessels) = %d, want 0", len(vessels))
	}

	events := readEvents(t, env.store)
	searchResults := filterShimEvents(events, dtu.EventKindShimResult, "gh", []string{"search", "issues"})
	if len(searchResults) != 1 {
		t.Fatalf("len(search results) = %d, want 1", len(searchResults))
	}
	if searchResults[0].Shim.ExitCode == nil || *searchResults[0].Shim.ExitCode != 0 {
		t.Fatalf("search result exit code = %#v, want 0", searchResults[0].Shim.ExitCode)
	}
}

func TestScenarioIssueGHAuthFailureFailsScan(t *testing.T) {
	env := newScenarioEnv(t, "issue-gh-auth-scan-failure.yaml")
	defer withWorkingDir(t, env.repoDir)()

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	_, err := scan.Scan(context.Background())
	if err == nil {
		t.Fatal("Scan() error = nil, want gh auth failure")
	}
	if !strings.Contains(err.Error(), "gh search issues") {
		t.Fatalf("Scan() error = %v, want gh search issues", err)
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("Scan() error = %v, want authentication required", err)
	}

	vessels, listErr := env.queue.List()
	if listErr != nil {
		t.Fatalf("List() error = %v", listErr)
	}
	if len(vessels) != 0 {
		t.Fatalf("len(vessels) = %d, want 0", len(vessels))
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 5), []string{"bug"})

	events := readEvents(t, env.store)
	searchResults := filterShimEvents(events, dtu.EventKindShimResult, "gh", []string{"search", "issues"})
	if len(searchResults) != 1 {
		t.Fatalf("len(search results) = %d, want 1", len(searchResults))
	}
	if searchResults[0].Shim.ExitCode == nil || *searchResults[0].Shim.ExitCode != 1 {
		t.Fatalf("search result exit code = %#v, want 1", searchResults[0].Shim.ExitCode)
	}
}

func TestScenarioIssueGHRateLimitOnEnqueueDoesNotBlock(t *testing.T) {
	env := newScenarioEnv(t, "issue-gh-rate-limit-on-enqueue.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "fix", prompt: "Fix issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:    "queued",
						Running:   "in-progress",
						Completed: "done",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	vessel, err := env.queue.FindByID("issue-6")
	if err != nil {
		t.Fatalf("FindByID(issue-6) error = %v", err)
	}
	if vessel.State != queue.StatePending {
		t.Fatalf("vessel.State after scan = %q, want %q", vessel.State, queue.StatePending)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 6), []string{"bug"})

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err = env.queue.FindByID("issue-6")
	if err != nil {
		t.Fatalf("FindByID(issue-6) final error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}
	// Trigger label "bug" removed by OnComplete; only terminal status remains.
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 6), []string{"done"})

	state := loadState(t, env.store)
	if got := len(state.RepositoryBySlug("owner/repo").Worktrees); got != 0 {
		t.Fatalf("len(Worktrees) = %d, want 0 after completion", got)
	}

	events := readEvents(t, env.store)
	editResults := filterShimEvents(events, dtu.EventKindShimResult, "gh", []string{"issue", "edit", "6", "--repo", "owner/repo"})
	// 5 calls: OnEnqueue (rate-limited), OnStart, OnComplete (status
	// transition), OnComplete (trigger-label removal), RemoveRunningLabel
	// (defer). The 5th call is a harmless double-removal of the running
	// label. The 4th is the new trigger-label removal added to prevent
	// duplicate enqueue on the next scan tick.
	if len(editResults) != 5 {
		t.Fatalf("len(issue edit results) = %d, want 5", len(editResults))
	}
	if !reflect.DeepEqual(editResults[0].Shim.Args, []string{"issue", "edit", "6", "--repo", "owner/repo", "--add-label", "queued"}) {
		t.Fatalf("first issue edit args = %v, want queued-label mutation", editResults[0].Shim.Args)
	}
	gotCodes := make([]int, 0, len(editResults))
	for _, event := range editResults {
		if event.Shim.ExitCode == nil {
			gotCodes = append(gotCodes, 0)
			continue
		}
		gotCodes = append(gotCodes, *event.Shim.ExitCode)
	}
	if !reflect.DeepEqual(gotCodes, []int{1, 0, 0, 0, 0}) {
		t.Fatalf("issue edit exit codes = %v, want [1 0 0 0 0]", gotCodes)
	}
}

func TestScenarioIssueHappyPath(t *testing.T) {
	env := newScenarioEnv(t, "issue-happy-path.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "plan", prompt: "Plan issue {{.Issue.Number}}"},
		{name: "implement", prompt: "Implement issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:    "queued",
						Running:   "in-progress",
						Completed: "done",
					},
				},
			},
		},
	}

	// --- Scan ---
	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 1), []string{"bug", "queued"})

	// --- Drain ---
	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainer.Reporter = &reporter.Reporter{Runner: env.cmdRunner, Repo: "owner/repo"}

	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	// --- Assert vessel completed ---
	vessel, err := env.queue.FindByID("issue-1")
	if err != nil {
		t.Fatalf("FindByID(issue-1) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}

	// --- Assert status labels ---
	// Trigger label "bug" removed by OnComplete; only terminal status remains.
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 1), []string{"done"})

	// --- Assert completion comment posted ---
	state := loadState(t, env.store)
	issue := state.RepositoryBySlug("owner/repo").IssueByNumber(1)
	if issue == nil {
		t.Fatal("IssueByNumber(1) = nil")
		return
	}
	// Expect 3 comments: phase plan complete, phase implement complete, vessel completed summary
	if len(issue.Comments) != 3 {
		t.Fatalf("len(issue.Comments) = %d, want 3", len(issue.Comments))
	}
	if got := issue.Comments[0].Body; !strings.Contains(got, "**xylem — phase `plan` completed**") {
		t.Fatalf("comment[0] = %q, want plan phase-complete comment", got)
	}
	if got := issue.Comments[1].Body; !strings.Contains(got, "**xylem — phase `implement` completed**") {
		t.Fatalf("comment[1] = %q, want implement phase-complete comment", got)
	}
	if got := issue.Comments[2].Body; !strings.Contains(got, "**xylem — all phases completed**") {
		t.Fatalf("comment[2] = %q, want vessel-completed summary comment", got)
	}
	// Summary comment should contain the phase table
	summaryComment := issue.Comments[2].Body
	if !strings.Contains(summaryComment, "| plan |") || !strings.Contains(summaryComment, "| implement |") {
		t.Fatalf("summary comment missing phase table: %q", summaryComment)
	}

	// --- Assert worktree cleaned up ---
	repo := state.RepositoryBySlug("owner/repo")
	if len(repo.Worktrees) != 0 {
		t.Fatalf("len(Worktrees) = %d, want 0 after completion", len(repo.Worktrees))
	}

	// --- Assert events ---
	events := readEvents(t, env.store)
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 2 {
		t.Fatalf("len(claude invocations) = %d, want 2", len(claudeInvocations))
	}
	if claudeInvocations[0].Shim.Phase != "plan" || claudeInvocations[1].Shim.Phase != "implement" {
		t.Fatalf("unexpected claude phases: %#v", claudeInvocations)
	}
	commentCalls := filterShimEvents(events, dtu.EventKindShimInvocation, "gh", []string{"issue", "comment", "1", "--repo", "owner/repo", "--body"})
	if len(commentCalls) != 3 {
		t.Fatalf("len(issue comment invocations) = %d, want 3", len(commentCalls))
	}
}

func TestScenarioGithubMergeProviderFailure(t *testing.T) {
	env := newScenarioEnv(t, "github-merge-provider-failure.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "post-merge", []scenarioPhase{
		{name: "postmerge", prompt: "Handle merged PR {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"merged-prs": {
			Type: "github-merge",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"merged": {Workflow: "post-merge"},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHubMerge{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", drainResult.Failed)
	}

	vessel, err := env.queue.FindByID("merge-pr-31-deadbeef")
	if err != nil {
		t.Fatalf("FindByID(merge-pr-31-deadbeef) error = %v", err)
	}
	if vessel.State != queue.StateFailed {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateFailed)
	}
	if !strings.Contains(vessel.Error, "exit status 2") {
		t.Fatalf("vessel.Error = %q, want exit status 2", vessel.Error)
	}

	events := readEvents(t, env.store)
	var postmergeResult *dtu.ShimEvent
	for _, event := range events {
		if event.Kind == dtu.EventKindShimResult && event.Shim != nil && event.Shim.Command == "claude" && event.Shim.Phase == "postmerge" {
			postmergeResult = event.Shim
			break
		}
	}
	if postmergeResult == nil || postmergeResult.ExitCode == nil || *postmergeResult.ExitCode != 2 {
		t.Fatalf("missing postmerge result with exit code 2: %#v", events)
	}
}

func TestScenarioGithubPRProviderFailure(t *testing.T) {
	env := newScenarioEnv(t, "github-pr-provider-failure.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "review-pr", []scenarioPhase{
		{name: "review", prompt: "Review PR {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"pull-requests": {
			Type: "github-pr",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"ready-prs": {
					Labels:   []string{"ready"},
					Workflow: "review-pr",
					StatusLabels: &config.StatusLabels{
						Running: "in-progress",
						Failed:  "failed",
					},
				},
			},
		},
	}

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}

	src := &source.GitHubPR{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if drainResult.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", drainResult.Failed)
	}

	// Vessel IDs are workflow-qualified (pr-<n>-<workflow>) so that two
	// github-pr sources can enqueue distinct vessels for the same PR
	// when they target different workflows.
	vessel, err := env.queue.FindByID("pr-15-review-pr")
	if err != nil {
		t.Fatalf("FindByID(pr-15-review-pr) error = %v", err)
	}
	if vessel.State != queue.StateFailed {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateFailed)
	}
	if !strings.Contains(vessel.Error, "exit status 2") {
		t.Fatalf("vessel.Error = %q, want exit status 2", vessel.Error)
	}
	assertStringSliceEqual(t, readPRLabels(t, env.store, "owner/repo", 15), []string{"failed", "ready"})

	events := readEvents(t, env.store)
	var reviewResult *dtu.ShimEvent
	for _, event := range events {
		if event.Kind == dtu.EventKindShimResult && event.Shim != nil && event.Shim.Command == "claude" && event.Shim.Phase == "review" {
			reviewResult = event.Shim
			break
		}
	}
	if reviewResult == nil || reviewResult.ExitCode == nil || *reviewResult.ExitCode != 2 {
		t.Fatalf("missing review result with exit code 2: %#v", events)
	}
}
