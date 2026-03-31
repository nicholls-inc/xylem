package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/anomaly"
	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show queue state and vessel summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")
			stateFilter, _ := cmd.Flags().GetString("state")
			return cmdStatus(deps.q, deps.cfg.StateDir, jsonMode, stateFilter)
		},
	}
	cmd.Flags().Bool("json", false, "Output as JSON")
	cmd.Flags().String("state", "", "Filter by vessel state")
	return cmd
}

// statusOutput is the JSON representation of the status command output.
type statusOutput struct {
	Vessels   []queue.Vessel    `json:"vessels"`
	Anomalies []anomaly.Anomaly `json:"anomalies"`
}

func cmdStatus(q *queue.Queue, stateDir string, jsonMode bool, stateFilter string) error {
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

	// Read anomalies; treat read errors as non-fatal.
	anomalies, anomalyErr := anomaly.Read(stateDir)
	if anomalyErr != nil {
		fmt.Fprintf(os.Stderr, "warn: could not read anomalies: %v\n", anomalyErr)
		anomalies = []anomaly.Anomaly{}
	}

	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if vessels == nil {
			vessels = []queue.Vessel{}
		}
		if anomalies == nil {
			anomalies = []anomaly.Anomaly{}
		}
		enc.Encode(statusOutput{Vessels: vessels, Anomalies: anomalies}) //nolint:errcheck
		return nil
	}

	if len(vessels) == 0 {
		fmt.Println("No vessels in queue.")
	} else {
		fmt.Printf("%-14s  %-14s  %-20s  %-10s  %-30s  %-12s  %s\n",
			"ID", "Source", "Workflow", "State", "Info", "Started", "Duration")
		fmt.Printf("%-14s  %-14s  %-20s  %-10s  %-30s  %-12s  %s\n",
			"----", "------", "-----", "-----", "----", "-------", "--------")

		counts := map[queue.VesselState]int{}
		for _, j := range vessels {
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
			info := vesselInfo(j)
			fmt.Printf("%-14s  %-14s  %-20s  %-10s  %-30s  %-12s  %s\n",
				j.ID, j.Source, wf, string(j.State), info, started, duration)
		}

		fmt.Printf("\nSummary: %d pending, %d running, %d completed, %d failed, %d cancelled, %d waiting, %d timed_out\n",
			counts[queue.StatePending], counts[queue.StateRunning],
			counts[queue.StateCompleted], counts[queue.StateFailed],
			counts[queue.StateCancelled], counts[queue.StateWaiting],
			counts[queue.StateTimedOut])
	}

	// Show anomalies from the last 24 hours.
	cutoff := time.Now().Add(-24 * time.Hour)
	var recent []anomaly.Anomaly
	for _, a := range anomalies {
		if a.Timestamp.After(cutoff) {
			recent = append(recent, a)
		}
	}
	if len(recent) > 0 {
		fmt.Printf("\nAnomalies (last 24h):\n")
		for _, a := range recent {
			fmt.Printf("  %-10s  %-30s  %-14s  %s\n",
				string(a.Severity), string(a.Type), a.VesselID, a.Detail)
		}
	}

	return nil
}

// vesselInfo returns additional context for the Info column based on vessel state.
func vesselInfo(v queue.Vessel) string {
	if v.State == queue.StateWaiting && v.WaitingFor != "" {
		elapsed := "unknown"
		if v.WaitingSince != nil {
			elapsed = time.Since(*v.WaitingSince).Round(time.Second).String()
		}
		return fmt.Sprintf("waiting for %q (%s)", v.WaitingFor, elapsed)
	}
	return ""
}

func pauseMarkerPath(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir, "paused")
}

func isPaused(cfg *config.Config) bool {
	_, err := os.Stat(pauseMarkerPath(cfg))
	return err == nil
}
