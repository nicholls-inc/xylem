package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfigValidateFile(t *testing.T, dir, name, contents string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	return path
}

func writeConfigValidateAdaptPlanFile(t *testing.T, dir string, plan bootstrap.AdaptPlan) string {
	t.Helper()

	data, err := json.Marshal(plan)
	require.NoError(t, err)
	path := filepath.Join(dir, "adapt-plan.json")
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o644))
	return path
}

func TestSmoke_S1_ValidateBypassesPersistentPreRun(t *testing.T) {
	t.Setenv("PATH", "")

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(origWD))
	})

	require.NoError(t, cmdInit(configPath, false))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--config", configPath, "config", "validate"})

	out := captureStdout(func() {
		require.NoError(t, cmd.Execute())
	})

	assert.Contains(t, out, "Config valid.")
}

func TestSmoke_S2_RejectsEmptyAdaptRepoValidation(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigValidateFile(t, dir, ".xylem.yml", `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      adapt:
        labels: [bootstrap]
        workflow: adapt-repo
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)

	err := cmdConfigValidate(configPath, "", io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation: at least one of format, lint, build, or test must be set")
	assert.Contains(t, err.Error(), "adapt-repo")
}

func TestSmoke_S3_ProposedPlanAppliesValidationOverrides(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigValidateFile(t, dir, ".xylem.yml", `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      adapt:
        labels: [bootstrap]
        workflow: adapt-repo
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)
	planPath := writeConfigValidateAdaptPlanFile(t, dir, bootstrap.AdaptPlan{
		SchemaVersion: 1,
		Detected:      bootstrap.AdaptPlanDetected{},
		PlannedChanges: []bootstrap.AdaptPlanChange{{
			Path:        ".xylem.yml",
			Op:          "patch",
			Rationale:   "seed validation",
			DiffSummary: "validation.test: cd cli && go test ./...",
		}},
		Skipped: []bootstrap.AdaptPlanSkipped{},
	})

	require.NoError(t, cmdConfigValidate(configPath, planPath, io.Discard))
}

func TestSmoke_S4_RejectsUnknownPlanFields(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigValidateFile(t, dir, ".xylem.yml", `concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`)
	planPath := writeConfigValidateFile(t, dir, "adapt-plan.json", `{
  "schema_version": 1,
  "detected": {},
  "planned_changes": [],
  "skipped": [],
  "unexpected": true
}`)

	err := cmdConfigValidate(configPath, planPath, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestSmoke_S5_RejectsProfileLockDrift(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(origWD))
	})

	require.NoError(t, cmdInitWithProfile(configPath, false, "core"))

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	updated := strings.Replace(string(data), "profiles:\n    - core", "profiles:\n    - core\n    - self-hosting-xylem", 1)
	require.NoError(t, os.WriteFile(configPath, []byte(updated), 0o644))

	err = cmdConfigValidate(configPath, "", io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile.lock profiles")
}
