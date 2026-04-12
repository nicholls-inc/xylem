package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/policy"
	"github.com/nicholls-inc/xylem/cli/internal/profiles"
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

func writeWorkflowFile(t *testing.T, configPath, name, yaml string) string {
	t.Helper()

	workflowsDir := filepath.Join(filepath.Dir(configPath), ".xylem", "workflows")
	path := filepath.Join(workflowsDir, name+".yaml")
	require.NoError(t, os.MkdirAll(workflowsDir, 0o755))
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))
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
phase:
  context_budget: 120000
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
	if cfg.Phase.ContextBudget != 120000 {
		t.Fatalf("Phase.ContextBudget = %d, want 120000", cfg.Phase.ContextBudget)
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
	if cfg.Daemon.StallMonitor.PhaseStallThreshold != "30m" {
		t.Fatalf("Daemon.StallMonitor.PhaseStallThreshold = %q, want 30m", cfg.Daemon.StallMonitor.PhaseStallThreshold)
	}
	if cfg.Daemon.StallMonitor.ScannerIdleThreshold != "5m" {
		t.Fatalf("Daemon.StallMonitor.ScannerIdleThreshold = %q, want 5m", cfg.Daemon.StallMonitor.ScannerIdleThreshold)
	}
	if !cfg.Daemon.StallMonitor.OrphanCheckEnabled {
		t.Fatal("Daemon.StallMonitor.OrphanCheckEnabled = false, want true")
	}
	if cfg.Phase.ContextBudget != DefaultPhaseContextBudget {
		t.Fatalf("Phase.ContextBudget = %d, want %d", cfg.Phase.ContextBudget, DefaultPhaseContextBudget)
	}

	// Legacy config should be normalized into Sources
	if len(cfg.Sources) != 1 {
		t.Fatalf("expected 1 source after normalization, got %d", len(cfg.Sources))
	}
	if got := cfg.CostOnExceeded(); got != DefaultCostOnExceeded {
		t.Fatalf("CostOnExceeded() = %q, want %q", got, DefaultCostOnExceeded)
	}
}

