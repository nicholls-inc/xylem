package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfigFile(t *testing.T, yaml string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	return path
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}

	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %q", want, err.Error())
	}
}

func TestLoadValid(t *testing.T) {
	path := writeConfigFile(t, `repo: owner/name
tasks:
  fix-bugs:
    labels: [bug, ready-for-work]
    workflow: fix-bug
concurrency: 3
max_turns: 30
timeout: "15m"
state_dir: ".xylem"
exclude: [wontfix, duplicate]
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
  flags: "--bare"
  env:
    ANTHROPIC_API_KEY: "test-key"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Repo != "owner/name" {
		t.Fatalf("Repo = %q, want owner/name", cfg.Repo)
	}

	task, ok := cfg.Tasks["fix-bugs"]
	if !ok {
		t.Fatalf("missing task fix-bugs")
	}

	if len(task.Labels) != 2 || task.Labels[0] != "bug" || task.Labels[1] != "ready-for-work" {
		t.Fatalf("task labels = %#v, want [bug ready-for-work]", task.Labels)
	}

	if task.Workflow != "fix-bug" {
		t.Fatalf("task workflow = %q, want fix-bug", task.Workflow)
	}

	if cfg.Concurrency != 3 {
		t.Fatalf("Concurrency = %d, want 3", cfg.Concurrency)
	}

	if cfg.MaxTurns != 30 {
		t.Fatalf("MaxTurns = %d, want 30", cfg.MaxTurns)
	}

	if cfg.Timeout != "15m" {
		t.Fatalf("Timeout = %q, want 15m", cfg.Timeout)
	}

	if cfg.StateDir != ".xylem" {
		t.Fatalf("StateDir = %q, want .xylem", cfg.StateDir)
	}

	if len(cfg.Exclude) != 2 || cfg.Exclude[0] != "wontfix" || cfg.Exclude[1] != "duplicate" {
		t.Fatalf("Exclude = %#v, want [wontfix duplicate]", cfg.Exclude)
	}

	if cfg.Claude.Command != "claude" {
		t.Fatalf("Claude.Command = %q, want claude", cfg.Claude.Command)
	}

	if cfg.Claude.Flags != "--bare" {
		t.Fatalf("Claude.Flags = %q, want --bare", cfg.Claude.Flags)
	}

	if cfg.Claude.Env["ANTHROPIC_API_KEY"] != "test-key" {
		t.Fatalf("Claude.Env[ANTHROPIC_API_KEY] = %q, want test-key", cfg.Claude.Env["ANTHROPIC_API_KEY"])
	}

	// Legacy config should be normalized into Sources
	if len(cfg.Sources) != 1 {
		t.Fatalf("expected 1 source after normalization, got %d", len(cfg.Sources))
	}
	ghSrc, ok := cfg.Sources["github"]
	if !ok {
		t.Fatal("expected github source after normalization")
	}
	if ghSrc.Repo != "owner/name" {
		t.Fatalf("source repo = %q, want owner/name", ghSrc.Repo)
	}
}

func TestLoadDefaults(t *testing.T) {
	path := writeConfigFile(t, `repo: owner/name
tasks:
  fix-bugs:
    labels: [bug]
    workflow: fix-bug
claude:
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Concurrency != 2 {
		t.Fatalf("Concurrency = %d, want 2", cfg.Concurrency)
	}

	if cfg.MaxTurns != 50 {
		t.Fatalf("MaxTurns = %d, want 50", cfg.MaxTurns)
	}

	if cfg.Timeout != "30m" {
		t.Fatalf("Timeout = %q, want 30m", cfg.Timeout)
	}

	if cfg.StateDir != ".xylem" {
		t.Fatalf("StateDir = %q, want .xylem", cfg.StateDir)
	}

	wantExclude := []string{"wontfix", "duplicate", "in-progress", "no-bot"}
	if len(cfg.Exclude) != len(wantExclude) {
		t.Fatalf("Exclude length = %d, want %d (%#v)", len(cfg.Exclude), len(wantExclude), cfg.Exclude)
	}

	for i := range wantExclude {
		if cfg.Exclude[i] != wantExclude[i] {
			t.Fatalf("Exclude[%d] = %q, want %q", i, cfg.Exclude[i], wantExclude[i])
		}
	}

	if cfg.Claude.Command != "claude" {
		t.Fatalf("Claude.Command = %q, want claude", cfg.Claude.Command)
	}

	if cfg.Claude.Flags != "" {
		t.Fatalf("Claude.Flags = %q, want empty", cfg.Claude.Flags)
	}

	// Daemon defaults
	if cfg.Daemon.ScanInterval != "60s" {
		t.Fatalf("Daemon.ScanInterval = %q, want 60s", cfg.Daemon.ScanInterval)
	}
	if cfg.Daemon.DrainInterval != "30s" {
		t.Fatalf("Daemon.DrainInterval = %q, want 30s", cfg.Daemon.DrainInterval)
	}

	// Legacy config should be normalized into Sources
	if len(cfg.Sources) != 1 {
		t.Fatalf("expected 1 source after normalization, got %d", len(cfg.Sources))
	}
}

func validConfig() *Config {
	return &Config{
		Repo: "owner/name",
		Tasks: map[string]Task{
			"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
		},
		Sources: map[string]SourceConfig{
			"github": {
				Type: "github",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
				},
			},
		},
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude: ClaudeConfig{
			Command:      "claude",
			DefaultModel: "claude-sonnet-4-6",
		},
	}
}

