package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

// --- Mock types ---

type mockCmdRunner struct {
	processErr   error
	outputErr    error
	outputData   []byte
	phaseOutputs map[string][]byte // keyed by substring in prompt
	phaseErr     error
	started      int32
	gateOutput   []byte
	gateErr      error
	// Per-call gate results; when non-nil, overrides gateOutput/gateErr by call index
	gateCallResults []gateCallResult
	gateCallCount   int32
	// Track calls for assertion
	phaseCalls []phaseCall
	outputArgs [][]string
}

type gateCallResult struct {
	output []byte
	err    error
}

type phaseCall struct {
	dir    string
	prompt string
	name   string
	args   []string
}

func (m *mockCmdRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	m.outputArgs = append(m.outputArgs, append([]string{name}, args...))
	// Detect gate commands by matching the exact shape produced by gate.RunCommandGate:
	// RunOutput("sh", "-c", "cd <dir> && <cmd>")
	isGate := false
	if name == "sh" && len(args) >= 2 && args[0] == "-c" && strings.Contains(args[1], "cd ") {
		isGate = true
	}
	if isGate && m.gateCallResults != nil {
		idx := int(atomic.AddInt32(&m.gateCallCount, 1) - 1)
		if idx < len(m.gateCallResults) {
			return m.gateCallResults[idx].output, m.gateCallResults[idx].err
		}
		last := m.gateCallResults[len(m.gateCallResults)-1]
		return last.output, last.err
	}
	if m.gateOutput != nil && isGate {
		return m.gateOutput, m.gateErr
	}
	if m.outputData != nil {
		return m.outputData, m.outputErr
	}
	return []byte{}, m.outputErr
}

func (m *mockCmdRunner) RunProcess(_ context.Context, _ string, _ string, _ ...string) error {
	atomic.AddInt32(&m.started, 1)
	return m.processErr
}

func (m *mockCmdRunner) RunPhase(_ context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	prompt, _ := io.ReadAll(stdin)
	m.phaseCalls = append(m.phaseCalls, phaseCall{
		dir:    dir,
		prompt: string(prompt),
		name:   name,
		args:   args,
	})
	atomic.AddInt32(&m.started, 1)

	// Return canned output based on prompt content
	for key, output := range m.phaseOutputs {
		if bytes.Contains(prompt, []byte(key)) {
			return output, m.phaseErr
		}
	}
	return []byte("mock output"), m.phaseErr
}

// mockExitError implements the exitCoder interface that gate.RunCommandGate checks.
type mockExitError struct {
	code int
}

func (e *mockExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *mockExitError) ExitCode() int { return e.code }

type countingCmdRunner struct {
	concurrent int32
	maxSeen    int32
	delay      time.Duration
}

func (c *countingCmdRunner) RunOutput(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return []byte{}, nil
}

