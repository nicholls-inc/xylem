package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

func newCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove stale worktrees and old phase outputs",
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			return cmdCleanup(deps.cfg, deps.q, deps.wt, dryRun)
		},
	}
	cmd.Flags().Bool("dry-run", false, "Preview what would be removed")
	return cmd
}

func cmdCleanup(cfg *config.Config, q *queue.Queue, wt *worktree.Manager, dryRun bool) error {
	if err := cleanupWorktrees(wt, q, dryRun); err != nil {
		return err
	}

	cleanupPhaseOutputs(cfg, q, dryRun)
	compactQueue(q, dryRun)

	return nil
}

func compactQueue(q *queue.Queue, dryRun bool) {
	if dryRun {
		removed, err := q.CompactDryRun()
		if err != nil {
			slog.Warn("queue compaction dry-run check failed", "error", err)
			return
		}
		if removed > 0 {
			fmt.Printf("Would remove %d stale queue record(s) (dry-run — no changes made)\n", removed)
		}
		return
	}

	removed, err := q.Compact()
	if err != nil {
		slog.Warn("queue compaction failed", "error", err)
		return
	}
	if removed > 0 {
		fmt.Printf("Compacted queue: removed %d stale record(s)\n", removed)
	}
}

func cleanupWorktrees(wt *worktree.Manager, q *queue.Queue, dryRun bool) error {
	ctx := context.Background()
	trees, err := wt.ListXylem(ctx)
	if err != nil {
		return fmt.Errorf("error listing worktrees: %w", err)
	}
	if len(trees) == 0 {
		fmt.Println("No xylem worktrees found.")
		return nil
	}
	pruner := runner.New(nil, q, wt, nil)
	stale, err := pruner.FindStaleWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("find stale worktrees: %w", err)
	}

	removed := 0
	skipped := len(trees) - len(stale)
	for _, t := range stale {
		if dryRun {
			fmt.Printf("Would remove: %s\n", t.Path)
			removed++
			continue
		}
		if err := wt.Remove(ctx, t.Path); err != nil {
			fmt.Fprintf(os.Stderr, "error removing %s: %v\n", t.Path, err)
			continue
		}
		fmt.Printf("Removed %s\n", t.Path)
		removed++
	}

	if skipped > 0 {
		fmt.Printf("\nSkipped %d active vessel worktree(s)\n", skipped)
	}
	if dryRun {
		fmt.Printf("%d worktree(s) would be removed (dry-run — no changes made)\n", removed)
	} else {
		fmt.Printf("\nRemoved %d worktree(s)\n", removed)
	}
	return nil
}

func cleanupPhaseOutputs(cfg *config.Config, q *queue.Queue, dryRun bool) {
	phasesDir := config.RuntimePath(cfg.StateDir, "phases")
	if _, err := os.Stat(phasesDir); os.IsNotExist(err) {
		return // no phases directory yet
	}

	vessels, err := q.List()
	if err != nil {
		slog.Warn("phase cleanup failed to read queue", "error", err)
		return
	}

	cutoff := time.Now().Add(-cfg.CleanupAfterDuration())

	// Build set of terminal vessel IDs older than cutoff
	terminalIDs := make(map[string]bool)
	for _, v := range vessels {
		if v.State.IsTerminal() && v.EndedAt != nil && v.EndedAt.Before(cutoff) {
			terminalIDs[v.ID] = true
		}
	}

	entries, err := os.ReadDir(phasesDir)
	if err != nil {
		slog.Warn("phase cleanup failed to read phases directory", "path", phasesDir, "error", err)
		return
	}

	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !terminalIDs[entry.Name()] {
			continue
		}
		dirPath := filepath.Join(phasesDir, entry.Name())
		if dryRun {
			fmt.Printf("Would remove phase outputs: %s\n", dirPath)
		} else {
			if err := os.RemoveAll(dirPath); err != nil {
				slog.Warn("phase cleanup failed to remove directory", "path", dirPath, "error", err)
				continue
			}
			fmt.Printf("Removed phase outputs: %s\n", dirPath)
		}
		removed++
	}

	if removed > 0 {
		fmt.Printf("Cleaned up %d phase output directory(ies)\n", removed)
	}
}
