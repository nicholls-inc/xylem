package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
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
			Command: "claude",
		},
	}
}

func TestValidateMissingRepoInGitHubSource(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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

func TestLoadWithHarnessObservabilityAndCost(t *testing.T) {
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
state_dir: ".xylem"
claude:
  command: "claude"
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
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Harness.AuditLog != "audit.jsonl" {
		t.Fatalf("Harness.AuditLog = %q, want %q", cfg.Harness.AuditLog, "audit.jsonl")
	}
	if len(cfg.Harness.ProtectedSurfaces.Paths) != 4 {
		t.Fatalf("len(Harness.ProtectedSurfaces.Paths) = %d, want 4", len(cfg.Harness.ProtectedSurfaces.Paths))
	}
	if len(cfg.Harness.Policy.Rules) != 4 {
		t.Fatalf("len(Harness.Policy.Rules) = %d, want 4", len(cfg.Harness.Policy.Rules))
	}
	if cfg.Observability.Enabled == nil || !*cfg.Observability.Enabled {
		t.Fatalf("Observability.Enabled = %#v, want true pointer", cfg.Observability.Enabled)
	}
	if cfg.Observability.SampleRate != 1.0 {
		t.Fatalf("Observability.SampleRate = %v, want 1.0", cfg.Observability.SampleRate)
	}
	if cfg.Cost.Budget == nil {
		t.Fatal("Cost.Budget = nil, want budget config")
	}
	if cfg.Cost.Budget.MaxCostUSD != 10.0 {
		t.Fatalf("Cost.Budget.MaxCostUSD = %v, want 10.0", cfg.Cost.Budget.MaxCostUSD)
	}
	if cfg.Cost.Budget.MaxTokens != 1000000 {
		t.Fatalf("Cost.Budget.MaxTokens = %d, want 1000000", cfg.Cost.Budget.MaxTokens)
	}

	budget := cfg.VesselBudget()
	if budget == nil {
		t.Fatal("VesselBudget() = nil, want budget")
	}
	if budget.CostLimitUSD != 10.0 {
		t.Fatalf("VesselBudget().CostLimitUSD = %v, want 10.0", budget.CostLimitUSD)
	}
	if budget.TokenLimit != 1000000 {
		t.Fatalf("VesselBudget().TokenLimit = %d, want 1000000", budget.TokenLimit)
	}
}

func TestConfigHarnessDefaultsHelpers(t *testing.T) {
	cfg := validConfig()

	if got := cfg.EffectiveProtectedSurfaces(); !reflect.DeepEqual(got, DefaultProtectedSurfaces) {
		t.Fatalf("EffectiveProtectedSurfaces() = %#v, want %#v", got, DefaultProtectedSurfaces)
	}
	if got := cfg.EffectiveAuditLogPath(); got != DefaultAuditLogPath {
		t.Fatalf("EffectiveAuditLogPath() = %q, want %q", got, DefaultAuditLogPath)
	}
	if !cfg.ObservabilityEnabled() {
		t.Fatal("ObservabilityEnabled() = false, want true")
	}
	if got := cfg.ObservabilitySampleRate(); got != 1.0 {
		t.Fatalf("ObservabilitySampleRate() = %v, want 1.0", got)
	}
	if budget := cfg.VesselBudget(); budget != nil {
		t.Fatalf("VesselBudget() = %#v, want nil", budget)
	}

	policies := cfg.BuildIntermediaryPolicies()
	if len(policies) != 1 {
		t.Fatalf("len(BuildIntermediaryPolicies()) = %d, want 1", len(policies))
	}
	if policies[0].Name != "default" {
		t.Fatalf("BuildIntermediaryPolicies()[0].Name = %q, want %q", policies[0].Name, "default")
	}

	auditLog := intermediary.NewAuditLog(filepath.Join(t.TempDir(), "audit.jsonl"))
	interm := intermediary.NewIntermediary(policies, auditLog, nil)

	deny := interm.Evaluate(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/HARNESS.md",
		AgentID:  "vessel-1",
	})
	if deny.Effect != intermediary.Deny {
		t.Fatalf("default deny effect = %q, want %q", deny.Effect, intermediary.Deny)
	}

	requireApproval := interm.Evaluate(intermediary.Intent{
		Action:   "git_push",
		Resource: "main",
		AgentID:  "vessel-2",
	})
	if requireApproval.Effect != intermediary.RequireApproval {
		t.Fatalf("default require_approval effect = %q, want %q", requireApproval.Effect, intermediary.RequireApproval)
	}

	allow := interm.Evaluate(intermediary.Intent{
		Action:   "phase_execute",
		Resource: "fix",
		AgentID:  "vessel-3",
	})
	if allow.Effect != intermediary.Allow {
		t.Fatalf("default allow effect = %q, want %q", allow.Effect, intermediary.Allow)
	}
}

