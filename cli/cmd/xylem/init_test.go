package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/profiles"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var expectedCoreWorkflows = []string{
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
}

var expectedSelfHostingWorkflows = []string{
	"audit",
	"backlog-refinement",
	"continuous-improvement",
	"continuous-simplicity",
	"diagnose-failures",
	"hardening-audit",
	"implement-harness",
	"ingest-field-reports",
	"initiative-tracker",
	"metrics-collector",
	"portfolio-analyst",
	"release-cadence",
	"sota-gap-analysis",
	"unblock-wave",
}

func TestInitCreatesConfigAndStateDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	stateDir := filepath.Join(dir, ".xylem")

	// Temporarily change to temp dir so defaultStateDir resolves there
	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// Config file created
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("config file not created: %v", err)
	}

	// State directory created
	if _, err := os.Stat(stateDir); err != nil {
		t.Errorf("state dir not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "state")); err != nil {
		t.Errorf("runtime state dir not created: %v", err)
	}

	// .gitignore created
	gitignore := filepath.Join(stateDir, ".gitignore")
	data, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatalf("gitignore not created: %v", err)
	}
	if string(data) != scaffoldGitignore {
		t.Errorf("unexpected gitignore content: %q", string(data))
	}

	if _, err := os.Stat(filepath.Join(stateDir, "profile.lock")); err != nil {
		t.Errorf("profile.lock not created: %v", err)
	}

}

func TestInitIdempotentWithoutForce(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Write existing config
	existing := "existing: true\n"
	os.WriteFile(configPath, []byte(existing), 0o644) //nolint:errcheck

	out := captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// Config preserved
	data, _ := os.ReadFile(configPath)
	if string(data) != existing {
		t.Errorf("config was overwritten, got: %s", string(data))
	}

	if !strings.Contains(out, "already exists") {
		t.Errorf("expected 'already exists' message, got: %s", out)
	}

	// State dir still created
	stateDir := filepath.Join(dir, ".xylem")
	if _, err := os.Stat(stateDir); err != nil {
		t.Errorf("state dir not created: %v", err)
	}
}

func TestInitForceOverwritesConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Write existing config
	os.WriteFile(configPath, []byte("old: true\n"), 0o644) //nolint:errcheck

	out := captureStdout(func() {
		err := cmdInit(configPath, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	data, _ := os.ReadFile(configPath)
	if strings.Contains(string(data), "old: true") {
		t.Errorf("config was not overwritten")
	}
	if !strings.Contains(string(data), "sources:") {
		t.Errorf("expected scaffold config, got: %s", string(data))
	}

	if !strings.Contains(out, "Created") {
		t.Errorf("expected 'Created' in output, got: %s", out)
	}
}

func TestInitStateDirAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	stateDir := filepath.Join(dir, ".xylem")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Pre-create state dir with a file
	os.MkdirAll(stateDir, 0o755)                                                    //nolint:errcheck
	os.WriteFile(filepath.Join(stateDir, "queue.jsonl"), []byte("existing"), 0o644) //nolint:errcheck

	err := cmdInit(configPath, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Existing file preserved
	data, _ := os.ReadFile(filepath.Join(stateDir, "queue.jsonl"))
	if string(data) != "existing" {
		t.Errorf("existing file in state dir was modified")
	}
}

func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"SSH", "git@github.com:owner/repo.git", "owner/repo"},
		{"SSH no .git", "git@github.com:owner/repo", "owner/repo"},
		{"HTTPS", "https://github.com/owner/repo.git", "owner/repo"},
		{"HTTPS no .git", "https://github.com/owner/repo", "owner/repo"},
		{"ssh protocol", "ssh://git@github.com/owner/repo.git", "owner/repo"},
		{"ssh protocol no .git", "ssh://git@github.com/owner/repo", "owner/repo"},
		{"non-GitHub SSH", "git@gitlab.com:owner/repo.git", ""},
		{"non-GitHub HTTPS", "https://gitlab.com/owner/repo.git", ""},
		{"malformed", "not-a-url", ""},
		{"empty", "", ""},
		{"with trailing newline", "git@github.com:owner/repo.git\n", "owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGitHubRepo(tt.input)
			if got != tt.expected {
				t.Errorf("parseGitHubRepo(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestInitScaffoldConfigLoads(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		if err := cmdInit(configPath, false); err != nil {
			t.Fatalf("cmdInit failed: %v", err)
		}
	})

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("scaffold config failed to load: %v", err)
	}
	if len(cfg.Sources) == 0 {
		t.Error("expected at least one source in scaffold config")
	}

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "profiles:")
	assert.Contains(t, string(data), "- core")
	assert.Contains(t, string(data), "# validation:")
	assert.Equal(t, []string{"core"}, cfg.Profiles)
}

