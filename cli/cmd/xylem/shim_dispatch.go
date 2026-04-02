package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/dtushim"
)

func newShimDispatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "shim-dispatch <shim> [args...]",
		Short:              "Dispatch an internal DTU shim",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("shim name is required")
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			code := dtushim.Execute(ctx, args[0], args[1:], os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Environ())
			if code != 0 {
				return &exitError{code: code}
			}
			return nil
		},
	}
}
