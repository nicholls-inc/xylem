package profiles

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	workflowpkg "github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCore(t *testing.T) {
	profile, err := Load("core")
	require.NoError(t, err)

	assert.Equal(t, "core", profile.Name)
	assert.Equal(t, 1, profile.Version)

	data, err := fs.ReadFile(profile.FS, "HARNESS.md.tmpl")
	require.NoError(t, err)
	assert.Contains(t, string(data), "# Project Overview")

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

func TestComposeCoreIncludesSeededAssets(t *testing.T) {
	composed, err := Compose("core")
	require.NoError(t, err)

	require.NotNil(t, composed)
	require.Len(t, composed.Profiles, 1)
	assert.Equal(t, "core", composed.Profiles[0].Name)
	assert.Equal(t, 1, composed.Profiles[0].Version)

	assert.Equal(t, []string{"adapt-repo", "fix-bug", "implement-feature"}, sortedKeys(composed.Workflows))
	assert.Equal(t, []string{
		"adapt-repo/apply",
		"adapt-repo/plan",
		"adapt-repo/pr",
		"fix-bug/analyze",
		"fix-bug/implement",
		"fix-bug/plan",
		"fix-bug/pr",
		"implement-feature/analyze",
		"implement-feature/implement",
		"implement-feature/plan",
		"implement-feature/pr",
	}, sortedKeys(composed.Prompts))
	assert.Equal(t, []string{"adapt-repo", "bugs"}, sortedKeys(composed.Sources))
	require.Len(t, composed.ConfigOverlays, 1)
	assert.Contains(t, string(composed.Workflows["adapt-repo"]), `name: adapt-repo`)
	assert.Contains(t, string(composed.Workflows["adapt-repo"]), `class: harness-maintenance`)
	assert.Contains(t, string(composed.Workflows["fix-bug"]), `name: fix-bug`)
	assert.Contains(t, string(composed.Workflows["implement-feature"]), `name: implement-feature`)
	assert.Contains(t, string(composed.Prompts["adapt-repo/apply"]), "Use the `Edit` tool only")
	assert.Contains(t, string(composed.Prompts["adapt-repo/plan"]), "schema_version")
	assert.Contains(t, string(composed.Prompts["fix-bug/pr"]), "publication boundary")
	assert.Contains(t, string(composed.Sources["adapt-repo"]), "xylem-adapt-repo")
	assert.Contains(t, string(composed.Sources["adapt-repo"]), `workflow: adapt-repo`)
	assert.Contains(t, string(composed.ConfigOverlays[0]), `repo: "{{ .Repo }}"`)
}

func TestComposeUnknownProfile(t *testing.T) {
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

func TestComposeRejectsWorkflowConflictsAcrossProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profiles []string
	}{
		{
			name:     "core first",
			profiles: []string{"core", "self-hosting-xylem"},
		},
		{
			name:     "self hosting first",
			profiles: []string{"self-hosting-xylem", "core"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			composed, err := Compose(tt.profiles...)
			require.Error(t, err)
			assert.Nil(t, composed)
			assert.Contains(t, err.Error(), `workflow "fix-bug" conflicts`)
			for _, profileName := range tt.profiles {
				assert.Contains(t, err.Error(), profileName)
			}
		})
	}
}

func TestComposeProfileAppliesConfigOverlaysInFixedOrder(t *testing.T) {
	composed := &ComposedProfile{
		Workflows: make(map[string][]byte),
		Prompts:   make(map[string][]byte),
		Sources:   make(map[string][]byte),
	}
	workflowOwners := make(map[string]string)
	sourceOwners := make(map[string]string)

	profile := Profile{
		Name: "test-profile",
		FS: fstest.MapFS{
			"xylem.overlay.yml": &fstest.MapFile{Data: []byte("daemon:\n  log_level: debug\n")},
			"xylem.yml.tmpl":    &fstest.MapFile{Data: []byte("repo: '{{ .Repo }}'\n")},
		},
	}

	err := composeProfile(profile, composed, workflowOwners, sourceOwners)
	require.NoError(t, err)
	require.Len(t, composed.ConfigOverlays, 2)
	assert.Equal(t, "repo: '{{ .Repo }}'\n", string(composed.ConfigOverlays[0]))
	assert.Equal(t, "daemon:\n  log_level: debug\n", string(composed.ConfigOverlays[1]))
}

func TestComposeProfileKeepsFirstWorkflowOwnerForIdenticalContent(t *testing.T) {
	composed := &ComposedProfile{
		Workflows: make(map[string][]byte),
		Prompts:   make(map[string][]byte),
		Sources:   make(map[string][]byte),
	}
	workflowOwners := make(map[string]string)
	sourceOwners := make(map[string]string)

	first := Profile{
		Name: "first",
		FS: fstest.MapFS{
			"workflows/fix-bug.yaml": &fstest.MapFile{Data: []byte("name: fix-bug\nclass: one\n")},
		},
	}
	second := Profile{
		Name: "second",
		FS: fstest.MapFS{
			"workflows/fix-bug.yaml": &fstest.MapFile{Data: []byte("name: fix-bug\nclass: one\n")},
		},
	}
	third := Profile{
		Name: "third",
		FS: fstest.MapFS{
			"workflows/fix-bug.yaml": &fstest.MapFile{Data: []byte("name: fix-bug\nclass: two\n")},
		},
	}

	require.NoError(t, composeProfile(first, composed, workflowOwners, sourceOwners))
	require.NoError(t, composeProfile(second, composed, workflowOwners, sourceOwners))

	err := composeProfile(third, composed, workflowOwners, sourceOwners)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `workflow "fix-bug" conflicts between "first" and "third"`)
}

func TestSmoke_S1_AdaptRepoWorkflowAssetParsesCleanly(t *testing.T) {
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

func TestSmoke_S2_AdaptRepoPromptAssetsEnforceGuardrails(t *testing.T) {
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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