// TestWS6S26LegacyConfigBackwardsCompat verifies that configs without
// harness, observability, or cost sections still load cleanly and expose safe
// helper defaults.
//
// Covers: WS6 S26.
func TestWS6S26LegacyConfigBackwardsCompat(t *testing.T) {
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
  command: claude
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for legacy config", err)
	}
	if cfg.Concurrency != 2 {
		t.Errorf("Concurrency = %d, want 2", cfg.Concurrency)
	}
	if cfg.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50", cfg.MaxTurns)
	}
	if surfaces := cfg.EffectiveProtectedSurfaces(); !reflect.DeepEqual(surfaces, DefaultProtectedSurfaces) {
		t.Errorf("EffectiveProtectedSurfaces() = %#v, want %#v", surfaces, DefaultProtectedSurfaces)
	}
	if got := cfg.EffectiveAuditLogPath(); got != DefaultAuditLogPath {
		t.Errorf("EffectiveAuditLogPath() = %q, want %q", got, DefaultAuditLogPath)
	}
	if !cfg.ObservabilityEnabled() {
		t.Error("ObservabilityEnabled() = false, want true")
	}
	if got := cfg.ObservabilitySampleRate(); got != 1.0 {
		t.Errorf("ObservabilitySampleRate() = %v, want 1.0", got)
	}
	if budget := cfg.VesselBudget(); budget != nil {
		t.Errorf("VesselBudget() = %#v, want nil", budget)
	}

	policies := cfg.BuildIntermediaryPolicies()
	if len(policies) != 1 {
		t.Fatalf("len(BuildIntermediaryPolicies()) = %d, want 1", len(policies))
	}
	if policies[0].Name != "default" {
		t.Errorf("BuildIntermediaryPolicies()[0].Name = %q, want %q", policies[0].Name, "default")
	}
}

func TestEffectiveProtectedSurfacesNoneDisablesProtection(t *testing.T) {
	cfg := validConfig()
	cfg.Harness.ProtectedSurfaces.Paths = []string{"none"}

	if got := cfg.EffectiveProtectedSurfaces(); got != nil {
		t.Fatalf("EffectiveProtectedSurfaces() = %#v, want nil", got)
	}
}

func TestValidateHarnessProtectedSurfaceGlobInvalid(t *testing.T) {
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
harness:
  protected_surfaces:
    paths:
      - "[invalid-glob"
`)

	_, err := Load(path)
	requireErrorContains(t, err, "harness.protected_surfaces.paths")
	requireErrorContains(t, err, "invalid glob")
	requireErrorContains(t, err, "[invalid-glob")
}

func TestValidateHarnessPolicyEffectInvalid(t *testing.T) {
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
harness:
  policy:
    rules:
      - action: "file_write"
        resource: "*"
        effect: "approve_maybe"
`)

	_, err := Load(path)
	requireErrorContains(t, err, "harness.policy.rules[0]")
	requireErrorContains(t, err, "invalid effect")
	requireErrorContains(t, err, "approve_maybe")
}

func TestValidateObservabilitySampleRateInvalid(t *testing.T) {
	cfg := validConfig()
	cfg.Observability.SampleRate = -0.5

	err := cfg.Validate()
	requireErrorContains(t, err, "observability.sample_rate")
}

func TestValidateCostBudgetNegativeValues(t *testing.T) {
	tests := []struct {
		name   string
		budget *BudgetConfig
		want   string
	}{
		{
			name:   "negative max cost",
			budget: &BudgetConfig{MaxCostUSD: -1.0},
			want:   "cost.budget.max_cost_usd",
		},
		{
			name:   "negative max tokens",
			budget: &BudgetConfig{MaxTokens: -1},
			want:   "cost.budget.max_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Cost.Budget = tt.budget

			err := cfg.Validate()
			requireErrorContains(t, err, tt.want)
		})
	}
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

func TestLoadDefaultModelEmpty(t *testing.T) {
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

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Claude.DefaultModel != "" {
		t.Fatalf("Claude.DefaultModel = %q, want empty", cfg.Claude.DefaultModel)
	}
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
			// Ensure copilot command is set for "copilot" to avoid command validation failure
			if tt.llm == "copilot" {
				cfg.Copilot = CopilotConfig{Command: "copilot"}
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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
		Claude:      ClaudeConfig{Command: "claude"},
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

func TestValidateUnknownSourceType(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude"},
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
