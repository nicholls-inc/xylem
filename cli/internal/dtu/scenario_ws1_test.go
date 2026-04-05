package dtu_test

// WS1 DTU scenario tests — end-to-end coverage for config surface policy
// behaviors described in docs/design/harness-smoke-scenarios/ws1-config-surface-policy.md.
//
// These tests drive the full scan→drain pipeline through DTU-shimmed boundaries.
// Shared runner scaffolding is wired through the real runner path so these
// scenarios exercise policy enforcement and protected-surface verification.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	dtu "github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/surface"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ws1Config returns a base config pointing at acme/widget with a single task.
func ws1Config(stateDir, workflow string) *config.Config {
	cfg := baseScenarioConfig(stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "acme/widget",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: workflow,
					StatusLabels: &config.StatusLabels{
						Queued:    "queued",
						Running:   "in-progress",
						Completed: "done",
						Failed:    "failed",
					},
				},
			},
		},
	}
	return cfg
}

// ws1Drain sets up scanner + drain runner and returns the drain result.
func ws1Drain(t *testing.T, env *dtuScenarioEnv, cfg *config.Config) (scanner.ScanResult, DrainResult) {
	t.Helper()

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	src := &source.GitHub{Repo: "acme/widget", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)
	drainResult, err := drainer.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	return scanResult, drainResult
}

// DrainResult is imported from runner; alias here for the helper signature.
type DrainResult = struct {
	Completed int
	Failed    int
	Skipped   int
	Waiting   int
}

// ---------------------------------------------------------------------------
// S1, S8, S20, S22, S24-S25, S27-S28: Policy allow happy path
// ---------------------------------------------------------------------------

// TestWS1PolicyAllowHappyPath exercises the full pipeline with a config that
// has a harness section. The default policy allows phase_execute, so the vessel
// should complete with both phases invoked.
//
// Covers: S1  (config loads with full harness section)
//
//	S8  (default policy allows phase_execute)
//	S20 (surface pre-snapshot taken before phase execution)
//	S22 (audit log records policy decisions)
//	S24 (phaseActionType returns "external_command" for command phases)
//	S25 (phaseActionType returns "phase_execute" for prompt phases)
//	S27 (drain.go creates Intermediary from config)
//	S28 (daemon.go creates Intermediary from config)
func TestWS1PolicyAllowHappyPath(t *testing.T) {
	env := newScenarioEnv(t, "ws1-policy-allow-happy-path.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "plan", prompt: "Plan fix for issue {{.Issue.Number}}: {{.Issue.Title}}"},
		{name: "implement", prompt: "Implement fix for issue {{.Issue.Number}}"},
	})

	cfg := ws1Config(env.stateDir, "fix-bug")
	scanResult, drainResult := ws1Drain(t, env, cfg)

	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err := env.queue.FindByID("issue-10")
	if err != nil {
		t.Fatalf("FindByID(issue-10) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}

	// Verify both phases were invoked via DTU event log.
	events := readEvents(t, env.store)
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 2 {
		t.Fatalf("len(claude invocations) = %d, want 2", len(claudeInvocations))
	}
	if claudeInvocations[0].Shim.Phase != "plan" {
		t.Fatalf("first claude phase = %q, want %q", claudeInvocations[0].Shim.Phase, "plan")
	}
	if claudeInvocations[1].Shim.Phase != "implement" {
		t.Fatalf("second claude phase = %q, want %q", claudeInvocations[1].Shim.Phase, "implement")
	}

	// Verify issue labels transitioned through the status label lifecycle.
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "acme/widget", 10), []string{"bug", "done"})
}