func TestLoadStructuredConcurrency(t *testing.T) {
	path := writeConfigFile(t, `repo: owner/name
tasks:
  fix-bugs:
    labels: [bug]
    workflow: fix-bug
concurrency:
  global: 6
  per_class:
    implement-feature: 2
    merge-pr: 3
claude:
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Concurrency != 6 {
		t.Fatalf("Concurrency = %d, want 6", cfg.Concurrency)
	}
	if got, ok := cfg.ConcurrencyLimit("implement-feature"); !ok || got != 2 {
		t.Fatalf("ConcurrencyLimit(implement-feature) = (%d, %t), want (2, true)", got, ok)
	}
	if got, ok := cfg.ConcurrencyLimit("merge-pr"); !ok || got != 3 {
		t.Fatalf("ConcurrencyLimit(merge-pr) = (%d, %t), want (3, true)", got, ok)
	}
}

func TestLoadStructuredConcurrencyRequiresGlobal(t *testing.T) {
	path := writeConfigFile(t, `repo: owner/name
tasks:
  fix-bugs:
    labels: [bug]
    workflow: fix-bug
concurrency:
  per_class:
    implement-feature: 2
claude:
  default_model: "claude-sonnet-4-6"
`)

	_, err := Load(path)
	requireErrorContains(t, err, "concurrency.global is required when concurrency is a map")
}

func TestLoadRejectsNonPositivePhaseContextBudget(t *testing.T) {
	path := writeConfigFile(t, `repo: owner/name
tasks:
  fix-bugs:
    labels: [bug]
    workflow: fix-bug
claude:
  default_model: "claude-sonnet-4-6"
phase:
  context_budget: 0
`)

	_, err := Load(path)
	requireErrorContains(t, err, "phase.context_budget must be greater than 0")
}

func TestValidateRejectsNegativePhaseContextBudget(t *testing.T) {
	cfg := validConfig()
	cfg.Phase.ContextBudget = -1

	requireErrorContains(t, cfg.Validate(), "phase.context_budget must be greater than 0")
}

func TestResolveStateDir(t *testing.T) {
	t.Run("rebases relative state dir under root", func(t *testing.T) {
		root := filepath.Join("var", "tmp", "daemon-root")
		got := ResolveStateDir(root, ".xylem")
		want := filepath.Join(root, ".xylem")
		if got != want {
			t.Fatalf("ResolveStateDir() = %q, want %q", got, want)
		}
	})

	t.Run("preserves absolute state dir", func(t *testing.T) {
		abs := filepath.Join(string(filepath.Separator), "tmp", "xylem-state")
		if got := ResolveStateDir("daemon-root", abs); got != abs {
			t.Fatalf("ResolveStateDir() = %q, want %q", got, abs)
		}
	})

	t.Run("trims root and state dir before rebasing", func(t *testing.T) {
		got := ResolveStateDir(" daemon-root ", " .xylem ")
		want := filepath.Join("daemon-root", ".xylem")
		if got != want {
			t.Fatalf("ResolveStateDir() = %q, want %q", got, want)
		}
	})

	t.Run("returns trimmed state dir when root is empty", func(t *testing.T) {
		if got := ResolveStateDir(" ", " .xylem "); got != ".xylem" {
			t.Fatalf("ResolveStateDir() = %q, want %q", got, ".xylem")
		}
	})

	t.Run("returns empty when state dir is empty", func(t *testing.T) {
		if got := ResolveStateDir("daemon-root", " "); got != "" {
			t.Fatalf("ResolveStateDir() = %q, want empty string", got)
		}
	})
}

func TestSmoke_S2_RuntimePathPrefersProfileReadyStateWhenPresent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("state/\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "state"), 0o755))

	got := RuntimePath(root, "queue.jsonl")
	want := filepath.Join(root, "state", "queue.jsonl")
	assert.Equal(t, want, got)
	assert.Equal(t, filepath.Join(root, "state"), RuntimeRoot(root))
}

func TestSmoke_S3_RuntimePathFallsBackToLegacyFlatArtifact(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("state/\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "queue.jsonl"), []byte("{}\n"), 0o644))

	got := RuntimePath(root, "queue.jsonl")
	want := filepath.Join(root, "queue.jsonl")
	assert.Equal(t, want, got)
	assert.Equal(t, root, RuntimeRoot(root))
}

func TestRuntimePathDoesNotDoubleNestExplicitStatePaths(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("state/\n"), 0o644))

	got := RuntimePath(root, "state", "bootstrap", "marker.json")
	want := filepath.Join(root, "state", "bootstrap", "marker.json")
	assert.Equal(t, want, got)
}

func TestRuntimePathPreservesNonControlPlaneRoots(t *testing.T) {
	root := t.TempDir()

	got := RuntimePath(root, "phases", "issue-1", "summary.json")
	want := filepath.Join(root, "phases", "issue-1", "summary.json")
	assert.Equal(t, want, got)
	assert.Equal(t, root, RuntimeRoot(root))
}

func TestMigrateFlatStateToRuntimeMovesLegacyFiles(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	legacyQueuePath := filepath.Join(stateDir, "queue.jsonl")
	legacyAuditPath := filepath.Join(stateDir, DefaultAuditLogPath)
	legacyPIDPath := filepath.Join(stateDir, "daemon.pid")

	queueLine := `{"id":"issue-388","source":"manual","state":"pending","created_at":"2026-04-12T01:00:00Z"}`
	require.NoError(t, os.WriteFile(legacyQueuePath, []byte(queueLine+"\n"), 0o644))
	require.NoError(t, os.WriteFile(legacyQueuePath+".lock", []byte("legacy queue lock"), 0o644))

	legacyAudit := intermediary.NewAuditLog(legacyAuditPath)
	require.NoError(t, legacyAudit.Append(intermediary.AuditEntry{
		Intent:    intermediary.Intent{Action: "phase_execute", Resource: "implement", AgentID: "issue-388"},
		Decision:  intermediary.Allow,
		Timestamp: time.Date(2026, time.April, 12, 1, 1, 0, 0, time.UTC),
	}))
	require.NoError(t, os.WriteFile(legacyPIDPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644))

	require.NoError(t, MigrateFlatStateToRuntime(stateDir))

	runtimeQueuePath := filepath.Join(stateDir, "state", "queue.jsonl")
	runtimeAuditPath := filepath.Join(stateDir, "state", DefaultAuditLogPath)
	runtimePIDPath := filepath.Join(stateDir, "state", "daemon.pid")

	assert.NoFileExists(t, legacyQueuePath)
	assert.NoFileExists(t, legacyAuditPath)
	assert.NoFileExists(t, legacyPIDPath)
	assert.NoFileExists(t, legacyQueuePath+".lock")
	assert.NoFileExists(t, legacyAuditPath+".lock")
	assert.FileExists(t, legacyQueuePath+".migrated")
	assert.FileExists(t, legacyAuditPath+".migrated")
	assert.FileExists(t, legacyPIDPath+".migrated")

	assert.FileExists(t, runtimeQueuePath)
	assert.FileExists(t, runtimeAuditPath)
	assert.NoFileExists(t, runtimePIDPath)
	assert.FileExists(t, runtimeQueuePath+".lock")
	assert.FileExists(t, runtimeAuditPath+".lock")
	assert.Equal(t, runtimeQueuePath, RuntimePath(stateDir, "queue.jsonl"))
	assert.Equal(t, runtimeAuditPath, RuntimePath(stateDir, DefaultAuditLogPath))
	assert.Equal(t, runtimePIDPath, RuntimePath(stateDir, "daemon.pid"))

	queueData, err := os.ReadFile(RuntimePath(stateDir, "queue.jsonl"))
	require.NoError(t, err)
	var vessel struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(queueData))), &vessel))
	assert.Equal(t, "issue-388", vessel.ID)

	runtimeAudit := intermediary.NewAuditLog(RuntimePath(stateDir, DefaultAuditLogPath))
	entries, err := runtimeAudit.Entries()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, intermediary.Allow, entries[0].Decision)

	_, err = os.Stat(runtimePIDPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestMigrateFlatStateToRuntimeRejectsSplitBrainQueue(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "state"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "queue.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "state", "queue.jsonl"), []byte("{}\n"), 0o644))

	err := MigrateFlatStateToRuntime(stateDir)
	require.Error(t, err)
	require.ErrorContains(t, err, "both legacy and runtime queue.jsonl exist")
}

func TestMigrateFlatStateToRuntimeRejectsSplitBrainAuditLog(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "state"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, DefaultAuditLogPath), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "state", DefaultAuditLogPath), []byte("{}\n"), 0o644))

	err := MigrateFlatStateToRuntime(stateDir)
	require.Error(t, err)
	require.ErrorContains(t, err, "both legacy and runtime audit.jsonl exist")
}

func TestMigrateFlatStateToRuntimeRejectsSplitBrainDaemonPID(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "state"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "daemon.pid"), []byte("0"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "state", "daemon.pid"), []byte("0"), 0o644))

	err := MigrateFlatStateToRuntime(stateDir)
	require.Error(t, err)
	require.ErrorContains(t, err, "both legacy and runtime daemon.pid exist")
}

func validConfig() *Config {
	cfg := &Config{
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
		Copilot: CopilotConfig{
			Command:      "copilot",
			DefaultModel: "gpt-5.4",
		},
	}
	cfg.normalize()
	return cfg
}

func validationRequiredSourceConfig(workflow string) map[string]SourceConfig {
	return map[string]SourceConfig{
		"github": {
			Type: "github",
			Repo: "owner/name",
			Tasks: map[string]Task{
				"validation": {Labels: []string{"ci"}, Workflow: workflow},
			},
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

func TestValidateScheduleSource(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"lessons": {
			Type:     "schedule",
			Cadence:  "@daily",
			Workflow: "lessons",
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsInvalidPhaseStallThreshold(t *testing.T) {
	cfg := validConfig()
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "not-a-duration"
	requireErrorContains(t, cfg.Validate(), "daemon.stall_monitor.phase_stall_threshold")
}

func TestValidateScheduleSourceRejectsMalformedCadence(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"lessons": {
			Type:     "schedule",
			Cadence:  "not-a-cadence",
			Workflow: "lessons",
		},
	}

	requireErrorContains(t, cfg.Validate(), `source "lessons" (schedule): cadence is invalid`)
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

func TestValidateHarnessToolPermissionsRejectsUnknownTool(t *testing.T) {
	cfg := validConfig()
	cfg.Harness.ToolPermissions.Roles = map[string]ToolRoleConfig{
		"diagnostic": {
			AllowedTools: []string{"Read", "NotARealTool"},
		},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, `role "diagnostic"`)
	requireErrorContains(t, err, `tool "NotARealTool" not found`)
}

func TestValidateHarnessToolPermissionsRejectsCustomRoleWithoutScope(t *testing.T) {
	cfg := validConfig()
	cfg.Harness.ToolPermissions.Roles = map[string]ToolRoleConfig{
		"custom": {
			AllowedTools: []string{"Read"},
		},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, `role "custom": max_scope is required for custom roles`)
}

func TestBuildPhaseToolCatalogAppliesOverrides(t *testing.T) {
	cfg := validConfig()
	cfg.Harness.ToolPermissions.PhaseRoles = map[string]string{
		"implement": "diagnostic",
	}
	cfg.Harness.ToolPermissions.Roles = map[string]ToolRoleConfig{
		"diagnostic": {
			AllowedTools: []string{"Read", "Edit"},
			MaxScope:     "full_autonomy",
		},
	}

	role := cfg.PhaseToolRole(policy.Delivery, "implement", "prompt")
	require.Equal(t, "diagnostic", role)

	toolCatalog, err := cfg.BuildPhaseToolCatalog()
	require.NoError(t, err)

	allowed, err := toolCatalog.ResolveRoleTools(role, []string{"Read", "Edit"})
	require.NoError(t, err)
	require.Equal(t, []string{"Read", "Edit"}, allowed)
}

func TestSmoke_S2_ConfigToolOverridesTakePrecedenceOverWorkflowClass(t *testing.T) {
	cfg := validConfig()
	cfg.Harness.ToolPermissions.PhaseRoles = map[string]string{
		"implement": "diagnostic",
	}
	cfg.Harness.ToolPermissions.Roles = map[string]ToolRoleConfig{
		"diagnostic": {
			AllowedTools: []string{"Read", "Edit"},
			MaxScope:     "full_autonomy",
		},
	}

	role := cfg.PhaseToolRole(policy.Ops, "implement", "prompt")
	require.Equal(t, "diagnostic", role)

	toolCatalog, err := cfg.BuildPhaseToolCatalog()
	require.NoError(t, err)

	allowed, err := toolCatalog.ResolveRoleTools(role, []string{"Read", "Edit"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Read", "Edit"}, allowed)

	_, err = toolCatalog.ResolveRoleTools("housekeeping", []string{"Read", "Edit"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `role "housekeeping" is not allowed to use tool "Edit"`)
}

func TestPhaseToolRoleDefaults(t *testing.T) {
	cfg := validConfig()

	assert.Equal(t, "delivery", cfg.PhaseToolRole(policy.Delivery, "analyze", "prompt"))
	assert.Equal(t, "housekeeping", cfg.PhaseToolRole(policy.HarnessMaintenance, "analyze", "prompt"))
	assert.Equal(t, "housekeeping", cfg.PhaseToolRole(policy.Ops, "merge_pr", "prompt"))
	assert.Equal(t, "diagnostic", cfg.PhaseToolRole("", "analyze", "prompt"))
	assert.Equal(t, "delivery", cfg.PhaseToolRole("", "implement", "prompt"))
	assert.Equal(t, "housekeeping", cfg.PhaseToolRole("", "review", "prompt"))
	assert.Equal(t, "housekeeping", cfg.PhaseToolRole("", "pr_draft", "prompt"))
	assert.Equal(t, "housekeeping", cfg.PhaseToolRole("", "smoke", "command"))
}

func TestValidateCostBudgetNegativeMaxTokens(t *testing.T) {
	cfg := validConfig()
	cfg.Cost.Budget = &BudgetConfig{MaxTokens: -1}

	err := cfg.Validate()
	requireErrorContains(t, err, "cost.budget.max_tokens")
}

func TestValidateCostDailyBudgetNegative(t *testing.T) {
	cfg := validConfig()
	cfg.Cost.DailyBudgetUSD = -1

	err := cfg.Validate()
	requireErrorContains(t, err, "cost.daily_budget_usd")
}

func TestValidateCostPerClassLimitNegative(t *testing.T) {
	cfg := validConfig()
	cfg.Cost.PerClassLimit = map[string]float64{"delivery": -1}

	err := cfg.Validate()
	requireErrorContains(t, err, `cost.per_class_limit["delivery"]`)
}

func TestValidateCostPerClassLimitRejectsBlankClassName(t *testing.T) {
	cfg := validConfig()
	cfg.Cost.PerClassLimit = map[string]float64{"   ": 1}

	err := cfg.Validate()
	requireErrorContains(t, err, "cost.per_class_limit keys must be non-empty")
}

func TestValidateCostPerClassLimitOversubscribed(t *testing.T) {
	cfg := validConfig()
	cfg.Cost.DailyBudgetUSD = 10
	cfg.Cost.PerClassLimit = map[string]float64{
		"delivery":            8,
		"harness-maintenance": 3,
	}

	err := cfg.Validate()
	requireErrorContains(t, err, "cost.per_class_limit total")
}

func TestValidateCostPerClassLimitAllowsUnlimitedDailyBudget(t *testing.T) {
	cfg := validConfig()
	cfg.Cost.DailyBudgetUSD = 0
	cfg.Cost.PerClassLimit = map[string]float64{
		"delivery":            40,
		"harness-maintenance": 5,
	}

	require.NoError(t, cfg.Validate())
}

func TestValidateCostOnExceededRejectsUnknownValue(t *testing.T) {
	cfg := validConfig()
	cfg.Cost.OnExceeded = "stop"

	err := cfg.Validate()
	requireErrorContains(t, err, "cost.on_exceeded")
}

func TestCostOnExceededDefaultsAndNormalizes(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "empty defaults", mode: "", want: DefaultCostOnExceeded},
		{name: "whitespace defaults", mode: "   ", want: DefaultCostOnExceeded},
		{name: "drain only trims", mode: " drain_only ", want: DefaultCostOnExceeded},
		{name: "pause normalizes case", mode: " Pause ", want: "pause"},
		{name: "alert normalizes case", mode: "ALERT", want: "alert"},
		{name: "unknown falls back", mode: "stop", want: DefaultCostOnExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Cost: CostConfig{OnExceeded: tt.mode}}
			assert.Equal(t, tt.want, cfg.CostOnExceeded())
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
	if got := cfg.Providers["claude"].Tiers[DefaultLLMRoutingTier]; got != "claude-sonnet-4-20250514" {
		t.Fatalf("Providers[claude].Tiers[%q] = %q, want claude-sonnet-4-20250514", DefaultLLMRoutingTier, got)
	}
}

func TestValidateLegacyClaudeDefaultModelRequired(t *testing.T) {
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
	requireErrorContains(t, err, "providers.claude.tiers.med must be set")
}

func TestLoadLegacyProvidersNormalizesIntoRouting(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
        tier: high
concurrency: 2
max_turns: 50
timeout: "30m"
llm: copilot
model: gpt-5.4
claude:
  command: "claude"
  flags: "--dangerously-skip-permissions"
  default_model: "claude-sonnet-4-6"
  env:
    ANTHROPIC_API_KEY: "test-key"
copilot:
  command: "copilot"
  flags: "--yolo --autopilot"
  default_model: "gpt-5.4"
  env:
    GITHUB_TOKEN: "gh-token"
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, "high", cfg.Sources["github"].Tasks["fix-bugs"].Tier)
	require.Equal(t, DefaultLLMRoutingTier, cfg.LLMRouting.DefaultTier)
	require.Equal(t, []string{"copilot", "claude"}, cfg.LLMRouting.Tiers[DefaultLLMRoutingTier].Providers)
	require.Equal(t, ProviderConfig{
		Kind:    "claude",
		Command: "claude",
		Flags:   "--dangerously-skip-permissions",
		Tiers: map[string]string{
			DefaultLLMRoutingTier: "claude-sonnet-4-6",
		},
		Env: map[string]string{
			"ANTHROPIC_API_KEY": "test-key",
		},
	}, cfg.Providers["claude"])
	require.Equal(t, ProviderConfig{
		Kind:    "copilot",
		Command: "copilot",
		Flags:   "--yolo --autopilot",
		Tiers: map[string]string{
			DefaultLLMRoutingTier: "gpt-5.4",
		},
		Env: map[string]string{
			"GITHUB_TOKEN": "gh-token",
		},
	}, cfg.Providers["copilot"])
}

func TestLoadNewProvidersAndRoutingShape(t *testing.T) {
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
providers:
  claude:
    kind: claude
    command: "claude"
    flags: "--dangerously-skip-permissions"
    tiers:
      high: "claude-opus-4-6"
      med: "claude-sonnet-4-6"
      low: "claude-haiku-4-5"
    env:
      ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
  copilot:
    kind: copilot
    command: "copilot"
    flags: "--yolo --autopilot"
    tiers:
      high: "gpt-5.4"
      med: "gpt-5.2-codex"
      low: "gpt-5-mini"
    env:
      GITHUB_TOKEN: "${COPILOT_GITHUB_TOKEN}"
llm_routing:
  default_tier: med
  tiers:
    high:
      providers: [claude]
    med:
      providers: [claude, copilot]
    low:
      providers: [copilot, claude]
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, "med", cfg.LLMRouting.DefaultTier)
	require.Equal(t, []string{"claude"}, cfg.LLMRouting.Tiers["high"].Providers)
	require.Equal(t, []string{"claude", "copilot"}, cfg.LLMRouting.Tiers["med"].Providers)
	require.Equal(t, []string{"copilot", "claude"}, cfg.LLMRouting.Tiers["low"].Providers)
	require.Equal(t, "claude-opus-4-6", cfg.Providers["claude"].Tiers["high"])
	require.Equal(t, "gpt-5-mini", cfg.Providers["copilot"].Tiers["low"])
}

func TestValidateRoutingRejectsUnknownProvider(t *testing.T) {
	cfg := validConfig()
	cfg.Providers = map[string]ProviderConfig{
		"claude": {
			Kind:    "claude",
			Command: "claude",
			Tiers: map[string]string{
				"med": "claude-sonnet-4-6",
			},
		},
	}
	cfg.LLMRouting = LLMRoutingConfig{
		DefaultTier: "med",
		Tiers: map[string]TierRouting{
			"med": {Providers: []string{"claude", "copilot"}},
		},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, `llm_routing.tiers.med.providers references unknown provider "copilot"`)
}

func TestValidateRoutingRejectsMissingTierModel(t *testing.T) {
	cfg := validConfig()
	cfg.Providers = map[string]ProviderConfig{
		"claude": {
			Kind:    "claude",
			Command: "claude",
			Tiers: map[string]string{
				"med": "claude-sonnet-4-6",
			},
		},
	}
	cfg.LLMRouting = LLMRoutingConfig{
		DefaultTier: "med",
		Tiers: map[string]TierRouting{
			"med":  {Providers: []string{"claude"}},
			"high": {Providers: []string{"claude"}},
		},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, "providers.claude.tiers.high must be set")
}

func TestValidateRoutingRejectsUnknownKind(t *testing.T) {
	cfg := validConfig()
	cfg.Providers = map[string]ProviderConfig{
		"gemini": {
			Kind:    "gemini",
			Command: "gemini",
			Tiers: map[string]string{
				"med": "gemini-pro",
			},
		},
	}
	cfg.LLMRouting = LLMRoutingConfig{
		DefaultTier: "med",
		Tiers: map[string]TierRouting{
			"med": {Providers: []string{"gemini"}},
		},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, `providers.gemini.kind must be one of claude or copilot`)
}

func TestValidateRoutingRejectsUnknownDefaultTier(t *testing.T) {
	cfg := validConfig()
	cfg.Providers = map[string]ProviderConfig{
		"claude": {
			Kind:    "claude",
			Command: "claude",
			Tiers: map[string]string{
				"med": "claude-sonnet-4-6",
			},
		},
	}
	cfg.LLMRouting = LLMRoutingConfig{
		DefaultTier: "high",
		Tiers: map[string]TierRouting{
			"med": {Providers: []string{"claude"}},
		},
	}

	err := cfg.Validate()
	requireErrorContains(t, err, `llm_routing.default_tier "high" must exist in llm_routing.tiers`)
}

func TestLoadProvidersWithoutRoutingSynthesizesDefaultChain(t *testing.T) {
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
providers:
  claude:
    kind: claude
    command: "claude"
    tiers:
      med: "claude-sonnet-4-6"
  copilot:
    kind: copilot
    command: "copilot"
    tiers:
      med: "gpt-5.4"
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, DefaultLLMRoutingTier, cfg.LLMRouting.DefaultTier)
	require.Equal(t, []string{"copilot", "claude"}, cfg.LLMRouting.Tiers[DefaultLLMRoutingTier].Providers)
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

func TestLoadLabelGateLabels(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
        label_gate_labels:
          waiting: "blocked"
          ready: "ready-for-implementation"
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
	if task.LabelGateLabels == nil {
		t.Fatal("LabelGateLabels should not be nil when label_gate_labels block is present")
	}
	if task.LabelGateLabels.Waiting != "blocked" {
		t.Errorf("LabelGateLabels.Waiting = %q, want blocked", task.LabelGateLabels.Waiting)
	}
	if task.LabelGateLabels.Ready != "ready-for-implementation" {
		t.Errorf("LabelGateLabels.Ready = %q, want ready-for-implementation", task.LabelGateLabels.Ready)
	}
}

func TestLoadLabelGateLabelsOmitted(t *testing.T) {
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
	if task.LabelGateLabels != nil {
		t.Errorf("expected LabelGateLabels to be nil when label_gate_labels block is omitted, got %+v", task.LabelGateLabels)
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
							PROpened:        true,
							PRHeadUpdated:   true,
							Debounce:        "10m",
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

func TestValidateGitHubPREventsInvalidDebounce(t *testing.T) {
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
							PROpened: true,
							Debounce: "nope",
						},
					},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "parse debounce")
}

func TestValidateGitHubPREventsRejectsNegativeDebounce(t *testing.T) {
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
							PROpened: true,
							Debounce: "-1s",
						},
					},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "debounce must be non-negative")
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

func TestValidateGitHubPREventsPROpenedNoAuthorFilter(t *testing.T) {
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
						Workflow: "review-pr",
						On:       &PREventsConfig{PROpened: true},
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

func TestValidateScheduledSourceValid(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"audit": {
				Type:     "scheduled",
				Repo:     "owner/name",
				Schedule: "24h",
				Tasks: map[string]Task{
					"context": {Workflow: "context-weight-audit"},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid scheduled config, got: %v", err)
	}
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

func TestValidateScheduledSourceMissingSchedule(t *testing.T) {
	cfg := &Config{
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		Claude:      ClaudeConfig{Command: "claude", DefaultModel: "claude-sonnet-4-6"},
		Sources: map[string]SourceConfig{
			"audit": {
				Type: "scheduled",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"context": {Workflow: "context-weight-audit"},
				},
			},
		},
	}
	err := cfg.Validate()
	requireErrorContains(t, err, "schedule is required")
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
	inter := intermediary.NewIntermediary(cfg.BuildIntermediaryPolicies(), auditLog, nil)
	inter.SetMode(cfg.HarnessPolicyMode())
	return inter
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
    mode: "warn"
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
	assert.Equal(t, intermediary.PolicyModeWarn, cfg.HarnessPolicyMode())
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
	assert.Zero(t, cfg.Cost.DailyBudgetUSD)
	assert.Nil(t, cfg.Cost.PerClassLimit)
	assert.Equal(t, DefaultCostOnExceeded, cfg.CostOnExceeded())
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

func TestDefaultProtectedSurfacesComment(t *testing.T) {
	// Read config.go source to assert the comment stays up-to-date.
	src, err := os.ReadFile("config.go")
	require.NoError(t, err)
	content := string(src)
	assert.Contains(t, content, "#366", "DefaultProtectedSurfaces comment must reference PR #366")
	assert.Contains(t, content, "class matrix", "DefaultProtectedSurfaces comment must reference the class matrix")
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

func TestSmoke_S5b_InvalidPolicyModeRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `harness:
  policy:
    mode: "observe"
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness.policy.mode")
	assert.Contains(t, err.Error(), "invalid policy mode")
}

func TestSmoke_S6_DefaultPolicyDeniesHarnessWrite(t *testing.T) {
	result := newSmokeIntermediary(t, validConfig()).EvaluateWithContext(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/HARNESS.md",
		AgentID:  "vessel-001",
	}, intermediary.EvaluationContext{WorkflowClass: policy.Delivery, VesselID: "vessel-001"})

	assert.Equal(t, intermediary.Deny, result.Effect)
	assert.Equal(t, "write_control_plane", result.Operation)
	assert.Equal(t, "delivery.no_control_plane_writes", result.RuleMatched)
	assert.Equal(t, policy.Delivery, result.WorkflowClass)
}