func TestInitRespectsConfigFlag(t *testing.T) {
	dir := t.TempDir()
	customPath := filepath.Join(dir, "custom.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--config", customPath, "init"})

	captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("init with --config failed: %v", err)
		}
	})

	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("custom config not created at %s: %v", customPath, err)
	}

	// Default path should NOT exist
	if _, err := os.Stat(filepath.Join(dir, ".xylem.yml")); err == nil {
		t.Error(".xylem.yml was created despite --config pointing elsewhere")
	}
}

func TestResolveProfiles(t *testing.T) {
	profiles, err := resolveProfiles("core,self-hosting-xylem")
	require.NoError(t, err)
	assert.Equal(t, []string{"core", "self-hosting-xylem"}, profiles)
}

func TestSyncProfileAssetsWritesMissingWorkflowAndPromptFiles(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	composed := &profiles.ComposedProfile{
		Workflows: map[string][]byte{
			"merge-pr": []byte("run: gh pr merge\n"),
		},
		Prompts: map[string][]byte{
			"merge-pr/check": []byte("review the merge preconditions\n"),
		},
	}

	require.NoError(t, syncProfileAssets(stateDir, composed, false))

	workflowData, err := os.ReadFile(filepath.Join(stateDir, "workflows", "merge-pr.yaml"))
	require.NoError(t, err)
	assert.Equal(t, composed.Workflows["merge-pr"], workflowData)

	promptData, err := os.ReadFile(filepath.Join(stateDir, "prompts", "merge-pr", "check.md"))
	require.NoError(t, err)
	assert.Equal(t, composed.Prompts["merge-pr/check"], promptData)
}

func TestSyncProfileAssetsSkipsExistingFilesWithoutForce(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	workflowPath := filepath.Join(stateDir, "workflows", "merge-pr.yaml")
	promptPath := filepath.Join(stateDir, "prompts", "merge-pr", "check.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(promptPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("existing workflow\n"), 0o644))
	require.NoError(t, os.WriteFile(promptPath, []byte("existing prompt\n"), 0o644))

	composed := &profiles.ComposedProfile{
		Workflows: map[string][]byte{
			"merge-pr": []byte("new workflow\n"),
			"triage":   []byte("triage workflow\n"),
		},
		Prompts: map[string][]byte{
			"merge-pr/check":     []byte("new prompt\n"),
			"merge-pr/summarize": []byte("summarize prompt\n"),
		},
	}

	require.NoError(t, syncProfileAssets(stateDir, composed, false))

	workflowData, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, "existing workflow\n", string(workflowData))

	promptData, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	assert.Equal(t, "existing prompt\n", string(promptData))

	missingWorkflowData, err := os.ReadFile(filepath.Join(stateDir, "workflows", "triage.yaml"))
	require.NoError(t, err)
	assert.Equal(t, composed.Workflows["triage"], missingWorkflowData)

	missingPromptData, err := os.ReadFile(filepath.Join(stateDir, "prompts", "merge-pr", "summarize.md"))
	require.NoError(t, err)
	assert.Equal(t, composed.Prompts["merge-pr/summarize"], missingPromptData)
}

func TestSyncProfileAssetsForceOverwritesExistingFiles(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")
	workflowPath := filepath.Join(stateDir, "workflows", "merge-pr.yaml")
	promptPath := filepath.Join(stateDir, "prompts", "merge-pr", "check.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(promptPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("existing workflow\n"), 0o644))
	require.NoError(t, os.WriteFile(promptPath, []byte("existing prompt\n"), 0o644))

	composed := &profiles.ComposedProfile{
		Workflows: map[string][]byte{
			"merge-pr": []byte("new workflow\n"),
		},
		Prompts: map[string][]byte{
			"merge-pr/check": []byte("new prompt\n"),
		},
	}

	require.NoError(t, syncProfileAssets(stateDir, composed, true))

	workflowData, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, composed.Workflows["merge-pr"], workflowData)

	promptData, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	assert.Equal(t, composed.Prompts["merge-pr/check"], promptData)
}

func TestSyncProfileAssetsRejectsNilProfile(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")

	err := syncProfileAssets(stateDir, nil, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "composed profile is required")
	assert.NoDirExists(t, filepath.Join(stateDir, "workflows"))
	assert.NoDirExists(t, filepath.Join(stateDir, "prompts"))
}

func TestSyncProfileAssetsAllowsEmptyAssets(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".xylem")

	require.NoError(t, syncProfileAssets(stateDir, &profiles.ComposedProfile{}, false))
	assert.NoDirExists(t, filepath.Join(stateDir, "workflows"))
	assert.NoDirExists(t, filepath.Join(stateDir, "prompts"))
}

