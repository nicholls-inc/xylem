package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
)

func newRecoveryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recovery",
		Short: "Inspect and update persisted recovery decisions",
	}
	cmd.AddCommand(newRecoveryRefreshCmd())
	return cmd
}

func newRecoveryRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <vessel-id>",
		Short: "Refresh a suppressed recovery decision so the vessel can be retried",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdRecoveryRefresh(deps.cfg, args[0])
		},
	}
}

func cmdRecoveryRefresh(cfg *config.Config, vesselID string) error {
	if cfg == nil {
		return fmt.Errorf("recovery refresh: config must not be nil")
	}
	artifact, err := recovery.RefreshRetryDecisionForVessel(cfg.StateDir, vesselID, recovery.RefreshOptions{
		ReviewedAt: commandNow(),
	})
	if err != nil {
		return fmt.Errorf("recovery refresh: %w", err)
	}
	fmt.Printf(
		"Refreshed recovery decision for %s; retry is now allowed with action %s\n",
		artifact.VesselID,
		artifact.RecoveryAction,
	)
	return nil
}