func TestValidateMissingRepoInGitHubSource(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"github": {Type: "github", Repo: "", Tasks: map[string]Task{
				"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
			}},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "repo")
}

func TestValidateNoSourcesNoRepoIsValid(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
	}
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected valid config with no sources (enqueue-only), got: %v", err)
	}
}

func TestValidateEmptyTasksInGitHubSource(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"github": {Type: "github", Repo: "owner/name", Tasks: nil},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "task")
}

func TestValidateZeroConcurrency(t *testing.T) {
	cfg := validConfig()
	cfg.Concurrency = 0

	err := cfg.Validate()
	requireErrorContains(t, err, "concurrency")
}

func TestValidateInvalidTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.Timeout = "invalid"

	err := cfg.Validate()
	requireErrorContains(t, err, "timeout")
}

func TestValidateTaskMissingLabels(t *testing.T) {
	cfg := validConfig()
	cfg.Sources["github"] = SourceConfig{
		Type: "github", Repo: "owner/name",
		Tasks: map[string]Task{"fix-bugs": {Workflow: "fix-bug"}},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, "labels")
}

func TestValidateTaskMissingWorkflow(t *testing.T) {
	cfg := validConfig()
	cfg.Sources["github"] = SourceConfig{
		Type: "github", Repo: "owner/name",
		Tasks: map[string]Task{"fix-bugs": {Labels: []string{"bug"}}},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, "workflow")
}

func TestMalformedYAML(t *testing.T) {
	path := writeConfigFile(t, "repo: [owner/name\n")

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for malformed yaml")
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
}

func TestValidateZeroMaxTurns(t *testing.T) {
	cfg := validConfig()
	cfg.MaxTurns = 0

	err := cfg.Validate()
	requireErrorContains(t, err, "max_turns")
}

func TestValidateTemplateRejected(t *testing.T) {
	cfg := validConfig()
	cfg.Claude.Template = "{{.Command}} -p \"/{{.Workflow}} {{.Ref}}\" --max-turns {{.MaxTurns}}"

	err := cfg.Validate()
	requireErrorContains(t, err, "claude.template is no longer supported")
}

func TestValidateTimeoutTooLow(t *testing.T) {
	cfg := validConfig()
	cfg.Timeout = "5s"

	err := cfg.Validate()
	requireErrorContains(t, err, "timeout must be at least")
}

