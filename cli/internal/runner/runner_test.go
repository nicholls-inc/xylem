package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/surface"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// --- Mock types ---

type mockCmdRunner struct {
	mu           sync.Mutex // protects phaseCalls and outputArgs
	processErr   error
	outputErr    error
	outputData   []byte
	phaseOutputs map[string][]byte // keyed by substring in prompt
	phaseErr     error
	// Per-phase prompt error injection keyed by substring in prompt.
	phaseErrByPrompt map[string]error
	started          int32
	gateOutput       []byte
	gateErr          error
	// Per-call gate results; when non-nil, overrides gateOutput/gateErr by call index
	gateCallResults []gateCallResult
	gateCallCount   int32
	runOutputHook   func(name string, args ...string) ([]byte, error, bool)
	runPhaseHook    func(dir, prompt, name string, args ...string) ([]byte, error, bool)
	// Track calls for assertion
	phaseCalls []phaseCall
	outputArgs [][]string
	lastBody   string
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
	m.mu.Lock()
	m.outputArgs = append(m.outputArgs, append([]string{name}, args...))
	for i, arg := range args {
		if arg == "--body" && i+1 < len(args) {
			m.lastBody = args[i+1]
		}
	}
	m.mu.Unlock()
	if m.runOutputHook != nil {
		if out, err, handled := m.runOutputHook(name, args...); handled {
			return out, err
		}
	}
	// Detect gate commands by matching the exact shape produced by gate.RunCommandGate:
	// RunOutput("sh", "-c", "cd <dir> && <cmd>")
	isGate := name == "sh" && len(args) >= 2 && args[0] == "-c" && strings.Contains(args[1], "cd ")
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
	m.mu.Lock()
	m.phaseCalls = append(m.phaseCalls, phaseCall{
		dir:    dir,
		prompt: string(prompt),
		name:   name,
		args:   args,
	})
	m.mu.Unlock()
	atomic.AddInt32(&m.started, 1)

	for key, err := range m.phaseErrByPrompt {
		if bytes.Contains(prompt, []byte(key)) {
			return nil, err
		}
	}

	if m.runPhaseHook != nil {
		if out, err, handled := m.runPhaseHook(dir, string(prompt), name, args...); handled {
			return out, err
		}
	}

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
	mu           sync.Mutex
	createErr    error
	path         string
	removeErr    error
	removeCalled bool
	removePath   string
}

func (m *mockWorktree) Create(_ context.Context, branchName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return "", m.createErr
	}
	if m.path != "" {
		return m.path, nil
	}
	return ".claude/worktrees/" + branchName, nil
}

func (m *mockWorktree) Remove(_ context.Context, worktreePath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func containsArgSequence(args []string, flag string, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func countRunOutputCalls(m *mockCmdRunner, name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	for _, args := range m.outputArgs {
		if len(args) > 0 && args[0] == name {
			count++
		}
	}
	return count
}

func loadSingleVessel(t *testing.T, q *queue.Queue) queue.Vessel {
	t.Helper()

	vessels, err := q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	return vessels[0]
}

func setBudget(cfg *config.Config, maxCostUSD float64, maxTokens int) {
	cfg.Cost = config.CostConfig{
		Budget: &config.BudgetConfig{
			MaxCostUSD: maxCostUSD,
			MaxTokens:  maxTokens,
		},
	}
}

func setPricedModel(cfg *config.Config) {
	cfg.Claude.DefaultModel = "claude-sonnet-4"
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
		fmt.Fprintf(&phaseYAML, "  - name: %s\n", p.name)

		if p.phaseType == "command" {
			phaseYAML.WriteString("    type: command\n")
			fmt.Fprintf(&phaseYAML, "    run: %q\n", p.run)
		} else {
			promptPath := filepath.Join(dir, ".xylem", "prompts", name, p.name+".md")
			os.MkdirAll(filepath.Dir(promptPath), 0o755)
			os.WriteFile(promptPath, []byte(p.promptContent), 0o644)

			fmt.Fprintf(&phaseYAML, "    prompt_file: %s\n", promptPath)
			fmt.Fprintf(&phaseYAML, "    max_turns: %d\n", p.maxTurns)
		}
		if p.noopMatch != "" {
			phaseYAML.WriteString("    noop:\n")
			fmt.Fprintf(&phaseYAML, "      match: %q\n", p.noopMatch)
		}
		if p.gate != "" {
			fmt.Fprintf(&phaseYAML, "    gate:\n%s\n", p.gate)
		}
		if p.allowedTools != "" {
			fmt.Fprintf(&phaseYAML, "    allowed_tools: %q\n", p.allowedTools)
		}
		if len(p.dependsOn) > 0 {
			phaseYAML.WriteString("    depends_on:\n")
			for _, dep := range p.dependsOn {
				fmt.Fprintf(&phaseYAML, "      - %s\n", dep)
			}
		}
	}

	workflowContent := fmt.Sprintf("name: %s\nphases:\n%s", name, phaseYAML.String())
	os.WriteFile(filepath.Join(workflowDir, name+".yaml"), []byte(workflowContent), 0o644)
}

func withTestWorkingDir(t *testing.T, dir string) {
	t.Helper()

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%q) error = %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}

func newTestTracer(t *testing.T) (*observability.Tracer, *tracetest.SpanRecorder) {
	t.Helper()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(rec),
	)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})

	return observability.NewTracerFromProvider(tp), rec
}

func endedSpanByName(t *testing.T, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	for _, span := range rec.Ended() {
		if span.Name() == name {
			return span
		}
	}

	t.Fatalf("no ended span named %q", name)
	return nil
}

func endedSpansByName(rec *tracetest.SpanRecorder, name string) []sdktrace.ReadOnlySpan {
	var spans []sdktrace.ReadOnlySpan
	for _, span := range rec.Ended() {
		if span.Name() == name {
			spans = append(spans, span)
		}
	}
	return spans
}

func spanAttrMap(span sdktrace.ReadOnlySpan) map[string]string {
	attrs := make(map[string]string, len(span.Attributes()))
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	return attrs
}

func spanHasExceptionEvent(span sdktrace.ReadOnlySpan, message string) bool {
	for _, event := range span.Events() {
		if event.Name != "exception" {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key == attribute.Key("exception.message") && attr.Value.AsString() == message {
				return true
			}
		}
	}
	return false
}

func newWorkflowRunner(t *testing.T, workflowName string, phases []testPhase, cmdRunner *mockCmdRunner, tracer *observability.Tracer) *Runner {
	t.Helper()

	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	if _, err := q.Enqueue(makeVessel(1, workflowName)); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	writeWorkflowFile(t, dir, workflowName, phases)
	withTestWorkingDir(t, dir)

	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Tracer = tracer
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	return r
}

func writePromptFile(t *testing.T, dir, relativePath, content string) string {
	t.Helper()

	path := filepath.Join(dir, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir prompt dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	return path
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

func TestDrainTracingSurfacesVesselHealthAndPatterns(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "trace-health"))

	writeWorkflowFile(t, dir, "trace-health", []testPhase{
		{name: "implement", promptContent: "Implement change", maxTurns: 5},
	})
	withTestWorkingDir(t, dir)

	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Implement change": []byte("broken output"),
		},
		phaseErr: errors.New("boom"),
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Tracer = tracer
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", result.Failed)
	}

	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	vesselAttrs := spanAttrMap(vesselSpan)
	if vesselAttrs["xylem.vessel.health"] != "unhealthy" {
		t.Fatalf("xylem.vessel.health = %q, want unhealthy", vesselAttrs["xylem.vessel.health"])
	}
	if vesselAttrs["xylem.vessel.anomalies"] != "run_failed,phase_failed" {
		t.Fatalf("xylem.vessel.anomalies = %q, want failure anomalies", vesselAttrs["xylem.vessel.anomalies"])
	}

	drainSpan := endedSpanByName(t, rec, "drain_run")
	drainAttrs := spanAttrMap(drainSpan)
	if drainAttrs["xylem.drain.unhealthy_vessels"] != "1" {
		t.Fatalf("xylem.drain.unhealthy_vessels = %q, want 1", drainAttrs["xylem.drain.unhealthy_vessels"])
	}
	if drainAttrs["xylem.drain.unhealthy_patterns"] != "phase_failed=1, run_failed=1" {
		t.Fatalf("xylem.drain.unhealthy_patterns = %q, want joined pattern counts", drainAttrs["xylem.drain.unhealthy_patterns"])
	}
}

func TestInspectVesselStatusMissingSummaryDoesNotWarn(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	r := New(cfg, nil, nil, nil)
	buf := captureStandardLogger(t)

	report := r.inspectVesselStatus(queue.Vessel{
		ID:        "issue-1",
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})

	if report.Health != VesselHealthHealthy {
		t.Fatalf("Health = %q, want %q", report.Health, VesselHealthHealthy)
	}
	if len(report.Anomalies) != 0 {
		t.Fatalf("Anomalies = %#v, want none", report.Anomalies)
	}
	if strings.Contains(buf.String(), "load vessel summary") {
		t.Fatalf("expected no warning for missing summary, got %q", buf.String())
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
	phaseType     string   // "command" or "" for prompt (default)
	run           string   // shell command for type=command
	dependsOn     []string // explicit phase dependencies
}

// --- Tests ---

func TestPhaseActionType(t *testing.T) {
	tests := []struct {
		name  string
		phase *workflow.Phase
		want  string
	}{
		{
			name:  "S25 prompt phase",
			phase: &workflow.Phase{Name: "plan"},
			want:  "phase_execute",
		},
		{
			name:  "S24 command phase",
			phase: &workflow.Phase{Name: "lint", Type: "command"},
			want:  "external_command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := phaseActionType(tt.phase); got != tt.want {
				t.Fatalf("phaseActionType(%+v) = %q, want %q", tt.phase, got, tt.want)
			}
		})
	}
}

func TestPhasePolicyIntents_ClassifiesHighRiskCommandActions(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	r := New(cfg, nil, nil, nil)
	vessel := queue.Vessel{
		ID:       "issue-1",
		Source:   "github-issue",
		Workflow: "fix-bug",
		Meta:     map[string]string{"config_source": "github"},
	}
	phaseDef := workflow.Phase{Name: "publish", Type: "command"}

	intents := r.phasePolicyIntents(vessel, phaseDef, `git commit -m "ship" && git push origin feature-1 && gh pr create --repo owner/repo`, "")
	require.Len(t, intents, 4)

	assert.Equal(t, "external_command", intents[0].Action)
	assert.Equal(t, "publish", intents[0].Resource)
	assert.Equal(t, "git_commit", intents[1].Action)
	assert.Equal(t, "*", intents[1].Resource)
	assert.Equal(t, "git_push", intents[2].Action)
	assert.Equal(t, "feature-1", intents[2].Resource)
	assert.Equal(t, "pr_create", intents[3].Action)
	assert.Equal(t, "owner/repo", intents[3].Resource)
	assert.Equal(t, "command", intents[1].Metadata["classified_from"])
}

func TestPhasePolicyIntents_ClassifiesHighRiskPromptActions(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	r := New(cfg, nil, nil, nil)
	vessel := queue.Vessel{
		ID:       "issue-1",
		Source:   "github-issue",
		Workflow: "fix-bug",
		Meta:     map[string]string{"config_source": "github"},
	}
	phaseDef := workflow.Phase{Name: "pr"}

	intents := r.phasePolicyIntents(vessel, phaseDef, "", "Commit all changes with a clear commit message, push the branch, and create a pull request using gh pr create.")
	require.Len(t, intents, 4)

	assert.Equal(t, "phase_execute", intents[0].Action)
	assert.Equal(t, "git_commit", intents[1].Action)
	assert.Equal(t, "git_push", intents[2].Action)
	assert.Equal(t, "*", intents[2].Resource)
	assert.Equal(t, "pr_create", intents[3].Action)
	assert.Equal(t, "owner/repo", intents[3].Resource)
	assert.Equal(t, "prompt", intents[1].Metadata["classified_from"])
}

func TestPhasePolicyIntents_IgnoresHighRiskPhrasesFromRenderedPromptContext(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	r := New(cfg, nil, nil, nil)
	vessel := queue.Vessel{
		ID:       "issue-1",
		Source:   "github-issue",
		Workflow: "fix-bug",
		Meta:     map[string]string{"config_source": "github"},
	}
	phaseDef := workflow.Phase{Name: "analyze"}

	intents := r.phasePolicyIntents(vessel, phaseDef, "", "Analyze the following issue:\n\nIssue body:\n{{.Issue.Body}}\n")
	require.Len(t, intents, 1)
	assert.Equal(t, "phase_execute", intents[0].Action)
	assert.Equal(t, "analyze", intents[0].Resource)
}

