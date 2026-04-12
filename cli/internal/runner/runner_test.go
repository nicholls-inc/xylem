package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/gate"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/policy"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
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
	phaseCalls    []phaseCall
	outputArgs    [][]string
	commentBodies []string
	lastBody      string
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
	isIssueComment := name == "gh" && len(args) >= 3 && args[0] == "issue" && args[1] == "comment"
	for i, arg := range args {
		if arg == "--body" && i+1 < len(args) {
			body := args[i+1]
			m.lastBody = body
			if isIssueComment {
				m.commentBodies = append(m.commentBodies, body)
			}
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

func (m *mockCmdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return m.RunOutput(ctx, name, args...)
}

func (m *mockCmdRunner) RunProcess(_ context.Context, _ string, _ string, _ ...string) error {
	atomic.AddInt32(&m.started, 1)
	return m.processErr
}

func (m *mockCmdRunner) RunPhase(_ context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return m.RunPhaseWithEnv(context.Background(), dir, nil, stdin, name, args...)
}

func (m *mockCmdRunner) RunPhaseWithEnv(_ context.Context, dir string, _ []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
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
	return c.RunPhaseWithEnv(context.Background(), "", nil, stdin, "")
}

func (c *countingCmdRunner) RunPhaseWithEnv(_ context.Context, _ string, _ []string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
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

type classCountingCmdRunner struct {
	mu             sync.Mutex
	currentByClass map[string]int
	maxByClass     map[string]int
	globalCurrent  int
	globalMax      int
	delay          time.Duration
}

func (c *classCountingCmdRunner) RunOutput(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return []byte{}, nil
}

func (c *classCountingCmdRunner) RunProcess(_ context.Context, _ string, _ string, _ ...string) error {
	return nil
}

func (c *classCountingCmdRunner) RunPhase(_ context.Context, _ string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	return c.RunPhaseWithEnv(context.Background(), "", nil, stdin, "")
}

func (c *classCountingCmdRunner) RunPhaseWithEnv(_ context.Context, _ string, _ []string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	prompt, _ := io.ReadAll(stdin)
	class := "unknown"
	switch {
	case bytes.Contains(prompt, []byte("implement-feature")):
		class = "implement-feature"
	case bytes.Contains(prompt, []byte("merge-pr")):
		class = "merge-pr"
	}

	c.mu.Lock()
	if c.currentByClass == nil {
		c.currentByClass = make(map[string]int)
	}
	if c.maxByClass == nil {
		c.maxByClass = make(map[string]int)
	}
	c.currentByClass[class]++
	if c.currentByClass[class] > c.maxByClass[class] {
		c.maxByClass[class] = c.currentByClass[class]
	}
	c.globalCurrent++
	if c.globalCurrent > c.globalMax {
		c.globalMax = c.globalCurrent
	}
	c.mu.Unlock()

	if c.delay > 0 {
		time.Sleep(c.delay)
	}

	c.mu.Lock()
	c.currentByClass[class]--
	c.globalCurrent--
	c.mu.Unlock()
	return []byte("mock output"), nil
}

type blockingPhaseCmdRunner struct {
	mu            sync.Mutex
	phaseCalls    []string
	resolveStart  chan struct{}
	resolveExit   chan struct{}
	pushStarted   atomic.Int32
	resolveOnce   sync.Once
	resolveExitMu sync.Once
}

func newBlockingPhaseCmdRunner() *blockingPhaseCmdRunner {
	return &blockingPhaseCmdRunner{
		resolveStart: make(chan struct{}),
		resolveExit:  make(chan struct{}),
	}
}

func (b *blockingPhaseCmdRunner) RunOutput(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return []byte{}, nil
}

func (b *blockingPhaseCmdRunner) RunProcess(_ context.Context, _ string, _ string, _ ...string) error {
	return nil
}

func (b *blockingPhaseCmdRunner) RunPhase(ctx context.Context, _ string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	return b.RunPhaseWithEnv(ctx, "", nil, stdin, "")
}

func (b *blockingPhaseCmdRunner) RunPhaseWithEnv(ctx context.Context, _ string, _ []string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	prompt, _ := io.ReadAll(stdin)
	promptText := string(prompt)

	b.mu.Lock()
	b.phaseCalls = append(b.phaseCalls, promptText)
	b.mu.Unlock()

	switch {
	case strings.Contains(promptText, "Analyze conflicts"):
		return []byte("analysis complete"), nil
	case strings.Contains(promptText, "Resolve conflicts"):
		b.resolveOnce.Do(func() { close(b.resolveStart) })
		<-ctx.Done()
		b.resolveExitMu.Do(func() { close(b.resolveExit) })
		return nil, ctx.Err()
	case strings.Contains(promptText, "Push branch"):
		b.pushStarted.Add(1)
		return []byte("push complete"), nil
	default:
		return []byte("mock output"), nil
	}
}

type observedBlockingPhaseCmdRunner struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func newObservedBlockingPhaseCmdRunner() *observedBlockingPhaseCmdRunner {
	return &observedBlockingPhaseCmdRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *observedBlockingPhaseCmdRunner) RunOutput(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return []byte{}, nil
}

func (b *observedBlockingPhaseCmdRunner) RunProcess(_ context.Context, _ string, _ string, _ ...string) error {
	return nil
}

func (b *observedBlockingPhaseCmdRunner) RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return b.RunPhaseObserved(ctx, dir, stdin, nil, name, args...)
}

func (b *observedBlockingPhaseCmdRunner) RunPhaseWithEnv(ctx context.Context, dir string, _ []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return b.RunPhaseObserved(ctx, dir, stdin, nil, name, args...)
}

func (b *observedBlockingPhaseCmdRunner) RunPhaseObserved(ctx context.Context, _ string, stdin io.Reader, observer PhaseProcessObserver, _ string, _ ...string) ([]byte, error) {
	if _, err := io.ReadAll(stdin); err != nil {
		return nil, err
	}
	if observer != nil {
		pid := os.Getpid()
		observer.ProcessStarted(pid)
		defer observer.ProcessExited(pid)
	}
	b.startOnce.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return []byte("observed output"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *observedBlockingPhaseCmdRunner) RunPhaseObservedWithEnv(ctx context.Context, dir string, _ []string, stdin io.Reader, observer PhaseProcessObserver, name string, args ...string) ([]byte, error) {
	return b.RunPhaseObserved(ctx, dir, stdin, observer, name, args...)
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
	lastBranch  string
	path        string
	createCalls int
}

func (tw *trackingWorktree) Create(_ context.Context, branchName string) (string, error) {
	tw.createCalls++
	tw.lastBranch = branchName
	if tw.path != "" {
		return tw.path, nil
	}
	return ".claude/worktrees/" + branchName, nil
}

func (tw *trackingWorktree) Remove(_ context.Context, _ string) error {
	return nil
}

type recordingSource struct {
	startCalls    atomic.Int32
	completeCalls atomic.Int32
	failCalls     atomic.Int32
}

func (s *recordingSource) Name() string { return "github-issue" }

func (s *recordingSource) Scan(context.Context) ([]queue.Vessel, error) { return nil, nil }

func (s *recordingSource) OnEnqueue(context.Context, queue.Vessel) error { return nil }

func (s *recordingSource) OnStart(context.Context, queue.Vessel) error {
	s.startCalls.Add(1)
	return nil
}

func (s *recordingSource) OnWait(context.Context, queue.Vessel) error { return nil }

func (s *recordingSource) OnResume(context.Context, queue.Vessel) error { return nil }

func (s *recordingSource) OnComplete(context.Context, queue.Vessel) error {
	s.completeCalls.Add(1)
	return nil
}

func (s *recordingSource) OnFail(context.Context, queue.Vessel) error {
	s.failCalls.Add(1)
	return nil
}

func (s *recordingSource) OnTimedOut(context.Context, queue.Vessel) error { return nil }

func (s *recordingSource) RemoveRunningLabel(context.Context, queue.Vessel) error { return nil }

func (s *recordingSource) BranchName(vessel queue.Vessel) string {
	return "task/" + vessel.ID
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
		// Explicit protected surfaces because the package default is now
		// empty (to support xylem's self-improving use case — see PR
		// loop11/#194). Tests in this package exercise the verifier and
		// rely on non-empty protection patterns to trigger violations.
		Harness: config.HarnessConfig{
			ProtectedSurfaces: config.ProtectedSurfacesConfig{
				Paths: []string{
					".xylem/HARNESS.md",
					".xylem.yml",
					".xylem/workflows/*.yaml",
					".xylem/prompts/*/*.md",
				},
			},
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

func hasRunOutputCallContaining(m *mockCmdRunner, fragments ...string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, args := range m.outputArgs {
		joined := strings.Join(args, " ")
		match := true
		for _, fragment := range fragments {
			if !strings.Contains(joined, fragment) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func issueCommentBodies(m *mockCmdRunner) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.commentBodies...)
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

type testWorkflowOptions struct {
	class                         string
	allowAdditiveProtectedWrites  bool
	allowCanonicalProtectedWrites bool
}

// writeWorkflowFile creates a workflow YAML and its prompt files in the given dir.
func writeWorkflowFile(t *testing.T, dir, name string, phases []testPhase) {
	t.Helper()
	writeWorkflowFileWithOptions(t, dir, name, testWorkflowOptions{}, phases)
}

func writeWorkflowFileWithOptions(t *testing.T, dir, name string, opts testWorkflowOptions, phases []testPhase) {
	t.Helper()
	workflowDir := filepath.Join(dir, ".xylem", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0o755))

	var phaseYAML strings.Builder
	for _, p := range phases {
		fmt.Fprintf(&phaseYAML, "  - name: %s\n", p.name)

		if p.phaseType == "command" {
			phaseYAML.WriteString("    type: command\n")
			fmt.Fprintf(&phaseYAML, "    run: %q\n", p.run)
		} else {
			promptPath := filepath.Join(dir, ".xylem", "prompts", name, p.name+".md")
			require.NoError(t, os.MkdirAll(filepath.Dir(promptPath), 0o755))
			require.NoError(t, os.WriteFile(promptPath, []byte(p.promptContent), 0o644))

			fmt.Fprintf(&phaseYAML, "    prompt_file: %s\n", promptPath)
			fmt.Fprintf(&phaseYAML, "    max_turns: %d\n", p.maxTurns)
			if p.evaluatorPromptContent != "" {
				evaluatorPromptPath := filepath.Join(dir, ".xylem", "prompts", name, p.name+".evaluator.md")
				require.NoError(t, os.MkdirAll(filepath.Dir(evaluatorPromptPath), 0o755))
				require.NoError(t, os.WriteFile(evaluatorPromptPath, []byte(p.evaluatorPromptContent), 0o644))

				phaseYAML.WriteString("    evaluator:\n")
				fmt.Fprintf(&phaseYAML, "      prompt_file: %s\n", evaluatorPromptPath)
				fmt.Fprintf(&phaseYAML, "      max_turns: %d\n", p.evaluatorMaxTurns)
				if p.evaluatorMaxIterations > 0 {
					fmt.Fprintf(&phaseYAML, "      max_iterations: %d\n", p.evaluatorMaxIterations)
				}
				if p.evaluatorPassThreshold > 0 {
					fmt.Fprintf(&phaseYAML, "      pass_threshold: %.2f\n", p.evaluatorPassThreshold)
				}
				if len(p.evaluatorCriteria) > 0 {
					phaseYAML.WriteString("      criteria:\n")
					for _, criterion := range p.evaluatorCriteria {
						fmt.Fprintf(&phaseYAML, "        - name: %q\n", criterion.Name)
						if criterion.Description != "" {
							fmt.Fprintf(&phaseYAML, "          description: %q\n", criterion.Description)
						}
						fmt.Fprintf(&phaseYAML, "          weight: %.2f\n", criterion.Weight)
						fmt.Fprintf(&phaseYAML, "          threshold: %.2f\n", criterion.Threshold)
					}
				}
			}
		}
		if p.noopMatch != "" {
			phaseYAML.WriteString("    noop:\n")
			fmt.Fprintf(&phaseYAML, "      match: %q\n", p.noopMatch)
		}
		if p.output != "" {
			fmt.Fprintf(&phaseYAML, "    output: %s\n", p.output)
		}
		if p.discussionCategory != "" || p.discussionTitleTemplate != "" || p.discussionTitleSearchTemplate != "" {
			phaseYAML.WriteString("    discussion:\n")
			if p.discussionCategory != "" {
				fmt.Fprintf(&phaseYAML, "      category: %q\n", p.discussionCategory)
			}
			if p.discussionTitleTemplate != "" {
				fmt.Fprintf(&phaseYAML, "      title_template: %q\n", p.discussionTitleTemplate)
			}
			if p.discussionTitleSearchTemplate != "" {
				fmt.Fprintf(&phaseYAML, "      title_search_template: %q\n", p.discussionTitleSearchTemplate)
			}
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

	workflowContent := fmt.Sprintf("name: %s\n", name)
	if opts.class != "" {
		workflowContent += fmt.Sprintf("class: %s\n", opts.class)
	}
	if opts.allowAdditiveProtectedWrites {
		workflowContent += "allow_additive_protected_writes: true\n"
	}
	if opts.allowCanonicalProtectedWrites {
		workflowContent += "allow_canonical_protected_writes: true\n"
	}
	workflowContent += fmt.Sprintf("phases:\n%s", phaseYAML.String())
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, name+".yaml"), []byte(workflowContent), 0o644))
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
	_, ok := spanExceptionEvent(span, message)
	return ok
}

func spanExceptionEvent(span sdktrace.ReadOnlySpan, message string) (sdktrace.Event, bool) {
	for _, event := range span.Events() {
		if event.Name != "exception" {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key == attribute.Key("exception.message") && attr.Value.AsString() == message {
				return event, true
			}
		}
	}
	return sdktrace.Event{}, false
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

	result, err := r.DrainAndWait(context.Background())
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

func TestDrainEvaluatorLoopRetriesGeneratorAndPersistsQualityReport(t *testing.T) {
	t.Setenv("XYLEM_DTU_STATE_PATH", "/tmp/dtu/state.json")

	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(_ string, prompt, _ string, _ ...string) ([]byte, error, bool) {
			switch {
			case strings.Contains(prompt, "Evaluate output") && strings.Contains(prompt, "Output: draft 1"):
				return []byte(`{"pass":false,"score":{"overall":0.40,"criteria":{"correctness":0.40},"issues":[{"severity":1,"description":"missing tests","suggestion":"add tests"}]},"feedback":[{"severity":1,"description":"missing tests","suggestion":"add tests"}]}`), nil, true
			case strings.Contains(prompt, "Evaluate output") && strings.Contains(prompt, "Output: draft 2"):
				return []byte(`{"pass":true,"score":{"overall":0.95,"criteria":{"correctness":0.95}},"feedback":[]}`), nil, true
			case strings.Contains(prompt, "Implement fix") && strings.Contains(prompt, "Suggestion: add tests"):
				return []byte("draft 2"), nil, true
			case strings.Contains(prompt, "Implement fix"):
				return []byte("draft 1"), nil, true
			default:
				return nil, nil, false
			}
		},
	}

	r := newWorkflowRunner(t, "eval-loop", []testPhase{
		{
			name:                   "implement",
			promptContent:          "Implement fix\nFeedback: {{.Evaluation.Feedback}}",
			maxTurns:               6,
			evaluatorPromptContent: "Evaluate output\nCriteria: {{.Evaluation.Criteria}}\nOutput: {{.Evaluation.Output}}",
			evaluatorMaxTurns:      4,
			evaluatorMaxIterations: 2,
			evaluatorPassThreshold: 0.70,
			evaluatorCriteria: []evaluator.Criterion{
				{Name: "correctness", Description: "Fix is correct", Weight: 1.0, Threshold: 0.70},
			},
		},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Completed)

	summary := loadSummary(t, r.Config.StateDir, "issue-1")
	require.Len(t, summary.Phases, 1)
	assert.Equal(t, evalReportRelativePath("issue-1"), summary.EvalReportPath)
	assert.Equal(t, evidenceManifestRelativePath("issue-1"), summary.EvidenceManifestPath)
	assert.Equal(t, 2, summary.Phases[0].EvalIterations)
	assert.True(t, summary.Phases[0].EvalConverged)
	assert.NotEmpty(t, summary.Phases[0].EvalIntensity)

	manifest, err := evidence.LoadManifest(r.Config.StateDir, "issue-1")
	require.NoError(t, err)
	require.Len(t, manifest.Claims, 1)
	assert.Equal(t, `Evaluator review for phase "implement" met configured quality thresholds`, manifest.Claims[0].Claim)
	assert.Equal(t, evidence.BehaviorallyChecked, manifest.Claims[0].Level)
	assert.Equal(t, evalReportRelativePath("issue-1"), manifest.Claims[0].ArtifactPath)
	assert.True(t, manifest.Claims[0].Passed)

	outputPath := config.RuntimePath(r.Config.StateDir, "phases", "issue-1", "implement.output")
	outputData, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Equal(t, "draft 2", string(outputData))

	reportPath := config.RuntimePath(r.Config.StateDir, "phases", "issue-1", evalReportFileName)
	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)

	var artifact EvaluationArtifact
	require.NoError(t, json.Unmarshal(reportData, &artifact))
	require.Len(t, artifact.Phases, 1)
	assert.Equal(t, "implement", artifact.Phases[0].Phase)
	assert.Equal(t, 2, artifact.Phases[0].Iterations)
	assert.True(t, artifact.Phases[0].Converged)
	if assert.NotNil(t, artifact.Phases[0].FinalResult) {
		assert.True(t, artifact.Phases[0].FinalResult.Pass)
	}

	cmdRunner.mu.Lock()
	require.Len(t, cmdRunner.phaseCalls, 4)
	assert.Contains(t, cmdRunner.phaseCalls[1].prompt, "Output: draft 1")
	assert.Contains(t, cmdRunner.phaseCalls[2].prompt, "Suggestion: add tests")
	assert.Contains(t, cmdRunner.phaseCalls[3].prompt, "Output: draft 2")
	assert.True(t, containsArgSequence(cmdRunner.phaseCalls[0].args, "--dtu-attempt", "1001"))
	assert.True(t, containsArgSequence(cmdRunner.phaseCalls[1].args, "--dtu-attempt", "1001"))
	assert.True(t, containsArgSequence(cmdRunner.phaseCalls[2].args, "--dtu-attempt", "1002"))
	assert.True(t, containsArgSequence(cmdRunner.phaseCalls[3].args, "--dtu-attempt", "1002"))
	cmdRunner.mu.Unlock()

	phaseSpan := endedSpanByName(t, rec, "phase:implement")
	phaseAttrs := spanAttrMap(phaseSpan)
	assert.Equal(t, "true", phaseAttrs["xylem.eval.enabled"])
	assert.Equal(t, "2", phaseAttrs["xylem.eval.iterations"])
	assert.Equal(t, "true", phaseAttrs["xylem.eval.converged"])
	assert.Equal(t, "true", phaseAttrs["xylem.eval.pass"])
	assert.NotEmpty(t, phaseAttrs["signals.health"])
}

func TestDrainEvaluatorLoopAndGatePersistBothEvidenceClaims(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(2, "eval-and-gate")
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "eval-and-gate", []testPhase{
		{
			name:                   "implement",
			promptContent:          "Implement fix",
			maxTurns:               6,
			evaluatorPromptContent: "Evaluate output\nCriteria: {{.Evaluation.Criteria}}\nOutput: {{.Evaluation.Output}}",
			evaluatorMaxTurns:      4,
			evaluatorMaxIterations: 1,
			evaluatorPassThreshold: 0.70,
			evaluatorCriteria: []evaluator.Criterion{
				{Name: "correctness", Description: "Fix is correct", Weight: 1.0, Threshold: 0.70},
			},
			gate: `      type: command
      run: "make test"
      retries: 0
      evidence:
        claim: "Implementation gate passed"
        level: behaviorally_checked
        checker: "make test"`,
		},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Implement fix": []byte("draft 1"),
		},
		runPhaseHook: func(_ string, prompt, _ string, _ ...string) ([]byte, error, bool) {
			if strings.Contains(prompt, "Evaluate output") {
				return []byte(`{"pass":true,"score":{"overall":0.95,"criteria":{"correctness":0.95}},"feedback":[]}`), nil, true
			}
			return nil, nil, false
		},
		gateOutput: []byte("ok"),
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	manifest, err := evidence.LoadManifest(cfg.StateDir, vessel.ID)
	require.NoError(t, err)
	require.Len(t, manifest.Claims, 2)
	assert.Equal(t, evidence.BehaviorallyChecked, manifest.Claims[0].Level)
	assert.Equal(t, `Evaluator review for phase "implement" met configured quality thresholds`, manifest.Claims[0].Claim)
	assert.Equal(t, evalReportRelativePath(vessel.ID), manifest.Claims[0].ArtifactPath)
	assert.Equal(t, "implement", manifest.Claims[0].Phase)
	assert.Equal(t, "Implementation gate passed", manifest.Claims[1].Claim)
	assert.Equal(t, phaseArtifactRelativePath(vessel.ID, "implement"), manifest.Claims[1].ArtifactPath)
}

func TestBuildEvaluationClaimReflectsFailure(t *testing.T) {
	claim := buildEvaluationClaim("issue-1", workflow.Phase{Name: "implement"}, PhaseEvaluationReport{
		Converged:   false,
		FinalResult: &evaluator.EvalResult{Pass: false},
	}, time.Unix(123, 0))

	assert.Equal(t, `Evaluator review for phase "implement" did not meet configured quality thresholds`, claim.Claim)
	assert.False(t, claim.Passed)
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
	name                          string
	promptContent                 string
	maxTurns                      int
	evaluatorPromptContent        string
	evaluatorMaxTurns             int
	evaluatorMaxIterations        int
	evaluatorPassThreshold        float64
	evaluatorCriteria             []evaluator.Criterion
	noopMatch                     string
	gate                          string
	allowedTools                  string
	phaseType                     string // "command" or "" for prompt (default)
	run                           string // shell command for type=command
	output                        string
	discussionCategory            string
	discussionTitleTemplate       string
	discussionTitleSearchTemplate string
	dependsOn                     []string // explicit phase dependencies
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

func TestPhasePolicyIntents_DoesNotTreatCatReadAsControlPlaneWrite(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	r := New(cfg, nil, nil, nil)
	vessel := queue.Vessel{
		ID:       "issue-1",
		Source:   "github-issue",
		Workflow: "fix-bug",
		Meta:     map[string]string{"config_source": "github"},
	}
	phaseDef := workflow.Phase{Name: "inspect", Type: "command"}

	intents := r.phasePolicyIntents(vessel, phaseDef, "cat .xylem.yml", "")
	require.Len(t, intents, 1)
	assert.Equal(t, "external_command", intents[0].Action)
	assert.Equal(t, "inspect", intents[0].Resource)
}

func TestSmoke_S5_DiscussionOutputCreatesDiscussion(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Sources["scheduled"] = config.SourceConfig{Type: "scheduled", Repo: "owner/repo"}

	writeWorkflowFile(t, dir, "weekly-report", []testPhase{{
		name:                    "report",
		promptContent:           "Generate weekly report",
		maxTurns:                5,
		output:                  "discussion",
		discussionCategory:      "Reports",
		discussionTitleTemplate: "Velocity Report — {{.Date}}",
	}})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:        "scheduled-1",
		Source:    "scheduled",
		Workflow:  "weekly-report",
		Meta:      map[string]string{"config_source": "scheduled"},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	dequeued, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, dequeued)

	var discussionTitle string
	var discussionBody string
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Generate weekly report": []byte("## Weekly report\n\nAll green."),
		},
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name != "gh" {
				return nil, nil, false
			}
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, discussionResolveQuery):
				return []byte(`{"data":{"repository":{"id":"R_1","discussionCategories":{"nodes":[{"id":"C_1","name":"Reports"}]}}}}`), nil, true
			case strings.Contains(joined, discussionSearchQuery):
				return []byte(`{"data":{"node":{"discussions":{"nodes":[]}}}}`), nil, true
			case strings.Contains(joined, discussionCreateMutation):
				for i := 0; i+1 < len(args); i++ {
					switch {
					case args[i] == "-f" && strings.HasPrefix(args[i+1], "title="):
						discussionTitle = strings.TrimPrefix(args[i+1], "title=")
					case args[i] == "-f" && strings.HasPrefix(args[i+1], "body="):
						discussionBody = strings.TrimPrefix(args[i+1], "body=")
					}
				}
				return []byte(`{"data":{"createDiscussion":{"discussion":{"id":"D_1","title":"Velocity Report — 2026-04-11","url":"https://github.com/owner/repo/discussions/1"}}}}`), nil, true
			default:
				return nil, nil, false
			}
		},
	}

	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"scheduled": &source.Scheduled{Repo: "owner/repo"},
	}

	outcome := r.runVessel(context.Background(), *dequeued)
	assert.Equal(t, "completed", outcome)
	assert.Equal(t, queue.StateCompleted, loadSingleVessel(t, q).State)
	assert.Equal(t, "## Weekly report\n\nAll green.", discussionBody)
	assert.Contains(t, discussionTitle, "Velocity Report — ")
	assert.NotContains(t, discussionTitle, "{{")
	assert.True(t, hasRunOutputCallContaining(cmdRunner, "gh api graphql", "discussionCategories"))
	assert.True(t, hasRunOutputCallContaining(cmdRunner, "gh api graphql", "discussions(first: 20"))
	assert.True(t, hasRunOutputCallContaining(cmdRunner, "gh api graphql", "createDiscussion"))
}