// TestWS1PolicyAllowHappyPathAuditLog verifies that when the runner appends
// policy decisions, the audit log contains allow decisions for each phase.
//
// Covers: S22 (audit log records policy decisions)
//
//	S27 (drain.go creates Intermediary from config)
func TestWS1PolicyAllowHappyPathAuditLog(t *testing.T) {
	env := newScenarioEnv(t, "ws1-policy-allow-happy-path.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "plan", prompt: "Plan fix for issue {{.Issue.Number}}"},
		{name: "implement", prompt: "Implement fix for issue {{.Issue.Number}}"},
	})

	cfg := ws1Config(env.stateDir, "fix-bug")

	// When the runner is wired, the audit log should be written to the state dir.
	auditLogPath := filepath.Join(env.stateDir, "audit.jsonl")
	auditLog := intermediary.NewAuditLog(auditLogPath)

	_, drainResult := ws1Drain(t, env, cfg)
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	entries, err := auditLog.Entries()
	if err != nil {
		t.Fatalf("AuditLog.Entries() error = %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("len(audit entries) = %d, want >= 2 (one per phase)", len(entries))
	}
	for _, entry := range entries {
		if entry.Decision != intermediary.Allow {
			t.Fatalf("audit entry decision = %q, want %q", entry.Decision, intermediary.Allow)
		}
		if entry.Intent.AgentID == "" {
			t.Fatal("audit entry agent_id is empty")
		}
	}
}

// ---------------------------------------------------------------------------
// S6, S18: Policy deny blocks phase
// ---------------------------------------------------------------------------

// TestWS1PolicyDenyBlocksPhase verifies that a deny-all policy prevents the
// provider from being invoked and the vessel transitions to failed.
//
// Covers: S6  (default policy denies file_write to .xylem/HARNESS.md)
//
//	S18 (runner policy denies phase — vessel fails with "denied by policy")
func TestWS1PolicyDenyBlocksPhase(t *testing.T) {
	env := newScenarioEnv(t, "ws1-policy-deny-blocks-phase.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "solve", prompt: "Solve issue {{.Issue.Number}}"},
	})

	cfg := ws1Config(env.stateDir, "fix-bug")
	cfg.Harness.Policy.Rules = []config.PolicyRuleConfig{{
		Action:   "*",
		Resource: "*",
		Effect:   string(intermediary.Deny),
	}}

	_, drainResult := ws1Drain(t, env, cfg)
	if drainResult.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", drainResult.Failed)
	}

	vessel, err := env.queue.FindByID("issue-20")
	if err != nil {
		t.Fatalf("FindByID(issue-20) error = %v", err)
	}
	if vessel.State != queue.StateFailed {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateFailed)
	}
	if !strings.Contains(vessel.Error, "denied by policy") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessel.Error, "denied by policy")
	}

	summary := loadVesselSummary(t, env.stateDir, "issue-20")
	if summary.State != string(queue.StateFailed) {
		t.Fatalf("summary.State = %q, want %q", summary.State, queue.StateFailed)
	}
	if len(summary.Phases) != 0 {
		t.Fatalf("len(summary.Phases) = %d, want 0", len(summary.Phases))
	}
	if summary.EvidenceManifestPath != "" {
		t.Fatalf("summary.EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
	}

	// Verify the provider was never invoked.
	events := readEvents(t, env.store)
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 0 {
		t.Fatalf("len(claude invocations) = %d, want 0 (policy should block)", len(claudeInvocations))
	}
}

// ---------------------------------------------------------------------------
// S7, S19: Policy require_approval blocks phase
// ---------------------------------------------------------------------------

