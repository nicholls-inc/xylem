package cost

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// genClass generates a workflow class name.
func genClass() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{"delivery", "harness-maintenance", "ops", "fix-bug"})
}

// genOnExceeded generates a valid on_exceeded config value.
func genOnExceeded() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{"drain_only", "pause", "alert", ""})
}

// propTempDir creates a temp directory for a rapid iteration and returns a cleanup func.
func propTempDir(rt *rapid.T) (string, func()) {
	dir, err := os.MkdirTemp("", "rapid-budgetgate-*")
	if err != nil {
		rt.Fatalf("MkdirTemp: %v", err)
	}
	return dir, func() { os.RemoveAll(dir) }
}

// writeReportsForTotal writes a single cost-report.json fixture in stateDir
// with the given TotalCostUSD for the given class using today's time.
func writeReportsForTotal(rt *rapid.T, stateDir, class string, total float64, now time.Time) {
	if total <= 0 {
		return
	}
	vesselDir := filepath.Join(stateDir, "phases", "prop-vessel")
	if err := os.MkdirAll(vesselDir, 0o755); err != nil {
		rt.Fatalf("mkdir: %v", err)
	}
	r := &CostReport{
		MissionID:    "prop-vessel",
		Workflow:     class,
		TotalCostUSD: total,
		GeneratedAt:  now,
	}
	if err := SaveReport(filepath.Join(vesselDir, "cost-report.json"), r); err != nil {
		rt.Fatalf("save report: %v", err)
	}
}

// TestPropBudgetGate_NeverDeniesWhenDrainOnly verifies that drain_only mode
// always returns Allowed=true regardless of spend and limits.
func TestPropBudgetGate_NeverDeniesWhenDrainOnly(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, cleanup := propTempDir(rt)
		defer cleanup()
		now := time.Now().UTC()
		class := genClass().Draw(rt, "class")
		limit := float64(rapid.IntRange(1, 1000).Draw(rt, "limit_cents")) / 100.0
		spend := float64(rapid.IntRange(0, 2000).Draw(rt, "spend_cents")) / 100.0

		writeReportsForTotal(rt, dir, class, spend, now)

		g := &BudgetGate{
			cfg: GateConfig{
				DailyBudgetUSD: limit,
				OnExceeded:     "drain_only",
			},
			stateDir: dir,
			now:      pinNow(now),
		}
		d := g.Check(class)
		if !d.Allowed {
			rt.Fatalf("drain_only must never deny; class=%s spend=%.4f limit=%.4f reason=%q",
				class, spend, limit, d.Reason)
		}
	})
}

// TestPropBudgetGate_PauseMode_AllowedIffSpendBelowLimit verifies that in pause
// mode, Allowed is false iff total spend >= daily limit.
func TestPropBudgetGate_PauseMode_AllowedIffSpendBelowLimit(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, cleanup := propTempDir(rt)
		defer cleanup()
		now := time.Now().UTC()
		class := genClass().Draw(rt, "class")
		limitCents := rapid.IntRange(1, 500).Draw(rt, "limit_cents")
		limit := float64(limitCents) / 100.0
		spendCents := rapid.IntRange(0, 1000).Draw(rt, "spend_cents")
		spend := float64(spendCents) / 100.0

		writeReportsForTotal(rt, dir, class, spend, now)

		g := &BudgetGate{
			cfg: GateConfig{
				DailyBudgetUSD: limit,
				OnExceeded:     "pause",
			},
			stateDir: dir,
			now:      pinNow(now),
		}
		d := g.Check(class)
		exceeded := spend >= limit
		if exceeded && d.Allowed {
			rt.Fatalf("pause: spend(%.4f) >= limit(%.4f) but Allowed=true", spend, limit)
		}
		if !exceeded && !d.Allowed {
			rt.Fatalf("pause: spend(%.4f) < limit(%.4f) but Allowed=false (reason=%q)", spend, limit, d.Reason)
		}
	})
}

