package cost

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pinNow returns a func() time.Time that always returns t (UTC).
func pinNow(t time.Time) func() time.Time {
	return func() time.Time { return t.UTC() }
}

func writeGateReport(t *testing.T, stateDir, vesselID string, r *CostReport) {
	t.Helper()
	d := filepath.Join(stateDir, "phases", vesselID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := SaveReport(filepath.Join(d, "cost-report.json"), r); err != nil {
		t.Fatalf("save report: %v", err)
	}
}

func TestBudgetGate_NilGateAlwaysAllows(t *testing.T) {
	var g *BudgetGate
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatal("nil gate should always allow")
	}
}

func TestBudgetGate_ZeroCostConfigAlwaysAllows(t *testing.T) {
	dir := t.TempDir()
	g := NewBudgetGate(GateConfig{}, dir)
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatal("zero config should always allow")
	}
}

func TestBudgetGate_DailyBudgetNotYetExceeded(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 0.40, GeneratedAt: now,
	})

	g := &BudgetGate{cfg: GateConfig{DailyBudgetUSD: 1.00, OnExceeded: "pause"}, stateDir: dir, now: pinNow(now)}
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatalf("should be allowed under budget, got reason=%q", d.Reason)
	}
	if math.Abs(d.RemainingUSD-0.60) > 1e-9 {
		t.Fatalf("RemainingUSD: want 0.60, got %f", d.RemainingUSD)
	}
}

func TestBudgetGate_DailyBudgetExceeded_DrainOnly_Allows(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 1.50, GeneratedAt: now,
	})

	g := &BudgetGate{cfg: GateConfig{DailyBudgetUSD: 1.00, OnExceeded: "drain_only"}, stateDir: dir, now: pinNow(now)}
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatal("drain_only: should always allow even when budget exceeded")
	}
	if !strings.Contains(d.Reason, "daily budget") {
		t.Fatalf("expected reason about daily budget, got %q", d.Reason)
	}
}

func TestBudgetGate_DailyBudgetExceeded_Pause_Denies(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 0.90, GeneratedAt: now,
	})
	writeGateReport(t, dir, "v-002", &CostReport{
		MissionID: "v-002", Workflow: "delivery", TotalCostUSD: 0.15, GeneratedAt: now,
	})

	g := &BudgetGate{cfg: GateConfig{DailyBudgetUSD: 1.00, OnExceeded: "pause"}, stateDir: dir, now: pinNow(now)}
	d := g.Check("delivery")
	if d.Allowed {
		t.Fatal("pause: should deny when daily budget exceeded")
	}
	if d.RemainingUSD != 0 {
		t.Fatalf("RemainingUSD: want 0, got %f", d.RemainingUSD)
	}
	if !strings.Contains(d.Reason, "daily budget") {
		t.Fatalf("expected reason about daily budget, got %q", d.Reason)
	}
}

func TestBudgetGate_DailyBudgetExceeded_Alert_Allows(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 2.00, GeneratedAt: now,
	})

	g := &BudgetGate{cfg: GateConfig{DailyBudgetUSD: 1.00, OnExceeded: "alert"}, stateDir: dir, now: pinNow(now)}
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatal("alert: should always allow even when budget exceeded")
	}
}

func TestBudgetGate_PerClassLimitExceeded_Pause_Denies(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 5.50, GeneratedAt: now,
	})

	g := &BudgetGate{
		cfg: GateConfig{
			DailyBudgetUSD: 50.0,
			PerClassLimit:  map[string]float64{"delivery": 5.00},
			OnExceeded:     "pause",
		},
		stateDir: dir,
		now:      pinNow(now),
	}
	d := g.Check("delivery")
	if d.Allowed {
		t.Fatal("pause: should deny when per-class limit exceeded")
	}
	if !strings.Contains(d.Reason, "per-class limit") {
		t.Fatalf("expected reason about per-class limit, got %q", d.Reason)
	}
}

func TestBudgetGate_PerClassLimitExceeded_DrainOnly_Allows(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 6.00, GeneratedAt: now,
	})

	g := &BudgetGate{
		cfg: GateConfig{
			PerClassLimit: map[string]float64{"delivery": 5.00},
			OnExceeded:    "drain_only",
		},
		stateDir: dir,
		now:      pinNow(now),
	}
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatal("drain_only: should allow even when per-class limit exceeded")
	}
}