func TestSmoke_S1_PolicyDenialShortCircuitsBeforeSurfaceSnapshot(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "policy-deny"))

	writeWorkflowFile(t, dir, "policy-deny", []testPhase{
		{name: "solve", promptContent: "Solve the issue", maxTurns: 5},
	})
	require.NoError(t, os.Symlink("broken-target", filepath.Join(dir, ".xylem.yml")))
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "deny-all",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.Deny}},
	}}, nil, nil)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "denied by policy")
	assert.Len(t, cmdRunner.phaseCalls, 0)

	_, statErr := os.Stat(filepath.Join(cfg.StateDir, "phases", "issue-1", "solve.output"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestSmoke_S2_SurfacePreSnapshotFailureShortCircuitsBeforePhaseExecution(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "snapshot-fail"))

	writeWorkflowFile(t, dir, "snapshot-fail", []testPhase{
		{name: "plan", promptContent: "Plan the fix", maxTurns: 5},
	})
	require.NoError(t, os.Symlink("broken-target", filepath.Join(dir, ".xylem.yml")))
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "snapshot failed")
	assert.Len(t, cmdRunner.phaseCalls, 0)

	_, statErr := os.Stat(filepath.Join(cfg.StateDir, "phases", "issue-1", "plan.output"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestSmoke_S3_PhaseExecutionFailureShortCircuitsBeforeSurfacePostVerification(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "phase-fail"))

	writeWorkflowFile(t, dir, "phase-fail", []testPhase{
		{name: "implement", promptContent: "Implement the fix", maxTurns: 5},
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".xylem.yml"), []byte("repo: owner/repo\n"), 0o644))
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(worktreePath, _, _ string, _ ...string) ([]byte, error, bool) {
			if err := os.WriteFile(filepath.Join(worktreePath, ".xylem.yml"), []byte("tampered: true\n"), 0o644); err != nil {
				return nil, err, true
			}
			return nil, errors.New("phase boom"), true
		},
	}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "allow-all",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.Allow}},
	}}, auditLog, nil)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "phase boom")
	assert.NotContains(t, vessel.Error, "violated protected surfaces")
	assert.Len(t, cmdRunner.phaseCalls, 1)

	entries, entryErr := auditLog.Entries()
	require.NoError(t, entryErr)
	for _, entry := range entries {
		assert.NotEqual(t, "file_write", entry.Intent.Action)
	}
}

func TestSmoke_S4_SurfaceViolationShortCircuitsBeforeBudgetCheck(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 0.0001, 0)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "surface-before-budget"))

	writeWorkflowFile(t, dir, "surface-before-budget", []testPhase{
		{name: "implement", promptContent: "Implement the fix", maxTurns: 5},
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".xylem.yml"), []byte("repo: owner/repo\n"), 0o644))
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(worktreePath, _, _ string, _ ...string) ([]byte, error, bool) {
			if err := os.WriteFile(filepath.Join(worktreePath, ".xylem.yml"), []byte("tampered: true\n"), 0o644); err != nil {
				return nil, err, true
			}
			return []byte(strings.Repeat("x", 4000)), nil, true
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "violated protected surfaces")
	assert.NotContains(t, vessel.Error, "budget exceeded")

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	assert.False(t, summary.BudgetExceeded)
	assert.Zero(t, summary.TotalTokensEst)
}

func TestSmoke_S17_RunnerWithNilIntermediarySkipsPolicyCheck(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "nil-intermediary"))

	writeWorkflowFile(t, dir, "nil-intermediary", []testPhase{
		{name: "solve", promptContent: "Solve the issue", maxTurns: 5},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{"Solve the issue": []byte("done")},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateCompleted, vessel.State)
	assert.Empty(t, vessel.Error)
	assert.Len(t, cmdRunner.phaseCalls, 1)
}

func TestSmoke_S18_RunnerPolicyDeniesPhase(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "deny-phase"))

	writeWorkflowFile(t, dir, "deny-phase", []testPhase{
		{name: "solve", promptContent: "Solve the issue", maxTurns: 5},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "deny-all",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.Deny}},
	}}, nil, nil)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "denied by policy")
	assert.Contains(t, vessel.Error, "solve")
	assert.Len(t, cmdRunner.phaseCalls, 0)
}

func TestSmoke_S19_RunnerPolicyRequireApproval(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "approval-phase"))

	writeWorkflowFile(t, dir, "approval-phase", []testPhase{
		{name: "deploy", promptContent: "Deploy the fix", maxTurns: 5},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "require-approval",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.RequireApproval}},
	}}, nil, nil)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "requires approval")
	assert.Contains(t, vessel.Error, "deploy")
	assert.Contains(t, vessel.Error, "automatic approval not yet supported")
	assert.Len(t, cmdRunner.phaseCalls, 0)
}

func TestSmoke_S20_SurfacePreSnapshotIsTakenBeforePhaseExecution(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "surface-order"))

	writeWorkflowFile(t, dir, "surface-order", []testPhase{
		{name: "implement", promptContent: "Implement the fix", maxTurns: 5},
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".xylem.yml"), []byte("repo: owner/repo\n"), 0o644))
	withTestWorkingDir(t, dir)

	var sawOriginal atomic.Bool
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(worktreePath, _, _ string, _ ...string) ([]byte, error, bool) {
			data, err := os.ReadFile(filepath.Join(worktreePath, ".xylem.yml"))
			if err != nil {
				return nil, err, true
			}
			sawOriginal.Store(string(data) == "repo: owner/repo\n")
			if err := os.WriteFile(filepath.Join(worktreePath, ".xylem.yml"), []byte("tampered: true\n"), 0o644); err != nil {
				return nil, err, true
			}
			return []byte("mutated"), nil, true
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.True(t, sawOriginal.Load())

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "violated protected surfaces")
	assert.Contains(t, vessel.Error, ".xylem.yml")
}

func TestSmoke_S21_SurfacePostVerificationDetectsMutation(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "surface-mutation"))

	writeWorkflowFile(t, dir, "surface-mutation", []testPhase{
		{name: "implement", promptContent: "Implement the fix", maxTurns: 5},
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".xylem.yml"), []byte("repo: owner/repo\n"), 0o644))
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(worktreePath, _, _ string, _ ...string) ([]byte, error, bool) {
			if err := os.WriteFile(filepath.Join(worktreePath, ".xylem.yml"), []byte("tampered: true\n"), 0o644); err != nil {
				return nil, err, true
			}
			return []byte("mutated"), nil, true
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "violated protected surfaces")
	assert.Contains(t, vessel.Error, ".xylem.yml")
}

func TestSmoke_S22_AuditLogRecordsPolicyDecisions(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "audit-policy"))

	writeWorkflowFile(t, dir, "audit-policy", []testPhase{
		{name: "implement", promptContent: "Implement the fix", maxTurns: 5},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{"Implement the fix": []byte("done")},
	}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "allow-all",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.Allow}},
	}}, auditLog, nil)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	entries, entryErr := auditLog.Entries()
	require.NoError(t, entryErr)
	require.NotEmpty(t, entries)
	assert.Equal(t, "phase_execute", entries[0].Intent.Action)
	assert.Equal(t, "issue-1", entries[0].Intent.AgentID)
	assert.Equal(t, intermediary.Allow, entries[0].Decision)
}

func TestSmoke_S23_AuditLogRecordsSurfaceViolations(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "audit-surface"))

	writeWorkflowFile(t, dir, "audit-surface", []testPhase{
		{name: "implement", promptContent: "Implement the fix", maxTurns: 5},
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".xylem.yml"), []byte("repo: owner/repo\n"), 0o644))
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(worktreePath, _, _ string, _ ...string) ([]byte, error, bool) {
			if err := os.WriteFile(filepath.Join(worktreePath, ".xylem.yml"), []byte("tampered: true\n"), 0o644); err != nil {
				return nil, err, true
			}
			return []byte("mutated"), nil, true
		},
	}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "allow-all",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.Allow}},
	}}, auditLog, nil)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	entries, entryErr := auditLog.Entries()
	require.NoError(t, entryErr)

	found := false
	for _, entry := range entries {
		if entry.Intent.Action == "file_write" && entry.Intent.Resource == ".xylem.yml" {
			found = true
			assert.Equal(t, intermediary.Deny, entry.Decision)
		}
	}
	assert.True(t, found)
}

func TestSmoke_S26_FormatViolationsProducesHumanReadableOutput(t *testing.T) {
	got := formatViolations([]surface.Violation{
		{Path: ".xylem.yml", Before: "abc", After: "xyz"},
		{Path: ".xylem/HARNESS.md", Before: "111", After: "deleted"},
	})

	assert.Contains(t, got, ".xylem.yml")
	assert.Contains(t, got, "before: abc")
	assert.Contains(t, got, "after: xyz")
	assert.Contains(t, got, ".xylem/HARNESS.md")
	assert.Contains(t, got, "deleted")
}

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

// TestWS6S29NilHarnessFieldsRunsNormally verifies that a runner without
// intermediary, audit log, or tracer behaves like the pre-harness runner.
//
// Covers: WS6 S29.
func TestWS6S29NilHarnessFieldsRunsNormally(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	if _, err := q.Enqueue(makeVessel(1, "fix-bug")); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "fix", promptContent: "Fix issue.", maxTurns: 5},
	})

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%q) error = %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	if r.Intermediary != nil {
		t.Fatalf("r.Intermediary = %#v, want nil", r.Intermediary)
	}
	if r.AuditLog != nil {
		t.Fatalf("r.AuditLog = %#v, want nil", r.AuditLog)
	}
	if r.Tracer != nil {
		t.Fatalf("r.Tracer = %#v, want nil", r.Tracer)
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("Completed = %d, want 1", result.Completed)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if vessels[0].State != queue.StateCompleted {
		t.Errorf("vessel state = %q, want %q", vessels[0].State, queue.StateCompleted)
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

func TestSmoke_WS3_S25_PerVesselTrackerIsCreatedFreshForEachVessel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 10.0, 150)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "tracker-fresh"))
	_, _ = q.Enqueue(makeVessel(2, "tracker-fresh"))

	writeWorkflowFile(t, dir, "tracker-fresh", []testPhase{
		{name: "implement", promptContent: "Implement {{.Vessel.ID}}", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	longOutput := strings.Repeat("x", 400)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"issue-1": []byte(longOutput),
			"issue-2": []byte(longOutput),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, result.Completed)

	summary1 := loadSummary(t, cfg.StateDir, "issue-1")
	summary2 := loadSummary(t, cfg.StateDir, "issue-2")
	require.Len(t, summary1.Phases, 1)
	require.Len(t, summary2.Phases, 1)

	phase1Tokens := summary1.Phases[0].InputTokensEst + summary1.Phases[0].OutputTokensEst
	phase2Tokens := summary2.Phases[0].InputTokensEst + summary2.Phases[0].OutputTokensEst

	assert.False(t, summary1.BudgetExceeded)
	assert.False(t, summary2.BudgetExceeded)
	assert.Positive(t, phase1Tokens)
	assert.Equal(t, phase1Tokens, summary1.TotalTokensEst)
	assert.Equal(t, phase2Tokens, summary2.TotalTokensEst)
	assert.Less(t, phase1Tokens, 150)
	assert.Greater(t, phase1Tokens*2, 150)
}

func TestSmoke_WS3_S26_CostRecordedAfterEachPromptTypePhase(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 10.0, 10000)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "prompt-costs"))

	writeWorkflowFile(t, dir, "prompt-costs", []testPhase{
		{name: "plan", promptContent: "Plan {{.Vessel.ID}}", maxTurns: 5},
		{name: "implement", promptContent: "Implement {{.PreviousOutputs.plan}}", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Plan":      []byte("analysis complete"),
			"Implement": []byte("implementation complete"),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	require.Len(t, summary.Phases, 2)
	assert.Positive(t, summary.TotalTokensEst)
	for i, phaseSummary := range summary.Phases {
		assert.Equal(t, "prompt", phaseSummary.Type, "phase %d type", i)
		assert.Positive(t, phaseSummary.InputTokensEst, "phase %d input tokens", i)
		assert.Positive(t, phaseSummary.OutputTokensEst, "phase %d output tokens", i)
		assert.Positive(t, phaseSummary.CostUSDEst, "phase %d cost", i)
	}
}

func TestSmoke_S27_CommandTypePhasesDoNotGenerateCostRecords(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 10.0, 10000)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "mixed-costs"))

	writeWorkflowFile(t, dir, "mixed-costs", []testPhase{
		{name: "plan", promptContent: "Plan the fix", maxTurns: 5},
		{name: "lint", phaseType: "command", run: "echo lint ok"},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Plan the fix": []byte("analysis"),
		},
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "sh" {
				return []byte("lint ok"), nil, true
			}
			return nil, nil, false
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	require.Len(t, summary.Phases, 2)
	promptPhase := summary.Phases[0]
	commandPhase := summary.Phases[1]

	assert.Equal(t, "prompt", promptPhase.Type)
	assert.Positive(t, promptPhase.InputTokensEst)
	assert.Positive(t, promptPhase.OutputTokensEst)
	assert.Positive(t, promptPhase.CostUSDEst)
	assert.Equal(t, "command", commandPhase.Type)
	assert.Zero(t, commandPhase.InputTokensEst)
	assert.Zero(t, commandPhase.OutputTokensEst)
	assert.Zero(t, commandPhase.CostUSDEst)
	assert.Equal(t, promptPhase.InputTokensEst+promptPhase.OutputTokensEst, summary.TotalTokensEst)
	assert.Equal(t, promptPhase.CostUSDEst, summary.TotalCostUSDEst)
}

