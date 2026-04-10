package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

func TestPropNormalizePathMatchesAbsoluteEquivalent(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		repoRoot, err := os.MkdirTemp("", "xylem-worktree-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(repoRoot)

		m := &Manager{RepoRoot: repoRoot}
		segments := []string{
			".claude",
			"worktrees",
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "seg-1"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "seg-2"),
			rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, "seg-3"),
		}

		relativePath := filepath.Join(segments...)
		absolutePath := filepath.Join(append([]string{repoRoot}, segments...)...)

		gotRelative := m.NormalizePath(relativePath)
		gotAbsolute := m.NormalizePath(absolutePath)
		if gotRelative != gotAbsolute {
			rt.Fatalf("NormalizePath(relative) = %q, NormalizePath(absolute) = %q", gotRelative, gotAbsolute)
		}
	})
}
