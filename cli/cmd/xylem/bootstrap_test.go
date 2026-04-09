package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmoke_S1_BootstrapHelpShowsSubcommands(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"bootstrap", "--help"})

	out := captureStdout(func() {
		require.NoError(t, cmd.Execute())
	})

	assert.Contains(t, out, "analyze-repo")
	assert.Contains(t, out, "audit-legibility")
}

func TestBootstrapAnalyzeRepoBypassesPersistentPreRunE(t *testing.T) {
	t.Setenv("PATH", "")
	root := t.TempDir()
	wantRoot, err := filepath.Abs(root)
	require.NoError(t, err)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"bootstrap", "analyze-repo", "--root", root})

	out := captureStdout(func() {
		require.NoError(t, cmd.Execute())
	})

	var got analyzeRepoOutput
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output:\n%s", out)

	assert.Equal(t, wantRoot, got.Root)
	assert.Empty(t, got.Languages)
	assert.Empty(t, got.EntryPoints)
	assert.NotEmpty(t, got.Dimensions)
}

func TestSmoke_S2_AnalyzeRepoWritesSchemaJSON(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), ".xylem", "state", "bootstrap", "repo.json")
	wantRoot, err := filepath.Abs(repoRoot(t))
	require.NoError(t, err)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"bootstrap", "analyze-repo", "--root", repoRoot(t), "--output", outputPath})

	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)

	var got analyzeRepoOutput
	require.NoError(t, json.Unmarshal(data, &got), "output:\n%s", string(data))
	_, err = time.Parse(timeFormatRFC3339, got.Timestamp)
	require.NoError(t, err)

	assert.Equal(t, wantRoot, got.Root)
	assert.Contains(t, got.Languages, "go")
	assert.Contains(t, got.EntryPoints, "cli/cmd/xylem")
	assert.NotEmpty(t, got.Frameworks)
	assert.NotEmpty(t, got.BuildTools)
	assert.NotEmpty(t, got.Dimensions)
	assert.Contains(t, got.Dimensions, "self_sufficiency")
	assert.Contains(t, got.Dimensions, "validation_harness")
}

func TestSmoke_S3_AuditLegibilityWritesValidJSON(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), ".xylem", "state", "bootstrap", "legibility.json")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"bootstrap", "audit-legibility", "--root", repoRoot(t), "--output", outputPath})

	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)

	var got auditLegibilityOutput
	require.NoError(t, json.Unmarshal(data, &got), "output:\n%s", string(data))
	_, err = time.Parse(timeFormatRFC3339, got.Timestamp)
	require.NoError(t, err)

	wantRoot, err := filepath.Abs(repoRoot(t))
	require.NoError(t, err)

	assert.Equal(t, wantRoot, got.Root)
	assert.GreaterOrEqual(t, got.Overall, 0.0)
	assert.LessOrEqual(t, got.Overall, 1.0)
	assert.Len(t, got.Dimensions, 7)

	foundValidationHarness := false
	for _, dim := range got.Dimensions {
		if dim.Name == "Validation Harness" {
			foundValidationHarness = true
			assert.NotEmpty(t, dim.Description)
			break
		}
	}
	assert.True(t, foundValidationHarness, "expected Validation Harness dimension in %v", got.Dimensions)
}

func TestSmoke_S4_RootHelpListsBootstrapSubcommand(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"--help"})

	out := captureStdout(func() {
		require.NoError(t, cmd.Execute())
	})

	assert.Contains(t, out, "bootstrap")
}