func TestSmoke_S28_BudgetEnforcementFailsVesselWhenBudgetIsExceeded(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 0.0001, 0)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "budget-fail"))

	writeWorkflowFile(t, dir, "budget-fail", []testPhase{
		{name: "plan", promptContent: "Budget failure phase 1", maxTurns: 5},
		{name: "implement", promptContent: "Budget failure phase 2", maxTurns: 5},
		{name: "verify", promptContent: "Budget failure phase 3", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Budget failure phase 1": []byte(strings.Repeat("y", 4000)),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessels, err := q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, queue.StateFailed, vessels[0].State)
	assert.Contains(t, vessels[0].Error, "budget exceeded")
	assert.Contains(t, vessels[0].Error, "estimated cost")
	assert.Contains(t, vessels[0].Error, "estimated tokens")
	require.Len(t, cmdRunner.phaseCalls, 1)
	assert.Contains(t, cmdRunner.phaseCalls[0].prompt, "Budget failure phase 1")

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	assert.Equal(t, "failed", summary.State)
	require.Len(t, summary.Phases, 1)
	assert.Equal(t, "failed", summary.Phases[0].Status)
	assert.True(t, summary.BudgetExceeded)
}

func TestSmoke_S29_NilBudgetMeansNoEnforcement(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "budget-none"))

	writeWorkflowFile(t, dir, "budget-none", []testPhase{
		{name: "implement", promptContent: "Budget disabled path", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Budget disabled path": []byte(strings.Repeat("z", 4000)),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	vessels, err := q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, queue.StateCompleted, vessels[0].State)
	assert.NotContains(t, vessels[0].Error, "budget exceeded")

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	assert.Equal(t, "completed", summary.State)
	assert.False(t, summary.BudgetExceeded)
}

func TestSmoke_WS6_S5_BudgetExceededShortCircuitsBeforeGate(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 0.0001, 0)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "gate-budget"))

	writeWorkflowFile(t, dir, "gate-budget", []testPhase{
		{
			name:          "implement",
			promptContent: "Budget gate short circuit",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"go test ./...\"",
		},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Budget gate short circuit": []byte(strings.Repeat("g", 4000)),
		},
		gateOutput: []byte("ok"),
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, 0, countRunOutputCalls(cmdRunner, "sh"))

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "budget exceeded")

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	assert.Equal(t, "failed", summary.State)
	assert.True(t, summary.BudgetExceeded)
	require.Len(t, summary.Phases, 1)
	assert.Equal(t, "failed", summary.Phases[0].Status)
	assert.Equal(t, "command", summary.Phases[0].GateType)
}

func TestSmoke_WS6_S12_PromptOnlyVesselGetsVesselSpan(t *testing.T) {
	tracer, rec := newTestTracer(t)
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "prompt-only span"))

	r := New(cfg, q, &mockWorktree{path: dir}, &mockCmdRunner{})
	r.Tracer = tracer
	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	drainSpan := endedSpanByName(t, rec, "drain_run")
	vesselSpan := endedSpanByName(t, rec, "vessel:prompt-1")
	assert.Equal(t, drainSpan.SpanContext().TraceID(), vesselSpan.Parent().TraceID())
	assert.Equal(t, drainSpan.SpanContext().SpanID(), vesselSpan.Parent().SpanID())
	attrs := spanAttrMap(vesselSpan)
	assert.Equal(t, "prompt-1", attrs["xylem.vessel.id"])
	assert.Equal(t, "manual", attrs["xylem.vessel.source"])
	assert.Equal(t, "", attrs["xylem.vessel.workflow"])
}

func TestSmoke_WS6_S13_PromptOnlyVesselGetsCostTracking(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 10.0, 10000)

	vessel := makePromptVessel(1, "prompt-only cost tracking")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(vessel)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"prompt-only cost tracking": []byte(strings.Repeat("c", 800)),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	vrs := newVesselRunState(cfg, vessel, time.Now().UTC())

	outcome := r.runPromptOnly(context.Background(), vessel, dir, &source.Manual{}, vrs)
	assert.Equal(t, "completed", outcome)
	require.NotNil(t, vrs.costTracker)
	assert.Positive(t, vrs.costTracker.TotalTokens())
	assert.Positive(t, vrs.costTracker.TotalCost())

	summary := loadSummary(t, cfg.StateDir, "prompt-1")
	assert.Positive(t, summary.TotalTokensEst)
	assert.Positive(t, summary.TotalCostUSDEst)
}

func TestSmoke_WS6_S14_PromptOnlyVesselGetsSurfaceVerification(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	protectedPath := filepath.Join(dir, ".xylem.yml")
	originalContents := "repo: owner/repo\n"

	require.NoError(t, os.WriteFile(protectedPath, []byte(originalContents), 0o644))

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "prompt-only surfaces"))

	var sawOriginalContents bool
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(dirPath, prompt, name string, args ...string) ([]byte, error, bool) {
			data, err := os.ReadFile(protectedPath)
			require.NoError(t, err)
			sawOriginalContents = string(data) == originalContents
			return []byte("no protected mutations"), nil, true
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	assert.True(t, sawOriginalContents)
	require.Len(t, cmdRunner.phaseCalls, 1)
	assert.Equal(t, queue.StateCompleted, loadSingleVessel(t, q).State)
	data, readErr := os.ReadFile(protectedPath)
	require.NoError(t, readErr)
	assert.Equal(t, originalContents, string(data))
}

func TestSmoke_WS6_S15_PromptOnlyVesselNoPolicy(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "prompt-only bypasses policy"))

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "deny-all",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.Deny}},
	}}, auditLog, nil)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	entries, err := auditLog.Entries()
	if err != nil {
		t.Fatalf("AuditLog.Entries() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}

func TestSmoke_WS6_S16_PromptOnlyVesselNoEvidence(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "prompt-only no evidence"))

	r := New(cfg, q, &mockWorktree{path: dir}, &mockCmdRunner{})
	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	manifestPath := filepath.Join(cfg.StateDir, "phases", "prompt-1", "evidence-manifest.json")
	assert.NoFileExists(t, manifestPath)
}

func TestSmoke_WS6_S17_PromptOnlyVesselSummaryArtifact(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "prompt-only summary"))

	r := New(cfg, q, &mockWorktree{path: dir}, &mockCmdRunner{})
	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	assert.FileExists(t, filepath.Join(cfg.StateDir, "phases", "prompt-1", summaryFileName))
	summary := loadSummary(t, cfg.StateDir, "prompt-1")
	assert.Equal(t, "completed", summary.State)
	assert.Equal(t, "prompt-1", summary.VesselID)
}

