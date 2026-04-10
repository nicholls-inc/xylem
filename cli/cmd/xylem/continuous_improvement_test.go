package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/continuousimprovement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmoke_S2_SelectCommandWritesDefaultArtifactsAndSummary(t *testing.T) {
	cfg := &config.Config{StateDir: t.TempDir(), Repo: "owner/repo"}
	originalDeps := deps
	t.Cleanup(func() { deps = originalDeps })
	deps = &appDeps{cfg: cfg}

	original := selectContinuousImprovement
	t.Cleanup(func() { selectContinuousImprovement = original })
	selectContinuousImprovement = continuousimprovement.SelectAndPersist

	now := time.Date(2026, time.April, 10, 4, 0, 0, 0, time.UTC)
	out := captureStdout(func() {
		cmd := newContinuousImprovementSelectCmd()
		cmd.SetArgs([]string{"--now", now.Format(time.RFC3339)})
		require.NoError(t, cmd.Execute())
	})

	expectedStatePath := filepath.Join(cfg.StateDir, "state", "continuous-improvement", "state.json")
	expectedSelectionPath := filepath.Join(cfg.StateDir, "state", "continuous-improvement", "current-selection.json")
	assert.Contains(t, out, "Continuous Improvement Focus")
	assert.Contains(t, out, "owner/repo")
	assert.Contains(t, out, now.Format(time.RFC3339))

	loadedState, err := continuousimprovement.LoadState(expectedStatePath)
	require.NoError(t, err)
	assert.Equal(t, 1, loadedState.Runs)
	require.Len(t, loadedState.History, 1)
	assert.Equal(t, now.Format(time.RFC3339), loadedState.History[0].SelectedAt)

	selectionData, err := os.ReadFile(expectedSelectionPath)
	require.NoError(t, err)
	var persisted continuousimprovement.Selection
	require.NoError(t, json.Unmarshal(selectionData, &persisted))
	assert.Equal(t, "owner/repo", persisted.Repo)
	assert.Equal(t, now.Format(time.RFC3339), persisted.GeneratedAt)
	assert.Equal(t, expectedStatePath, persisted.StatePath)
	assert.Equal(t, expectedSelectionPath, persisted.SelectionPath)
	assert.NotEmpty(t, persisted.Focus.Key)
	assert.Contains(t, out, persisted.Focus.Key)
}