func TestInitCreatesV2Files(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	expectedFiles := []string{
		".xylem/HARNESS.md",
		".xylem/profile.lock",
		".xylem/prompts/adapt-repo/apply.md",
		".xylem/prompts/adapt-repo/plan.md",
		".xylem/prompts/security-compliance/synthesize.md",
		".xylem/prompts/workflow-health-report/analyze.md",
	}
	for _, workflowName := range expectedCoreWorkflows {
		expectedFiles = append(expectedFiles, filepath.Join(".xylem", "workflows", workflowName+".yaml"))
	}
	for _, f := range expectedFiles {
		path := filepath.Join(dir, f)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", f)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("expected file %s to be non-empty", f)
		}
	}
}

func TestSmoke_S6_InitSeedCreatesAdaptRepoMarkerSynchronously(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	runner := &seedRunnerStub{
		outputs: map[string][]byte{
			adaptRepoSearchCallForState("owner/name", "open"):   []byte("[]"),
			adaptRepoSearchCallForState("owner/name", "closed"): []byte("[]"),
			adaptRepoCreateCall("owner/name"):                   []byte("https://github.com/owner/name/issues/12\n"),
		},
	}

	captureStdout(func() {
		err := cmdInitWithOptions(configPath, false, true, runner)
		require.NoError(t, err)
	})

	markerPath := filepath.Join(dir, ".xylem", "state", "bootstrap", "adapt-repo-seeded.json")
	marker, err := readAdaptRepoSeedMarker(markerPath)
	require.NoError(t, err)
	assert.Equal(t, 12, marker.IssueNumber)
	assert.Equal(t, "https://github.com/owner/name/issues/12", marker.IssueURL)
	assert.Equal(t, adaptRepoSeededByInit, marker.SeededBy)
	assert.Equal(t, 3, marker.ProfileVersion)
	assert.Len(t, runner.calls, 3)
}

func TestInitSkipsExistingV2Files(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Pre-create HARNESS.md with custom content
	os.MkdirAll(filepath.Join(dir, ".xylem"), 0o755) //nolint:errcheck
	custom := "# My custom harness\n"
	os.WriteFile(filepath.Join(dir, ".xylem", "HARNESS.md"), []byte(custom), 0o644) //nolint:errcheck

	out := captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// HARNESS.md should be preserved
	data, _ := os.ReadFile(filepath.Join(dir, ".xylem", "HARNESS.md"))
	if string(data) != custom {
		t.Errorf("HARNESS.md was overwritten, got: %s", string(data))
	}

	if !strings.Contains(out, "skipped") {
		t.Errorf("expected 'skipped' message for existing file, got: %s", out)
	}

	// But other files should still be created
	if _, err := os.Stat(filepath.Join(dir, ".xylem", "workflows", "fix-bug.yaml")); os.IsNotExist(err) {
		t.Error("expected fix-bug.yaml to be created")
	}
}

func TestInitForceOverwritesV2Files(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Pre-create HARNESS.md with custom content
	os.MkdirAll(filepath.Join(dir, ".xylem"), 0o755)                                  //nolint:errcheck
	os.WriteFile(filepath.Join(dir, ".xylem", "HARNESS.md"), []byte("custom"), 0o644) //nolint:errcheck

	captureStdout(func() {
		err := cmdInit(configPath, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// HARNESS.md should now have the default content
	data, _ := os.ReadFile(filepath.Join(dir, ".xylem", "HARNESS.md"))
	if string(data) == "custom" {
		t.Error("HARNESS.md was not overwritten with force=true")
	}
	if !strings.Contains(string(data), "Project Overview") {
		t.Error("expected default HARNESS.md content after force overwrite")
	}

	lockData, err := os.ReadFile(filepath.Join(dir, ".xylem", "profile.lock"))
	require.NoError(t, err)
	assert.Contains(t, string(lockData), "name: core")
}

func TestInitScaffoldConfigV2Format(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "flags:") {
		t.Error("expected v2 config to contain 'flags:' field")
	}
	assert.Contains(t, content, "profiles:")
	assert.Contains(t, content, "workflow-health-report")
	assert.Contains(t, content, "security-compliance")
	assert.NotContains(t, content, "template:")
}

func TestInitProfileDefaultsToCoreWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	require.NoError(t, cmdInit(configPath, false))

	lockData, err := os.ReadFile(filepath.Join(dir, ".xylem", "profile.lock"))
	require.NoError(t, err)
	assert.Contains(t, string(lockData), "name: core")
	assert.NotContains(t, string(lockData), "self-hosting-xylem")
}

func TestInitProfileCoreAndSelfHostingXylem(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:nicholls-inc/xylem.git")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	require.NoError(t, cmdInitWithProfile(configPath, false, "core,self-hosting-xylem"))

	for _, workflowName := range expectedSelfHostingWorkflows {
		path := filepath.Join(dir, ".xylem", "workflows", workflowName+".yaml")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected overlay workflow %s to exist: %v", workflowName, err)
		}
	}

	lockData, err := os.ReadFile(filepath.Join(dir, ".xylem", "profile.lock"))
	require.NoError(t, err)
	assert.Contains(t, string(lockData), "name: core")
	assert.Contains(t, string(lockData), "name: self-hosting-xylem")

	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"core", "self-hosting-xylem"}, cfg.Profiles)
	if _, ok := cfg.Sources["harness-impl"]; !ok {
		t.Fatal("expected harness-impl source from self-hosting overlay")
	}
}