func TestValidateMalformedRepo(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want string
	}{
		{"no slash", "ownername", "owner/name format"},
		{"trailing slash", "owner/", "owner/name format"},
		{"leading slash", "/name", "owner/name format"},
		{"multiple slashes", "owner/name/extra", "owner/name format"},
		{"just slash", "/", "owner/name format"},
		{"whitespace parts", " / ", "owner/name format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Concurrency: 2,
				MaxTurns:    50,
				Timeout:     "30m",
				Claude:      ClaudeConfig{Command: "claude"},
				Sources: map[string]SourceConfig{
					"github": {
						Type: "github", Repo: tt.repo,
						Tasks: map[string]Task{"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
					},
				},
			}
			err := cfg.Validate()
			requireErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateTaskWorkflowWhitespaceOnly(t *testing.T) {
	cfg := validConfig()
	cfg.Sources["github"] = SourceConfig{
		Type: "github", Repo: "owner/name",
		Tasks: map[string]Task{"fix-bugs": {Labels: []string{"bug"}, Workflow: "  "}},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, "workflow")
}

func TestValidateTimeoutExactlyMinimum(t *testing.T) {
	cfg := validConfig()
	cfg.Timeout = "30s" // exactly the minimum

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected 30s timeout to be valid, got: %v", err)
	}
}

func TestValidateMultipleTasksOneInvalid(t *testing.T) {
	cfg := validConfig()
	tasks := map[string]Task{
		"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
		"broken":   {Labels: []string{}, Workflow: "fix-bug"}, // no labels
	}
	cfg.Sources["github"] = SourceConfig{Type: "github", Repo: "owner/name", Tasks: tasks}

	err := cfg.Validate()
	requireErrorContains(t, err, "labels")
}

func TestValidateAllowedToolsRejected(t *testing.T) {
	cfg := validConfig()
	cfg.Claude.AllowedTools = []string{"WebFetch"}

	err := cfg.Validate()
	requireErrorContains(t, err, "claude.allowed_tools is no longer supported")
}

func TestValidateAllowedToolsEmpty(t *testing.T) {
	cfg := validConfig()
	cfg.Claude.AllowedTools = []string{}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected empty allowed_tools to be valid, got: %v", err)
	}
}

func TestValidateBareWithoutAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.Claude.Flags = "--bare --dangerously-skip-permissions"

	err := cfg.Validate()
	requireErrorContains(t, err, "--bare requires ANTHROPIC_API_KEY in claude.env")
}

func TestValidateBareWithAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.Claude.Flags = "--bare --dangerously-skip-permissions"
	cfg.Claude.Env = map[string]string{"ANTHROPIC_API_KEY": "test-key"}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected valid config with --bare and API key, got: %v", err)
	}
}

func TestValidateDaemonIntervalValid(t *testing.T) {
	cfg := validConfig()
	cfg.Daemon = DaemonConfig{
		ScanInterval:  "60s",
		DrainInterval: "30s",
	}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected valid daemon config, got: %v", err)
	}
}

func TestValidateDaemonScanIntervalInvalid(t *testing.T) {
	cfg := validConfig()
	cfg.Daemon = DaemonConfig{
		ScanInterval: "not-a-duration",
	}

	err := cfg.Validate()
	requireErrorContains(t, err, "daemon.scan_interval must be a valid duration")
}

func TestValidateDaemonDrainIntervalInvalid(t *testing.T) {
	cfg := validConfig()
	cfg.Daemon = DaemonConfig{
		DrainInterval: "bad",
	}

	err := cfg.Validate()
	requireErrorContains(t, err, "daemon.drain_interval must be a valid duration")
}

func TestLoadWithFlagsAndEnv(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
  flags: "--bare --dangerously-skip-permissions"
  env:
    ANTHROPIC_API_KEY: "sk-test-123"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Claude.Flags != "--bare --dangerously-skip-permissions" {
		t.Fatalf("Claude.Flags = %q, want --bare --dangerously-skip-permissions", cfg.Claude.Flags)
	}
	if cfg.Claude.Env["ANTHROPIC_API_KEY"] != "sk-test-123" {
		t.Fatalf("Claude.Env[ANTHROPIC_API_KEY] = %q, want sk-test-123", cfg.Claude.Env["ANTHROPIC_API_KEY"])
	}
}

func TestLoadRejectsOldTemplate(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  template: "{{.Command}} -p \"/{{.Workflow}} {{.Ref}}\" --max-turns {{.MaxTurns}}"
`)

	_, err := Load(path)
	requireErrorContains(t, err, "claude.template is no longer supported")
}

func TestLoadRejectsOldAllowedTools(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  allowed_tools:
    - "Bash(gh issue view *)"
`)

	_, err := Load(path)
	requireErrorContains(t, err, "claude.allowed_tools is no longer supported")
}

func TestValidateCostBudgetNegativeMaxTokens(t *testing.T) {
	cfg := validConfig()
	cfg.Cost.Budget = &BudgetConfig{MaxTokens: -1}

	err := cfg.Validate()
	requireErrorContains(t, err, "cost.budget.max_tokens")
}

func TestLoadDefaultModel(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-20250514"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Claude.DefaultModel != "claude-sonnet-4-20250514" {
		t.Fatalf("Claude.DefaultModel = %q, want claude-sonnet-4-20250514", cfg.Claude.DefaultModel)
	}
}