// TestPropBudgetGate_RemainingUSDMatchesComputed verifies that RemainingUSD
// equals exactly (limit - spend) when under budget, and exactly 0 when
// the limit is exceeded. The sign-only check (>= 0) is trivially satisfied
// by construction; this property catches wrong arithmetic (e.g. spend-limit).
func TestPropBudgetGate_RemainingUSDMatchesComputed(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, cleanup := propTempDir(rt)
		defer cleanup()
		now := time.Now().UTC()
		class := genClass().Draw(rt, "class")
		limit := float64(rapid.IntRange(1, 1000).Draw(rt, "limit_cents")) / 100.0
		spend := float64(rapid.IntRange(0, 2000).Draw(rt, "spend_cents")) / 100.0
		onExceeded := genOnExceeded().Draw(rt, "on_exceeded")

		writeReportsForTotal(rt, dir, class, spend, now)

		g := &BudgetGate{
			cfg: GateConfig{
				DailyBudgetUSD: limit,
				OnExceeded:     onExceeded,
			},
			stateDir: dir,
			now:      pinNow(now),
		}
		d := g.Check(class)

		exceeded := spend >= limit
		if exceeded {
			// Exceeded path: RemainingUSD must be exactly 0.
			if d.RemainingUSD != 0 {
				rt.Fatalf("exceeded: RemainingUSD must be 0, got %f (spend=%.4f limit=%.4f)",
					d.RemainingUSD, spend, limit)
			}
		} else {
			// Under-budget path: RemainingUSD must equal limit - spend.
			want := limit - spend
			if d.RemainingUSD < 0 {
				rt.Fatalf("under budget: RemainingUSD must be >= 0, got %f", d.RemainingUSD)
			}
			if diff := d.RemainingUSD - want; diff < -1e-9 || diff > 1e-9 {
				rt.Fatalf("under budget: RemainingUSD=%.9f want=%.9f (spend=%.4f limit=%.4f)",
					d.RemainingUSD, want, spend, limit)
			}
		}
	})
}

// TestPropSpentToday_TotalIsNonNegativeAndAtLeastClassSpend verifies
// total >= forClass >= 0 for any set of reports.
func TestPropSpentToday_TotalIsNonNegativeAndAtLeastClassSpend(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, cleanup := propTempDir(rt)
		defer cleanup()
		now := time.Now().UTC()
		targetClass := genClass().Draw(rt, "target_class")
		n := rapid.IntRange(1, 10).Draw(rt, "n_reports")

		for i := range n {
			class := genClass().Draw(rt, "class")
			costVal := float64(rapid.IntRange(0, 500).Draw(rt, "cost_cents")) / 100.0
			vesselID := filepath.Join("v", string(rune('a'+i)))
			vesselDir := filepath.Join(dir, "phases", vesselID)
			if err := os.MkdirAll(vesselDir, 0o755); err != nil {
				rt.Fatalf("mkdir: %v", err)
			}
			r := &CostReport{
				MissionID:    vesselID,
				Workflow:     class,
				TotalCostUSD: costVal,
				GeneratedAt:  now,
			}
			if err := SaveReport(filepath.Join(vesselDir, "cost-report.json"), r); err != nil {
				rt.Fatalf("save: %v", err)
			}
		}

		total, forClass, err := SpentToday(dir, targetClass, now)
		if err != nil {
			rt.Fatalf("SpentToday error: %v", err)
		}
		if total < 0 {
			rt.Fatalf("total must be >= 0, got %f", total)
		}
		if forClass < 0 {
			rt.Fatalf("forClass must be >= 0, got %f", forClass)
		}
		// total >= forClass always (forClass is a subset of total).
		if forClass > total+1e-9 {
			rt.Fatalf("forClass(%.4f) > total(%.4f): impossible", forClass, total)
		}
	})
}

// TestPropBudgetGate_PerClassLimitBindsTighter verifies that when a per-class
// limit is more restrictive than the global daily budget, it is the binding
// constraint in pause mode.
func TestPropBudgetGate_PerClassLimitBindsTighter(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, cleanup := propTempDir(rt)
		defer cleanup()
		now := time.Now().UTC()
		class := genClass().Draw(rt, "class")

		// Per-class limit is always tighter than the daily budget.
		classLimitCents := rapid.IntRange(1, 300).Draw(rt, "class_limit_cents")
		classLimit := float64(classLimitCents) / 100.0
		dailyLimit := classLimit * 10 // daily is always looser

		spendCents := rapid.IntRange(0, 600).Draw(rt, "spend_cents")
		spend := float64(spendCents) / 100.0

		writeReportsForTotal(rt, dir, class, spend, now)

		g := &BudgetGate{
			cfg: GateConfig{
				DailyBudgetUSD: dailyLimit,
				PerClassLimit:  map[string]float64{class: classLimit},
				OnExceeded:     "pause",
			},
			stateDir: dir,
			now:      pinNow(now),
		}
		d := g.Check(class)
		exceededClass := spend >= classLimit
		if exceededClass && d.Allowed {
			rt.Fatalf("per-class exceeded but Allowed=true; spend=%.4f classLimit=%.4f", spend, classLimit)
		}
		if !exceededClass && !d.Allowed {
			rt.Fatalf("per-class not exceeded but Allowed=false; spend=%.4f classLimit=%.4f reason=%q",
				spend, classLimit, d.Reason)
		}
		// RemainingUSD must be non-negative.
		if d.RemainingUSD < 0 {
			rt.Fatalf("RemainingUSD must be >= 0, got %f", d.RemainingUSD)
		}
	})
}