func TestSmoke_WS6_S18_PromptOnlyVesselSummaryEmptyPhases(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "prompt-only empty phases"))

	r := New(cfg, q, &mockWorktree{path: dir}, &mockCmdRunner{})
	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	summary := loadSummary(t, cfg.StateDir, "prompt-1")
	if len(summary.Phases) != 0 {
		t.Fatalf("len(summary.Phases) = %d, want 0", len(summary.Phases))
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
	t.Setenv("XYLEM_DTU_STATE_PATH", "/tmp/dtu/state.json")

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
	for i, call := range cmdRunner.phaseCalls {
		wantAttempt := strconv.Itoa(i + 1)
		if !containsArgSequence(call.args, "--dtu-attempt", wantAttempt) {
			t.Errorf("phase call %d args = %v, want --dtu-attempt %s", i, call.args, wantAttempt)
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

func TestCheckWaitingVesselsResumesAndDrainCompletes(t *testing.T) {
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
		{name: "implement", promptContent: "Implement after approval", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	labelViewJSON := `{"labels":[{"name":"plan-approved"}]}`
	cmdRunner := &mockCmdRunner{
		outputData: []byte(labelViewJSON),
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	first, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("first Drain() error = %v", err)
	}
	if first.Waiting != 1 {
		t.Fatalf("first Drain().Waiting = %d, want 1", first.Waiting)
	}

	waiting, err := q.FindByID("issue-1")
	if err != nil {
		t.Fatalf("FindByID(waiting) error = %v", err)
	}
	if waiting.State != queue.StateWaiting {
		t.Fatalf("state after first drain = %s, want waiting", waiting.State)
	}
	if waiting.CurrentPhase != 1 {
		t.Fatalf("CurrentPhase after waiting = %d, want 1", waiting.CurrentPhase)
	}
	if waiting.WorktreePath == "" {
		t.Fatal("expected WorktreePath to be persisted while waiting")
	}

	r.CheckWaitingVessels(context.Background())

	resumed, err := q.FindByID("issue-1")
	if err != nil {
		t.Fatalf("FindByID(resumed) error = %v", err)
	}
	if resumed.State != queue.StatePending {
		t.Fatalf("state after resume = %s, want pending", resumed.State)
	}
	if resumed.WaitingSince != nil {
		t.Fatal("expected WaitingSince cleared after resume")
	}
	if resumed.WaitingFor != "" {
		t.Fatalf("expected WaitingFor cleared after resume, got %q", resumed.WaitingFor)
	}
	if resumed.CurrentPhase != 1 {
		t.Fatalf("CurrentPhase after resume = %d, want 1", resumed.CurrentPhase)
	}
	if resumed.WorktreePath != waiting.WorktreePath {
		t.Fatalf("WorktreePath after resume = %q, want %q", resumed.WorktreePath, waiting.WorktreePath)
	}

	second, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("second Drain() error = %v", err)
	}
	if second.Completed != 1 {
		t.Fatalf("second Drain().Completed = %d, want 1", second.Completed)
	}

	done, err := q.FindByID("issue-1")
	if err != nil {
		t.Fatalf("FindByID(done) error = %v", err)
	}
	if done.State != queue.StateCompleted {
		t.Fatalf("final state = %s, want completed", done.State)
	}
	if len(cmdRunner.phaseCalls) != 2 {
		t.Fatalf("phase call count = %d, want 2", len(cmdRunner.phaseCalls))
	}
	if !strings.Contains(cmdRunner.phaseCalls[1].prompt, "Implement after approval") {
		t.Fatalf("second phase prompt = %q, want implement phase prompt", cmdRunner.phaseCalls[1].prompt)
	}
	if wt.removePath != waiting.WorktreePath {
		t.Fatalf("removed worktree path = %q, want %q", wt.removePath, waiting.WorktreePath)
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
		args := buildPhaseArgs(cfg, nil, nil, p, "")

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
		args := buildPhaseArgs(cfg, nil, nil, p, "")

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
		args := buildPhaseArgs(cfg, nil, nil, p, "")

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
		args := buildPhaseArgs(cfg, nil, nil, p, "harness content")

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
			args := buildPhaseArgs(tt.cfg, nil, tt.wf, tt.phase, "")

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
			got := resolveProvider(cfg, nil, wf, p)
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
			got := resolveModel(cfg, nil, wf, p, tt.provider)
			if got != tt.want {
				t.Errorf("resolveModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveProviderWithSource(t *testing.T) {
	copilot := "copilot"
	tests := []struct {
		name     string
		cfgLLM   string
		srcLLM   string
		wfLLM    *string
		phaseLLM *string
		want     string
	}{
		{"source overrides config", "claude", "copilot", nil, nil, "copilot"},
		{"workflow overrides source", "claude", "copilot", strPtrRunner("claude"), nil, "claude"},
		{"phase overrides source", "claude", "copilot", nil, &copilot, "copilot"},
		{"empty source falls to config", "copilot", "", nil, nil, "copilot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{LLM: tt.cfgLLM}
			var srcCfg *config.SourceConfig
			if tt.srcLLM != "" {
				srcCfg = &config.SourceConfig{LLM: tt.srcLLM}
			}
			var wf *workflow.Workflow
			if tt.wfLLM != nil {
				wf = &workflow.Workflow{LLM: tt.wfLLM}
			}
			var p *workflow.Phase
			if tt.phaseLLM != nil {
				p = &workflow.Phase{LLM: tt.phaseLLM}
			}
			got := resolveProvider(cfg, srcCfg, wf, p)
			if got != tt.want {
				t.Errorf("resolveProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveModelWithSourceAndConfigModel(t *testing.T) {
	tests := []struct {
		name       string
		srcModel   string
		wfModel    *string
		phaseModel *string
		provider   string
		want       string
	}{
		{"provider default used when no phase/wf/src for claude", "", nil, nil, "claude", "claude-default"},
		{"provider default used when no phase/wf/src for copilot", "", nil, nil, "copilot", "copilot-default"},
		{"source model beats provider default", "src-model", nil, nil, "claude", "src-model"},
		{"workflow model beats source model", "src-model", strPtrRunner("wf-model"), nil, "claude", "wf-model"},
		{"phase model beats all", "src-model", strPtrRunner("wf-model"), strPtrRunner("phase-model"), "claude", "phase-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Claude:  config.ClaudeConfig{DefaultModel: "claude-default"},
				Copilot: config.CopilotConfig{DefaultModel: "copilot-default"},
			}
			var srcCfg *config.SourceConfig
			if tt.srcModel != "" {
				srcCfg = &config.SourceConfig{Model: tt.srcModel}
			}
			var wf *workflow.Workflow
			if tt.wfModel != nil {
				wf = &workflow.Workflow{Model: tt.wfModel}
			}
			var p *workflow.Phase
			if tt.phaseModel != nil {
				p = &workflow.Phase{Model: tt.phaseModel}
			}
			got := resolveModel(cfg, srcCfg, wf, p, tt.provider)
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
		prompt         string
		wantContains   []string
		wantNotContain []string
		wantPairs      [][2]string // [flag, value] pairs that must be adjacent
	}{
		{
			name:           "basic copilot args",
			cfg:            &config.Config{Copilot: config.CopilotConfig{Command: "copilot"}},
			phase:          &workflow.Phase{MaxTurns: maxTurns},
			prompt:         "test prompt",
			wantContains:   []string{"-p", "-s"},
			wantNotContain: []string{"--headless", "--max-turns", "--system-prompt"},
		},
		{
			name: "copilot with model from config default",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{Command: "copilot", DefaultModel: "gpt-4o"},
			},
			phase:     &workflow.Phase{MaxTurns: maxTurns},
			prompt:    "test prompt",
			wantPairs: [][2]string{{"--model", "gpt-4o"}},
		},
		{
			name: "copilot with model from phase overrides default",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{Command: "copilot", DefaultModel: "gpt-4o"},
			},
			phase:          &workflow.Phase{MaxTurns: maxTurns, Model: strPtrRunner("phase-model")},
			prompt:         "test prompt",
			wantPairs:      [][2]string{{"--model", "phase-model"}},
			wantNotContain: []string{"gpt-4o"},
		},
		{
			name: "copilot with extra flags",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{Command: "copilot", Flags: "--verbose"},
			},
			phase:        &workflow.Phase{MaxTurns: maxTurns},
			prompt:       "test prompt",
			wantContains: []string{"--verbose"},
		},
		{
			name:         "copilot with allowed_tools uses --available-tools",
			cfg:          &config.Config{Copilot: config.CopilotConfig{Command: "copilot"}},
			phase:        &workflow.Phase{MaxTurns: maxTurns, AllowedTools: strPtrRunner("Bash,Read")},
			prompt:       "test prompt",
			wantPairs:    [][2]string{{"--available-tools", "Bash,Read"}},
			wantContains: []string{"--allow-all-tools"},
		},
		{
			name:           "copilot harness prepended to prompt",
			cfg:            &config.Config{Copilot: config.CopilotConfig{Command: "copilot"}},
			phase:          &workflow.Phase{MaxTurns: maxTurns},
			harness:        "harness instructions",
			prompt:         "test prompt",
			wantNotContain: []string{"--system-prompt"},
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
			prompt:         "test prompt",
			wantPairs:      [][2]string{{"--model", "new-model"}},
			wantNotContain: []string{"old-model", "--headless"},
		},
		{
			name: "copilot strips legacy --headless from flags",
			cfg: &config.Config{
				Copilot: config.CopilotConfig{
					Command: "copilot",
					Flags:   "--headless --verbose",
				},
			},
			phase:          &workflow.Phase{MaxTurns: maxTurns},
			prompt:         "test prompt",
			wantContains:   []string{"--verbose"},
			wantNotContain: []string{"--headless"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildCopilotPhaseArgs(tt.cfg, nil, tt.wf, tt.phase, tt.harness, tt.prompt)

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

			// Verify -p appears exactly once (as args[0])
			pCount := 0
			for _, a := range args {
				if a == "-p" {
					pCount++
				}
			}
			if pCount != 1 {
				t.Errorf("expected exactly 1 -p, got %d (args: %v)", pCount, args)
			}

			// Verify no invalid copilot flags
			for _, a := range args {
				if a == "--headless" || a == "--system-prompt" || a == "--allowed-tools" {
					t.Errorf("args contain invalid copilot flag %q (args: %v)", a, args)
				}
			}
		})
	}

	// Dedicated test: harness content is prepended to the prompt in -p value
	t.Run("harness prepended to prompt in -p value", func(t *testing.T) {
		cfg := &config.Config{Copilot: config.CopilotConfig{Command: "copilot"}}
		phase := &workflow.Phase{MaxTurns: maxTurns}
		args := buildCopilotPhaseArgs(cfg, nil, nil, phase, "harness text", "user prompt")
		if len(args) < 2 {
			t.Fatalf("expected at least 2 args, got %d: %v", len(args), args)
		}
		if args[0] != "-p" {
			t.Errorf("args[0] = %q, want -p", args[0])
		}
		promptValue := args[1]
		if !strings.Contains(promptValue, "harness text") {
			t.Errorf("prompt value missing harness: %q", promptValue)
		}
		if !strings.Contains(promptValue, "user prompt") {
			t.Errorf("prompt value missing user prompt: %q", promptValue)
		}
		if !strings.HasPrefix(promptValue, "harness text") {
			t.Errorf("harness should be prepended, got: %q", promptValue)
		}
	})
}

func TestBuildProviderPhaseArgsDispatch(t *testing.T) {
	claudeCmd := "claude"
	copilotCmd := "copilot"

	cfg := &config.Config{
		Claude:  config.ClaudeConfig{Command: claudeCmd},
		Copilot: config.CopilotConfig{Command: copilotCmd},
	}
	phase := &workflow.Phase{MaxTurns: 5}

	t.Run("claude provider returns claude command and stdin", func(t *testing.T) {
		cmd, args, stdin := buildProviderPhaseArgs(cfg, nil, nil, phase, "", "claude", "test prompt", 1)
		if cmd != claudeCmd {
			t.Errorf("cmd = %q, want %q", cmd, claudeCmd)
		}
		if len(args) == 0 || args[0] != "-p" {
			t.Errorf("claude args[0] = %q, want -p (args: %v)", args[0], args)
		}
		if stdin == nil {
			t.Error("claude stdin should be non-nil (prompt delivered via stdin)")
		}
		// Claude args must NOT contain copilot-specific flags
		for _, a := range args {
			if a == "--headless" || a == "--available-tools" || a == "-s" {
				t.Errorf("claude args contain copilot-specific flag %q (args: %v)", a, args)
			}
		}
	})

	t.Run("copilot provider returns copilot command and nil stdin", func(t *testing.T) {
		cmd, args, stdin := buildProviderPhaseArgs(cfg, nil, nil, phase, "", "copilot", "test prompt", 1)
		if cmd != copilotCmd {
			t.Errorf("cmd = %q, want %q", cmd, copilotCmd)
		}
		if len(args) == 0 || args[0] != "-p" {
			t.Errorf("copilot args[0] = %q, want -p (args: %v)", args[0], args)
		}
		if stdin != nil {
			t.Error("copilot stdin should be nil (prompt embedded in -p flag)")
		}
		// Copilot args must NOT contain claude-specific flags
		for _, a := range args {
			if a == "--append-system-prompt" || a == "--allowedTools" || a == "--max-turns" {
				t.Errorf("copilot args contain claude-specific flag %q (args: %v)", a, args)
			}
		}
	})
}

func TestBuildProviderPhaseArgsAddsDTUMetadataWhenDTUActive(t *testing.T) {
	t.Setenv("XYLEM_DTU_STATE_PATH", "/tmp/dtu/state.json")

	cfg := &config.Config{
		Claude:  config.ClaudeConfig{Command: "claude"},
		Copilot: config.CopilotConfig{Command: "copilot"},
	}
	phase := &workflow.Phase{
		Name:       "implement",
		PromptFile: ".xylem/prompts/fix-bug/implement.md",
		MaxTurns:   5,
	}

	_, claudeArgs, _ := buildProviderPhaseArgs(cfg, nil, nil, phase, "", "claude", "prompt", 2)
	if !containsArgSequence(claudeArgs, "--dtu-phase", "implement") {
		t.Fatalf("claude args missing DTU phase: %v", claudeArgs)
	}
	if !containsArgSequence(claudeArgs, "--dtu-script", "implement") {
		t.Fatalf("claude args missing DTU script: %v", claudeArgs)
	}
	if !containsArgSequence(claudeArgs, "--dtu-attempt", "2") {
		t.Fatalf("claude args missing DTU attempt: %v", claudeArgs)
	}

	_, copilotArgs, _ := buildProviderPhaseArgs(cfg, nil, nil, phase, "", "copilot", "prompt", 3)
	if !containsArgSequence(copilotArgs, "--dtu-attempt", "3") {
		t.Fatalf("copilot args missing DTU attempt: %v", copilotArgs)
	}
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
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %d: %v", len(args), args)
	}
	// Copilot uses -p <text> (flag+value pair)
	if args[0] != "-p" {
		t.Errorf("expected -p flag, got %q", args[0])
	}
	if args[1] != "Fix the null pointer in handler.go" {
		t.Errorf("expected prompt text as -p value, got %q", args[1])
	}
	// Must NOT contain invalid copilot flags
	for _, a := range args {
		if a == "--headless" {
			t.Errorf("copilot buildCommand should not contain --headless (args: %v)", args)
		}
		if a == "--max-turns" {
			t.Errorf("copilot buildCommand should not contain --max-turns (args: %v)", args)
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
	if args[0] != "-p" {
		t.Errorf("expected -p flag, got %q", args[0])
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
	if args[0] != "-p" {
		t.Errorf("expected -p flag, got %q", args[0])
	}
	if !strings.Contains(args[1], "Ref: https://github.com/owner/repo/issues/99") {
		t.Errorf("expected ref URL in prompt, got %q", args[1])
	}
	if !strings.Contains(args[1], "Fix the null pointer in handler.go") {
		t.Errorf("expected original prompt text, got %q", args[1])
	}
}

func TestBuildPromptOnlyCmdArgsCopilotFlagStripping(t *testing.T) {
	t.Run("legacy --headless stripped and --verbose preserved", func(t *testing.T) {
		cfg := &config.Config{
			LLM:      "copilot",
			MaxTurns: 50,
			Copilot: config.CopilotConfig{
				Command: "copilot",
				Flags:   "--headless --verbose",
			},
		}
		_, args := buildPromptOnlyCmdArgs(cfg, "test prompt")
		// --headless should be fully stripped
		for _, a := range args {
			if a == "--headless" {
				t.Errorf("expected --headless to be stripped, got: %v", args)
			}
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
	})

	t.Run("-p value-aware strip from user flags", func(t *testing.T) {
		cfg := &config.Config{
			LLM:      "copilot",
			MaxTurns: 50,
			Copilot: config.CopilotConfig{
				Command: "copilot",
				Flags:   "-p old-prompt --verbose",
			},
		}
		_, args := buildPromptOnlyCmdArgs(cfg, "new prompt")
		// -p should appear exactly once with "new prompt" value
		pCount := 0
		for _, a := range args {
			if a == "-p" {
				pCount++
			}
		}
		if pCount != 1 {
			t.Errorf("expected exactly 1 -p, got %d (args: %v)", pCount, args)
		}
		// "old-prompt" should be stripped along with -p from user flags
		for _, a := range args {
			if a == "old-prompt" {
				t.Errorf("expected old-prompt to be stripped, got: %v", args)
			}
		}
	})
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
			{output: []byte("tests pass\n"), err: nil}, // gate execution
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

func TestDrainPREventsVessel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	// Enqueue a PR-events vessel (review submitted)
	_, _ = q.Enqueue(queue.Vessel{
		ID:       "pr-10-review-abc123",
		Source:   "github-pr-events",
		Ref:      "https://github.com/owner/repo/pull/10#review-1001",
		Workflow: "fix-bug",
		Meta: map[string]string{
			"pr_num":         "10",
			"event_type":     "review_submitted",
			"pr_head_branch": "feature-branch",
		},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "respond", promptContent: "Respond to review on {{.Issue.Title}}", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	// Mock the gh pr view call that fetchIssueData triggers
	prViewJSON := `{"title":"Test PR","body":"PR body","url":"https://github.com/owner/repo/pull/10","labels":[{"name":"needs-review"}]}`
	cmdRunner := &mockCmdRunner{
		outputData: []byte(prViewJSON),
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-pr-events": &source.GitHubPREvents{Repo: "owner/repo"},
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}

	// Verify that the prompt included the PR title from fetchIssueData
	if len(cmdRunner.phaseCalls) != 1 {
		t.Fatalf("expected 1 phase call, got %d", len(cmdRunner.phaseCalls))
	}
	if !strings.Contains(cmdRunner.phaseCalls[0].prompt, "Test PR") {
		t.Errorf("expected prompt to contain PR title 'Test PR', got: %s", cmdRunner.phaseCalls[0].prompt)
	}
}

func TestResolveRepoNewSources(t *testing.T) {
	r := &Runner{
		Sources: map[string]source.Source{
			"github-pr-events": &source.GitHubPREvents{Repo: "owner/events-repo"},
			"github-merge":     &source.GitHubMerge{Repo: "owner/merge-repo"},
		},
	}

	tests := []struct {
		source string
		want   string
	}{
		{"github-pr-events", "owner/events-repo"},
		{"github-merge", "owner/merge-repo"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		got := r.resolveRepo(queue.Vessel{Source: tt.source})
		if got != tt.want {
			t.Errorf("resolveRepo(%q) = %q, want %q", tt.source, got, tt.want)
		}
	}
}

// --- Orchestrated (parallel) execution tests ---

func TestDrainOrchestratedDiamondWorkflow(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "diamond"))

	// Diamond: analyze -> implement_a + implement_b -> merge
	writeWorkflowFile(t, dir, "diamond", []testPhase{
		{name: "analyze", promptContent: "Analyze issue", maxTurns: 5},
		{name: "implement_a", promptContent: "Implement A: {{.PreviousOutputs.analyze}}", maxTurns: 10, dependsOn: []string{"analyze"}},
		{name: "implement_b", promptContent: "Implement B: {{.PreviousOutputs.analyze}}", maxTurns: 10, dependsOn: []string{"analyze"}},
		{name: "merge", promptContent: "Merge: {{.PreviousOutputs.implement_a}} {{.PreviousOutputs.implement_b}}", maxTurns: 5, dependsOn: []string{"implement_a", "implement_b"}},
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
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d (failed=%d)", result.Completed, result.Failed)
	}

	// All 4 phases should have been executed.
	if len(cmdRunner.phaseCalls) != 4 {
		t.Errorf("expected 4 phase calls, got %d", len(cmdRunner.phaseCalls))
	}

	// Verify output files were written.
	for _, name := range []string{"analyze", "implement_a", "implement_b", "merge"} {
		path := filepath.Join(cfg.StateDir, "phases", "issue-1", name+".output")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected output file for phase %q", name)
		}
	}
}

func TestDrainOrchestratedContextFirewall(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "firewall"))

	// Phase b depends on a, phase c depends on a.
	// Phase c should NOT see b's output in PreviousOutputs.
	writeWorkflowFile(t, dir, "firewall", []testPhase{
		{name: "a", promptContent: "Phase A", maxTurns: 5},
		{name: "b", promptContent: "Phase B with dep: {{.PreviousOutputs.a}}", maxTurns: 5, dependsOn: []string{"a"}},
		{name: "c", promptContent: "Phase C with dep: {{.PreviousOutputs.a}} absent: {{.PreviousOutputs.b}}", maxTurns: 5, dependsOn: []string{"a"}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Phase A": []byte("output-A"),
			"Phase B": []byte("output-B"),
			"Phase C": []byte("output-C"),
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
		t.Errorf("expected 1 completed, got %d (failed=%d)", result.Completed, result.Failed)
	}

	// Check that phase c's prompt was rendered with a's output but not b's.
	// The template {{.PreviousOutputs.b}} should render as empty (context firewall).
	for _, call := range cmdRunner.phaseCalls {
		if strings.Contains(call.prompt, "Phase C") {
			if strings.Contains(call.prompt, "output-B") {
				t.Error("context firewall violated: phase c saw phase b's output")
			}
			// Phase c should see "output-A" from its dependency on a.
			if !strings.Contains(call.prompt, "output-A") {
				t.Error("phase c should see phase a's output via depends_on")
			}
		}
	}
}

func TestDrainOrchestratedPhaseFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fail-test"))

	writeWorkflowFile(t, dir, "fail-test", []testPhase{
		{name: "a", promptContent: "Phase A", maxTurns: 5},
		{name: "b", promptContent: "Phase B", maxTurns: 5, dependsOn: []string{"a"}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseErr: errors.New("claude crashed"),
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
		t.Errorf("expected 1 failed, got failed=%d completed=%d", result.Failed, result.Completed)
	}
	// Phase b should NOT have been called since a failed.
	if len(cmdRunner.phaseCalls) != 1 {
		t.Errorf("expected 1 phase call (only a), got %d", len(cmdRunner.phaseCalls))
	}
}

func TestDrainOrchestratedNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "noop-test"))

	writeWorkflowFile(t, dir, "noop-test", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5, noopMatch: "XYLEM_NOOP"},
		{name: "implement", promptContent: "Implement", maxTurns: 10, dependsOn: []string{"analyze"}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("Nothing to do XYLEM_NOOP"),
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
		t.Errorf("expected 1 completed (noop), got completed=%d failed=%d", result.Completed, result.Failed)
	}
	// Only analyze should have been called (noop stops workflow).
	if len(cmdRunner.phaseCalls) != 1 {
		t.Errorf("expected 1 phase call (noop), got %d", len(cmdRunner.phaseCalls))
	}
}

func TestDrainOrchestratedWithGate(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "gate-test"))

	writeWorkflowFile(t, dir, "gate-test", []testPhase{
		{name: "implement", promptContent: "Implement", maxTurns: 10,
			gate: "      type: command\n      run: \"go test ./...\""},
		{name: "pr", promptContent: "Create PR: {{.PreviousOutputs.implement}}", maxTurns: 5, dependsOn: []string{"implement"}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	// Gate passes (exit code 0 from RunOutput for gate commands).
	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("ok"),
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
		t.Errorf("expected 1 completed, got completed=%d failed=%d", result.Completed, result.Failed)
	}
	// Both phases should have executed.
	if len(cmdRunner.phaseCalls) != 2 {
		t.Errorf("expected 2 phase calls, got %d", len(cmdRunner.phaseCalls))
	}
}

// perPhaseCmdRunner returns per-phase errors based on prompt content.
// Phases whose prompt contains a key in failOn return the corresponding error.
type perPhaseCmdRunner struct {
	mu     sync.Mutex
	failOn map[string]error // prompt substring -> error
	calls  []phaseCall
}

func (m *perPhaseCmdRunner) RunOutput(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return []byte{}, nil
}

func (m *perPhaseCmdRunner) RunProcess(_ context.Context, _ string, _ string, _ ...string) error {
	return nil
}

func (m *perPhaseCmdRunner) RunPhase(_ context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	prompt, _ := io.ReadAll(stdin)
	m.mu.Lock()
	m.calls = append(m.calls, phaseCall{dir: dir, prompt: string(prompt), name: name, args: args})
	m.mu.Unlock()

	for key, err := range m.failOn {
		if bytes.Contains(prompt, []byte(key)) {
			return []byte("failed output"), err
		}
	}
	return []byte("mock output"), nil
}

// TestDrainOrchestratedParallelFailureNoRace verifies that two phases in the
// same wave can both fail without a data race on vessel fields. Run with
// `go test -race` to confirm.
func TestDrainOrchestratedParallelFailureNoRace(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "parallel-fail"))

	// root -> a, root -> b: a and b are in the same wave and run concurrently.
	writeWorkflowFile(t, dir, "parallel-fail", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "a", promptContent: "Phase A", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "b", promptContent: "Phase B", maxTurns: 5, dependsOn: []string{"root"}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	// root succeeds; a and b both fail. Since a and b are in the same wave,
	// they run concurrently and both write to their local vessel copy.
	// Before the fix (vessel passed by pointer), this was a data race.
	cmdRunner := &perPhaseCmdRunner{
		failOn: map[string]error{
			"Phase A": errors.New("phase A crashed"),
			"Phase B": errors.New("phase B crashed"),
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
		t.Errorf("expected 1 failed vessel, got failed=%d completed=%d", result.Failed, result.Completed)
	}
}

func TestDrainPolicyBlocksPhaseBeforeExecution(t *testing.T) {
	tests := []struct {
		name          string
		phase         testPhase
		policy        intermediary.Effect
		wantErrorPart string
		assertBlocked func(t *testing.T, cmdRunner *mockCmdRunner)
	}{
		{
			name:          "deny prompt phase",
			phase:         testPhase{name: "solve", promptContent: "Solve the issue", maxTurns: 5},
			policy:        intermediary.Deny,
			wantErrorPart: "denied by policy",
			assertBlocked: func(t *testing.T, cmdRunner *mockCmdRunner) {
				t.Helper()
				if len(cmdRunner.phaseCalls) != 0 {
					t.Fatalf("len(phaseCalls) = %d, want 0", len(cmdRunner.phaseCalls))
				}
			},
		},
		{
			name:          "require approval command phase",
			phase:         testPhase{name: "deploy", phaseType: "command", run: "echo deploy"},
			policy:        intermediary.RequireApproval,
			wantErrorPart: "automatic approval not yet supported",
			assertBlocked: func(t *testing.T, cmdRunner *mockCmdRunner) {
				t.Helper()
				if got := countRunOutputCalls(cmdRunner, "sh"); got != 0 {
					t.Fatalf("countRunOutputCalls(sh) = %d, want 0", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := makeTestConfig(dir, 2)
			cfg.StateDir = filepath.Join(dir, ".xylem")
			q := queue.New(filepath.Join(dir, "queue.jsonl"))
			_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

			writeWorkflowFile(t, dir, "fix-bug", []testPhase{tt.phase})

			oldWd, _ := os.Getwd()
			os.Chdir(dir)
			defer os.Chdir(oldWd)

			cmdRunner := &mockCmdRunner{}
			wt := &mockWorktree{path: dir}
			r := New(cfg, q, wt, cmdRunner)
			r.Sources = map[string]source.Source{
				"github-issue": makeGitHubSource(),
			}

			auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
			r.AuditLog = auditLog
			r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
				Name:  "block-all",
				Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: tt.policy}},
			}}, auditLog, nil)

			result, err := r.Drain(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Failed != 1 {
				t.Fatalf("expected 1 failed vessel, got failed=%d completed=%d", result.Failed, result.Completed)
			}

			vessels, err := q.List()
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(vessels) != 1 {
				t.Fatalf("len(vessels) = %d, want 1", len(vessels))
			}
			if vessels[0].State != queue.StateFailed {
				t.Fatalf("vessel.State = %q, want %q", vessels[0].State, queue.StateFailed)
			}
			if !strings.Contains(vessels[0].Error, tt.wantErrorPart) {
				t.Fatalf("vessel.Error = %q, want to contain %q", vessels[0].Error, tt.wantErrorPart)
			}

			summary := loadSummary(t, cfg.StateDir, "issue-1")
			if summary.State != "failed" {
				t.Fatalf("summary.State = %q, want failed", summary.State)
			}
			if len(summary.Phases) != 0 {
				t.Fatalf("len(summary.Phases) = %d, want 0", len(summary.Phases))
			}
			if summary.EvidenceManifestPath != "" {
				t.Fatalf("summary.EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
			}

			tt.assertBlocked(t, cmdRunner)

			entries, err := auditLog.Entries()
			if err != nil {
				t.Fatalf("AuditLog.Entries() error = %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("len(entries) = %d, want 1", len(entries))
			}
			if entries[0].Decision != tt.policy {
				t.Fatalf("entry.Decision = %q, want %q", entries[0].Decision, tt.policy)
			}
		})
	}
}

// TestDrainCommandPhaseHighRiskActionRequiresApproval verifies the intermediary
// correctly enforces a RequireApproval policy on git_push when the operator
// explicitly configures one. The default policy now allows git_push for
// autonomous self-healing, so this test uses an explicit restrictive policy
// to preserve enforcement-mechanism coverage.
func TestDrainCommandPhaseHighRiskActionRequiresApproval(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	// Opt into the old restrictive policy via harness.policy.rules.
	cfg.Harness.Policy = config.PolicyConfig{
		Rules: []config.PolicyRuleConfig{
			{Action: "git_push", Resource: "*", Effect: "require_approval"},
			{Action: "*", Resource: "*", Effect: "allow"},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "push-command"))

	writeWorkflowFile(t, dir, "push-command", []testPhase{
		{name: "publish", phaseType: "command", run: "git push origin feature-1"},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, 0, countRunOutputCalls(cmdRunner, "sh"))

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "requires approval")
	assert.Contains(t, vessel.Error, "git_push")

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "external_command", entries[0].Intent.Action)
	assert.Equal(t, intermediary.Allow, entries[0].Decision)
	assert.Equal(t, "git_push", entries[1].Intent.Action)
	assert.Equal(t, intermediary.RequireApproval, entries[1].Decision)
	assert.Equal(t, "feature-1", entries[1].Intent.Resource)
}

// TestDrainPromptPhaseHighRiskActionRequiresApproval verifies the intermediary
// correctly enforces a RequireApproval policy on prompt-phase git_push when
// the operator explicitly configures one. The default policy now allows
// git_push for autonomous self-healing.
func TestDrainPromptPhaseHighRiskActionRequiresApproval(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	// Opt into the old restrictive policy via harness.policy.rules.
	cfg.Harness.Policy = config.PolicyConfig{
		Rules: []config.PolicyRuleConfig{
			{Action: "git_push", Resource: "*", Effect: "require_approval"},
			{Action: "*", Resource: "*", Effect: "allow"},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "pr-phase"))

	writeWorkflowFile(t, dir, "pr-phase", []testPhase{
		{name: "pr", promptContent: "Commit all changes with a clear commit message, push the branch, and create a pull request using gh pr create.", maxTurns: 5},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Len(t, cmdRunner.phaseCalls, 0)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "requires approval")
	assert.Contains(t, vessel.Error, "git_push")

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, "phase_execute", entries[0].Intent.Action)
	assert.Equal(t, intermediary.Allow, entries[0].Decision)
	assert.Equal(t, "git_commit", entries[1].Intent.Action)
	assert.Equal(t, intermediary.Allow, entries[1].Decision)
	assert.Equal(t, "git_push", entries[2].Intent.Action)
	assert.Equal(t, intermediary.RequireApproval, entries[2].Decision)
}

func TestDrainOrchestratedPolicyBlocksSinglePhaseWave(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "orchestrated-policy"))

	writeWorkflowFile(t, dir, "orchestrated-policy", []testPhase{
		{name: "plan", promptContent: "Plan", maxTurns: 5},
		{name: "implement", promptContent: "Implement", maxTurns: 5, dependsOn: []string{"plan"}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{path: dir}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "deny-all",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.Deny}},
	}}, auditLog, nil)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("expected 1 failed vessel, got failed=%d completed=%d", result.Failed, result.Completed)
	}
	if len(cmdRunner.phaseCalls) != 0 {
		t.Fatalf("len(phaseCalls) = %d, want 0", len(cmdRunner.phaseCalls))
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !strings.Contains(vessels[0].Error, "denied by policy") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessels[0].Error, "denied by policy")
	}

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	if summary.State != "failed" {
		t.Fatalf("summary.State = %q, want failed", summary.State)
	}
	if len(summary.Phases) != 0 {
		t.Fatalf("len(summary.Phases) = %d, want 0", len(summary.Phases))
	}
	if summary.EvidenceManifestPath != "" {
		t.Fatalf("summary.EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
	}

	entries, err := auditLog.Entries()
	if err != nil {
		t.Fatalf("AuditLog.Entries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Decision != intermediary.Deny {
		t.Fatalf("entry.Decision = %q, want %q", entries[0].Decision, intermediary.Deny)
	}
}

func TestDrainOrchestratedProtectedSurfaceViolationFails(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "orchestrated-surface"))

	writeWorkflowFile(t, dir, "orchestrated-surface", []testPhase{
		{name: "tamper", phaseType: "command", run: "echo tampered > .xylem.yml"},
		{name: "implement", promptContent: "Implement", maxTurns: 5, dependsOn: []string{"tamper"}},
	})
	if err := os.WriteFile(filepath.Join(dir, ".xylem.yml"), []byte("repo: owner/repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.xylem.yml) error = %v", err)
	}

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name != "sh" || len(args) < 2 {
				return nil, nil, false
			}
			if err := os.WriteFile(filepath.Join(dir, ".xylem.yml"), []byte("tampered: true\n"), 0o644); err != nil {
				return nil, err, true
			}
			return []byte("tampered"), nil, true
		},
	}
	wt := &mockWorktree{path: dir}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary([]intermediary.Policy{{
		Name:  "allow-all",
		Rules: []intermediary.Rule{{Action: "*", Resource: "*", Effect: intermediary.Allow}},
	}}, auditLog, nil)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("expected 1 failed vessel, got failed=%d completed=%d", result.Failed, result.Completed)
	}
	if got := countRunOutputCalls(cmdRunner, "sh"); got != 1 {
		t.Fatalf("countRunOutputCalls(sh) = %d, want 1 command invocation", got)
	}
	if len(cmdRunner.phaseCalls) != 0 {
		t.Fatalf("len(phaseCalls) = %d, want 0 dependent phase invocations", len(cmdRunner.phaseCalls))
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !strings.Contains(vessels[0].Error, "violated protected surfaces") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessels[0].Error, "violated protected surfaces")
	}
	if !strings.Contains(vessels[0].Error, ".xylem.yml") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessels[0].Error, ".xylem.yml")
	}

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	if summary.State != "failed" {
		t.Fatalf("summary.State = %q, want failed", summary.State)
	}
	if len(summary.Phases) != 1 {
		t.Fatalf("len(summary.Phases) = %d, want 1", len(summary.Phases))
	}
	if summary.Phases[0].Name != "tamper" {
		t.Fatalf("summary.Phases[0].Name = %q, want tamper", summary.Phases[0].Name)
	}
	if summary.Phases[0].Status != "failed" {
		t.Fatalf("summary.Phases[0].Status = %q, want failed", summary.Phases[0].Status)
	}
	if summary.EvidenceManifestPath != "" {
		t.Fatalf("summary.EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
	}

	entries, err := auditLog.Entries()
	if err != nil {
		t.Fatalf("AuditLog.Entries() error = %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Intent.Action != "file_write" || entry.Intent.Resource != ".xylem.yml" {
			continue
		}
		found = true
		if entry.Decision != intermediary.Deny {
			t.Fatalf("file_write entry decision = %q, want %q", entry.Decision, intermediary.Deny)
		}
		if !strings.Contains(entry.Error, "violated protected surfaces") {
			t.Fatalf("file_write entry error = %q, want to contain %q", entry.Error, "violated protected surfaces")
		}
		if entry.Intent.Metadata["phase"] != "tamper" {
			t.Fatalf("file_write entry phase metadata = %q, want tamper", entry.Intent.Metadata["phase"])
		}
		if entry.Intent.Metadata["before"] == "" || entry.Intent.Metadata["after"] == "" {
			t.Fatalf("file_write entry missing before/after metadata: %+v", entry.Intent.Metadata)
		}
	}
	if !found {
		t.Fatalf("no file_write audit entry recorded; entries = %+v", entries)
	}
}

