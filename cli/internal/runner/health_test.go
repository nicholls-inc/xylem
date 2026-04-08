package runner

import (
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func TestAnalyzeVesselStatusHealthyCompletedRun(t *testing.T) {
	now := time.Now().UTC()
	vessel := queue.Vessel{
		ID:        "issue-1",
		State:     queue.StateCompleted,
		CreatedAt: now,
	}
	summary := &VesselSummary{
		VesselID: "issue-1",
		State:    "completed",
		Phases: []PhaseSummary{
			{Name: "implement", Status: "completed"},
		},
	}

	report := AnalyzeVesselStatus(vessel, summary)
	if report.Health != VesselHealthHealthy {
		t.Fatalf("Health = %q, want %q", report.Health, VesselHealthHealthy)
	}
	if len(report.Anomalies) != 0 {
		t.Fatalf("Anomalies = %#v, want none", report.Anomalies)
	}
}

func TestAnalyzeVesselStatusFlagsBudgetAndGateFailures(t *testing.T) {
	now := time.Now().UTC()
	failedGate := false
	vessel := queue.Vessel{
		ID:        "issue-2",
		State:     queue.StateFailed,
		CreatedAt: now,
	}
	summary := &VesselSummary{
		VesselID:        "issue-2",
		State:           "failed",
		BudgetExceeded:  true,
		TotalTokensEst:  10,
		TotalCostUSDEst: 0.5,
		Phases: []PhaseSummary{
			{Name: "implement", Status: "failed", GateType: "command", GatePassed: &failedGate},
		},
	}

	report := AnalyzeVesselStatus(vessel, summary)
	if report.Health != VesselHealthUnhealthy {
		t.Fatalf("Health = %q, want %q", report.Health, VesselHealthUnhealthy)
	}
	if len(report.Anomalies) != 4 {
		t.Fatalf("len(Anomalies) = %d, want 4", len(report.Anomalies))
	}
}

func TestAnalyzeFleetStatusAggregatesPatterns(t *testing.T) {
	now := time.Now().UTC()
	waitingSince := now.Add(-time.Minute)
	vessels := []queue.Vessel{
		{ID: "issue-1", State: queue.StateCompleted, CreatedAt: now},
		{ID: "issue-2", State: queue.StateWaiting, CreatedAt: now, WaitingFor: "approved", WaitingSince: &waitingSince},
		{ID: "issue-3", State: queue.StateFailed, CreatedAt: now},
	}
	summaries := map[string]*VesselSummary{
		"issue-1": {VesselID: "issue-1", State: "completed", Phases: []PhaseSummary{}},
	}

	report := AnalyzeFleetStatus(vessels, summaries)
	if report.Healthy != 1 || report.Degraded != 1 || report.Unhealthy != 1 {
		t.Fatalf("unexpected fleet counts: %#v", report)
	}
	if len(report.Patterns) != 2 {
		t.Fatalf("len(Patterns) = %d, want 2", len(report.Patterns))
	}
	if got := FormatFleetPatterns(report.Patterns); got != "run_failed=1, waiting_on_gate=1" {
		t.Fatalf("FormatFleetPatterns() = %q", got)
	}
}

func TestLoadVesselSummariesSkipsIDsThatCannotHaveSummaryFiles(t *testing.T) {
	stateDir := t.TempDir()
	summary := &VesselSummary{
		VesselID: "issue-1",
		State:    string(queue.StateCompleted),
		Phases:   []PhaseSummary{},
	}
	if err := SaveVesselSummary(stateDir, summary); err != nil {
		t.Fatalf("SaveVesselSummary() error = %v", err)
	}

	summaries, err := LoadVesselSummaries(stateDir, []string{"issue-1", "manual/task"})
	if err != nil {
		t.Fatalf("LoadVesselSummaries() error = %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	if got := summaries["issue-1"]; got == nil {
		t.Fatal("expected summary for issue-1")
	}
	if _, ok := summaries["manual/task"]; ok {
		t.Fatalf("did not expect summary entry for invalid vessel ID")
	}
}
