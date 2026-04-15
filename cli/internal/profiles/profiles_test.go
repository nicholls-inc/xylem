package profiles_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/policy"
	. "github.com/nicholls-inc/xylem/cli/internal/profiles"
	workflowpkg "github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type profileSourceConfig struct {
	Type     string                       `yaml:"type"`
	Repo     string                       `yaml:"repo,omitempty"`
	Schedule string                       `yaml:"schedule,omitempty"`
	Tasks    map[string]profileTaskConfig `yaml:"tasks,omitempty"`
}

type profileTaskConfig struct {
	Workflow string `yaml:"workflow,omitempty"`
	Ref      string `yaml:"ref,omitempty"`
}

func stageProfileWorkflowAsset(t *testing.T, profile *Profile, workflowName string, promptNames []string) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range promptNames {
		data, err := fs.ReadFile(profile.FS, filepath.Join("prompts", workflowName, name))
		require.NoError(t, err)

		target := filepath.Join(dir, ".xylem", "prompts", workflowName, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, data, 0o644))
	}

	workflowData, err := fs.ReadFile(profile.FS, filepath.Join("workflows", workflowName+".yaml"))
	require.NoError(t, err)

	workflowPath := filepath.Join(dir, workflowName+".yaml")
	require.NoError(t, os.WriteFile(workflowPath, workflowData, 0o644))
	return workflowPath
}

