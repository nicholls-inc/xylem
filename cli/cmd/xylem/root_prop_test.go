package main

import (
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

func TestPropFindConfigPath_NeverPanics(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		path := rapid.String().Draw(rt, "path")
		// Must not panic regardless of input.
		_ = findConfigPath(path)
	})
}

func TestPropFindConfigPath_AbsoluteInputPassthrough(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate a plausible absolute path by prepending a separator.
		rel := rapid.StringMatching(`[a-zA-Z0-9_.-]{1,30}`).Draw(rt, "rel")
		abs := string(filepath.Separator) + rel
		got := findConfigPath(abs)
		if got != abs {
			rt.Fatalf("findConfigPath(%q) = %q, want passthrough %q", abs, got, abs)
		}
	})
}

func TestPropFindConfigPath_FoundFileIsAccessible(t *testing.T) {
	// Capture CWD once, before rapid.Check, so we can restore between iterations.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origWD) }) //nolint:errcheck

	rapid.Check(t, func(rt *rapid.T) {
		// Restore CWD after each iteration so the next iteration starts clean.
		defer os.Chdir(origWD) //nolint:errcheck

		depth := rapid.IntRange(1, 4).Draw(rt, "depth")

		rootDir := t.TempDir()

		// Build a leaf directory nested `depth` levels under rootDir.
		leafDir := rootDir
		for i := 0; i < depth; i++ {
			leafDir = filepath.Join(leafDir, "sub")
		}
		if err := os.MkdirAll(leafDir, 0o755); err != nil {
			rt.Fatalf("MkdirAll: %v", err)
		}

		// Place .xylem.yml at rootDir (the top of the temp tree).
		configFile := filepath.Join(rootDir, ".xylem.yml")
		if err := os.WriteFile(configFile, []byte("repo: test\n"), 0o644); err != nil {
			rt.Fatalf("WriteFile: %v", err)
		}

		if err := os.Chdir(leafDir); err != nil {
			rt.Fatalf("Chdir: %v", err)
		}

		got := findConfigPath(".xylem.yml")

		if _, err := os.Stat(got); err != nil {
			rt.Fatalf("returned path %q is not accessible: %v", got, err)
		}
	})
}