func (c *countingCmdRunner) RunProcess(_ context.Context, _ string, _ string, _ ...string) error {
	cur := atomic.AddInt32(&c.concurrent, 1)
	for {
		old := atomic.LoadInt32(&c.maxSeen)
		if cur <= old {
			break
		}
		if atomic.CompareAndSwapInt32(&c.maxSeen, old, cur) {
			break
		}
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	atomic.AddInt32(&c.concurrent, -1)
	return nil
}

func (c *countingCmdRunner) RunPhase(_ context.Context, _ string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	io.ReadAll(stdin)
	cur := atomic.AddInt32(&c.concurrent, 1)
	for {
		old := atomic.LoadInt32(&c.maxSeen)
		if cur <= old {
			break
		}
		if atomic.CompareAndSwapInt32(&c.maxSeen, old, cur) {
			break
		}
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	atomic.AddInt32(&c.concurrent, -1)
	return []byte("mock output"), nil
}

type mockWorktree struct {
	createErr    error
	path         string
	removeErr    error
	removeCalled bool
	removePath   string
}

func (m *mockWorktree) Create(_ context.Context, branchName string) (string, error) {
	if m.createErr != nil {
		return "", m.createErr
	}
	if m.path != "" {
		return m.path, nil
	}
	return ".claude/worktrees/" + branchName, nil
}

func (m *mockWorktree) Remove(_ context.Context, worktreePath string) error {
	m.removeCalled = true
	m.removePath = worktreePath
	return m.removeErr
}

type trackingWorktree struct {
	lastBranch string
}

func (tw *trackingWorktree) Create(_ context.Context, branchName string) (string, error) {
	tw.lastBranch = branchName
	return ".claude/worktrees/" + branchName, nil
}

func (tw *trackingWorktree) Remove(_ context.Context, _ string) error {
	return nil
}

// --- Helpers ---

func makeTestConfig(dir string, concurrency int) *config.Config {
	return &config.Config{
		Concurrency: concurrency,
		MaxTurns:    50,
		Timeout:     "30s",
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

func makeVessel(num int, workflow string) queue.Vessel {
	return queue.Vessel{
		ID:        fmt.Sprintf("issue-%d", num),
		Source:    "github-issue",
		Ref:       fmt.Sprintf("https://github.com/owner/repo/issues/%d", num),
		Workflow:  workflow,
		Meta:      map[string]string{"issue_num": strconv.Itoa(num)},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	}
}

func makePromptVessel(num int, prompt string) queue.Vessel {
	return queue.Vessel{
		ID:        fmt.Sprintf("prompt-%d", num),
		Source:    "manual",
		Prompt:    prompt,
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	}
}

func makeGitHubSource() *source.GitHub {
	return &source.GitHub{
		Repo: "owner/repo",
	}
}

func TestBuildCommand(t *testing.T) {
	cfg := &config.Config{
		MaxTurns: 50,
		Claude: config.ClaudeConfig{
			Command: "claude",
		},
	}
	vessel := &queue.Vessel{
		Source:   "github-issue",
		Workflow: "fix-bug",
		Ref:      "https://github.com/owner/repo/issues/42",
	}
	cmd, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "claude" {
		t.Errorf("expected cmd 'claude', got %q", cmd)
	}
	full := cmd + " " + strings.Join(args, " ")
	if !strings.Contains(full, "fix-bug") {
		t.Errorf("expected workflow in command, got: %s", full)
	}
	if !strings.Contains(full, "42") {
		t.Errorf("expected issue URL in command, got: %s", full)
	}
	if !strings.Contains(full, "--max-turns") {
		t.Errorf("expected --max-turns in command, got: %s", full)
	}
}

// writeWorkflowFile creates a workflow YAML and its prompt files in the given dir.
func writeWorkflowFile(t *testing.T, dir, name string, phases []testPhase) {
	t.Helper()
	workflowDir := filepath.Join(dir, ".xylem", "workflows")
	os.MkdirAll(workflowDir, 0o755)

	var phaseYAML strings.Builder
	for _, p := range phases {
		phaseYAML.WriteString(fmt.Sprintf("  - name: %s\n", p.name))

		if p.phaseType == "command" {
			phaseYAML.WriteString("    type: command\n")
			phaseYAML.WriteString(fmt.Sprintf("    run: %q\n", p.run))
		} else {
			promptPath := filepath.Join(dir, ".xylem", "prompts", name, p.name+".md")
			os.MkdirAll(filepath.Dir(promptPath), 0o755)
			os.WriteFile(promptPath, []byte(p.promptContent), 0o644)

			phaseYAML.WriteString(fmt.Sprintf("    prompt_file: %s\n", promptPath))
			phaseYAML.WriteString(fmt.Sprintf("    max_turns: %d\n", p.maxTurns))
		}
		if p.noopMatch != "" {
			phaseYAML.WriteString("    noop:\n")
			phaseYAML.WriteString(fmt.Sprintf("      match: %q\n", p.noopMatch))
		}
		if p.gate != "" {
			phaseYAML.WriteString(fmt.Sprintf("    gate:\n%s\n", p.gate))
		}
		if p.allowedTools != "" {
			phaseYAML.WriteString(fmt.Sprintf("    allowed_tools: %q\n", p.allowedTools))
		}
	}

	workflowContent := fmt.Sprintf("name: %s\nphases:\n%s", name, phaseYAML.String())
	os.WriteFile(filepath.Join(workflowDir, name+".yaml"), []byte(workflowContent), 0o644)
}

func TestBuildCommandDirectPrompt(t *testing.T) {
	cfg := &config.Config{
		MaxTurns: 50,
		Claude: config.ClaudeConfig{
			Command: "claude",
		},
	}
	vessel := &queue.Vessel{
		Source: "manual",
		Prompt: "Fix the null pointer in handler.go",
	}
	cmd, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "claude" {
		t.Errorf("expected cmd 'claude', got %q", cmd)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(args), args)
	}
	if args[0] != "-p" {
		t.Errorf("expected -p flag, got %q", args[0])
	}
	if args[1] != "Fix the null pointer in handler.go" {
		t.Errorf("expected prompt text, got %q", args[1])
	}
}

func TestBuildCommandDirectPromptWithRef(t *testing.T) {
	cfg := &config.Config{
		MaxTurns: 50,
		Claude: config.ClaudeConfig{
			Command: "claude",
		},
	}
	vessel := &queue.Vessel{
		Source: "manual",
		Prompt: "Fix the null pointer in handler.go",
		Ref:    "https://github.com/owner/repo/issues/99",
	}
	cmd, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "claude" {
		t.Errorf("expected cmd 'claude', got %q", cmd)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(args), args)
	}
	if args[0] != "-p" {
		t.Errorf("expected -p flag, got %q", args[0])
	}
	// The prompt should contain the ref prepended
	if !strings.Contains(args[1], "Ref: https://github.com/owner/repo/issues/99") {
		t.Errorf("expected ref URL in prompt, got %q", args[1])
	}
	if !strings.Contains(args[1], "Fix the null pointer in handler.go") {
		t.Errorf("expected original prompt text in prompt, got %q", args[1])
	}
	// Ref should come before the prompt text
	refIdx := strings.Index(args[1], "Ref:")
	promptIdx := strings.Index(args[1], "Fix the null pointer")
	if refIdx >= promptIdx {
		t.Errorf("expected ref to come before prompt, ref at %d, prompt at %d", refIdx, promptIdx)
	}
}

func TestBuildCommandWorkflowBased(t *testing.T) {
	cfg := &config.Config{
		MaxTurns: 50,
		Claude: config.ClaudeConfig{
			Command: "claude",
		},
	}
	vessel := &queue.Vessel{
		Source:   "github-issue",
		Workflow: "fix-bug",
		Ref:      "https://github.com/owner/repo/issues/42",
	}
	cmd, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "claude" {
		t.Errorf("expected cmd 'claude', got %q", cmd)
	}
	// Should produce: -p "/fix-bug https://..." --max-turns 50
	if args[0] != "-p" {
		t.Errorf("expected -p flag, got %q", args[0])
	}
	if !strings.Contains(args[1], "/fix-bug") {
		t.Errorf("expected workflow in prompt, got %q", args[1])
	}
	if !strings.Contains(args[1], "issues/42") {
		t.Errorf("expected ref in prompt, got %q", args[1])
	}
}

type testPhase struct {
	name          string
	promptContent string
	maxTurns      int
	noopMatch     string
	gate          string
	allowedTools  string
	phaseType     string // "command" or "" for prompt (default)
	run           string // shell command for type=command
}

// --- Tests ---

func TestDrainSingleVessel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze the issue", maxTurns: 5},
	})
	cfg.StateDir = filepath.Join(dir, ".xylem")

	// Override workflow path: change working directory context
	// We need the workflow to be loadable from .xylem/workflows/fix-bug.yaml
	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}
	if result.Failed != 0 {
		t.Errorf("expected 0 failed, got %d", result.Failed)
	}
	if atomic.LoadInt32(&cmdRunner.started) != 1 {
		t.Errorf("expected claude started once, got %d", cmdRunner.started)
	}
	vessels, _ := q.List()
	if vessels[0].State != queue.StateCompleted {
		t.Errorf("expected vessel completed, got %s", vessels[0].State)
	}
}