func TestSmoke_S1_LoadCoreProfileReturnsEmbeddedAssets(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	require.NotNil(t, profile)
	assert.Equal(t, "core", profile.Name)
	assert.Equal(t, 3, profile.Version)

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
	assert.Equal(t, 3, composed.Profiles[0].Version)

	assert.Equal(t, []string{
		"adapt-repo",
		"context-weight-audit",
		"doc-garden",
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
	assert.Contains(t, sortedKeys(composed.Prompts), "doc-garden/analyze")
	assert.Contains(t, sortedKeys(composed.Prompts), "fix-bug/implement_evaluator")
	assert.Contains(t, sortedKeys(composed.Prompts), "implement-feature/implement_evaluator")
	assert.Contains(t, sortedKeys(composed.Prompts), "security-compliance/synthesize")
	assert.Contains(t, sortedKeys(composed.Prompts), "workflow-health-report/report")
	assert.Contains(t, sortedKeys(composed.Sources), "doc-gardener")
	assert.Contains(t, sortedKeys(composed.Sources), "pr-lifecycle")
	assert.Contains(t, sortedKeys(composed.Sources), "security-compliance")
	assert.Contains(t, sortedKeys(composed.Sources), "field-report")
	require.Len(t, composed.ConfigOverlays, 1)

	assert.Contains(t, string(composed.Workflows["fix-bug"]), "name: fix-bug")
	assert.Contains(t, string(composed.Workflows["implement-feature"]), "name: implement-feature")
	assert.Contains(t, string(composed.Workflows["doc-garden"]), "name: doc-garden")
	assert.Contains(t, string(composed.Prompts["fix-bug/implement"]), "{{.Evaluation.Feedback}}")
	assert.Contains(t, string(composed.Prompts["implement-feature/implement"]), "{{.Evaluation.Feedback}}")
	assert.Contains(t, string(composed.Prompts["adapt-repo/pr"]), `--label "ready-to-merge"`)
	assert.Contains(t, string(composed.Prompts["fix-bug/pr"]), "Create a pull request")
	assert.Contains(t, string(composed.Prompts["fix-bug/pr"]), `--label "ready-to-merge"`)
	assert.Contains(t, string(composed.Prompts["implement-feature/pr"]), `--label "ready-to-merge"`)
	assert.Contains(t, string(composed.ConfigOverlays[0]), `repo: "{{ .Repo }}"`)

	var fixBug workflowpkg.Workflow
	require.NoError(t, yaml.Unmarshal(composed.Workflows["fix-bug"], &fixBug))
	require.Len(t, fixBug.Phases, 5)
	require.NotNil(t, fixBug.Phases[2].Evaluator)
	assert.Equal(t, ".xylem/prompts/fix-bug/implement_evaluator.md", fixBug.Phases[2].Evaluator.PromptFile)

	var mergePR workflowpkg.Workflow
	require.NoError(t, yaml.Unmarshal(composed.Workflows["merge-pr"], &mergePR))
	assert.Equal(t, policy.Ops, mergePR.Class)
	assert.Equal(t, 2, fixBug.Phases[2].Evaluator.MaxIterations)

	var implementFeature workflowpkg.Workflow
	require.NoError(t, yaml.Unmarshal(composed.Workflows["implement-feature"], &implementFeature))
	require.Len(t, implementFeature.Phases, 5)
	require.NotNil(t, implementFeature.Phases[2].Evaluator)
	assert.Equal(t, ".xylem/prompts/implement-feature/implement_evaluator.md", implementFeature.Phases[2].Evaluator.PromptFile)
	assert.Equal(t, 2, implementFeature.Phases[2].Evaluator.MaxIterations)
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
	assert.Contains(t, sortedKeys(composed.Workflows), "release-cadence")
	assert.Contains(t, sortedKeys(composed.Prompts), "implement-harness/pr_draft")
	assert.Contains(t, sortedKeys(composed.Prompts), "continuous-improvement/verify")
	assert.Contains(t, sortedKeys(composed.Prompts), "hardening-audit/rank")
	assert.Contains(t, sortedKeys(composed.Prompts), "backlog-refinement/analyze")
	assert.Contains(t, sortedKeys(composed.Prompts), "backlog-refinement/report")
	assert.Contains(t, sortedKeys(composed.Sources), "harness-pr-lifecycle")
	assert.Contains(t, sortedKeys(composed.Sources), "continuous-improvement")
	assert.Contains(t, sortedKeys(composed.Sources), "continuous-simplicity")
	assert.Contains(t, sortedKeys(composed.Sources), "hardening-audit")
	assert.Contains(t, sortedKeys(composed.Sources), "sota-gap")
	assert.Contains(t, sortedKeys(composed.Sources), "initiative-tracker")
	assert.Contains(t, sortedKeys(composed.Sources), "backlog-refinement")
	assert.Contains(t, sortedKeys(composed.Sources), "ingest-field-reports")
	assert.Contains(t, sortedKeys(composed.Sources), "release-cadence")
	require.Len(t, composed.ConfigOverlays, 2)

	assert.Contains(t, sortedKeys(composed.Scripts), "post-discussion.sh")
	overlays := joinOverlays(composed.ConfigOverlays)
	assert.Contains(t, overlays, "concurrency:\n  global: 3\n  per_class:\n    backlog-refinement: 1")
	assert.Contains(t, overlays, `auto_merge_labels: [ready-to-merge]`)
	assert.Contains(t, overlays, `auto_merge_branch_pattern: "^((feat|fix|chore)/issue-[0-9]+|release-please--.+)"`)
	assert.Contains(t, overlays, `auto_merge_reviewer: "copilot-pull-request-reviewer"`)

	implementHarnessWorkflow := string(composed.Workflows["implement-harness"])
	assert.Contains(t, implementHarnessWorkflow, `--repo nicholls-inc/xylem`)
	assert.Contains(t, implementHarnessWorkflow, `--label "harness-impl"`)
	assert.Contains(t, implementHarnessWorkflow, `--label "ready-to-merge"`)
}

func TestSmoke_S3_SelfHostingProfileScaffoldsContinuousImprovementScheduledWorkflow(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	var source profileSourceConfig
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

	var source profileSourceConfig
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

	var source profileSourceConfig
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

func TestSmoke_S6_SelfHostingProfileScaffoldsReleaseCadenceWorkflow(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	var source profileSourceConfig
	require.NoError(t, yaml.Unmarshal(composed.Sources["release-cadence"], &source))
	assert.Equal(t, "scheduled", source.Type)
	assert.Equal(t, "{{ .Repo }}", source.Repo)
	assert.Equal(t, "4h", source.Schedule)
	require.Contains(t, source.Tasks, "label-mature-release-pr")
	assert.Equal(t, "release-cadence", source.Tasks["label-mature-release-pr"].Workflow)
	assert.Equal(t, "release-cadence", source.Tasks["label-mature-release-pr"].Ref)

	var wf workflowpkg.Workflow
	require.NoError(t, yaml.Unmarshal(composed.Workflows["release-cadence"], &wf))
	assert.Equal(t, "release-cadence", wf.Name)
	assert.Equal(t, workflowpkg.ClassHarnessMaintenance, wf.Class)
	require.Len(t, wf.Phases, 1)
	assert.Equal(t, "label_ready", wf.Phases[0].Name)
	assert.Equal(t, "command", wf.Phases[0].Type)
	assert.Contains(t, wf.Phases[0].Run, "./cli/xylem release-cadence label-ready --repo {{ .Repo }}")
	require.NotNil(t, wf.Phases[0].NoOp)
	assert.Equal(t, "XYLEM_NOOP", wf.Phases[0].NoOp.Match)
}

func TestSmoke_S4_AdaptRepoWorkflowBundleIsSeededInCoreProfile(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core")
	require.NoError(t, err)

	workflowAsset, ok := composed.Workflows["adapt-repo"]
	require.True(t, ok)
	assert.Contains(t, string(workflowAsset), "name: adapt-repo")
	assert.Contains(t, string(workflowAsset), "class: harness-maintenance")
	assert.Contains(t, string(workflowAsset), "allow_additive_protected_writes: true")
	assert.Contains(t, string(workflowAsset), "xylem bootstrap analyze-repo --output .xylem/state/bootstrap/repo-analysis.json")
	assert.Contains(t, string(workflowAsset), "xylem validation run --from-config")

	planPrompt, ok := composed.Prompts["adapt-repo/plan"]
	require.True(t, ok)
	assert.Contains(t, string(planPrompt), ".xylem/state/bootstrap/adapt-plan.json")
	assert.Contains(t, string(planPrompt), `"schema_version": 1`)

	applyPrompt, ok := composed.Prompts["adapt-repo/apply"]
	require.True(t, ok)
	assert.Contains(t, string(applyPrompt), "Use the `Edit` tool only.")

	prPrompt, ok := composed.Prompts["adapt-repo/pr"]
	require.True(t, ok)
	assert.Contains(t, string(prPrompt), `[xylem] adapt harness to this repository`)
	assert.Contains(t, string(prPrompt), `--label "ready-to-merge"`)
}

func TestSmoke_S5_AdaptRepoWorkflowParsesAsSevenPhaseHarnessMaintenanceWorkflow(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	workflowPath := stageProfileWorkflowAsset(t, profile, "adapt-repo", []string{"plan.md", "apply.md", "pr.md"})
	wf, err := workflowpkg.Load(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, "adapt-repo", wf.Name)
	assert.Equal(t, workflowpkg.ClassHarnessMaintenance, wf.Class)
	assert.True(t, wf.AllowAdditiveProtectedWrites)
	require.Len(t, wf.Phases, 7)

	assert.Equal(t, "analyze", wf.Phases[0].Name)
	assert.Equal(t, "command", wf.Phases[0].Type)
	assert.Contains(t, wf.Phases[0].Run, "xylem bootstrap analyze-repo")
	assert.Contains(t, wf.Phases[0].Run, ".xylem/state/bootstrap/repo-analysis.json")

	assert.Equal(t, "legibility", wf.Phases[1].Name)
	assert.Equal(t, "command", wf.Phases[1].Type)
	assert.Contains(t, wf.Phases[1].Run, "xylem bootstrap audit-legibility")
	assert.Contains(t, wf.Phases[1].Run, ".xylem/state/bootstrap/legibility-report.json")

	assert.Equal(t, "plan", wf.Phases[2].Name)
	assert.Equal(t, ".xylem/prompts/adapt-repo/plan.md", wf.Phases[2].PromptFile)
	require.NotNil(t, wf.Phases[2].NoOp)
	assert.Equal(t, "XYLEM_NOOP", wf.Phases[2].NoOp.Match)

	assert.Equal(t, "validate", wf.Phases[3].Name)
	assert.Equal(t, "command", wf.Phases[3].Type)
	assert.Contains(t, wf.Phases[3].Run, "xylem config validate --proposed .xylem/state/bootstrap/adapt-plan.json")
	assert.Contains(t, wf.Phases[3].Run, "xylem workflow validate --proposed .xylem/state/bootstrap/adapt-plan.json")

	assert.Equal(t, "apply", wf.Phases[4].Name)
	assert.Equal(t, ".xylem/prompts/adapt-repo/apply.md", wf.Phases[4].PromptFile)
	require.NotNil(t, wf.Phases[4].AllowedTools)
	assert.Equal(t, "Edit", *wf.Phases[4].AllowedTools)

	assert.Equal(t, "verify", wf.Phases[5].Name)
	assert.Equal(t, "command", wf.Phases[5].Type)
	assert.Equal(t, "xylem validation run --from-config", wf.Phases[5].Run)

	assert.Equal(t, "pr", wf.Phases[6].Name)
	assert.Equal(t, ".xylem/prompts/adapt-repo/pr.md", wf.Phases[6].PromptFile)
}

func TestSmoke_S6_AdaptRepoPromptsEnforceBootstrapAndMergeReadyContracts(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	planPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "adapt-repo", "plan.md"))
	require.NoError(t, err)
	assert.Contains(t, string(planPrompt), "Your only allowed write in this phase is `.xylem/state/bootstrap/adapt-plan.json`.")
	assert.Contains(t, string(planPrompt), `"schema_version": 1`)
	assert.Contains(t, string(planPrompt), "`planned_changes[].op` must be one of `patch`, `replace`, `create`, or `delete`.")
	assert.Contains(t, string(planPrompt), "`planned_changes[].path` must stay within `.xylem/`, `.xylem.yml`, `AGENTS.md`, or `docs/`.")
	assert.Contains(t, string(planPrompt), "Fail closed")

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
	assert.Contains(t, string(prPrompt), `--label "ready-to-merge"`)
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

func TestSmoke_S4_DocGardenWorkflowBundleIsSeededInCoreProfile(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core")
	require.NoError(t, err)

	workflowAsset, ok := composed.Workflows["doc-garden"]
	require.True(t, ok)
	assert.Contains(t, string(workflowAsset), "name: doc-garden")
	assert.Contains(t, string(workflowAsset), "class: harness-maintenance")

	analyzePrompt, ok := composed.Prompts["doc-garden/analyze"]
	require.True(t, ok)
	assert.Contains(t, string(analyzePrompt), "XYLEM_NOOP")
	assert.Contains(t, string(analyzePrompt), "cheap heuristics")

	verifyPrompt, ok := composed.Prompts["doc-garden/verify"]
	require.True(t, ok)
	assert.Contains(t, string(verifyPrompt), "current checked-in defaults and behavior")

	sourceAsset, ok := composed.Sources["doc-gardener"]
	require.True(t, ok)
	assert.Contains(t, string(sourceAsset), "type: schedule")
	assert.Contains(t, string(sourceAsset), "cadence: '@daily'")
	assert.Contains(t, string(sourceAsset), "workflow: doc-garden")
}

func TestSmoke_S5_DocGardenWorkflowParsesAsFourPhaseMaintenanceWorkflow(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	workflowPath := stageProfileWorkflowAsset(t, profile, "doc-garden", []string{"analyze.md", "implement.md", "verify.md", "pr.md"})
	wf, err := workflowpkg.Load(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, "doc-garden", wf.Name)
	assert.Equal(t, workflowpkg.ClassHarnessMaintenance, wf.Class)
	require.Len(t, wf.Phases, 4)
	assert.Equal(t, "analyze", wf.Phases[0].Name)
	assert.Equal(t, ".xylem/prompts/doc-garden/analyze.md", wf.Phases[0].PromptFile)
	require.NotNil(t, wf.Phases[0].NoOp)
	assert.Equal(t, "XYLEM_NOOP", wf.Phases[0].NoOp.Match)
	assert.Equal(t, "implement", wf.Phases[1].Name)
	assert.Equal(t, "verify", wf.Phases[2].Name)
	assert.Equal(t, "pr", wf.Phases[3].Name)
	assert.Equal(t, ".xylem/prompts/doc-garden/pr.md", wf.Phases[3].PromptFile)
}

func TestSmoke_S6_DocGardenPromptsDocumentHeuristicAndPRContract(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	analyzePrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "doc-garden", "analyze.md"))
	require.NoError(t, err)
	assert.Contains(t, string(analyzePrompt), "This is a recurring documentation-maintenance vessel")
	assert.Contains(t, string(analyzePrompt), "cheap heuristics")
	assert.Contains(t, string(analyzePrompt), "CANDIDATE_FILES:")
	assert.Contains(t, string(analyzePrompt), "XYLEM_NOOP")

	implementPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "doc-garden", "implement.md"))
	require.NoError(t, err)
	assert.Contains(t, string(implementPrompt), "Prefer documentation-only changes.")
	assert.Contains(t, string(implementPrompt), "Do not change production behavior just to match stale docs.")

	prPrompt, err := fs.ReadFile(profile.FS, filepath.Join("prompts", "doc-garden", "pr.md"))
	require.NoError(t, err)
	assert.Contains(t, string(prPrompt), "scheduled `doc-garden` workflow")
	assert.Contains(t, string(prPrompt), "[xylem] refresh repository documentation")
	assert.Contains(t, string(prPrompt), "{{.Vessel.Ref}}")
}

