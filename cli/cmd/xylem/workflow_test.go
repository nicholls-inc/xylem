package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmoke_S1_WorkflowValidateBypassesPersistentPreRunToolingChecks(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(oldWd))
	})

	configPath := filepath.Join(dir, ".xylem.yml")
	require.NoError(t, cmdInit(configPath, false))
	workflowPaths, err := discoverWorkflowPaths(filepath.Join(dir, defaultStateDir, "workflows"))
	require.NoError(t, err)
	require.NotEmpty(t, workflowPaths)

	t.Setenv("PATH", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--config", configPath, "workflow", "validate"})

	out := captureStdout(func() {
		require.NoError(t, cmd.Execute())
	})

	assert.Equal(t, fmt.Sprintf("Validated %d workflow(s).\n", len(workflowPaths)), out)
}

func TestSmoke_S2_WorkflowValidateMatchesDaemonReloadValidationError(t *testing.T) {
	rootDir, configPath, cfg := writeDaemonReloadRepo(t, "# Harness A\n")
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, ".xylem", "workflows", "fix-bug.yaml"), []byte("name: fix-bug\nphases:\n  - name: Bad\n"), 0o644))

	var stdout bytes.Buffer
	cmdErr := cmdWorkflowValidate(&stdout, cfg.StateDir, "")
	require.Error(t, cmdErr)

	_, reloadErr := validateDaemonReloadCandidate(rootDir, configPath)
	require.Error(t, reloadErr)

	assert.Equal(t, reloadErr.Error(), cmdErr.Error())
	assert.Empty(t, stdout.String())
}

func TestSmoke_S3_WorkflowValidateWalksDefaultWorkflowDirectoryAndReportsCoverage(t *testing.T) {
	stateDir := newWorkflowValidationStateDir(t)
	writeValidWorkflowFile(t, stateDir, "fix-bug")
	writeValidWorkflowFile(t, stateDir, "review")

	var stdout bytes.Buffer
	err := cmdWorkflowValidate(&stdout, stateDir, "")
	require.NoError(t, err)
	assert.Equal(t, "Validated 2 workflow(s).\n", stdout.String())
}

func TestSmoke_S4_WorkflowValidateProposedDeleteIgnoresDeletedBrokenWorkflow(t *testing.T) {
	stateDir := newWorkflowValidationStateDir(t)
	writeValidWorkflowFile(t, stateDir, "fix-bug")
	writeWorkflowFile(t, stateDir, "broken", strings.Join([]string{
		"name: broken",
		"phases:",
		"  - name: Bad",
		"",
	}, "\n"))
	planPath := writeAdaptPlanFile(t, filepath.Dir(stateDir), strings.Join([]string{
		"{",
		`  "schema_version": 1,`,
		`  "detected": {"languages": ["go"], "build_tools": ["go"], "test_runners": ["go test"], "linters": ["goimports"], "has_frontend": false, "has_database": false, "entry_points": ["cli/cmd/xylem"]},`,
		`  "planned_changes": [{"path": ".xylem/workflows/broken.yaml", "op": "delete", "rationale": "remove invalid workflow"}],`,
		`  "skipped": []`,
		"}",
	}, "\n"))

	var stdout bytes.Buffer
	err := cmdWorkflowValidate(&stdout, stateDir, planPath)
	require.NoError(t, err)
	assert.Equal(t, "Validated 1 workflow(s).\n", stdout.String())
}

func TestSmoke_S5_WorkflowValidateProposedRejectsWorkflowPatchWithoutContents(t *testing.T) {
	stateDir := newWorkflowValidationStateDir(t)
	writeValidWorkflowFile(t, stateDir, "fix-bug")
	planPath := writeAdaptPlanFile(t, filepath.Dir(stateDir), strings.Join([]string{
		"{",
		`  "schema_version": 1,`,
		`  "detected": {"languages": ["go"], "build_tools": ["go"], "test_runners": ["go test"], "linters": ["goimports"], "has_frontend": false, "has_database": false, "entry_points": ["cli/cmd/xylem"]},`,
		`  "planned_changes": [{"path": ".xylem/workflows/fix-bug.yaml", "op": "patch", "rationale": "update gate"}],`,
		`  "skipped": []`,
		"}",
	}, "\n"))

	err := cmdWorkflowValidate(ioDiscard{}, stateDir, planPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not include workflow contents")
}

func TestWorkflowValidateProposedRejectsMalformedPlan(t *testing.T) {
	stateDir := newWorkflowValidationStateDir(t)
	writeValidWorkflowFile(t, stateDir, "fix-bug")
	planPath := writeAdaptPlanFile(t, filepath.Dir(stateDir), strings.Join([]string{
		"{",
		`  "schema_version": 1,`,
		`  "detected": {"languages": ["go"], "build_tools": ["go"], "test_runners": ["go test"], "linters": ["goimports"], "has_frontend": false, "has_database": false, "entry_points": ["cli/cmd/xylem"]},`,
		`  "planned_changes": [],`,
		`  "unknown": true,`,
		`  "skipped": []`,
		"}",
	}, "\n"))

	err := cmdWorkflowValidate(ioDiscard{}, stateDir, planPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load proposed plan")
	assert.Contains(t, err.Error(), "unknown field")
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func newWorkflowValidationStateDir(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	stateDir := filepath.Join(root, ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "prompts"), 0o755))
	return stateDir
}

func writeValidWorkflowFile(t *testing.T, stateDir, name string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "prompts", name+".md"), []byte("Prompt\n"), 0o644))
	writeWorkflowFile(t, stateDir, name, strings.Join([]string{
		"name: " + name,
		"phases:",
		"  - name: plan",
		"    prompt_file: .xylem/prompts/" + name + ".md",
		"    max_turns: 1",
		"",
	}, "\n"))
}

func writeWorkflowFile(t *testing.T, stateDir, name, body string) {
	t.Helper()

	path := filepath.Join(stateDir, "workflows", name+".yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func writeAdaptPlanFile(t *testing.T, rootDir, body string) string {
	t.Helper()

	path := filepath.Join(rootDir, "adapt-plan.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}
