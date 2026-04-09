package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	otellog "go.opentelemetry.io/otel/log"
	otelglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

type recordingLogExporter struct {
	mu            sync.Mutex
	records       []sdklog.Record
	shutdownCalls int
}

func (e *recordingLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}

func (e *recordingLogExporter) Shutdown(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shutdownCalls++
	return nil
}

func (e *recordingLogExporter) ForceFlush(context.Context) error {
	return nil
}

func (e *recordingLogExporter) snapshot() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]sdklog.Record, len(e.records))
	for i, record := range e.records {
		out[i] = record.Clone()
	}
	return out
}

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

func TestNewConfiguredLoggerWithOTelBridgeExportsRecordsAndRestoresProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = "collector:4317"
	cfg.Observability.Insecure = true

	exporter := &recordingLogExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))

	prevBuilder := buildOTelLogHandler
	buildOTelLogHandler = func(*config.Config) (slog.Handler, *sdklog.LoggerProvider, error) {
		return otelslog.NewHandler("test/logger", otelslog.WithLoggerProvider(provider)), provider, nil
	}
	defer func() {
		buildOTelLogHandler = prevBuilder
	}()

	prevProvider := otelglobal.GetLoggerProvider()

	logger, cleanup, err := newConfiguredLogger(cfg, loggerOptions{})
	if err != nil {
		t.Fatalf("newConfiguredLogger() error = %v", err)
	}

	logger.Info("otel bridge message", "workflow", "fix-bug")
	cleanup()

	if got := otelglobal.GetLoggerProvider(); got != prevProvider {
		t.Fatal("global logger provider was not restored")
	}
	if exporter.shutdownCalls != 1 {
		t.Fatalf("shutdown calls = %d, want 1", exporter.shutdownCalls)
	}

	records := exporter.snapshot()
	if len(records) != 1 {
		t.Fatalf("exported records = %d, want 1", len(records))
	}
	if got := records[0].Body().AsString(); got != "otel bridge message" {
		t.Fatalf("record body = %q, want %q", got, "otel bridge message")
	}

	attrs := map[string]string{}
	records[0].WalkAttributes(func(kv otellog.KeyValue) bool {
		attrs[kv.Key] = kv.Value.AsString()
		return true
	})
	if got := attrs["workflow"]; got != "fix-bug" {
		t.Fatalf("record workflow = %q, want %q", got, "fix-bug")
	}
}

func TestNewConfiguredLoggerWithOTelBridgeFailureFallsBackToLocalHandlers(t *testing.T) {
	dir := t.TempDir()
	cfg := makeDrainConfig(dir)
	cfg.Observability.Endpoint = "collector:4317"
	cfg.Observability.Insecure = true

	prevBuilder := buildOTelLogHandler
	buildOTelLogHandler = func(*config.Config) (slog.Handler, *sdklog.LoggerProvider, error) {
		return nil, nil, errors.New("bridge down")
	}
	defer func() {
		buildOTelLogHandler = prevBuilder
	}()

	logger, cleanup, err := newConfiguredLogger(cfg, loggerOptions{daemonFile: true})
	if err != nil {
		t.Fatalf("newConfiguredLogger() error = %v", err)
	}

	logger.Info("fallback log entry", "mode", "local")
	cleanup()

	data, err := os.ReadFile(daemonLogPath(dir))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", daemonLogPath(dir), err)
	}
	got := string(data)
	if !strings.Contains(got, "msg=\"fallback log entry\"") {
		t.Fatalf("daemon log file missing fallback message: %q", got)
	}
	if !strings.Contains(got, "mode=local") {
		t.Fatalf("daemon log file missing fallback attribute: %q", got)
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