func TestSmoke_S7_DocGardenScheduledSourceUsesDailyCadence(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	configTemplate, err := fs.ReadFile(profile.FS, "xylem.yml.tmpl")
	require.NoError(t, err)

	assert.Contains(t, string(configTemplate), "doc-gardener:\n    type: schedule\n    cadence: \"@daily\"\n    workflow: doc-garden")
}

func TestSmoke_S5_SecurityComplianceWorkflowParsesAsFourPhaseAudit(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	workflowPath := stageProfileWorkflowAsset(t, profile, "security-compliance", []string{"scan_secrets.md", "static_analysis.md", "dependency_audit.md", "synthesize.md"})
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

func TestSmoke_S6_ResolveConflictsWorkflowResyncsHeadBeforeMerge(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core")
	require.NoError(t, err)

	workflowData, ok := composed.Workflows["resolve-conflicts"]
	require.True(t, ok)

	var wf workflowpkg.Workflow
	require.NoError(t, yaml.Unmarshal(workflowData, &wf))
	require.Len(t, wf.Phases, 4)

	mergeMainPhase := wf.Phases[0]
	assert.Equal(t, "merge_main", mergeMainPhase.Name)
	assert.Equal(t, "command", mergeMainPhase.Type)
	assert.Contains(t, mergeMainPhase.Run, "head_branch=\"$(gh pr view {{ .Issue.Number }} --json headRefName --jq '.headRefName')\"")
	assert.Contains(t, mergeMainPhase.Run, "git fetch origin \"$head_branch\" main")
	assert.Contains(t, mergeMainPhase.Run, "git branch -D \"$head_branch\" >/dev/null 2>&1 || true")
	assert.Contains(t, mergeMainPhase.Run, "git reset --hard \"origin/$head_branch\"")
	assert.Contains(t, mergeMainPhase.Run, "git merge origin/main --no-commit --no-ff")
	require.NotNil(t, mergeMainPhase.NoOp)
	assert.Equal(t, "XYLEM_NOOP", mergeMainPhase.NoOp.Match)

	analyzePrompt, ok := composed.Prompts["resolve-conflicts/analyze"]
	require.True(t, ok)
	assert.Contains(t, string(analyzePrompt), "{{.PreviousOutputs.merge_main}}")
	assert.Contains(t, string(analyzePrompt), "Do not rerun the merge.")

	resolvePrompt, ok := composed.Prompts["resolve-conflicts/resolve"]
	require.True(t, ok)
	assert.Contains(t, string(resolvePrompt), "{{.PreviousOutputs.merge_main}}")
	assert.Contains(t, string(resolvePrompt), "complete the in-progress merge before validation")

	pushPrompt, ok := composed.Prompts["resolve-conflicts/push"]
	require.True(t, ok)
	assert.Contains(t, string(pushPrompt), "Push the already-completed merge resolution")
	assert.NotContains(t, string(pushPrompt), "git add -A && git commit")
}

func TestSmoke_S7_SecurityComplianceScheduledSourceUsesDailyCadence(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	configTemplate, err := fs.ReadFile(profile.FS, "xylem.yml.tmpl")
	require.NoError(t, err)

	assert.Contains(t, string(configTemplate), "security-compliance:\n    type: schedule\n    cadence: \"@daily\"\n    workflow: security-compliance")
}

func TestSmoke_S8_SecurityCompliancePromptsDocumentReportingContract(t *testing.T) {
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

func TestAllEmbeddedWorkflowsValidate(t *testing.T) {
	t.Parallel()

	profileNames := []string{"core", "self-hosting-xylem"}
	for _, profileName := range profileNames {
		composed, err := Compose(profileName)
		require.NoError(t, err, "Compose(%q)", profileName)

		for name, data := range composed.Workflows {
			name, data := name, data
			t.Run(profileName+"/"+name, func(t *testing.T) {
				t.Parallel()
				_, err := workflowpkg.LoadFromBytes(name, data)
				assert.NoError(t, err)
			})
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