func TestDrainMultiPhaseWorkflow(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze: {{.Issue.Title}}", maxTurns: 5},
		{name: "implement", promptContent: "Implement based on: {{.PreviousOutputs.analyze}}", maxTurns: 10},
		{name: "pr", promptContent: "Create PR", maxTurns: 3},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze":   []byte("analysis result"),
			"Implement": []byte("implementation done"),
			"Create PR": []byte("PR created"),
		},
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}

	// All 3 phases should have been invoked
	if len(cmdRunner.phaseCalls) != 3 {
		t.Fatalf("expected 3 phase calls, got %d", len(cmdRunner.phaseCalls))
	}

	// Verify output files exist
	for _, pName := range []string{"analyze", "implement", "pr"} {
		outputPath := filepath.Join(dir, ".xylem", "phases", "issue-1", pName+".output")
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			t.Errorf("expected output file %s to exist", outputPath)
		}
	}
}

func TestDrainPhaseNoOpCompletesWorkflowEarly(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze: {{.Issue.Title}}",
			maxTurns:      5,
			noopMatch:     "XYLEM_NOOP",
			gate:          "      type: command\n      run: \"make test\"",
		},
		{name: "implement", promptContent: "Implement", maxTurns: 10},
		{name: "pr", promptContent: "Create PR", maxTurns: 3},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("Already fixed in main.\n\nXYLEM_NOOP\n"),
		},
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("expected 1 completed, got %d", result.Completed)
	}
	if len(cmdRunner.phaseCalls) != 1 {
		t.Fatalf("expected only the noop phase to run, got %d phase calls", len(cmdRunner.phaseCalls))
	}
	for _, args := range cmdRunner.outputArgs {
		if strings.Contains(strings.Join(args, " "), "make test") {
			t.Fatalf("expected noop to skip gate execution, got output args %v", cmdRunner.outputArgs)
		}
	}
	if !wt.removeCalled {
		t.Fatal("expected worktree cleanup after noop completion")
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateCompleted {
		t.Fatalf("expected vessel completed, got %s", vessels[0].State)
	}
	if vessels[0].CurrentPhase != 1 {
		t.Fatalf("expected CurrentPhase 1, got %d", vessels[0].CurrentPhase)
	}
	if len(vessels[0].PhaseOutputs) != 1 {
		t.Fatalf("expected only one phase output, got %v", vessels[0].PhaseOutputs)
	}
	if _, ok := vessels[0].PhaseOutputs["analyze"]; !ok {
		t.Fatalf("expected analyze output path to be persisted, got %v", vessels[0].PhaseOutputs)
	}

	analyzeOutputPath := filepath.Join(dir, ".xylem", "phases", "issue-1", "analyze.output")
	if _, err := os.Stat(analyzeOutputPath); err != nil {
		t.Fatalf("expected analyze output file to exist: %v", err)
	}
	implementOutputPath := filepath.Join(dir, ".xylem", "phases", "issue-1", "implement.output")
	if _, err := os.Stat(implementOutputPath); !os.IsNotExist(err) {
		t.Fatalf("expected implement output file not to exist, got err=%v", err)
	}
}

func TestDrainPhaseFailsStopsSubsequent(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
		{name: "implement", promptContent: "Implement", maxTurns: 10},
		{name: "pr", promptContent: "PR", maxTurns: 3},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis done"),
		},
		phaseErr: errors.New("exit status 1"),
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}

	// Only 1 phase should have been called (first one fails)
	if len(cmdRunner.phaseCalls) != 1 {
		t.Errorf("expected 1 phase call (stopped at failure), got %d", len(cmdRunner.phaseCalls))
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateFailed {
		t.Errorf("expected vessel failed, got %s", vessels[0].State)
	}
}

func TestDrainPromptOnlyVessel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "Fix the null pointer in handler.go"))

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}

	// Should be exactly 1 RunPhase call
	if len(cmdRunner.phaseCalls) != 1 {
		t.Fatalf("expected 1 phase call for prompt-only, got %d", len(cmdRunner.phaseCalls))
	}

	// Verify the prompt was passed via stdin
	call := cmdRunner.phaseCalls[0]
	if !strings.Contains(call.prompt, "Fix the null pointer in handler.go") {
		t.Errorf("expected prompt in stdin, got: %s", call.prompt)
	}

	// Verify -p flag is present
	hasP := false
	for _, a := range call.args {
		if a == "-p" {
			hasP = true
			break
		}
	}
	if !hasP {
		t.Errorf("expected -p flag in args, got: %v", call.args)
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateCompleted {
		t.Errorf("expected vessel completed, got %s", vessels[0].State)
	}
}

func TestDrainPromptOnlyWithRef(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	v := makePromptVessel(1, "Fix the null pointer")
	v.Ref = "https://github.com/owner/repo/issues/99"
	_, _ = q.Enqueue(v)

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}

	call := cmdRunner.phaseCalls[0]
	if !strings.Contains(call.prompt, "Ref: https://github.com/owner/repo/issues/99") {
		t.Errorf("expected ref in prompt, got: %s", call.prompt)
	}
	if !strings.Contains(call.prompt, "Fix the null pointer") {
		t.Errorf("expected original prompt in stdin, got: %s", call.prompt)
	}
}

func TestDrainCommandGatePasses(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "implement", promptContent: "Implement fix", maxTurns: 10,
			gate: "      type: command\n      run: \"make test\"",
		},
		{name: "pr", promptContent: "Create PR", maxTurns: 3},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("all tests passed"),
		gateErr:    nil, // gate passes
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}

	// Both phases should have run
	if len(cmdRunner.phaseCalls) != 2 {
		t.Errorf("expected 2 phase calls (gate passed), got %d", len(cmdRunner.phaseCalls))
	}
}

func TestDrainCommandGateFailsNoRetries(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "implement", promptContent: "Implement fix", maxTurns: 10,
			gate: "      type: command\n      run: \"make test\"\n      retries: 0",
		},
		{name: "pr", promptContent: "Create PR", maxTurns: 3},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("FAIL: TestFoo"),
		gateErr:    errors.New("exit status 1"), // gate fails
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}

	// Only 1 phase call (gate failed, no retry, phase 2 not invoked)
	if len(cmdRunner.phaseCalls) != 1 {
		t.Errorf("expected 1 phase call, got %d", len(cmdRunner.phaseCalls))
	}
}