func TestInitRejectsUnknownProfile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	err := cmdInitWithProfile(configPath, false, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --profile")
}

func TestSmoke_S4_CoreInitScaffoldsTrackedControlPlane(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	output := captureStdout(func() {
		require.NoError(t, cmdInit(configPath, false))
	})

	assert.Contains(t, output, "Created "+configPath)
	assert.Contains(t, output, "Created .xylem/profile.lock")
	assert.Contains(t, output, "Created .xylem/workflows/context-weight-audit.yaml")
	assert.Contains(t, output, "Created .xylem/workflows/workflow-health-report.yaml")

	workflows := scaffoldedWorkflowNames(t, dir)
	assert.Equal(t, expectedCoreWorkflows, workflows)

	assert.DirExists(t, filepath.Join(dir, ".xylem", "state"))

	gitignoreData, err := os.ReadFile(filepath.Join(dir, ".xylem", ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, scaffoldGitignore, string(gitignoreData))

	lockData, err := os.ReadFile(filepath.Join(dir, ".xylem", "profile.lock"))
	require.NoError(t, err)
	assert.Contains(t, string(lockData), "name: core")
	assert.NotContains(t, string(lockData), "self-hosting-xylem")

	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"core"}, cfg.Profiles)
	assert.Contains(t, cfg.Sources, "adaptation")
	assert.Contains(t, cfg.Sources, "pr-lifecycle")
	assert.Contains(t, cfg.Sources, "lessons-hygiene")
	assert.Contains(t, cfg.Sources, "context-audit")
	assert.Contains(t, cfg.Sources, "security-compliance")
	assert.Contains(t, cfg.Sources, "workflow-health")
	assert.Contains(t, cfg.Sources, "field-report")
	require.NoError(t, cfg.Validate())
}

func TestSmoke_S5_CorePlusSelfHostingOverlayScaffoldsOverlayWorkflows(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:nicholls-inc/xylem.git")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	output := captureStdout(func() {
		require.NoError(t, cmdInitWithProfile(configPath, false, "core,self-hosting-xylem"))
	})

	expectedWorkflows := append(append([]string{}, expectedCoreWorkflows...), expectedSelfHostingWorkflows...)
	sort.Strings(expectedWorkflows)
	assert.Equal(t, expectedWorkflows, scaffoldedWorkflowNames(t, dir))
	assert.Contains(t, output, "Created .xylem/workflows/continuous-simplicity.yaml")
	assert.Contains(t, output, "Created .xylem/workflows/implement-harness.yaml")
	assert.Contains(t, output, "Created .xylem/workflows/release-cadence.yaml")
	assert.Contains(t, output, "Created .xylem/workflows/unblock-wave.yaml")

	lockData, err := os.ReadFile(filepath.Join(dir, ".xylem", "profile.lock"))
	require.NoError(t, err)
	assert.Contains(t, string(lockData), "name: core")
	assert.Contains(t, string(lockData), "name: self-hosting-xylem")

	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"core", "self-hosting-xylem"}, cfg.Profiles)
	assert.Contains(t, cfg.Sources, "harness-impl")
	assert.Contains(t, cfg.Sources, "harness-pr-lifecycle")
	assert.Contains(t, cfg.Sources, "release-cadence")
	assert.Equal(t, []string{"ready-to-merge"}, cfg.Daemon.EffectiveAutoMergeLabels())
	assert.Equal(t, "^((feat|fix|chore)/issue-[0-9]+|release-please--.+)", cfg.Daemon.EffectiveAutoMergeBranchPattern())
	assert.Equal(t, "copilot-pull-request-reviewer", cfg.Daemon.EffectiveAutoMergeReviewer())
	require.NoError(t, cfg.Validate())
}

func TestSmoke_S6_NonexistentProfileFailsFast(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	err := cmdInitWithProfile(configPath, false, "nonexistent")
	require.Error(t, err)

	assert.Contains(t, err.Error(), "invalid --profile")
	assert.Contains(t, err.Error(), "nonexistent")
	assert.NoFileExists(t, configPath)
	assert.NoFileExists(t, filepath.Join(dir, ".xylem", "profile.lock"))
}

