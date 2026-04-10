package profiles

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLoadCore(t *testing.T) {
	profile, err := Load("core")
	require.NoError(t, err)

	assert.Equal(t, "core", profile.Name)
	assert.Equal(t, 1, profile.Version)

	data, err := fs.ReadFile(profile.FS, "HARNESS.md.tmpl")
	require.NoError(t, err)
	assert.Contains(t, string(data), "# Project Overview")
}

func TestLoadSelfHostingXylemReturnsExtractedAssets(t *testing.T) {
	profile, err := Load("self-hosting-xylem")
	require.NoError(t, err)

	assert.Equal(t, "self-hosting-xylem", profile.Name)
	assert.Equal(t, 1, profile.Version)

	overlay, err := fs.ReadFile(profile.FS, "xylem.overlay.yml")
	require.NoError(t, err)
	assert.Contains(t, string(overlay), "bugs:")
	assert.Contains(t, string(overlay), "features:")
	assert.Contains(t, string(overlay), "triage:")
	assert.Contains(t, string(overlay), "refinement:")
	assert.Contains(t, string(overlay), "harness-impl")
	assert.Contains(t, string(overlay), "auto_merge_repo: nicholls-inc/xylem")

	workflowData, err := fs.ReadFile(profile.FS, "workflows/implement-harness.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(workflowData), "name: implement-harness")

	promptData, err := fs.ReadFile(profile.FS, "prompts/implement-harness/analyze.md")
	require.NoError(t, err)
	assert.NotEmpty(t, promptData)
}

func TestSmoke_S1_LoadCoreProfileReturnsEmbeddedAssets(t *testing.T) {
	t.Parallel()

	profile, err := Load("core")
	require.NoError(t, err)

	require.NotNil(t, profile)
	assert.Equal(t, "core", profile.Name)
	assert.Equal(t, 1, profile.Version)

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

func TestValidateOverlayRejectsCoreAutomationFields(t *testing.T) {
	t.Parallel()

	for field := range coreForbiddenDaemonFields {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()

			overlay := fmt.Sprintf("daemon:\n  %s: true\n", field)

			err := validateOverlay("core", "xylem.yml.tmpl", []byte(overlay))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "compose profiles: core overlay")
			assert.Contains(t, err.Error(), "daemon."+field)
		})
	}
}

func TestSmoke_S2_ComposeCoreIncludesSeededWorkflowsAndTemplates(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core")
	require.NoError(t, err)

	require.NotNil(t, composed)
	require.Len(t, composed.Profiles, 1)
	assert.Equal(t, "core", composed.Profiles[0].Name)
	assert.Equal(t, 1, composed.Profiles[0].Version)

	assert.Equal(t, []string{"fix-bug", "implement-feature"}, sortedKeys(composed.Workflows))
	assert.Equal(t, []string{
		"fix-bug/analyze",
		"fix-bug/implement",
		"fix-bug/plan",
		"fix-bug/pr",
		"implement-feature/analyze",
		"implement-feature/implement",
		"implement-feature/plan",
		"implement-feature/pr",
	}, sortedKeys(composed.Prompts))
	assert.Equal(t, []string{"bugs"}, sortedKeys(composed.Sources))
	require.Len(t, composed.ConfigOverlays, 1)

	assert.Contains(t, string(composed.Workflows["fix-bug"]), "name: fix-bug")
	assert.Contains(t, string(composed.Workflows["implement-feature"]), "name: implement-feature")
	assert.Contains(t, string(composed.Prompts["fix-bug/pr"]), "publication boundary")
	assert.Contains(t, string(composed.ConfigOverlays[0]), `repo: "{{ .Repo }}"`)
}

func TestComposeUnknownProfileWrapsComposeContext(t *testing.T) {
	composed, err := Compose("nonexistent")
	require.Error(t, err)
	assert.Nil(t, composed)
	assert.Contains(t, err.Error(), `compose profiles: load profile "nonexistent"`)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "unknown profile")
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

