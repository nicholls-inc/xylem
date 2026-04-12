package notify

import (
	"strings"
	"testing"
	"time"
)

// helper: find a rule by code from DefaultRules.
func findRule(t *testing.T, code string) EscalationRule {
	t.Helper()
	for _, r := range DefaultRules() {
		if r.Code == code {
			return r
		}
	}
	t.Fatalf("no rule found with code %q", code)
	return EscalationRule{} // unreachable
}

// helper: generate N auth-related failures.
func authFailures(n int) []VesselFailure {
	failures := make([]VesselFailure, n)
	for i := range failures {
		failures[i] = VesselFailure{
			VesselID:  "v" + string(rune('0'+i)),
			Error:     "authentication failed: token expired",
			Timestamp: time.Now(),
		}
	}
	return failures
}

// helper: generate N generic failures.
func genericFailures(n int) []VesselFailure {
	failures := make([]VesselFailure, n)
	for i := range failures {
		failures[i] = VesselFailure{
			VesselID:  "v" + string(rune('0'+i)),
			Error:     "something went wrong",
			Timestamp: time.Now(),
		}
	}
	return failures
}

func TestEscalation_AuthFailure_FiresAt3(t *testing.T) {
	rule := findRule(t, "auth_failure")
	state := FleetState{
		RecentFailures: authFailures(3),
	}
	alert := rule.Check(state)
	if alert == nil {
		t.Fatal("expected auth_failure alert with 3 auth failures, got nil")
	}
	if alert.Code != "auth_failure" {
		t.Errorf("expected code auth_failure, got %q", alert.Code)
	}
	if alert.Severity != SeverityCritical {
		t.Errorf("expected severity critical, got %q", alert.Severity)
	}
	if !strings.Contains(alert.Detail, "3 vessels") {
		t.Errorf("expected detail to mention 3 vessels, got: %q", alert.Detail)
	}
}

func TestEscalation_AuthFailure_DoesNotFireAt2(t *testing.T) {
	rule := findRule(t, "auth_failure")
	state := FleetState{
		RecentFailures: authFailures(2),
	}
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert with only 2 auth failures, got: %+v", alert)
	}
}

func TestEscalation_AuthFailure_IgnoresNonAuthErrors(t *testing.T) {
	rule := findRule(t, "auth_failure")
	state := FleetState{
		RecentFailures: genericFailures(10),
	}
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert with non-auth failures, got: %+v", alert)
	}
}

func TestEscalation_HighFailureRate_FiresAbove50Pct(t *testing.T) {
	rule := findRule(t, "high_failure_rate")
	state := FleetState{
		RecentFailures:    genericFailures(4),
		RecentCompletions: 1,
	}
	// 4 failures out of 5 total = 80%
	alert := rule.Check(state)
	if alert == nil {
		t.Fatal("expected high_failure_rate alert at 80% failure rate, got nil")
	}
	if alert.Code != "high_failure_rate" {
		t.Errorf("expected code high_failure_rate, got %q", alert.Code)
	}
}

func TestEscalation_HighFailureRate_DoesNotFireWithFewTotal(t *testing.T) {
	rule := findRule(t, "high_failure_rate")
	state := FleetState{
		RecentFailures:    genericFailures(3),
		RecentCompletions: 0,
	}
	// 3 failures out of 3 total = 100%, but total < 5
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert with total < 5, got: %+v", alert)
	}
}

func TestEscalation_HighFailureRate_DoesNotFireAt50Pct(t *testing.T) {
	rule := findRule(t, "high_failure_rate")
	state := FleetState{
		RecentFailures:    genericFailures(5),
		RecentCompletions: 5,
	}
	// 5/10 = 50%, check is > 0.5 (strict)
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert at exactly 50%%, got: %+v", alert)
	}
}

func TestEscalation_AllVesselsFailing_Fires(t *testing.T) {
	rule := findRule(t, "all_vessels_failing")
	state := FleetState{
		RecentFailures:    genericFailures(5),
		RecentCompletions: 0,
	}
	alert := rule.Check(state)
	if alert == nil {
		t.Fatal("expected all_vessels_failing alert, got nil")
	}
	if alert.Code != "all_vessels_failing" {
		t.Errorf("expected code all_vessels_failing, got %q", alert.Code)
	}
}

func TestEscalation_AllVesselsFailing_DoesNotFireWithCompletion(t *testing.T) {
	rule := findRule(t, "all_vessels_failing")
	state := FleetState{
		RecentFailures:    genericFailures(5),
		RecentCompletions: 1,
	}
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert when there are completions, got: %+v", alert)
	}
}

func TestEscalation_AllVesselsFailing_DoesNotFireWithFewFailures(t *testing.T) {
	rule := findRule(t, "all_vessels_failing")
	state := FleetState{
		RecentFailures:    genericFailures(4),
		RecentCompletions: 0,
	}
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert with < 5 failures, got: %+v", alert)
	}
}

func TestEscalation_StallDetected_FiresOnCriticalHealthCheck(t *testing.T) {
	rule := findRule(t, "stall_detected")
	state := FleetState{
		HealthChecks: []HealthCheck{
			{Code: "phase_stall", Level: "ok", Message: "all good"},
			{Code: "orphan", Level: "critical", Message: "vessel stuck for 2h"},
		},
	}
	alert := rule.Check(state)
	if alert == nil {
		t.Fatal("expected stall_detected alert on critical health check, got nil")
	}
	if alert.Detail != "vessel stuck for 2h" {
		t.Errorf("expected detail from critical check, got: %q", alert.Detail)
	}
	if alert.Severity != SeverityWarning {
		t.Errorf("expected severity warning, got %q", alert.Severity)
	}
}