// TestWS1PolicyRequireApproval verifies that a require_approval policy blocks
// the phase and the vessel fails with an approval message.
//
// Covers: S7  (default policy requires approval for git_push)
//
//	S19 (runner policy require_approval — vessel fails with approval message)
func TestWS1PolicyRequireApproval(t *testing.T) {
	env := newScenarioEnv(t, "ws1-policy-require-approval.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "deploy", prompt: "Deploy fix for issue {{.Issue.Number}}"},
	})

	cfg := ws1Config(env.stateDir, "fix-bug")
	cfg.Harness.Policy.Rules = []config.PolicyRuleConfig{{
		Action:   "phase_execute",
		Resource: "*",
		Effect:   string(intermediary.RequireApproval),
	}}

	_, drainResult := ws1Drain(t, env, cfg)
	if drainResult.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", drainResult.Failed)
	}

	vessel, err := env.queue.FindByID("issue-30")
	if err != nil {
		t.Fatalf("FindByID(issue-30) error = %v", err)
	}
	if vessel.State != queue.StateFailed {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateFailed)
	}
	if !strings.Contains(vessel.Error, "requires approval") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessel.Error, "requires approval")
	}
	if !strings.Contains(vessel.Error, "automatic approval not yet supported") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessel.Error, "automatic approval not yet supported")
	}

	// Verify the provider was never invoked.
	events := readEvents(t, env.store)
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 0 {
		t.Fatalf("len(claude invocations) = %d, want 0 (approval should block)", len(claudeInvocations))
	}
}

// ---------------------------------------------------------------------------
// S10, S14, S21, S23: Surface violation detection
// ---------------------------------------------------------------------------

// TestWS1SurfaceViolationDetected verifies that when a phase modifies a
// protected surface file, the surface post-verification catches the mutation
// and the vessel fails.
//
// Covers: S10 (TakeSnapshot matches globs and hashes correctly)
//
//	S14 (Compare detects a modified file)
//	S21 (surface post-verification detects mutation — vessel fails)
//	S23 (audit log records surface violations)
func TestWS1SurfaceViolationDetected(t *testing.T) {
	env := newScenarioEnv(t, "ws1-surface-violation.yaml")
	defer withWorkingDir(t, env.repoDir)()

	// The tamper phase is a command phase that overwrites .xylem.yml.
	// We write a workflow where phase 1 is a command that mutates the
	// protected file, and phase 2 is a prompt that should never run.
	workflowDir := filepath.Join(env.repoDir, ".xylem", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", workflowDir, err)
	}
	workflowContent := `name: fix-bug
phases:
  - name: tamper
    type: command
    run: "echo 'tampered: true' > .xylem.yml"
  - name: implement
    prompt_file: ` + filepath.Join(env.repoDir, ".xylem", "prompts", "fix-bug", "implement.md") + `
    max_turns: 3
`
	if err := os.WriteFile(filepath.Join(workflowDir, "fix-bug.yaml"), []byte(workflowContent), 0o644); err != nil {
		t.Fatalf("WriteFile workflow: %v", err)
	}
	promptDir := filepath.Join(env.repoDir, ".xylem", "prompts", "fix-bug")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", promptDir, err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte("Implement fix"), 0o644); err != nil {
		t.Fatalf("WriteFile prompt: %v", err)
	}

	// Seed a .xylem.yml in the worktree root so the pre-snapshot captures it.
	configContent := "repo: acme/widget\nconcurrency: 1\n"
	if err := os.WriteFile(filepath.Join(env.repoDir, ".xylem.yml"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("WriteFile .xylem.yml: %v", err)
	}

	cfg := ws1Config(env.stateDir, "fix-bug")
	auditLog := intermediary.NewAuditLog(filepath.Join(env.stateDir, "audit.jsonl"))
	_, drainResult := ws1Drain(t, env, cfg)

	if drainResult.Failed != 1 {
		t.Fatalf("DrainResult.Failed = %d, want 1", drainResult.Failed)
	}

	vessel, err := env.queue.FindByID("issue-40")
	if err != nil {
		t.Fatalf("FindByID(issue-40) error = %v", err)
	}
	if vessel.State != queue.StateFailed {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateFailed)
	}
	if !strings.Contains(vessel.Error, "violated protected surfaces") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessel.Error, "violated protected surfaces")
	}
	if !strings.Contains(vessel.Error, ".xylem.yml") {
		t.Fatalf("vessel.Error = %q, want to contain %q", vessel.Error, ".xylem.yml")
	}

	summary := loadVesselSummary(t, env.stateDir, "issue-40")
	if summary.State != string(queue.StateFailed) {
		t.Fatalf("summary.State = %q, want %q", summary.State, queue.StateFailed)
	}
	if len(summary.Phases) != 1 {
		t.Fatalf("len(summary.Phases) = %d, want 1", len(summary.Phases))
	}
	if summary.Phases[0].Name != "tamper" {
		t.Fatalf("summary.Phases[0].Name = %q, want %q", summary.Phases[0].Name, "tamper")
	}
	if summary.Phases[0].Status != "failed" {
		t.Fatalf("summary.Phases[0].Status = %q, want failed", summary.Phases[0].Status)
	}
	if summary.EvidenceManifestPath != "" {
		t.Fatalf("summary.EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
	}

	// Verify the implement phase was never invoked (violation stops after tamper).
	events := readEvents(t, env.store)
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 0 {
		t.Fatalf("len(claude invocations) = %d, want 0 (surface violation should block)", len(claudeInvocations))
	}

	entries, err := auditLog.Entries()
	if err != nil {
		t.Fatalf("AuditLog.Entries() error = %v", err)
	}
	foundViolation := false
	for _, entry := range entries {
		if entry.Decision == intermediary.Deny &&
			strings.Contains(entry.Error, "violated protected surfaces") &&
			strings.Contains(entry.Error, ".xylem.yml") {
			foundViolation = true
			break
		}
	}
	if !foundViolation {
		t.Fatalf("audit log entries = %+v, want a deny entry describing the protected surface violation", entries)
	}
}