// TestVerifyProtectedSurfacesSkipsWhenWorktreeMissing exercises the race
// guard added to verifyProtectedSurfaces. If CheckHungVessels (or any other
// cleanup path) removes the worktree directory between the before-snapshot
// and the after-snapshot, the guard must treat the check as non-observable
// and return nil rather than flag every protected file as "deleted".
//
// Regression: without this guard, 8 false-positive protected_surface_violation
// events were recorded in production on 2026-04-07, each listing every
// protected .xylem/ file as "deleted" despite vessels that had made no
// edits (including one that produced XYLEM_NOOP).
func TestVerifyProtectedSurfacesSkipsWhenWorktreeMissing(t *testing.T) {
	worktreeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".xylem", "workflows"), 0o755); err != nil {
		t.Fatalf("MkdirAll workflows = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".xylem", "prompts", "fix-bug"), 0o755); err != nil {
		t.Fatalf("MkdirAll prompts = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), []byte("repo: owner/repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .xylem.yml = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem", "workflows", "fix-bug.yaml"), []byte("name: fix-bug\n"), 0o644); err != nil {
		t.Fatalf("WriteFile workflow = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem", "prompts", "fix-bug", "analyze.md"), []byte("analyze\n"), 0o644); err != nil {
		t.Fatalf("WriteFile prompt = %v", err)
	}

	stateDir := t.TempDir()
	cfg := makeTestConfig(stateDir, 1)
	cfg.StateDir = stateDir
	auditPath := filepath.Join(stateDir, "audit.jsonl")
	auditLog := intermediary.NewAuditLog(auditPath)
	r := New(cfg, queue.New(filepath.Join(stateDir, "queue.jsonl")), &mockWorktree{path: worktreeDir}, &mockCmdRunner{})
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(worktreeDir)
	if err != nil {
		t.Fatalf("takeProtectedSurfaceSnapshot() error = %v", err)
	}
	if !ok {
		t.Fatalf("takeProtectedSurfaceSnapshot() checkProtectedSurfaces = false, want true")
	}
	if len(before.Files) == 0 {
		t.Fatalf("before snapshot has no files; fixture setup is broken")
	}

	// Simulate the race: something (CheckHungVessels) removed the worktree
	// while the vessel goroutine was still executing a phase.
	if err := os.RemoveAll(worktreeDir); err != nil {
		t.Fatalf("RemoveAll(worktreeDir) = %v", err)
	}

	// Capture log output so we can assert the skip warning fired.
	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldWriter)

	vessel := queue.Vessel{ID: "issue-guard", Source: "github-issue", Workflow: "fix-bug"}
	ph := workflow.Phase{Name: "analyze"}
	if err := r.verifyProtectedSurfaces(vessel, ph, worktreeDir, before); err != nil {
		t.Fatalf("verifyProtectedSurfaces() returned error on missing worktree, want nil: %v", err)
	}

	if !strings.Contains(logBuf.String(), "surface check skipped") {
		t.Fatalf("expected skip warning in log, got: %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "no longer exists") {
		t.Fatalf("expected 'no longer exists' in log, got: %q", logBuf.String())
	}

	entries, err := auditLog.Entries()
	if err != nil {
		t.Fatalf("AuditLog.Entries() = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("AuditLog.Entries() len = %d, want 0; entries = %+v", len(entries), entries)
	}
}

// TestVerifyProtectedSurfacesDetectsLegitimateDeletionWhenWorktreeExists is
// a regression guard for the defensive guard added to verifyProtectedSurfaces:
// ensure that a genuine in-worktree deletion (worktree dir still present,
// protected file gone) is still detected and recorded as a violation.
func TestVerifyProtectedSurfacesDetectsLegitimateDeletionWhenWorktreeExists(t *testing.T) {
	worktreeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".xylem", "workflows"), 0o755); err != nil {
		t.Fatalf("MkdirAll workflows = %v", err)
	}
	protectedFile := filepath.Join(worktreeDir, ".xylem.yml")
	if err := os.WriteFile(protectedFile, []byte("repo: owner/repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .xylem.yml = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem", "workflows", "fix-bug.yaml"), []byte("name: fix-bug\n"), 0o644); err != nil {
		t.Fatalf("WriteFile workflow = %v", err)
	}

	stateDir := t.TempDir()
	cfg := makeTestConfig(stateDir, 1)
	cfg.StateDir = stateDir
	auditPath := filepath.Join(stateDir, "audit.jsonl")
	auditLog := intermediary.NewAuditLog(auditPath)
	r := New(cfg, queue.New(filepath.Join(stateDir, "queue.jsonl")), &mockWorktree{path: worktreeDir}, &mockCmdRunner{})
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(worktreeDir)
	if err != nil {
		t.Fatalf("takeProtectedSurfaceSnapshot() error = %v", err)
	}
	if !ok {
		t.Fatalf("takeProtectedSurfaceSnapshot() checkProtectedSurfaces = false, want true")
	}

	// Simulate a real in-worktree deletion: worktree dir still exists but
	// a protected file has been removed.
	if err := os.Remove(protectedFile); err != nil {
		t.Fatalf("Remove(protectedFile) = %v", err)
	}

	vessel := queue.Vessel{ID: "issue-legit", Source: "github-issue", Workflow: "fix-bug"}
	ph := workflow.Phase{Name: "implement"}
	verifyErr := r.verifyProtectedSurfaces(vessel, ph, worktreeDir, before)
	if verifyErr == nil {
		t.Fatalf("verifyProtectedSurfaces() returned nil, want violation error")
	}
	if !strings.Contains(verifyErr.Error(), "violated protected surfaces") {
		t.Fatalf("verifyProtectedSurfaces() error = %q, want 'violated protected surfaces'", verifyErr)
	}
	if !strings.Contains(verifyErr.Error(), ".xylem.yml") {
		t.Fatalf("verifyProtectedSurfaces() error = %q, want to mention .xylem.yml", verifyErr)
	}
	if !strings.Contains(verifyErr.Error(), "deleted") {
		t.Fatalf("verifyProtectedSurfaces() error = %q, want to mention 'deleted'", verifyErr)
	}

	entries, err := auditLog.Entries()
	if err != nil {
		t.Fatalf("AuditLog.Entries() = %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Intent.Action == "file_write" && e.Intent.Resource == ".xylem.yml" && e.Decision == intermediary.Deny {
			found = true
			if !strings.Contains(e.Error, ".xylem.yml") {
				t.Fatalf("audit entry error = %q, want to mention .xylem.yml", e.Error)
			}
			if e.Intent.Metadata["phase"] != "implement" {
				t.Fatalf("audit entry phase metadata = %q, want implement", e.Intent.Metadata["phase"])
			}
			if e.Intent.Metadata["after"] != "deleted" {
				t.Fatalf("audit entry after metadata = %q, want deleted", e.Intent.Metadata["after"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("no file_write audit entry recorded; entries = %+v", entries)
	}
}

func TestSmoke_WS6_S19_OrchestratedVesselRunStateNoRace(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "race-diamond"))

	writeWorkflowFile(t, dir, "race-diamond", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "left", promptContent: "Left branch {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "right", promptContent: "Right branch {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Root phase":   []byte("root output"),
			"Left branch":  []byte("left output"),
			"Right branch": []byte("right output"),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}
}

func TestSmoke_WS6_S20_SinglePhaseResultHasPhaseSummary(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	promptPath := writePromptFile(t, dir, ".xylem/prompts/direct/plan.md", "Single phase summary")
	wf := &workflow.Workflow{
		Name: "direct",
		Phases: []workflow.Phase{{
			Name:       "plan",
			PromptFile: promptPath,
			MaxTurns:   5,
		}},
	}

	vessel := makeVessel(1, "direct")
	vrs := newVesselRunState(cfg, vessel, time.Now().UTC())
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Single phase summary": []byte("done"),
		},
		runPhaseHook: func(dir, prompt, name string, args ...string) ([]byte, error, bool) {
			time.Sleep(10 * time.Millisecond)
			return nil, nil, false
		},
	}
	r := New(cfg, queue.New(filepath.Join(dir, "queue.jsonl")), &mockWorktree{path: dir}, cmdRunner)

	result := r.runSinglePhase(context.Background(), vessel, wf, 0, map[string]string{}, phase.IssueData{}, "", dir, &source.Manual{}, vrs, true)
	assert.Equal(t, "plan", result.phaseSummary.Name)
	assert.Equal(t, "completed", result.phaseSummary.Status)
	assert.Greater(t, result.phaseSummary.DurationMS, int64(0))
}

func TestSmoke_WS6_S21_EvidenceClaimNilWhenNoGate(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	promptPath := writePromptFile(t, dir, ".xylem/prompts/direct/no-gate.md", "No gate phase")
	wf := &workflow.Workflow{
		Name: "direct-no-gate",
		Phases: []workflow.Phase{{
			Name:       "plan",
			PromptFile: promptPath,
			MaxTurns:   5,
		}},
	}

	vessel := makeVessel(1, "direct-no-gate")
	vrs := newVesselRunState(cfg, vessel, time.Now().UTC())
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"No gate phase": []byte("done"),
		},
	}
	r := New(cfg, queue.New(filepath.Join(dir, "queue.jsonl")), &mockWorktree{path: dir}, cmdRunner)

	result := r.runSinglePhase(context.Background(), vessel, wf, 0, map[string]string{}, phase.IssueData{}, "", dir, &source.Manual{}, vrs, true)
	if result.evidenceClaim != nil {
		t.Fatalf("result.evidenceClaim = %+v, want nil", result.evidenceClaim)
	}
}