func TestSmoke_S6_DiscussionOutputCommentsExistingDiscussionByTitlePrefix(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Sources["scheduled"] = config.SourceConfig{Type: "scheduled", Repo: "owner/repo"}

	writeWorkflowFile(t, dir, "weekly-report", []testPhase{{
		name:                          "report",
		promptContent:                 "Generate weekly report",
		maxTurns:                      5,
		output:                        "discussion",
		discussionCategory:            "Reports",
		discussionTitleTemplate:       "Velocity Report — {{.Date}}",
		discussionTitleSearchTemplate: "Velocity Report",
	}})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:        "scheduled-2",
		Source:    "scheduled",
		Workflow:  "weekly-report",
		Meta:      map[string]string{"config_source": "scheduled"},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	dequeued, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, dequeued)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Generate weekly report": []byte("## Weekly report\n\nExisting thread."),
		},
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name != "gh" {
				return nil, nil, false
			}
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, discussionResolveQuery):
				return []byte(`{"data":{"repository":{"id":"R_1","discussionCategories":{"nodes":[{"id":"C_1","name":"Reports"}]}}}}`), nil, true
			case strings.Contains(joined, discussionSearchQuery):
				return []byte(`{"data":{"node":{"discussions":{"nodes":[{"id":"D_1","title":"Velocity Report — 2026-04-01","url":"https://github.com/owner/repo/discussions/1"}]}}}}`), nil, true
			case strings.Contains(joined, discussionCommentMutation):
				return []byte(`{"data":{"addDiscussionComment":{"comment":{"url":"https://github.com/owner/repo/discussions/1#discussioncomment-1"}}}}`), nil, true
			default:
				return nil, nil, false
			}
		},
	}

	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"scheduled": &source.Scheduled{Repo: "owner/repo"},
	}

	outcome := r.runVessel(context.Background(), *dequeued)
	assert.Equal(t, "completed", outcome)
	assert.Equal(t, queue.StateCompleted, loadSingleVessel(t, q).State)
	assert.True(t, hasRunOutputCallContaining(cmdRunner, "gh api graphql", "addDiscussionComment"))
	assert.False(t, hasRunOutputCallContaining(cmdRunner, "gh api graphql", "createDiscussion"))
}

func TestSmoke_S7_DiscussionOutputFailsWithoutRepoSlug(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Sources["manual-scheduled"] = config.SourceConfig{Type: "scheduled"}

	writeWorkflowFile(t, dir, "weekly-report", []testPhase{{
		name:                    "report",
		promptContent:           "Generate weekly report",
		maxTurns:                5,
		output:                  "discussion",
		discussionCategory:      "Reports",
		discussionTitleTemplate: "Velocity Report — {{.Date}}",
	}})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:        "scheduled-3",
		Source:    "manual",
		Workflow:  "weekly-report",
		Meta:      map[string]string{"config_source": "manual-scheduled"},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	dequeued, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, dequeued)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Generate weekly report": []byte("## Weekly report\n\nMissing repo."),
		},
	}

	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)

	outcome := r.runVessel(context.Background(), *dequeued)
	assert.Equal(t, "failed", outcome)

	final := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, final.State)
	assert.Contains(t, final.Error, `phase report: publish discussion for phase report: repo slug "" must be in owner/repo form`)
	assert.False(t, hasRunOutputCallContaining(cmdRunner, "gh api graphql", "createDiscussion"))
}

func TestSmoke_S8_DiscussionOutputSkipsPublishOnNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Sources["scheduled"] = config.SourceConfig{Type: "scheduled", Repo: "owner/repo"}

	writeWorkflowFile(t, dir, "weekly-report", []testPhase{{
		name:                    "post",
		phaseType:               "command",
		run:                     "cat .xylem/state/report.md",
		noopMatch:               "XYLEM_NOOP",
		output:                  "discussion",
		discussionCategory:      "Reports",
		discussionTitleTemplate: "Velocity Report — {{.Date}}",
	}})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:        "scheduled-4",
		Source:    "scheduled",
		Workflow:  "weekly-report",
		Meta:      map[string]string{"config_source": "scheduled"},
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	dequeued, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, dequeued)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("XYLEM_NOOP: no weekly report available yet\n"),
	}

	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"scheduled": &source.Scheduled{Repo: "owner/repo"},
	}

	outcome := r.runVessel(context.Background(), *dequeued)
	assert.Equal(t, "completed", outcome)
	assert.Equal(t, queue.StateCompleted, loadSingleVessel(t, q).State)
	assert.False(t, hasRunOutputCallContaining(cmdRunner, "gh api graphql", "createDiscussion"))
	assert.False(t, hasRunOutputCallContaining(cmdRunner, "gh api graphql", "addDiscussionComment"))
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

func TestPhasePolicyIntents_DoesNotClassifyDestructiveGitOrDeploySeparately(t *testing.T) {
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

	intents := r.phasePolicyIntents(vessel, phaseDef, "git reset --hard HEAD~1 && git push --force origin main && ./deploy.sh production", "")
	require.Len(t, intents, 2)

	assert.Equal(t, "external_command", intents[0].Action)
	assert.Equal(t, "publish", intents[0].Resource)
	assert.Equal(t, "git_push", intents[1].Action)
	assert.Equal(t, "main", intents[1].Resource)
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

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "denied by policy")
	assert.Len(t, cmdRunner.phaseCalls, 0)

	_, statErr := os.Stat(config.RuntimePath(cfg.StateDir, "phases", "issue-1", "solve.output"))
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

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "snapshot failed")
	assert.Len(t, cmdRunner.phaseCalls, 0)

	_, statErr := os.Stat(config.RuntimePath(cfg.StateDir, "phases", "issue-1", "plan.output"))
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

func TestRunnerPolicyMatrixDeniesDeliveryControlPlaneWrite(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "delivery-write"))

	writeWorkflowFileWithOptions(t, dir, "delivery-write", testWorkflowOptions{
		class: string(policy.Delivery),
	}, []testPhase{{
		name:          "implement",
		promptContent: "Write .xylem/HARNESS.md with the updated harness rules.",
		maxTurns:      5,
	}})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Len(t, cmdRunner.phaseCalls, 0)

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "denied by policy")
	assert.Contains(t, vessel.Error, ".xylem/HARNESS.md")

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	var writeEntry *intermediary.AuditEntry
	for i := range entries {
		if entries[i].Intent.Action == "file_write" && entries[i].Intent.Resource == ".xylem/HARNESS.md" {
			writeEntry = &entries[i]
			break
		}
	}
	require.NotNil(t, writeEntry)
	assert.Equal(t, intermediary.Deny, writeEntry.Decision)
	assert.Equal(t, "delivery", writeEntry.WorkflowClass)
	assert.Equal(t, "write_control_plane", writeEntry.Operation)
	assert.Equal(t, "delivery.no_control_plane_writes", writeEntry.RuleMatched)
	assert.Equal(t, vessel.ID, writeEntry.VesselID)
	assert.Equal(t, filepath.Join(dir, ".xylem", "HARNESS.md"), writeEntry.FilePath)
}

func TestRunnerPolicyMatrixAllowsHarnessMaintenanceControlPlaneWrite(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "harness-write"))

	writeWorkflowFileWithOptions(t, dir, "harness-write", testWorkflowOptions{
		class:                        string(policy.HarnessMaintenance),
		allowAdditiveProtectedWrites: true,
	}, []testPhase{{
		name:          "implement",
		promptContent: "Write .xylem/HARNESS.md with the updated harness rules.",
		maxTurns:      5,
	}})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{"Write .xylem/HARNESS.md": []byte("done")},
	}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	assert.Len(t, cmdRunner.phaseCalls, 1)

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	var writeEntry *intermediary.AuditEntry
	for i := range entries {
		if entries[i].Intent.Action == "file_write" && entries[i].Intent.Resource == ".xylem/HARNESS.md" {
			writeEntry = &entries[i]
			break
		}
	}
	require.NotNil(t, writeEntry)
	assert.Equal(t, intermediary.Allow, writeEntry.Decision)
	assert.Equal(t, "harness-maintenance", writeEntry.WorkflowClass)
	assert.Equal(t, "write_control_plane", writeEntry.Operation)
	assert.Equal(t, "harness_maintenance.worktree_writes_allowed", writeEntry.RuleMatched)
	assert.Equal(t, filepath.Join(dir, ".xylem", "HARNESS.md"), writeEntry.FilePath)
}

func TestRunnerPolicyWarnModeLogsButAllowsControlPlaneWrite(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Harness.Policy.Mode = "warn"

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "warn-write"))

	writeWorkflowFileWithOptions(t, dir, "warn-write", testWorkflowOptions{
		class: string(policy.Delivery),
	}, []testPhase{{
		name:          "implement",
		promptContent: "Write .xylem/HARNESS.md with the updated harness rules.",
		maxTurns:      5,
	}})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{"Write .xylem/HARNESS.md": []byte("done")},
	}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)
	r.Intermediary.SetMode(cfg.HarnessPolicyMode())

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	assert.Len(t, cmdRunner.phaseCalls, 1)

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	var writeEntry *intermediary.AuditEntry
	for i := range entries {
		if entries[i].Intent.Action == "file_write" && entries[i].Intent.Resource == ".xylem/HARNESS.md" {
			writeEntry = &entries[i]
			break
		}
	}
	require.NotNil(t, writeEntry)
	assert.Equal(t, intermediary.Deny, writeEntry.Decision)
	assert.Equal(t, "delivery", writeEntry.WorkflowClass)
	assert.Equal(t, "write_control_plane", writeEntry.Operation)
	assert.Equal(t, "delivery.no_control_plane_writes", writeEntry.RuleMatched)
}

func TestRunnerHarnessMaintenanceDefaultBranchPushDeniedAtGitLayer(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	// HarnessPolicyMode() zero-value is warn per
	// docs/plans/sota-gap-implementation-2026-04-11.md, so explicitly opt
	// into enforce mode: this test is specifically asserting that git_push
	// to the default branch is *blocked* (not just warned) when a harness-
	// maintenance workflow runs under strict policy enforcement.
	cfg.Harness.Policy.Mode = "enforce"

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "push-main"))

	writeWorkflowFileWithOptions(t, dir, "push-main", testWorkflowOptions{
		class: string(policy.HarnessMaintenance),
	}, []testPhase{{
		name:      "publish",
		phaseType: "command",
		run:       "git push origin main",
	}})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "git" && len(args) >= 4 && args[2] == "symbolic-ref" {
				return []byte("refs/remotes/origin/main\n"), nil, true
			}
			return nil, nil, false
		},
	}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath()))
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.AuditLog = auditLog
	r.Intermediary = intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, 0, countRunOutputCalls(cmdRunner, "sh"))

	vessel := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, vessel.State)
	assert.Contains(t, vessel.Error, "denied by policy")
	assert.Contains(t, vessel.Error, "git_push")

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	var guardEntry *intermediary.AuditEntry
	for i := range entries {
		if entries[i].Operation == "commit_default_branch" {
			guardEntry = &entries[i]
			break
		}
	}
	require.NotNil(t, guardEntry)
	assert.Equal(t, intermediary.Deny, guardEntry.Decision)
	assert.Equal(t, "harness-maintenance", guardEntry.WorkflowClass)
	assert.Equal(t, harnessMaintenanceDefaultBranchRule, guardEntry.RuleMatched)
	assert.Equal(t, vessel.ID, guardEntry.VesselID)
}

func TestSmoke_S4_WorkflowClassEnforcement(t *testing.T) {
	t.Run("delivery denies control-plane write", TestRunnerPolicyMatrixDeniesDeliveryControlPlaneWrite)
	t.Run("harness-maintenance allows control-plane write", TestRunnerPolicyMatrixAllowsHarnessMaintenanceControlPlaneWrite)
	t.Run("warn mode logs but allows control-plane write", TestRunnerPolicyWarnModeLogsButAllowsControlPlaneWrite)
	t.Run("harness-maintenance default-branch push denied", TestRunnerHarnessMaintenanceDefaultBranchPushDeniedAtGitLayer)
}

func TestEnsureWorktreeRecreatesMissingInheritedPath(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := queue.Vessel{
		ID:           "issue-99-retry-1",
		Source:       "manual",
		Workflow:     "fix-bug",
		Ref:          "retry-spec",
		State:        queue.StatePending,
		CreatedAt:    time.Now().UTC(),
		CurrentPhase: 2,
		WorktreePath: filepath.Join(dir, "missing-worktree"),
		PhaseOutputs: map[string]string{"plan": config.RuntimePath(filepath.Join(dir, ".xylem"), "phases", "issue-99", "plan.output")},
	}
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	recreatedPath := filepath.Join(dir, "recreated-worktree")
	wt := &trackingWorktree{path: recreatedPath}
	r := New(cfg, q, wt, &mockCmdRunner{})

	worktreePath, ok := r.ensureWorktree(context.Background(), &vessel, &source.Manual{})
	require.True(t, ok)
	require.Equal(t, recreatedPath, worktreePath)
	require.Equal(t, recreatedPath, vessel.WorktreePath)
	require.Equal(t, 1, wt.createCalls)
	require.Equal(t, "task/issue-99-retry-1-retry-spec", wt.lastBranch)

	stored, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	require.Equal(t, recreatedPath, stored.WorktreePath)
	require.Equal(t, 2, stored.CurrentPhase)
	require.Equal(t, vessel.PhaseOutputs, stored.PhaseOutputs)
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

	result, err := r.DrainAndWait(context.Background())
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

// TestDrainBudgetStopsDequeueingAfterDeadline verifies that a non-zero
// Runner.DrainBudget bounds how long Drain() continues dequeueing new
// vessels. Under sustained load the budget elapses, Drain() stops
// dequeueing, waits for already-started goroutines, and returns. The
// remaining pending vessels are left for the next drain tick.
//
// This is the primary regression guard for the auto-upgrade deadlock:
// without a bounded Drain(), the daemon's drain-end periodic upgrade
// check at cli/cmd/xylem/daemon.go:248-254 cannot fire during sustained
// saturation.
func TestDrainBudgetStopsDequeueingAfterDeadline(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1) // concurrency=1 for deterministic timing
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	// Enqueue 10 vessels; each runs one "fix" phase that sleeps for
	// 60ms via the countingCmdRunner delay. With concurrency=1, each
	// vessel takes ~60ms to process. Budget=150ms allows ~2 vessels to
	// complete before the dequeue loop stops.
	for i := 1; i <= 10; i++ {
		if _, err := q.Enqueue(makeVessel(i, "fix-bug")); err != nil {
			t.Fatalf("Enqueue(%d) error = %v", i, err)
		}
	}
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "fix", promptContent: "Fix issue.", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &countingCmdRunner{delay: 60 * time.Millisecond}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.DrainBudget = 150 * time.Millisecond

	start := time.Now()
	result, err := r.DrainAndWait(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	// Drain() must return in roughly (budget + one vessel cycle), well
	// under the time it would take to process all 10 vessels (>600ms).
	if elapsed > 400*time.Millisecond {
		t.Errorf("Drain() took %s, expected under 400ms (budget=150ms + in-flight)", elapsed)
	}
	if result.Completed >= 10 {
		t.Errorf("Drain() completed %d vessels, expected partial drain (fewer than 10)", result.Completed)
	}
	if result.Completed < 1 {
		t.Errorf("Drain() completed %d vessels, expected at least 1", result.Completed)
	}

	// Remaining vessels must still be pending for the next tick.
	vessels, _ := q.List()
	var pending, completed int
	for _, v := range vessels {
		switch v.State {
		case queue.StatePending:
			pending++
		case queue.StateCompleted:
			completed++
		}
	}
	if completed != result.Completed {
		t.Errorf("queue completed count %d != DrainResult.Completed %d", completed, result.Completed)
	}
	if pending == 0 {
		t.Errorf("expected some pending vessels after budget cutoff, got 0")
	}
	total := pending + completed
	if total != 10 {
		t.Errorf("pending+completed = %d, want 10 (no vessel lost)", total)
	}
}

// TestDrainBudgetZeroDisablesBudget verifies that DrainBudget == 0
// preserves the legacy unbounded behavior: Drain() processes every
// pending vessel in one call.
func TestDrainBudgetZeroDisablesBudget(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	for i := 1; i <= 5; i++ {
		if _, err := q.Enqueue(makeVessel(i, "fix-bug")); err != nil {
			t.Fatalf("Enqueue(%d) error = %v", i, err)
		}
	}
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "fix", promptContent: "Fix issue.", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	// DrainBudget deliberately left at zero.

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 5 {
		t.Errorf("Drain() completed = %d, want 5 (unbounded drain)", result.Completed)
	}
}

// TestDrainBudgetRespectsContextCancellation verifies that context
// cancellation short-circuits the budget check: Drain() stops
// dequeueing immediately on cancel regardless of the budget state.
func TestDrainBudgetRespectsContextCancellation(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	for i := 1; i <= 5; i++ {
		if _, err := q.Enqueue(makeVessel(i, "fix-bug")); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "fix", promptContent: "Fix issue.", maxTurns: 5},
	})
	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &countingCmdRunner{delay: 50 * time.Millisecond}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	// Large budget so context cancel is the deciding factor.
	r.DrainBudget = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after 30ms — before the first vessel completes.
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := r.DrainAndWait(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	// With a 10s budget, if cancel didn't short-circuit we'd wait
	// for all 5 × 50ms vessels + budget expiry.
	if elapsed > 500*time.Millisecond {
		t.Errorf("Drain() took %s, expected fast cancel-driven return", elapsed)
	}
}

