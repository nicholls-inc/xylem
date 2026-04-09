package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the xylem binary version",
		Long: `Prints the commit hash embedded at build time via Go's VCS integration.

This is useful for diagnosing stale-daemon issues: compare the output to
` + "`git log --oneline -1 origin/main`" + ` to confirm the daemon is running the
latest binary. Also prints (modified) if the binary was built from a dirty
working tree.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(buildInfo())
			return nil
		},
	}
}