func TestValidateClaudeDefaultModelRequired(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
`)

	_, err := Load(path)
	requireErrorContains(t, err, "claude.default_model must be set")
}

func TestLoadStatusLabels(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
        status_labels:
          queued: "queued"
          running: "in-progress"
          completed: "done"
          failed: "failed"
          timed_out: "timed-out"
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	src, ok := cfg.Sources["github"]
	if !ok {
		t.Fatal("missing github source")
	}
	task, ok := src.Tasks["fix-bugs"]
	if !ok {
		t.Fatal("missing fix-bugs task")
	}

	if task.StatusLabels == nil {
		t.Fatal("StatusLabels should not be nil when status_labels block is present")
	}
	sl := task.StatusLabels
	if sl.Queued != "queued" {
		t.Errorf("StatusLabels.Queued = %q, want queued", sl.Queued)
	}
	if sl.Running != "in-progress" {
		t.Errorf("StatusLabels.Running = %q, want in-progress", sl.Running)
	}
	if sl.Completed != "done" {
		t.Errorf("StatusLabels.Completed = %q, want done", sl.Completed)
	}
	if sl.Failed != "failed" {
		t.Errorf("StatusLabels.Failed = %q, want failed", sl.Failed)
	}
	if sl.TimedOut != "timed-out" {
		t.Errorf("StatusLabels.TimedOut = %q, want timed-out", sl.TimedOut)
	}
}

func TestLoadStatusLabelsOmitted(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	task := cfg.Sources["github"].Tasks["fix-bugs"]
	if task.StatusLabels != nil {
		t.Errorf("expected StatusLabels to be nil when status_labels block is omitted, got %+v", task.StatusLabels)
	}
}

func TestLoadDaemonConfig(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
daemon:
  scan_interval: "120s"
  drain_interval: "45s"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Daemon.ScanInterval != "120s" {
		t.Fatalf("Daemon.ScanInterval = %q, want 120s", cfg.Daemon.ScanInterval)
	}
	if cfg.Daemon.DrainInterval != "45s" {
		t.Fatalf("Daemon.DrainInterval = %q, want 45s", cfg.Daemon.DrainInterval)
	}
}

func TestValidateLLM(t *testing.T) {
	tests := []struct {
		name    string
		llm     string
		wantErr string
	}{
		{"claude explicit", "claude", ""},
		{"copilot explicit", "copilot", ""},
		{"empty defaults to claude", "", ""},
		{"invalid value", "gpt4", `llm must be "claude" or "copilot"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.LLM = tt.llm
			// Ensure copilot config is set for "copilot" to avoid command/model validation failure
			if tt.llm == "copilot" {
				cfg.Copilot = CopilotConfig{Command: "copilot", DefaultModel: "gpt-5.4"}
			}
			err := cfg.Validate()
			if tt.wantErr != "" {
				requireErrorContains(t, err, tt.wantErr)
			} else if err != nil {
				t.Fatalf("expected valid config with llm=%q, got: %v", tt.llm, err)
			}
		})
	}
}

func TestValidateCopilotEmptyCommand(t *testing.T) {
	cfg := validConfig()
	cfg.LLM = "copilot"
	cfg.Copilot = CopilotConfig{Command: ""}
	err := cfg.Validate()
	requireErrorContains(t, err, "copilot.command must be non-empty")
}

func TestLoadCopilotConfig(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
llm: copilot
copilot:
  command: "copilot"
  flags: "--headless"
  default_model: "gpt-4o"
  env:
    GITHUB_TOKEN: "ghp-test"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.LLM != "copilot" {
		t.Fatalf("LLM = %q, want copilot", cfg.LLM)
	}
	if cfg.Copilot.Command != "copilot" {
		t.Fatalf("Copilot.Command = %q, want copilot", cfg.Copilot.Command)
	}
	if cfg.Copilot.Flags != "--headless" {
		t.Fatalf("Copilot.Flags = %q, want --headless", cfg.Copilot.Flags)
	}
	if cfg.Copilot.DefaultModel != "gpt-4o" {
		t.Fatalf("Copilot.DefaultModel = %q, want gpt-4o", cfg.Copilot.DefaultModel)
	}
	if cfg.Copilot.Env["GITHUB_TOKEN"] != "ghp-test" {
		t.Fatalf("Copilot.Env[GITHUB_TOKEN] = %q, want ghp-test", cfg.Copilot.Env["GITHUB_TOKEN"])
	}
}

