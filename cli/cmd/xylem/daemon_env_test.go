package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDaemonStartupEnvAppliesDaemonRootEnv(t *testing.T) {
	repoDir := t.TempDir()
	envPath := daemonSupervisorEnvFilePath(repoDir, ".env")
	require.NoError(t, os.MkdirAll(filepath.Dir(envPath), 0o755))
	require.NoError(t, os.WriteFile(envPath, []byte("API_TOKEN=from-file\nEMPTY=\n"), 0o644))

	t.Setenv("API_TOKEN", "from-process")
	t.Setenv("EMPTY", "non-empty")

	require.NoError(t, loadDaemonStartupEnv(repoDir, ".env"))
	assert.Equal(t, "from-file", os.Getenv("API_TOKEN"))
	assert.Equal(t, "", os.Getenv("EMPTY"))
}

func TestLoadDaemonStartupEnvMissingFileIsNoop(t *testing.T) {
	repoDir := t.TempDir()
	t.Setenv("API_TOKEN", "from-process")

	require.NoError(t, loadDaemonStartupEnv(repoDir, ".env"))
	assert.Equal(t, "from-process", os.Getenv("API_TOKEN"))
}

func TestLoadDaemonStartupEnvReturnsParseError(t *testing.T) {
	repoDir := t.TempDir()
	envPath := daemonSupervisorEnvFilePath(repoDir, ".env")
	require.NoError(t, os.MkdirAll(filepath.Dir(envPath), 0o755))
	require.NoError(t, os.WriteFile(envPath, []byte("not-an-assignment\n"), 0o644))

	err := loadDaemonStartupEnv(repoDir, ".env")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load daemon startup env")
	assert.Contains(t, err.Error(), ".env")
}

func TestLoadDaemonStartupEnvUsesConfiguredEnvFile(t *testing.T) {
	repoDir := t.TempDir()
	envPath := daemonSupervisorEnvFilePath(repoDir, "secrets.env")
	require.NoError(t, os.MkdirAll(filepath.Dir(envPath), 0o755))
	require.NoError(t, os.WriteFile(envPath, []byte("CONFIGURED_KEY=configured-value\n"), 0o644))

	t.Setenv("CONFIGURED_KEY", "original")

	require.NoError(t, loadDaemonStartupEnv(repoDir, "secrets.env"))
	assert.Equal(t, "configured-value", os.Getenv("CONFIGURED_KEY"))
}

func TestApplyDaemonEnvEntriesRejectsInvalidEntry(t *testing.T) {
	err := applyDaemonEnvEntries([]string{"missing-separator"}, func(string, string) error {
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid environment entry")
}

func TestApplyDaemonEnvEntriesPreservesEmbeddedEquals(t *testing.T) {
	got := map[string]string{}

	err := applyDaemonEnvEntries([]string{"API_TOKEN=alpha=beta"}, func(key, value string) error {
		got[key] = value
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "alpha=beta", got["API_TOKEN"])
}

func TestApplyDaemonEnvEntriesReturnsSetterError(t *testing.T) {
	err := applyDaemonEnvEntries([]string{"API_TOKEN=value"}, func(string, string) error {
		return fmt.Errorf("boom")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set API_TOKEN")
	assert.Contains(t, err.Error(), "boom")
}