func TestSmoke_S7_DefaultPolicyAllowsGitPush(t *testing.T) {
	// Autonomous self-healing requires git_push to succeed without manual
	// approval. Operators who want approval gates can override via
	// harness.policy in .xylem.yml.
	result := newSmokeIntermediary(t, validConfig()).EvaluateWithContext(intermediary.Intent{
		Action:   "git_push",
		Resource: "main",
		AgentID:  "vessel-002",
	}, intermediary.EvaluationContext{WorkflowClass: policy.Delivery, VesselID: "vessel-002"})

	assert.Equal(t, intermediary.Allow, result.Effect)
	assert.Equal(t, "push_branch", result.Operation)
	assert.Equal(t, "delivery.feature_branch_push_allowed", result.RuleMatched)
}

func TestDefaultPolicyAllowsClassifiedGitLifecycleActions(t *testing.T) {
	tests := []struct {
		name            string
		action          string
		resource        string
		wantOperation   string
		wantRuleMatched string
	}{
		{
			name:     "git commit",
			action:   "git_commit",
			resource: "*",
		},
		{
			name:            "git push",
			action:          "git_push",
			resource:        "main",
			wantOperation:   "push_branch",
			wantRuleMatched: "delivery.feature_branch_push_allowed",
		},
		{
			name:            "pull request create",
			action:          "pr_create",
			resource:        "owner/name",
			wantOperation:   "create_pr",
			wantRuleMatched: "delivery.pr_creation_allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := newSmokeIntermediary(t, validConfig()).EvaluateWithContext(intermediary.Intent{
				Action:   tt.action,
				Resource: tt.resource,
				AgentID:  "vessel-009",
			}, intermediary.EvaluationContext{WorkflowClass: policy.Delivery, VesselID: "vessel-009"})

			assert.Equal(t, intermediary.Allow, result.Effect)
			assert.Equal(t, policy.Delivery, result.WorkflowClass)
			if tt.action == "git_commit" {
				assert.Empty(t, result.Operation)
				assert.NotNil(t, result.MatchedRule)
				assert.Equal(t, "*", result.MatchedRule.Action)
				assert.Equal(t, "*", result.MatchedRule.Resource)
				return
			}
			assert.Equal(t, tt.wantOperation, result.Operation)
			assert.Equal(t, tt.wantRuleMatched, result.RuleMatched)
		})
	}
}

