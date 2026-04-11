package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/hardening"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCmdHardenInventoryUsesDefaultOutput(t *testing.T) {
	cfg := &config.Config{StateDir: filepath.Join(t.TempDir(), ".xylem")}
	originalDeps := deps
	t.Cleanup(func() { deps = originalDeps })
	deps = &appDeps{cfg: cfg}

	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "prompts", "hardening-audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "prompts", "hardening-audit", "rank.md"), []byte("Write exactly one JSON file."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "workflows", "hardening-audit.yaml"), []byte(`
name: hardening-audit
phases:
  - name: rank
    prompt_file: .xylem/prompts/hardening-audit/rank.md
    max_turns: 20
`), 0o644))

	inventory, err := cmdHardenInventory(repoRoot, ".xylem/workflows", "", time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.NotNil(t, inventory)
	assert.FileExists(t, filepath.Join(cfg.StateDir, "state", "hardening-audit", "inventory.json"))
}

func TestCmdHardenTrackAppendsLedger(t *testing.T) {
	repoRoot := t.TempDir()
	proposalsPath := filepath.Join(repoRoot, "proposals.json")
	filedPath := filepath.Join(repoRoot, "filed.json")
	require.NoError(t, hardening.WriteJSON(proposalsPath, []hardening.Proposal{{
		PhaseID:             "hardening-audit/rank",
		Workflow:            "hardening-audit",
		Phase:               "rank",
		Title:               "[harden] hardening-audit/rank",
		Body:                "body",
		CLISignature:        "xylem harden score",
		PackageLocation:     "cli/internal/hardening",
		EstimatedComplexity: "medium",
		TestCases:           []string{"writes scores"},
	}}))
	data, err := json.Marshal(hardening.FileResult{
		Created: []hardening.FiledIssue{{
			PhaseID: "hardening-audit/rank",
			Title:   "[harden] hardening-audit/rank",
			Number:  12,
			URL:     "https://github.com/owner/repo/issues/12",
			Created: true,
		}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filedPath, data, 0o644))

	err = cmdHardenTrack(repoRoot, proposalsPath, filedPath, "docs/hardening-ledger.md", time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(repoRoot, "docs", "hardening-ledger.md"))
}

func TestHardenInventoryBypassesToolLookup(t *testing.T) {
	t.Setenv("PATH", "")

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".xylem", "prompts", "hardening-audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".xylem", "prompts", "hardening-audit", "rank.md"), []byte("Write exactly one JSON file."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".xylem", "workflows", "hardening-audit.yaml"), []byte(`
name: hardening-audit
phases:
  - name: rank
    prompt_file: .xylem/prompts/hardening-audit/rank.md
    max_turns: 20
`), 0o644))
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
  copilot:
    kind: copilot
    command: "copilot"
    flags: "--yolo --autopilot"
    tiers:
      high: "gpt-5.4"
      med: "gpt-5.2-codex"
      low: "gpt-5-mini"
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
  hardening:
    type: scheduled
    repo: owner/repo
    schedule: "@monthly"
    tasks:
      monthly:
        workflow: hardening-audit
        ref: hardening-audit
`), 0o644))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(orig))
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--config", configPath, "harden", "inventory", "--repo-root", ".", "--workflow-dir", ".xylem/workflows"})

	out := captureStdout(func() {
		require.NoError(t, cmd.Execute())
	})
	assert.Contains(t, out, "Wrote")
	assert.FileExists(t, filepath.Join(dir, ".xylem", "state", "hardening-audit", "inventory.json"))
}
