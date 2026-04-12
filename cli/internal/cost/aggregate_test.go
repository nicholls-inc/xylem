package cost

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeReport(t *testing.T, dir, vesselID string, r *CostReport) {
	t.Helper()
	vesselDir := filepath.Join(dir, "phases", vesselID)
	if err := os.MkdirAll(vesselDir, 0o755); err != nil {
		t.Fatalf("create vessel dir: %v", err)
	}
	if err := SaveReport(filepath.Join(vesselDir, "cost-report.json"), r); err != nil {
		t.Fatalf("save report: %v", err)
	}
}

func TestSpentToday_MissingPhasesDir_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	// No phases/ subdir created — Glob returns nil matches safely.
	total, forClass, err := SpentToday(dir, "delivery", time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || forClass != 0 {
		t.Fatalf("expected zero, got total=%f forClass=%f", total, forClass)
	}
}

func TestSpentToday_EmptyPhasesDir_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	// phases/ exists but contains no vessel subdirectories.
	if err := os.MkdirAll(filepath.Join(dir, "phases"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	total, forClass, err := SpentToday(dir, "delivery", time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || forClass != 0 {
		t.Fatalf("expected zero, got total=%f forClass=%f", total, forClass)
	}
}

func TestSpentToday_SingleReportToday_Counted(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeReport(t, dir, "v-001", &CostReport{
		MissionID:    "v-001",
		Workflow:     "delivery",
		TotalCostUSD: 0.42,
		GeneratedAt:  now,
	})

	total, forClass, err := SpentToday(dir, "delivery", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(total-0.42) > 1e-9 {
		t.Fatalf("total: want 0.42, got %f", total)
	}
	if math.Abs(forClass-0.42) > 1e-9 {
		t.Fatalf("forClass: want 0.42, got %f", forClass)
	}
}

func TestSpentToday_ReportYesterday_Excluded(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	yesterday := now.Add(-25 * time.Hour)
	writeReport(t, dir, "v-001", &CostReport{
		MissionID:    "v-001",
		Workflow:     "delivery",
		TotalCostUSD: 0.50,
		GeneratedAt:  yesterday,
	})

	total, forClass, err := SpentToday(dir, "delivery", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || forClass != 0 {
		t.Fatalf("expected zero (yesterday excluded), got total=%f forClass=%f", total, forClass)
	}
}

func TestSpentToday_MultipleReports_SumsCorrectly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeReport(t, dir, "v-001", &CostReport{MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 0.30, GeneratedAt: now})
	writeReport(t, dir, "v-002", &CostReport{MissionID: "v-002", Workflow: "delivery", TotalCostUSD: 0.20, GeneratedAt: now})
	writeReport(t, dir, "v-003", &CostReport{MissionID: "v-003", Workflow: "harness-maintenance", TotalCostUSD: 0.10, GeneratedAt: now})

	total, forDelivery, err := SpentToday(dir, "delivery", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(total-0.60) > 1e-9 {
		t.Fatalf("total: want 0.60, got %f", total)
	}
	if math.Abs(forDelivery-0.50) > 1e-9 {
		t.Fatalf("forDelivery: want 0.50, got %f", forDelivery)
	}
}

func TestSpentToday_ClassFilterUsesWorkflowClassOverWorkflow(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	// WorkflowClass overrides Workflow for matching.
	writeReport(t, dir, "v-001", &CostReport{
		MissionID:     "v-001",
		Workflow:      "implement-feature",
		WorkflowClass: "delivery",
		TotalCostUSD:  0.55,
		GeneratedAt:   now,
	})

	// Should match on WorkflowClass "delivery", not Workflow "implement-feature".
	_, forDelivery, err := SpentToday(dir, "delivery", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(forDelivery-0.55) > 1e-9 {
		t.Fatalf("forDelivery: want 0.55, got %f", forDelivery)
	}

	_, forImplFeature, err := SpentToday(dir, "implement-feature", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if forImplFeature != 0 {
		t.Fatalf("implement-feature should not match when WorkflowClass is set, got %f", forImplFeature)
	}
}

func TestSpentToday_ClassFilterFallsBackToWorkflow(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	// WorkflowClass is empty — should fall back to Workflow.
	writeReport(t, dir, "v-001", &CostReport{
		MissionID:    "v-001",
		Workflow:     "fix-bug",
		TotalCostUSD: 0.33,
		GeneratedAt:  now,
	})

	_, forFixBug, err := SpentToday(dir, "fix-bug", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(forFixBug-0.33) > 1e-9 {
		t.Fatalf("forFixBug: want 0.33, got %f", forFixBug)
	}
}

func TestSpentToday_CorruptReport_Skipped(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// Write a valid report.
	writeReport(t, dir, "v-001", &CostReport{MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 0.10, GeneratedAt: now})

	// Write a corrupt report.
	vesselDir := filepath.Join(dir, "phases", "v-bad")
	if err := os.MkdirAll(vesselDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vesselDir, "cost-report.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	total, _, err := SpentToday(dir, "delivery", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should only count the valid report.
	if math.Abs(total-0.10) > 1e-9 {
		t.Fatalf("total: want 0.10, got %f", total)
	}
}