func TestSmoke_WS6_S22_WaveResultsMergedAfterWgWait(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "wave-merge"))

	writeWorkflowFile(t, dir, "wave-merge", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "a", promptContent: "Branch A {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "b", promptContent: "Branch B {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})

	preseedDir := filepath.Join(cfg.StateDir, "phases", "issue-1")
	if err := os.MkdirAll(preseedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(preseedDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(preseedDir, "root.output"), []byte("root output"), 0o644); err != nil {
		t.Fatalf("WriteFile(root.output) error = %v", err)
	}

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Branch A": []byte("left output"),
			"Branch B": []byte("right output"),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	require.Len(t, cmdRunner.phaseCalls, 2)

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	require.Len(t, summary.Phases, 2)
	assert.Equal(t, []string{"a", "b"}, []string{summary.Phases[0].Name, summary.Phases[1].Name})
	for _, phaseSummary := range summary.Phases {
		assert.Equal(t, "completed", phaseSummary.Status)
	}
}

func TestSmoke_WS6_S23_CostTrackerConcurrentAccessSafe(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 10.0, 10000)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "wave-cost"))

	writeWorkflowFile(t, dir, "wave-cost", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "a", promptContent: "Cost A {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "b", promptContent: "Cost B {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})

	preseedDir := filepath.Join(cfg.StateDir, "phases", "issue-1")
	if err := os.MkdirAll(preseedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(preseedDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(preseedDir, "root.output"), []byte("root output"), 0o644); err != nil {
		t.Fatalf("WriteFile(root.output) error = %v", err)
	}

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Cost A": []byte(strings.Repeat("a", 600)),
			"Cost B": []byte(strings.Repeat("b", 600)),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	if len(summary.Phases) != 2 {
		t.Fatalf("len(summary.Phases) = %d, want 2", len(summary.Phases))
	}
	sumPhaseTokens := 0
	for _, phaseSummary := range summary.Phases {
		sumPhaseTokens += phaseSummary.InputTokensEst + phaseSummary.OutputTokensEst
	}
	if summary.TotalTokensEst != sumPhaseTokens {
		t.Fatalf("summary.TotalTokensEst = %d, want %d", summary.TotalTokensEst, sumPhaseTokens)
	}
}

