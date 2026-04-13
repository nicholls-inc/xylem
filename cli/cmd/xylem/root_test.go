package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindConfigPath_FileAtCWD(t *testing.T) {
	dir := t.TempDir()
	origWD, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(origWD) }) //nolint:errcheck
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	configFile := filepath.Join(dir, ".xylem.yml")
	if err := os.WriteFile(configFile, []byte("repo: owner/repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := findConfigPath(".xylem.yml")
	if got != ".xylem.yml" {
		t.Errorf("got %q, want %q", got, ".xylem.yml")
	}
}

func TestFindConfigPath_FileAtParent(t *testing.T) {
	rootDir := t.TempDir()
	subDir := filepath.Join(rootDir, "cli")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	configFile := filepath.Join(rootDir, ".xylem.yml")
	if err := os.WriteFile(configFile, []byte("repo: owner/repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	origWD, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(origWD) }) //nolint:errcheck
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	got := findConfigPath(".xylem.yml")
	// Resolve symlinks for comparison (macOS /var -> /private/var).
	wantReal, _ := filepath.EvalSymlinks(configFile)
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal != wantReal {
		t.Errorf("got %q (real: %q), want %q (real: %q)", got, gotReal, configFile, wantReal)
	}
}

func TestFindConfigPath_FileAtGrandparent(t *testing.T) {
	rootDir := t.TempDir()
	leafDir := filepath.Join(rootDir, "cli", "cmd")
	if err := os.MkdirAll(leafDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	configFile := filepath.Join(rootDir, ".xylem.yml")
	if err := os.WriteFile(configFile, []byte("repo: owner/repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	origWD, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(origWD) }) //nolint:errcheck
	if err := os.Chdir(leafDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	got := findConfigPath(".xylem.yml")
	// Resolve symlinks for comparison (macOS /var -> /private/var).
	wantReal, _ := filepath.EvalSymlinks(configFile)
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal != wantReal {
		t.Errorf("got %q (real: %q), want %q (real: %q)", got, gotReal, configFile, wantReal)
	}
}

func TestFindConfigPath_NotFound(t *testing.T) {
	dir := t.TempDir()
	origWD, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(origWD) }) //nolint:errcheck
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	got := findConfigPath(".xylem.yml")
	if got != ".xylem.yml" {
		t.Errorf("got %q, want original %q", got, ".xylem.yml")
	}
}

func TestFindConfigPath_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, ".xylem.yml")
	// File doesn't exist — absolute paths must not be walked up.
	got := findConfigPath(abs)
	if got != abs {
		t.Errorf("got %q, want %q", got, abs)
	}
}

func TestFindConfigPath_ExplicitRelativePath(t *testing.T) {
	dir := t.TempDir()
	origWD, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(origWD) }) //nolint:errcheck
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	// A relative path with a directory component is left unchanged.
	rel := "../custom.yml"
	got := findConfigPath(rel)
	if got != rel {
		t.Errorf("got %q, want %q", got, rel)
	}
}

func TestPersistentPreRunE_LoadsConfigFromParentDir(t *testing.T) {
	rootDir := t.TempDir()
	subDir := filepath.Join(rootDir, "cli")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	configPath := filepath.Join(rootDir, ".xylem.yml")
	if err := cmdInit(configPath, false); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}

	// Also create a minimal state dir so Queue.New doesn't fail.
	stateDir := filepath.Join(rootDir, ".xylem", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll state: %v", err)
	}

	origWD, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(origWD) }) //nolint:errcheck
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	// Use daemon stop — it's in skipTooling so no git/gh tooling check runs.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"daemon", "stop"})

	// Should not fail with "no such file" — walk-up finds config at rootDir.
	if err := cmd.Execute(); err != nil {
		t.Errorf("Execute() error = %v (expected config to be found via walk-up)", err)
	}

	// Confirm PersistentPreRunE actually populated deps from the walked-up config.
	if deps == nil {
		t.Fatal("deps is nil: PersistentPreRunE did not run or failed silently")
	}
	if deps.cfg == nil {
		t.Fatal("deps.cfg is nil: config was not loaded")
	}
}
