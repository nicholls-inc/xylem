package runner

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

type xylemWorktreeLister interface {
	ListXylem(ctx context.Context) ([]worktree.WorktreeInfo, error)
}

type worktreePathNormalizer interface {
	NormalizePath(worktreePath string) string
}

// FindStaleWorktrees returns registered xylem worktrees that are not associated
// with any non-terminal vessel in the queue.
func (r *Runner) FindStaleWorktrees(ctx context.Context) ([]worktree.WorktreeInfo, error) {
	lister, ok := r.Worktree.(xylemWorktreeLister)
	if !ok {
		return nil, fmt.Errorf("list xylem worktrees: unsupported worktree manager %T", r.Worktree)
	}

	trees, err := lister.ListXylem(ctx)
	if err != nil {
		return nil, fmt.Errorf("list xylem worktrees: %w", err)
	}

	active, err := r.activeWorktreePaths()
	if err != nil {
		return nil, fmt.Errorf("list active worktrees: %w", err)
	}

	stale := make([]worktree.WorktreeInfo, 0, len(trees))
	for _, tree := range trees {
		if _, ok := active[r.normalizeWorktreePath(tree.Path)]; ok {
			continue
		}
		stale = append(stale, tree)
	}

	return stale, nil
}

// PruneStaleWorktrees removes stale xylem worktrees best-effort and returns the
// number successfully removed.
func (r *Runner) PruneStaleWorktrees(ctx context.Context) int {
	stale, err := r.FindStaleWorktrees(ctx)
	if err != nil {
		log.Printf("warn: find stale worktrees: %v", err)
		return 0
	}

	removed := 0
	for _, tree := range stale {
		if err := r.Worktree.Remove(ctx, tree.Path); err != nil {
			log.Printf("warn: remove stale worktree %s: %v", tree.Path, err)
			continue
		}
		removed++
	}

	return removed
}

func (r *Runner) activeWorktreePaths() (map[string]struct{}, error) {
	vessels, err := r.Queue.List()
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}

	active := make(map[string]struct{}, len(vessels))
	for _, vessel := range vessels {
		if vessel.State.IsTerminal() || vessel.WorktreePath == "" {
			continue
		}
		active[r.normalizeWorktreePath(vessel.WorktreePath)] = struct{}{}
	}

	return active, nil
}

func (r *Runner) normalizeWorktreePath(worktreePath string) string {
	if normalizer, ok := r.Worktree.(worktreePathNormalizer); ok {
		return normalizer.NormalizePath(worktreePath)
	}

	if filepath.IsAbs(worktreePath) {
		return filepath.Clean(worktreePath)
	}

	return filepath.Clean(worktreePath)
}
