package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"pgregory.net/rapid"
)

func TestPropDaemonUpgradeTargetAlwaysUsesWorkingDirectory(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		baseDir, err := os.MkdirTemp("", "xylem-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(baseDir)
		resolvedBaseDir, err := filepath.EvalSymlinks(baseDir)
		if err != nil {
			rt.Fatalf("EvalSymlinks(%q) error = %v", baseDir, err)
		}

		oldWd, err := os.Getwd()
		if err != nil {
			rt.Fatalf("Getwd() error = %v", err)
		}
		if err := os.Chdir(baseDir); err != nil {
			rt.Fatalf("Chdir(%q) error = %v", baseDir, err)
		}
		defer func() {
			if err := os.Chdir(oldWd); err != nil {
				t.Fatalf("restore working directory: %v", err)
			}
		}()

		worktreeSegments := []string{
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "worktree-seg-1"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "worktree-seg-2"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "worktree-seg-3"),
		}
		executableASegments := []string{
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "binary-a-seg-1"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "binary-a-seg-2"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "binary-a-seg-3"),
		}
		executableBSegments := []string{
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "binary-b-seg-1"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "binary-b-seg-2"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "binary-b-seg-3"),
		}

		workingDir := filepath.Join(worktreeSegments...)
		executablePathA := filepath.Join(append([]string{resolvedBaseDir}, executableASegments...)...)
		executablePathB := filepath.Join(append([]string{resolvedBaseDir}, executableBSegments...)...)
		targetA, err := daemonUpgradeTargetFromPaths(workingDir, executablePathA)
		if err != nil {
			rt.Fatalf("daemonUpgradeTargetFromPaths() error = %v", err)
		}
		targetB, err := daemonUpgradeTargetFromPaths(workingDir, executablePathB)
		if err != nil {
			rt.Fatalf("daemonUpgradeTargetFromPaths() error = %v", err)
		}

		wantRepoDir, err := filepath.Abs(filepath.Join(append([]string{resolvedBaseDir}, worktreeSegments...)...))
		if err != nil {
			rt.Fatalf("filepath.Abs() error = %v", err)
		}
		if targetA.repoDir != wantRepoDir {
			rt.Fatalf("targetA.repoDir = %q, want %q", targetA.repoDir, wantRepoDir)
		}
		if targetB.repoDir != wantRepoDir {
			rt.Fatalf("targetB.repoDir = %q, want %q", targetB.repoDir, wantRepoDir)
		}
		if targetA.repoDir != targetB.repoDir {
			rt.Fatalf("repoDir depended on executable path: targetA=%q targetB=%q", targetA.repoDir, targetB.repoDir)
		}
		if targetA.executablePath != executablePathA {
			rt.Fatalf("targetA.executablePath = %q, want %q", targetA.executablePath, executablePathA)
		}
		if targetB.executablePath != executablePathB {
			rt.Fatalf("targetB.executablePath = %q, want %q", targetB.executablePath, executablePathB)
		}
		if targetA.repoDir == filepath.Dir(filepath.Dir(executablePathA)) {
			rt.Fatalf("repoDir unexpectedly matched first binary repo: repoDir=%q executablePath=%q", targetA.repoDir, executablePathA)
		}
		if targetB.repoDir == filepath.Dir(filepath.Dir(executablePathB)) {
			rt.Fatalf("repoDir unexpectedly matched second binary repo: repoDir=%q executablePath=%q", targetB.repoDir, executablePathB)
		}
	})
}

func TestDaemonProfileNamesDefaultsToCoreWhenConfigIsNil(t *testing.T) {
	t.Parallel()

	got := daemonProfileNames(nil)
	if len(got) != 1 || got[0] != "core" {
		t.Fatalf("daemonProfileNames(nil) = %v, want [core]", got)
	}
}

func TestPropDaemonProfileNamesNormalizesConfiguredProfiles(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		rawNames := rapid.SliceOf(rapid.SampledFrom([]string{
			"",
			" ",
			"core",
			" core ",
			"self-hosting-xylem",
			" self-hosting-xylem ",
		})).Draw(t, "profiles")

		got := daemonProfileNames(&config.Config{Profiles: rawNames})

		expectedIndex := 0
		for _, raw := range rawNames {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			if expectedIndex >= len(got) {
				t.Fatalf("daemonProfileNames(%v) returned too few names: got %v", rawNames, got)
			}
			if got[expectedIndex] != trimmed {
				t.Fatalf("daemonProfileNames(%v)[%d] = %q, want %q", rawNames, expectedIndex, got[expectedIndex], trimmed)
			}
			if got[expectedIndex] == "" || got[expectedIndex] != strings.TrimSpace(got[expectedIndex]) {
				t.Fatalf("daemonProfileNames(%v) returned untrimmed name %q", rawNames, got[expectedIndex])
			}
			expectedIndex++
		}

		if expectedIndex == 0 {
			if len(got) != 1 || got[0] != "core" {
				t.Fatalf("daemonProfileNames(%v) = %v, want [core]", rawNames, got)
			}
			return
		}

		if len(got) != expectedIndex {
			t.Fatalf("daemonProfileNames(%v) returned extra names: got %v", rawNames, got)
		}
	})
}