func TestEscalation_StallDetected_DoesNotFireWithoutCritical(t *testing.T) {
	rule := findRule(t, "stall_detected")
	state := FleetState{
		HealthChecks: []HealthCheck{
			{Code: "phase_stall", Level: "ok", Message: "fine"},
			{Code: "orphan", Level: "warning", Message: "meh"},
		},
	}
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert without critical health checks, got: %+v", alert)
	}
}

func TestEscalation_QueueBacklog_FiresWithBothConditions(t *testing.T) {
	rule := findRule(t, "queue_backlog")
	state := FleetState{
		Pending:          11,
		OldestPendingAge: 31 * time.Minute,
	}
	alert := rule.Check(state)
	if alert == nil {
		t.Fatal("expected queue_backlog alert, got nil")
	}
	if alert.Code != "queue_backlog" {
		t.Errorf("expected code queue_backlog, got %q", alert.Code)
	}
}

func TestEscalation_QueueBacklog_DoesNotFireWithLowPending(t *testing.T) {
	rule := findRule(t, "queue_backlog")
	state := FleetState{
		Pending:          10, // <= 10 threshold
		OldestPendingAge: 1 * time.Hour,
	}
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert with pending <= 10, got: %+v", alert)
	}
}

func TestEscalation_QueueBacklog_DoesNotFireWithShortAge(t *testing.T) {
	rule := findRule(t, "queue_backlog")
	state := FleetState{
		Pending:          20,
		OldestPendingAge: 29 * time.Minute, // < 30min threshold
	}
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert with age < 30m, got: %+v", alert)
	}
}

func TestEscalation_UpgradeStuck_Fires(t *testing.T) {
	rule := findRule(t, "upgrade_stuck")
	state := FleetState{
		UpgradeOverdue:   true,
		UpgradeOverdueBy: 15 * time.Minute,
	}
	alert := rule.Check(state)
	if alert == nil {
		t.Fatal("expected upgrade_stuck alert, got nil")
	}
	if alert.Code != "upgrade_stuck" {
		t.Errorf("expected code upgrade_stuck, got %q", alert.Code)
	}
	if !strings.Contains(alert.Detail, "15m") {
		t.Errorf("expected overdue duration in detail, got: %q", alert.Detail)
	}
}

func TestEscalation_UpgradeStuck_DoesNotFireWhenNotOverdue(t *testing.T) {
	rule := findRule(t, "upgrade_stuck")
	state := FleetState{
		UpgradeOverdue: false,
	}
	alert := rule.Check(state)
	if alert != nil {
		t.Errorf("expected no alert when not overdue, got: %+v", alert)
	}
}

func TestEvaluate_ReturnsAllFiredRules(t *testing.T) {
	rules := DefaultRules()
	state := FleetState{
		// Auth failure condition: 3+ auth errors.
		RecentFailures: authFailures(5),
		// High failure rate: 5/5 = 100% (also triggers all_vessels_failing).
		RecentCompletions: 0,
		// Stall detected: critical health check.
		HealthChecks: []HealthCheck{
			{Code: "test", Level: "critical", Message: "stuck"},
		},
		// Queue backlog: >10 pending, >30min old.
		Pending:          20,
		OldestPendingAge: 1 * time.Hour,
		// Upgrade stuck.
		UpgradeOverdue:   true,
		UpgradeOverdueBy: 10 * time.Minute,
	}

	alerts := Evaluate(rules, state)

	// With this state, we expect all 6 rules to fire.
	codes := make(map[string]bool)
	for _, a := range alerts {
		codes[a.Code] = true
	}

	expected := []string{
		"auth_failure",
		"high_failure_rate",
		"all_vessels_failing",
		"stall_detected",
		"queue_backlog",
		"upgrade_stuck",
	}
	for _, code := range expected {
		if !codes[code] {
			t.Errorf("expected rule %q to fire, but it did not. Fired rules: %v", code, codes)
		}
	}
}

func TestEvaluate_ReturnsNilWhenNoRulesFire(t *testing.T) {
	rules := DefaultRules()
	state := FleetState{} // empty state, nothing should fire

	alerts := Evaluate(rules, state)
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts on empty state, got %d: %+v", len(alerts), alerts)
	}
}

func TestContainsCI(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		want   bool
	}{
		{"authentication failed", "auth", true},
		{"AUTHENTICATION FAILED", "auth", true},
		{"Authentication Failed", "AUTH", true},
		{"some error", "auth", false},
		{"", "auth", false},
		{"auth", "", true},
		{"short", "this is longer than the input", false},
		{"Token expired", "token", true},
		{"error 401 unauthorized", "401", true},
		{"error 403 forbidden", "403", true},
		{"credential renewal needed", "CREDENTIAL", true},
	}

	for _, tt := range tests {
		t.Run(tt.s+"_"+tt.substr, func(t *testing.T) {
			got := containsCI(tt.s, tt.substr)
			if got != tt.want {
				t.Errorf("containsCI(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
			}
		})
	}
}
