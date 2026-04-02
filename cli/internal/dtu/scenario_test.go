package dtu_test

import (
	"bytes"
	"context"
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

func newDrainRunner(cfg *config.Config, q *queue.Queue, cmdRunner *dtuScenarioCmdRunner, repoDir string, src source.Source) *runnerpkg.Runner {
	drainer := runnerpkg.New(cfg, q, worktree.New(repoDir, cmdRunner), cmdRunner)
	drainer.Sources = map[string]source.Source{
		src.Name(): src,
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
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)

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
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 1), []string{"bug", "in-progress", "plan-approved"})

	drainResult, err = drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("second Drain() error = %v", err)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("second DrainResult.Completed = %d, want 1", drainResult.Completed)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 1), []string{"bug", "done", "plan-approved"})

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
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)
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
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)
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
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)
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
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)
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
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)
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
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)
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
	drainer := newDrainRunner(cfg, env.queue, env.cmdRunner, env.repoDir, src)
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
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 1), []string{"bug", "done"})

	// --- Assert completion comment posted ---
	state := loadState(t, env.store)
	issue := state.RepositoryBySlug("owner/repo").IssueByNumber(1)
	if issue == nil {
		t.Fatal("IssueByNumber(1) = nil")
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
