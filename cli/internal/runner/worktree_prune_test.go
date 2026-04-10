package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

type pruningWorktree struct {
	list       []worktree.WorktreeInfo
	removeErrs map[string]error
	removed    []string
	repoRoot   string
}

func (w *pruningWorktree) Create(_ context.Context, branchName string) (string, error) {
	return filepath.Join(".claude", "worktrees", branchName), nil
}

func (w *pruningWorktree) Remove(_ context.Context, worktreePath string) error {
	w.removed = append(w.removed, worktreePath)
	if err := w.removeErrs[worktreePath]; err != nil {
		return err
	}
	return nil
}

func (w *pruningWorktree) ListXylem(_ context.Context) ([]worktree.WorktreeInfo, error) {
	return append([]worktree.WorktreeInfo(nil), w.list...), nil
}

func (w *pruningWorktree) NormalizePath(worktreePath string) string {
	normalized := worktreePath
	if !filepath.IsAbs(normalized) {
		normalized = filepath.Join(w.repoRoot, normalized)
	}
	absPath, err := filepath.Abs(normalized)
	if err == nil {
		normalized = absPath
	}
	return filepath.Clean(normalized)
}

func TestFindStaleWorktreesNormalizesRelativeActivePaths(t *testing.T) {
	repoRoot := t.TempDir()
	q := queue.New(filepath.Join(repoRoot, "queue.jsonl"))
	now := time.Now().UTC()
	if _, err := q.Enqueue(queue.Vessel{
		ID:           "issue-1",
		Source:       "manual",
		Workflow:     "fix-bug",
		State:        queue.StatePending,
		CreatedAt:    now,
		WorktreePath: filepath.Join(".claude", "worktrees", "fix", "issue-1"),
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	wt := &pruningWorktree{
		repoRoot: repoRoot,
		list: []worktree.WorktreeInfo{
			{Path: filepath.Join(repoRoot, ".claude", "worktrees", "fix", "issue-1"), Branch: "fix/issue-1"},
			{Path: filepath.Join(repoRoot, ".claude", "worktrees", "fix", "issue-2"), Branch: "fix/issue-2"},
		},
	}
	r := New(nil, q, wt, nil)

	stale, err := r.FindStaleWorktrees(context.Background())
	if err != nil {
		t.Fatalf("FindStaleWorktrees() error = %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("len(stale) = %d, want 1", len(stale))
	}
	if got := stale[0].Path; got != filepath.Join(repoRoot, ".claude", "worktrees", "fix", "issue-2") {
		t.Fatalf("stale[0].Path = %q, want issue-2 worktree", got)
	}
}

func TestPruneStaleWorktreesRemovesOnlyDetectedStalePaths(t *testing.T) {
	repoRoot := t.TempDir()
	q := queue.New(filepath.Join(repoRoot, "queue.jsonl"))
	now := time.Now().UTC()
	if _, err := q.Enqueue(queue.Vessel{
		ID:           "issue-1",
		Source:       "manual",
		Workflow:     "fix-bug",
		State:        queue.StatePending,
		CreatedAt:    now,
		WorktreePath: filepath.Join(".claude", "worktrees", "fix", "issue-1"),
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	stalePath := filepath.Join(repoRoot, ".claude", "worktrees", "fix", "issue-2")
	wt := &pruningWorktree{
		repoRoot: repoRoot,
		list: []worktree.WorktreeInfo{
			{Path: filepath.Join(repoRoot, ".claude", "worktrees", "fix", "issue-1"), Branch: "fix/issue-1"},
			{Path: stalePath, Branch: "fix/issue-2"},
		},
		removeErrs: map[string]error{
			stalePath: errors.New("busy"),
		},
	}
	r := New(nil, q, wt, nil)

	removed := r.PruneStaleWorktrees(context.Background())
	if removed != 0 {
		t.Fatalf("PruneStaleWorktrees() removed %d, want 0", removed)
	}
	if len(wt.removed) != 1 || wt.removed[0] != stalePath {
		t.Fatalf("removed paths = %#v, want [%q]", wt.removed, stalePath)
	}
}