func TestInitCobraBypassesPersistentPreRunE(t *testing.T) {
	dir := t.TempDir()

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Negative control: status (non-init) should fail without a config file
	statusCmd := newRootCmd()
	statusCmd.SetArgs([]string{"status"})
	captureStdout(func() {
		if err := statusCmd.Execute(); err == nil {
			t.Fatal("status should fail without config via PersistentPreRunE")
		}
	})

	// init should succeed in the same condition (no config file)
	initCmd := newRootCmd()
	initCmd.SetArgs([]string{"init"})
	captureStdout(func() {
		if err := initCmd.Execute(); err != nil {
			t.Fatalf("init should bypass PersistentPreRunE: %v", err)
		}
	})
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), string(out))
}

func scaffoldedWorkflowNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(dir, ".xylem", "workflows"))
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names
}

type smokeHarborConfig struct {
	Agent             string  `yaml:"agent"`
	Model             string  `yaml:"model"`
	Path              string  `yaml:"path"`
	NAttempts         int     `yaml:"n_attempts"`
	NConcurrent       int     `yaml:"n_concurrent"`
	TimeoutMultiplier float64 `yaml:"timeout_multiplier"`
}

type smokeTaskFile struct {
	Task struct {
		ID          string `toml:"id"`
		Version     string `toml:"version"`
		Environment struct {
			TimeoutSeconds int `toml:"timeout_seconds"`
		} `toml:"environment"`
		Metadata struct {
			Category   string   `toml:"category"`
			Tags       []string `toml:"tags"`
			Difficulty string   `toml:"difficulty"`
			Canary     string   `toml:"canary"`
		} `toml:"metadata"`
	} `toml:"task"`
}

