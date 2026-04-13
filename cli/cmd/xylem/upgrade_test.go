package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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

func TestResolveDaemonUpgradeTargetUsesWorkingDirectory(t *testing.T) {
	worktreeDir := filepath.Join(t.TempDir(), ".xylem", "worktrees", ".daemon-root")
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
		filepath.Join(".xylem", "worktrees", ".daemon-root"),
		filepath.Join(root, "cli", "xylem"),
	)
	require.NoError(t, err)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	wantRepoDir, err := filepath.Abs(filepath.Join(resolvedRoot, ".xylem", "worktrees", ".daemon-root"))
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

	binaryWorkflowPath := filepath.Join(binaryRepo, ".xylem", "workflows", "merge-pr.yaml")
	daemonWorkflowPath := filepath.Join(daemonRepo, ".xylem", "workflows", "merge-pr.yaml")
	binaryPromptPath := filepath.Join(binaryRepo, ".xylem", "prompts", "merge-pr", "check.md")
	daemonPromptPath := filepath.Join(daemonRepo, ".xylem", "prompts", "merge-pr", "check.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(binaryWorkflowPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(daemonWorkflowPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(binaryPromptPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(daemonPromptPath), 0o755))
	require.NoError(t, os.WriteFile(binaryWorkflowPath, []byte(`run: "gh pr merge 181 --auto"`), 0o644))
	require.NoError(t, os.WriteFile(daemonWorkflowPath, []byte(`run: "gh pr merge 181 --admin"`), 0o644))
	require.NoError(t, os.WriteFile(binaryPromptPath, []byte("use the upgraded merge workflow"), 0o644))
	require.NoError(t, os.WriteFile(daemonPromptPath, []byte("stale prompt from old worktree"), 0o644))

	var pulledRepo string
	var builtCLI string
	var execPath string
	stubDaemonUpgradeDependencies(
		t,
		func(repoDir string) error {
			pulledRepo = repoDir
			updated, err := os.ReadFile(binaryWorkflowPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(daemonWorkflowPath, updated, 0o644); err != nil {
				return err
			}
			updatedPrompt, err := os.ReadFile(binaryPromptPath)
			if err != nil {
				return err
			}
			return os.WriteFile(daemonPromptPath, updatedPrompt, 0o644)
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
	assert.Contains(t, string(got), "--auto")
	assert.NotContains(t, string(got), "--admin")

	prompt, err := os.ReadFile(filepath.Join(target.repoDir, ".xylem", "prompts", "merge-pr", "check.md"))
	require.NoError(t, err)
	assert.Equal(t, "use the upgraded merge workflow", string(prompt))
}
