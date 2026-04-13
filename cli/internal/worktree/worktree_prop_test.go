package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestPropRemoveDeletesExactBranchName(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		repoRoot, err := os.MkdirTemp("", "xylem-worktree-remove-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(repoRoot)

		branch := rapid.StringMatching(`[a-z0-9-]{1,8}(?:/[a-z0-9-]{1,8}){1,3}`).Draw(rt, "branch")
		segments := strings.Split(branch, "/")
		worktreeRel := filepath.Join(append([]string{".xylem", "worktrees"}, segments...)...)
		worktreeAbs := filepath.Join(append([]string{repoRoot, ".xylem", "worktrees"}, segments...)...)

		r := newMock()
		r.setOutput("git worktree list --porcelain", []byte(fmt.Sprintf("worktree %s\nHEAD abc123\nbranch refs/heads/%s\n\n", worktreeAbs, branch)))

		m := New(repoRoot, r)
		if err := m.Remove(context.Background(), worktreeRel); err != nil {
			rt.Fatalf("Remove() error = %v", err)
		}
		if !r.called("git", "branch", "-D", branch) {
			rt.Fatalf("expected branch deletion for %q, calls = %v", branch, r.calls)
		}
		if truncated := filepath.Base(branch); truncated != branch && r.called("git", "branch", "-D", truncated) {
			rt.Fatalf("unexpected truncated branch deletion for %q, calls = %v", truncated, r.calls)
		}
	})
}