func TestDrainGateRetriesNotBleedBetweenPhases(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	// Phase A has retries:2 but its gate passes on the first attempt.
	// Phase B has retries:0 and its gate always fails.
	// Without the fix, Phase A's leftover GateRetries bleeds into Phase B,
	// giving it phantom retries and causing Phase B to run 3 times instead of once.
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "implement", promptContent: "Implement fix", maxTurns: 10,
			gate: "      type: command\n      run: \"make test\"\n      retries: 2\n      retry_delay: \"1ms\"",
		},
		{
			name: "pr", promptContent: "Create PR", maxTurns: 3,
			gate: "      type: command\n      run: \"make lint\"\n      retries: 0",
		},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		// First gate call (Phase A): passes. Second gate call (Phase B): fails with a
		// non-zero exit code. Use mockExitError so RunCommandGate returns (output, false, nil)
		// and the retry-check path (vessel.GateRetries <= 0) is exercised.
		// errors.New("exit status 1") would NOT satisfy the exitCoder interface, causing
		// a system error that bypasses the retry check entirely and masking the bleed bug.
		gateCallResults: []gateCallResult{
			{output: []byte("ok"), err: nil},
			{output: []byte("FAIL: lint"), err: &mockExitError{code: 1}},
		},
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}
	// Phase A ran once, Phase B ran once — no phantom retries from bleed.
	if len(cmdRunner.phaseCalls) != 2 {
		t.Errorf("expected 2 phase calls (one per phase, no phantom retries), got %d", len(cmdRunner.phaseCalls))
	}
}

func TestDrainCommandGateFailsWithRetries(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "implement", promptContent: "Implement fix\n{{.GateResult}}", maxTurns: 10,
			gate: "      type: command\n      run: \"make test\"\n      retries: 2\n      retry_delay: \"1ms\"",
		},
		{name: "pr", promptContent: "Create PR", maxTurns: 3},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("FAIL: TestFoo"),
		gateErr:    &mockExitError{code: 1}, // gate always fails (non-zero exit)
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}

	// Phase should be invoked 3 times total: initial + 2 retries
	if len(cmdRunner.phaseCalls) != 3 {
		t.Errorf("expected 3 phase calls (1 + 2 retries), got %d", len(cmdRunner.phaseCalls))
	}

	// The 2nd and 3rd calls should include gate result context in rendered prompt
	for i := 1; i < len(cmdRunner.phaseCalls); i++ {
		if !strings.Contains(cmdRunner.phaseCalls[i].prompt, "gate check failed") {
			t.Errorf("retry %d prompt should contain gate failure context, got: %s", i, cmdRunner.phaseCalls[i].prompt)
		}
	}
}

func TestDrainLabelGateTransitionsToWaiting(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "plan", promptContent: "Create plan", maxTurns: 5,
			gate: "      type: label\n      wait_for: \"plan-approved\"\n      timeout: \"24h\"",
		},
		{name: "implement", promptContent: "Implement", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Waiting != 1 {
		t.Errorf("expected 1 waiting, got %d", result.Waiting)
	}

	// Only 1 phase should have been invoked (plan)
	if len(cmdRunner.phaseCalls) != 1 {
		t.Errorf("expected 1 phase call, got %d", len(cmdRunner.phaseCalls))
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateWaiting {
		t.Errorf("expected vessel waiting, got %s", vessels[0].State)
	}
	if vessels[0].WaitingFor != "plan-approved" {
		t.Errorf("expected WaitingFor=plan-approved, got %s", vessels[0].WaitingFor)
	}
	if vessels[0].WaitingSince == nil {
		t.Error("expected WaitingSince to be set")
	}
}

func TestDrainVesselFails(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{phaseErr: errors.New("exit status 1")}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}
	vessels, _ := q.List()
	if vessels[0].State != queue.StateFailed {
		t.Errorf("expected vessel failed, got %s", vessels[0].State)
	}
}

func TestDrainWorktreeCreateFails(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{createErr: errors.New("git fetch failed")}
	r := New(cfg, q, wt, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed (worktree error), got %d", result.Failed)
	}
	if atomic.LoadInt32(&cmdRunner.started) != 0 {
		t.Error("claude should NOT be started when worktree fails")
	}
}

func TestDrainConcurrencyLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	// Create workflow and enqueue vessels
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	for i := 1; i <= 4; i++ {
		_, _ = q.Enqueue(makeVessel(i, "fix-bug"))
	}

	counter := &countingCmdRunner{delay: 50 * time.Millisecond}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, counter)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	_, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	max := atomic.LoadInt32(&counter.maxSeen)
	if max > 2 {
		t.Errorf("concurrency exceeded limit: max concurrent = %d, limit = 2", max)
	}
}

func TestDrainContextCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	for i := 1; i <= 5; i++ {
		_, _ = q.Enqueue(makeVessel(i, "fix-bug"))
	}

	ctx, cancel := context.WithCancel(context.Background())

	counter := &countingCmdRunner{delay: 20 * time.Millisecond}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, counter)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	result, err := r.Drain(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	total := result.Completed + result.Failed + result.Skipped
	if total >= 5 {
		t.Errorf("expected cancellation to stop some vessels, but all 5 ran")
	}
}

func TestDrainTimeout(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.Timeout = "50ms"
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	cmdRunner := &mockCmdRunner{
		processErr: context.DeadlineExceeded,
	}

	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected timed-out vessel to be marked failed, got completed=%d failed=%d", result.Completed, result.Failed)
	}
}

func TestBuildCommandEmptyCommand(t *testing.T) {
	cfg := &config.Config{
		MaxTurns: 50,
		Claude: config.ClaudeConfig{
			Command: "",
		},
	}
	vessel := &queue.Vessel{Workflow: "fix-bug", Ref: "https://example.com"}
	cmd, _, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty command is returned as-is; the OS will fail to exec it
	if cmd != "" {
		t.Errorf("expected empty command, got %q", cmd)
	}
}

func TestDrainEmptyQueue(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 0 {
		t.Errorf("expected 0 completed, got %d", result.Completed)
	}
	if result.Failed != 0 {
		t.Errorf("expected 0 failed, got %d", result.Failed)
	}
	if atomic.LoadInt32(&cmdRunner.started) != 0 {
		t.Error("no processes should have started on empty queue")
	}
}

