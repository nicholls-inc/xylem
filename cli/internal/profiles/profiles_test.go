package profiles

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	workflowpkg "github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSmoke_S1_LoadCoreProfileReturnsEmbeddedAssets(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	require.NotNil(t, profile)
	assert.Equal(t, "core", profile.Name)
	assert.Equal(t, 2, profile.Version)

	harnessTemplate, err := fs.ReadFile(profile.FS, "HARNESS.md.tmpl")
	require.NoError(t, err)
	assert.Contains(t, string(harnessTemplate), "# Project Overview")

	configTemplate, err := fs.ReadFile(profile.FS, "xylem.yml.tmpl")
	require.NoError(t, err)
	assert.Contains(t, string(configTemplate), `repo: "{{ .Repo }}"`)

	lockTemplate, err := fs.ReadFile(profile.FS, "profile.lock.tmpl")
	require.NoError(t, err)
	assert.Contains(t, string(lockTemplate), "profile_version: 1")
	assert.Contains(t, string(lockTemplate), "{{ .LockedAt }}")
}

func TestLoadUnknownProfile(t *testing.T) {
	profile, err := Load("nonexistent")
	require.Error(t, err)
	assert.Nil(t, profile)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "unknown profile")
}

func TestSmoke_S2_ComposeCoreIncludesSeededWorkflowsAndTemplates(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core")
	require.NoError(t, err)

	require.NotNil(t, composed)
	require.Len(t, composed.Profiles, 1)
	assert.Equal(t, "core", composed.Profiles[0].Name)
	assert.Equal(t, 2, composed.Profiles[0].Version)

	assert.Equal(t, []string{
		"adapt-repo",
		"context-weight-audit",
		"field-report",
		"fix-bug",
		"fix-pr-checks",
		"implement-feature",
		"lessons",
		"merge-pr",
		"refine-issue",
		"resolve-conflicts",
		"respond-to-pr-review",
		"review-pr",
		"security-compliance",
		"triage",
		"workflow-health-report",
	}, sortedKeys(composed.Workflows))
	assert.Contains(t, sortedKeys(composed.Prompts), "adapt-repo/plan")
	assert.Contains(t, sortedKeys(composed.Prompts), "adapt-repo/pr")
	assert.Contains(t, sortedKeys(composed.Prompts), "security-compliance/synthesize")
	assert.Contains(t, sortedKeys(composed.Prompts), "workflow-health-report/report")
	assert.Contains(t, sortedKeys(composed.Sources), "pr-lifecycle")
	assert.Contains(t, sortedKeys(composed.Sources), "security-compliance")
	assert.Contains(t, sortedKeys(composed.Sources), "field-report")
	require.Len(t, composed.ConfigOverlays, 1)

	assert.Contains(t, string(composed.Workflows["fix-bug"]), "name: fix-bug")
	assert.Contains(t, string(composed.Workflows["implement-feature"]), "name: implement-feature")
	assert.Contains(t, string(composed.Prompts["adapt-repo/pr"]), `--label "ready-to-merge"`)
	assert.Contains(t, string(composed.Prompts["fix-bug/pr"]), "Create a pull request")
	assert.Contains(t, string(composed.Prompts["fix-bug/pr"]), `--label "ready-to-merge"`)
	assert.Contains(t, string(composed.Prompts["implement-feature/pr"]), `--label "ready-to-merge"`)
	assert.Contains(t, string(composed.ConfigOverlays[0]), `repo: "{{ .Repo }}"`)
}

func TestSmoke_S3_ComposeUnknownProfileReturnsClearError(t *testing.T) {
	t.Parallel()

	composed, err := Compose("nonexistent")
	require.Error(t, err)

	assert.Nil(t, composed)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "unknown profile")
}

func TestComposeRequiresAtLeastOneProfile(t *testing.T) {
	composed, err := Compose()
	require.Error(t, err)
	assert.Nil(t, composed)
	assert.Contains(t, err.Error(), "no profiles requested")
}

func TestComposeRejectsConflictingSourceKeys(t *testing.T) {
	composed, err := Compose("core", "core")
	require.Error(t, err)
	assert.Nil(t, composed)
	assert.Contains(t, err.Error(), `source "`)
	assert.Contains(t, err.Error(), `conflicts`)
	assert.True(t, strings.Count(err.Error(), `"core"`) >= 2, "expected both conflicting profiles in error: %q", err.Error())
}

