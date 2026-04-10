package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/profiles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stubDaemonUpgradeDependencies(
	t *testing.T,
	gitPull func(string) error,
	goBuild func(string, string) error,
	execFn func(string, []string, []string) error,
) {
	t.Helper()

	prevGitPull := daemonGitPull
	prevGoBuild := daemonGoBuild
	prevExec := daemonExec
	daemonGitPull = gitPull
	daemonGoBuild = goBuild
	daemonExec = execFn
	t.Cleanup(func() {
		daemonGitPull = prevGitPull
		daemonGoBuild = prevGoBuild
		daemonExec = prevExec
	})
}

func stubUpgradeProfileDependencies(
	t *testing.T,
	loadConfig func(string) (*config.Config, error),
	composeProfiles func(...string) (*profiles.ComposedProfile, error),
	syncProfileFiles func(string, *profiles.ComposedProfile, bool) error,
) {
	t.Helper()

	prevLoadConfig := daemonLoadConfig
	prevComposeProfiles := daemonComposeProfiles
	prevSyncProfileFiles := daemonSyncProfileFiles
	daemonLoadConfig = loadConfig
	daemonComposeProfiles = composeProfiles
	daemonSyncProfileFiles = syncProfileFiles
	t.Cleanup(func() {
		daemonLoadConfig = prevLoadConfig
		daemonComposeProfiles = prevComposeProfiles
		daemonSyncProfileFiles = prevSyncProfileFiles
	})
}

func TestResolveDaemonUpgradeTargetUsesWorkingDirectory(t *testing.T) {
	worktreeDir := filepath.Join(t.TempDir(), ".claude", "worktrees", ".daemon-root")
	executablePath := filepath.Join(t.TempDir(), "cli", "xylem")

	target, err := resolveDaemonUpgradeTarget(
		func() (string, error) { return worktreeDir, nil },
		func() (string, error) { return executablePath, nil },
	)
	require.NoError(t, err)

	assert.Equal(t, worktreeDir, target.repoDir)
	assert.Equal(t, executablePath, target.executablePath)
	assert.NotEqual(t, filepath.Dir(filepath.Dir(executablePath)), target.repoDir)
}

func TestResolveDaemonUpgradeTargetPropagatesExecutableError(t *testing.T) {
	wantErr := errors.New("boom")

	_, err := resolveDaemonUpgradeTarget(
		func() (string, error) { return t.TempDir(), nil },
		func() (string, error) { return "", wantErr },
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "resolve executable path")
}

func TestResolveDaemonUpgradeTargetPropagatesWorkingDirectoryError(t *testing.T) {
	wantErr := errors.New("boom")

	_, err := resolveDaemonUpgradeTarget(
		func() (string, error) { return "", wantErr },
		func() (string, error) { return filepath.Join(t.TempDir(), "cli", "xylem"), nil },
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "resolve working directory")
}

func TestDaemonUpgradeTargetFromPathsNormalizesRelativeWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	defer func() {
		require.NoError(t, os.Chdir(oldWd))
	}()

	target, err := daemonUpgradeTargetFromPaths(
		filepath.Join(".claude", "worktrees", ".daemon-root"),
		filepath.Join(root, "cli", "xylem"),
	)
	require.NoError(t, err)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	wantRepoDir, err := filepath.Abs(filepath.Join(resolvedRoot, ".claude", "worktrees", ".daemon-root"))
	require.NoError(t, err)

	assert.Equal(t, wantRepoDir, target.repoDir)
	assert.Equal(t, filepath.Join(root, "cli", "xylem"), target.executablePath)
}