func TestDefaultPolicyDeniesPromptFileWrite(t *testing.T) {
	result := newSmokeIntermediary(t, validConfig()).EvaluateWithContext(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/prompts/fix-bug/analyze.md",
		AgentID:  "vessel-004",
	}, intermediary.EvaluationContext{WorkflowClass: policy.Delivery, VesselID: "vessel-004"})

	assert.Equal(t, intermediary.Deny, result.Effect)
	assert.Equal(t, "write_control_plane", result.Operation)
	assert.Equal(t, "delivery.no_control_plane_writes", result.RuleMatched)
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

func TestSmoke_S30_CostConfigLoadsBudgetAndDailyBudgetPolicy(t *testing.T) {
	path := writeSmokeConfigFile(t, `cost:
  budget:
    max_cost_usd: 5.0
    max_tokens: 500000
  daily_budget_usd: 50
  per_class_limit:
    " delivery ": 40
    " harness-maintenance ": 5
    ops: 5
  on_exceeded: " Pause "
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	budget := cfg.VesselBudget()
	require.NotNil(t, budget)
	assert.Equal(t, 5.0, budget.CostLimitUSD)
	assert.Equal(t, 500000, budget.TokenLimit)
	assert.Equal(t, 50.0, cfg.Cost.DailyBudgetUSD)
	assert.Equal(t, map[string]float64{
		"delivery":            40,
		"harness-maintenance": 5,
		"ops":                 5,
	}, cfg.Cost.PerClassLimit)
	assert.Equal(t, "pause", cfg.CostOnExceeded())
}

func TestLoadDailyBudgetOversubscriptionRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `cost:
  daily_budget_usd: 10
  per_class_limit:
    delivery: 8
    ops: 4
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cost.per_class_limit total")
}

func TestLoadDailyBudgetUnknownOnExceededRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `cost:
  daily_budget_usd: 50
  on_exceeded: stop
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cost.on_exceeded")
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

func TestSmoke_S36b_ScheduledSourceAllowsMonthlyAlias(t *testing.T) {
	cfg := validConfig()
	cfg.Sources = map[string]SourceConfig{
		"hardening-audit": {
			Type:     "scheduled",
			Repo:     "owner/repo",
			Schedule: "@monthly",
			Tasks: map[string]Task{
				"monthly": {Workflow: "hardening-audit", Ref: "hardening-audit"},
			},
		},
	}

	err := cfg.Validate()
	require.NoError(t, err)
	assert.Equal(t, "@monthly", cfg.Sources["hardening-audit"].Schedule)
}

func TestSmoke_S37_ScheduledSourceAllowsMissingRef(t *testing.T) {
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
	require.NoError(t, err)
	assert.Empty(t, cfg.Sources["sota-gap"].Tasks["weekly"].Ref)
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

func TestSmoke_S39_LegacyProvidersNormalizeWithPreferredProviderFirst(t *testing.T) {
	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
        tier: high
concurrency: 2
max_turns: 50
timeout: "30m"
llm: copilot
model: "gpt-5.4"
claude:
  command: "claude"
  flags: "--dangerously-skip-permissions"
  default_model: "claude-sonnet-4-6"
  env:
    ANTHROPIC_API_KEY: "test-key"
copilot:
  command: "copilot"
  flags: "--yolo --autopilot"
  default_model: "gpt-5.4"
  env:
    GITHUB_TOKEN: "gh-token"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Contains(t, cfg.Sources, "github")
	require.Contains(t, cfg.Sources["github"].Tasks, "fix-bugs")
	require.Contains(t, cfg.Providers, "claude")
	require.Contains(t, cfg.Providers, "copilot")
	require.Contains(t, cfg.LLMRouting.Tiers, DefaultLLMRoutingTier)

	assert.Equal(t, "high", cfg.Sources["github"].Tasks["fix-bugs"].Tier)
	assert.Equal(t, DefaultLLMRoutingTier, cfg.LLMRouting.DefaultTier)
	assert.Equal(t, []string{"copilot", "claude"}, cfg.LLMRouting.Tiers[DefaultLLMRoutingTier].Providers)
	assert.Equal(t, ProviderConfig{
		Kind:    "claude",
		Command: "claude",
		Flags:   "--dangerously-skip-permissions",
		Tiers: map[string]string{
			DefaultLLMRoutingTier: "claude-sonnet-4-6",
		},
		Env: map[string]string{
			"ANTHROPIC_API_KEY": "test-key",
		},
	}, cfg.Providers["claude"])
	assert.Equal(t, ProviderConfig{
		Kind:    "copilot",
		Command: "copilot",
		Flags:   "--yolo --autopilot",
		Tiers: map[string]string{
			DefaultLLMRoutingTier: "gpt-5.4",
		},
		Env: map[string]string{
			"GITHUB_TOKEN": "gh-token",
		},
	}, cfg.Providers["copilot"])
}

func TestSmoke_S40_NewProvidersAndRoutingShapeLoadsCleanly(t *testing.T) {
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
providers:
  claude:
    kind: claude
    command: "claude"
    flags: "--dangerously-skip-permissions"
    tiers:
      high: "claude-opus-4-6"
      med: "claude-sonnet-4-6"
      low: "claude-haiku-4-5"
    env:
      ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
  copilot:
    kind: copilot
    command: "copilot"
    flags: "--yolo --autopilot"
    tiers:
      high: "gpt-5.4"
      med: "gpt-5.2-codex"
      low: "gpt-5-mini"
    env:
      GITHUB_TOKEN: "${COPILOT_GITHUB_TOKEN}"
llm_routing:
  default_tier: med
  tiers:
    high:
      providers: [claude]
    med:
      providers: [claude, copilot]
    low:
      providers: [copilot, claude]
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Contains(t, cfg.Providers, "claude")
	require.Contains(t, cfg.Providers, "copilot")
	require.Contains(t, cfg.LLMRouting.Tiers, "high")
	require.Contains(t, cfg.LLMRouting.Tiers, "med")
	require.Contains(t, cfg.LLMRouting.Tiers, "low")

	assert.Equal(t, "med", cfg.LLMRouting.DefaultTier)
	assert.Equal(t, []string{"claude"}, cfg.LLMRouting.Tiers["high"].Providers)
	assert.Equal(t, []string{"claude", "copilot"}, cfg.LLMRouting.Tiers["med"].Providers)
	assert.Equal(t, []string{"copilot", "claude"}, cfg.LLMRouting.Tiers["low"].Providers)
	assert.Equal(t, "claude", cfg.Providers["claude"].Kind)
	assert.Equal(t, "claude-opus-4-6", cfg.Providers["claude"].Tiers["high"])
	assert.Equal(t, "claude-haiku-4-5", cfg.Providers["claude"].Tiers["low"])
	assert.Equal(t, "copilot", cfg.Providers["copilot"].Kind)
	assert.Equal(t, "gpt-5.2-codex", cfg.Providers["copilot"].Tiers["med"])
	assert.Equal(t, "gpt-5-mini", cfg.Providers["copilot"].Tiers["low"])
}

func TestSmoke_S41_RoutingValidationRejectsInvalidConfigurations(t *testing.T) {
	const prefix = `sources:
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
`

	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "unknown provider in tier chain",
			yaml: prefix + `providers:
  claude:
    kind: claude
    command: "claude"
    tiers:
      med: "claude-sonnet-4-6"
llm_routing:
  default_tier: med
  tiers:
    med:
      providers: [claude, copilot]
`,
			want: []string{
				`llm_routing.tiers.med.providers references unknown provider "copilot"`,
			},
		},
		{
			name: "missing tier model on provider",
			yaml: prefix + `providers:
  claude:
    kind: claude
    command: "claude"
    tiers:
      med: "claude-sonnet-4-6"
llm_routing:
  default_tier: med
  tiers:
    med:
      providers: [claude]
    high:
      providers: [claude]
`,
			want: []string{
				"providers.claude.tiers.high must be set",
			},
		},
		{
			name: "unknown provider kind",
			yaml: prefix + `providers:
  gemini:
    kind: gemini
    command: "gemini"
    tiers:
      med: "gemini-pro"
llm_routing:
  default_tier: med
  tiers:
    med:
      providers: [gemini]
`,
			want: []string{
				`providers.gemini.kind must be one of claude or copilot`,
			},
		},
		{
			name: "bad default tier",
			yaml: prefix + `providers:
  claude:
    kind: claude
    command: "claude"
    tiers:
      med: "claude-sonnet-4-6"
llm_routing:
  default_tier: high
  tiers:
    med:
      providers: [claude]
`,
			want: []string{
				`llm_routing.default_tier "high" must exist in llm_routing.tiers`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfigFile(t, tt.yaml))
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestSmoke_S42_ProvidersWithoutRoutingSynthesizeDefaultChain(t *testing.T) {
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
providers:
  claude:
    kind: claude
    command: "claude"
    tiers:
      med: "claude-sonnet-4-6"
  copilot:
    kind: copilot
    command: "copilot"
    tiers:
      med: "gpt-5.4"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Contains(t, cfg.LLMRouting.Tiers, DefaultLLMRoutingTier)

	assert.Equal(t, DefaultLLMRoutingTier, cfg.LLMRouting.DefaultTier)
	assert.Equal(t, []string{"copilot", "claude"}, cfg.LLMRouting.Tiers[DefaultLLMRoutingTier].Providers)
	assert.Equal(t, "claude-sonnet-4-6", cfg.Providers["claude"].Tiers[DefaultLLMRoutingTier])
	assert.Equal(t, "gpt-5.4", cfg.Providers["copilot"].Tiers[DefaultLLMRoutingTier])
}

func TestSmoke_S43_GitHubPREventsLoadsPROpenedPRHeadUpdatedAndDebounce(t *testing.T) {
	path := writeConfigFile(t, `sources:
  pr-lifecycle:
    type: github-pr-events
    repo: owner/name
    tasks:
      review:
        workflow: review-pr
        on:
          pr_opened: true
          pr_head_updated: true
          debounce: 10m
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	src, ok := cfg.Sources["pr-lifecycle"]
	require.True(t, ok)

	task, ok := src.Tasks["review"]
	require.True(t, ok)
	require.NotNil(t, task.On)
	assert.True(t, task.On.PROpened)
	assert.True(t, task.On.PRHeadUpdated)
	assert.Equal(t, "10m", task.On.Debounce)
	assert.Equal(t, "review-pr", task.Workflow)
}

