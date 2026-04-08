package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

type VesselHealth string

const (
	VesselHealthHealthy   VesselHealth = "healthy"
	VesselHealthDegraded  VesselHealth = "degraded"
	VesselHealthUnhealthy VesselHealth = "unhealthy"
)

type VesselAnomaly struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type VesselStatusReport struct {
	Health    VesselHealth    `json:"health"`
	Anomalies []VesselAnomaly `json:"anomalies,omitempty"`
}

type FleetPattern struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type FleetStatusReport struct {
	Healthy   int            `json:"healthy"`
	Degraded  int            `json:"degraded"`
	Unhealthy int            `json:"unhealthy"`
	Patterns  []FleetPattern `json:"patterns,omitempty"`
}

func LoadVesselSummary(stateDir, vesselID string) (*VesselSummary, error) {
	if err := validateSummaryPathComponent(vesselID); err != nil {
		return nil, fmt.Errorf("load vessel summary: invalid vessel ID: %w", err)
	}

	data, err := os.ReadFile(summaryPath(stateDir, vesselID))
	if err != nil {
		return nil, fmt.Errorf("load vessel summary: read: %w", err)
	}

	var summary VesselSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, fmt.Errorf("load vessel summary: unmarshal: %w", err)
	}
	if summary.Phases == nil {
		summary.Phases = []PhaseSummary{}
	}

	return &summary, nil
}

func LoadVesselSummaries(stateDir string, vesselIDs []string) (map[string]*VesselSummary, error) {
	summaries := make(map[string]*VesselSummary, len(vesselIDs))
	for _, vesselID := range vesselIDs {
		if err := validateSummaryPathComponent(vesselID); err != nil {
			continue
		}
		summary, err := LoadVesselSummary(stateDir, vesselID)
		if err == nil {
			summaries[vesselID] = summary
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return nil, err
	}
	return summaries, nil
}

func AnalyzeVesselStatus(vessel queue.Vessel, summary *VesselSummary) VesselStatusReport {
	report := VesselStatusReport{
		Health:    VesselHealthHealthy,
		Anomalies: []VesselAnomaly{},
	}
	seen := make(map[string]struct{})
	addAnomaly := func(code, severity, message string) {
		if _, ok := seen[code]; ok {
			return
		}
		seen[code] = struct{}{}
		report.Anomalies = append(report.Anomalies, VesselAnomaly{
			Code:     code,
			Severity: severity,
			Message:  message,
		})
		switch severity {
		case "critical":
			report.Health = VesselHealthUnhealthy
		case "warning":
			if report.Health == VesselHealthHealthy {
				report.Health = VesselHealthDegraded
			}
		}
	}

	if summary != nil {
		switch summary.State {
		case string(queue.StateFailed):
			addAnomaly("run_failed", "critical", "run failed")
		case string(queue.StateTimedOut):
			addAnomaly("timed_out", "critical", "run timed out")
		case string(queue.StateCancelled):
			addAnomaly("cancelled", "warning", "run cancelled")
		}
		if summary.BudgetExceeded {
			severity := "warning"
			if summary.State != string(queue.StateCompleted) {
				severity = "critical"
			}
			addAnomaly("budget_exceeded", severity, "budget exceeded")
		}
		for _, phase := range summary.Phases {
			if phase.GatePassed != nil && !*phase.GatePassed {
				msg := "gate failed"
				if phase.GateType != "" {
					msg = phase.GateType + " gate failed"
				}
				if phase.Name != "" {
					msg = fmt.Sprintf("phase %q %s", phase.Name, msg)
				}
				addAnomaly("gate_failed", "critical", msg)
			}
			if phase.Status == "failed" {
				msg := "phase failed"
				if phase.Name != "" {
					msg = fmt.Sprintf("phase %q failed", phase.Name)
				}
				addAnomaly("phase_failed", "critical", msg)
			}
		}
	}

	switch vessel.State {
	case queue.StateWaiting:
		msg := "waiting on label gate"
		if vessel.WaitingFor != "" {
			msg = fmt.Sprintf("waiting for %q", vessel.WaitingFor)
		}
		addAnomaly("waiting_on_gate", "warning", msg)
	case queue.StateTimedOut:
		addAnomaly("timed_out", "critical", "run timed out")
	case queue.StateFailed:
		addAnomaly("run_failed", "critical", "run failed")
	case queue.StateCancelled:
		addAnomaly("cancelled", "warning", "run cancelled")
	}

	return report
}

func AnalyzeFleetStatus(vessels []queue.Vessel, summaries map[string]*VesselSummary) FleetStatusReport {
	report := FleetStatusReport{}
	patternCounts := make(map[string]int)
	for _, vessel := range vessels {
		status := AnalyzeVesselStatus(vessel, summaries[vessel.ID])
		switch status.Health {
		case VesselHealthHealthy:
			report.Healthy++
		case VesselHealthDegraded:
			report.Degraded++
		case VesselHealthUnhealthy:
			report.Unhealthy++
		}
		for _, anomaly := range status.Anomalies {
			patternCounts[anomaly.Code]++
		}
	}

	report.Patterns = make([]FleetPattern, 0, len(patternCounts))
	for code, count := range patternCounts {
		report.Patterns = append(report.Patterns, FleetPattern{Code: code, Count: count})
	}
	sort.Slice(report.Patterns, func(i, j int) bool {
		if report.Patterns[i].Count == report.Patterns[j].Count {
			return report.Patterns[i].Code < report.Patterns[j].Code
		}
		return report.Patterns[i].Count > report.Patterns[j].Count
	})

	return report
}

func AnomalyCodes(anomalies []VesselAnomaly) []string {
	codes := make([]string, len(anomalies))
	for i, anomaly := range anomalies {
		codes[i] = anomaly.Code
	}
	return codes
}

func FormatFleetPatterns(patterns []FleetPattern) string {
	if len(patterns) == 0 {
		return ""
	}
	parts := make([]string, len(patterns))
	for i, pattern := range patterns {
		parts[i] = fmt.Sprintf("%s=%d", pattern.Code, pattern.Count)
	}
	return strings.Join(parts, ", ")
}