func assertCancelledVesselStopsRunningPhaseAndDropsInFlight(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	vessel := makeVessel(181, "resolve-conflicts")
	vessel.Meta["issue_title"] = "Resolve PR conflicts"
	vessel.Meta["issue_body"] = "Merge origin/main into the PR branch."
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "resolve-conflicts", []testPhase{
		{name: "analyze", promptContent: "Analyze conflicts", maxTurns: 5},
		{name: "resolve", promptContent: "Resolve conflicts", maxTurns: 5},
		{name: "push", promptContent: "Push branch", maxTurns: 5},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := newBlockingPhaseCmdRunner()
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldWriter)

	drainResult, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, drainResult.Launched)

	select {
	case <-cmdRunner.resolveStart:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resolve phase to start")
	}

	require.NoError(t, q.Cancel(vessel.ID))

	select {
	case <-cmdRunner.resolveExit:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resolve phase to exit after cancel")
	}

	waitDone := make(chan DrainResult, 1)
	go func() {
		waitDone <- r.Wait()
	}()

	var waited DrainResult
	select {
	case waited = <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runner.Wait() after cancel")
	}

	assert.Equal(t, 0, r.InFlightCount())
	assert.Equal(t, 1, waited.Skipped)
	assert.Zero(t, cmdRunner.pushStarted.Load())
	assert.NotContains(t, logBuf.String(), "invalid state transition")

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, queue.StateCancelled, updated.State)

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Equal(t, "cancelled", summary.State)
	assert.Equal(t, vessel.ID, summary.VesselID)
}

func TestSmoke_S38_CancelledVesselStopsRunningPhaseAndDropsInFlight(t *testing.T) {
	assertCancelledVesselStopsRunningPhaseAndDropsInFlight(t)
}

func TestSmoke_S33_DrainReturnsBeforeInFlightWorkCompletes(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "fix-bug"))
	require.NoError(t, err)
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "fix", promptContent: "Fix issue.", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &countingCmdRunner{delay: 120 * time.Millisecond}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	start := time.Now()
	result, err := r.Drain(context.Background())
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Launched)
	assert.LessOrEqual(t, elapsed, 80*time.Millisecond)
	assert.Equal(t, 1, r.InFlightCount())
	vessel, err := q.FindByID("issue-1")
	require.NoError(t, err)
	require.NotNil(t, vessel)
	assert.Equal(t, queue.StateRunning, vessel.State)

	waited := r.Wait()
	assert.Equal(t, 1, waited.Completed)
	assert.Equal(t, 0, r.InFlightCount())
}

func TestSmoke_S34_DrainUsesRemainingCapacityFromPreviousTicks(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 3)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	for i := 1; i <= 3; i++ {
		_, err := q.Enqueue(makeVessel(i, "fix-bug"))
		require.NoError(t, err)
	}
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "fix", promptContent: "Fix issue.", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &countingCmdRunner{delay: 60 * time.Millisecond}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	r.sem <- struct{}{}
	r.inFlight.Add(1)
	r.wg.Add(1)
	heldDone := make(chan struct{})
	go func() {
		<-heldDone
		<-r.sem
		r.inFlight.Add(-1)
		r.wg.Done()
	}()

	result, err := r.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, result.Launched)
	assert.Equal(t, 3, r.InFlightCount())

	close(heldDone)
	waited := r.Wait()
	assert.Equal(t, 2, waited.Completed)
	vessels, err := q.List()
	require.NoError(t, err)
	var pending, completed int
	for _, vessel := range vessels {
		switch vessel.State {
		case queue.StatePending:
			pending++
		case queue.StateCompleted:
			completed++
		}
	}
	assert.Equal(t, 1, pending)
	assert.Equal(t, 2, completed)
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

	result, err := r.DrainAndWait(context.Background())
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
		outputPath := config.RuntimePath(filepath.Join(dir, ".xylem"), "phases", "issue-1", pName+".output")
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

	result, err := r.DrainAndWait(context.Background())
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

	analyzeOutputPath := config.RuntimePath(filepath.Join(dir, ".xylem"), "phases", "issue-1", "analyze.output")
	if _, err := os.Stat(analyzeOutputPath); err != nil {
		t.Fatalf("expected analyze output file to exist: %v", err)
	}
	implementOutputPath := config.RuntimePath(filepath.Join(dir, ".xylem"), "phases", "issue-1", "implement.output")
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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
	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	assert.True(t, sawOriginalContents)
	require.Len(t, cmdRunner.phaseCalls, 1)
	assert.Equal(t, queue.StateCompleted, loadSingleVessel(t, q).State)
	data, readErr := os.ReadFile(protectedPath)
	require.NoError(t, readErr)
	assert.Equal(t, originalContents, string(data))
}

func TestRunVessel_BuiltinWorkflowCompletesWithoutLoadingWorkflowFile(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:        "lessons-1",
		Source:    "manual",
		Workflow:  "lessons",
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	vessel, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)

	wt := &mockWorktree{createErr: errors.New("worktree should not be created")}
	r := New(cfg, q, wt, &mockCmdRunner{})
	called := false
	r.BuiltinWorkflows = map[string]BuiltinWorkflowHandler{
		"lessons": func(_ context.Context, got queue.Vessel) error {
			called = true
			assert.Equal(t, "lessons-1", got.ID)
			return nil
		},
	}

	outcome := r.runVessel(context.Background(), *vessel)
	assert.Equal(t, "completed", outcome)
	assert.True(t, called)

	final := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateCompleted, final.State)
	assert.Empty(t, final.WorktreePath)

	summary := loadSummary(t, cfg.StateDir, "lessons-1")
	require.Len(t, summary.Phases, 1)
	assert.Equal(t, "lessons", summary.Phases[0].Name)
	assert.Equal(t, "builtin", summary.Phases[0].Type)
	assert.Equal(t, "completed", summary.Phases[0].Status)
}

func TestSmoke_S6_WorkflowDigestSnapshotPopulatedAtLaunch(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	writeWorkflowFile(t, dir, "fix-bug", []testPhase{{
		name:          "analyze",
		promptContent: "do the thing",
		maxTurns:      1,
	}})
	withTestWorkingDir(t, dir)

	workflowPath := filepath.Join(dir, ".xylem", "workflows", "fix-bug.yaml")
	_, expectedDigest, err := workflow.LoadWithDigest(workflowPath)
	require.NoError(t, err)

	originalWorkflow, err := os.ReadFile(workflowPath)
	require.NoError(t, err)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	v := makeVessel(1, "fix-bug")
	v.WorkflowDigest = "wf-stale-scan-digest"
	_, err = q.Enqueue(v)
	require.NoError(t, err)

	dequeued, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, dequeued)

	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(dirPath, prompt, name string, args ...string) ([]byte, error, bool) {
			mutated := append([]byte("description: mutated during run\n"), originalWorkflow...)
			require.NoError(t, os.WriteFile(workflowPath, mutated, 0o644))
			return nil, errors.New("phase boom"), true
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)

	outcome := r.runVessel(context.Background(), *dequeued)
	assert.Equal(t, "failed", outcome)

	final := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, final.State)
	assert.Equal(t, expectedDigest, final.WorkflowDigest)
	assert.Equal(t, expectedDigest, final.Meta[recovery.MetaWorkflowDigest])
	assert.NotEqual(t, "wf-stale-scan-digest", final.WorkflowDigest)
	assert.NotEqual(t, recovery.DigestFile(workflowPath, "wf"), final.WorkflowDigest)

	snapshotPath := config.RuntimePath(cfg.StateDir, "phases", final.ID, workflowSnapshotDirName, final.Workflow+".yaml")
	snapshotBytes, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	assert.Equal(t, originalWorkflow, snapshotBytes)

	artifact, err := recovery.Load(config.RuntimePath(cfg.StateDir, "phases", final.ID, "failure-review.json"))
	require.NoError(t, err)
	assert.Equal(t, expectedDigest, artifact.WorkflowDigest)
}

func TestSmoke_S7_WaitingVesselResumesAgainstFrozenWorkflowSnapshot(t *testing.T) {
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

	workflowPath := filepath.Join(dir, ".xylem", "workflows", "fix-bug.yaml")
	_, expectedDigest, err := workflow.LoadWithDigest(workflowPath)
	require.NoError(t, err)

	labelViewJSON := `{"labels":[{"name":"plan-approved"}]}`
	cmdRunner := &mockCmdRunner{
		outputData: []byte(labelViewJSON),
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	first, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, first.Waiting)

	waiting, err := q.FindByID("issue-1")
	require.NoError(t, err)
	require.Equal(t, queue.StateWaiting, waiting.State)
	require.Equal(t, expectedDigest, waiting.WorkflowDigest)

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "plan", promptContent: "Create mutated plan", maxTurns: 5,
			gate: "      type: label\n      wait_for: \"plan-approved\"\n      timeout: \"24h\"",
		},
		{name: "mutated", promptContent: "Mutated after approval", maxTurns: 10},
	})

	r.CheckWaitingVessels(context.Background())

	second, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, second.Completed)

	done, err := q.FindByID("issue-1")
	require.NoError(t, err)
	require.Equal(t, queue.StateCompleted, done.State)
	require.Equal(t, expectedDigest, done.WorkflowDigest)
	require.Len(t, cmdRunner.phaseCalls, 2)
	assert.Contains(t, cmdRunner.phaseCalls[1].prompt, "Implement after approval")
	assert.NotContains(t, cmdRunner.phaseCalls[1].prompt, "Mutated after approval")

	snapshotPath := config.RuntimePath(cfg.StateDir, "phases", done.ID, workflowSnapshotDirName, done.Workflow+".yaml")
	_, snapshotDigest, err := workflow.LoadWithDigest(snapshotPath)
	require.NoError(t, err)
	assert.Equal(t, expectedDigest, snapshotDigest)
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

	result, err := r.DrainAndWait(context.Background())
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
	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	manifestPath := config.RuntimePath(cfg.StateDir, "phases", "prompt-1", "evidence-manifest.json")
	assert.NoFileExists(t, manifestPath)
}

func TestSmoke_WS6_S17_PromptOnlyVesselSummaryArtifact(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "prompt-only summary"))

	r := New(cfg, q, &mockWorktree{path: dir}, &mockCmdRunner{})
	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	assert.FileExists(t, config.RuntimePath(cfg.StateDir, "phases", "prompt-1", summaryFileName))
	summary := loadSummary(t, cfg.StateDir, "prompt-1")
	assert.Equal(t, "completed", summary.State)
	assert.Equal(t, "prompt-1", summary.VesselID)
	require.Len(t, summary.Phases, 1)
	assert.Equal(t, "prompt", summary.Phases[0].Name)
	assert.Equal(t, "prompt", summary.Phases[0].Type)
	assert.Equal(t, "completed", summary.Phases[0].Status)
	assert.Equal(t, cost.UsageSourceEstimated, summary.Phases[0].UsageSource)
	assert.Positive(t, summary.Phases[0].InputTokensEst)
	assert.Positive(t, summary.Phases[0].OutputTokensEst)
	assert.Positive(t, summary.Phases[0].CostUSDEst)
	assert.Equal(t, summary.TotalTokensEst, summary.Phases[0].InputTokensEst+summary.Phases[0].OutputTokensEst)
	assert.Equal(t, summary.TotalCostUSDEst, summary.Phases[0].CostUSDEst)

	report, err := cost.LoadReport(config.RuntimePath(cfg.StateDir, "phases", "prompt-1", costReportFileName))
	require.NoError(t, err)
	require.Len(t, report.Phases, 1)
	assert.Equal(t, "prompt", report.Phases[0].Name)
	assert.Equal(t, "prompt", report.Phases[0].Type)
	assert.Equal(t, "completed", report.Phases[0].Status)
	assert.Equal(t, cost.UsageSourceEstimated, report.Phases[0].UsageSource)
	assert.Equal(t, summary.TotalTokensEst, report.Phases[0].TotalTokens)
	assert.Equal(t, summary.TotalCostUSDEst, report.Phases[0].CostUSD)
}

func TestSmoke_S4_CompactedPromptArtifactWrittenWithinContextBudget(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Phase.ContextBudget = config.DefaultPhaseContextBudget

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "context-compaction"))
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "context-compaction", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the oversized prompt inputs",
			maxTurns:      5,
		},
		{
			name:          "implement",
			promptContent: strings.Repeat("{{.PreviousOutputs.analyze}}\n", 40),
			maxTurns:      5,
		},
	})

	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the oversized prompt inputs": []byte(strings.Repeat("analysis context block ", 1200)),
			"[context compacted":                  []byte("implementation completed"),
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	promptPath := config.RuntimePath(cfg.StateDir, "phases", "issue-1", "implement.prompt")
	assert.FileExists(t, promptPath)

	prompt, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	assert.LessOrEqual(t, ctxmgr.EstimateTokens(string(prompt)), config.DefaultPhaseContextBudget)
	assert.Contains(t, string(prompt), "[context compacted")
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

func TestSmoke_S39_GateRetryFailurePostsOnlyFinalFailedComment(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name:          "implement",
			promptContent: "Implement fix\n{{.GateResult}}",
			maxTurns:      10,
			gate:          "      type: command\n      run: \"make test\"\n      retries: 2\n      retry_delay: \"0s\"",
		},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("FAIL: TestFoo"),
		gateErr:    &mockExitError{code: 1},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	r.Reporter = &reporter.Reporter{Runner: cmdRunner, Repo: "owner/repo"}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Len(t, cmdRunner.phaseCalls, 3)

	bodies := issueCommentBodies(cmdRunner)
	require.Len(t, bodies, 1)
	assert.Contains(t, bodies[0], "failed at phase `implement`")
	assert.NotContains(t, bodies[0], "phase `implement` completed")
}

func TestSmoke_S40_GateRetrySuccessPostsCompletedOnceAfterGatePass(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name:          "implement",
			promptContent: "Implement fix\n{{.GateResult}}",
			maxTurns:      10,
			gate:          "      type: command\n      run: \"make test\"\n      retries: 2\n      retry_delay: \"0s\"",
		},
		{name: "pr", promptContent: "Create PR", maxTurns: 3},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		gateCallResults: []gateCallResult{
			{output: []byte("FAIL: TestFoo"), err: &mockExitError{code: 1}},
			{output: []byte("ok"), err: nil},
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	r.Reporter = &reporter.Reporter{Runner: cmdRunner, Repo: "owner/repo"}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	assert.Len(t, cmdRunner.phaseCalls, 3)

	bodies := issueCommentBodies(cmdRunner)
	require.Len(t, bodies, 3)
	assert.Contains(t, bodies[0], "phase `implement` completed")
	assert.Contains(t, bodies[1], "phase `pr` completed")
	assert.Contains(t, bodies[2], "**xylem — all phases completed**")
	assert.Equal(t, 1, strings.Count(bodies[2], "| implement |"))
	assert.Equal(t, 1, strings.Count(bodies[2], "| pr |"))
}

func TestSmoke_S31_ResolveConflictsGateFailsBeforePushWhenOriginMainIsNotAncestor(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	vessel := makeVessel(164, "resolve-conflicts")
	vessel.Meta["issue_title"] = "Resolve PR conflicts"
	vessel.Meta["issue_body"] = "Merge origin/main into the PR branch."
	_, _ = q.Enqueue(vessel)

	writeWorkflowFile(t, dir, "resolve-conflicts", []testPhase{
		{name: "analyze", promptContent: "Analyze conflicts", maxTurns: 5},
		{
			name:          "resolve",
			promptContent: "Resolve conflicts\n{{.GateResult}}",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"git fetch origin main && test \\\"$(git branch --show-current)\\\" = \\\"$(gh pr view {{.Issue.Number}} --json headRefName --jq '.headRefName')\\\" && git merge-base --is-ancestor origin/main HEAD && cd cli && go test ./...\"\n      retries: 0",
		},
		{name: "push", promptContent: "Push branch", maxTurns: 5},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte("origin/main is not an ancestor of HEAD"),
		gateErr:    &mockExitError{code: 1},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	require.Len(t, cmdRunner.phaseCalls, 2)
	assert.Contains(t, cmdRunner.phaseCalls[0].prompt, "Analyze conflicts")
	assert.Contains(t, cmdRunner.phaseCalls[1].prompt, "Resolve conflicts")

	stored := loadSingleVessel(t, q)
	assert.Equal(t, queue.StateFailed, stored.State)
	assert.Equal(t, "resolve", stored.FailedPhase)
	assert.Contains(t, stored.GateOutput, "origin/main is not an ancestor")
}

func TestDrainCommandGateRendersTemplateData(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	vessel := makeVessel(42, "fix-bug")
	vessel.Meta["issue_title"] = "Template gate"
	vessel.Meta["issue_body"] = "Ensure gate commands render template data."
	_, _ = q.Enqueue(vessel)

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name:          "implement",
			promptContent: "Implement fix",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"echo {{.Issue.Number}} {{.Vessel.ID}} {{.Phase.Name}}\"\n      retries: 0",
		},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	var gateCommand string
	for _, args := range cmdRunner.outputArgs {
		if len(args) == 3 && args[0] == "sh" && args[1] == "-c" && strings.Contains(args[2], "echo 42 issue-42 implement") {
			gateCommand = args[2]
			break
		}
	}
	require.NotEmpty(t, gateCommand)
	assert.NotContains(t, gateCommand, "{{")
}

func TestSmoke_S32_ResolveConflictsGateRendersPRHeadAndAncestorChecks(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	vessel := makeVessel(42, "resolve-conflicts")
	vessel.Meta["issue_title"] = "Resolve PR conflicts"
	vessel.Meta["issue_body"] = "Merge origin/main into the PR branch."
	_, _ = q.Enqueue(vessel)

	writeWorkflowFile(t, dir, "resolve-conflicts", []testPhase{
		{name: "analyze", promptContent: "Analyze conflicts", maxTurns: 5},
		{
			name:          "resolve",
			promptContent: "Resolve conflicts",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"git fetch origin main && test \\\"$(git branch --show-current)\\\" = \\\"$(gh pr view {{.Issue.Number}} --json headRefName --jq '.headRefName')\\\" && git merge-base --is-ancestor origin/main HEAD && cd cli && go test ./...\"\n      retries: 0",
		},
	})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	var gateCommand string
	for _, args := range cmdRunner.outputArgs {
		if len(args) == 3 && args[0] == "sh" && args[1] == "-c" && strings.Contains(args[2], "gh pr view 42 --json headRefName --jq '.headRefName'") {
			gateCommand = args[2]
			break
		}
	}
	require.NotEmpty(t, gateCommand)
	assert.Contains(t, gateCommand, "git fetch origin main")
	assert.Contains(t, gateCommand, "git branch --show-current")
	assert.Contains(t, gateCommand, "gh pr view 42 --json headRefName --jq '.headRefName'")
	assert.NotContains(t, gateCommand, "{{")
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

	result, err := r.DrainAndWait(context.Background())
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

	first, err := r.DrainAndWait(context.Background())
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

	second, err := r.DrainAndWait(context.Background())
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

func TestDrainLabelGateAppliesWaitingLabels(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(1, "fix-bug")
	vessel.Meta["status_label_running"] = "in-progress"
	vessel.Meta["label_gate_label_waiting"] = "blocked"
	_, _ = q.Enqueue(vessel)

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "plan", promptContent: "Create plan", maxTurns: 5,
			gate: "      type: label\n      wait_for: \"plan-approved\"\n      timeout: \"24h\"",
		},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": &source.GitHub{Repo: "owner/repo", CmdRunner: cmdRunner},
	}

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Waiting != 1 {
		t.Fatalf("DrainAndWait().Waiting = %d, want 1", result.Waiting)
	}
	if !hasRunOutputCallContaining(cmdRunner, "gh issue edit 1 --repo owner/repo", "--add-label blocked") {
		t.Fatalf("expected waiting-label gh issue edit call, got %v", cmdRunner.outputArgs)
	}
	if !hasRunOutputCallContaining(cmdRunner, "gh issue edit 1 --repo owner/repo", "--remove-label in-progress") {
		t.Fatalf("expected running-label cleanup gh issue edit call, got %v", cmdRunner.outputArgs)
	}
}

