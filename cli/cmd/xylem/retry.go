package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
)

func newRetryCmd() *cobra.Command {
	var fromScratch bool
	cmd := &cobra.Command{
		Use:   "retry <vessel-id>",
		Short: "Retry a failed vessel with failure context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdRetry(deps.q, deps.cfg, args[0], fromScratch)
		},
	}
	cmd.Flags().BoolVar(&fromScratch, "from-scratch", false, "Re-execute all phases from scratch instead of resuming")
	return cmd
}

func cmdRetry(q *queue.Queue, cfg *config.Config, id string, fromScratch bool) error {
	vessel, err := q.FindByID(id)
	if err != nil {
		return fmt.Errorf("retry error: %w", err)
	}

	if vessel.State != queue.StateFailed && vessel.State != queue.StateTimedOut {
		return fmt.Errorf("error: vessel %s is not in a retryable state (current: %s)", id, vessel.State)
	}
	if err := recovery.RetryAuthorized(cfg.StateDir, vessel.ID); err != nil {
		return fmt.Errorf("retry error: %w", err)
	}

	var artifact *recovery.Artifact
	if cfg != nil && cfg.StateDir != "" {
		loaded, loadErr := recovery.LoadForVessel(cfg.StateDir, vessel.ID)
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			return fmt.Errorf("load recovery artifact: %w", loadErr)
		}
		if loadErr == nil {
			artifact = loaded
		}
	}

	now := commandNow()
	newVessel := recovery.NextRetryVessel(*vessel, *vessel, artifact, q, now, "decision")

	// Resume from failed phase unless --from-scratch or no worktree exists
	if !fromScratch && vessel.WorktreePath != "" {
		newVessel.CurrentPhase = vessel.CurrentPhase
		newVessel.WorktreePath = vessel.WorktreePath
		newVessel.PhaseOutputs = rewritePhaseOutputs(vessel.PhaseOutputs, vessel.ID, newVessel.ID)

		if err := copyPhaseOutputFiles(cfg.StateDir, vessel.ID, newVessel.ID); err != nil {
			return fmt.Errorf("copy phase outputs: %w", err)
		}
	}

	if _, err := q.Enqueue(newVessel); err != nil {
		return fmt.Errorf("enqueue retry: %w", err)
	}
	if err := recovery.UpdateRetryOutcome(cfg.StateDir, vessel.ID, "enqueued"); err != nil {
		return fmt.Errorf("record retry outcome: %w", err)
	}
	fmt.Printf("Created retry vessel %s (retrying %s)\n", newVessel.ID, vessel.ID)
	return nil
}

// rewritePhaseOutputs copies the PhaseOutputs map, replacing the old vessel ID
// in file paths with the new vessel ID.
func rewritePhaseOutputs(outputs map[string]string, oldID, newID string) map[string]string {
	if len(outputs) == 0 {
		return nil
	}
	rewritten := make(map[string]string, len(outputs))
	oldSeg := string(filepath.Separator) + oldID + string(filepath.Separator)
	newSeg := string(filepath.Separator) + newID + string(filepath.Separator)
	for k, v := range outputs {
		rewritten[k] = strings.Replace(v, oldSeg, newSeg, 1)
	}
	return rewritten
}

// copyPhaseOutputFiles copies all files from the original vessel's phase output
// directory to the retry vessel's phase output directory.
func copyPhaseOutputFiles(stateDir, oldID, newID string) error {
	srcDir := config.RuntimePath(stateDir, "phases", oldID)
	dstDir := config.RuntimePath(stateDir, "phases", newID)

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no outputs to copy
		}
		return fmt.Errorf("read phase dir: %w", err)
	}

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("create phase dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := copyFile(
			filepath.Join(srcDir, entry.Name()),
			filepath.Join(dstDir, entry.Name()),
		); err != nil {
			return fmt.Errorf("copy %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func retryID(originalID string, q *queue.Queue) string { return recovery.RetryID(originalID, q) }
