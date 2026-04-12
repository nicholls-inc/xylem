package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/daemonhealth"
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
	daemonSnapshot, daemonErr := loadDaemonSnapshot(cfg)
	if daemonErr != nil {
		return daemonErr
	}

	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rows) //nolint:errcheck
		return nil
	}

	counts := map[queue.VesselState]int{}
	for _, j := range vessels {
		counts[j.State]++
	}

	fmt.Printf("Queue: %d pending, %d running, %d waiting | %d completed, %d failed, %d cancelled, %d timed_out\n",
		counts[queue.StatePending], counts[queue.StateRunning], counts[queue.StateWaiting],
		counts[queue.StateCompleted], counts[queue.StateFailed], counts[queue.StateCancelled],
		counts[queue.StateTimedOut])
	renderDaemonHealth(daemonSnapshot)
	if len(vessels) == 0 {
		fmt.Println("No vessels in queue.")
		return nil
	}

	fmt.Printf("%-14s  %-14s  %-20s  %-10s  %-10s  %-12s  %-42s  %-12s  %s\n",
		"ID", "Source", "Workflow", "State", "Health", "Cost", "Info", "Started", "Duration")
	fmt.Printf("%-14s  %-14s  %-20s  %-10s  %-10s  %-12s  %-42s  %-12s  %s\n",
		"----", "------", "-----", "-----", "------", "----", "----", "-------", "--------")

	for i, j := range vessels {
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

	fmt.Printf("\nHealth: %d healthy, %d degraded, %d unhealthy\n",
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
	return config.RuntimePath(cfg.StateDir, "paused")
}

func isPaused(cfg *config.Config) bool {
	_, err := os.Stat(pauseMarkerPath(cfg))
	return err == nil
}

func loadDaemonSnapshot(cfg *config.Config) (*daemonhealth.Snapshot, error) {
	if cfg == nil || cfg.StateDir == "" {
		return nil, nil
	}
	snapshot, err := daemonhealth.Load(cfg.StateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load daemon health: %w", err)
	}
	return snapshot, nil
}

func renderDaemonHealth(snapshot *daemonhealth.Snapshot) {
	if snapshot == nil {
		return
	}
	fmt.Println("Health:")
	if daemonProcessAlive(snapshot.PID) {
		fmt.Printf("  %s Daemon alive (pid=%d, uptime=%s)\n", daemonHealthIcon(daemonhealth.LevelOK), snapshot.PID, time.Since(snapshot.StartedAt).Round(time.Second))
	} else {
		fmt.Printf("  %s Daemon not running (pid=%d, last heartbeat=%s)\n", daemonHealthIcon(daemonhealth.LevelCritical), snapshot.PID, snapshot.UpdatedAt.UTC().Format("15:04:05"))
	}
	if !snapshot.LastUpgradeAt.IsZero() {
		fmt.Printf("  %s Auto-upgrade current (binary=%s, last=%s)\n", daemonHealthIcon(daemonhealth.LevelOK), snapshot.Binary, snapshot.LastUpgradeAt.UTC().Format("15:04:05"))
	}
	checks := append([]daemonhealth.Check(nil), snapshot.Checks...)
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Level == checks[j].Level {
			return checks[i].Code < checks[j].Code
		}
		return daemonLevelRank(checks[i].Level) > daemonLevelRank(checks[j].Level)
	})
	for _, check := range checks {
		fmt.Printf("  %s %s\n", daemonHealthIcon(check.Level), check.Message)
	}
}

func daemonProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func daemonHealthIcon(level daemonhealth.Level) string {
	switch level {
	case daemonhealth.LevelCritical:
		return "✗"
	case daemonhealth.LevelWarning:
		return "⚠"
	default:
		return "✓"
	}
}

func daemonLevelRank(level daemonhealth.Level) int {
	switch level {
	case daemonhealth.LevelCritical:
		return 3
	case daemonhealth.LevelWarning:
		return 2
	default:
		return 1
	}
}