func TestCheckWaitingVesselsAppliesReadyLabelsOnResume(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(1, "fix-bug")
	vessel.Meta["label_gate_label_waiting"] = "blocked"
	vessel.Meta["label_gate_label_ready"] = "ready-for-implementation"
	_, _ = q.Enqueue(vessel)

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

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": &source.GitHub{Repo: "owner/repo", CmdRunner: cmdRunner},
	}

	first, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("first Drain() error = %v", err)
	}
	if first.Waiting != 1 {
		t.Fatalf("first DrainAndWait().Waiting = %d, want 1", first.Waiting)
	}

	cmdRunner.outputData = []byte(`{"labels":[{"name":"plan-approved"}]}`)
	r.CheckWaitingVessels(context.Background())

	if !hasRunOutputCallContaining(cmdRunner, "gh issue edit 1 --repo owner/repo", "--add-label ready-for-implementation", "--remove-label blocked") {
		t.Fatalf("expected ready-label gh issue edit call, got %v", cmdRunner.outputArgs)
	}
}

func TestSmoke_S1_WarnsOnWorkflowDriftDuringResume(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "plan", promptContent: "Create plan", maxTurns: 5,
			gate: "      type: label\n      wait_for: \"plan-approved\"\n      timeout: \"24h\"",
		},
		{name: "implement", promptContent: "Implement after approval", maxTurns: 10},
	})
	withTestWorkingDir(t, dir)

	currentDigest := recovery.DigestFile(filepath.Join(".xylem", "workflows", "fix-bug.yaml"), "wf")
	require.NotEmpty(t, currentDigest)

	waitingSince := time.Now().UTC().Add(-time.Hour)
	vessel := makeVessel(1, "fix-bug")
	vessel.State = queue.StateWaiting
	vessel.FailedPhase = "plan"
	vessel.WaitingFor = "plan-approved"
	vessel.WaitingSince = &waitingSince
	vessel.CurrentPhase = 1
	vessel.Meta[recovery.MetaWorkflowDigest] = "wf-stale"
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	cmdRunner := &mockCmdRunner{outputData: []byte(`{"labels":[{"name":"plan-approved"}]}`)}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldWriter)

	r.CheckWaitingVessels(context.Background())

	resumed, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	require.Equal(t, queue.StatePending, resumed.State)
	assert.Contains(t, logBuf.String(), `warn: waiting vessel issue-1 workflow "fix-bug" drifted while waiting`)
	assert.Contains(t, logBuf.String(), "stored=wf-stale")
	assert.Contains(t, logBuf.String(), "current="+currentDigest)
}

func TestSmoke_S2_DoesNotWarnWhenWorkflowDigestMatches(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{
			name: "plan", promptContent: "Create plan", maxTurns: 5,
			gate: "      type: label\n      wait_for: \"plan-approved\"\n      timeout: \"24h\"",
		},
		{name: "implement", promptContent: "Implement after approval", maxTurns: 10},
	})
	withTestWorkingDir(t, dir)

	currentDigest := recovery.DigestFile(filepath.Join(".xylem", "workflows", "fix-bug.yaml"), "wf")
	require.NotEmpty(t, currentDigest)

	waitingSince := time.Now().UTC().Add(-time.Hour)
	vessel := makeVessel(1, "fix-bug")
	vessel.State = queue.StateWaiting
	vessel.FailedPhase = "plan"
	vessel.WaitingFor = "plan-approved"
	vessel.WaitingSince = &waitingSince
	vessel.CurrentPhase = 1
	vessel.Meta[recovery.MetaWorkflowDigest] = currentDigest
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	cmdRunner := &mockCmdRunner{outputData: []byte(`{"labels":[{"name":"plan-approved"}]}`)}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldWriter)

	r.CheckWaitingVessels(context.Background())

	resumed, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	require.Equal(t, queue.StatePending, resumed.State)
	assert.NotContains(t, logBuf.String(), "drifted while waiting")
	assert.NotContains(t, logBuf.String(), "stored="+currentDigest)
	assert.NotContains(t, logBuf.String(), "current="+currentDigest)
}

func TestWorkflowDigestDrifted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stored  string
		current string
		want    bool
	}{
		{name: "matching digests", stored: "wf-same", current: "wf-same", want: false},
		{name: "mismatched digests", stored: "wf-old", current: "wf-new", want: true},
		{name: "missing stored digest", stored: "", current: "wf-new", want: false},
		{name: "missing current digest", stored: "wf-old", current: "", want: false},
		{name: "whitespace is ignored", stored: " wf-same ", current: "wf-same", want: false},
		{name: "whitespace only stored digest is not comparable", stored: " \t\n ", current: "wf-new", want: false},
		{name: "whitespace only current digest is not comparable", stored: "wf-old", current: " \t\n ", want: false},
		{name: "different after trimming still drifts", stored: "\twf-old\n", current: " wf-new ", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := workflowDigestDrifted(tt.stored, tt.current); got != tt.want {
				t.Fatalf("workflowDigestDrifted(%q, %q) = %t, want %t", tt.stored, tt.current, got, tt.want)
			}
		})
	}
}

func TestCheckWaitingVesselsTimeoutAppliesTimedOutLabels(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	waitingSince := time.Now().UTC().Add(-48 * time.Hour)
	_, _ = q.Enqueue(queue.Vessel{
		ID:           "issue-1",
		Source:       "github-issue",
		Ref:          "https://github.com/owner/repo/issues/1",
		Workflow:     "fix-bug",
		Meta:         map[string]string{"issue_num": "1", "status_label_timed_out": "timed-out", "label_gate_label_waiting": "blocked"},
		State:        queue.StateWaiting,
		CreatedAt:    time.Now().UTC(),
		WaitingFor:   "plan-approved",
		WaitingSince: &waitingSince,
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": &source.GitHub{Repo: "owner/repo", CmdRunner: cmdRunner},
	}

	r.CheckWaitingVessels(context.Background())

	done, err := q.FindByID("issue-1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if done.State != queue.StateTimedOut {
		t.Fatalf("state after timeout = %s, want timed_out", done.State)
	}
	if !hasRunOutputCallContaining(cmdRunner, "gh issue edit 1 --repo owner/repo", "--add-label timed-out", "--remove-label blocked") {
		t.Fatalf("expected timed-out gh issue edit call, got %v", cmdRunner.outputArgs)
	}
}

func TestCheckWaitingVesselsResumeEmitsTraceSpan(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "fix-bug"))

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{{
		name: "plan", promptContent: "Create plan", maxTurns: 5,
		gate: "      type: label\n      wait_for: \"plan-approved\"\n      timeout: \"24h\"",
	}, {
		name: "implement", promptContent: "Implement after approval", maxTurns: 10,
	}})
	withTestWorkingDir(t, dir)

	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Tracer = tracer
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	first, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, first.Waiting)

	cmdRunner.outputData = []byte(`{"labels":[{"name":"plan-approved"}]}`)
	r.CheckWaitingVessels(context.Background())

	waitSpan := endedSpanByName(t, rec, "wait_transition:waiting")
	waitAttrs := spanAttrMap(waitSpan)
	assert.Equal(t, "waiting", waitAttrs["xylem.wait.transition"])
	assert.Equal(t, "plan-approved", waitAttrs["xylem.wait.label"])
	assert.Equal(t, "plan", waitAttrs["xylem.wait.phase"])

	resumeSpan := endedSpanByName(t, rec, "wait_transition:resumed")
	resumeAttrs := spanAttrMap(resumeSpan)
	assert.Equal(t, "resumed", resumeAttrs["xylem.wait.transition"])
	assert.Equal(t, "plan-approved", resumeAttrs["xylem.wait.label"])
	assert.Equal(t, "plan", resumeAttrs["xylem.wait.phase"])
}

func TestCheckWaitingVesselsTimeoutWritesTraceableSummary(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	waitingSince := time.Now().UTC().Add(-48 * time.Hour)
	_, _ = q.Enqueue(queue.Vessel{
		ID:           "issue-1",
		Source:       "github-issue",
		Ref:          "https://github.com/owner/repo/issues/1",
		Workflow:     "fix-bug",
		Meta:         map[string]string{"issue_num": "1"},
		State:        queue.StateWaiting,
		CreatedAt:    time.Now().UTC(),
		FailedPhase:  "plan",
		WaitingFor:   "plan-approved",
		WaitingSince: &waitingSince,
	})
	withTestWorkingDir(t, dir)

	tracer, rec := newTestTracer(t)
	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	r.Tracer = tracer
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	r.CheckWaitingVessels(context.Background())

	summary, err := LoadVesselSummary(cfg.StateDir, "issue-1")
	require.NoError(t, err)
	require.NotNil(t, summary.Trace)
	assert.Equal(t, "timed_out", summary.State)

	timeoutSpan := endedSpanByName(t, rec, "wait_transition:timed_out")
	assert.Equal(t, timeoutSpan.SpanContext().TraceID().String(), summary.Trace.TraceID)
	assert.Equal(t, timeoutSpan.SpanContext().SpanID().String(), summary.Trace.SpanID)
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	max := atomic.LoadInt32(&counter.maxSeen)
	if result.Completed != 4 {
		t.Fatalf("DrainAndWait().Completed = %d, want 4", result.Completed)
	}
	if max != 2 {
		t.Fatalf("max concurrent = %d, want exactly 2 to prove the limit is enforced without collapsing throughput", max)
	}
}

func TestDrainPerClassConcurrencyLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 3)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.ConcurrencyPerClass = map[string]int{
		"implement-feature": 1,
		"merge-pr":          2,
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	writeWorkflowFile(t, dir, "implement-feature", []testPhase{
		{name: "analyze", promptContent: "implement-feature", maxTurns: 5},
	})
	writeWorkflowFile(t, dir, "merge-pr", []testPhase{
		{name: "analyze", promptContent: "merge-pr", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	_, _ = q.Enqueue(makeVessel(1, "implement-feature"))
	_, _ = q.Enqueue(makeVessel(2, "implement-feature"))
	_, _ = q.Enqueue(makeVessel(3, "merge-pr"))
	_, _ = q.Enqueue(makeVessel(4, "merge-pr"))

	counter := &classCountingCmdRunner{delay: 50 * time.Millisecond}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, counter)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Completed != 4 {
		t.Fatalf("DrainAndWait().Completed = %d, want 4", result.Completed)
	}
	if counter.maxByClass["implement-feature"] != 1 {
		t.Fatalf("implement-feature max concurrent = %d, want 1", counter.maxByClass["implement-feature"])
	}
	if counter.maxByClass["merge-pr"] != 2 {
		t.Fatalf("merge-pr max concurrent = %d, want 2", counter.maxByClass["merge-pr"])
	}
	if counter.globalMax != 3 {
		t.Fatalf("global max concurrent = %d, want 3", counter.globalMax)
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

	result, err := r.DrainAndWait(ctx)
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	_, err := r.DrainAndWait(context.Background())
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

	_, err := r.DrainAndWait(context.Background())
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

			_, err := r.DrainAndWait(context.Background())
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
		args := buildPhaseArgs(cfg, "claude", mustProviderConfigForTest(t, cfg, "claude"), nil, nil, p, "med", "")

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
		args := buildPhaseArgs(cfg, "claude", mustProviderConfigForTest(t, cfg, "claude"), nil, nil, p, "med", "")

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
		args := buildPhaseArgs(cfg, "claude", mustProviderConfigForTest(t, cfg, "claude"), nil, nil, p, "med", "")

		var got []string
		for i := 0; i < len(args); i++ {
			if args[i] == "--allowedTools" && i+1 < len(args) {
				got = append(got, args[i+1])
			}
		}
		require.Equal(t, []string{"Read,Edit", "WebFetch"}, got)
	})

	t.Run("with harness", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5}
		args := buildPhaseArgs(cfg, "claude", mustProviderConfigForTest(t, cfg, "claude"), nil, nil, p, "med", "harness content")

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

func mustProviderConfigForTest(t *testing.T, cfg *config.Config, name string) config.ProviderConfig {
	t.Helper()
	provider, ok := providerConfigForName(cfg, name)
	if !ok {
		t.Fatalf("provider %q not configured", name)
	}
	return provider
}

func TestResolvePhaseAllowedTools(t *testing.T) {
	t.Run("derives defaults from diagnostic role", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		r := New(cfg, nil, nil, nil)
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5}

		got, err := r.resolvePhaseAllowedTools(nil, p, mustProviderConfigForTest(t, cfg, "claude"))
		require.NoError(t, err)
		require.Equal(t, "Bash,Glob,Grep,LS,Read,WebFetch,WebSearch", got)
	})

	t.Run("uses workflow class before phase-name heuristic", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		r := New(cfg, nil, nil, nil)
		wf := &workflow.Workflow{Class: policy.Delivery}
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5}

		got, err := r.resolvePhaseAllowedTools(wf, p, mustProviderConfigForTest(t, cfg, "claude"))
		require.NoError(t, err)
		require.Equal(t, "Bash,Glob,Grep,LS,Read,WebFetch,WebSearch,Edit,MultiEdit,Write", got)
	})

	t.Run("rejects unauthorized tool for default role", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		r := New(cfg, nil, nil, nil)
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5, AllowedTools: strPtrRunner("Edit")}

		_, err := r.resolvePhaseAllowedTools(nil, p, mustProviderConfigForTest(t, cfg, "claude"))
		require.Error(t, err)
		require.Contains(t, err.Error(), `role "diagnostic"`)
		require.Contains(t, err.Error(), `tool "Edit"`)
	})

	t.Run("phase role override wins over workflow class", func(t *testing.T) {
		baseCfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		baseRunner := New(baseCfg, nil, nil, nil)
		wf := &workflow.Workflow{Class: policy.Ops}
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5, AllowedTools: strPtrRunner("Edit")}

		_, err := baseRunner.resolvePhaseAllowedTools(wf, p, mustProviderConfigForTest(t, baseCfg, "claude"))
		require.Error(t, err)
		require.Contains(t, err.Error(), `role "housekeeping"`)

		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
			Harness: config.HarnessConfig{
				ToolPermissions: config.ToolPermissionsConfig{
					PhaseRoles: map[string]string{"analyze": "delivery"},
				},
			},
		}
		r := New(cfg, nil, nil, nil)

		got, err := r.resolvePhaseAllowedTools(wf, p, mustProviderConfigForTest(t, cfg, "claude"))
		require.NoError(t, err)
		require.Equal(t, "Edit", got)
	})

	t.Run("merges provider defaults with phase request", func(t *testing.T) {
		cfg := &config.Config{
			Providers: map[string]config.ProviderConfig{
				"claude": {
					Kind:         "claude",
					Command:      "claude",
					AllowedTools: []string{"WebFetch"},
				},
			},
		}
		r := New(cfg, nil, nil, nil)
		p := &workflow.Phase{Name: "analyze", MaxTurns: 5, AllowedTools: strPtrRunner("Read")}

		got, err := r.resolvePhaseAllowedTools(&workflow.Workflow{Class: policy.Delivery}, p, mustProviderConfigForTest(t, cfg, "claude"))
		require.NoError(t, err)
		require.Equal(t, "Read,WebFetch", got)
	})
}

func TestBuildPhaseArgsModelResolution(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		wf        *workflow.Workflow
		phase     *workflow.Phase
		tier      string
		wantModel string
	}{
		{
			name: "tier model comes from provider tiers",
			cfg: &config.Config{
				Providers: map[string]config.ProviderConfig{
					"claude": {Kind: "claude", Command: "claude", Tiers: map[string]string{"med": "claude-med"}},
				},
			},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5},
			tier:      "med",
			wantModel: "claude-med",
		},
		{
			name: "legacy workflow model overrides provider tier when tier unset",
			cfg: &config.Config{
				Claude: config.ClaudeConfig{Command: "claude", DefaultModel: "claude-default"},
			},
			wf:        &workflow.Workflow{Model: strPtr("workflow-model")},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5},
			tier:      "med",
			wantModel: "workflow-model",
		},
		{
			name: "phase tier suppresses legacy model override",
			cfg: &config.Config{
				Providers: map[string]config.ProviderConfig{
					"claude": {Kind: "claude", Command: "claude", Tiers: map[string]string{"high": "claude-high"}},
				},
			},
			wf:        &workflow.Workflow{Model: strPtr("legacy-workflow-model")},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5, Tier: strPtr("high")},
			tier:      "high",
			wantModel: "claude-high",
		},
		{
			name: "flags --model stripped when hierarchy resolves model",
			cfg: &config.Config{
				Providers: map[string]config.ProviderConfig{
					"claude": {Kind: "claude", Command: "claude", Flags: "--bare --model old-model --dangerously-skip-permissions", Tiers: map[string]string{"med": "workflow-model"}},
				},
			},
			wf:        &workflow.Workflow{Tier: strPtr("med")},
			phase:     &workflow.Phase{Name: "analyze", MaxTurns: 5, Tier: strPtr("med")},
			tier:      "med",
			wantModel: "workflow-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildPhaseArgs(tt.cfg, "claude", mustProviderConfigForTest(t, tt.cfg, "claude"), nil, tt.wf, tt.phase, tt.tier, "")

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
			flags := mustProviderConfigForTest(t, tt.cfg, "claude").Flags
			if tt.wantModel != "" && strings.Contains(flags, "--model") {
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

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("expected timed-out vessel to be marked failed, got completed=%d failed=%d", result.Completed, result.Failed)
	}
}

func TestResolveTier(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *config.Config
		vessel queue.Vessel
		wf     *workflow.Workflow
		phase  *workflow.Phase
		want   string
	}{
		{
			name:   "phase tier wins",
			cfg:    &config.Config{LLMRouting: config.LLMRoutingConfig{DefaultTier: "med"}},
			vessel: queue.Vessel{Tier: "low"},
			wf:     &workflow.Workflow{Tier: strPtr("high")},
			phase:  &workflow.Phase{Tier: strPtr("urgent")},
			want:   "urgent",
		},
		{
			name:   "workflow tier beats vessel",
			cfg:    &config.Config{LLMRouting: config.LLMRoutingConfig{DefaultTier: "med"}},
			vessel: queue.Vessel{Tier: "low"},
			wf:     &workflow.Workflow{Tier: strPtr("high")},
			want:   "high",
		},
		{
			name:   "vessel tier beats config default",
			cfg:    &config.Config{LLMRouting: config.LLMRoutingConfig{DefaultTier: "med"}},
			vessel: queue.Vessel{Tier: "low"},
			want:   "low",
		},
		{
			name: "config default used when no higher source",
			cfg:  &config.Config{LLMRouting: config.LLMRoutingConfig{DefaultTier: "high"}},
			want: "high",
		},
		{
			name: "hard default is med",
			cfg:  &config.Config{},
			want: "med",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveTier(tt.cfg, tt.vessel, tt.wf, tt.phase)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestResolveProviderChain(t *testing.T) {
	cfg := &config.Config{
		LLMRouting: config.LLMRoutingConfig{
			DefaultTier: "med",
			Tiers: map[string]config.TierRouting{
				"med":  {Providers: []string{"claude", "copilot"}},
				"high": {Providers: []string{"claude"}},
			},
		},
	}

	require.Equal(t, []string{"claude"}, resolveProviderChain(cfg, "high"))
	require.Equal(t, []string{"claude", "copilot"}, resolveProviderChain(cfg, "missing"))

	legacy := &config.Config{LLM: "copilot"}
	require.Equal(t, []string{"copilot"}, resolveProviderChain(legacy, "med"))
}

func TestModelForProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"claude":  {Kind: "claude", Tiers: map[string]string{"high": "claude-opus"}},
			"copilot": {Kind: "copilot", Tiers: map[string]string{"med": "gpt-5.2-codex"}},
		},
	}
	require.Equal(t, "claude-opus", modelForProvider(cfg, "claude", "high"))
	require.Equal(t, "gpt-5.2-codex", modelForProvider(cfg, "copilot", "med"))

	legacy := &config.Config{
		LLMRouting: config.LLMRoutingConfig{DefaultTier: "med"},
		Claude:     config.ClaudeConfig{DefaultModel: "claude-default"},
	}
	require.Equal(t, "claude-default", modelForProvider(legacy, "claude", "med"))
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
				LLM:     "copilot",
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
			args := buildCopilotPhaseArgs(tt.cfg, "copilot", mustProviderConfigForTest(t, tt.cfg, "copilot"), nil, tt.wf, tt.phase, "med", tt.harness, tt.prompt)

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
		args := buildCopilotPhaseArgs(cfg, "copilot", mustProviderConfigForTest(t, cfg, "copilot"), nil, nil, phase, "med", "harness text", "user prompt")
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
		Providers: map[string]config.ProviderConfig{
			"claude":  {Kind: "claude", Command: claudeCmd, Tiers: map[string]string{"med": "claude-med"}},
			"copilot": {Kind: "copilot", Command: copilotCmd, Tiers: map[string]string{"med": "gpt-med"}},
		},
	}
	phase := &workflow.Phase{MaxTurns: 5}

	t.Run("claude provider returns claude command and stdin", func(t *testing.T) {
		cmd, args, stdin, model, err := buildProviderPhaseArgs(cfg, mustProviderConfigForTest(t, cfg, "claude"), nil, nil, phase, "", "claude", "med", "test prompt", 1)
		require.NoError(t, err)
		if cmd != claudeCmd {
			t.Errorf("cmd = %q, want %q", cmd, claudeCmd)
		}
		require.Equal(t, "claude-med", model)
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
		cmd, args, stdin, model, err := buildProviderPhaseArgs(cfg, mustProviderConfigForTest(t, cfg, "copilot"), nil, nil, phase, "", "copilot", "med", "test prompt", 1)
		require.NoError(t, err)
		if cmd != copilotCmd {
			t.Errorf("cmd = %q, want %q", cmd, copilotCmd)
		}
		require.Equal(t, "gpt-med", model)
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
		Providers: map[string]config.ProviderConfig{
			"claude":  {Kind: "claude", Command: "claude", Tiers: map[string]string{"med": "claude-med"}},
			"copilot": {Kind: "copilot", Command: "copilot", Tiers: map[string]string{"med": "gpt-med"}},
		},
	}
	phase := &workflow.Phase{
		Name:       "implement",
		PromptFile: ".xylem/prompts/fix-bug/implement.md",
		MaxTurns:   5,
	}

	_, claudeArgs, _, _, err := buildProviderPhaseArgs(cfg, mustProviderConfigForTest(t, cfg, "claude"), nil, nil, phase, "", "claude", "med", "prompt", 2)
	require.NoError(t, err)
	if !containsArgSequence(claudeArgs, "--dtu-phase", "implement") {
		t.Fatalf("claude args missing DTU phase: %v", claudeArgs)
	}
	if !containsArgSequence(claudeArgs, "--dtu-script", "implement") {
		t.Fatalf("claude args missing DTU script: %v", claudeArgs)
	}
	if !containsArgSequence(claudeArgs, "--dtu-attempt", "2") {
		t.Fatalf("claude args missing DTU attempt: %v", claudeArgs)
	}

	_, copilotArgs, _, _, err := buildProviderPhaseArgs(cfg, mustProviderConfigForTest(t, cfg, "copilot"), nil, nil, phase, "", "copilot", "med", "prompt", 3)
	require.NoError(t, err)
	if !containsArgSequence(copilotArgs, "--dtu-attempt", "3") {
		t.Fatalf("copilot args missing DTU attempt: %v", copilotArgs)
	}
}

