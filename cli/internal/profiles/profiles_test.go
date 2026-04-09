package profiles

import (
	"io/fs"
	"sort"
	"strings"
	"testing"

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

func TestComposeCoreIncludesSeededAssets(t *testing.T) {
	composed, err := Compose("core")
	require.NoError(t, err)

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
	assert.Contains(t, string(composed.Workflows["fix-bug"]), `name: fix-bug`)
	assert.Contains(t, string(composed.Workflows["implement-feature"]), `name: implement-feature`)
	assert.Contains(t, string(composed.Prompts["fix-bug/pr"]), "publication boundary")
	assert.Contains(t, string(composed.ConfigOverlays[0]), `repo: "{{ .Repo }}"`)
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

func TestComposeUnknownProfile(t *testing.T) {
	composed, err := Compose("nonexistent")
	require.Error(t, err)
	assert.Nil(t, composed)
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

func TestComposeRejectsConflictingSourceKeys(t *testing.T) {
	composed, err := Compose("core", "core")
	require.Error(t, err)
	assert.Nil(t, composed)
	assert.Contains(t, err.Error(), `source "bugs" conflicts`)
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

func TestSmoke_S4_ComposeCoreAndSelfHostingXylemRejectsWorkflowConflicts(t *testing.T) {
	t.Parallel()

	composed, err := Compose("core", "self-hosting-xylem")
	require.Error(t, err)

	assert.Nil(t, composed)
	assert.Contains(t, err.Error(), `workflow "fix-bug" conflicts`)
	assert.Contains(t, err.Error(), "core")
	assert.Contains(t, err.Error(), "self-hosting-xylem")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