func TestCmdContinuousImprovementSelectPrintsSelectionSummary(t *testing.T) {
	cfg := &config.Config{StateDir: t.TempDir(), Repo: "owner/repo"}
	originalDeps := deps
	t.Cleanup(func() { deps = originalDeps })
	deps = &appDeps{cfg: cfg}

	customStatePath := filepath.Join(cfg.StateDir, "custom-state.json")
	customSelectionPath := filepath.Join(cfg.StateDir, "custom-selection.json")
	now := time.Date(2026, time.April, 10, 5, 6, 7, 0, time.UTC)

	original := selectContinuousImprovement
	t.Cleanup(func() { selectContinuousImprovement = original })
	var gotOpts continuousimprovement.Options
	selectContinuousImprovement = func(opts continuousimprovement.Options) (*continuousimprovement.Selection, error) {
		gotOpts = opts
		return &continuousimprovement.Selection{
			Version:       continuousimprovement.StateVersion,
			Repo:          opts.Repo,
			GeneratedAt:   opts.Now.Format(time.RFC3339),
			RotationSlot:  0,
			RotationGroup: continuousimprovement.GroupRepoSpecific,
			Focus: continuousimprovement.Focus{
				Key:     "workflow-prompts-and-gates",
				Title:   "Workflow prompts and gates",
				Group:   continuousimprovement.GroupRepoSpecific,
				Summary: "summary",
			},
			Reason:      "weighted repo-specific slot",
			Fingerprint: "cafebabe",
		}, nil
	}

	out := captureStdout(func() {
		cmd := newContinuousImprovementSelectCmd()
		cmd.SetArgs([]string{
			"--state", customStatePath,
			"--selection", customSelectionPath,
			"--now", now.Format(time.RFC3339),
		})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if gotOpts.Repo != cfg.Repo {
		t.Fatalf("Repo = %q, want %q", gotOpts.Repo, cfg.Repo)
	}
	if gotOpts.StatePath != customStatePath {
		t.Fatalf("StatePath = %q, want %q", gotOpts.StatePath, customStatePath)
	}
	if gotOpts.SelectionPath != customSelectionPath {
		t.Fatalf("SelectionPath = %q, want %q", gotOpts.SelectionPath, customSelectionPath)
	}
	if !gotOpts.Now.Equal(now) {
		t.Fatalf("Now = %s, want %s", gotOpts.Now.Format(time.RFC3339), now.Format(time.RFC3339))
	}
	if !strings.Contains(out, "Continuous Improvement Focus") {
		t.Fatalf("output = %q, want rendered selection summary", out)
	}
	if !strings.Contains(out, "workflow-prompts-and-gates") {
		t.Fatalf("output = %q, want focus key", out)
	}
	if !strings.Contains(out, now.Format(time.RFC3339)) {
		t.Fatalf("output = %q, want generated timestamp", out)
	}
}

func TestCmdContinuousImprovementSelectUsesDefaultPaths(t *testing.T) {
	cfg := &config.Config{StateDir: t.TempDir(), Repo: "owner/repo"}
	originalDeps := deps
	t.Cleanup(func() { deps = originalDeps })
	deps = &appDeps{cfg: cfg}

	original := selectContinuousImprovement
	t.Cleanup(func() { selectContinuousImprovement = original })
	selectContinuousImprovement = continuousimprovement.SelectAndPersist

	selection, err := cmdContinuousImprovementSelect("", "", time.Date(2026, time.April, 10, 4, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("cmdContinuousImprovementSelect() error = %v", err)
	}
	expectedStatePath := filepath.Join(cfg.StateDir, "state", "continuous-improvement", "state.json")
	expectedSelectionPath := filepath.Join(cfg.StateDir, "state", "continuous-improvement", "current-selection.json")
	if selection.StatePath != expectedStatePath {
		t.Fatalf("StatePath = %q, want %q", selection.StatePath, expectedStatePath)
	}
	if selection.SelectionPath != expectedSelectionPath {
		t.Fatalf("SelectionPath = %q, want %q", selection.SelectionPath, expectedSelectionPath)
	}
	if _, err := os.Stat(expectedStatePath); err != nil {
		t.Fatalf("default state file missing: %v", err)
	}
	if _, err := os.Stat(expectedSelectionPath); err != nil {
		t.Fatalf("default selection file missing: %v", err)
	}
}

func TestContinuousImprovementSelectBypassesToolLookup(t *testing.T) {
	t.Setenv("PATH", "")

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
repo: owner/repo
state_dir: ".xylem"
providers:
  claude:
    kind: claude
    command: "claude"
    flags: "--dangerously-skip-permissions"
    tiers:
      high: "claude-opus-4-6"
      med: "claude-sonnet-4-6"
      low: "claude-haiku-4-5"
    env:
      ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
  copilot:
    kind: copilot
    command: "copilot"
    flags: "--yolo --autopilot"
    tiers:
      high: "gpt-5.4"
      med: "gpt-5.2-codex"
      low: "gpt-5-mini"
    env:
      GITHUB_TOKEN: "${COPILOT_GITHUB_TOKEN}"
llm_routing:
  default_tier: med
  tiers:
    high:
      providers: [claude]
    med:
      providers: [claude, copilot]
    low:
      providers: [copilot, claude]
sources:
  ci:
    type: scheduled
    repo: owner/repo
    schedule: "@daily"
    tasks:
      daily:
        workflow: continuous-improvement
        ref: continuous-improvement
`), 0o644))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(orig))
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--config", configPath, "continuous-improvement", "select", "--now", "2026-04-10T04:00:00Z"})

	out := captureStdout(func() {
		require.NoError(t, cmd.Execute())
	})

	assert.Contains(t, out, "Continuous Improvement Focus")
	assert.FileExists(t, filepath.Join(dir, ".xylem", "state", "continuous-improvement", "state.json"))
	assert.FileExists(t, filepath.Join(dir, ".xylem", "state", "continuous-improvement", "current-selection.json"))
}