func TestRunPromptInvocationToolPermissions(t *testing.T) {
	t.Run("derived tools are passed to provider", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		cmdRunner := &mockCmdRunner{}
		r := New(cfg, nil, nil, cmdRunner)

		_, _, _, _, err := r.runPromptInvocation(context.Background(), queue.Vessel{ID: "v1"}, dir, nil, &workflow.Workflow{Class: policy.Delivery}, &workflow.Phase{Name: "analyze", MaxTurns: 5}, "", "prompt body", 1)
		require.NoError(t, err)
		require.Len(t, cmdRunner.phaseCalls, 1)
		assert.Contains(t, cmdRunner.phaseCalls[0].args, "--allowedTools")
		assert.Contains(t, cmdRunner.phaseCalls[0].args, "Bash,Glob,Grep,LS,Read,WebFetch,WebSearch,Edit,MultiEdit,Write")
	})

	t.Run("unauthorized tool fails before subprocess", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{
			Claude: config.ClaudeConfig{Command: "claude"},
		}
		cmdRunner := &mockCmdRunner{}
		r := New(cfg, nil, nil, cmdRunner)

		_, _, _, _, err := r.runPromptInvocation(context.Background(), queue.Vessel{ID: "v1"}, dir, nil, nil, &workflow.Phase{Name: "analyze", MaxTurns: 5, AllowedTools: strPtrRunner("Edit")}, "", "prompt body", 1)
		require.Error(t, err)
		require.Empty(t, cmdRunner.phaseCalls)
	})
}

func TestSmoke_S3_DeliveryPhaseLaunchPassesResolvedAllowedTools(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Command: "claude"},
	}
	cmdRunner := &mockCmdRunner{}
	r := New(cfg, nil, nil, cmdRunner)
	wf := &workflow.Workflow{Class: policy.Delivery}
	phase := &workflow.Phase{Name: "analyze", MaxTurns: 5}

	output, promptForCost, provider, model, err := r.runPromptInvocation(context.Background(), queue.Vessel{ID: "v1"}, dir, nil, wf, phase, "", "prompt body", 1)
	require.NoError(t, err)

	assert.Equal(t, []byte("mock output"), output)
	assert.Equal(t, "prompt body", promptForCost)
	assert.Equal(t, "claude", provider)
	assert.Equal(t, "", model)
	require.Len(t, cmdRunner.phaseCalls, 1)

	call := cmdRunner.phaseCalls[0]
	assert.Equal(t, dir, call.dir)
	assert.Equal(t, "claude", call.name)
	assert.Contains(t, call.args, "--allowedTools")
	assert.Contains(t, call.args, "Bash,Glob,Grep,LS,Read,WebFetch,WebSearch,Edit,MultiEdit,Write")
	assert.Equal(t, "prompt body", call.prompt)
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
		_, args := buildPromptOnlyCmdArgs(cfg, "copilot", "med", "test prompt")
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
		_, args := buildPromptOnlyCmdArgs(cfg, "copilot", "med", "new prompt")
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

	result, err := r.DrainAndWait(context.Background())
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
	outputPath := config.RuntimePath(filepath.Join(dir, ".xylem"), "phases", "issue-1", "build.output")
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
	commandPath := config.RuntimePath(filepath.Join(dir, ".xylem"), "phases", "issue-1", "build.command")
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

func assertCommandPhaseOutputAvailableToNextPrompt(t *testing.T, workflowName string) {
	t.Helper()

	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, workflowName))
	require.NoError(t, err)

	writeWorkflowFile(t, dir, workflowName, []testPhase{
		{name: "merge_main", phaseType: "command", run: "git merge origin/main --no-commit --no-ff"},
		{name: "analyze", promptContent: "Merge output: {{.PreviousOutputs.merge_main}}", maxTurns: 10},
	})

	withTestWorkingDir(t, dir)

	const mergeOutput = "Auto-merging cli/internal/runner/runner.go\nCONFLICT (content): Merge conflict in cli/internal/runner/runner.go\n"
	var sawInterpolatedMergeOutput bool

	cmdRunner := &mockCmdRunner{
		gateOutput: []byte(mergeOutput),
		runPhaseHook: func(_ string, prompt string, _ string, _ ...string) ([]byte, error, bool) {
			sawInterpolatedMergeOutput = strings.Contains(prompt, "Merge output: "+mergeOutput)
			return []byte("analyzed conflicts"), nil, true
		},
	}
	wt := &mockWorktree{}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
	require.Len(t, cmdRunner.phaseCalls, 1)
	assert.True(t, sawInterpolatedMergeOutput, "prompt = %q, want command phase output", cmdRunner.phaseCalls[0].prompt)
}

func TestDrainCommandPhaseOutputAvailableToNextPrompt(t *testing.T) {
	assertCommandPhaseOutputAvailableToNextPrompt(t, "fix-bug")
}

func TestSmoke_S8_ResolveConflictsDeterministicMergeOutputFeedsAnalysisPrompt(t *testing.T) {
	assertCommandPhaseOutputAvailableToNextPrompt(t, "resolve-conflicts")
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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
			"events-source":    &source.GitHubPREvents{Repo: "owner/config-repo"},
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

	got := r.resolveRepo(queue.Vessel{
		Source: "github-pr-events",
		Meta:   map[string]string{"config_source": "events-source"},
	})
	if got != "owner/config-repo" {
		t.Errorf("resolveRepo(config_source) = %q, want %q", got, "owner/config-repo")
	}
}

func TestResolveSourcePrefersConfigSourceForScheduleVessel(t *testing.T) {
	r := &Runner{
		Sources: map[string]source.Source{
			"schedule": &source.Schedule{ConfigName: "fallback"},
			"doctor":   &source.Schedule{ConfigName: "doctor"},
		},
	}

	resolved, ok := r.resolveSourceForVessel(queue.Vessel{
		Source: "schedule",
		Meta: map[string]string{
			"config_source":        "doctor",
			"schedule.fired_at":    "2026-04-09T06:00:00Z",
			"schedule.cadence":     "1h",
			"schedule.source_name": "doctor",
		},
	}).(*source.Schedule)
	if !ok {
		t.Fatalf("resolveSourceForVessel() returned %T, want *source.Schedule", r.resolveSourceForVessel(queue.Vessel{
			Source: "schedule",
			Meta:   map[string]string{"config_source": "doctor"},
		}))
	}
	if resolved.ConfigName != "doctor" {
		t.Fatalf("resolved.ConfigName = %q, want doctor", resolved.ConfigName)
	}
}

func TestResolveRepoPrefersConfigSource(t *testing.T) {
	r := &Runner{
		Config: &config.Config{
			Sources: map[string]config.SourceConfig{
				"sota-gap": {Type: "scheduled", Repo: "owner/primary-repo"},
			},
		},
		Sources: map[string]source.Source{
			"scheduled": &source.Scheduled{Repo: "owner/fallback-repo"},
			"sota-gap":  &source.Scheduled{Repo: "owner/primary-repo"},
		},
	}

	got := r.resolveRepo(queue.Vessel{
		Source: "scheduled",
		Meta:   map[string]string{"config_source": "sota-gap"},
	})
	if got != "owner/primary-repo" {
		t.Fatalf("resolveRepo() = %q, want owner/primary-repo", got)
	}
}

func TestSmoke_S4_BuildTemplateDataExposesRepoAndValidation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.DefaultBranch = "trunk"
	cfg.Validation = config.ValidationConfig{
		Format: "fmt ./...",
		Lint:   "lint ./...",
		Build:  "build ./...",
		Test:   "test ./...",
	}
	cfg.Sources["harness-merge"] = config.SourceConfig{
		Type: "github-pr",
		Repo: "owner/config-repo",
		Tasks: map[string]config.Task{
			"merge-ready": {Labels: []string{"ready"}, Workflow: "merge-pr"},
		},
	}
	r := &Runner{
		Config: cfg,
		Sources: map[string]source.Source{
			"github-pr":     &source.GitHubPR{Repo: "owner/runtime-repo"},
			"harness-merge": &source.GitHubPR{Repo: "owner/config-repo"},
		},
	}

	vessel := queue.Vessel{
		ID:     "pr-42",
		Ref:    "https://github.com/owner/config-repo/pull/42",
		Source: "github-pr",
		Meta: map[string]string{
			"config_source":        "harness-merge",
			"schedule.source_name": "security-compliance",
		},
	}
	td := r.buildTemplateData(vessel, nil, phase.IssueData{Number: 42}, "merge", 0, nil, "", phase.EvaluationData{})

	rendered, err := renderCommandTemplate("merge", "command", "gh pr merge {{.Issue.Number}} --repo {{.Repo.Slug}} && echo {{.Source.Name}} {{.Source.Repo}} {{.Repo.DefaultBranch}} {{.Validation.Format}} {{.Validation.Lint}} {{.Validation.Build}} {{.Validation.Test}} {{.Vessel.Ref}} {{index .Vessel.Meta \"schedule.source_name\"}}", td)
	require.NoError(t, err)
	assert.Contains(t, rendered, "gh pr merge 42 --repo owner/config-repo")
	assert.Contains(t, rendered, "echo harness-merge owner/config-repo trunk fmt ./... lint ./... build ./... test ./... https://github.com/owner/config-repo/pull/42 security-compliance")
}

func TestResolveSourcePrefersConfigSource(t *testing.T) {
	primary := &source.Scheduled{Repo: "owner/primary-repo"}
	fallback := &source.Scheduled{Repo: "owner/fallback-repo"}
	r := &Runner{
		Sources: map[string]source.Source{
			"scheduled": fallback,
			"sota-gap":  primary,
		},
	}

	got := r.resolveSourceForVessel(queue.Vessel{
		Source: "scheduled",
		Meta:   map[string]string{"config_source": "sota-gap"},
	})
	if got != primary {
		t.Fatalf("resolveSourceForVessel() returned %T %p, want %T %p", got, got, primary, primary)
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

	result, err := r.DrainAndWait(context.Background())
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
		path := config.RuntimePath(cfg.StateDir, "phases", "issue-1", name+".output")
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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
	return m.RunPhaseWithEnv(context.Background(), dir, nil, stdin, name, args...)
}

func (m *perPhaseCmdRunner) RunPhaseWithEnv(_ context.Context, dir string, _ []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
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

	result, err := r.DrainAndWait(context.Background())
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

			result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("expected 1 failed vessel, got failed=%d completed=%d", result.Failed, result.Completed)
	}
	if got := countRunOutputCalls(cmdRunner, "sh"); got != 0 {
		t.Fatalf("countRunOutputCalls(sh) = %d, want 0 command invocations", got)
	}
	if len(cmdRunner.phaseCalls) != 0 {
		t.Fatalf("len(phaseCalls) = %d, want 0 dependent phase invocations", len(cmdRunner.phaseCalls))
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !strings.Contains(vessels[0].Error, "denied by policy") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessels[0].Error, "denied by policy")
	}
	if !strings.Contains(vessels[0].Error, ".xylem.yml") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessels[0].Error, ".xylem.yml")
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
	found := false
	for _, entry := range entries {
		if entry.Intent.Action != "file_write" || entry.Intent.Resource != ".xylem.yml" {
			continue
		}
		found = true
		if entry.Decision != intermediary.Deny {
			t.Fatalf("file_write entry decision = %q, want %q", entry.Decision, intermediary.Deny)
		}
		if entry.RuleMatched != "delivery.no_control_plane_writes" {
			t.Fatalf("file_write entry rule = %q, want %q", entry.RuleMatched, "delivery.no_control_plane_writes")
		}
		if entry.Intent.Metadata["phase"] != "tamper" {
			t.Fatalf("file_write entry phase metadata = %q, want tamper", entry.Intent.Metadata["phase"])
		}
		if entry.Operation != "write_control_plane" {
			t.Fatalf("file_write entry operation = %q, want %q", entry.Operation, "write_control_plane")
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
	r := New(cfg, queue.New(config.RuntimePath(stateDir, "queue.jsonl")), &mockWorktree{path: worktreeDir}, &mockCmdRunner{})
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
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
	r := New(cfg, queue.New(config.RuntimePath(stateDir, "queue.jsonl")), &mockWorktree{path: worktreeDir}, &mockCmdRunner{})
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
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

func TestTakeProtectedSurfaceSnapshotRestoresMissingProtectedFilesFromSourceRoot(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeDir := filepath.Join(repoRoot, ".claude", "worktrees", "review", "pr-1")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".xylem", "workflows"), 0o755); err != nil {
		t.Fatalf("MkdirAll(workflows) = %v", err)
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".xylem.yml"), []byte("repo: owner/repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.xylem.yml) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".xylem", "workflows", "fix-bug.yaml"), []byte("name: fix-bug\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(workflow) = %v", err)
	}

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name != "git" {
				return nil, nil, false
			}
			if len(args) == 5 &&
				args[0] == "-C" &&
				args[1] == worktreeDir &&
				args[2] == "rev-parse" &&
				args[3] == "--path-format=absolute" &&
				args[4] == "--git-common-dir" {
				return []byte(filepath.Join(repoRoot, ".git")), nil, true
			}
			return nil, nil, false
		},
	}

	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, cmdRunner)
	snapshot, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	if err != nil {
		t.Fatalf("takeProtectedSurfaceSnapshot() error = %v", err)
	}
	if !ok {
		t.Fatalf("takeProtectedSurfaceSnapshot() checkProtectedSurfaces = false, want true")
	}

	restoredConfig := filepath.Join(worktreeDir, ".xylem.yml")
	if _, err := os.Stat(restoredConfig); err != nil {
		t.Fatalf("restored .xylem.yml missing: %v", err)
	}
	restoredWorkflow := filepath.Join(worktreeDir, ".xylem", "workflows", "fix-bug.yaml")
	if _, err := os.Stat(restoredWorkflow); err != nil {
		t.Fatalf("restored workflow missing: %v", err)
	}
	if len(snapshot.Files) != 2 {
		t.Fatalf("len(snapshot.Files) = %d, want 2", len(snapshot.Files))
	}
}

func TestVerifyProtectedSurfacesSelfHealsDeletedFileFromDefaultBranch(t *testing.T) {
	worktreeDir := t.TempDir()
	protectedFile := filepath.Join(worktreeDir, ".xylem.yml")
	if err := os.WriteFile(protectedFile, []byte("repo: owner/repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.xylem.yml) = %v", err)
	}

	before, err := surface.TakeSnapshot(worktreeDir, []string{".xylem.yml"})
	if err != nil {
		t.Fatalf("TakeSnapshot(before) = %v", err)
	}
	if err := os.Remove(protectedFile); err != nil {
		t.Fatalf("Remove(.xylem.yml) = %v", err)
	}

	cfg := makeTestConfig(t.TempDir(), 1)
	cfg.StateDir = t.TempDir()
	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name != "git" {
				return nil, nil, false
			}
			command := strings.Join(append([]string{name}, args...), " ")
			switch command {
			case "git -C " + worktreeDir + " checkout -- .xylem.yml":
				return nil, errors.New("path missing from HEAD"), true
			case "git -C " + worktreeDir + " symbolic-ref refs/remotes/origin/HEAD":
				return []byte("refs/remotes/origin/main\n"), nil, true
			case "git -C " + worktreeDir + " checkout origin/main -- .xylem.yml":
				if err := os.WriteFile(protectedFile, []byte("repo: owner/repo\n"), 0o644); err != nil {
					return nil, err, true
				}
				return []byte{}, nil, true
			default:
				return nil, nil, false
			}
		},
	}
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	r := New(cfg, queue.New(config.RuntimePath(cfg.StateDir, "queue.jsonl")), &mockWorktree{path: worktreeDir}, cmdRunner)
	r.AuditLog = auditLog

	err = r.verifyProtectedSurfaces(
		queue.Vessel{ID: "issue-self-heal", Source: "github-issue", Workflow: "fix-bug"},
		workflow.Phase{Name: "analyze"},
		worktreeDir,
		before,
	)
	if err == nil {
		t.Fatal("verifyProtectedSurfaces() returned nil, want violation error")
	}
	if !strings.Contains(err.Error(), "violated protected surfaces") {
		t.Fatalf("verifyProtectedSurfaces() error = %q, want violation", err)
	}
	data, readErr := os.ReadFile(protectedFile)
	if readErr != nil {
		t.Fatalf("ReadFile(restored .xylem.yml) = %v", readErr)
	}
	if string(data) != "repo: owner/repo\n" {
		t.Fatalf("restored .xylem.yml = %q, want canonical contents", string(data))
	}
	info, statErr := os.Stat(protectedFile)
	if statErr != nil {
		t.Fatalf("Stat(restored .xylem.yml) = %v", statErr)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("restored .xylem.yml perms = %#o, want 0444", info.Mode().Perm())
	}
}

// TestVerifyProtectedSurfacesSuppressesTransientDeletionsViaPreVerifyRestore
// documents the fix for issue #174: when a phase temporarily removes protected
// files (e.g., resolve-conflicts's gh pr checkout on a pre-#157 PR branch),
// the pre-verify restore step should copy them back from the source root
// BEFORE taking the after-snapshot, so the Compare sees them present and
// emits zero "deleted" violations. Modifications (present-but-different) are
// still caught because restore only fires on absent files.
func TestVerifyProtectedSurfacesSuppressesTransientDeletionsViaPreVerifyRestore(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeDir := filepath.Join(repoRoot, ".claude", "worktrees", "review", "pr-test")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".xylem", "workflows"), 0o755); err != nil {
		t.Fatalf("MkdirAll(workflows) = %v", err)
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree) = %v", err)
	}
	// Give the worktree a .git directory so the exclude-entry path in
	// copyProtectedSurfaceFile can succeed (mirrors a regular worktree).
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".git", "info"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree .git) = %v", err)
	}
	// Source root has the canonical files.
	canonicalConfig := []byte("repo: owner/repo\n")
	canonicalWorkflow := []byte("name: fix-bug\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".xylem.yml"), canonicalConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(.xylem.yml) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".xylem", "workflows", "fix-bug.yaml"), canonicalWorkflow, 0o644); err != nil {
		t.Fatalf("WriteFile(workflow) = %v", err)
	}

	// Seed the worktree with the same files — these represent the pre-phase
	// snapshot state.
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".xylem", "workflows"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree workflows) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), canonicalConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .xylem.yml) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem", "workflows", "fix-bug.yaml"), canonicalWorkflow, 0o644); err != nil {
		t.Fatalf("WriteFile(worktree workflow) = %v", err)
	}

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name != "git" {
				return nil, nil, false
			}
			// Source-root resolution: return the canonical repo root's .git
			if len(args) == 5 &&
				args[0] == "-C" &&
				args[1] == worktreeDir &&
				args[2] == "rev-parse" &&
				args[3] == "--path-format=absolute" &&
				args[4] == "--git-common-dir" {
				return []byte(filepath.Join(repoRoot, ".git")), nil, true
			}
			return nil, nil, false
		},
	}

	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, cmdRunner)

	// Take the before-snapshot while the files are present.
	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	if err != nil {
		t.Fatalf("takeProtectedSurfaceSnapshot() error = %v", err)
	}
	if !ok {
		t.Fatalf("takeProtectedSurfaceSnapshot() checkProtectedSurfaces = false, want true")
	}
	if len(before.Files) < 2 {
		t.Fatalf("before.Files = %d, want >= 2", len(before.Files))
	}

	// Simulate a phase that transiently removes the protected files (e.g.,
	// resolve-conflicts workflow's gh pr checkout on a pre-#157 PR branch).
	if err := os.Remove(filepath.Join(worktreeDir, ".xylem.yml")); err != nil {
		t.Fatalf("Remove(.xylem.yml) = %v", err)
	}
	if err := os.Remove(filepath.Join(worktreeDir, ".xylem", "workflows", "fix-bug.yaml")); err != nil {
		t.Fatalf("Remove(workflow) = %v", err)
	}

	// verifyProtectedSurfaces should NOT return a violation: pre-verify
	// restore copies the missing files from the source root, the
	// after-snapshot sees them present, and Compare finds no diffs.
	err = r.verifyProtectedSurfaces(
		queue.Vessel{ID: "issue-transient-delete", Source: "github-pr", Workflow: "resolve-conflicts"},
		workflow.Phase{Name: "analyze"},
		worktreeDir,
		before,
	)
	if err != nil {
		t.Fatalf("verifyProtectedSurfaces() returned violation %v, want nil (pre-verify restore should suppress transient deletions)", err)
	}

	// Sanity: verify the files were actually restored to the worktree.
	data, readErr := os.ReadFile(filepath.Join(worktreeDir, ".xylem.yml"))
	if readErr != nil {
		t.Fatalf("restored .xylem.yml missing after verify: %v", readErr)
	}
	if string(data) != string(canonicalConfig) {
		t.Fatalf("restored .xylem.yml = %q, want %q", string(data), string(canonicalConfig))
	}
	if _, statErr := os.Stat(filepath.Join(worktreeDir, ".xylem", "workflows", "fix-bug.yaml")); statErr != nil {
		t.Fatalf("restored workflow missing after verify: %v", statErr)
	}
}