func TestLoadCopilotDefaultCommand(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	// Default copilot command should be set even when copilot block is absent
	if cfg.Copilot.Command != "copilot" {
		t.Fatalf("Copilot.Command default = %q, want copilot", cfg.Copilot.Command)
	}
}

func TestLoadLLMAbsentDefaultsClaude(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.LLM != "" {
		t.Fatalf("LLM = %q, want empty (defaults to claude at runtime)", cfg.LLM)
	}
}

func TestValidateGitHubPREventsValid(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"review": {
						Workflow: "handle-review",
						On: &PREventsConfig{
							Labels:          []string{"needs-review"},
							ReviewSubmitted: true,
							AuthorAllow:     []string{"copilot-pull-request-reviewer[bot]"},
						},
					},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidateGitHubPREventsReviewSubmittedRequiresAuthorFilter(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"review": {
						Workflow: "handle-review",
						On:       &PREventsConfig{ReviewSubmitted: true},
					},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "author_allow or author_deny")
}

func TestValidateGitHubPREventsCommentedRequiresAuthorFilter(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"comment": {
						Workflow: "respond",
						On:       &PREventsConfig{Commented: true},
					},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "author_allow or author_deny")
}

func TestValidateGitHubPREventsAuthorDenyAloneIsEnough(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"review": {
						Workflow: "handle-review",
						On: &PREventsConfig{
							ReviewSubmitted: true,
							AuthorDeny:      []string{"some-bot"},
						},
					},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid (author_deny alone is sufficient), got: %v", err)
	}
}

// TestValidateGitHubPREventsChecksFailedNoAuthorFilter confirms that
// checks_failed (a non-authored trigger) does not require author_allow /
// author_deny — it scans CI state, not user-authored events.
func TestValidateGitHubPREventsChecksFailedNoAuthorFilter(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"checks": {
						Workflow: "fix-ci",
						On:       &PREventsConfig{ChecksFailed: true},
					},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidateGitHubPREventsMissingOn(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"review": {Workflow: "handle-review"},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "'on' block")
}

func TestValidateGitHubPREventsEmptyTriggers(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"review": {
						Workflow: "handle-review",
						On:       &PREventsConfig{},
					},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "at least one trigger")
}

