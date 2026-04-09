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