// TestVerifyProtectedSurfacesStillCatchesModificationsAfterPreVerifyRestore
// ensures the pre-verify restore does NOT mask modifications: a file that's
// present-but-different should still cause a violation because the restore
// only touches absent files.
func TestVerifyProtectedSurfacesStillCatchesModificationsAfterPreVerifyRestore(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeDir := filepath.Join(repoRoot, ".claude", "worktrees", "review", "pr-mod")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) = %v", err)
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree) = %v", err)
	}
	canonical := []byte("canonical content\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".xylem.yml"), canonical, 0o644); err != nil {
		t.Fatalf("WriteFile source = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), canonical, 0o644); err != nil {
		t.Fatalf("WriteFile worktree = %v", err)
	}

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "git" && len(args) >= 5 && args[2] == "rev-parse" && args[4] == "--git-common-dir" {
				return []byte(filepath.Join(repoRoot, ".git")), nil, true
			}
			return nil, nil, false
		},
	}
	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, cmdRunner)
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	if err != nil || !ok {
		t.Fatalf("takeProtectedSurfaceSnapshot error = %v ok = %v", err, ok)
	}

	// Simulate a phase that MODIFIES the file (doesn't delete it).
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), []byte("modified by rogue agent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile modify = %v", err)
	}

	err = r.verifyProtectedSurfaces(
		queue.Vessel{ID: "issue-mod-check", Source: "github-issue", Workflow: "fix-bug"},
		workflow.Phase{Name: "implement"},
		worktreeDir,
		before,
	)
	if err == nil {
		t.Fatal("verifyProtectedSurfaces() returned nil, want violation for modified file")
	}
	if !strings.Contains(err.Error(), "violated protected surfaces") {
		t.Fatalf("error = %q, want containing 'violated protected surfaces'", err)
	}
	if !strings.Contains(err.Error(), ".xylem.yml") {
		t.Fatalf("error = %q, want containing '.xylem.yml' path", err)
	}
}

func TestSmoke_S21_SurfacePostVerificationAllowsOptedInAdditiveWrites(t *testing.T) {
	repoRoot := t.TempDir()
	withTestWorkingDir(t, repoRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "prompts", "doctor"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "prompts", "doctor", "analyze.md"), []byte("analyze"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "workflows", "doctor.yaml"), []byte(`name: doctor
allow_additive_protected_writes: true
phases:
  - name: analyze
    prompt_file: .xylem/prompts/doctor/analyze.md
    max_turns: 1
`), 0o644))

	worktreeDir := filepath.Join(repoRoot, "worktree")
	require.NoError(t, os.MkdirAll(worktreeDir, 0o755))

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	require.NoError(t, os.MkdirAll(cfg.StateDir, 0o755))
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, &mockCmdRunner{})
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, os.MkdirAll(filepath.Join(worktreeDir, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktreeDir, ".xylem", "workflows", "doctor.yaml"), []byte("name: doctor\n"), 0o644))

	err = r.verifyProtectedSurfaces(
		queue.Vessel{ID: "issue-additive-allowed", Source: "manual", Workflow: "doctor"},
		workflow.Phase{Name: "implement"},
		worktreeDir,
		before,
	)
	require.NoError(t, err)

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func setupCanonicalProtectedWriteSmokeTest(t *testing.T) (*Runner, *intermediary.AuditLog, string, string, surface.Snapshot) {
	t.Helper()

	repoRoot := t.TempDir()
	withTestWorkingDir(t, repoRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "prompts", "doctor"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "prompts", "doctor", "analyze.md"), []byte("analyze"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "workflows", "doctor.yaml"), []byte(`name: doctor
allow_canonical_protected_writes: true
phases:
  - name: analyze
    prompt_file: .xylem/prompts/doctor/analyze.md
    max_turns: 1
`), 0o644))

	worktreeDir := filepath.Join(repoRoot, "worktree")
	protectedRelPath := ".xylem/workflows/doctor.yaml"
	protectedPath := filepath.Join(worktreeDir, filepath.FromSlash(protectedRelPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(protectedPath), 0o755))
	require.NoError(t, os.WriteFile(protectedPath, []byte("name: doctor\nphases: []\n"), 0o644))

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	require.NoError(t, os.MkdirAll(cfg.StateDir, 0o755))
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, &mockCmdRunner{})
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	require.NoError(t, err)
	require.True(t, ok)

	return r, auditLog, worktreeDir, protectedPath, before
}

func TestSmoke_S21_SurfacePostVerificationAllowsReferencedCanonicalProtectedWrite(t *testing.T) {
	r, auditLog, worktreeDir, protectedPath, before := setupCanonicalProtectedWriteSmokeTest(t)

	modifiedContents := "name: doctor\nupdated: true\n"
	require.NoError(t, os.WriteFile(protectedPath, []byte(modifiedContents), 0o644))

	err := r.verifyProtectedSurfaces(
		queue.Vessel{
			ID:       "issue-canonical-allowed",
			Source:   "github-issue",
			Workflow: "doctor",
			Meta: map[string]string{
				"issue_body": "Please fix `.xylem/workflows/doctor.yaml` so the harness workflow stops failing.",
			},
		},
		workflow.Phase{Name: "implement"},
		worktreeDir,
		before,
	)
	require.NoError(t, err)

	data, readErr := os.ReadFile(protectedPath)
	require.NoError(t, readErr)
	assert.Equal(t, modifiedContents, string(data))

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestSmoke_S23_AuditLogRecordsDeniedCanonicalProtectedWriteWithoutIssuePathReference(t *testing.T) {
	r, auditLog, worktreeDir, protectedPath, before := setupCanonicalProtectedWriteSmokeTest(t)

	modifiedContents := "name: doctor\nupdated: true\n"
	require.NoError(t, os.WriteFile(protectedPath, []byte(modifiedContents), 0o644))

	err := r.verifyProtectedSurfaces(
		queue.Vessel{
			ID:       "issue-canonical-denied",
			Source:   "github-issue",
			Workflow: "doctor",
			Meta: map[string]string{
				"issue_body": "Please fix the harness workflow bug.",
			},
		},
		workflow.Phase{Name: "implement"},
		worktreeDir,
		before,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "violated protected surfaces")
	assert.Contains(t, err.Error(), ".xylem/workflows/doctor.yaml")

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "file_write", entries[0].Intent.Action)
	assert.Equal(t, ".xylem/workflows/doctor.yaml", entries[0].Intent.Resource)
	assert.Equal(t, intermediary.Deny, entries[0].Decision)
}

func TestSmoke_S23_AuditLogRecordsDeniedAdditiveProtectedWriteWithoutWorkflowOptIn(t *testing.T) {
	repoRoot := t.TempDir()
	withTestWorkingDir(t, repoRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "prompts", "doctor"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "prompts", "doctor", "analyze.md"), []byte("analyze"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "workflows", "doctor.yaml"), []byte(`name: doctor
phases:
  - name: analyze
    prompt_file: .xylem/prompts/doctor/analyze.md
    max_turns: 1
`), 0o644))

	worktreeDir := filepath.Join(repoRoot, "worktree")
	require.NoError(t, os.MkdirAll(worktreeDir, 0o755))

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	require.NoError(t, os.MkdirAll(cfg.StateDir, 0o755))
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, &mockCmdRunner{})
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, os.MkdirAll(filepath.Join(worktreeDir, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktreeDir, ".xylem", "workflows", "doctor.yaml"), []byte("name: doctor\n"), 0o644))

	err = r.verifyProtectedSurfaces(
		queue.Vessel{ID: "issue-additive-denied", Source: "manual", Workflow: "doctor"},
		workflow.Phase{Name: "implement"},
		worktreeDir,
		before,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "violated protected surfaces")
	assert.Contains(t, err.Error(), ".xylem/workflows/doctor.yaml")
	assert.Contains(t, err.Error(), "before: absent")

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "file_write", entries[0].Intent.Action)
	assert.Equal(t, ".xylem/workflows/doctor.yaml", entries[0].Intent.Resource)
	assert.Equal(t, intermediary.Deny, entries[0].Decision)
}

func TestRecordProtectedSurfaceViolations_OmitsWorkflowClassWhenAuditLookupFails(t *testing.T) {
	repoRoot := t.TempDir()
	withTestWorkingDir(t, repoRoot)

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	require.NoError(t, os.MkdirAll(cfg.StateDir, 0o755))

	worktreeDir := filepath.Join(repoRoot, "worktree")
	require.NoError(t, os.MkdirAll(worktreeDir, 0o755))

	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, &mockCmdRunner{})
	r.AuditLog = auditLog

	err := r.recordProtectedSurfaceViolations(
		queue.Vessel{ID: "issue-missing-workflow", Source: "manual", Workflow: "missing-workflow"},
		workflow.Phase{Name: "tamper"},
		worktreeDir,
		"violated protected surfaces",
		[]surface.Violation{{Path: ".xylem.yml", Before: "abc", After: "xyz"}},
	)
	require.NoError(t, err)

	entries, err := auditLog.Entries()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "", entries[0].WorkflowClass)
	assert.Equal(t, "", entries[0].RuleMatched)
}

// TestCopyProtectedSurfaceFileAddsWorktreeExcludeEntry verifies that the
// pre-verify restore's copyProtectedSurfaceFile helper appends the restored
// path to .git/info/exclude, so subsequent `git add -A` in the push phase
// of resolve-conflicts won't stage the restored file into the PR commit.
func TestCopyProtectedSurfaceFileAddsWorktreeExcludeEntry(t *testing.T) {
	sourceRoot := t.TempDir()
	worktreePath := t.TempDir()

	// Source has the canonical file
	if err := os.MkdirAll(filepath.Join(sourceRoot, ".xylem"), 0o755); err != nil {
		t.Fatalf("MkdirAll(source .xylem) = %v", err)
	}
	canonical := []byte("harness content\n")
	if err := os.WriteFile(filepath.Join(sourceRoot, ".xylem", "HARNESS.md"), canonical, 0o644); err != nil {
		t.Fatalf("WriteFile source = %v", err)
	}

	// Worktree has a .git directory (simulating a regular worktree — for
	// linked worktrees the .git file path resolution is exercised by the
	// integration path).
	if err := os.MkdirAll(filepath.Join(worktreePath, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) = %v", err)
	}

	if err := copyProtectedSurfaceFile(sourceRoot, worktreePath, ".xylem/HARNESS.md"); err != nil {
		t.Fatalf("copyProtectedSurfaceFile() = %v", err)
	}

	// The file should exist at the destination
	if _, err := os.Stat(filepath.Join(worktreePath, ".xylem", "HARNESS.md")); err != nil {
		t.Fatalf("restored file missing: %v", err)
	}

	// .git/info/exclude should contain the path with leading slash
	excludeData, err := os.ReadFile(filepath.Join(worktreePath, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("ReadFile(exclude) = %v", err)
	}
	expected := "/.xylem/HARNESS.md"
	if !strings.Contains(string(excludeData), expected) {
		t.Fatalf("exclude = %q, want containing %q", string(excludeData), expected)
	}

	// Second call should be idempotent — no duplicate entry
	if err := copyProtectedSurfaceFile(sourceRoot, worktreePath, ".xylem/HARNESS.md"); err != nil {
		t.Fatalf("second copyProtectedSurfaceFile() = %v", err)
	}
	excludeData, err = os.ReadFile(filepath.Join(worktreePath, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("ReadFile(exclude) second = %v", err)
	}
	count := strings.Count(string(excludeData), expected)
	if count != 1 {
		t.Fatalf("exclude contains %d copies of %q, want 1 (must be idempotent)", count, expected)
	}
}

// TestResolveWorktreeGitdirHandlesLinkedWorktree verifies that for a linked
// worktree (where .git is a file pointing at the per-worktree gitdir), the
// gitdir resolution follows the pointer correctly. This is critical for the
// xylem vessel case where worktrees are created via `git worktree add`.
func TestResolveWorktreeGitdirHandlesLinkedWorktree(t *testing.T) {
	mainRepo := t.TempDir()
	linkedWorktree := t.TempDir()
	perWorktreeGitdir := filepath.Join(mainRepo, ".git", "worktrees", "linked")
	if err := os.MkdirAll(perWorktreeGitdir, 0o755); err != nil {
		t.Fatalf("MkdirAll(per-worktree gitdir) = %v", err)
	}

	// Write the .git FILE pointing at the per-worktree gitdir
	gitFilePath := filepath.Join(linkedWorktree, ".git")
	if err := os.WriteFile(gitFilePath, []byte("gitdir: "+perWorktreeGitdir+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.git) = %v", err)
	}

	got, err := resolveWorktreeGitdir(linkedWorktree)
	if err != nil {
		t.Fatalf("resolveWorktreeGitdir() = %v", err)
	}
	if got != perWorktreeGitdir {
		t.Fatalf("resolveWorktreeGitdir() = %q, want %q", got, perWorktreeGitdir)
	}

	// Regular worktree (.git directory) should return the .git path itself
	regularRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(regularRepo, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(regular .git) = %v", err)
	}
	got, err = resolveWorktreeGitdir(regularRepo)
	if err != nil {
		t.Fatalf("resolveWorktreeGitdir(regular) = %v", err)
	}
	if got != filepath.Join(regularRepo, ".git") {
		t.Fatalf("resolveWorktreeGitdir(regular) = %q, want %q", got, filepath.Join(regularRepo, ".git"))
	}
}

// TestVerifyProtectedSurfacesStillCatchesMutualDeletion verifies that when
// BOTH the worktree and source root lack a file, the pre-verify restore is
// a no-op and the outer Compare still raises a violation (because the
// before-snapshot had the file). This ensures the suppression logic doesn't
// over-reach and mask legitimate removal from the canonical source.
func TestVerifyProtectedSurfacesStillCatchesMutualDeletion(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeDir := filepath.Join(repoRoot, ".claude", "worktrees", "review", "pr-mutual")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) = %v", err)
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree) = %v", err)
	}
	// Source root + worktree both have the file initially
	canonical := []byte("before\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".xylem.yml"), canonical, 0o644); err != nil {
		t.Fatalf("WriteFile source = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), canonical, 0o644); err != nil {
		t.Fatalf("WriteFile worktree = %v", err)
	}

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "git" && len(args) >= 5 && args[2] == "rev-parse" && args[4] == "--git-common-dir" {
				return []byte(filepath.Join(repoRoot, ".git")), nil, true
			}
			return nil, nil, false
		},
	}
	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, cmdRunner)
	r.AuditLog = auditLog

	// Before snapshot captures the file present
	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	if err != nil || !ok {
		t.Fatalf("takeProtectedSurfaceSnapshot = %v %v", err, ok)
	}

	// Now delete the file from BOTH worktree and source root (simulating a
	// phase that legitimately tried to remove it from everywhere — e.g., a
	// rogue agent doing `rm -rf /.xylem/`).
	if err := os.Remove(filepath.Join(worktreeDir, ".xylem.yml")); err != nil {
		t.Fatalf("Remove worktree = %v", err)
	}
	if err := os.Remove(filepath.Join(repoRoot, ".xylem.yml")); err != nil {
		t.Fatalf("Remove source = %v", err)
	}

	// verifyProtectedSurfaces should STILL raise a violation because the
	// pre-verify restore has nothing to restore from (source root is empty)
	// and the outer Compare sees the before-snapshot had the file.
	err = r.verifyProtectedSurfaces(
		queue.Vessel{ID: "issue-mutual-del", Source: "github-issue", Workflow: "fix-bug"},
		workflow.Phase{Name: "implement"},
		worktreeDir,
		before,
	)
	if err == nil {
		t.Fatal("verifyProtectedSurfaces() = nil, want violation for mutual deletion")
	}
	if !strings.Contains(err.Error(), "violated protected surfaces") {
		t.Fatalf("error = %q, want containing 'violated protected surfaces'", err)
	}
}

// TestVerifyProtectedSurfacesSuppressesAlignmentModifications documents the
// loop 9 fix for the final Fix B cascade: when a resolve-conflicts vessel
// runs `git merge origin/main --no-commit` on a pre-#157 PR branch, the
// protected prompts get updated from the branch's old content to main's
// canonical content. The verifier previously flagged this as a modification
// violation (b72aa068 → 1d19afcf), blocking PR #143 and #164 from ever
// resolving. This test verifies the alignment filter suppresses such
// violations when the after-hash matches the source root's current hash.
func TestVerifyProtectedSurfacesSuppressesAlignmentModifications(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeDir := filepath.Join(repoRoot, ".claude", "worktrees", "review", "pr-align")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".git", "info"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree .git) = %v", err)
	}

	// Source root has the NEW (canonical) version of the protected file.
	canonicalContent := []byte("# hardened prompt (PR #172 version)\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".xylem.yml"), canonicalContent, 0o644); err != nil {
		t.Fatalf("WriteFile source = %v", err)
	}

	// Worktree starts with the OLD version (simulating a PR branch cut before #172).
	oldContent := []byte("# old prompt (pre-#172 version)\n")
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), oldContent, 0o644); err != nil {
		t.Fatalf("WriteFile worktree = %v", err)
	}

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "git" && len(args) >= 5 && args[2] == "rev-parse" && args[4] == "--git-common-dir" {
				return []byte(filepath.Join(repoRoot, ".git")), nil, true
			}
			return nil, nil, false
		},
	}
	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, cmdRunner)
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	if err != nil || !ok {
		t.Fatalf("takeProtectedSurfaceSnapshot = %v %v", err, ok)
	}

	// Phase simulation: git merge origin/main --no-commit updates the file
	// to match main's version (exactly the source root's content).
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), canonicalContent, 0o644); err != nil {
		t.Fatalf("WriteFile simulated merge = %v", err)
	}

	// verifyProtectedSurfaces should NOT return a violation — the modification
	// brought the file into alignment with the source root.
	err = r.verifyProtectedSurfaces(
		queue.Vessel{ID: "pr-143-resolve-conflicts-retry", Source: "github-pr", Workflow: "resolve-conflicts"},
		workflow.Phase{Name: "analyze"},
		worktreeDir,
		before,
	)
	if err != nil {
		t.Fatalf("verifyProtectedSurfaces() = %v, want nil (alignment should be suppressed)", err)
	}
}