func TestSmoke_S1_LoadsValidationAndAutoMergeConfig(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, `profiles: [core]
sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-checks:
        labels: [ci]
        workflow: fix-pr-checks
validation:
  format: "fmt ./..."
  lint: "vet ./..."
  build: "build ./..."
  test: "test ./..."
daemon:
  auto_merge: true
  auto_merge_repo: owner/name
  auto_merge_labels: [ready-to-merge, harness-impl]
  auto_merge_branch_pattern: "^feat/"
  auto_merge_reviewer: "copilot-bot"
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "fmt ./...", cfg.Validation.Format)
	assert.Equal(t, "vet ./...", cfg.Validation.Lint)
	assert.Equal(t, "build ./...", cfg.Validation.Build)
	assert.Equal(t, "test ./...", cfg.Validation.Test)
	assert.Equal(t, []string{"ready-to-merge", "harness-impl"}, cfg.Daemon.AutoMergeLabels)
	assert.Equal(t, "^feat/", cfg.Daemon.AutoMergeBranchPattern)
	assert.Equal(t, "copilot-bot", cfg.Daemon.AutoMergeReviewer)
}

func TestSmoke_S2_RejectsEmptyValidationForActivePRValidationWorkflows(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-checks:
        labels: [ci]
        workflow: fix-pr-checks
      resolve-conflicts:
        labels: [merge]
        workflow: resolve-conflicts
      adapt-repo:
        labels: [bootstrap]
        workflow: adapt-repo
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation: at least one of format, lint, build, or test must be set")
	assert.Contains(t, err.Error(), "adapt-repo")
	assert.Contains(t, err.Error(), "fix-pr-checks")
	assert.Contains(t, err.Error(), "resolve-conflicts")
}

func TestSmoke_S3_AllowsPartialValidationForActivePRValidationWorkflow(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix-checks:
        labels: [ci]
        workflow: adapt-repo
validation:
  test: "go test ./..."
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "go test ./...", cfg.Validation.Test)
}

func TestSmoke_S4_RejectsGoimportsPackagePatternForValidationRequiredWorkflows(t *testing.T) {
	for _, workflow := range []string{"fix-pr-checks", "resolve-conflicts", "adapt-repo"} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()

			path := writeConfigFile(t, fmt.Sprintf(`sources:
  github:
    type: github
    repo: owner/name
    tasks:
      validation:
        labels: [ci]
        workflow: %s
validation:
  format: "goimports -l ./cli/..."
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`, workflow))

			_, err := Load(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `validation.format uses goimports package pattern "./cli/..."`)
			assert.Contains(t, err.Error(), workflow)
		})
	}
}

func TestSmoke_S5_AllowsGoimportsDirectoryTargetsForValidationRequiredWorkflows(t *testing.T) {
	for _, workflow := range []string{"fix-pr-checks", "resolve-conflicts", "adapt-repo"} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()

			path := writeConfigFile(t, fmt.Sprintf(`sources:
  github:
    type: github
    repo: owner/name
    tasks:
      validation:
        labels: [ci]
        workflow: %s
validation:
  format: "goimports -l ."
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`, workflow))

			cfg, err := Load(path)
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, "goimports -l .", cfg.Validation.Format)
		})
	}
}

func TestSmoke_S6_AllowsGoimportsLocalPrefixContainingEllipsisForValidationRequiredWorkflows(t *testing.T) {
	for _, workflow := range []string{"fix-pr-checks", "resolve-conflicts", "adapt-repo"} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()

			path := writeConfigFile(t, fmt.Sprintf(`sources:
  github:
    type: github
    repo: owner/name
    tasks:
      validation:
        labels: [ci]
        workflow: %s
validation:
  format: "goimports -local example.com/... -l ."
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`, workflow))

			cfg, err := Load(path)
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, "goimports -local example.com/... -l .", cfg.Validation.Format)
		})
	}
}

func TestSmoke_S7_RejectsRepoRootGoCLIPathsForValidationRequiredWorkflows(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		field      string
		command    string
		errMessage string
	}{
		{
			name:       "lint",
			field:      "lint",
			command:    "go vet ./cli/...",
			errMessage: `validation.lint runs go from repo root against "./cli/..."`,
		},
		{
			name:       "build",
			field:      "build",
			command:    "go build ./cli/cmd/xylem",
			errMessage: `validation.build runs go from repo root against "./cli/cmd/xylem"`,
		},
		{
			name:       "test",
			field:      "test",
			command:    "go test ./cli/...",
			errMessage: `validation.test runs go from repo root against "./cli/..."`,
		},
	}

	for _, workflow := range []string{"fix-pr-checks", "resolve-conflicts", "adapt-repo"} {
		workflow := workflow
		for _, tc := range testCases {
			tc := tc
			t.Run(workflow+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				path := writeConfigFile(t, fmt.Sprintf(`sources:
  github:
    type: github
    repo: owner/name
    tasks:
      validation:
        labels: [ci]
        workflow: %s
validation:
  %s: %q
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`, workflow, tc.field, tc.command))

				_, err := Load(path)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errMessage)
				assert.Contains(t, err.Error(), workflow)
			})
		}
	}
}

func TestSmoke_S8_AllowsCLIWorkingDirGoCommandsForValidationRequiredWorkflows(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		field   string
		command string
	}{
		{name: "lint", field: "lint", command: "cd cli && go vet ./..."},
		{name: "build", field: "build", command: "cd cli && go build ./cmd/xylem"},
		{name: "test", field: "test", command: "cd cli && go test ./..."},
	}

	for _, workflow := range []string{"fix-pr-checks", "resolve-conflicts", "adapt-repo"} {
		workflow := workflow
		for _, tc := range testCases {
			tc := tc
			t.Run(workflow+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				path := writeConfigFile(t, fmt.Sprintf(`sources:
  github:
    type: github
    repo: owner/name
    tasks:
      validation:
        labels: [ci]
        workflow: %s
validation:
  %s: %q
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`, workflow, tc.field, tc.command))

				cfg, err := Load(path)
				require.NoError(t, err)
				require.NotNil(t, cfg)
				switch tc.field {
				case "lint":
					assert.Equal(t, tc.command, cfg.Validation.Lint)
				case "build":
					assert.Equal(t, tc.command, cfg.Validation.Build)
				case "test":
					assert.Equal(t, tc.command, cfg.Validation.Test)
				default:
					t.Fatalf("unexpected field %q", tc.field)
				}
			})
		}
	}
}

func TestValidateRejectsInvalidAutoMergeBranchPattern(t *testing.T) {
	cfg := validConfig()
	cfg.Daemon.AutoMergeBranchPattern = "("

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon.auto_merge_branch_pattern")
}

func TestValidateAutoMergeWorkflowClass(t *testing.T) {
	tests := []struct {
		name      string
		profiles  []string
		autoMerge bool
		wantErr   string
	}{
		{
			name:      "allows disabled auto merge without ops profile",
			autoMerge: false,
		},
		{
			name:      "rejects enabled auto merge without ops profile",
			autoMerge: true,
			wantErr:   "daemon.auto_merge requires an active profile with at least one ops-class workflow",
		},
		{
			name:      "allows enabled auto merge with ops profile",
			profiles:  []string{"core"},
			autoMerge: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Profiles = tt.profiles
			cfg.Daemon.AutoMerge = tt.autoMerge

			err := cfg.validateAutoMergeWorkflowClass()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestComposedProfileHasWorkflowClassTreatsUnsetClassAsDelivery(t *testing.T) {
	composed := &profiles.ComposedProfile{
		Workflows: map[string][]byte{
			"merge-pr": []byte(`name: merge-pr
phases:
  - name: analyze
    prompt_file: prompts/merge-pr/analyze.md
    max_turns: 1
`),
		},
	}

	hasOps, err := composedProfileHasWorkflowClass(composed, policy.Ops)
	require.NoError(t, err)
	assert.False(t, hasOps)
}

func TestSmoke_S9_RejectsAutoMergeWithoutOpsProfile(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix:
        labels: [bug]
        workflow: fix-bug
daemon:
  auto_merge: true
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon.auto_merge requires an active profile with at least one ops-class workflow")
}

func TestSmoke_S10_AllowsAutoMergeForSelfHostingProfileComposition(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, `profiles: [core, self-hosting-xylem]
sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix:
        labels: [bug]
        workflow: fix-bug
daemon:
  auto_merge: true
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	workflows, err := cfg.activeWorkflowDefinitions()
	require.NoError(t, err)
	hasOps, err := workflowMapHasClass(workflows, policy.Ops)
	require.NoError(t, err)
	assert.True(t, hasOps)
}

