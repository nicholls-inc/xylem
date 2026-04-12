package reporter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/daemonhealth"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// StatusReport holds a complete status snapshot.
type StatusReport struct {
	Timestamp      time.Time
	Daemon         DaemonStatus
	Vessels        VesselMetrics
	Fleet          FleetHealth
	ActiveVessels  []ActiveVessel
	RecentFailures []RecentFailure
	Warnings       []string
}

// DaemonStatus holds the daemon's current health state.
type DaemonStatus struct {
	PID           int
	StartedAt     time.Time
	Uptime        time.Duration
	Binary        string
	LastUpgradeAt time.Time
	Checks        []daemonhealth.Check
}

// VesselMetrics holds counts by vessel state.
type VesselMetrics struct {
	Pending   int
	Running   int
	Completed int
	Failed    int
	TimedOut  int
	Waiting   int
	Cancelled int
	Total     int
}

// FleetHealth is a local representation of fleet-level health metrics.
// It mirrors runner.FleetStatusReport to avoid an import cycle (runner
// already imports reporter for issue commenting).
type FleetHealth struct {
	Healthy   int
	Degraded  int
	Unhealthy int
	Patterns  []FleetPattern
}

// FleetPattern pairs an anomaly code with its occurrence count.
type FleetPattern struct {
	Code  string
	Count int
}

// ActiveVessel describes a currently running vessel.
type ActiveVessel struct {
	ID       string
	Phase    string
	Duration time.Duration
	Workflow string
}

// RecentFailure describes a vessel that failed or timed out recently.
type RecentFailure struct {
	ID        string
	Error     string
	Phase     string
	Timestamp time.Time
}

// StatusDeps provides the status collector's dependencies via interfaces.
type StatusDeps struct {
	StateDir string
	Queue    QueueReader

	// FleetAnalyzer computes fleet-level health from the vessel list. When
	// nil, the Fleet field is left at its zero value. Callers typically wire
	// this to a closure over runner.LoadVesselSummaries and
	// runner.AnalyzeFleetStatus.
	FleetAnalyzer func(stateDir string, vessels []queue.Vessel) FleetHealth
}

// QueueReader is the subset of queue.Queue needed by the status collector.
type QueueReader interface {
	List() ([]queue.Vessel, error)
}

// CollectStatus gathers a complete status snapshot from the daemon's state
// files and queue. It does not send notifications; the notify package handles
// that.
func CollectStatus(ctx context.Context, deps StatusDeps, now time.Time) StatusReport {
	report := StatusReport{Timestamp: now}

	// Daemon health.
	if snapshot, err := daemonhealth.Load(deps.StateDir); err == nil {
		report.Daemon = DaemonStatus{
			PID:           snapshot.PID,
			StartedAt:     snapshot.StartedAt,
			Uptime:        now.Sub(snapshot.StartedAt),
			Binary:        snapshot.Binary,
			LastUpgradeAt: snapshot.LastUpgradeAt,
			Checks:        snapshot.Checks,
		}
	}

	// Queue state.
	vessels, err := deps.Queue.List()
	if err != nil {
		report.Warnings = append(report.Warnings, "Failed to read queue: "+err.Error())
		return report
	}

	for _, v := range vessels {
		switch v.State {
		case queue.StatePending:
			report.Vessels.Pending++
		case queue.StateRunning:
			report.Vessels.Running++
		case queue.StateCompleted:
			report.Vessels.Completed++
		case queue.StateFailed:
			report.Vessels.Failed++
		case queue.StateTimedOut:
			report.Vessels.TimedOut++
		case queue.StateWaiting:
			report.Vessels.Waiting++
		case queue.StateCancelled:
			report.Vessels.Cancelled++
		}
		report.Vessels.Total++
	}

	// Active vessels (running).
	for _, v := range vessels {
		if v.State != queue.StateRunning {
			continue
		}
		var dur time.Duration
		if v.StartedAt != nil {
			dur = now.Sub(*v.StartedAt)
		}
		report.ActiveVessels = append(report.ActiveVessels, ActiveVessel{
			ID:       v.ID,
			Phase:    v.FailedPhase,
			Duration: dur,
			Workflow: v.Workflow,
		})
	}

	// Recent failures (last 24h).
	cutoff := now.Add(-24 * time.Hour)
	for _, v := range vessels {
		if v.State != queue.StateFailed && v.State != queue.StateTimedOut {
			continue
		}
		if v.EndedAt == nil || !v.EndedAt.After(cutoff) {
			continue
		}
		report.RecentFailures = append(report.RecentFailures, RecentFailure{
			ID:        v.ID,
			Error:     v.Error,
			Phase:     v.FailedPhase,
			Timestamp: *v.EndedAt,
		})
	}

	// Fleet health (caller provides the analyzer to avoid runner import cycle).
	if deps.FleetAnalyzer != nil {
		report.Fleet = deps.FleetAnalyzer(deps.StateDir, vessels)
	}

	// Warnings.
	if report.Vessels.Failed > 5 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%d failed vessels in queue", report.Vessels.Failed))
	}
	if report.Vessels.Pending > 10 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%d pending vessels in backlog", report.Vessels.Pending))
	}
	for _, check := range report.Daemon.Checks {
		if check.Level == daemonhealth.LevelCritical || check.Level == daemonhealth.LevelWarning {
			report.Warnings = append(report.Warnings, check.Message)
		}
	}

	return report
}

// formatFleetPatterns renders fleet patterns as a compact string.
func formatFleetPatterns(patterns []FleetPattern) string {
	if len(patterns) == 0 {
		return ""
	}
	parts := make([]string, len(patterns))
	for i, p := range patterns {
		parts[i] = fmt.Sprintf("%s=%d", p.Code, p.Count)
	}
	return strings.Join(parts, ", ")
}