func TestDrainHarnessAppended(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	})

	// Write harness file
	harnessDir := filepath.Join(dir, ".xylem")
	os.MkdirAll(harnessDir, 0o755)
	os.WriteFile(filepath.Join(harnessDir, "HARNESS.md"), []byte("Golden rules for this repo"), 0o644)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	_, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cmdRunner.phaseCalls) != 1 {
		t.Fatalf("expected 1 phase call, got %d", len(cmdRunner.phaseCalls))
	}

	// Check that --append-system-prompt is in the args
	call := cmdRunner.phaseCalls[0]
	found := false
	for i, a := range call.args {
		if a == "--append-system-prompt" && i+1 < len(call.args) {
			if strings.Contains(call.args[i+1], "Golden rules") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected --append-system-prompt with harness content, got args: %v", call.args)
	}
}

func TestDrainPreviousOutputsAvailable(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze the issue", maxTurns: 5},
		{name: "implement", promptContent: "Previous analysis: {{.PreviousOutputs.analyze}}", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("found root cause in auth.go"),
		},
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	_, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cmdRunner.phaseCalls) != 2 {
		t.Fatalf("expected 2 phase calls, got %d", len(cmdRunner.phaseCalls))
	}

	// Second phase prompt should contain the first phase's output
	secondPrompt := cmdRunner.phaseCalls[1].prompt
	if !strings.Contains(secondPrompt, "found root cause in auth.go") {
		t.Errorf("expected previous output in second phase prompt, got: %s", secondPrompt)
	}
}

func TestBranchPrefixSelection(t *testing.T) {
	tests := []struct {
		workflow   string
		wantPrefix string
	}{
		{"fix-bug", "fix"},
		{"Fix-Bug", "fix"},
		{"hotfix", "fix"},
		{"implement-feature", "feat"},
		{"add-docs", "feat"},
		{"refactor", "feat"},
	}

	for _, tc := range tests {
		t.Run(tc.workflow, func(t *testing.T) {
			dir := t.TempDir()
			cfg := makeTestConfig(dir, 1)
			cfg.StateDir = filepath.Join(dir, ".xylem")
			q := queue.New(filepath.Join(dir, "queue.jsonl"))
			_, _ = q.Enqueue(makeVessel(1, tc.workflow))

			writeWorkflowFile(t, dir, tc.workflow, []testPhase{
				{name: "work", promptContent: "Do work", maxTurns: 5},
			})

			oldWd, _ := os.Getwd()
			os.Chdir(dir)
			defer os.Chdir(oldWd)

			tracker := &trackingWorktree{}
			cmdRunner := &mockCmdRunner{}
			r := New(cfg, q, tracker, cmdRunner)
			r.Sources = map[string]source.Source{
				"github-issue": makeGitHubSource(),
			}

			_, err := r.Drain(context.Background())
			if err != nil {
				t.Fatalf("drain: %v", err)
			}

			createdBranch := tracker.lastBranch
			wantPrefix := tc.wantPrefix + "/issue-1-"
			if !strings.HasPrefix(createdBranch, wantPrefix) {
				t.Errorf("for workflow %q, expected branch prefix %q, got %q", tc.workflow, wantPrefix, createdBranch)
			}
		})
	}
}

func TestBuildCommandWithFlags(t *testing.T) {
	cfg := &config.Config{
		MaxTurns: 50,
		Claude: config.ClaudeConfig{
			Command: "claude",
			Flags:   "--bare --dangerously-skip-permissions",
		},
	}
	vessel := &queue.Vessel{
		Source:   "github-issue",
		Workflow: "fix-bug",
		Ref:      "https://github.com/owner/repo/issues/42",
	}
	_, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have: -p, prompt, --max-turns, 50, --bare, --dangerously-skip-permissions
	if len(args) != 6 {
		t.Fatalf("expected 6 args, got %d: %v", len(args), args)
	}
	if args[4] != "--bare" {
		t.Errorf("expected --bare flag, got %q", args[4])
	}
	if args[5] != "--dangerously-skip-permissions" {
		t.Errorf("expected --dangerously-skip-permissions flag, got %q", args[5])
	}
}

func TestBuildCommandNoFlags(t *testing.T) {
	cfg := &config.Config{
		MaxTurns: 50,
		Claude: config.ClaudeConfig{
			Command: "claude",
		},
	}
	vessel := &queue.Vessel{
		Source: "manual",
		Prompt: "Fix the null pointer in handler.go",
	}
	_, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have exactly: -p, prompt, --max-turns, 50
	if len(args) != 4 {
		t.Fatalf("expected 4 args (no extra flags), got %d: %v", len(args), args)
	}
}

func TestBuildCommandDirectPromptWithFlags(t *testing.T) {
	cfg := &config.Config{
		MaxTurns: 50,
		Claude: config.ClaudeConfig{
			Command: "claude",
			Flags:   "--bare",
		},
	}
	vessel := &queue.Vessel{
		Source: "manual",
		Prompt: "Fix the null pointer in handler.go",
	}
	cmd, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "claude" {
		t.Errorf("expected cmd 'claude', got %q", cmd)
	}
	// Should have: -p, prompt, --max-turns, 50, --bare
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args), args)
	}
	if args[4] != "--bare" {
		t.Errorf("expected --bare flag, got %q", args[4])
	}
}

func TestBuildPhaseArgs(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5}
		args := buildPhaseArgs(cfg, nil, p, "")

		if args[0] != "-p" {
			t.Errorf("expected -p, got %s", args[0])
		}
		if args[1] != "--max-turns" || args[2] != "5" {
			t.Errorf("expected --max-turns 5, got %v", args[1:3])
		}
	})

	t.Run("with flags", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude", Flags: "--bare --dangerously-skip-permissions"},
		}
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5}
		args := buildPhaseArgs(cfg, nil, p, "")

		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--bare") {
			t.Errorf("expected --bare in args, got: %v", args)
		}
		if !strings.Contains(joined, "--dangerously-skip-permissions") {
			t.Errorf("expected --dangerously-skip-permissions in args, got: %v", args)
		}
	})

	t.Run("with allowed tools", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude", AllowedTools: []string{"WebFetch"}},
		}
		allowedTools := "Read,Edit"
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5, AllowedTools: &allowedTools}
		args := buildPhaseArgs(cfg, nil, p, "")

		// Should have both phase-level and config-level tools
		toolCount := 0
		for _, a := range args {
			if a == "--allowedTools" {
				toolCount++
			}
		}
		if toolCount != 2 {
			t.Errorf("expected 2 --allowedTools flags, got %d in %v", toolCount, args)
		}
	})

	t.Run("with harness", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5}
		args := buildPhaseArgs(cfg, nil, p, "harness content")

		found := false
		for i, a := range args {
			if a == "--append-system-prompt" && i+1 < len(args) && args[i+1] == "harness content" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected --append-system-prompt with harness, got: %v", args)
		}
	})
}