func TestComposeCoreAndSelfHostingXylemIncludesOverlayAssets(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	assert.Contains(t, sortedKeys(composed.Workflows), "implement-harness")
	assert.Contains(t, sortedKeys(composed.Workflows), "hardening-audit")
	assert.Contains(t, sortedKeys(composed.Workflows), "continuous-improvement")
	assert.Contains(t, sortedKeys(composed.Workflows), "continuous-simplicity")
	assert.Contains(t, sortedKeys(composed.Workflows), "sota-gap-analysis")
	assert.Contains(t, sortedKeys(composed.Workflows), "unblock-wave")
	assert.Contains(t, sortedKeys(composed.Workflows), "diagnose-failures")
	assert.Contains(t, sortedKeys(composed.Workflows), "initiative-tracker")
	assert.Contains(t, sortedKeys(composed.Workflows), "backlog-refinement")
	assert.Contains(t, sortedKeys(composed.Workflows), "ingest-field-reports")
	assert.Contains(t, sortedKeys(composed.Prompts), "implement-harness/pr_draft")
	assert.Contains(t, sortedKeys(composed.Prompts), "continuous-improvement/verify")
	assert.Contains(t, sortedKeys(composed.Prompts), "hardening-audit/rank")
	assert.Contains(t, sortedKeys(composed.Prompts), "backlog-refinement/analyze")
	assert.Contains(t, sortedKeys(composed.Prompts), "backlog-refinement/report")
	assert.Contains(t, sortedKeys(composed.Sources), "harness-impl")
	assert.Contains(t, sortedKeys(composed.Sources), "harness-pr-lifecycle")
	assert.Contains(t, sortedKeys(composed.Sources), "continuous-improvement")
	assert.Contains(t, sortedKeys(composed.Sources), "continuous-simplicity")
	assert.Contains(t, sortedKeys(composed.Sources), "hardening-audit")
	assert.Contains(t, sortedKeys(composed.Sources), "sota-gap")
	assert.Contains(t, sortedKeys(composed.Sources), "initiative-tracker")
	assert.Contains(t, sortedKeys(composed.Sources), "backlog-refinement")
	assert.Contains(t, sortedKeys(composed.Sources), "ingest-field-reports")
	require.Len(t, composed.ConfigOverlays, 2)

	assert.Contains(t, sortedKeys(composed.Scripts), "post-discussion.sh")
	assert.Contains(t, joinOverlays(composed.ConfigOverlays), "concurrency:\n  global: 3\n  per_class:\n    backlog-refinement: 1")

	implementHarnessWorkflow := string(composed.Workflows["implement-harness"])
	assert.Contains(t, implementHarnessWorkflow, `--repo nicholls-inc/xylem`)
	assert.Contains(t, implementHarnessWorkflow, `--label "harness-impl"`)
	assert.Contains(t, implementHarnessWorkflow, `--label "ready-to-merge"`)
}

func TestSmoke_S3_SelfHostingProfileScaffoldsContinuousImprovementScheduledWorkflow(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	var source config.SourceConfig
	require.NoError(t, yaml.Unmarshal(composed.Sources["continuous-improvement"], &source))
	assert.Equal(t, "scheduled", source.Type)
	assert.Equal(t, "{{ .Repo }}", source.Repo)
	assert.Equal(t, "@daily", source.Schedule)
	require.Contains(t, source.Tasks, "daily-rotation")
	assert.Equal(t, "continuous-improvement", source.Tasks["daily-rotation"].Workflow)
	assert.Equal(t, "continuous-improvement", source.Tasks["daily-rotation"].Ref)

	var wf workflowpkg.Workflow
	require.NoError(t, yaml.Unmarshal(composed.Workflows["continuous-improvement"], &wf))
	assert.Equal(t, "continuous-improvement", wf.Name)
	assert.Equal(t, workflowpkg.ClassHarnessMaintenance, wf.Class)
	require.Len(t, wf.Phases, 5)
	assert.Equal(t, "select_focus", wf.Phases[0].Name)
	assert.Equal(t, "command", wf.Phases[0].Type)
	assert.Contains(t, wf.Phases[0].Run, "continuous-improvement select")
	assert.Equal(t, ".xylem/prompts/continuous-improvement/analyze.md", wf.Phases[1].PromptFile)
	assert.Equal(t, ".xylem/prompts/continuous-improvement/verify.md", wf.Phases[4].PromptFile)
	require.NotNil(t, wf.Phases[3].Gate)
	assert.Equal(t, "command", wf.Phases[3].Gate.Type)
	assert.Equal(t, 2, wf.Phases[3].Gate.Retries)
	assert.Contains(t, wf.Phases[3].Gate.Run, ".Validation.Format")
	assert.Contains(t, wf.Phases[3].Gate.Run, ".Validation.Test")

	assert.Contains(t, sortedKeys(composed.Prompts), "continuous-improvement/analyze")
	assert.Contains(t, sortedKeys(composed.Prompts), "continuous-improvement/plan")
	assert.Contains(t, sortedKeys(composed.Prompts), "continuous-improvement/implement")
	assert.Contains(t, sortedKeys(composed.Prompts), "continuous-improvement/verify")
}

