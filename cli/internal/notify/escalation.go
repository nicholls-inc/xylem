package notify

import (
	"fmt"
	"time"
)

// EscalationRule defines when an alert should fire.
type EscalationRule struct {
	Code     string
	Severity AlertSeverity
	Check    func(state FleetState) *Alert
}

// FleetState is the input to escalation checks, collected by the reporter.
type FleetState struct {
	// Vessel counts by state.
	Pending   int
	Running   int
	Completed int
	Failed    int
	TimedOut  int
	Waiting   int
	Cancelled int

	// Recent history (within evaluation window).
	RecentFailures    []VesselFailure
	RecentCompletions int

	// Health check findings from daemon.
	HealthChecks []HealthCheck

	// Queue age.
	OldestPendingAge time.Duration

	// Upgrade state.
	UpgradeOverdue   bool
	UpgradeOverdueBy time.Duration
}

// VesselFailure captures a recent vessel failure for pattern detection.
type VesselFailure struct {
	VesselID  string
	Error     string
	Timestamp time.Time
}

// HealthCheck mirrors daemonhealth.Check for escalation evaluation.
type HealthCheck struct {
	Code    string
	Level   string // "ok", "warning", "critical"
	Message string
}

// DefaultRules returns the standard escalation rules.
func DefaultRules() []EscalationRule {
	return []EscalationRule{
		{
			Code:     "auth_failure",
			Severity: SeverityCritical,
			Check: func(s FleetState) *Alert {
				authFailures := filterFailures(s.RecentFailures, isAuthError)
				if len(authFailures) < 3 {
					return nil
				}
				ids := failureIDs(authFailures)
				return &Alert{
					Severity:  SeverityCritical,
					Code:      "auth_failure",
					Title:     "Authentication Failure",
					Detail:    fmt.Sprintf("%d vessels failed with authentication errors. Credentials may need renewal.", len(authFailures)),
					Timestamp: time.Now(),
					VesselIDs: ids,
				}
			},
		},
		{
			Code:     "high_failure_rate",
			Severity: SeverityCritical,
			Check: func(s FleetState) *Alert {
				total := s.RecentCompletions + len(s.RecentFailures)
				if total < 5 {
					return nil // not enough data
				}
				failRate := float64(len(s.RecentFailures)) / float64(total)
				if failRate <= 0.5 {
					return nil
				}
				return &Alert{
					Severity:  SeverityCritical,
					Code:      "high_failure_rate",
					Title:     "High Failure Rate",
					Detail:    fmt.Sprintf("%.0f%% of recent vessels failed (%d/%d).", failRate*100, len(s.RecentFailures), total),
					Timestamp: time.Now(),
					VesselIDs: failureIDs(s.RecentFailures),
				}
			},
		},
		{
			Code:     "all_vessels_failing",
			Severity: SeverityCritical,
			Check: func(s FleetState) *Alert {
				if len(s.RecentFailures) < 5 || s.RecentCompletions > 0 {
					return nil
				}
				return &Alert{
					Severity:  SeverityCritical,
					Code:      "all_vessels_failing",
					Title:     "All Vessels Failing",
					Detail:    fmt.Sprintf("Last %d consecutive vessels all failed with zero completions.", len(s.RecentFailures)),
					Timestamp: time.Now(),
					VesselIDs: failureIDs(s.RecentFailures),
				}
			},
		},
		{
			Code:     "stall_detected",
			Severity: SeverityWarning,
			Check: func(s FleetState) *Alert {
				for _, hc := range s.HealthChecks {
					if hc.Level == "critical" {
						return &Alert{
							Severity:  SeverityWarning,
							Code:      "stall_detected",
							Title:     "Stall Detected",
							Detail:    hc.Message,
							Timestamp: time.Now(),
						}
					}
				}
				return nil
			},
		},
		{
			Code:     "queue_backlog",
			Severity: SeverityWarning,
			Check: func(s FleetState) *Alert {
				if s.Pending <= 10 || s.OldestPendingAge < 30*time.Minute {
					return nil
				}
				return &Alert{
					Severity:  SeverityWarning,
					Code:      "queue_backlog",
					Title:     "Queue Backlog Growing",
					Detail:    fmt.Sprintf("%d pending vessels, oldest waiting %s.", s.Pending, s.OldestPendingAge.Truncate(time.Minute)),
					Timestamp: time.Now(),
				}
			},
		},
		{
			Code:     "upgrade_stuck",
			Severity: SeverityWarning,
			Check: func(s FleetState) *Alert {
				if !s.UpgradeOverdue {
					return nil
				}
				return &Alert{
					Severity:  SeverityWarning,
					Code:      "upgrade_stuck",
					Title:     "Auto-Upgrade Stuck",
					Detail:    fmt.Sprintf("Daemon auto-upgrade is overdue by %s.", s.UpgradeOverdueBy.Truncate(time.Minute)),
					Timestamp: time.Now(),
				}
			},
		},
	}
}

// Evaluate runs all rules against the current fleet state and returns fired alerts.
func Evaluate(rules []EscalationRule, state FleetState) []Alert {
	var alerts []Alert
	for _, rule := range rules {
		if alert := rule.Check(state); alert != nil {
			alerts = append(alerts, *alert)
		}
	}
	return alerts
}

func isAuthError(f VesselFailure) bool {
	for _, keyword := range []string{"auth", "token", "credential", "login", "401", "403"} {
		if containsCI(f.Error, keyword) {
			return true
		}
	}
	return false
}

// containsCI reports whether s contains substr, case-insensitively.
func containsCI(s, substr string) bool {
	sl := len(substr)
	if sl > len(s) {
		return false
	}
	for i := 0; i <= len(s)-sl; i++ {
		match := true
		for j := 0; j < sl; j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func filterFailures(failures []VesselFailure, pred func(VesselFailure) bool) []VesselFailure {
	var result []VesselFailure
	for _, f := range failures {
		if pred(f) {
			result = append(result, f)
		}
	}
	return result
}

func failureIDs(failures []VesselFailure) []string {
	seen := map[string]bool{}
	var ids []string
	for _, f := range failures {
		if !seen[f.VesselID] {
			seen[f.VesselID] = true
			ids = append(ids, f.VesselID)
		}
	}
	return ids
}