func TestBudgetGate_PerClassLimitNotExceeded_GlobalBudgetExceeded_Pause_Denies(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	// Two classes contributing to global total.
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 3.00, GeneratedAt: now,
	})
	writeGateReport(t, dir, "v-002", &CostReport{
		MissionID: "v-002", Workflow: "harness-maintenance", TotalCostUSD: 48.00, GeneratedAt: now,
	})

	// delivery per-class limit (40) not exceeded (only 3 spent), but daily (50) is exceeded (51 total).
	g := &BudgetGate{
		cfg: GateConfig{
			DailyBudgetUSD: 50.0,
			PerClassLimit:  map[string]float64{"delivery": 40.0},
			OnExceeded:     "pause",
		},
		stateDir: dir,
		now:      pinNow(now),
	}
	d := g.Check("delivery")
	if d.Allowed {
		t.Fatal("pause: should deny when global daily budget exceeded, even if per-class limit not exceeded")
	}
	if !strings.Contains(d.Reason, "daily budget") {
		t.Fatalf("expected reason about daily budget, got %q", d.Reason)
	}
}

func TestBudgetGate_PhasesIsFile_DoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	// phases exists as a regular file (not a directory) — filepath.Glob returns
	// no matches, SpentToday returns (0, 0, nil). Gate should allow with
	// RemainingUSD equal to the full daily budget.
	phasesPath := filepath.Join(dir, "phases")
	if err := os.WriteFile(phasesPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	g := &BudgetGate{
		cfg:      GateConfig{DailyBudgetUSD: 1.00, OnExceeded: "pause"},
		stateDir: dir,
		now:      pinNow(time.Now().UTC()),
	}
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatalf("should allow (zero spend < 1.00 limit), got reason=%q", d.Reason)
	}
	if math.Abs(d.RemainingUSD-1.00) > 1e-9 {
		t.Fatalf("RemainingUSD: want 1.00, got %f", d.RemainingUSD)
	}
}

func TestBudgetGate_OldReportsExcludedFromToday(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	yesterday := now.Add(-25 * time.Hour)

	// Yesterday's report pushes "total" to 2.00 if counted — but it should be excluded.
	writeGateReport(t, dir, "v-old", &CostReport{
		MissionID: "v-old", Workflow: "delivery", TotalCostUSD: 2.00, GeneratedAt: yesterday,
	})
	// Today's report: only 0.30 spent.
	writeGateReport(t, dir, "v-new", &CostReport{
		MissionID: "v-new", Workflow: "delivery", TotalCostUSD: 0.30, GeneratedAt: now,
	})

	g := &BudgetGate{
		cfg:      GateConfig{DailyBudgetUSD: 1.00, OnExceeded: "pause"},
		stateDir: dir,
		now:      pinNow(now),
	}
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatalf("old reports must not be counted; should be allowed (0.30 < 1.00), got reason=%q", d.Reason)
	}
	if math.Abs(d.RemainingUSD-0.70) > 1e-9 {
		t.Fatalf("RemainingUSD: want 0.70, got %f", d.RemainingUSD)
	}
}

func TestBudgetGate_RemainingUSDIsAccurate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "ops", TotalCostUSD: 3.75, GeneratedAt: now,
	})

	g := &BudgetGate{
		cfg:      GateConfig{DailyBudgetUSD: 10.00},
		stateDir: dir,
		now:      pinNow(now),
	}
	d := g.Check("ops")
	if !d.Allowed {
		t.Fatal("should be allowed (3.75 < 10.00)")
	}
	if math.Abs(d.RemainingUSD-6.25) > 1e-9 {
		t.Fatalf("RemainingUSD: want 6.25, got %f", d.RemainingUSD)
	}
}

func TestBudgetGate_CorruptReportSkipped(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// Corrupt report.
	d2 := filepath.Join(dir, "phases", "v-bad")
	if err := os.MkdirAll(d2, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d2, "cost-report.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Valid report: 0.50 spent.
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 0.50, GeneratedAt: now,
	})

	g := &BudgetGate{
		cfg:      GateConfig{DailyBudgetUSD: 1.00, OnExceeded: "pause"},
		stateDir: dir,
		now:      pinNow(now),
	}
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatal("should allow — corrupt report skipped, only 0.50 counted")
	}
	if math.Abs(d.RemainingUSD-0.50) > 1e-9 {
		t.Fatalf("RemainingUSD: want 0.50, got %f", d.RemainingUSD)
	}
}

func TestBudgetGate_OnExceededEmpty_DefaultsToDrainOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGateReport(t, dir, "v-001", &CostReport{
		MissionID: "v-001", Workflow: "delivery", TotalCostUSD: 2.00, GeneratedAt: now,
	})

	// OnExceeded left empty — should default to drain_only (always allow).
	g := &BudgetGate{
		cfg:      GateConfig{DailyBudgetUSD: 1.00},
		stateDir: dir,
		now:      pinNow(now),
	}
	d := g.Check("delivery")
	if !d.Allowed {
		t.Fatal("empty on_exceeded defaults to drain_only — should allow")
	}
}