func TestSmoke_S4_SelfHostingProfileScaffoldsMonthlyHardeningAuditWorkflow(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	var source config.SourceConfig
	require.NoError(t, yaml.Unmarshal(composed.Sources["hardening-audit"], &source))
	assert.Equal(t, "scheduled", source.Type)
	assert.Equal(t, "{{ .Repo }}", source.Repo)
	assert.Equal(t, "@monthly", source.Schedule)
	require.Contains(t, source.Tasks, "monthly-hardening-audit")
	assert.Equal(t, "hardening-audit", source.Tasks["monthly-hardening-audit"].Workflow)
	assert.Equal(t, "hardening-audit", source.Tasks["monthly-hardening-audit"].Ref)

	var wf workflowpkg.Workflow
	require.NoError(t, yaml.Unmarshal(composed.Workflows["hardening-audit"], &wf))
	assert.Equal(t, "hardening-audit", wf.Name)
	assert.Equal(t, workflowpkg.ClassHarnessMaintenance, wf.Class)
	require.Len(t, wf.Phases, 5)
	assert.Equal(t, "inventory", wf.Phases[0].Name)
	assert.Equal(t, "command", wf.Phases[0].Type)
	assert.Contains(t, wf.Phases[0].Run, "harden inventory")
	assert.Equal(t, "evaluate", wf.Phases[1].Name)
	assert.Equal(t, "command", wf.Phases[1].Type)
	assert.Contains(t, wf.Phases[1].Run, "harden score")
	assert.Equal(t, ".xylem/prompts/hardening-audit/rank.md", wf.Phases[2].PromptFile)
	assert.Equal(t, "file_issues", wf.Phases[3].Name)
	assert.Equal(t, "command", wf.Phases[3].Type)
	assert.Contains(t, wf.Phases[3].Run, "harden file-issues")
	assert.Equal(t, "track", wf.Phases[4].Name)
	assert.Contains(t, wf.Phases[4].Run, "docs/hardening-ledger.md")

	assert.Contains(t, sortedKeys(composed.Prompts), "hardening-audit/rank")
}

func TestSmoke_S5_SelfHostingProfileScaffoldsDailyBacklogRefinementWorkflow(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	var source config.SourceConfig
	require.NoError(t, yaml.Unmarshal(composed.Sources["backlog-refinement"], &source))
	assert.Equal(t, "scheduled", source.Type)
	assert.Equal(t, "{{ .Repo }}", source.Repo)
	assert.Equal(t, "@daily", source.Schedule)
	require.Contains(t, source.Tasks, "daily-backlog-refinement")
	assert.Equal(t, "backlog-refinement", source.Tasks["daily-backlog-refinement"].Workflow)
	assert.Equal(t, "backlog-refinement", source.Tasks["daily-backlog-refinement"].Ref)

	var wf workflowpkg.Workflow
	require.NoError(t, yaml.Unmarshal(composed.Workflows["backlog-refinement"], &wf))
	assert.Equal(t, "backlog-refinement", wf.Name)
	assert.Equal(t, workflowpkg.ClassHarnessMaintenance, wf.Class)
	require.Len(t, wf.Phases, 5)
	assert.Equal(t, "collect", wf.Phases[0].Name)
	assert.Equal(t, "command", wf.Phases[0].Type)
	assert.Contains(t, wf.Phases[0].Run, "gh issue list --repo nicholls-inc/xylem")
	assert.Contains(t, wf.Phases[0].Run, "gh pr list --repo nicholls-inc/xylem")
	assert.Contains(t, wf.Phases[0].Run, "labels?per_page=100")
	assert.NotNil(t, wf.Phases[0].NoOp)
	assert.Equal(t, "XYLEM_NOOP", wf.Phases[0].NoOp.Match)
	assert.Equal(t, ".xylem/prompts/backlog-refinement/analyze.md", wf.Phases[1].PromptFile)
	assert.Equal(t, ".xylem/prompts/backlog-refinement/report.md", wf.Phases[2].PromptFile)
	assert.Equal(t, "apply_actions", wf.Phases[3].Name)
	assert.Equal(t, "command", wf.Phases[3].Type)
	assert.Contains(t, wf.Phases[3].Run, "gh issue edit")
	assert.Contains(t, wf.Phases[3].Run, "gh issue comment")
	assert.Equal(t, "persist_summary", wf.Phases[4].Name)
	assert.Equal(t, "command", wf.Phases[4].Type)
	assert.Contains(t, wf.Phases[4].Run, "summary.md")

	assert.Contains(t, sortedKeys(composed.Prompts), "backlog-refinement/analyze")
	assert.Contains(t, sortedKeys(composed.Prompts), "backlog-refinement/report")
}

