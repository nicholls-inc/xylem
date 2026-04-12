package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

func cleanupConfiguredCommandLogger(t *testing.T) {
	t.Helper()

	t.Cleanup(func() {
		commandLoggerMu.Lock()
		cleanup := commandLoggerCleanup
		commandLoggerCleanup = nil
		commandLoggerMu.Unlock()
		if cleanup != nil {
			cleanup()
		}
	})
}

func normalizeSlogOutput(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i, line := range lines {
		if idx := strings.Index(line, " level="); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	return strings.Join(lines, "\n")
}

func TestNewConfiguredLoggerDaemonWritesToFile(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)

	logger, cleanup, err := newConfiguredLogger(cfg, loggerOptions{daemonFile: true})
	if err != nil {
		t.Fatalf("newConfiguredLogger() error = %v", err)
	}

	logger.Info("daemon log entry", "component", "daemon")
	cleanup()

	data, err := os.ReadFile(daemonLogPath(dir))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", daemonLogPath(dir), err)
	}
	got := string(data)
	if !strings.Contains(got, "msg=\"daemon log entry\"") {
		t.Fatalf("daemon log file missing message: %q", got)
	}
	if !strings.Contains(got, "component=daemon") {
		t.Fatalf("daemon log file missing structured attribute: %q", got)
	}
}

func TestDaemonLogPathUsesRuntimeStateForControlPlaneDirectories(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("state/\n"), 0o644))

	assert.Equal(t, config.RuntimePath(dir, daemonLogFileName), daemonLogPath(dir))
}

func TestNewConfiguredLoggerWithoutDaemonFileSkipsLogFile(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)

	logger, cleanup, err := newConfiguredLogger(cfg, loggerOptions{})
	if err != nil {
		t.Fatalf("newConfiguredLogger() error = %v", err)
	}
	defer cleanup()

	logger.Info("stderr only log")

	if _, err := os.Stat(daemonLogPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not-exist", daemonLogPath(dir), err)
	}
}

func TestNewConfiguredLoggerRestoresDefaultLogger(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)

	var sentinel bytes.Buffer
	slog.SetDefault(newTextLogger(&sentinel))

	logger, cleanup, err := newConfiguredLogger(cfg, loggerOptions{})
	if err != nil {
		t.Fatalf("newConfiguredLogger() error = %v", err)
	}

	slog.SetDefault(logger)
	cleanup()

	slog.Info("restored default logger")

	if got := sentinel.String(); !strings.Contains(got, "msg=\"restored default logger\"") {
		t.Fatalf("restored logger output = %q, want restored message", got)
	}
}

func TestNewConfiguredLoggerIgnoresObservabilitySettings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{
			name: "malformed endpoint still keeps local handlers",
			mutate: func(cfg *config.Config) {
				cfg.Observability.Endpoint = ":// malformed endpoint with spaces"
				cfg.Observability.Insecure = true
			},
		},
		{
			name: "disabled observability still keeps local handlers",
			mutate: func(cfg *config.Config) {
				cfg.Observability.Endpoint = "collector:4317"
				disabled := false
				cfg.Observability.Enabled = &disabled
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := makeDrainConfig(dir)
			tt.mutate(cfg)

			logger, cleanup, err := newConfiguredLogger(cfg, loggerOptions{daemonFile: true})
			require.NoError(t, err)

			logger.Info("local log entry", "mode", "local")
			cleanup()

			data, err := os.ReadFile(daemonLogPath(dir))
			require.NoError(t, err)

			got := normalizeSlogOutput(string(data))
			assert.Contains(t, got, `level=INFO msg="local log entry"`)
			assert.Contains(t, got, "mode=local")
		})
	}
}

func TestOpenDaemonLogFileReturnsErrorWhenStateDirCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("block"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", blocker, err)
	}

	_, err := openDaemonLogFile(filepath.Join(blocker, "nested"))
	if err == nil {
		t.Fatal("openDaemonLogFile() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create daemon log directory") {
		t.Fatalf("openDaemonLogFile() error = %v, want directory creation context", err)
	}
}

func TestOpenDaemonLogFileReturnsErrorWhenLogPathCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", stateDir, err)
	}
	if err := os.Mkdir(daemonLogPath(stateDir), 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error = %v", daemonLogPath(stateDir), err)
	}

	_, err := openDaemonLogFile(stateDir)
	if err == nil {
		t.Fatal("openDaemonLogFile() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "open daemon log file") {
		t.Fatalf("openDaemonLogFile() error = %v, want open-file context", err)
	}
}

func TestConfigureCommandLoggerUsesDaemonFileForDaemonCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cmd := &cobra.Command{Use: "daemon"}

	if err := configureCommandLogger(cmd, cfg); err != nil {
		t.Fatalf("configureCommandLogger() error = %v", err)
	}
	cleanupConfiguredCommandLogger(t)

	slog.Info("daemon command log")

	data, err := os.ReadFile(daemonLogPath(dir))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", daemonLogPath(dir), err)
	}
	if !strings.Contains(string(data), "msg=\"daemon command log\"") {
		t.Fatalf("daemon log file missing command output: %q", string(data))
	}
}

func TestConfigureCommandLoggerSkipsDaemonFileForNonDaemonCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cmd := &cobra.Command{Use: "drain"}

	if err := configureCommandLogger(cmd, cfg); err != nil {
		t.Fatalf("configureCommandLogger() error = %v", err)
	}
	cleanupConfiguredCommandLogger(t)

	slog.Info("non-daemon command log")

	if _, err := os.Stat(daemonLogPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not-exist", daemonLogPath(dir), err)
	}
}