// TestVerifyProtectedSurfacesStillCatchesRogueModifications ensures the
// alignment filter does NOT mask modifications to content that doesn't
// match the source root — those are still rogue modifications and should
// raise violations.
func TestVerifyProtectedSurfacesStillCatchesRogueModifications(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeDir := filepath.Join(repoRoot, ".claude", "worktrees", "review", "pr-rogue")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".git", "info"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree .git) = %v", err)
	}

	canonicalContent := []byte("# canonical\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".xylem.yml"), canonicalContent, 0o644); err != nil {
		t.Fatalf("WriteFile source = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), canonicalContent, 0o644); err != nil {
		t.Fatalf("WriteFile worktree = %v", err)
	}

	cfg := makeTestConfig(repoRoot, 1)
	cfg.StateDir = filepath.Join(repoRoot, ".xylem-state")
	auditLog := intermediary.NewAuditLog(filepath.Join(cfg.StateDir, "audit.jsonl"))
	cmdRunner := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "git" && len(args) >= 5 && args[2] == "rev-parse" && args[4] == "--git-common-dir" {
				return []byte(filepath.Join(repoRoot, ".git")), nil, true
			}
			return nil, nil, false
		},
	}
	r := New(cfg, queue.New(filepath.Join(repoRoot, "queue.jsonl")), &mockWorktree{path: worktreeDir}, cmdRunner)
	r.AuditLog = auditLog

	before, ok, err := r.takeProtectedSurfaceSnapshot(context.Background(), worktreeDir)
	if err != nil || !ok {
		t.Fatalf("takeProtectedSurfaceSnapshot = %v %v", err, ok)
	}

	// Rogue modification: agent writes content that matches NEITHER the
	// before state (canonical) NOR any other source-root state.
	if err := os.WriteFile(filepath.Join(worktreeDir, ".xylem.yml"), []byte("# evil rogue content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile rogue = %v", err)
	}

	err = r.verifyProtectedSurfaces(
		queue.Vessel{ID: "issue-rogue-mod", Source: "github-issue", Workflow: "fix-bug"},
		workflow.Phase{Name: "implement"},
		worktreeDir,
		before,
	)
	if err == nil {
		t.Fatal("verifyProtectedSurfaces() = nil, want violation for rogue modification that does NOT match source root")
	}
	if !strings.Contains(err.Error(), "violated protected surfaces") {
		t.Fatalf("error = %q, want containing 'violated protected surfaces'", err)
	}
}

// TestFilterViolationsAlignedWithSourceRootUnit is a focused unit test on the
// filter helper to verify edge cases: absent Before, deleted After, path not
// in source snapshot, multi-violation mix.
func TestFilterViolationsAlignedWithSourceRootUnit(t *testing.T) {
	source := surface.Snapshot{Files: []surface.FileHash{
		{Path: ".xylem.yml", Hash: "src-yml-hash"},
		{Path: ".xylem/workflows/fix-bug.yaml", Hash: "src-wf-hash"},
	}}
	violations := []surface.Violation{
		// Aligned: suppress
		{Path: ".xylem.yml", Before: "old-yml", After: "src-yml-hash"},
		// Rogue mod: keep
		{Path: ".xylem/workflows/fix-bug.yaml", Before: "old-wf", After: "rogue-wf-hash"},
		// Addition: keep (not a modification, filter passes through)
		{Path: ".xylem/prompts/new/file.md", Before: "absent", After: "added-hash"},
		// Deletion: keep (not a modification, filter passes through)
		{Path: ".xylem/HARNESS.md", Before: "old-harness", After: "deleted"},
		// Path not in source: keep (can't verify alignment)
		{Path: ".xylem/workflows/unknown.yaml", Before: "old", After: "new"},
	}
	vessel := queue.Vessel{ID: "unit-test", Workflow: "resolve-conflicts"}
	phase := workflow.Phase{Name: "analyze"}

	filtered := filterViolationsAlignedWithSourceRoot(vessel, phase, violations, source)

	if len(filtered) != 4 {
		t.Fatalf("filtered count = %d, want 4 (1 aligned suppressed, 4 kept)", len(filtered))
	}
	// Ensure the aligned one is NOT in the filtered list
	for _, v := range filtered {
		if v.Path == ".xylem.yml" {
			t.Fatalf("filtered still contains aligned violation on .xylem.yml")
		}
	}
}

func TestSmoke_S19_VesselRunStateIsOwnedByRunVesselOrchestratedNotSharedAcrossGoroutines(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "race-diamond"))
	require.NoError(t, err)

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

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)
}

func TestSmoke_S20_SinglePhaseResultIncludesAPhaseSummaryField(t *testing.T) {
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

func TestSmoke_S21_SinglePhaseResultEvidenceClaimsAreEmptyWhenNoGateIsPresent(t *testing.T) {
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
	assert.Empty(t, result.evidenceClaims)
}

func TestSmoke_S22_WaveResultsAreMergedIntoVesselRunStateAfterWgWait(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "wave-merge"))
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "wave-merge", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "a", promptContent: "Branch A {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "b", promptContent: "Branch B {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})

	preseedDir := config.RuntimePath(cfg.StateDir, "phases", "issue-1")
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

	result, err := r.DrainAndWait(context.Background())
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

func TestSmoke_S23_CostTrackerConcurrentAccessFromMultipleGoroutinesIsSafe(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 10.0, 10000)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "wave-cost"))
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "wave-cost", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "a", promptContent: "Cost A {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "b", promptContent: "Cost B {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})

	preseedDir := config.RuntimePath(cfg.StateDir, "phases", "issue-1")
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

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	require.Len(t, summary.Phases, 2)
	sumPhaseTokens := 0
	for _, phaseSummary := range summary.Phases {
		sumPhaseTokens += phaseSummary.InputTokensEst + phaseSummary.OutputTokensEst
	}
	assert.Equal(t, sumPhaseTokens, summary.TotalTokensEst)
}

func TestSmoke_S24_VesselSpanContextIsPropagatedToGoroutineChildPhaseSpans(t *testing.T) {
	tracer, rec := newTestTracer(t)
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "trace-diamond"))
	require.NoError(t, err)

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

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	for _, phaseName := range []string{"phase:root", "phase:left", "phase:right"} {
		phaseSpan := endedSpanByName(t, rec, phaseName)
		assert.Equal(t, vesselSpan.SpanContext().TraceID(), phaseSpan.Parent().TraceID(), phaseName)
		assert.Equal(t, vesselSpan.SpanContext().SpanID(), phaseSpan.Parent().SpanID(), phaseName)
	}
}

func TestSmoke_S25_ConcurrentPhasesMayCauseSlightOverspendWithoutRetroactiveFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	setPricedModel(cfg)
	setBudget(cfg, 10.0, 150)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "concurrent-budget"))
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "concurrent-budget", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "a", promptContent: "Concurrent A {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
		{name: "b", promptContent: "Concurrent B {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})

	preseedDir := config.RuntimePath(cfg.StateDir, "phases", "issue-1")
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

	result, err := r.DrainAndWait(context.Background())
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

func TestValidateIssueDataForWorkflow_CommandGateZeroNumber(t *testing.T) {
	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
	}
	data := phase.IssueData{Number: 0}
	wf := &workflow.Workflow{
		Phases: []workflow.Phase{
			{
				Name:       "resolve",
				PromptFile: ".xylem/prompts/resolve-conflicts/resolve.md",
				MaxTurns:   10,
				Gate: &workflow.Gate{
					Type: "command",
					Run:  "gh pr view {{.Issue.Number}} --json mergeable",
				},
			},
		},
	}

	err := validateIssueDataForWorkflow(vessel, data, wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command gate for phase resolve references .Issue")
	assert.Contains(t, err.Error(), "Number is 0")
}

func TestValidateIssueDataForWorkflow_CommandGateValidNumber(t *testing.T) {
	vessel := queue.Vessel{
		ID:     "issue-1",
		Source: "github-issue",
	}
	data := phase.IssueData{Number: 42}
	wf := &workflow.Workflow{
		Phases: []workflow.Phase{
			{
				Name:       "resolve",
				PromptFile: ".xylem/prompts/resolve-conflicts/resolve.md",
				MaxTurns:   10,
				Gate: &workflow.Gate{
					Type: "command",
					Run:  "gh pr view {{.Issue.Number}} --json mergeable",
				},
			},
		},
	}

	require.NoError(t, validateIssueDataForWorkflow(vessel, data, wf))
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

func TestGitHubIssueURLRegex(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantOK  bool
		wantNum string
	}{
		{"standard issue URL", "https://github.com/owner/repo/issues/42", true, "42"},
		{"http issue URL", "http://github.com/owner/repo/issues/1", true, "1"},
		{"PR URL does not match issue regex", "https://github.com/owner/repo/pull/42", false, ""},
		{"non-GitHub URL", "https://example.com/issues/42", false, ""},
		{"no number", "https://github.com/owner/repo/issues/", false, ""},
		{"issue URL with trailing path", "https://github.com/owner/repo/issues/42#comment", true, "42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := githubIssueURLRe.FindStringSubmatch(tt.url)
			if tt.wantOK {
				require.NotNil(t, m, "expected match for %s", tt.url)
				assert.Equal(t, tt.wantNum, m[2])
			} else {
				assert.Nil(t, m, "expected no match for %s", tt.url)
			}
		})
	}
}

func TestGitHubPRURLRegex(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantOK  bool
		wantNum string
	}{
		{"standard PR URL", "https://github.com/owner/repo/pull/99", true, "99"},
		{"issue URL does not match PR regex", "https://github.com/owner/repo/issues/42", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := githubPRURLRe.FindStringSubmatch(tt.url)
			if tt.wantOK {
				require.NotNil(t, m, "expected match for %s", tt.url)
				assert.Equal(t, tt.wantNum, m[2])
			} else {
				assert.Nil(t, m, "expected no match for %s", tt.url)
			}
		})
	}
}

func TestFetchManualIssueData_GitHubIssueRef(t *testing.T) {
	issueJSON := `{"title":"Fix the bug","body":"Something is broken","url":"https://github.com/nicholls-inc/xylem/issues/230","labels":[{"name":"bug"}]}`
	cmdRunner := &mockCmdRunner{
		outputData: []byte(issueJSON),
	}
	r := &Runner{
		Runner: cmdRunner,
		Sources: map[string]source.Source{
			"manual": &source.Manual{},
		},
	}

	vessel := queue.Vessel{
		ID:     "issue-230-fresh",
		Source: "manual",
		Ref:    "https://github.com/nicholls-inc/xylem/issues/230",
	}

	data := r.fetchIssueData(context.Background(), &vessel)

	assert.Equal(t, 230, data.Number)
	assert.Equal(t, "Fix the bug", data.Title)
	assert.Equal(t, "Something is broken", data.Body)
	assert.Contains(t, data.Labels, "bug")

	// Verify meta was hydrated
	assert.Equal(t, "230", vessel.Meta["issue_num"])
	assert.Equal(t, "nicholls-inc/xylem", vessel.Meta["source_repo"])
}

func TestFetchManualIssueData_GitHubPRRef(t *testing.T) {
	prJSON := `{"title":"Add feature","body":"New feature","url":"https://github.com/owner/repo/pull/55","labels":[]}`
	cmdRunner := &mockCmdRunner{
		outputData: []byte(prJSON),
	}
	r := &Runner{
		Runner: cmdRunner,
		Sources: map[string]source.Source{
			"manual": &source.Manual{},
		},
	}

	vessel := queue.Vessel{
		ID:     "pr-55",
		Source: "manual",
		Ref:    "https://github.com/owner/repo/pull/55",
	}

	data := r.fetchIssueData(context.Background(), &vessel)

	assert.Equal(t, 55, data.Number)
	assert.Equal(t, "Add feature", data.Title)
	assert.Equal(t, "55", vessel.Meta["pr_num"])
	assert.Equal(t, "owner/repo", vessel.Meta["source_repo"])
}

func TestFetchManualIssueData_NonGitHubRef(t *testing.T) {
	r := &Runner{
		Runner:  &mockCmdRunner{},
		Sources: map[string]source.Source{"manual": &source.Manual{}},
	}

	vessel := queue.Vessel{
		ID:     "task-1",
		Source: "manual",
		Ref:    "just a description",
	}

	data := r.fetchIssueData(context.Background(), &vessel)
	assert.Equal(t, 0, data.Number)
	assert.Empty(t, data.Title)
}

func TestFetchManualIssueData_EmptyRef(t *testing.T) {
	r := &Runner{
		Runner:  &mockCmdRunner{},
		Sources: map[string]source.Source{"manual": &source.Manual{}},
	}

	vessel := queue.Vessel{
		ID:     "task-2",
		Source: "manual",
		Ref:    "",
	}

	data := r.fetchIssueData(context.Background(), &vessel)
	assert.Equal(t, 0, data.Number)
}

func TestResolveRepo_ManualWithSourceRepo(t *testing.T) {
	r := &Runner{
		Sources: map[string]source.Source{"manual": &source.Manual{}},
	}

	vessel := queue.Vessel{
		Source: "manual",
		Meta:   map[string]string{"source_repo": "nicholls-inc/xylem"},
	}
	got := r.resolveRepo(vessel)
	assert.Equal(t, "nicholls-inc/xylem", got)
}

func TestResolveRepo_ManualWithoutSourceRepo(t *testing.T) {
	r := &Runner{
		Sources: map[string]source.Source{"manual": &source.Manual{}},
	}

	vessel := queue.Vessel{Source: "manual"}
	got := r.resolveRepo(vessel)
	assert.Equal(t, "", got)
}

func TestHydrateManualMeta_DoesNotOverwrite(t *testing.T) {
	r := &Runner{}
	vessel := &queue.Vessel{
		Meta: map[string]string{
			"issue_num":   "99",
			"source_repo": "existing/repo",
		},
	}
	r.hydrateManualMeta(vessel, "issue_num", "230", "new/repo")
	assert.Equal(t, "99", vessel.Meta["issue_num"])
	assert.Equal(t, "existing/repo", vessel.Meta["source_repo"])
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

	result, err := r.DrainAndWait(context.Background())
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
	if wt.removeCalled {
		t.Errorf("expected timed out vessel worktree cleanup to wait for run exit, got %s", wt.removePath)
	}
}

func TestCheckHungVesselsWritesTraceableSummary(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Timeout = "1s"

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "hung-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	vessel, _ := q.Dequeue()
	require.NotNil(t, vessel)

	old := now.Add(-5 * time.Minute)
	vessel.StartedAt = &old
	require.NoError(t, q.UpdateVessel(*vessel))

	tracer, rec := newTestTracer(t)
	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	r.Tracer = tracer

	r.CheckHungVessels(context.Background())

	summary, err := LoadVesselSummary(cfg.StateDir, "hung-1")
	require.NoError(t, err)
	require.NotNil(t, summary.Trace)
	assert.Equal(t, "timed_out", summary.State)

	timeoutSpan := endedSpanByName(t, rec, "wait_transition:timed_out")
	assert.Equal(t, timeoutSpan.SpanContext().TraceID().String(), summary.Trace.TraceID)
	assert.Equal(t, timeoutSpan.SpanContext().SpanID().String(), summary.Trace.SpanID)
}

func TestSmoke_S1_UntrackedPhaseStalledVesselTimesOut(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"
	cfg.Daemon.StallMonitor.OrphanCheckEnabled = false

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "stall-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	vessel, _ := q.Dequeue()
	require.NotNil(t, vessel)

	outputPath := config.RuntimePath(cfg.StateDir, "phases", vessel.ID, "analyze.output")
	require.NoError(t, os.MkdirAll(filepath.Dir(outputPath), 0o755))
	require.NoError(t, os.WriteFile(outputPath, []byte(""), 0o644))
	old := now.Add(-11 * time.Minute)
	require.NoError(t, os.Chtimes(outputPath, old, old))
	require.NoError(t, q.UpdateVessel(*vessel))

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	findings := r.CheckStalledVessels(context.Background())
	require.Len(t, findings, 1)
	assert.Equal(t, "phase_stalled", findings[0].Code)
	assert.Equal(t, "analyze", findings[0].Phase)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateTimedOut, updated.State)
	assert.Contains(t, updated.Error, "phase stalled: no output for")
}

func TestCheckStalledVesselsDoesNotTimeoutLiveTrackedSubprocessWithOldOutput(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"
	cfg.Daemon.StallMonitor.OrphanCheckEnabled = true

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "live-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	vessel, _ := q.Dequeue()
	require.NotNil(t, vessel)

	outputPath := config.RuntimePath(cfg.StateDir, "phases", vessel.ID, "implement.output")
	require.NoError(t, os.MkdirAll(filepath.Dir(outputPath), 0o755))
	require.NoError(t, os.WriteFile(outputPath, []byte(""), 0o644))
	old := now.Add(-11 * time.Minute)
	require.NoError(t, os.Chtimes(outputPath, old, old))
	require.NoError(t, q.UpdateVessel(*vessel))

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	r.markProcessStarted(vessel.ID, "implement", os.Getpid())

	findings := r.CheckStalledVessels(context.Background())
	require.Empty(t, findings)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateRunning, updated.State)
	assert.Empty(t, updated.Error)
}

func TestCheckStalledVesselsDoesNotTimeoutObservedLivePhaseWithOldOutput(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"
	cfg.Daemon.StallMonitor.OrphanCheckEnabled = true

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{{
		name:          "implement",
		promptContent: "Implement the fix",
		maxTurns:      5,
	}})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	_, err := q.Enqueue(queue.Vessel{
		ID:        "observed-live-1",
		Source:    "manual",
		Workflow:  "fix-bug",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	require.NoError(t, err)

	vessel, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)

	cmdRunner := newObservedBlockingPhaseCmdRunner()
	r := New(cfg, q, &mockWorktree{path: dir}, cmdRunner)

	done := make(chan string, 1)
	go func() {
		done <- r.runVessel(context.Background(), *vessel)
	}()

	<-cmdRunner.started

	outputPath := config.RuntimePath(cfg.StateDir, "phases", vessel.ID, "implement.output")
	old := now.Add(-11 * time.Minute)
	require.NoError(t, os.Chtimes(outputPath, old, old))

	findings := r.CheckStalledVessels(context.Background())
	require.Empty(t, findings)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateRunning, updated.State)
	assert.Empty(t, updated.Error)

	close(cmdRunner.release)
	assert.Equal(t, "completed", <-done)
}

func TestSmoke_S3_OrphanedRunningVesselTimesOut(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"
	cfg.Daemon.StallMonitor.OrphanCheckEnabled = true

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "orphan-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	vessel, _ := q.Dequeue()
	require.NotNil(t, vessel)
	require.NoError(t, q.UpdateVessel(*vessel))

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	r.markProcessStarted(vessel.ID, "analyze", os.Getpid())
	r.markProcessExited(vessel.ID, os.Getpid())
	findings := r.CheckStalledVessels(context.Background())
	require.Len(t, findings, 1)
	assert.Equal(t, "orphaned_subprocess", findings[0].Code)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateTimedOut, updated.State)
	assert.Equal(t, "vessel orphaned (no live subprocess)", updated.Error)
}

func TestTimeoutRunningVesselReturnsFalseWhenStateAlreadyChanged(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "transition-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	vessel, _ := q.Dequeue()
	require.NotNil(t, vessel)
	require.NoError(t, q.Update(vessel.ID, queue.StateCompleted, ""))

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	ok := r.timeoutRunningVessel(context.Background(), *vessel, "phase stalled: no output for 11m0s")
	assert.False(t, ok)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateCompleted, updated.State)
	assert.Empty(t, updated.Error)
}

func TestCheckHungVesselsDoesNotRemoveWorktreeMidFlight(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Timeout = "1s"

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:           "hung-1",
		Source:       "manual",
		State:        queue.StatePending,
		CreatedAt:    now,
		WorktreePath: filepath.Join(dir, ".claude", "worktrees", "hung-1"),
	})
	vessel, _ := q.Dequeue()
	require.NotNil(t, vessel)
	startedAt := now.Add(-2 * time.Minute)
	vessel.StartedAt = &startedAt
	require.NoError(t, q.UpdateVessel(*vessel))

	wt := &mockWorktree{}
	r := New(cfg, q, wt, &mockCmdRunner{})
	r.CheckHungVessels(context.Background())

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateTimedOut, updated.State)
	assert.False(t, wt.removeCalled)
}

func TestRunVesselPromptOnlyTimeoutKeepsTimedOutStateAndCleansWorktreeAfterExit(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:        "prompt-timeout-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
		Prompt:    "solve it",
	})
	require.NoError(t, err)

	vessel, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)

	wt := &mockWorktree{path: filepath.Join(dir, ".claude", "worktrees", "prompt-timeout-1")}
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(_ string, _ string, _ string, _ ...string) ([]byte, error, bool) {
			updateErr := q.Update(vessel.ID, queue.StateTimedOut, "phase stalled: no output for 11m0s")
			require.NoError(t, updateErr)
			return nil, context.DeadlineExceeded, true
		},
	}

	r := New(cfg, q, wt, cmdRunner)
	outcome := r.runVessel(context.Background(), *vessel)
	assert.Equal(t, "timed_out", outcome)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateTimedOut, updated.State)
	assert.Equal(t, "phase stalled: no output for 11m0s", updated.Error)
	assert.True(t, wt.removeCalled)
	assert.Equal(t, wt.path, wt.removePath)
}

