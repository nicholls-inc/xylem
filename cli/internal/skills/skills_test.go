package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmoke_S1_DiscoverProjectSkillsReturnsExpectedSuite(t *testing.T) {
	t.Parallel()

	definitions, err := Discover(projectRoot(t))
	require.NoError(t, err)

	assert.Equal(t, []string{
		"xylem-cancel",
		"xylem-config",
		"xylem-dashboard",
		"xylem-debug",
		"xylem-doctor",
		"xylem-drain",
		"xylem-enqueue",
		"xylem-init",
		"xylem-logs",
		"xylem-prompts",
		"xylem-retry",
		"xylem-status",
		"xylem-workflow",
	}, definitionNames(definitions))
}

func TestSmoke_S2_ProjectSkillsMapToRealXylemSurfaces(t *testing.T) {
	t.Parallel()

	definitions, err := Discover(projectRoot(t))
	require.NoError(t, err)

	byName := make(map[string]Definition, len(definitions))
	for _, def := range definitions {
		byName[def.Name] = def
		assert.Contains(t, def.Description, "Use when")
	}

	expectedSnippets := map[string][]string{
		"xylem-status":    {"xylem status", "--json"},
		"xylem-debug":     {"xylem status --json", ".xylem/phases"},
		"xylem-config":    {".xylem.yml", "xylem scan --dry-run"},
		"xylem-workflow":  {".xylem/workflows", ".xylem/prompts"},
		"xylem-prompts":   {".xylem/prompts", "workflow YAML"},
		"xylem-init":      {"xylem init", ".xylem/HARNESS.md"},
		"xylem-enqueue":   {"xylem enqueue", "--workflow"},
		"xylem-drain":     {"xylem drain", "xylem status"},
		"xylem-retry":     {"xylem retry", ".xylem/phases/<vessel-id>/summary.json"},
		"xylem-cancel":    {"xylem cancel", "pending or running vessel"},
		"xylem-logs":      {".xylem/phases/<vessel-id>", "summary.json"},
		"xylem-dashboard": {"xylem status --json", "summary.json"},
		"xylem-doctor":    {"xylem status", "xylem visualize"},
	}

	for name, snippets := range expectedSnippets {
		def, ok := byName[name]
		require.Truef(t, ok, "missing skill %q", name)
		for _, snippet := range snippets {
			assert.Containsf(t, def.Body, snippet, "skill %s should reference %q", name, snippet)
		}
	}
}

func TestSmoke_S3_ProjectSkillsStayManualAndArgumented(t *testing.T) {
	t.Parallel()

	definitions, err := Discover(projectRoot(t))
	require.NoError(t, err)

	withArguments := map[string]bool{
		"xylem-cancel":    true,
		"xylem-config":    true,
		"xylem-dashboard": true,
		"xylem-debug":     true,
		"xylem-doctor":    true,
		"xylem-enqueue":   true,
		"xylem-logs":      true,
		"xylem-prompts":   true,
		"xylem-retry":     true,
		"xylem-status":    true,
		"xylem-workflow":  true,
	}

	for _, def := range definitions {
		assert.Truef(t, def.DisableModelInvocation, "skill %s should remain slash-command driven", def.Name)
		assert.Equalf(t, def.Directory, def.Name, "skill %s should keep its slash-command name aligned with its directory", def.Name)
		if withArguments[def.Name] {
			assert.NotEmptyf(t, def.ArgumentHint, "skill %s should advertise its arguments", def.Name)
		}
	}
}

func TestParseRejectsInvalidName(t *testing.T) {
	t.Parallel()

	_, err := Parse(filepath.Join("repo", filepath.FromSlash(DirName), "Bad_Name", EntryFile), []byte(`---
name: Bad_Name
description: bad
---

body
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid skill name")
}

func TestParseRejectsMismatchedDirectoryAndName(t *testing.T) {
	t.Parallel()

	_, err := Parse(filepath.Join("repo", filepath.FromSlash(DirName), "xylem-status", EntryFile), []byte(`---
name: xylem-dashboard
description: Use when checking status.
---

body
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match name")
}

func TestParseRejectsDescriptionWithoutUseWhen(t *testing.T) {
	t.Parallel()

	_, err := Parse(filepath.Join("repo", filepath.FromSlash(DirName), "xylem-status", EntryFile), []byte(`---
name: xylem-status
description: Status skill.
---

body
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `description must include "Use when"`)
}

func TestDiscoverReturnsMissingSkillFileError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(DirName), "xylem-status"), 0o755))

	_, err := Discover(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), EntryFile)
}

func projectRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func definitionNames(definitions []Definition) []string {
	names := make([]string, 0, len(definitions))
	for _, def := range definitions {
		names = append(names, def.Name)
	}
	return names
}