func TestComposeAllowsLaterProfileToOverrideOverlaySources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		profiles    []string
		wantRepo    string
		wantExclude []string
	}{
		{
			name:        "self hosting overrides core",
			profiles:    []string{"core", "self-hosting-xylem"},
			wantRepo:    "nicholls-inc/xylem",
			wantExclude: []string{"wontfix", "duplicate", "in-progress", "no-bot", "harness-impl", "xylem-failed"},
		},
		{
			name:        "core can override self hosting when requested last",
			profiles:    []string{"self-hosting-xylem", "core"},
			wantRepo:    "{{ .Repo }}",
			wantExclude: []string{"wontfix", "duplicate", "in-progress", "no-bot"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			composed, err := Compose(tt.profiles...)
			require.NoError(t, err)

			bugs := decodeSourceConfig(t, composed.Sources["bugs"])
			assert.Equal(t, tt.wantRepo, bugs.Repo)
			assert.ElementsMatch(t, tt.wantExclude, bugs.Exclude)
		})
	}
}

func TestComposeRejectsDuplicateProfileSourceConflicts(t *testing.T) {
	composed, err := Compose("core", "core")
	require.Error(t, err)
	assert.Nil(t, composed)
	assert.Contains(t, err.Error(), `source "bugs" conflicts`)
	assert.True(t, strings.Count(err.Error(), `"core"`) >= 2, "expected both conflicting profiles in error: %q", err.Error())
}

func TestSmoke_S4_ComposeCoreAndSelfHostingXylemIncludesExtractedOverlay(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	require.NotNil(t, composed)
	require.Len(t, composed.Profiles, 2)
	assert.Equal(t, []string{
		"diagnose-failures",
		"fix-bug",
		"implement-feature",
		"implement-harness",
		"sota-gap-analysis",
		"unblock-wave",
	}, sortedKeys(composed.Workflows))
	assert.Equal(t, []string{
		"bugs",
		"conflict-resolution",
		"features",
		"harness-impl",
		"harness-merge",
		"harness-post-merge",
		"harness-pr-lifecycle",
		"refinement",
		"sota-gap",
		"triage",
	}, sortedKeys(composed.Sources))
	require.Len(t, composed.ConfigOverlays, 2)
	assert.Contains(t, string(composed.ConfigOverlays[1]), "auto_merge_reviewer")
	assert.Contains(t, string(composed.Workflows["implement-harness"]), "name: implement-harness")
	assert.Contains(t, string(composed.Prompts["implement-harness/analyze"]), "Analyze the following harness spec implementation issue.")
	assert.Contains(t, string(composed.Prompts["sota-gap-analysis/report"]), "Turn the current SoTA snapshot")

	bugs := decodeSourceConfig(t, composed.Sources["bugs"])
	require.Equal(t, "github", bugs.Type)
	assert.Equal(t, "nicholls-inc/xylem", bugs.Repo)
	assert.ElementsMatch(t, []string{"wontfix", "duplicate", "in-progress", "no-bot", "harness-impl", "xylem-failed"}, bugs.Exclude)
	require.Contains(t, bugs.Tasks, "fix-bugs")
	require.NotNil(t, bugs.Tasks["fix-bugs"].StatusLabels)
	assert.Equal(t, "xylem-failed", bugs.Tasks["fix-bugs"].StatusLabels.Failed)
	assert.Equal(t, "xylem-failed", bugs.Tasks["fix-bugs"].StatusLabels.TimedOut)

	triage := decodeSourceConfig(t, composed.Sources["triage"])
	require.Equal(t, "copilot", triage.LLM)
	assert.Equal(t, "gpt-5-mini", triage.Model)
}

func TestSmoke_S5_SelfHostingAssetsMatchLegacyCopies(t *testing.T) {
	t.Parallel()

	profile, err := Load("self-hosting-xylem")
	require.NoError(t, err)

	for _, assetPath := range append(selfHostingWorkflowAssetPaths(), selfHostingPromptAssetPaths()...) {
		assetPath := assetPath
		t.Run(assetPath, func(t *testing.T) {
			t.Parallel()

			got, err := fs.ReadFile(profile.FS, assetPath)
			require.NoError(t, err)

			want, err := os.ReadFile(repoAssetPath(assetPath))
			require.NoError(t, err)

			assert.Equal(t, string(want), string(got))
		})
	}
}