func TestRunVesselOrchestratedTimeoutKeepsTimedOutStateAndCleansWorktreeAfterExit(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	writeWorkflowFile(t, dir, "orchestrated-timeout", []testPhase{
		{name: "root", promptContent: "Root phase", maxTurns: 5},
		{name: "child", promptContent: "Child phase {{.PreviousOutputs.root}}", maxTurns: 5, dependsOn: []string{"root"}},
	})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "orchestrated-timeout"))
	require.NoError(t, err)

	vessel, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)

	wt := &mockWorktree{path: filepath.Join(dir, ".claude", "worktrees", vessel.ID)}
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(_ string, prompt string, _ string, _ ...string) ([]byte, error, bool) {
			if !strings.Contains(prompt, "Root phase") {
				return nil, nil, false
			}
			updateErr := q.Update(vessel.ID, queue.StateTimedOut, "phase stalled: no output for 11m0s")
			require.NoError(t, updateErr)
			return nil, context.DeadlineExceeded, true
		},
	}

	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	outcome := r.runVessel(context.Background(), *vessel)
	assert.Equal(t, "timed_out", outcome)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateTimedOut, updated.State)
	assert.Equal(t, "phase stalled: no output for 11m0s", updated.Error)
	assert.Len(t, cmdRunner.phaseCalls, 1)
	assert.True(t, wt.removeCalled)
	assert.Equal(t, wt.path, wt.removePath)
}

func TestRunVesselGateTimeoutSkipsCompletionHooks(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	writeWorkflowFile(t, dir, "gate-timeout", []testPhase{
		{
			name:          "implement",
			promptContent: "Implement change",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make test\"",
		},
	})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "gate-timeout"))
	require.NoError(t, err)

	vessel, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)

	wt := &mockWorktree{path: filepath.Join(dir, ".claude", "worktrees", vessel.ID)}
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Implement change": []byte("implemented"),
		},
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name != "sh" || len(args) < 2 || args[0] != "-c" || !strings.Contains(args[1], "make test") {
				return nil, nil, false
			}
			updateErr := q.Update(vessel.ID, queue.StateTimedOut, "phase stalled: no output for 11m0s")
			require.NoError(t, updateErr)
			return []byte("gate passed"), nil, true
		},
	}
	src := &recordingSource{}

	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": src,
	}

	outcome := r.runVessel(context.Background(), *vessel)
	assert.Equal(t, "timed_out", outcome)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateTimedOut, updated.State)
	assert.Equal(t, int32(1), src.startCalls.Load())
	assert.Zero(t, src.completeCalls.Load())
	assert.Zero(t, src.failCalls.Load())
	assert.True(t, wt.removeCalled)
	assert.Equal(t, wt.path, wt.removePath)
}

func TestRunVesselOrchestratedGateTimeoutStopsDependentPhases(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	writeWorkflowFile(t, dir, "orchestrated-gate-timeout", []testPhase{
		{
			name:          "root",
			promptContent: "Root phase",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make test\"",
		},
		{
			name:          "child",
			promptContent: "Child phase {{.PreviousOutputs.root}}",
			maxTurns:      5,
			dependsOn:     []string{"root"},
		},
	})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "orchestrated-gate-timeout"))
	require.NoError(t, err)

	vessel, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)

	wt := &mockWorktree{path: filepath.Join(dir, ".claude", "worktrees", vessel.ID)}
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Root phase": []byte("root output"),
		},
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name != "sh" || len(args) < 2 || args[0] != "-c" || !strings.Contains(args[1], "make test") {
				return nil, nil, false
			}
			updateErr := q.Update(vessel.ID, queue.StateTimedOut, "phase stalled: no output for 11m0s")
			require.NoError(t, updateErr)
			return []byte("gate passed"), nil, true
		},
	}

	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	outcome := r.runVessel(context.Background(), *vessel)
	assert.Equal(t, "timed_out", outcome)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateTimedOut, updated.State)
	assert.Len(t, cmdRunner.phaseCalls, 1)
	assert.Contains(t, cmdRunner.phaseCalls[0].prompt, "Root phase")
	assert.True(t, wt.removeCalled)
	assert.Equal(t, wt.path, wt.removePath)
}

func TestSmoke_S41_OrchestratedGateRetryFailurePostsOnlyFinalFailedComment(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	writeWorkflowFile(t, dir, "orchestrated-gate-fail", []testPhase{
		{
			name:          "root",
			promptContent: "Root phase\n{{.GateResult}}",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make test\"\n      retries: 2\n      retry_delay: \"0s\"",
		},
		{
			name:          "child",
			promptContent: "Child phase {{.PreviousOutputs.root}}",
			maxTurns:      5,
			dependsOn:     []string{"root"},
		},
	})
	withTestWorkingDir(t, dir)

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(1, "orchestrated-gate-fail"))
	require.NoError(t, err)

	vessel, err := q.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Root phase": []byte("root output"),
		},
		gateOutput: []byte("FAIL: TestFoo"),
		gateErr:    &mockExitError{code: 1},
	}
	wt := &mockWorktree{path: filepath.Join(dir, ".claude", "worktrees", vessel.ID)}
	r := New(cfg, q, wt, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	r.Reporter = &reporter.Reporter{Runner: cmdRunner, Repo: "owner/repo"}

	outcome := r.runVessel(context.Background(), *vessel)
	assert.Equal(t, "failed", outcome)
	assert.Len(t, cmdRunner.phaseCalls, 3)
	require.Len(t, issueCommentBodies(cmdRunner), 1)
	assert.Contains(t, issueCommentBodies(cmdRunner)[0], "failed at phase `root`")
	assert.NotContains(t, issueCommentBodies(cmdRunner)[0], "phase `root` completed")
}

func TestCheckStalledVesselsDoesNotTimeoutUntrackedRecentPhase(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 2)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"
	cfg.Daemon.StallMonitor.OrphanCheckEnabled = true

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	_, _ = q.Enqueue(queue.Vessel{
		ID:        "command-1",
		Source:    "manual",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	vessel, _ := q.Dequeue()
	require.NotNil(t, vessel)

	outputPath := config.RuntimePath(cfg.StateDir, "phases", vessel.ID, "analyze.output")
	require.NoError(t, os.MkdirAll(filepath.Dir(outputPath), 0o755))
	require.NoError(t, os.WriteFile(outputPath, []byte("still running"), 0o644))
	require.NoError(t, os.Chtimes(outputPath, now, now))
	require.NoError(t, q.UpdateVessel(*vessel))

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	findings := r.CheckStalledVessels(context.Background())
	require.Empty(t, findings)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateRunning, updated.State)
}

func TestSmoke_S9_NilTracerSkipsAllSpanCreationWithoutPanicking(t *testing.T) {
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, nil)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	vessel, findErr := r.Queue.FindByID("issue-1")
	require.NoError(t, findErr)
	assert.Equal(t, queue.StateCompleted, vessel.State)

	outputPath := config.RuntimePath(r.Config.StateDir, "phases", "issue-1", "analyze.output")
	output, readErr := os.ReadFile(outputPath)
	require.NoError(t, readErr)
	assert.Equal(t, "analysis complete", string(output))
}

func TestSmoke_S10_DrainRunSpanWrapsEntireDrainCall(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	drainSpan := endedSpanByName(t, rec, "drain_run")
	drainAttrs := spanAttrMap(drainSpan)
	assert.Equal(t, strconv.Itoa(r.Config.Concurrency), drainAttrs["xylem.drain.concurrency"])
	assert.Equal(t, r.Config.Timeout, drainAttrs["xylem.drain.timeout"])

	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	assert.False(t, drainSpan.StartTime().After(vesselSpan.StartTime()))
	assert.False(t, drainSpan.StartTime().After(phaseSpan.StartTime()))
	assert.False(t, drainSpan.EndTime().Before(vesselSpan.EndTime()))
	assert.False(t, drainSpan.EndTime().Before(phaseSpan.EndTime()))
}

func TestSmoke_S11_VesselSpanIsChildOfDrainRunSpan(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	drainSpan := endedSpanByName(t, rec, "drain_run")
	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	assert.Equal(t, drainSpan.SpanContext().TraceID(), vesselSpan.Parent().TraceID())
	assert.Equal(t, drainSpan.SpanContext().SpanID(), vesselSpan.Parent().SpanID())

	vesselAttrs := spanAttrMap(vesselSpan)
	assert.Equal(t, "issue-1", vesselAttrs["xylem.vessel.id"])
	assert.Equal(t, "github-issue", vesselAttrs["xylem.vessel.source"])
	assert.Equal(t, "trace-basic", vesselAttrs["xylem.vessel.workflow"])
	assert.Equal(t, "https://github.com/owner/repo/issues/1", vesselAttrs["xylem.vessel.ref"])
}

func TestSmoke_S12_PhaseSpanIsChildOfVesselSpan(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	assert.Equal(t, vesselSpan.SpanContext().TraceID(), phaseSpan.Parent().TraceID())
	assert.Equal(t, vesselSpan.SpanContext().SpanID(), phaseSpan.Parent().SpanID())
}

func TestSmoke_S13_GateSpanIsChildOfPhaseSpan(t *testing.T) {
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

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	gateSpan := endedSpanByName(t, rec, "gate:command")
	assert.Equal(t, phaseSpan.SpanContext().TraceID(), gateSpan.Parent().TraceID())
	assert.Equal(t, phaseSpan.SpanContext().SpanID(), gateSpan.Parent().SpanID())

	gateAttrs := spanAttrMap(gateSpan)
	assert.Equal(t, "command", gateAttrs["xylem.gate.type"])
	assert.Equal(t, "true", gateAttrs["xylem.gate.passed"])
	assert.Equal(t, "1", gateAttrs["xylem.gate.retry_attempt"])
}

func TestSmoke_S14_PhaseSpanGetsResultAttributesAddedAfterExecution(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	attrs := spanAttrMap(endedSpanByName(t, rec, "phase:analyze"))
	require.NotEmpty(t, attrs["xylem.phase.input_tokens_est"])
	require.NotEmpty(t, attrs["xylem.phase.output_tokens_est"])
	require.NotEmpty(t, attrs["xylem.phase.cost_usd_est"])
	require.NotEmpty(t, attrs["xylem.phase.duration_ms"])

	costParts := strings.Split(attrs["xylem.phase.cost_usd_est"], ".")
	require.Len(t, costParts, 2)
	assert.Len(t, costParts[1], 6)
}

func TestSmoke_PhaseSpanCarriesResolvedLLMProviderAndTier(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)
	r.Config.Providers = map[string]config.ProviderConfig{
		"claude": {
			Kind:    "claude",
			Command: "claude",
			Tiers:   map[string]string{"med": "claude-sonnet-4"},
		},
	}
	r.Config.LLMRouting = config.LLMRoutingConfig{
		DefaultTier: "med",
		Tiers: map[string]config.TierRouting{
			"med": {Providers: []string{"claude"}},
		},
	}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Completed)

	attrs := spanAttrMap(endedSpanByName(t, rec, "phase:analyze"))
	assert.Equal(t, "claude", attrs["llm.provider"])
	assert.Equal(t, "med", attrs["llm.tier"])
}

func TestSmoke_S15_PhaseSpanRecordsErrorOnPhaseFailure(t *testing.T) {
	tracer, rec := newTestTracer(t)
	runErr := errors.New("provider crashed")
	cmdRunner := &mockCmdRunner{
		phaseErr: runErr,
	}
	r := newWorkflowRunner(t, "trace-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	assert.True(t, spanHasExceptionEvent(phaseSpan, runErr.Error()))
	assert.Equal(t, codes.Error, phaseSpan.Status().Code)
}

func TestSmoke_S16_PhaseSpanAlwaysEndsEvenWhenPhaseFails(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseErr: errors.New("provider crashed"),
	}
	r := newWorkflowRunner(t, "trace-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	for _, spanName := range []string{"drain_run", "vessel:issue-1", "phase:analyze"} {
		spans := endedSpansByName(rec, spanName)
		require.Len(t, spans, 1)
		assert.False(t, spans[0].EndTime().IsZero())
	}
}

func TestSmoke_S16b_PhaseSpanStatusIsFailedForEarlyPhaseErrors(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{}
	r := newWorkflowRunner(t, "trace-early-fail", []testPhase{
		{name: "analyze", promptContent: "{{", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	attrs := spanAttrMap(phaseSpan)
	assert.Equal(t, "failed", attrs["xylem.phase.status"])
	assert.Equal(t, codes.Error, phaseSpan.Status().Code)
}

func TestSmoke_VesselSummaryLinksToTraceOnCompletion(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze": []byte("analysis complete"),
		},
	}
	r := newWorkflowRunner(t, "trace-basic", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Completed)

	summary, err := LoadVesselSummary(r.Config.StateDir, "issue-1")
	require.NoError(t, err)
	require.NotNil(t, summary.Trace)

	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	assert.Equal(t, vesselSpan.SpanContext().TraceID().String(), summary.Trace.TraceID)
	assert.Equal(t, vesselSpan.SpanContext().SpanID().String(), summary.Trace.SpanID)
}

func TestSmoke_VesselSummaryLinksToTraceOnFailure(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseErr: errors.New("provider crashed"),
	}
	r := newWorkflowRunner(t, "trace-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)

	summary, err := LoadVesselSummary(r.Config.StateDir, "issue-1")
	require.NoError(t, err)
	require.NotNil(t, summary.Trace)
	assert.Equal(t, "failed", summary.State)

	vesselSpan := endedSpanByName(t, rec, "vessel:issue-1")
	assert.Equal(t, vesselSpan.SpanContext().TraceID().String(), summary.Trace.TraceID)
	assert.Equal(t, vesselSpan.SpanContext().SpanID().String(), summary.Trace.SpanID)
}

func TestSmoke_S10_PhaseSpanIsAlwaysEndedEvenOnFailureDuringOrchestratedExecution(t *testing.T) {
	tracer, rec := newTestTracer(t)
	cmdRunner := &mockCmdRunner{
		phaseErr: errors.New("orchestrated crash"),
	}
	r := newWorkflowRunner(t, "trace-orchestrated-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
		{name: "implement", promptContent: "Implement {{.PreviousOutputs.analyze}}", maxTurns: 5, dependsOn: []string{"analyze"}},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Len(t, cmdRunner.phaseCalls, 1)

	spans := endedSpansByName(rec, "phase:analyze")
	require.Len(t, spans, 1)
	assert.False(t, spans[0].EndTime().IsZero())
}

func TestSmoke_S11_ErrorIsRecordedOnSpanBeforeEndDuringOrchestratedExecution(t *testing.T) {
	tracer, rec := newTestTracer(t)
	runErr := errors.New("orchestrated crash")
	cmdRunner := &mockCmdRunner{
		phaseErr: runErr,
	}
	r := newWorkflowRunner(t, "trace-orchestrated-fail", []testPhase{
		{name: "analyze", promptContent: "Analyze", maxTurns: 5},
		{name: "implement", promptContent: "Implement {{.PreviousOutputs.analyze}}", maxTurns: 5, dependsOn: []string{"analyze"}},
	}, cmdRunner, tracer)

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	phaseSpan := endedSpanByName(t, rec, "phase:analyze")
	event, ok := spanExceptionEvent(phaseSpan, runErr.Error())
	require.True(t, ok)
	require.False(t, phaseSpan.EndTime().IsZero())
	assert.False(t, event.Time.After(phaseSpan.EndTime()))
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
		{"auth failure is not rate limit", errors.New("authentication failed: invalid x-api-key"), false},
		{"provider unavailable is not rate limit", errors.New("service unavailable"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimitError(tt.err); got != tt.want {
				t.Errorf("isRateLimitError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyProviderError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want providerErrorDisposition
	}{
		{name: "nil error", err: nil, want: providerErrorFail},
		{name: "generic error", err: errors.New("exit status 1"), want: providerErrorFail},
		{name: "rate limit retry", err: errors.New(`API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`), want: providerErrorRetrySameProvider},
		{name: "credit balance retry", err: errors.New("Credit balance is too low"), want: providerErrorRetrySameProvider},
		{name: "auth failure falls back", err: errors.New("anthropic: authentication failed: invalid x-api-key"), want: providerErrorFallbackNextProvider},
		{name: "github token failure falls back", err: errors.New("copilot: authentication required: github token missing"), want: providerErrorFallbackNextProvider},
		{name: "model access failure falls back", err: errors.New("provider: not authorized to access this model"), want: providerErrorFallbackNextProvider},
		{name: "service unavailable falls back", err: errors.New("provider returned service unavailable"), want: providerErrorFallbackNextProvider},
		{name: "missing command falls back", err: errors.New("claude: executable file not found in $PATH"), want: providerErrorFallbackNextProvider},
		{name: "plain unauthorized does not fall back", err: errors.New("unauthorized"), want: providerErrorFail},
		{name: "plain forbidden does not fall back", err: errors.New("forbidden"), want: providerErrorFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyProviderError(tt.err); got != tt.want {
				t.Fatalf("classifyProviderError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRunPhaseWithRateLimitRetryDoesNotRetryAuthFailures(t *testing.T) {
	authErr := errors.New("anthropic: authentication failed: invalid x-api-key")
	var callCount int32
	cmdRunner := &mockCmdRunner{
		runPhaseHook: func(dir, prompt, name string, args ...string) ([]byte, error, bool) {
			atomic.AddInt32(&callCount, 1)
			return nil, authErr, true
		},
	}
	r := New(&config.Config{Concurrency: 1}, nil, &mockWorktree{}, cmdRunner)

	output, err := r.runPhaseWithRateLimitRetry(
		context.Background(),
		"issue-1",
		"implement",
		t.TempDir(),
		"Fix the bug",
		nil,
		"claude",
		[]string{"--model", "claude-med"},
	)
	require.ErrorIs(t, err, authErr)
	assert.Nil(t, output)
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount))
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

	result, err := r.DrainAndWait(context.Background())
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

func TestRunVesselLiveHTTPGatePersistsObservedInSituEvidence(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(401, "live-http-pass")
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "live-http-pass", []testPhase{{
		name:          "implement",
		promptContent: "Implement the fix",
		maxTurns:      5,
		gate: fmt.Sprintf(`      type: live
      retries: 0
      evidence:
        claim: "Smoke check passes against the running service"
        level: observed_in_situ
        checker: "xylem live gate"
        trust_boundary: "running service"
      live:
        mode: http
        http:
          base_url: %q
          steps:
            - name: health
              url: /health
              expect_status: 200
              expect_json:
                - path: $.status
                  equals: ok`, server.URL),
	}})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Implement the fix": []byte("done"),
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.LiveGate = &gate.LiveVerifier{HTTPClient: server.Client()}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Completed)

	final := queueVesselByID(t, q, vessel.ID)
	assert.Equal(t, queue.StateCompleted, final.State)

	manifest, err := evidence.LoadManifest(cfg.StateDir, vessel.ID)
	require.NoError(t, err)
	require.Len(t, manifest.Claims, 1)
	assert.Equal(t, evidence.ObservedInSitu, manifest.Claims[0].Level)
	assert.Contains(t, manifest.Claims[0].ArtifactPath, "live-gate.json")

	summary := loadSummary(t, cfg.StateDir, vessel.ID)
	assert.Equal(t, evidenceManifestRelativePath(vessel.ID), summary.EvidenceManifestPath)
}

func TestRunVesselLiveHTTPGateFailureFailsVessel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
	}))
	defer server.Close()

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(402, "live-http-fail")
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "live-http-fail", []testPhase{{
		name:          "implement",
		promptContent: "Implement the fix",
		maxTurns:      5,
		gate: fmt.Sprintf(`      type: live
      retries: 0
      live:
        mode: http
        http:
          base_url: %q
          steps:
            - name: health
              url: /health
              expect_status: 200
              expect_json:
                - path: $.status
                  equals: ok`, server.URL),
	}})
	withTestWorkingDir(t, dir)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Implement the fix": []byte("done"),
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.LiveGate = &gate.LiveVerifier{HTTPClient: server.Client()}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)

	final := queueVesselByID(t, q, vessel.ID)
	assert.Equal(t, queue.StateFailed, final.State)
	assert.Contains(t, final.Error, "gate failed")
	assert.Contains(t, final.GateOutput, "live gate failed")
}

func TestRunVesselLiveGateEmitsStepSpans(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(makeVessel(403, "live-http-trace"))
	require.NoError(t, err)

	writeWorkflowFile(t, dir, "live-http-trace", []testPhase{{
		name:          "implement",
		promptContent: "Implement the fix",
		maxTurns:      5,
		gate: fmt.Sprintf(`      type: live
      retries: 0
      live:
        mode: http
        http:
          base_url: %q
          steps:
            - name: health
              url: /health
              expect_status: 200`, server.URL),
	}})
	withTestWorkingDir(t, dir)

	tracer, rec := newTestTracer(t)
	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Implement the fix": []byte("done"),
		},
	})
	r.Tracer = tracer
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.LiveGate = &gate.LiveVerifier{HTTPClient: server.Client()}

	_, err = r.DrainAndWait(context.Background())
	require.NoError(t, err)

	gateSpan := endedSpanByName(t, rec, "gate:live")
	gateAttrs := spanAttrMap(gateSpan)
	assert.Equal(t, "live", gateAttrs["xylem.gate.type"])
	assert.Equal(t, "true", gateAttrs["xylem.gate.passed"])

	stepSpan := endedSpanByName(t, rec, "gate_step:health")
	stepAttrs := spanAttrMap(stepSpan)
	assert.Equal(t, "health", stepAttrs["xylem.gate.step.name"])
	assert.Equal(t, "http", stepAttrs["xylem.gate.step.mode"])
	assert.Equal(t, "true", stepAttrs["xylem.gate.step.passed"])
	assert.Equal(t, gateSpan.SpanContext().SpanID(), stepSpan.Parent().SpanID())
}