func TestAdaptRepoWorkflowAssetParsesCleanly(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	dir := t.TempDir()
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(oldWd))
	})

	for _, name := range []string{"plan.md", "apply.md", "pr.md"} {
		data, readErr := fs.ReadFile(profile.FS, filepath.Join("prompts", "adapt-repo", name))
		require.NoError(t, readErr)
		target := filepath.Join(dir, ".xylem", "prompts", "adapt-repo", name)
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, data, 0o644))
	}

	workflowData, err := fs.ReadFile(profile.FS, filepath.Join("workflows", "adapt-repo.yaml"))
	require.NoError(t, err)
	workflowPath := filepath.Join(dir, "adapt-repo.yaml")
	require.NoError(t, os.WriteFile(workflowPath, workflowData, 0o644))

	wf, err := workflowpkg.Load(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, "adapt-repo", wf.Name)
	assert.Equal(t, workflowpkg.ClassHarnessMaintenance, wf.Class)
	require.Len(t, wf.Phases, 7)
}

func TestAdaptRepoPromptAssetsEnforceGuardrails(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	planPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "adapt-repo", "plan.md"))
	require.NoError(t, err)
	assert.Contains(t, string(planPrompt), "Your only allowed write in this phase is `.xylem/state/bootstrap/adapt-plan.json`.")
	assert.Contains(t, string(planPrompt), `"schema_version": 1`)
	assert.Contains(t, string(planPrompt), "`planned_changes[].op` must be one of `patch`, `replace`, `create`, or `delete`.")
	assert.Contains(t, string(planPrompt), "`planned_changes[].path` must stay within `.xylem/`, `.xylem.yml`, `AGENTS.md`, or `docs/`.")

	applyPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "adapt-repo", "apply.md"))
	require.NoError(t, err)
	assert.Contains(t, string(applyPrompt), "Only edit files under `.xylem/`, `.xylem.yml`, `AGENTS.md`, or minimal `docs/` stubs.")
	assert.Contains(t, string(applyPrompt), "Do not use `git`, Bash, network tools, or package-install commands.")
	assert.Contains(t, string(applyPrompt), "Use the `Edit` tool only.")
	assert.Contains(t, string(applyPrompt), "If the validated plan results in no material file changes, print `XYLEM_NOOP`")

	prPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "adapt-repo", "pr.md"))
	require.NoError(t, err)
	assert.Contains(t, string(prPrompt), "Use the title `[xylem] adapt harness to this repository`.")
	assert.Contains(t, string(prPrompt), "Link the seeding issue and the bootstrap artifacts under `.xylem/state/bootstrap/`.")
	assert.Contains(t, string(prPrompt), "Inline every `planned_changes` entry from `adapt-plan.json`.")
	assert.Contains(t, string(prPrompt), "Inline every `skipped` entry from `adapt-plan.json`.")
	assert.Contains(t, string(prPrompt), "remain PR-gated")
}

func TestSmoke_S4_SecurityComplianceWorkflowBundleIsSeededInCoreProfile(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core")
	require.NoError(t, err)

	require.NotNil(t, composed)
	workflowAsset, ok := composed.Workflows["security-compliance"]
	require.True(t, ok)
	assert.Contains(t, string(workflowAsset), "name: security-compliance")
	assert.Contains(t, string(workflowAsset), "class: ops")

	scanPrompt, ok := composed.Prompts["security-compliance/scan_secrets"]
	require.True(t, ok)
	assert.Contains(t, string(scanPrompt), "RESULT: CLEAN | FINDINGS | TOOLING-GAP")

	synthesizePrompt, ok := composed.Prompts["security-compliance/synthesize"]
	require.True(t, ok)
	assert.Contains(t, string(synthesizePrompt), "ISSUES_CREATED:")

	sourceAsset, ok := composed.Sources["security-compliance"]
	require.True(t, ok)
	assert.Contains(t, string(sourceAsset), "type: schedule")
	assert.Contains(t, string(sourceAsset), "cadence: '@daily'")
	assert.Contains(t, string(sourceAsset), "workflow: security-compliance")
}

