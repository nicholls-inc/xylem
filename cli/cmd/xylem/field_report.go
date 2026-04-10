package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/nicholls-inc/xylem/cli/internal/fieldreport"
	"github.com/spf13/cobra"
)

func newFieldReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "field-report",
		Short: "Deterministic helpers for field report generation",
	}
	cmd.AddCommand(newFieldReportGenerateCmd())
	return cmd
}

func newFieldReportGenerateCmd() *cobra.Command {
	var outputPath string
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate an anonymized field report from local vessel data",
		Long: `Reads vessel summaries from the state directory and produces a
privacy-safe, anonymized field report in JSON format. The report
contains only aggregate statistics — no repo names, issue titles,
prompt content, or individual vessel IDs.

Exit code 0 on success, 2 if there is insufficient data to produce
a meaningful report (fewer than 5 vessel summaries).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := deps.cfg

			if !cfg.TelemetryEnabled() {
				fmt.Fprintln(os.Stderr, "telemetry is disabled; skipping field report generation")
				return nil
			}

			opts := fieldreport.Options{
				XylemVersion:   buildInfo(),
				ProfileVersion: 2,
				Extended:       cfg.Telemetry.Extended,
			}

			if cfg.Telemetry.Extended && cfg.Repo != "" {
				opts.RepoSlug = cfg.Repo
			} else if cfg.Telemetry.Extended {
				// Try to find repo from sources
				for _, src := range cfg.Sources {
					if src.Repo != "" {
						opts.RepoSlug = src.Repo
						break
					}
				}
			}

			report, err := fieldreport.Generate(cfg.StateDir, opts)
			if err != nil {
				if err == fieldreport.ErrInsufficientData {
					fmt.Fprintln(os.Stderr, "insufficient vessel data for field report (need at least 5 runs)")
					os.Exit(2)
				}
				return fmt.Errorf("generate field report: %w", err)
			}

			if outputPath != "" {
				data, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal field report: %w", err)
				}
				if err := os.WriteFile(outputPath, data, 0o644); err != nil {
					return fmt.Errorf("write field report to %s: %w", outputPath, err)
				}
				fmt.Fprintf(os.Stderr, "field report written to %s\n", outputPath)
				return nil
			}

			path, err := fieldreport.Save(cfg.StateDir, report)
			if err != nil {
				return fmt.Errorf("save field report: %w", err)
			}
			fmt.Fprintf(os.Stderr, "field report saved to %s\n", path)

			// Also write to stdout for workflow consumption
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal field report: %w", err)
			}
			fmt.Println(string(data))
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Write report to specific file instead of default location")
	return cmd
}