func TestSmoke_S6_SelfHostingOverlayPreservesXylemSourceParity(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	rootCfg := loadRootXylemConfig(t)
	sourceNames := []string{
		"bugs",
		"features",
		"triage",
		"refinement",
		"harness-impl",
		"harness-pr-lifecycle",
		"harness-merge",
		"conflict-resolution",
		"sota-gap",
		"harness-post-merge",
	}

	for _, sourceName := range sourceNames {
		sourceName := sourceName
		t.Run(sourceName, func(t *testing.T) {
			t.Parallel()

			got, ok := composed.Sources[sourceName]
			require.Truef(t, ok, "composed profile missing source %q", sourceName)

			want, ok := rootCfg.Sources[sourceName]
			require.Truef(t, ok, "root config missing source %q", sourceName)

			assert.Equal(t, normalizeYAMLValue(t, want), decodeYAMLAny(t, got))
		})
	}

	assert.Equal(t, map[string]any{
		"auto_upgrade":    true,
		"auto_merge":      true,
		"auto_merge_repo": "nicholls-inc/xylem",
	}, daemonFieldsSubset(rootCfg.Daemon, "auto_upgrade", "auto_merge", "auto_merge_repo"))

	assert.Contains(t, string(composed.ConfigOverlays[1]), `auto_merge_labels: [ready-to-merge, harness-impl]`)
	assert.Contains(t, string(composed.ConfigOverlays[1]), `auto_merge_branch_pattern: "^(feat|fix|chore)/issue-\\d+"`)
	assert.Contains(t, string(composed.ConfigOverlays[1]), `auto_merge_reviewer: "copilot-pull-request-reviewer"`)
}

func selfHostingWorkflowAssetPaths() []string {
	return []string{
		"workflows/diagnose-failures.yaml",
		"workflows/implement-harness.yaml",
		"workflows/sota-gap-analysis.yaml",
		"workflows/unblock-wave.yaml",
	}
}

func selfHostingPromptAssetPaths() []string {
	return []string{
		"prompts/diagnose-failures/create-issues.md",
		"prompts/diagnose-failures/diagnose.md",
		"prompts/diagnose-failures/refine.md",
		"prompts/implement-harness/analyze.md",
		"prompts/implement-harness/implement.md",
		"prompts/implement-harness/plan.md",
		"prompts/implement-harness/pr.md",
		"prompts/implement-harness/pr_draft.md",
		"prompts/implement-harness/smoke.md",
		"prompts/implement-harness/test_critic.md",
		"prompts/implement-harness/verify.md",
		"prompts/sota-gap-analysis/ingest.md",
		"prompts/sota-gap-analysis/report.md",
		"prompts/sota-gap-analysis/survey.md",
		"prompts/unblock-wave/check_deps.md",
	}
}

func repoAssetPath(profileAssetPath string) string {
	parts := strings.Split(profileAssetPath, "/")
	switch parts[0] {
	case "workflows":
		return filepath.Join("..", "..", "..", ".xylem", filepath.Join(parts...))
	case "prompts":
		return filepath.Join("..", "..", "..", ".xylem", filepath.Join(parts...))
	default:
		return filepath.Join("..", "..", "..", ".xylem", filepath.Join(parts...))
	}
}

type sourceConfigFixture struct {
	Type    string                       `yaml:"type"`
	Repo    string                       `yaml:"repo"`
	LLM     string                       `yaml:"llm"`
	Model   string                       `yaml:"model"`
	Exclude []string                     `yaml:"exclude"`
	Tasks   map[string]taskConfigFixture `yaml:"tasks"`
}

type taskConfigFixture struct {
	StatusLabels *statusLabelsFixture `yaml:"status_labels"`
}

type statusLabelsFixture struct {
	Failed   string `yaml:"failed"`
	TimedOut string `yaml:"timed_out"`
}

type rootXylemConfigFixture struct {
	Sources map[string]any `yaml:"sources"`
	Daemon  map[string]any `yaml:"daemon"`
}

func decodeSourceConfig(t *testing.T, data []byte) sourceConfigFixture {
	t.Helper()

	var src sourceConfigFixture
	require.NoError(t, yaml.Unmarshal(data, &src))
	return src
}

func loadRootXylemConfig(t *testing.T) rootXylemConfigFixture {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".xylem.yml"))
	require.NoError(t, err)

	var cfg rootXylemConfigFixture
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	return cfg
}

func decodeYAMLAny(t *testing.T, data []byte) any {
	t.Helper()

	var value any
	require.NoError(t, yaml.Unmarshal(data, &value))
	return value
}

func normalizeYAMLValue(t *testing.T, value any) any {
	t.Helper()

	data, err := yaml.Marshal(value)
	require.NoError(t, err)
	return decodeYAMLAny(t, data)
}

func daemonFieldsSubset(daemon map[string]any, fields ...string) map[string]any {
	subset := make(map[string]any, len(fields))
	for _, field := range fields {
		if value, ok := daemon[field]; ok {
			subset[field] = value
		}
	}
	return subset
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
