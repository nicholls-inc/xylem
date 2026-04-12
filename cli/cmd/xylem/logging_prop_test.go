package main

import (
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

func TestPropOpenDaemonLogFileCreatesExpectedPath(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		root, err := os.MkdirTemp("", "xylem-log-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(root)

		stateDir := filepath.Join(
			root,
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "segment1"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "segment2"),
		)

		file, err := openDaemonLogFile(stateDir)
		if err != nil {
			rt.Fatalf("openDaemonLogFile(%q) error = %v", stateDir, err)
		}
		if err := file.Close(); err != nil {
			rt.Fatalf("file.Close() error = %v", err)
		}

		logPath := daemonLogPath(stateDir)
		info, err := os.Stat(logPath)
		if err != nil {
			rt.Fatalf("Stat(%q) error = %v", logPath, err)
		}
		if info.IsDir() {
			rt.Fatalf("%q is a directory, want file", logPath)
		}
		if filepath.Dir(logPath) != filepath.Clean(stateDir) {
			rt.Fatalf("log directory = %q, want %q", filepath.Dir(logPath), filepath.Clean(stateDir))
		}
	})
}

func TestPropNewConfiguredLoggerIgnoresObservabilityEndpoint(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		root, err := os.MkdirTemp("", "xylem-logger-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp(root) error = %v", err)
		}
		defer os.RemoveAll(root)

		controlDir := filepath.Join(root, "control")
		candidateDir := filepath.Join(root, "candidate")

		controlCfg := makeDrainConfig(controlDir)
		candidateCfg := makeDrainConfig(candidateDir)
		candidateCfg.Observability.Endpoint = rapid.StringMatching(`[\x20-\x7e]{0,32}`).Draw(rt, "endpoint")
		candidateCfg.Observability.Insecure = rapid.Bool().Draw(rt, "insecure")

		controlLogger, controlCleanup, err := newConfiguredLogger(controlCfg, loggerOptions{daemonFile: true})
		if err != nil {
			rt.Fatalf("newConfiguredLogger(control) error = %v", err)
		}
		candidateLogger, candidateCleanup, err := newConfiguredLogger(candidateCfg, loggerOptions{daemonFile: true})
		if err != nil {
			rt.Fatalf("newConfiguredLogger(candidate) error = %v", err)
		}

		controlLogger.Info("daemon property log")
		candidateLogger.Info("daemon property log")
		controlCleanup()
		candidateCleanup()

		controlData, err := os.ReadFile(daemonLogPath(controlDir))
		if err != nil {
			rt.Fatalf("ReadFile(%q) error = %v", daemonLogPath(controlDir), err)
		}
		candidateData, err := os.ReadFile(daemonLogPath(candidateDir))
		if err != nil {
			rt.Fatalf("ReadFile(%q) error = %v", daemonLogPath(candidateDir), err)
		}

		controlOutput := normalizeSlogOutput(string(controlData))
		candidateOutput := normalizeSlogOutput(string(candidateData))
		if controlOutput != candidateOutput {
			rt.Fatalf("logger output changed when observability endpoint was set:\ncontrol: %q\ncandidate: %q", controlOutput, candidateOutput)
		}
	})
}