func strPtr(s string) *string {
	return &s
}

func TestBuildPhaseArgsModelResolution(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		wf        *workflow.Workflow
		phase     *workflow.Phase
		wantModel string // expected --model value; empty means no --model flag
	}{
		{
			name: "phase model takes priority",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude", DefaultModel: "config-model"},
			},
			wf:        &workflow.Workflow{Model: strPtr("workflow-model")},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5, Model: strPtr("phase-model")},
			wantModel: "phase-model",
		},
		{
			name: "workflow model when phase has no model",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude", DefaultModel: "config-model"},
			},
			wf:        &workflow.Workflow{Model: strPtr("workflow-model")},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5},
			wantModel: "workflow-model",
		},
		{
			name: "config default model when neither phase nor workflow set",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude", DefaultModel: "config-model"},
			},
			wf:        &workflow.Workflow{},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5},
			wantModel: "config-model",
		},
		{
			name: "no model when nothing set",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude"},
			},
			wf:        &workflow.Workflow{},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5},
			wantModel: "",
		},
		{
			name: "nil workflow still falls back to config",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude", DefaultModel: "config-model"},
			},
			wf:        nil,
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5},
			wantModel: "config-model",
		},
		{
			name: "flags --model stripped when hierarchy resolves model",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude", Flags: "--bare --model old-model --dangerously-skip-permissions"},
			},
			wf:        &workflow.Workflow{Model: strPtr("workflow-model")},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5},
			wantModel: "workflow-model",
		},
		{
			name: "flags --model preserved when hierarchy does not resolve",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude", Flags: "--bare --model flags-model"},
			},
			wf:        &workflow.Workflow{},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5},
			wantModel: "flags-model",
		},
		{
			name: "empty string phase model falls through to workflow",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude"},
			},
			wf:        &workflow.Workflow{Model: strPtr("workflow-model")},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5, Model: strPtr("")},
			wantModel: "workflow-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildPhaseArgs(tt.cfg, tt.wf, tt.phase, "")

			// Find --model in args
			foundModel := ""
			for i, a := range args {
				if a == "--model" && i+1 < len(args) {
					foundModel = args[i+1]
					break
				}
			}

			if foundModel != tt.wantModel {
				t.Errorf("model = %q, want %q (args: %v)", foundModel, tt.wantModel, args)
			}

			// When hierarchy resolves a model, --model from flags should be stripped
			if tt.wantModel != "" && strings.Contains(tt.cfg.Claude.Flags, "--model") {
				// Count --model occurrences; should be exactly 1
				count := 0
				for _, a := range args {
					if a == "--model" {
						count++
					}
				}
				if count != 1 {
					t.Errorf("expected exactly 1 --model flag, got %d (args: %v)", count, args)
				}
			}
		})
	}
}

func TestDrainTimeoutV2Phase(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.Timeout = "50ms"
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseErr: context.DeadlineExceeded,
	}

	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected timed-out vessel to be marked failed, got completed=%d failed=%d", result.Completed, result.Failed)
	}
}