type smokeRubricFile struct {
	Rubric struct {
		Name        string `toml:"name"`
		Description string `toml:"description"`
		Criteria    []struct {
			Name        string  `toml:"name"`
			Description string  `toml:"description"`
			Weight      float64 `toml:"weight"`
		} `toml:"criteria"`
	} `toml:"rubric"`
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller should resolve init_test.go")

	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func repoPath(t *testing.T, elems ...string) string {
	t.Helper()

	parts := append([]string{repoRoot(t)}, elems...)
	return filepath.Join(parts...)
}

func readRepoFile(t *testing.T, elems ...string) []byte {
	t.Helper()

	path := repoPath(t, elems...)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	return data
}

func readRepoText(t *testing.T, elems ...string) string {
	t.Helper()
	return string(readRepoFile(t, elems...))
}

func evalScenarioDirs(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(repoPath(t, ".xylem", "eval", "scenarios"))
	require.NoError(t, err)

	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	sort.Strings(dirs)
	return dirs
}

func firstEvalScenario(t *testing.T) string {
	t.Helper()

	dirs := evalScenarioDirs(t)
	require.NotEmpty(t, dirs, "expected at least one eval scenario directory")
	return dirs[0]
}

func loadHarborConfig(t *testing.T) smokeHarborConfig {
	t.Helper()

	var cfg smokeHarborConfig
	require.NoError(t, yaml.Unmarshal(readRepoFile(t, ".xylem", "eval", "harbor.yaml"), &cfg))
	return cfg
}

func loadTaskFile(t *testing.T, scenario string) smokeTaskFile {
	t.Helper()

	var taskFile smokeTaskFile
	require.NoError(t, toml.Unmarshal(readRepoFile(t, ".xylem", "eval", "scenarios", scenario, "task.toml"), &taskFile))
	return taskFile
}

func loadRubricFile(t *testing.T, name string) smokeRubricFile {
	t.Helper()

	var rubric smokeRubricFile
	require.NoError(t, toml.Unmarshal(readRepoFile(t, ".xylem", "eval", "rubrics", name), &rubric))
	return rubric
}

func pythonFunctionNames(src string) []string {
	re := regexp.MustCompile(`(?m)^def ([A-Za-z_][A-Za-z0-9_]*)\(`)
	matches := re.FindAllStringSubmatch(src, -1)

	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

func pythonImportNames(src string) []string {
	re := regexp.MustCompile(`(?m)^import ([A-Za-z_][A-Za-z0-9_]*)`)
	matches := re.FindAllStringSubmatch(src, -1)

	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	sort.Strings(names)
	return names
}

func pythonDictBlock(t *testing.T, src, name string) string {
	t.Helper()

	start := strings.Index(src, name+" = {")
	require.NotEqual(t, -1, start, "expected %s dictionary", name)

	block := src[start:]
	end := strings.Index(block, "}")
	require.NotEqual(t, -1, end, "expected closing brace for %s dictionary", name)

	return block[:end+1]
}

func parsePythonIntDict(t *testing.T, src, name string) map[string]int {
	t.Helper()

	block := pythonDictBlock(t, src, name)
	re := regexp.MustCompile(`(?m)^\s*"([^"]*)":\s*([0-9]+),?`)
	matches := re.FindAllStringSubmatch(block, -1)
	require.NotEmpty(t, matches, "expected entries in %s", name)

	values := make(map[string]int, len(matches))
	for _, match := range matches {
		var value int
		_, err := fmt.Sscanf(match[2], "%d", &value)
		require.NoError(t, err)
		values[match[1]] = value
	}
	return values
}

func parsePythonFloatDict(t *testing.T, src, name string) map[string]float64 {
	t.Helper()

	block := pythonDictBlock(t, src, name)
	re := regexp.MustCompile(`(?m)^\s*"([^"]+)":\s*([0-9.]+),?`)
	matches := re.FindAllStringSubmatch(block, -1)
	require.NotEmpty(t, matches, "expected entries in %s", name)

	values := make(map[string]float64, len(matches))
	for _, match := range matches {
		var value float64
		_, err := fmt.Sscanf(match[2], "%f", &value)
		require.NoError(t, err)
		values[match[1]] = value
	}
	return values
}

func computeReward(checks []struct {
	Name   string
	Passed bool
}, weights map[string]float64) float64 {
	if len(checks) == 0 {
		return 0
	}

	totalWeight := 0.0
	earnedWeight := 0.0
	for _, check := range checks {
		weight := 1.0
		if weights != nil {
			if configured, ok := weights[check.Name]; ok {
				weight = configured
			}
		}
		totalWeight += weight
		if check.Passed {
			earnedWeight += weight
		}
	}

	if totalWeight == 0 {
		return 0
	}
	return earnedWeight / totalWeight
}

func TestSmoke_S1_EvalDirectoryScaffoldExists(t *testing.T) {
	expectedPaths := []string{
		repoPath(t, ".xylem", "eval", "harbor.yaml"),
		repoPath(t, ".xylem", "eval", "helpers", "xylem_verify.py"),
		repoPath(t, ".xylem", "eval", "helpers", "conftest.py"),
		repoPath(t, ".xylem", "eval", "scenarios"),
		repoPath(t, ".xylem", "eval", "rubrics", "plan_quality.toml"),
		repoPath(t, ".xylem", "eval", "rubrics", "evidence_quality.toml"),
	}

	for _, path := range expectedPaths {
		info, err := os.Stat(path)
		require.NoError(t, err, "expected %s to exist", path)
		if strings.HasSuffix(path, "scenarios") {
			assert.True(t, info.IsDir(), "%s should be a directory", path)
			continue
		}
		assert.False(t, info.IsDir(), "%s should be a file", path)
	}
}

func TestSmoke_S2_HarborYamlValidWithRequiredFields(t *testing.T) {
	cfg := loadHarborConfig(t)

	assert.Equal(t, "claude-code", cfg.Agent)
	assert.NotEmpty(t, cfg.Model)
	assert.Equal(t, "scenarios/", cfg.Path)
	assert.Positive(t, cfg.NAttempts)
	assert.Positive(t, cfg.NConcurrent)
}

func TestSmoke_S3_XylemVerifyImportsCleanly(t *testing.T) {
	src := readRepoText(t, ".xylem", "eval", "helpers", "xylem_verify.py")
	imports := pythonImportNames(src)

	assert.Equal(t, []string{"glob", "json", "os"}, imports)
}

func TestSmoke_S4_XylemVerifyExposesExpectedPublicAPI(t *testing.T) {
	src := readRepoText(t, ".xylem", "eval", "helpers", "xylem_verify.py")
	names := pythonFunctionNames(src)
	expected := []string{
		"find_vessel_dir",
		"load_summary",
		"load_evidence",
		"load_phase_output",
		"load_audit_log",
		"assert_vessel_completed",
		"assert_vessel_failed",
		"assert_phases_completed",
		"assert_gates_passed",
		"assert_evidence_level",
		"assert_cost_within_budget",
		"compute_reward",
		"write_reward",
	}

	for _, expectedName := range expected {
		assert.Contains(t, names, expectedName)
	}
	assert.Contains(t, src, "EVIDENCE_RANK =")
}

func TestSmoke_S5_EvidenceRankContainsAllFiveDocumentedLevels(t *testing.T) {
	ranks := parsePythonIntDict(t, readRepoText(t, ".xylem", "eval", "helpers", "xylem_verify.py"), "EVIDENCE_RANK")

	assert.Equal(t, 5, len(ranks))
	assert.Contains(t, ranks, "proved")
	assert.Contains(t, ranks, "mechanically_checked")
	assert.Contains(t, ranks, "behaviorally_checked")
	assert.Contains(t, ranks, "observed_in_situ")
	assert.Contains(t, ranks, "")
	assert.Greater(t, ranks["proved"], ranks["mechanically_checked"])
	assert.Greater(t, ranks["mechanically_checked"], ranks["behaviorally_checked"])
	assert.Greater(t, ranks["behaviorally_checked"], ranks["observed_in_situ"])
	assert.Greater(t, ranks["observed_in_situ"], ranks[""])
}

func TestSmoke_S7_ConftestExposesWorkDirTaskDirAndVerifyFixtures(t *testing.T) {
	names := pythonFunctionNames(readRepoText(t, ".xylem", "eval", "helpers", "conftest.py"))

	assert.Contains(t, names, "work_dir")
	assert.Contains(t, names, "task_dir")
	assert.Contains(t, names, "verify")
}

func TestSmoke_S8_ScenarioTaskTomlHasRequiredFields(t *testing.T) {
	scenario := firstEvalScenario(t)
	taskFile := loadTaskFile(t, scenario)

	assert.Equal(t, scenario, taskFile.Task.ID)
	assert.Positive(t, taskFile.Task.Environment.TimeoutSeconds)
}

func TestSmoke_S9_ScenarioInstructionContainsNoHarborSpecificTerms(t *testing.T) {
	scenario := firstEvalScenario(t)
	text := strings.ToLower(readRepoText(t, ".xylem", "eval", "scenarios", scenario, "instruction.md"))

	assert.NotEmpty(t, strings.TrimSpace(text))
	assert.NotContains(t, text, "harbor")
	assert.NotContains(t, text, "scoring")
	assert.NotContains(t, text, "verification")
	assert.Regexp(t, regexp.MustCompile(`\bxylem\s+\w+`), text)
}

func TestSmoke_S10_ScenarioDirectoryContainsAllRequiredFiles(t *testing.T) {
	scenario := firstEvalScenario(t)
	base := repoPath(t, ".xylem", "eval", "scenarios", scenario)

	requiredPaths := []string{
		filepath.Join(base, "instruction.md"),
		filepath.Join(base, "task.toml"),
		filepath.Join(base, "tests", "test.sh"),
		filepath.Join(base, "tests", "test_verification.py"),
	}

	for _, path := range requiredPaths {
		info, err := os.Stat(path)
		require.NoError(t, err, "expected %s to exist", path)
		assert.False(t, info.IsDir(), "%s should be a file", path)
	}

	info, err := os.Stat(filepath.Join(base, "tests", "test.sh"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o100, "test.sh should be executable by the owner")
}

func TestSmoke_S11_PlanQualityTomlValidWithWeightsSummingToOne(t *testing.T) {
	rubric := loadRubricFile(t, "plan_quality.toml")

	assert.Equal(t, "plan_quality", rubric.Rubric.Name)
	assert.Len(t, rubric.Rubric.Criteria, 3)

	total := 0.0
	for _, criterion := range rubric.Rubric.Criteria {
		total += criterion.Weight
		assert.NotEmpty(t, criterion.Description)
	}
	assert.InDelta(t, 1.0, total, 0.001)
}

func TestSmoke_S12_EvidenceQualityTomlValidWithWeightsSummingToOne(t *testing.T) {
	rubric := loadRubricFile(t, "evidence_quality.toml")

	assert.Equal(t, "evidence_quality", rubric.Rubric.Name)

	total := 0.0
	for _, criterion := range rubric.Rubric.Criteria {
		total += criterion.Weight
		assert.NotEmpty(t, criterion.Description)
	}
	assert.InDelta(t, 1.0, total, 0.001)
}

func TestSmoke_S13_HarborYamlPathResolvesToDirectoryThatExists(t *testing.T) {
	cfg := loadHarborConfig(t)
	resolved := repoPath(t, ".xylem", "eval", cfg.Path)

	info, err := os.Stat(resolved)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "%s should resolve to a directory", resolved)
}

func TestSmoke_S14_WorkflowExecutionVerificationTemplateProducesValidReward(t *testing.T) {
	template := readRepoText(t, ".xylem", "eval", "scenarios", "fix-simple-null-pointer", "tests", "test_verification.py")
	weights := parsePythonFloatDict(t, template, "weights")

	score := computeReward([]struct {
		Name   string
		Passed bool
	}{
		{Name: "vessel_completed", Passed: true},
		{Name: "phases_completed", Passed: true},
		{Name: "gate_passed", Passed: true},
		{Name: "evidence_level", Passed: true},
		{Name: "budget_ok", Passed: true},
	}, weights)

	assert.Equal(t, 1.0, score)
	assert.Contains(t, template, `assert score >= 0.8`)
}

func TestSmoke_S15_WorkflowExecutionVerificationTemplateFailsBelowThresholdWhenChecksFail(t *testing.T) {
	template := readRepoText(t, ".xylem", "eval", "scenarios", "fix-simple-null-pointer", "tests", "test_verification.py")
	weights := parsePythonFloatDict(t, template, "weights")

	score := computeReward([]struct {
		Name   string
		Passed bool
	}{
		{Name: "vessel_completed", Passed: false},
		{Name: "phases_completed", Passed: true},
		{Name: "gate_passed", Passed: true},
		{Name: "evidence_level", Passed: true},
		{Name: "budget_ok", Passed: true},
	}, weights)

	assert.Less(t, score, 0.8)
	assert.Contains(t, template, `assert score >= 0.8`)
}

func TestSmoke_S16_VerificationTemplateWritesRewardBeforeThresholdAssertion(t *testing.T) {
	template := readRepoText(t, ".xylem", "eval", "scenarios", "fix-simple-null-pointer", "tests", "test_verification.py")

	writeIdx := strings.Index(template, "verify.write_reward(task_dir, score)")
	require.NotEqual(t, -1, writeIdx, "verification template should persist reward output")

	assertIdx := strings.Index(template, "assert score >= 0.8")
	require.NotEqual(t, -1, assertIdx, "verification template should enforce a score threshold")

	assert.Less(t, writeIdx, assertIdx, "reward should be written before the template asserts the pass threshold")
}

func TestSmoke_S17_DeferredItemsAreNotPresent(t *testing.T) {
	scenarioDirs := evalScenarioDirs(t)
	for _, scenario := range scenarioDirs {
		dockerfile := repoPath(t, ".xylem", "eval", "scenarios", scenario, "environment", "Dockerfile")
		_, err := os.Stat(dockerfile)
		assert.True(t, os.IsNotExist(err), "Dockerfile is deferred and should not exist: %s", dockerfile)
	}

	for _, pattern := range []string{
		repoPath(t, ".github", "workflows", "*.yml"),
		repoPath(t, ".github", "workflows", "*.yaml"),
	} {
		files, err := filepath.Glob(pattern)
		require.NoError(t, err)
		for _, file := range files {
			content, err := os.ReadFile(file)
			require.NoError(t, err)
			assert.NotContains(t, string(content), "harbor run")
		}
	}

	_, err := os.Stat(repoPath(t, ".xylem", "eval", "jobs"))
	require.True(t, os.IsNotExist(err), ".xylem/eval/jobs should be absent")

	_, err = os.Stat(repoPath(t, "jobs"))
	require.True(t, os.IsNotExist(err), "jobs should be absent at repo root")
}

func TestSmoke_S18_ScenariosDirectoryPresentEvenWithNoScenariosYetPopulated(t *testing.T) {
	info, err := os.Stat(repoPath(t, ".xylem", "eval", "scenarios"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestInitEmitsAgentsMd(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		require.NoError(t, cmdInitWithProfile(configPath, false, "core"))
	})

	agentsPath := filepath.Join(dir, "AGENTS.md")
	_, err := os.Stat(agentsPath)
	require.NoError(t, err, "AGENTS.md should be created")

	data, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	content := string(data)

	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	assert.GreaterOrEqual(t, len(lines), 30, "AGENTS.md should be at least 30 lines")
	assert.LessOrEqual(t, len(lines), 50, "AGENTS.md should be at most 50 lines")
	assert.Equal(t, "# Agents guide", lines[0], "first line must be '# Agents guide'")
}

func TestInitAgentsMdSkippedWithoutForce(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	existing := "# My custom agents file\n\nDo not overwrite me.\n"
	require.NoError(t, os.WriteFile("AGENTS.md", []byte(existing), 0o644))

	out := captureStdout(func() {
		require.NoError(t, cmdInitWithProfile(configPath, false, "core"))
	})

	data, _ := os.ReadFile("AGENTS.md")
	assert.Equal(t, existing, string(data), "existing AGENTS.md must not be modified without --force")
	assert.Contains(t, out, "skipped")
}

func TestInitAgentsMdForceSkipsWhenFirstLineDiffers(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	existing := "# My custom agents file\n\nDo not overwrite me.\n"
	require.NoError(t, os.WriteFile("AGENTS.md", []byte(existing), 0o644))

	out := captureStdout(func() {
		require.NoError(t, cmdInitWithProfile(configPath, true, "core"))
	})

	data, _ := os.ReadFile("AGENTS.md")
	assert.Equal(t, existing, string(data), "--force must not overwrite when first line is not '# Agents guide'")
	assert.Contains(t, out, "skipped")
}

func TestInitAgentsMdForceOverwritesWhenFirstLineMatches(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	existing := "# Agents guide\n\nOld content.\n"
	require.NoError(t, os.WriteFile("AGENTS.md", []byte(existing), 0o644))

	captureStdout(func() {
		require.NoError(t, cmdInitWithProfile(configPath, true, "core"))
	})

	data, _ := os.ReadFile("AGENTS.md")
	content := string(data)
	assert.NotEqual(t, existing, content, "--force should overwrite when first line is '# Agents guide'")
	assert.Equal(t, "# Agents guide", strings.SplitN(content, "\n", 2)[0])
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	assert.GreaterOrEqual(t, len(lines), 30, "overwritten AGENTS.md should still be the full template")
}