func TestSmoke_S11_AllowsAutoMergeWithCheckedInOpsWorkflowWithoutProfiles(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix:
        labels: [bug]
        workflow: fix-bug
daemon:
  auto_merge: true
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)
	writeWorkflowFile(t, path, "merge-pr", `name: merge-pr
class: ops
phases:
  - name: merge
    prompt_file: prompts/merge-pr/merge.md
    max_turns: 1
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	workflows, err := cfg.activeWorkflowDefinitions()
	require.NoError(t, err)
	hasOps, err := workflowMapHasClass(workflows, policy.Ops)
	require.NoError(t, err)
	assert.True(t, hasOps)
}

func TestSmoke_S12_RejectsAutoMergeWhenWorkflowClassDefaultsToDelivery(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      fix:
        labels: [bug]
        workflow: fix-bug
daemon:
  auto_merge: true
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)
	writeWorkflowFile(t, path, "merge-pr", `name: merge-pr
phases:
  - name: merge
    prompt_file: prompts/merge-pr/merge.md
    max_turns: 1
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon.auto_merge requires an active profile with at least one ops-class workflow")
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

func TestTelemetryDefaultsWhenAbsent(t *testing.T) {
	cfg := Config{}
	assert.True(t, cfg.TelemetryEnabled())
	assert.Equal(t, "nicholls-inc/xylem", cfg.TelemetryTargetRepo())
	assert.False(t, cfg.Telemetry.Extended)
}

func TestTelemetryLoadsFromConfig(t *testing.T) {
	path := writeSmokeConfigFile(t, `telemetry:
  enabled: false
  target_repo: "myorg/myrepo"
  extended: true
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.False(t, cfg.TelemetryEnabled())
	assert.Equal(t, "myorg/myrepo", cfg.TelemetryTargetRepo())
	assert.True(t, cfg.Telemetry.Extended)
}

func TestTelemetryEnvOverridesConfig(t *testing.T) {
	cfg := Config{}
	assert.True(t, cfg.TelemetryEnabled())

	t.Setenv("XYLEM_TELEMETRY", "off")
	assert.False(t, cfg.TelemetryEnabled())

	t.Setenv("XYLEM_TELEMETRY", "on")
	assert.True(t, cfg.TelemetryEnabled())
}

func TestTelemetryInvalidTargetRepoRejected(t *testing.T) {
	path := writeSmokeConfigFile(t, `telemetry:
  target_repo: "not-a-valid-repo"
`)
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "telemetry.target_repo")
}