func TestResolveProvider(t *testing.T) {
	claude := "claude"
	copilot := "copilot"

	tests := []struct {
		name     string
		cfgLLM   string
		wfLLM    *string
		phaseLLM *string
		want     string
	}{
		{"all nil defaults to claude", "", nil, nil, "claude"},
		{"config claude", "claude", nil, nil, "claude"},
		{"config copilot", "copilot", nil, nil, "copilot"},
		{"workflow overrides config", "claude", &copilot, nil, "copilot"},
		{"phase overrides workflow", "claude", &claude, &copilot, "copilot"},
		{"phase overrides config", "claude", nil, &copilot, "copilot"},
		{"workflow override wins when config empty", "", &copilot, nil, "copilot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{LLM: tt.cfgLLM}
			var wf *workflow.Workflow
			if tt.wfLLM != nil {
				wf = &workflow.Workflow{LLM: tt.wfLLM}
			}
			var p *workflow.Phase
			if tt.phaseLLM != nil {
				p = &workflow.Phase{LLM: tt.phaseLLM}
			}
			got := resolveProvider(cfg, wf, p)
			if got != tt.want {
				t.Errorf("resolveProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveModel(t *testing.T) {
	cfg := &config.Config{
		Claude:  config.ClaudeConfig{DefaultModel: "claude-default"},
		Copilot: config.CopilotConfig{DefaultModel: "copilot-default"},
	}

	phaseModel := "phase-model"
	wfModel := "wf-model"

	tests := []struct {
		name       string
		wfModel    *string
		phaseModel *string
		provider   string
		want       string
	}{
		{"phase model wins", &wfModel, &phaseModel, "claude", "phase-model"},
		{"workflow model wins over default", &wfModel, nil, "claude", "wf-model"},
		{"claude default when no override", nil, nil, "claude", "claude-default"},
		{"copilot default when provider copilot", nil, nil, "copilot", "copilot-default"},
		{"workflow model wins for copilot", &wfModel, nil, "copilot", "wf-model"},
		{"phase model wins for copilot", &wfModel, &phaseModel, "copilot", "phase-model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wf *workflow.Workflow
			if tt.wfModel != nil {
				wf = &workflow.Workflow{Model: tt.wfModel}
			}
			var p *workflow.Phase
			if tt.phaseModel != nil {
				p = &workflow.Phase{Model: tt.phaseModel}
			}
			got := resolveModel(cfg, wf, p, tt.provider)
			if got != tt.want {
				t.Errorf("resolveModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildCopilotPhaseArgs(t *testing.T) {
	maxTurns := 10

	tests := []struct {
		name           string
		cfg            *config.Config
		wf             *workflow.Workflow
		phase          *workflow.Phase
		harness        string
		wantContains   []string
		wantNotContain []string
		wantPairs      [][2]string // [flag, value] pairs that must be adjacent
	}{
		{
			name:         "basic copilot args",
			cfg:          &config.Config{Copilot: config.CopilotConfig{Command: "copilot"}},
			phase:        &workflow.Phase{MaxTurns: maxTurns},
			wantContains: []string{"--headless"},
			wantPairs:    [][2]string{{"--max-turns", "10"}},
		},
		{
			name: "copilot with model from config default",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{Command: "copilot", DefaultModel: "gpt-4o"},
			},
			phase:     &workflow.Phase{MaxTurns: maxTurns},
			wantPairs: [][2]string{{"--model", "gpt-4o"}},
		},
		{
			name: "copilot with model from phase overrides default",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{Command: "copilot", DefaultModel: "gpt-4o"},
			},
			phase:          &workflow.Phase{MaxTurns: maxTurns, Model: strPtrRunner("phase-model")},
			wantPairs:      [][2]string{{"--model", "phase-model"}},
			wantNotContain: []string{"gpt-4o"},
		},
		{
			name: "copilot with extra flags",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{Command: "copilot", Flags: "--verbose"},
			},
			phase:        &workflow.Phase{MaxTurns: maxTurns},
			wantContains: []string{"--verbose"},
		},
		{
			name:      "copilot with allowed_tools",
			cfg:       &config.Config{Copilot: config.CopilotConfig{Command: "copilot"}},
			phase:     &workflow.Phase{MaxTurns: maxTurns, AllowedTools: strPtrRunner("Bash,Read")},
			wantPairs: [][2]string{{"--allowed-tools", "Bash,Read"}},
		},
		{
			name:      "copilot with harness",
			cfg:       &config.Config{Copilot: config.CopilotConfig{Command: "copilot"}},
			phase:     &workflow.Phase{MaxTurns: maxTurns},
			harness:   "harness instructions",
			wantPairs: [][2]string{{"--system-prompt", "harness instructions"}},
		},
		{
			name: "copilot strips --model from flags when model resolved",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{
					Command:      "copilot",
					Flags:        "--headless --model old-model",
					DefaultModel: "new-model",
				},
			},
			phase:          &workflow.Phase{MaxTurns: maxTurns},
			wantPairs:      [][2]string{{"--model", "new-model"}},
			wantNotContain: []string{"old-model"},
		},
		{
			name: "copilot strips duplicate --headless from flags",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{
					Command: "copilot",
					Flags:   "--headless --verbose",
				},
			},
			phase:        &workflow.Phase{MaxTurns: maxTurns},
			wantContains: []string{"--verbose"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildCopilotPhaseArgs(tt.cfg, tt.wf, tt.phase, tt.harness)

			for _, want := range tt.wantContains {
				found := false
				for _, a := range args {
					if a == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("args %v: expected to contain %q", args, want)
				}
			}

			for _, notWant := range tt.wantNotContain {
				for _, a := range args {
					if a == notWant {
						t.Errorf("args %v: expected NOT to contain %q", args, notWant)
						break
					}
				}
			}

			for _, pair := range tt.wantPairs {
				flag, value := pair[0], pair[1]
				found := false
				for i := 0; i < len(args)-1; i++ {
					if args[i] == flag && args[i+1] == value {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("args %v: expected adjacent pair [%q, %q]", args, flag, value)
				}
			}

			// Verify --headless appears exactly once
			headlessCount := 0
			for _, a := range args {
				if a == "--headless" {
					headlessCount++
				}
			}
			if headlessCount != 1 {
				t.Errorf("expected exactly 1 --headless, got %d (args: %v)", headlessCount, args)
			}
		})
	}
}

func TestBuildProviderPhaseArgsDispatch(t *testing.T) {
	claudeCmd := "claude"
	copilotCmd := "copilot"

	cfg := &config.Config{
		Claude:  config.ClaudeConfig{Command: claudeCmd},
		Copilot: config.CopilotConfig{Command: copilotCmd},
	}
	phase := &workflow.Phase{MaxTurns: 5}

	t.Run("claude provider returns claude command", func(t *testing.T) {
		cmd, args := buildProviderPhaseArgs(cfg, nil, phase, "", "claude")
		if cmd != claudeCmd {
			t.Errorf("cmd = %q, want %q", cmd, claudeCmd)
		}
		if len(args) == 0 || args[0] != "-p" {
			t.Errorf("claude args[0] = %q, want -p (args: %v)", args[0], args)
		}
		// Claude args must NOT contain copilot-specific flags
		for _, a := range args {
			if a == "--headless" || a == "--system-prompt" || a == "--allowed-tools" {
				t.Errorf("claude args contain copilot-specific flag %q (args: %v)", a, args)
			}
		}
	})

	t.Run("copilot provider returns copilot command", func(t *testing.T) {
		cmd, args := buildProviderPhaseArgs(cfg, nil, phase, "", "copilot")
		if cmd != copilotCmd {
			t.Errorf("cmd = %q, want %q", cmd, copilotCmd)
		}
		if len(args) == 0 || args[0] != "--headless" {
			t.Errorf("copilot args[0] = %q, want --headless (args: %v)", args[0], args)
		}
		// Copilot args must NOT contain claude-specific flags
		for _, a := range args {
			if a == "-p" || a == "--append-system-prompt" || a == "--allowedTools" {
				t.Errorf("copilot args contain claude-specific flag %q (args: %v)", a, args)
			}
		}
	})
}

func TestBuildCommandCopilotDirect(t *testing.T) {
	cfg := &config.Config{
		LLM:      "copilot",
		MaxTurns: 50,
		Copilot: config.CopilotConfig{
			Command: "copilot",
		},
	}
	vessel := &queue.Vessel{
		Source: "manual",
		Prompt: "Fix the null pointer in handler.go",
	}
	cmd, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "copilot" {
		t.Errorf("expected cmd 'copilot', got %q", cmd)
	}
	if len(args) < 4 {
		t.Fatalf("expected at least 4 args, got %d: %v", len(args), args)
	}
	if args[0] != "--headless" {
		t.Errorf("expected --headless flag, got %q", args[0])
	}
	if args[1] != "Fix the null pointer in handler.go" {
		t.Errorf("expected prompt text, got %q", args[1])
	}
	// Must NOT contain claude-specific flags
	for _, a := range args {
		if a == "-p" {
			t.Errorf("copilot buildCommand should not contain -p (args: %v)", args)
		}
	}
}

func TestBuildCommandCopilotWorkflow(t *testing.T) {
	cfg := &config.Config{
		LLM:      "copilot",
		MaxTurns: 50,
		Copilot: config.CopilotConfig{
			Command: "copilot",
			Flags:   "--verbose",
		},
	}
	vessel := &queue.Vessel{
		Source:   "github-issue",
		Workflow: "fix-bug",
		Ref:      "https://github.com/owner/repo/issues/42",
	}
	cmd, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "copilot" {
		t.Errorf("expected cmd 'copilot', got %q", cmd)
	}
	if args[0] != "--headless" {
		t.Errorf("expected --headless flag, got %q", args[0])
	}
	if !strings.Contains(args[1], "/fix-bug") {
		t.Errorf("expected workflow in prompt, got %q", args[1])
	}
	// Flags should be appended
	found := false
	for _, a := range args {
		if a == "--verbose" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --verbose in args, got: %v", args)
	}
}

func TestBuildCommandCopilotDirectWithRef(t *testing.T) {
	cfg := &config.Config{
		LLM:      "copilot",
		MaxTurns: 50,
		Copilot: config.CopilotConfig{
			Command: "copilot",
		},
	}
	vessel := &queue.Vessel{
		Source: "manual",
		Prompt: "Fix the null pointer in handler.go",
		Ref:    "https://github.com/owner/repo/issues/99",
	}
	cmd, args, err := buildCommand(cfg, vessel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "copilot" {
		t.Errorf("expected cmd 'copilot', got %q", cmd)
	}
	if !strings.Contains(args[1], "Ref: https://github.com/owner/repo/issues/99") {
		t.Errorf("expected ref URL in prompt, got %q", args[1])
	}
	if !strings.Contains(args[1], "Fix the null pointer in handler.go") {
		t.Errorf("expected original prompt text, got %q", args[1])
	}
}

func TestBuildPromptOnlyCmdArgsHeadlessDedup(t *testing.T) {
	cfg := &config.Config{
		LLM:      "copilot",
		MaxTurns: 50,
		Copilot: config.CopilotConfig{
			Command: "copilot",
			Flags:   "--headless --verbose",
		},
	}
	_, args := buildPromptOnlyCmdArgs(cfg, "")
	headlessCount := 0
	for _, a := range args {
		if a == "--headless" {
			headlessCount++
		}
	}
	if headlessCount != 1 {
		t.Errorf("expected exactly 1 --headless, got %d (args: %v)", headlessCount, args)
	}
	// --verbose should still be present
	found := false
	for _, a := range args {
		if a == "--verbose" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --verbose in args, got: %v", args)
	}
}

func strPtrRunner(s string) *string { return &s }

func TestDrainCommandPhase(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "build", phaseType: "command", run: "echo building"},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("build complete\n"),
		gateErr:    nil,
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}

	// Command phases go through RunOutput (like gates), not RunPhase
	if len(cmdRunner.phaseCalls) != 0 {
		t.Errorf("expected 0 RunPhase calls for command phase, got %d", len(cmdRunner.phaseCalls))
	}

	// Verify output file was written
	outputPath := filepath.Join(dir, ".xylem", "phases", "issue-1", "build.output")
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Errorf("expected output file %s to exist", outputPath)
	}
	outputData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !strings.Contains(string(outputData), "build complete") {
		t.Errorf("output file content = %q, want to contain 'build complete'", string(outputData))
	}

	// Verify command file was written
	commandPath := filepath.Join(dir, ".xylem", "phases", "issue-1", "build.command")
	if _, err := os.Stat(commandPath); os.IsNotExist(err) {
		t.Errorf("expected command file %s to exist", commandPath)
	}
	commandData, err := os.ReadFile(commandPath)
	if err != nil {
		t.Fatalf("read command file: %v", err)
	}
	if !strings.Contains(string(commandData), "echo building") {
		t.Errorf("command file content = %q, want to contain 'echo building'", string(commandData))
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateCompleted {
		t.Errorf("expected vessel completed, got %s", vessels[0].State)
	}
}

func TestDrainCommandPhaseFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "build", phaseType: "command", run: "make build"},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("compilation failed\n"),
		gateErr:    &mockExitError{code: 1},
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateFailed {
		t.Errorf("expected vessel failed, got %s", vessels[0].State)
	}
}

func TestDrainCommandPhaseWithGate(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "build", phaseType: "command", run: "make build",
			gate: "      type: command\n      run: \"make test\"",
		},
		{name: "pr", promptContent: "Create PR", maxTurns: 3},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		gateCallResults: []gateCallResult{
			{output: []byte("build ok\n"), err: nil},   // command phase execution
			{output: []byte("tests pass\n"), err: nil},  // gate execution
		},
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}

	// Verify both command phase and gate were called via RunOutput
	if cmdRunner.gateCallCount != 2 {
		t.Errorf("expected 2 RunOutput calls (command + gate), got %d", cmdRunner.gateCallCount)
	}

	// Second phase (pr) should have been invoked via RunPhase
	if len(cmdRunner.phaseCalls) != 1 {
		t.Errorf("expected 1 RunPhase call (for pr phase), got %d", len(cmdRunner.phaseCalls))
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateCompleted {
		t.Errorf("expected vessel completed, got %s", vessels[0].State)
	}
}

func TestDrainCommandPhaseWithNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "check", phaseType: "command", run: "make check",
			noopMatch: "XYLEM_NOOP",
		},
		{name: "implement", promptContent: "Implement", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("Already up to date.\n\nXYLEM_NOOP\n"),
		gateErr:    nil,
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("expected 1 completed, got %d", result.Completed)
	}

	// No RunPhase calls should have been made (command phase + noop skips remaining)
	if len(cmdRunner.phaseCalls) != 0 {
		t.Fatalf("expected 0 RunPhase calls (noop should skip implement phase), got %d", len(cmdRunner.phaseCalls))
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateCompleted {
		t.Errorf("expected vessel completed, got %s", vessels[0].State)
	}
	if vessels[0].CurrentPhase != 1 {
		t.Errorf("expected CurrentPhase 1, got %d", vessels[0].CurrentPhase)
	}
}