func TestSmoke_WS6_S24_VesselSpanContextPropagatedToGoroutines(t *testing.T) {
	tracer, rec := newTestTracer(t)
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "trace-diamond"))

	writeWorkflowFile(t, dir, "trace-diamond", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "left", promptContent: "Trace left {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "right", promptContent: "Trace right {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Root phase":  []byte("root output"),
			"Trace left":  []byte("left output"),
			"Trace right": []byte("right output"),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	r.Tracer = tracer

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	for _, phaseName := range []string{"phase:root", "phase:left", "phase:right"} {
		phaseSpan := endedSpanByName(t, rec, phaseName)
		assert.Equal(t, vesselSpan.SpanContext().TraceID(), phaseSpan.Parent().TraceID(), phaseName)
		assert.Equal(t, vesselSpan.SpanContext().SpanID(), phaseSpan.Parent().SpanID(), phaseName)
	}
}

func TestSmoke_WS6_S25_ConcurrentPhasesAllowOverspend(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 10.0, 150)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "concurrent-budget"))

	writeWorkflowFile(t, dir, "concurrent-budget", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "a", promptContent: "Concurrent A {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "b", promptContent: "Concurrent B {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})

	preseedDir := filepath.Join(cfg.StateDir, "phases", "issue-1")
	if err := os.MkdirAll(preseedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(preseedDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(preseedDir, "root.output"), []byte("root output"), 0o644); err != nil {
		t.Fatalf("WriteFile(root.output) error = %v", err)
	}

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Concurrent A": []byte(strings.Repeat("a", 400)),
			"Concurrent B": []byte(strings.Repeat("b", 400)),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	assert.Equal(t, "completed", summary.State)
	assert.True(t, summary.BudgetExceeded)
	assert.Greater(t, summary.TotalTokensEst, 150)
	assert.Positive(t, summary.TotalCostUSDEst)
}

// --- P1-2: Command phase template validation ---

func TestValidateCommandRender_UnresolvedTemplate(t *testing.T) {
	err := validateCommandRender("merge", "gh pr merge {{.Issue.Number}} --repo owner/repo")
	if err == nil {
		t.Fatal("expected error for unresolved template variable, got nil")
	}
	if !strings.Contains(err.Error(), "unresolved template variable") {
		t.Errorf("unexpected error message: %v", err)
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Errorf("expected phase name in error, got: %v", err)
	}
}

func TestValidateCommandRender_Resolved(t *testing.T) {
	err := validateCommandRender("merge", "gh pr merge 42 --repo owner/repo")
	if err != nil {
		t.Fatalf("unexpected error for resolved command: %v", err)
	}
}

func TestValidateIssueDataForWorkflow_CommandPhaseZeroNumber(t *testing.T) {
	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
	}
	data := phase.IssueData{Number: 0}
	wf := &workflow.Workflow{
		Phases: []workflow.Phase{
			{Name: "merge", Type: "command", Run: "gh pr merge {{.Issue.Number}} --repo owner/repo"},
		},
	}
	err := validateIssueDataForWorkflow(vessel, data, wf)
	if err == nil {
		t.Fatal("expected error for command phase with zero issue number, got nil")
	}
	if !strings.Contains(err.Error(), "Number is 0") {
		t.Errorf("unexpected error message: %v", err)
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Errorf("expected phase name in error: %v", err)
	}
}

func TestValidateIssueDataForWorkflow_CommandPhaseValidNumber(t *testing.T) {
	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
	}
	data := phase.IssueData{Number: 42}
	wf := &workflow.Workflow{
		Phases: []workflow.Phase{
			{Name: "merge", Type: "command", Run: "gh pr merge {{.Issue.Number}} --repo owner/repo"},
		},
	}
	if err := validateIssueDataForWorkflow(vessel, data, wf); err != nil {
		t.Fatalf("unexpected error for valid issue number: %v", err)
	}
}

func TestValidateIssueDataForWorkflow_NonCommandPhase(t *testing.T) {
	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
	}
	data := phase.IssueData{Number: 0}
	wf := &workflow.Workflow{
		Phases: []workflow.Phase{
			{Name: "analyze", Type: "", Run: ""},
		},
	}
	if err := validateIssueDataForWorkflow(vessel, data, wf); err != nil {
		t.Fatalf("non-command phase should not trigger validation: %v", err)
	}
}

func TestValidateIssueDataForWorkflow_CommandPhaseWithoutIssueRef(t *testing.T) {
	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
	}
	data := phase.IssueData{Number: 0}
	wf := &workflow.Workflow{
		Phases: []workflow.Phase{
			{Name: "build", Type: "command", Run: "make build"},
		},
	}
	if err := validateIssueDataForWorkflow(vessel, data, wf); err != nil {
		t.Fatalf("command without .Issue. reference should not trigger: %v", err)
	}
}

func TestValidateIssueDataForWorkflow_NilWorkflow(t *testing.T) {
	vessel := queue.Vessel{ID: "issue-1", Source: "github-issue"}
	data := phase.IssueData{Number: 0}
	if err := validateIssueDataForWorkflow(vessel, data, nil); err != nil {
		t.Fatalf("nil workflow should not trigger: %v", err)
	}
}

func TestDrainCommandPhaseTemplateValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	// Command phase using {{.Issue.Number}} — the mock won't fetch real data,
	// so Number will be 0 and the issue data validation should catch it.
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "merge", phaseType: "command", run: "gh pr merge {{.Issue.Number}} --repo owner/repo"},
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
	// Should fail because issue data Number is 0 (mock doesn't fetch real data)
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got failed=%d completed=%d", result.Failed, result.Completed)
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateFailed {
		t.Errorf("expected vessel failed, got %s", vessels[0].State)
	}
	if !strings.Contains(vessels[0].Error, "Number is 0") {
		t.Errorf("expected error about zero issue number, got: %s", vessels[0].Error)
	}
}

// --- P1-3: Hung vessel timeout ---

func TestCheckHungVessels_TimesOut(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.Timeout = "1s" // very short timeout

	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	// Enqueue and dequeue to get a running vessel
	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "hung-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	vessel, _ := q.Dequeue() // transitions to running, sets StartedAt
	if vessel == nil {
		t.Fatal("expected dequeued vessel")
		return
	}

	// Backdate StartedAt to simulate a hung vessel
	old := now.Add(-5 * time.Minute)
	vessel.StartedAt = &old
	if err := q.UpdateVessel(*vessel); err != nil {
		t.Fatalf("update vessel: %v", err)
	}

	wt := &mockWorktree{}
	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, wt, cmdRunner)

	r.CheckHungVessels(context.Background())

	vessels, _ := q.List()
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].State != queue.StateTimedOut {
		t.Errorf("expected vessel timed_out, got %s", vessels[0].State)
	}
	if !strings.Contains(vessels[0].Error, "vessel timed out after") {
		t.Errorf("expected timeout error message, got: %s", vessels[0].Error)
	}
}

func TestCheckHungVessels_NotTimedOut(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.Timeout = "1h" // long timeout

	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "ok-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	vessel, _ := q.Dequeue()
	if vessel == nil {
		t.Fatal("expected dequeued vessel")
		return
	}

	wt := &mockWorktree{}
	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, wt, cmdRunner)

	r.CheckHungVessels(context.Background())

	vessels, _ := q.List()
	if vessels[0].State != queue.StateRunning {
		t.Errorf("expected vessel still running, got %s", vessels[0].State)
	}
}