func TestValidateGitHubPREventsMissingRepo(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"pr-events": {
				Type: "github-pr-events",
				Repo: "",
				Tasks: map[string]Task{
					"review": {
						Workflow: "handle-review",
						On:       &PREventsConfig{ReviewSubmitted: true},
					},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "repo is required")
}

func TestValidateGitHubMergeValid(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"merge": {
				Type: "github-merge",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"deploy": {Workflow: "post-merge"},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidateGitHubMergeMissingRepo(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"merge": {
				Type:  "github-merge",
				Repo:  "",
				Tasks: map[string]Task{"deploy": {Workflow: "post-merge"}},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "repo is required")
}

func TestValidateScheduleValid(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"doctor": {
				Type:     "schedule",
				Cadence:  "@daily",
				Workflow: "doctor",
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid schedule config, got: %v", err)
	}
}

func TestValidateScheduleMissingWorkflow(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"doctor": {
				Type:    "schedule",
				Cadence: "1h",
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "workflow is required")
}

func TestValidateScheduleMalformedCadence(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"doctor": {
				Type:     "schedule",
				Cadence:  "bad cadence",
				Workflow: "doctor",
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "parse cadence")
}

func TestValidateScheduleRejectsTasks(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"doctor": {
				Type:     "schedule",
				Cadence:  "1h",
				Workflow: "doctor",
				Tasks: map[string]Task{
					"unused": {Workflow: "other"},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "tasks are not supported")
}

func TestValidateUnknownSourceType(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"unknown": {
				Type: "bitbucket",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"test": {Workflow: "test-wf"},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "unknown type")
}

func TestLoadTopLevelModel(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
model: claude-sonnet-4-6
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Model != "claude-sonnet-4-6" {
		t.Fatalf("Model = %q, want claude-sonnet-4-6", cfg.Model)
	}
}

func TestLoadSourceLLMAndModel(t *testing.T) {
	path := writeConfigFile(t, `sources:
  triage:
    type: github
    repo: owner/name
    llm: claude
    model: claude-haiku-4-5
    tasks:
      triage-issues:
        labels: [needs-triage]
        workflow: triage
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	src := cfg.Sources["triage"]
	if src.LLM != "claude" {
		t.Fatalf("Sources[triage].LLM = %q, want claude", src.LLM)
	}
	if src.Model != "claude-haiku-4-5" {
		t.Fatalf("Sources[triage].Model = %q, want claude-haiku-4-5", src.Model)
	}
}

func TestValidateSourceLLMInvalid(t *testing.T) {
	cfg := &Config{
		Concurrency: 1,
		MaxTurns:    10,
		Timeout:     "30m",
		Sources: map[string]SourceConfig{
			"bad": {
				Type: "github",
				Repo: "owner/name",
				LLM:  "invalid-provider",
				Tasks: map[string]Task{
					"test": {Labels: []string{"bug"}, Workflow: "fix-bug"},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "llm must be")
}

func writeSmokeConfigFile(t *testing.T, extra string) string {
	t.Helper()

	return writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`+extra)
}

func newSmokeIntermediary(t *testing.T, cfg *Config) *intermediary.Intermediary {
	t.Helper()

	auditLog := intermediary.NewAuditLog(filepath.Join(t.TempDir(), "audit.jsonl"))
	return intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)
}

func TestSmoke_S1_FullConfigLoadsWithHarnessSection(t *testing.T) {
	path := writeSmokeConfigFile(t, `state_dir: ".xylem"
harness:
  audit_log: "audit.jsonl"
  protected_surfaces:
    paths:
      - ".xylem/HARNESS.md"
      - ".xylem.yml"
      - ".xylem/workflows/*.yaml"
      - ".xylem/prompts/*/*.md"
  policy:
    rules:
      - action: "file_write"
        resource: ".xylem/*"
        effect: "deny"
      - action: "git_push"
        resource: "*"
        effect: "require_approval"
      - action: "pr_create"
        resource: "*"
        effect: "require_approval"
      - action: "*"
        resource: "*"
        effect: "allow"
observability:
  enabled: true
  sample_rate: 1.0
cost:
  budget:
    max_cost_usd: 10.0
    max_tokens: 1000000
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "audit.jsonl", cfg.Harness.AuditLog)
	require.Len(t, cfg.Harness.ProtectedSurfaces.Paths, 4)
	require.Len(t, cfg.Harness.Policy.Rules, 4)
	require.NotNil(t, cfg.Observability.Enabled)
	assert.True(t, *cfg.Observability.Enabled)
	assert.Equal(t, 1.0, cfg.Observability.SampleRate)
	require.NotNil(t, cfg.Cost.Budget)
	assert.Equal(t, 10.0, cfg.Cost.Budget.MaxCostUSD)
	assert.Equal(t, 1000000, cfg.Cost.Budget.MaxTokens)
}

func TestSmoke_S2_NoHarnessSectionDefaultsActivate(t *testing.T) {
	cfg, err := Load(writeSmokeConfigFile(t, ""))
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// DefaultProtectedSurfaces is now empty (see rationale in config.go).
	// EffectiveProtectedSurfaces() returns nil when no paths are configured,
	// which is equivalent to "nothing protected" — the self-improving
	// default. Deployments that want strict enforcement opt in explicitly
	// via harness.protected_surfaces.paths.
	assert.Empty(t, cfg.EffectiveProtectedSurfaces())
	assert.Equal(t, DefaultAuditLogPath, cfg.EffectiveAuditLogPath())
	assert.Equal(t, "manual", cfg.HarnessReviewCadence())
	assert.Equal(t, 10, cfg.HarnessReviewEveryNRuns())
	assert.Equal(t, 50, cfg.HarnessReviewLookbackRuns())
	assert.Equal(t, 3, cfg.HarnessReviewMinSamples())
	assert.Equal(t, "reviews", cfg.HarnessReviewOutputDir())
	assert.True(t, cfg.ObservabilityEnabled())
	assert.Equal(t, 1.0, cfg.ObservabilitySampleRate())
	assert.Nil(t, cfg.VesselBudget())

	policies := cfg.BuildIntermediaryPolicies()
	require.Len(t, policies, 1)
	assert.Equal(t, "default", policies[0].Name)
}

func TestSmoke_S3_PathsNoneDisablesProtection(t *testing.T) {
	cfg, err := Load(writeSmokeConfigFile(t, `harness:
  protected_surfaces:
    paths: ["none"]
`))
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Nil(t, cfg.EffectiveProtectedSurfaces())
}

func TestSmoke_S4_InvalidGlobRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `harness:
  protected_surfaces:
    paths:
      - "[invalid-glob"
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness.protected_surfaces.paths")
	assert.Contains(t, err.Error(), "invalid glob")
	assert.Contains(t, err.Error(), "[invalid-glob")
}

func TestSmoke_S5_InvalidPolicyEffectRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `harness:
  policy:
    rules:
      - action: "file_write"
        resource: "*"
        effect: "approve_maybe"
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness.policy.rules[0]")
	assert.Contains(t, err.Error(), "invalid effect")
	assert.Contains(t, err.Error(), "approve_maybe")
}

func TestSmoke_S6_DefaultPolicyDeniesHarnessWrite(t *testing.T) {
	result := newSmokeIntermediary(t, validConfig()).Evaluate(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/HARNESS.md",
		AgentID:  "vessel-001",
	})

	assert.Equal(t, intermediary.Deny, result.Effect)
	require.NotNil(t, result.MatchedRule)
	assert.Equal(t, "file_write", result.MatchedRule.Action)
	assert.Equal(t, ".xylem/HARNESS.md", result.MatchedRule.Resource)
}

func TestSmoke_S7_DefaultPolicyAllowsGitPush(t *testing.T) {
	// Autonomous self-healing requires git_push to succeed without manual
	// approval. Operators who want approval gates can override via
	// harness.policy in .xylem.yml.
	result := newSmokeIntermediary(t, validConfig()).Evaluate(intermediary.Intent{
		Action:   "git_push",
		Resource: "main",
		AgentID:  "vessel-002",
	})

	assert.Equal(t, intermediary.Allow, result.Effect)
	require.NotNil(t, result.MatchedRule)
	assert.Equal(t, "*", result.MatchedRule.Action)
	assert.Equal(t, "*", result.MatchedRule.Resource)
}

func TestDefaultPolicyAllowsClassifiedGitLifecycleActions(t *testing.T) {
	tests := []struct {
		name     string
		action   string
		resource string
	}{
		{
			name:     "git commit",
			action:   "git_commit",
			resource: "*",
		},
		{
			name:     "git push",
			action:   "git_push",
			resource: "main",
		},
		{
			name:     "pull request create",
			action:   "pr_create",
			resource: "owner/name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := newSmokeIntermediary(t, validConfig()).Evaluate(intermediary.Intent{
				Action:   tt.action,
				Resource: tt.resource,
				AgentID:  "vessel-009",
			})

			assert.Equal(t, intermediary.Allow, result.Effect)
			require.NotNil(t, result.MatchedRule)
			assert.Equal(t, "*", result.MatchedRule.Action)
			assert.Equal(t, "*", result.MatchedRule.Resource)
		})
	}
}

func TestDefaultPolicyDeniesPromptFileWrite(t *testing.T) {
	result := newSmokeIntermediary(t, validConfig()).Evaluate(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/prompts/fix-bug/analyze.md",
		AgentID:  "vessel-004",
	})

	assert.Equal(t, intermediary.Deny, result.Effect)
	require.NotNil(t, result.MatchedRule)
	assert.Equal(t, ".xylem/prompts/*/*.md", result.MatchedRule.Resource)
}

func TestSmoke_S8_DefaultPolicyAllowsPhaseExecute(t *testing.T) {
	result := newSmokeIntermediary(t, validConfig()).Evaluate(intermediary.Intent{
		Action:   "phase_execute",
		Resource: "lint",
		AgentID:  "vessel-003",
	})

	assert.Equal(t, intermediary.Allow, result.Effect)
	require.NotNil(t, result.MatchedRule)
	assert.Equal(t, "*", result.MatchedRule.Action)
	assert.Equal(t, "*", result.MatchedRule.Resource)
}

func TestSmoke_S29_ObservabilityDefaultsWhenAbsent(t *testing.T) {
	cfg := Config{}

	assert.True(t, cfg.ObservabilityEnabled())
	assert.Equal(t, 1.0, cfg.ObservabilitySampleRate())
}

func TestSmoke_S30_CostBudgetLoadsCorrectly(t *testing.T) {
	path := writeSmokeConfigFile(t, `cost:
  budget:
    max_cost_usd: 5.0
    max_tokens: 500000
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	budget := cfg.VesselBudget()
	require.NotNil(t, budget)
	assert.Equal(t, 5.0, budget.CostLimitUSD)
	assert.Equal(t, 500000, budget.TokenLimit)
}

func TestSmoke_S31_NegativeSampleRateRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `observability:
  sample_rate: -0.5
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "observability.sample_rate")
}

func TestSmoke_S32_NegativeMaxCostRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `cost:
  budget:
    max_cost_usd: -1.0
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cost.budget.max_cost_usd")
}

func TestSmoke_S33_HarnessReviewLoadsAndDefaults(t *testing.T) {
	path := writeSmokeConfigFile(t, `harness:
  review:
    enabled: true
    cadence: every_n_runs
    every_n_runs: 7
    lookback_runs: 12
    min_samples: 4
    output_dir: "insights"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "every_n_runs", cfg.HarnessReviewCadence())
	assert.Equal(t, 7, cfg.HarnessReviewEveryNRuns())
	assert.Equal(t, 12, cfg.HarnessReviewLookbackRuns())
	assert.Equal(t, 4, cfg.HarnessReviewMinSamples())
	assert.Equal(t, "insights", cfg.HarnessReviewOutputDir())
}

func TestSmoke_S34_HarnessReviewInvalidCadenceRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `harness:
  review:
    enabled: true
    cadence: hourly
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness.review.cadence")
}

func TestSmoke_S35_HarnessReviewRejectsAbsoluteOutputDir(t *testing.T) {
	path := writeSmokeConfigFile(t, `harness:
  review:
    output_dir: "/tmp/reviews"
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness.review.output_dir")
}

func TestSmoke_S36_ScheduledSourceLoads(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"sota-gap": {
			Type:     "scheduled",
			Repo:     "owner/repo",
			Schedule: "@weekly",
			Tasks: map[string]Task{
				"weekly": {Workflow: "sota-gap-analysis", Ref: "sota-gap-analysis"},
			},
		},
	}

	err := cfg.Validate()
	require.NoError(t, err)

	sourceCfg := cfg.Sources["sota-gap"]
	assert.Equal(t, "scheduled", sourceCfg.Type)
	assert.Equal(t, "@weekly", sourceCfg.Schedule)
	assert.Equal(t, "sota-gap-analysis", sourceCfg.Tasks["weekly"].Workflow)
	assert.Equal(t, "sota-gap-analysis", sourceCfg.Tasks["weekly"].Ref)
}

func TestSmoke_S37_ScheduledSourceRequiresRef(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"sota-gap": {
			Type:     "scheduled",
			Repo:     "owner/repo",
			Schedule: "@weekly",
			Tasks: map[string]Task{
				"weekly": {Workflow: "sota-gap-analysis"},
			},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ref is required")
}

func TestSmoke_S38_ScheduledSourceRejectsInvalidSchedule(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"sota-gap": {
			Type:     "scheduled",
			Repo:     "owner/repo",
			Schedule: "weeklyish",
			Tasks: map[string]Task{
				"weekly": {Workflow: "sota-gap-analysis", Ref: "sota-gap-analysis"},
			},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schedule is invalid")
}

func TestSourceTimeoutValid(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"slow": {
			Type:    "github",
			Repo:    "owner/name",
			Timeout: "1h",
			Tasks: map[string]Task{
				"impl": {Labels: []string{"implement"}, Workflow: "implement-feature"},
			},
		},
	}
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected valid config with source timeout, got: %v", err)
	}
}

func TestSourceTimeoutInvalidDuration(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"bad": {
			Type:    "github",
			Repo:    "owner/name",
			Timeout: "bad",
			Tasks: map[string]Task{
				"impl": {Labels: []string{"implement"}, Workflow: "implement-feature"},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "valid duration")
}

func TestSourceTimeoutTooShort(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"fast": {
			Type:    "github",
			Repo:    "owner/name",
			Timeout: "5s",
			Tasks: map[string]Task{
				"impl": {Labels: []string{"implement"}, Workflow: "implement-feature"},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "at least")
}

func TestSourceTimeoutEmptyInheritsGlobal(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"default": {
			Type: "github",
			Repo: "owner/name",
			Tasks: map[string]Task{
				"impl": {Labels: []string{"implement"}, Workflow: "implement-feature"},
			},
		},
	}
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected valid config with no source timeout (inherits global), got: %v", err)
	}
}