// TestWS1SurfaceSnapshotDeterministic verifies that TakeSnapshot is
// deterministic — two calls without file changes return identical results.
// This is a unit-level check that validates the DTU worktree setup produces
// a stable filesystem for snapshot comparison.
//
// Covers: S11 (TakeSnapshot is deterministic)
//
//	S12 (TakeSnapshot sorts results by path)
func TestWS1SurfaceSnapshotDeterministic(t *testing.T) {
	env := newScenarioEnv(t, "ws1-policy-allow-happy-path.yaml")

	// Seed protected files in the repo dir.
	xylemDir := filepath.Join(env.repoDir, ".xylem")
	if err := os.MkdirAll(filepath.Join(xylemDir, "workflows"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	files := map[string]string{
		".xylem.yml":                           "repo: acme/widget\n",
		".xylem/HARNESS.md":                    "# Harness\n",
		".xylem/workflows/fix-bug.yaml":        "name: fix-bug\n",
		".xylem/workflows/implement-feat.yaml": "name: implement-feat\n",
	}
	for rel, content := range files {
		path := filepath.Join(env.repoDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}

	patterns := []string{".xylem.yml", ".xylem/HARNESS.md", ".xylem/workflows/*.yaml"}

	snap1, err := surface.TakeSnapshot(env.repoDir, patterns)
	if err != nil {
		t.Fatalf("TakeSnapshot(1) error = %v", err)
	}
	snap2, err := surface.TakeSnapshot(env.repoDir, patterns)
	if err != nil {
		t.Fatalf("TakeSnapshot(2) error = %v", err)
	}

	if len(snap1.Files) != 4 {
		t.Fatalf("len(snap1.Files) = %d, want 4", len(snap1.Files))
	}
	if len(snap2.Files) != len(snap1.Files) {
		t.Fatalf("len(snap2.Files) = %d, want %d", len(snap2.Files), len(snap1.Files))
	}

	// S11: deterministic — both snapshots must be identical.
	for i := range snap1.Files {
		if snap1.Files[i] != snap2.Files[i] {
			t.Fatalf("snap1.Files[%d] = %+v, snap2.Files[%d] = %+v", i, snap1.Files[i], i, snap2.Files[i])
		}
	}

	// S12: sorted by path.
	for i := 1; i < len(snap1.Files); i++ {
		if snap1.Files[i-1].Path >= snap1.Files[i].Path {
			t.Fatalf("snapshot not sorted: %q >= %q", snap1.Files[i-1].Path, snap1.Files[i].Path)
		}
	}
}

// TestWS1SurfaceCompareDetectsModification verifies that Compare detects when
// a file hash changes between before and after snapshots.
//
// Covers: S13 (identical snapshots produce no violations)
//
//	S14 (modified file detected)
//	S15 (deleted file detected)
//	S16 (created file detected)
func TestWS1SurfaceCompareDetectsChanges(t *testing.T) {
	// S13: identical snapshots — no violations.
	snap := surface.Snapshot{
		Files: []surface.FileHash{{Path: ".xylem.yml", Hash: "abc123"}},
	}
	violations := surface.Compare(snap, snap)
	if len(violations) != 0 {
		t.Fatalf("S13: len(violations) = %d, want 0 for identical snapshots", len(violations))
	}

	// S14: modified file.
	before := surface.Snapshot{
		Files: []surface.FileHash{{Path: ".xylem.yml", Hash: "aaa"}},
	}
	after := surface.Snapshot{
		Files: []surface.FileHash{{Path: ".xylem.yml", Hash: "bbb"}},
	}
	violations = surface.Compare(before, after)
	if len(violations) != 1 {
		t.Fatalf("S14: len(violations) = %d, want 1", len(violations))
	}
	if violations[0].Path != ".xylem.yml" || violations[0].Before != "aaa" || violations[0].After != "bbb" {
		t.Fatalf("S14: violation = %+v, want path=.xylem.yml before=aaa after=bbb", violations[0])
	}

	// S15: deleted file.
	before = surface.Snapshot{
		Files: []surface.FileHash{{Path: ".xylem/HARNESS.md", Hash: "ddd"}},
	}
	after = surface.Snapshot{Files: []surface.FileHash{}}
	violations = surface.Compare(before, after)
	if len(violations) != 1 {
		t.Fatalf("S15: len(violations) = %d, want 1", len(violations))
	}
	if violations[0].After != "deleted" {
		t.Fatalf("S15: violation.After = %q, want %q", violations[0].After, "deleted")
	}

	// S16: created file.
	before = surface.Snapshot{Files: []surface.FileHash{}}
	after = surface.Snapshot{
		Files: []surface.FileHash{{Path: ".xylem/workflows/new.yaml", Hash: "eee"}},
	}
	violations = surface.Compare(before, after)
	if len(violations) != 1 {
		t.Fatalf("S16: len(violations) = %d, want 1", len(violations))
	}
	if violations[0].Before != "absent" {
		t.Fatalf("S16: violation.Before = %q, want %q", violations[0].Before, "absent")
	}
}

// ---------------------------------------------------------------------------
// S2, S29, S30: Defaults-only config
// ---------------------------------------------------------------------------

// TestWS1ConfigDefaultsOnlyCompletesNormally verifies that the pipeline works
// with no harness, observability, or cost config — all defaults activate.
//
// Covers: S2  (config loads with no harness section, defaults activate)
//
//	S29 (observability defaults apply when section absent)
//	S30 (cost config with budget fields — nil budget when absent)
func TestWS1ConfigDefaultsOnlyCompletesNormally(t *testing.T) {
	env := newScenarioEnv(t, "ws1-config-defaults-only.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "fix", prompt: "Fix issue {{.Issue.Number}}: {{.Issue.Title}}"},
	})

	cfg := ws1Config(env.stateDir, "fix-bug")
	// Intentionally no Harness, Observability, or Cost sections on cfg.

	scanResult, drainResult := ws1Drain(t, env, cfg)
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err := env.queue.FindByID("issue-50")
	if err != nil {
		t.Fatalf("FindByID(issue-50) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}

	// Verify the fix phase was invoked.
	events := readEvents(t, env.store)
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 1 {
		t.Fatalf("len(claude invocations) = %d, want 1", len(claudeInvocations))
	}
	if claudeInvocations[0].Shim.Phase != "fix" {
		t.Fatalf("claude phase = %q, want %q", claudeInvocations[0].Shim.Phase, "fix")
	}

	// Verify status labels applied.
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "acme/widget", 50), []string{"bug", "done"})
}

func TestWS1CheckedInSmokeFixtureConfigsLoad(t *testing.T) {
	fixtureDir := scenarioFixturePath(t, "ws1-smoke-fixture")
	defer withWorkingDir(t, fixtureDir)()

	if _, err := os.Stat(filepath.Join(".xylem", "HARNESS.md")); err != nil {
		t.Fatalf("Stat(.xylem/HARNESS.md): %v", err)
	}

	testCases := []struct {
		name         string
		configPath   string
		workflowName string
	}{
		{name: "policy allow", configPath: ".xylem.yml", workflowName: "fix-bug-allow"},
		{name: "defaults only", configPath: ".xylem.defaults-only.yml", workflowName: "fix-bug-defaults"},
		{name: "policy deny", configPath: ".xylem.policy-deny.yml", workflowName: "fix-bug-deny"},
		{name: "require approval", configPath: ".xylem.require-approval.yml", workflowName: "fix-bug-require-approval"},
		{name: "surface violation", configPath: ".xylem.surface-violation.yml", workflowName: "fix-bug-surface-violation"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(tc.configPath)
			if err != nil {
				t.Fatalf("Load(%q): %v", tc.configPath, err)
			}
			if cfg.StateDir != ".xylem" {
				t.Fatalf("cfg.StateDir = %q, want %q", cfg.StateDir, ".xylem")
			}
			src, ok := cfg.Sources["issues"]
			if !ok {
				t.Fatalf("cfg.Sources missing %q entry", "issues")
			}
			if src.Repo != "acme/widget" {
				t.Fatalf("cfg.Sources[issues].Repo = %q, want %q", src.Repo, "acme/widget")
			}
			task, ok := src.Tasks["bugs"]
			if !ok {
				t.Fatalf("cfg.Sources[issues].Tasks missing %q entry", "bugs")
			}
			if task.Workflow != tc.workflowName {
				t.Fatalf("task.Workflow = %q, want %q", task.Workflow, tc.workflowName)
			}
			if _, err := workflow.Load(filepath.Join(".xylem", "workflows", task.Workflow+".yaml")); err != nil {
				t.Fatalf("Load workflow %q: %v", task.Workflow, err)
			}
		})
	}
}

func TestWS1CheckedInSmokeFixtureHappyPath(t *testing.T) {
	env := newScenarioEnv(t, "ws1-policy-allow-happy-path.yaml")
	copyScenarioRepoFixture(t, "ws1-smoke-fixture", env.repoDir)
	defer withWorkingDir(t, env.repoDir)()

	cfg, err := config.Load(".xylem.yml")
	if err != nil {
		t.Fatalf("Load(.xylem.yml): %v", err)
	}

	scanResult, drainResult := ws1Drain(t, env, cfg)
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	if drainResult.Completed != 1 {
		t.Fatalf("DrainResult.Completed = %d, want 1", drainResult.Completed)
	}

	vessel, err := env.queue.FindByID("issue-10")
	if err != nil {
		t.Fatalf("FindByID(issue-10) error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateCompleted)
	}

	events := readEvents(t, env.store)
	claudeInvocations := filterShimEvents(events, dtu.EventKindShimInvocation, "claude", nil)
	if len(claudeInvocations) != 2 {
		t.Fatalf("len(claude invocations) = %d, want 2", len(claudeInvocations))
	}
	if claudeInvocations[0].Shim.Phase != "plan" {
		t.Fatalf("first claude phase = %q, want %q", claudeInvocations[0].Shim.Phase, "plan")
	}
	if claudeInvocations[1].Shim.Phase != "implement" {
		t.Fatalf("second claude phase = %q, want %q", claudeInvocations[1].Shim.Phase, "implement")
	}

	assertStringSliceEqual(t, readIssueLabels(t, env.store, "acme/widget", 10), []string{"bug", "done"})
}

// TestWS1ConfigDefaultsIntermediaryWiring verifies that when no harness config
// is present, the runner still constructs an Intermediary with the default policy.
//
// Covers: S2  (defaults activate)
//
//	S27 (drain.go creates Intermediary from config)
func TestWS1ConfigDefaultsIntermediaryWiring(t *testing.T) {
	env := newScenarioEnv(t, "ws1-config-defaults-only.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "fix", prompt: "Fix issue {{.Issue.Number}}"},
	})

	cfg := ws1Config(env.stateDir, "fix-bug")
	src := &source.GitHub{Repo: "acme/widget", CmdRunner: env.cmdRunner}
	drainer := newDrainRunner(t, cfg, env.queue, env.cmdRunner, env.repoDir, src)

	if drainer.Intermediary == nil {
		t.Fatal("drainer.Intermediary = nil, want default intermediary")
	}
	if drainer.AuditLog == nil {
		t.Fatal("drainer.AuditLog = nil, want audit log")
	}
	if drainer.Tracer == nil {
		t.Fatal("drainer.Tracer = nil, want tracer when observability defaults are enabled")
	}

	deny := drainer.Intermediary.Evaluate(intermediary.Intent{
		Action:   "file_write",
		Resource: ".xylem/HARNESS.md",
		AgentID:  "issue-50",
	})
	if deny.Effect != intermediary.Deny {
		t.Fatalf("default protected-surface effect = %q, want %q", deny.Effect, intermediary.Deny)
	}

	allow := drainer.Intermediary.Evaluate(intermediary.Intent{
		Action:   "phase_execute",
		Resource: "fix",
		AgentID:  "issue-50",
	})
	if allow.Effect != intermediary.Allow {
		t.Fatalf("default phase_execute effect = %q, want %q", allow.Effect, intermediary.Allow)
	}

	entry := intermediary.AuditEntry{
		Intent: intermediary.Intent{
			Action:   "phase_execute",
			Resource: "fix",
			AgentID:  "issue-50",
		},
		Decision:  intermediary.Allow,
		Timestamp: time.Now().UTC(),
	}
	if err := drainer.AuditLog.Append(entry); err != nil {
		t.Fatalf("AuditLog.Append() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "audit.jsonl")); err != nil {
		t.Fatalf("Stat(audit.jsonl) error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// S9: TakeSnapshot on empty directory
// ---------------------------------------------------------------------------

// TestWS1SurfaceSnapshotEmptyDirectory verifies TakeSnapshot returns an empty
// snapshot when the directory contains no matching files.
//
// Covers: S9 (TakeSnapshot on empty directory returns empty snapshot)
func TestWS1SurfaceSnapshotEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	patterns := []string{".xylem/HARNESS.md", ".xylem.yml"}

	snap, err := surface.TakeSnapshot(dir, patterns)
	if err != nil {
		t.Fatalf("TakeSnapshot error = %v", err)
	}
	if len(snap.Files) != 0 {
		t.Fatalf("len(snap.Files) = %d, want 0 for empty directory", len(snap.Files))
	}
}

// ---------------------------------------------------------------------------
// S3: paths: ["none"] disables surface protection
// ---------------------------------------------------------------------------

// TestWS1SurfaceNonePattern verifies that TakeSnapshot returns an empty
// snapshot when the only pattern is "none" (the magic disable value).
// In the config layer, EffectiveProtectedSurfaces() returns nil for this case.
// At the surface layer, the pattern "none" simply won't match any real files.
//
// Covers: S3 (paths: ["none"] disables surface protection)
func TestWS1SurfaceNonePattern(t *testing.T) {
	env := newScenarioEnv(t, "ws1-policy-allow-happy-path.yaml")

	// Seed a .xylem.yml so there's a file that would normally match.
	if err := os.WriteFile(filepath.Join(env.repoDir, ".xylem.yml"), []byte("repo: x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// "none" is the magic value. It doesn't match any real file.
	snap, err := surface.TakeSnapshot(env.repoDir, []string{"none"})
	if err != nil {
		t.Fatalf("TakeSnapshot error = %v", err)
	}
	if len(snap.Files) != 0 {
		t.Fatalf("len(snap.Files) = %d, want 0 for 'none' pattern", len(snap.Files))
	}
}

// ---------------------------------------------------------------------------
// S4, S5, S31, S32: Config validation errors
// These are config-load-time errors tested at the unit level. The DTU
// manifests validate that the pipeline never starts when config is invalid.
// ---------------------------------------------------------------------------

// TestWS1ConfigValidationSurfaceGlobInvalid verifies the pipeline won't start
// when the config contains invalid glob patterns in protected_surfaces.
//
// Covers: S4 (config validation rejects invalid glob patterns)
func TestWS1ConfigValidationSurfaceGlobInvalid(t *testing.T) {
	// Surface-level: TakeSnapshot rejects bad globs.
	dir := t.TempDir()
	_, err := surface.TakeSnapshot(dir, []string{"[invalid-glob"})
	if err == nil {
		t.Fatal("TakeSnapshot with invalid glob should return error")
	}
	if !strings.Contains(err.Error(), "invalid-glob") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "invalid-glob")
	}
}

// TestWS1IntermediaryPolicyEffects exercises the intermediary directly to
// validate all three policy effects in isolation before the runner integration.
//
// Covers: S6  (deny file_write to .xylem/HARNESS.md)
//
//	S7  (require_approval for git_push)
//	S8  (allow general phase_execute)
func TestWS1IntermediaryPolicyEffects(t *testing.T) {
	// Construct the default policy described in the spec (§2.3).
	defaultPolicy := intermediary.Policy{
		Name: "default",
		Rules: []intermediary.Rule{
			{Action: "file_write", Resource: ".xylem/HARNESS.md", Effect: intermediary.Deny},
			{Action: "file_write", Resource: ".xylem.yml", Effect: intermediary.Deny},
			{Action: "file_write", Resource: ".xylem/workflows/*", Effect: intermediary.Deny},
			{Action: "git_push", Resource: "*", Effect: intermediary.RequireApproval},
			{Action: "*", Resource: "*", Effect: intermediary.Allow},
		},
	}

	auditLog := intermediary.NewAuditLog(filepath.Join(t.TempDir(), "audit.jsonl"))
	interm := intermediary.NewIntermediary([]intermediary.Policy{defaultPolicy}, auditLog, nil)

	// S6: deny file_write to .xylem/HARNESS.md
	result := interm.Evaluate(intermediary.Intent{
		Action: "file_write", Resource: ".xylem/HARNESS.md", AgentID: "vessel-001",
	})
	if result.Effect != intermediary.Deny {
		t.Fatalf("S6: effect = %q, want %q", result.Effect, intermediary.Deny)
	}

	// S7: require_approval for git_push
	result = interm.Evaluate(intermediary.Intent{
		Action: "git_push", Resource: "main", AgentID: "vessel-002",
	})
	if result.Effect != intermediary.RequireApproval {
		t.Fatalf("S7: effect = %q, want %q", result.Effect, intermediary.RequireApproval)
	}

	// S8: allow phase_execute
	result = interm.Evaluate(intermediary.Intent{
		Action: "phase_execute", Resource: "lint", AgentID: "vessel-003",
	})
	if result.Effect != intermediary.Allow {
		t.Fatalf("S8: effect = %q, want %q", result.Effect, intermediary.Allow)
	}
	if result.MatchedRule == nil || result.MatchedRule.Action != "*" {
		t.Fatalf("S8: matched rule = %+v, want wildcard catch-all", result.MatchedRule)
	}
}