func TestSmoke_S5_SecurityComplianceWorkflowParsesAsFourPhaseAudit(t *testing.T) {
	profile, err := Load("core")
	require.NoError(t, err)

	dir := t.TempDir()
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(oldWd))
	})

	for _, name := range []string{"scan_secrets.md", "static_analysis.md", "dependency_audit.md", "synthesize.md"} {
		data, readErr := fs.ReadFile(profile.FS, filepath.Join("prompts", "security-compliance", name))
		require.NoError(t, readErr)
		target := filepath.Join(dir, ".xylem", "prompts", "security-compliance", name)
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, data, 0o644))
	}

	workflowData, err := fs.ReadFile(profile.FS, filepath.Join("workflows", "security-compliance.yaml"))
	require.NoError(t, err)
	workflowPath := filepath.Join(dir, "security-compliance.yaml")
	require.NoError(t, os.WriteFile(workflowPath, workflowData, 0o644))

	wf, err := workflowpkg.Load(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, "security-compliance", wf.Name)
	assert.Equal(t, workflowpkg.ClassOps, wf.Class)
	require.Len(t, wf.Phases, 4)
	assert.Equal(t, "scan_secrets", wf.Phases[0].Name)
	assert.Equal(t, "static_analysis", wf.Phases[1].Name)
	assert.Equal(t, "dependency_audit", wf.Phases[2].Name)
	assert.Equal(t, "synthesize", wf.Phases[3].Name)
	for i := 0; i < 3; i++ {
		require.NotNil(t, wf.Phases[i].Gate)
		assert.Equal(t, "command", wf.Phases[i].Gate.Type)
		assert.Contains(t, wf.Phases[i].Gate.Run, "test -s")
		assert.Contains(t, wf.Phases[i].Gate.Run, "grep -q '^RESULT:'")
		assert.Contains(t, wf.Phases[i].Gate.Run, "grep -q '^SEVERITY:'")
		assert.Contains(t, wf.Phases[i].Gate.Run, "grep -q '^TOOLS_RUN:'")
		assert.Contains(t, wf.Phases[i].Gate.Run, "grep -q '^SUMMARY:'")
		assert.Contains(t, wf.Phases[i].Gate.Run, "grep -q '^FINDINGS:'")
	}
	assert.Nil(t, wf.Phases[3].Gate)
}

func TestSmoke_S6_SecurityComplianceScheduledSourceUsesDailyCadence(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	configTemplate, err := fs.ReadFile(profile.FS, "xylem.yml.tmpl")
	require.NoError(t, err)

	assert.Contains(t, string(configTemplate), "security-compliance:\n    type: schedule\n    cadence: \"@daily\"\n    workflow: security-compliance")
}

func TestSmoke_S7_SecurityCompliancePromptsDocumentReportingContract(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	synthesizePrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "security-compliance", "synthesize.md"))
	require.NoError(t, err)
	assert.Contains(t, string(synthesizePrompt), "create or update GitHub issues labeled `security`")
	assert.Contains(t, string(synthesizePrompt), "REPORT_STATUS:")
	assert.Contains(t, string(synthesizePrompt), "ISSUES_CREATED:")
	assert.Contains(t, string(synthesizePrompt), "SLA_STATUS:")
	assert.Contains(t, string(synthesizePrompt), "ACTION_ITEMS:")

	scanPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "security-compliance", "scan_secrets.md"))
	require.NoError(t, err)
	assert.Contains(t, string(scanPrompt), "Stay read-only")
	assert.Contains(t, string(scanPrompt), "RESULT: CLEAN | FINDINGS | TOOLING-GAP")
	assert.Contains(t, string(scanPrompt), "SEVERITY: NONE | LOW | MEDIUM | HIGH | CRITICAL")
	assert.Contains(t, string(scanPrompt), "TOOLS_RUN:")

	staticPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "security-compliance", "static_analysis.md"))
	require.NoError(t, err)
	assert.Contains(t, string(staticPrompt), "RESULT: CLEAN | FINDINGS | TOOLING-GAP")
	assert.Contains(t, string(staticPrompt), "FOLLOW_UP:")

	dependencyPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "security-compliance", "dependency_audit.md"))
	require.NoError(t, err)
	assert.Contains(t, string(dependencyPrompt), "RESULT: CLEAN | FINDINGS | TOOLING-GAP")
	assert.Contains(t, string(dependencyPrompt), "open `security`-labeled issues")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