func TestCheckHungVessels_CleansUpWorktree(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.Timeout = "1s"

	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:           "hung-wt-1",
		Source:       "manual",
		State:        queue.StatePending,
		CreatedAt:    now,
		WorktreePath: "/tmp/some-worktree",
	})
	vessel, _ := q.Dequeue()
	if vessel == nil {
		t.Fatal("expected dequeued vessel")
		return
	}
	// Preserve worktree path after dequeue
	vessel.WorktreePath = "/tmp/some-worktree"
	old := now.Add(-5 * time.Minute)
	vessel.StartedAt = &old
	if err := q.UpdateVessel(*vessel); err != nil {
		t.Fatalf("update vessel: %v", err)
	}

	wt := &mockWorktree{}
	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, wt, cmdRunner)

	r.CheckHungVessels(context.Background())

	wt.mu.Lock()
	defer wt.mu.Unlock()
	if !wt.removeCalled {
		t.Error("expected worktree cleanup for timed out vessel")
	}
	if wt.removePath != "/tmp/some-worktree" {
		t.Errorf("expected worktree path /tmp/some-worktree, got %s", wt.removePath)
	}
}

func TestSmoke_S9_NilTracerNoPanic(t *testing.T) {
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, nil)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", result.Completed)
	}
}

func TestSmoke_S10_DrainRunSpanCreated(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", result.Completed)
	}

	_ = endedSpanByName(t, rec, "drain_run")
}

func TestSmoke_S11_VesselSpanChildOfDrainRun(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", result.Completed)
	}

	drainSpan := endedSpanByName(t, rec, "drain_run")
	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	if vesselSpan.Parent().TraceID() != drainSpan.SpanContext().TraceID() {
		t.Fatalf("vessel trace ID = %s, want %s", vesselSpan.Parent().TraceID(), drainSpan.SpanContext().TraceID())
	}
	if vesselSpan.Parent().SpanID() != drainSpan.SpanContext().SpanID() {
		t.Fatalf("vessel parent span ID = %s, want %s", vesselSpan.Parent().SpanID(), drainSpan.SpanContext().SpanID())
	}
}

func TestSmoke_S12_PhaseSpanChildOfVessel(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", result.Completed)
	}

	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	if phaseSpan.Parent().SpanID() != vesselSpan.SpanContext().SpanID() {
		t.Fatalf("phase parent span ID = %s, want %s", phaseSpan.Parent().SpanID(), vesselSpan.SpanContext().SpanID())
	}
}

func TestSmoke_S13_GateSpanChildOfPhase(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
		gateOutput: []byte("ok"),
	}
	r := newWorkflowRunner(t, "trace-gate", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"go test ./...\"",
		},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", result.Completed)
	}

	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	gateSpan := endedSpanByName(t, rec, "gate:command")
	if gateSpan.Parent().SpanID() != phaseSpan.SpanContext().SpanID() {
		t.Fatalf("gate parent span ID = %s, want %s", gateSpan.Parent().SpanID(), phaseSpan.SpanContext().SpanID())
	}
}

func TestSmoke_S14_PhaseResultAttributesPresent(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", result.Completed)
	}

	attrs := spanAttrMap(endedSpanByName(t, rec, "phase:analyze"))
	if attrs["xylem.phase.input_tokens_est"] == "" {
		t.Fatal("xylem.phase.input_tokens_est missing")
	}
	if attrs["xylem.phase.duration_ms"] == "" {
		t.Fatal("xylem.phase.duration_ms missing")
	}
}

func TestSmoke_S15_PhaseErrorRecordedOnSpan(t *testing.T) {
	tracer, rec := newTestTracer(t)
	runErr := errors.New("provider crashed")
	cmdRunner := &mockCmdRunner{
		phaseErr: runErr,
	}
	r := newWorkflowRunner(t, "trace-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", result.Failed)
	}

	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	if !spanHasExceptionEvent(phaseSpan, runErr.Error()) {
		t.Fatalf("phase span missing exception event for %q", runErr.Error())
	}
	if phaseSpan.Status().Code != codes.Error {
		t.Fatalf("phase span status code = %v, want %v", phaseSpan.Status().Code, codes.Error)
	}
}

func TestSmoke_S16_PhaseSpanAlwaysEnds(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseErr: errors.New("provider crashed"),
	}
	r := newWorkflowRunner(t, "trace-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", result.Failed)
	}
	if len(endedSpansByName(rec, "phase:analyze")) == 0 {
		t.Fatal("phase span did not end")
	}
}

func TestSmoke_WS6S10_PhaseSpanAlwaysEndedOnFailure(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseErr: errors.New("orchestrated crash"),
	}
	r := newWorkflowRunner(t, "trace-orchestrated-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
		{name: "implement", promptContent: "Implement {{.PreviousOutputs.analyze}}", maxTurns: 5, dependsOn: []string{"analyze"}},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", result.Failed)
	}
	if len(cmdRunner.phaseCalls) != 1 {
		t.Fatalf("phaseCalls = %d, want 1", len(cmdRunner.phaseCalls))
	}
	if len(endedSpansByName(rec, "phase:analyze")) == 0 {
		t.Fatal("orchestrated phase span did not end")
	}
}

func TestSmoke_WS6S11_ErrorRecordedBeforeEnd(t *testing.T) {
	tracer, rec := newTestTracer(t)
	runErr := errors.New("orchestrated crash")
	cmdRunner := &mockCmdRunner{
		phaseErr: runErr,
	}
	r := newWorkflowRunner(t, "trace-orchestrated-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
		{name: "implement", promptContent: "Implement {{.PreviousOutputs.analyze}}", maxTurns: 5, dependsOn: []string{"analyze"}},
	}, cmdRunner, tracer)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", result.Failed)
	}

	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	if !spanHasExceptionEvent(phaseSpan, runErr.Error()) {
		t.Fatalf("phase span missing exception event for %q", runErr.Error())
	}
	if phaseSpan.EndTime().IsZero() {
		t.Fatal("phase span end time not set")
	}
}

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"generic error", errors.New("exit status 1"), false},
		{"anthropic rate limit error", errors.New(`API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`), true},
		{"rate_limit_error substring", errors.New("rate_limit_error"), true},
		{"unrelated 429 in message", errors.New("processed 429 items"), false},
		// New patterns covering Copilot/OpenAI and generic HTTP 429
		{"anthropic credit balance", errors.New("Credit balance is too low"), true},
		{"copilot insufficient quota", errors.New("error: insufficient_quota on model gpt-5.4"), true},
		{"openai too many requests", errors.New("openai: Too Many Requests (429)"), true},
		{"http status 429", errors.New("http error: status 429 returned"), true},
		{"hyphenated rate-limit", errors.New("openai: rate-limit exceeded"), true},
		{"api error 429 prefix", errors.New("API Error: 429 retry after 30s"), true},
		{"unrelated insufficient word", errors.New("insufficient disk space"), false},
		{"plain rate in unrelated context", errors.New("update rate set to 5"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimitError(tt.err); got != tt.want {
				t.Errorf("isRateLimitError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// setupDTUClock creates a valid DTU state file so runtimeSleep advances a
// virtual clock instead of sleeping for real. Returns the state file path.
func setupDTUClock(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	stateDir := filepath.Join(base, "dtu", "test-universe")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create DTU state dir: %v", err)
	}
	statePath := filepath.Join(stateDir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"universe_id":"test-universe","version":"v1","metadata":{"name":"test"},"clock":{}}`), 0o644); err != nil {
		t.Fatalf("write DTU state: %v", err)
	}
	return statePath
}

func TestDrainRateLimitRetrySucceeds(t *testing.T) {
	t.Setenv("XYLEM_DTU_STATE_PATH", setupDTUClock(t))

	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "implement", promptContent: "Fix the bug", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	var callCount int32
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(dir, prompt, name string, args ...string) ([]byte, error, bool) {
			n := atomic.AddInt32(&callCount, 1)
			if n <= 2 {
				return nil, errors.New(`API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`), true
			}
			return []byte("success output"), nil, true
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("Completed = %d, want 1", result.Completed)
	}
	if got := atomic.LoadInt32(&callCount); got != 3 {
		t.Errorf("phase calls = %d, want 3 (1 initial + 2 retries then success)", got)
	}
}

func TestDrainRateLimitRetryExhausted(t *testing.T) {
	t.Setenv("XYLEM_DTU_STATE_PATH", setupDTUClock(t))

	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "implement", promptContent: "Fix the bug", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	rateLimitErr := errors.New(`API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
	var callCount int32
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(dir, prompt, name string, args ...string) ([]byte, error, bool) {
			atomic.AddInt32(&callCount, 1)
			return nil, rateLimitErr, true
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("Failed = %d, want 1", result.Failed)
	}
	if got := atomic.LoadInt32(&callCount); got != 4 {
		t.Errorf("phase calls = %d, want 4 (1 initial + 3 retries)", got)
	}

	vessels, _ := q.List()
	if vessels[0].State != queue.StateFailed {
		t.Errorf("vessel state = %s, want failed", vessels[0].State)
	}
	if !strings.Contains(vessels[0].Error, "rate_limit_error") {
		t.Errorf("vessel error = %q, want to contain rate_limit_error", vessels[0].Error)
	}
}

func TestDrainNonRateLimitErrorNotRetried(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "implement", promptContent: "Fix the bug", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	var callCount int32
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(dir, prompt, name string, args ...string) ([]byte, error, bool) {
			atomic.AddInt32(&callCount, 1)
			return nil, errors.New("exit status 1"), true
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("Failed = %d, want 1", result.Failed)
	}
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("phase calls = %d, want 1 (no retry for non-rate-limit error)", got)
	}
}

func TestDrainPromptOnlyRateLimitRetry(t *testing.T) {
	t.Setenv("XYLEM_DTU_STATE_PATH", setupDTUClock(t))

	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "Fix the null pointer"))

	var callCount int32
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(dir, prompt, name string, args ...string) ([]byte, error, bool) {
			n := atomic.AddInt32(&callCount, 1)
			if n <= 1 {
				return nil, errors.New(`API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`), true
			}
			return []byte("fixed"), nil, true
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("Completed = %d, want 1", result.Completed)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("phase calls = %d, want 2 (1 rate limit + 1 success)", got)
	}
}

// --- Per-source timeout ---

func TestCheckHungVessels_PerSourceTimeout(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.Timeout = "1s" // short global timeout

	// Add a slow source with a long timeout
	cfg.Sources["slow-source"] = config.SourceConfig{
		Type:    "github",
		Repo:    "owner/repo",
		Timeout: "1h",
		Tasks:   map[string]config.Task{"impl": {Labels: []string{"implement"}, Workflow: "implement-feature"}},
	}

	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "slow-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
		Meta:      map[string]string{"config_source": "slow-source"},
	})
	vessel, _ := q.Dequeue()
	if vessel == nil {
		t.Fatal("expected dequeued vessel")
		return
	}

	// Backdate StartedAt by 5 minutes — exceeds global 1s but not source 1h
	old := now.Add(-5 * time.Minute)
	vessel.StartedAt = &old
	if err := q.UpdateVessel(*vessel); err != nil {
		t.Fatalf("update vessel: %v", err)
	}

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	r.CheckHungVessels(context.Background())

	vessels, _ := q.List()
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].State != queue.StateRunning {
		t.Errorf("expected vessel still running (source timeout 1h), got %s", vessels[0].State)
	}
}

func TestCheckHungVessels_PerSourceTimeoutExpired(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.Timeout = "1h" // long global timeout

	// Add a fast source with a short timeout
	cfg.Sources["fast-source"] = config.SourceConfig{
		Type:    "github",
		Repo:    "owner/repo",
		Timeout: "1s",
		Tasks:   map[string]config.Task{"impl": {Labels: []string{"implement"}, Workflow: "implement-feature"}},
	}

	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "fast-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
		Meta:      map[string]string{"config_source": "fast-source"},
	})
	vessel, _ := q.Dequeue()
	if vessel == nil {
		t.Fatal("expected dequeued vessel")
		return
	}

	// Backdate StartedAt by 5 minutes — exceeds source 1s
	old := now.Add(-5 * time.Minute)
	vessel.StartedAt = &old
	if err := q.UpdateVessel(*vessel); err != nil {
		t.Fatalf("update vessel: %v", err)
	}

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	r.CheckHungVessels(context.Background())

	vessels, _ := q.List()
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].State != queue.StateTimedOut {
		t.Errorf("expected vessel timed_out (source timeout 1s), got %s", vessels[0].State)
	}
}

func TestResolveTimeout(t *testing.T) {
	cfg := &config.Config{Timeout: "30m"}

	tests := []struct {
		name    string
		srcCfg  *config.SourceConfig
		want    time.Duration
		wantErr bool
	}{
		{name: "nil source config uses global timeout", want: 30 * time.Minute},
		{name: "empty source timeout uses global timeout", srcCfg: &config.SourceConfig{}, want: 30 * time.Minute},
		{name: "source timeout overrides global timeout", srcCfg: &config.SourceConfig{Timeout: "2h"}, want: 2 * time.Hour},
		{name: "invalid source timeout returns error", srcCfg: &config.SourceConfig{Timeout: "not-a-duration"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTimeout(cfg, tt.srcCfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTimeout() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveTimeout() = %s, want %s", got, tt.want)
			}
		})
	}
}