func TestSmoke_S37_DaemonAutoUpgradeSyncsDaemonWorktreeControlPlaneFiles(t *testing.T) {
	binaryRepo := t.TempDir()
	daemonRepo := t.TempDir()
	executablePath := filepath.Join(binaryRepo, "cli", "xylem")
	require.NoError(t, os.MkdirAll(filepath.Dir(executablePath), 0o755))
	require.NoError(t, os.WriteFile(executablePath, []byte("old-binary"), 0o755))

	upgradedWorkflowData := []byte(`run: "gh pr merge 181 --auto"`)
	upgradedPromptData := []byte("use the upgraded merge workflow")
	daemonWorkflowPath := filepath.Join(daemonRepo, ".xylem", "workflows", "merge-pr.yaml")
	daemonPromptPath := filepath.Join(daemonRepo, ".xylem", "prompts", "merge-pr", "check.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(daemonWorkflowPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(daemonPromptPath), 0o755))
	require.NoError(t, os.WriteFile(daemonWorkflowPath, []byte(`run: "gh pr merge 181 --admin"`), 0o644))
	require.NoError(t, os.WriteFile(daemonPromptPath, []byte("stale prompt from old worktree"), 0o644))

	var pulledRepo string
	var builtCLI string
	var execPath string
	stubDaemonUpgradeDependencies(
		t,
		func(repoDir string) error {
			pulledRepo = repoDir
			targetWorkflowPath := filepath.Join(repoDir, ".xylem", "workflows", "merge-pr.yaml")
			targetPromptPath := filepath.Join(repoDir, ".xylem", "prompts", "merge-pr", "check.md")
			if err := os.MkdirAll(filepath.Dir(targetWorkflowPath), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPromptPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(targetWorkflowPath, upgradedWorkflowData, 0o644); err != nil {
				return err
			}
			return os.WriteFile(targetPromptPath, upgradedPromptData, 0o644)
		},
		func(cliDir, outPath string) error {
			builtCLI = cliDir
			return os.WriteFile(outPath, []byte("new-binary"), 0o755)
		},
		func(path string, _ []string, _ []string) error {
			execPath = path
			return errors.New("exec blocked in test")
		},
	)

	target, err := resolveDaemonUpgradeTarget(
		func() (string, error) { return daemonRepo, nil },
		func() (string, error) { return executablePath, nil },
	)
	require.NoError(t, err)

	selfUpgrade(target.repoDir, target.executablePath)

	assert.Equal(t, daemonRepo, pulledRepo)
	assert.Equal(t, filepath.Join(daemonRepo, "cli"), builtCLI)
	assert.Equal(t, executablePath, execPath)

	got, err := os.ReadFile(filepath.Join(target.repoDir, ".xylem", "workflows", "merge-pr.yaml"))
	require.NoError(t, err)
	assert.Equal(t, upgradedWorkflowData, got)

	prompt, err := os.ReadFile(filepath.Join(target.repoDir, ".xylem", "prompts", "merge-pr", "check.md"))
	require.NoError(t, err)
	assert.Equal(t, upgradedPromptData, prompt)

	binaryWorkflowPath := filepath.Join(binaryRepo, ".xylem", "workflows", "merge-pr.yaml")
	assert.NoFileExists(t, binaryWorkflowPath)
}

func TestSyncDaemonProfileAssetsUsesConfiguredProfilesAndStateDir(t *testing.T) {
	repoDir := t.TempDir()
	composed := &profiles.ComposedProfile{}

	var loadedConfigPath string
	var composedProfiles []string
	var syncedStateDir string
	var syncedForce bool
	var syncedProfile *profiles.ComposedProfile
	stubUpgradeProfileDependencies(
		t,
		func(path string) (*config.Config, error) {
			loadedConfigPath = path
			return &config.Config{
				Profiles: []string{"core", "self-hosting-xylem"},
				StateDir: ".state/xylem",
			}, nil
		},
		func(names ...string) (*profiles.ComposedProfile, error) {
			composedProfiles = append([]string(nil), names...)
			return composed, nil
		},
		func(stateDir string, got *profiles.ComposedProfile, force bool) error {
			syncedStateDir = stateDir
			syncedProfile = got
			syncedForce = force
			return nil
		},
	)

	require.NoError(t, syncDaemonProfileAssets(repoDir))
	assert.Equal(t, filepath.Join(repoDir, ".xylem.yml"), loadedConfigPath)
	assert.Equal(t, []string{"core", "self-hosting-xylem"}, composedProfiles)
	assert.Equal(t, filepath.Join(repoDir, ".state/xylem"), syncedStateDir)
	assert.Same(t, composed, syncedProfile)
	assert.False(t, syncedForce)
}

func TestSelfUpgradeContinuesToExecWhenProfileSyncFails(t *testing.T) {
	repoDir := t.TempDir()
	executablePath := filepath.Join(repoDir, "cli", "xylem")
	require.NoError(t, os.MkdirAll(filepath.Dir(executablePath), 0o755))
	require.NoError(t, os.WriteFile(executablePath, []byte("old-binary"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".xylem.yml"), []byte("profiles:\n  - core\n"), 0o644))

	var execPath string
	var loadedConfigPath string
	var composedProfiles []string
	syncAttempts := 0
	stubDaemonUpgradeDependencies(
		t,
		func(string) error { return nil },
		func(string, string) error {
			return os.WriteFile(executablePath+".upgrade", []byte("new-binary"), 0o755)
		},
		func(path string, _ []string, _ []string) error {
			execPath = path
			return errors.New("exec blocked in test")
		},
	)
	stubUpgradeProfileDependencies(
		t,
		func(path string) (*config.Config, error) {
			loadedConfigPath = path
			return &config.Config{Profiles: []string{"core"}, StateDir: ".xylem"}, nil
		},
		func(names ...string) (*profiles.ComposedProfile, error) {
			composedProfiles = append([]string(nil), names...)
			return &profiles.ComposedProfile{}, nil
		},
		func(string, *profiles.ComposedProfile, bool) error {
			syncAttempts++
			return errors.New("sync failed")
		},
	)

	selfUpgrade(repoDir, executablePath)

	assert.Equal(t, executablePath, execPath)
	assert.Equal(t, filepath.Join(repoDir, ".xylem.yml"), loadedConfigPath)
	assert.Equal(t, []string{"core"}, composedProfiles)
	assert.Equal(t, 1, syncAttempts)
}

func TestSmoke_S38_DaemonAutoUpgradeScaffoldsMissingProfileWorkflowAssets(t *testing.T) {
	repoDir := t.TempDir()
	executablePath := filepath.Join(repoDir, "cli", "xylem")
	require.NoError(t, os.MkdirAll(filepath.Dir(executablePath), 0o755))
	require.NoError(t, os.WriteFile(executablePath, []byte("old-binary"), 0o755))

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "remote", "add", "origin", "git@github.com:nicholls-inc/xylem.git")
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoDir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(oldWd))
	})
	require.NoError(t, cmdInitWithProfile(filepath.Join(repoDir, ".xylem.yml"), false, "core,self-hosting-xylem"))

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".xylem", "workflows", "fix-bug.yaml"), []byte("existing workflow\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(repoDir, ".xylem", "workflows", "implement-harness.yaml")))
	require.NoError(t, os.Remove(filepath.Join(repoDir, ".xylem", "prompts", "implement-harness", "plan.md")))

	var execPath string
	stubDaemonUpgradeDependencies(
		t,
		func(string) error { return nil },
		func(string, string) error {
			return os.WriteFile(executablePath+".upgrade", []byte("new-binary"), 0o755)
		},
		func(path string, _ []string, _ []string) error {
			execPath = path
			return errors.New("exec blocked in test")
		},
	)

	selfUpgrade(repoDir, executablePath)

	assert.Equal(t, executablePath, execPath)

	composed, err := profiles.Compose("core", "self-hosting-xylem")
	require.NoError(t, err)

	workflowData, err := os.ReadFile(filepath.Join(repoDir, ".xylem", "workflows", "implement-harness.yaml"))
	require.NoError(t, err)
	assert.Equal(t, composed.Workflows["implement-harness"], workflowData)

	promptData, err := os.ReadFile(filepath.Join(repoDir, ".xylem", "prompts", "implement-harness", "plan.md"))
	require.NoError(t, err)
	assert.Equal(t, composed.Prompts["implement-harness/plan"], promptData)

	existingWorkflowData, err := os.ReadFile(filepath.Join(repoDir, ".xylem", "workflows", "fix-bug.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "existing workflow\n", string(existingWorkflowData))
}
