package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show queue state and vessel summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")
			stateFilter, _ := cmd.Flags().GetString("state")
			return cmdStatus(deps.cfg, deps.q, jsonMode, stateFilter)
		},
	}
	cmd.Flags().Bool("json", false, "Output as JSON")
	cmd.Flags().String("state", "", "Filter by vessel state")
	return cmd
}

type statusRow struct {
	queue.Vessel
	Health           string                 `json:"health"`
	Anomalies        []runner.VesselAnomaly `json:"anomalies,omitempty"`
	EstimatedCostUSD float64                `json:"estimated_cost_usd,omitempty"`
	UsageSource      string                 `json:"usage_source,omitempty"`
	BudgetWarning    bool                   `json:"budget_warning,omitempty"`
}

func cmdStatus(cfg *config.Config, q *queue.Queue, jsonMode bool, stateFilter string) error {
	var vessels []queue.Vessel
	var err error
	if stateFilter != "" {
		vessels, err = q.ListByState(queue.VesselState(stateFilter))
	} else {
		vessels, err = q.List()
	}
	if err != nil {
		return fmt.Errorf("error reading queue: %w", err)
	}

	var summaries map[string]*runner.VesselSummary
	if cfg != nil && cfg.StateDir != "" {
		ids := make([]string, len(vessels))
		for i, vessel := range vessels {
			ids[i] = vessel.ID
		}
		summaries, err = runner.LoadVesselSummaries(cfg.StateDir, ids)
		if err != nil {
			return fmt.Errorf("load vessel summaries: %w", err)
		}
	}

	rows := make([]statusRow, len(vessels))
	for i, vessel := range vessels {
		status := runner.AnalyzeVesselStatus(vessel, summaries[vessel.ID])
		rows[i] = statusRow{
			Vessel:           vessel,
			Health:           string(status.Health),
			Anomalies:        status.Anomalies,
			EstimatedCostUSD: summaryCost(summaries[vessel.ID]),
			UsageSource:      summaryUsageSource(summaries[vessel.ID]),
			BudgetWarning:    summaries[vessel.ID] != nil && summaries[vessel.ID].BudgetWarning,
		}
	}
	fleet := runner.AnalyzeFleetStatus(vessels, summaries)

	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rows) //nolint:errcheck
		return nil
	}

	if len(vessels) == 0 {
		fmt.Println("No vessels in queue.")
		return nil
	}

	fmt.Printf("%-14s  %-14s  %-20s  %-10s  %-10s  %-12s  %-42s  %-12s  %s\n",
		"ID", "Source", "Workflow", "State", "Health", "Cost", "Info", "Started", "Duration")
	fmt.Printf("%-14s  %-14s  %-20s  %-10s  %-10s  %-12s  %-42s  %-12s  %s\n",
		"----", "------", "-----", "-----", "------", "----", "----", "-------", "--------")

	counts := map[queue.VesselState]int{}
	for i, j := range vessels {
		counts[j.State]++
		started := "—"
		duration := "—"
		if j.StartedAt != nil {
			started = j.StartedAt.UTC().Format("15:04 UTC")
			end := time.Now()
			if j.EndedAt != nil {
				end = *j.EndedAt
			}
			duration = end.Sub(*j.StartedAt).Round(time.Second).String()
		}
		wf := j.Workflow
		if wf == "" {
			wf = "(prompt)"
		}
		info := vesselInfo(j, summaries[j.ID], rows[i].Anomalies)
		fmt.Printf("%-14s  %-14s  %-20s  %-10s  %-10s  %-12s  %-42s  %-12s  %s\n",
			j.ID, j.Source, wf, string(j.State), rows[i].Health, formatSummaryCost(summaries[j.ID]), info, started, duration)
	}

	fmt.Printf("\nSummary: %d pending, %d running, %d completed, %d failed, %d cancelled, %d waiting, %d timed_out\n",
		counts[queue.StatePending], counts[queue.StateRunning],
		counts[queue.StateCompleted], counts[queue.StateFailed],
		counts[queue.StateCancelled], counts[queue.StateWaiting],
		counts[queue.StateTimedOut])
	fmt.Printf("Health: %d healthy, %d degraded, %d unhealthy\n",
		fleet.Healthy, fleet.Degraded, fleet.Unhealthy)
	if len(fleet.Patterns) > 0 {
		fmt.Printf("Patterns: %s\n", runner.FormatFleetPatterns(fleet.Patterns))
	}
	return nil
}

// vesselInfo returns additional context for the Info column based on vessel state.
func vesselInfo(v queue.Vessel, summary *runner.VesselSummary, anomalies []runner.VesselAnomaly) string {
	parts := make([]string, 0, len(anomalies)+1)
	if v.State == queue.StateWaiting && v.WaitingFor != "" {
		elapsed := "unknown"
		if v.WaitingSince != nil {
			elapsed = time.Since(*v.WaitingSince).Round(time.Second).String()
		}
		parts = append(parts, fmt.Sprintf("waiting for %q (%s)", v.WaitingFor, elapsed))
	}
	for _, anomaly := range anomalies {
		if anomaly.Code == "waiting_on_gate" {
			continue
		}
		parts = append(parts, anomaly.Message)
	}
	if summary != nil && summary.UsageUnavailableReason != "" && summary.UsageSource == "unavailable" {
		parts = append(parts, summary.UsageUnavailableReason)
	}
	return strings.Join(parts, "; ")
}

func summaryCost(summary *runner.VesselSummary) float64 {
	if summary == nil {
		return 0
	}
	return summary.TotalCostUSDEst
}

func summaryUsageSource(summary *runner.VesselSummary) string {
	if summary == nil {
		return ""
	}
	return string(summary.UsageSource)
}

func formatSummaryCost(summary *runner.VesselSummary) string {
	if summary == nil {
		return "—"
	}
	switch summary.UsageSource {
	case "estimated", "provider":
		return fmt.Sprintf("$%.4f", summary.TotalCostUSDEst)
	case "not_applicable", "unavailable":
		return "n/a"
	default:
		if summary.TotalCostUSDEst > 0 {
			return fmt.Sprintf("$%.4f", summary.TotalCostUSDEst)
		}
		return "—"
	}
}

func pauseMarkerPath(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir, "paused")
}

func isPaused(cfg *config.Config) bool {
	_, err := os.Stat(pauseMarkerPath(cfg))
	return err == nil
}
