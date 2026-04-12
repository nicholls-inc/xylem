package cost

import (
	"fmt"
	"time"
)

// Decision is the scanner-facing result of a budget-gate check.
type Decision struct {
	Allowed      bool
	Reason       string
	RemainingUSD float64
}

// GateConfig holds the budget enforcement parameters for BudgetGate.
// It mirrors the relevant fields from config.CostConfig without creating an
// import cycle (config already imports the cost package).
type GateConfig struct {
	DailyBudgetUSD float64
	PerClassLimit  map[string]float64
	// OnExceeded controls what happens when a limit is exceeded.
	// Accepted values (matching config validation): "", "drain_only", "pause", "alert".
	// Empty and "drain_only" both mean: always allow but log a warning.
	// "pause" means: deny (Allowed=false) when limit exceeded.
	// "alert" means: always allow but emit an alert.
	OnExceeded string
}

// BudgetGate enforces daily and per-class cost limits at the scanner boundary.
// It reads historical cost-report.json files from the state dir to compute
// daily spend, then applies DailyBudgetUSD and PerClassLimit limits.
type BudgetGate struct {
	cfg      GateConfig
	stateDir string
	now      func() time.Time // injectable for tests; defaults to time.Now
}

// NewBudgetGate builds a gate backed by the given GateConfig and state dir.
// A zero GateConfig (DailyBudgetUSD == 0, PerClassLimit == nil) always allows.
func NewBudgetGate(cfg GateConfig, stateDir string) *BudgetGate {
	return &BudgetGate{cfg: cfg, stateDir: stateDir, now: time.Now}
}

// Check reports whether a candidate of the given class may be enqueued.
// It reads historical cost-report.json files to compute daily spend, then
// applies DailyBudgetUSD and PerClassLimit limits.
//
// on_exceeded semantics (using config-validated values):
//
//	drain_only (default) — always Allowed=true; new work logs a warning.
//	pause                — Allowed=false when limit exceeded; vessel is skipped.
//	alert                — always Allowed=true; only emits an alert.
func (g *BudgetGate) Check(class string) Decision {
	if g == nil {
		return Decision{Allowed: true}
	}
	if g.cfg.DailyBudgetUSD == 0 && len(g.cfg.PerClassLimit) == 0 {
		return Decision{Allowed: true}
	}

	now := g.now()
	total, forClass, err := SpentToday(g.stateDir, class, now)
	if err != nil {
		// Aggregation errors are non-fatal; allow rather than blocking on I/O failure.
		return Decision{Allowed: true, Reason: fmt.Sprintf("aggregate error (allowing): %v", err)}
	}

	mode := g.cfg.OnExceeded
	if mode == "" {
		mode = "drain_only"
	}

	// Check per-class limit first (more specific).
	if classLimit, ok := g.cfg.PerClassLimit[class]; ok && classLimit > 0 {
		if forClass >= classLimit {
			reason := fmt.Sprintf("per-class limit %.4f USD exceeded for class %q (spent %.4f)", classLimit, class, forClass)
			if mode == "pause" {
				return Decision{Allowed: false, Reason: reason, RemainingUSD: 0}
			}
			return Decision{Allowed: true, Reason: reason, RemainingUSD: 0}
		}
		remaining := classLimit - forClass
		// Also check global daily budget; take the min remaining.
		if g.cfg.DailyBudgetUSD > 0 {
			if total >= g.cfg.DailyBudgetUSD {
				reason := fmt.Sprintf("daily budget %.4f USD exceeded (spent %.4f)", g.cfg.DailyBudgetUSD, total)
				if mode == "pause" {
					return Decision{Allowed: false, Reason: reason, RemainingUSD: 0}
				}
				return Decision{Allowed: true, Reason: reason, RemainingUSD: 0}
			}
			if globalRemaining := g.cfg.DailyBudgetUSD - total; globalRemaining < remaining {
				remaining = globalRemaining
			}
		}
		return Decision{Allowed: true, RemainingUSD: remaining}
	}

	// No per-class limit for this class — check global daily budget only.
	if g.cfg.DailyBudgetUSD > 0 {
		if total >= g.cfg.DailyBudgetUSD {
			reason := fmt.Sprintf("daily budget %.4f USD exceeded (spent %.4f)", g.cfg.DailyBudgetUSD, total)
			if mode == "pause" {
				return Decision{Allowed: false, Reason: reason, RemainingUSD: 0}
			}
			return Decision{Allowed: true, Reason: reason, RemainingUSD: 0}
		}
		return Decision{Allowed: true, RemainingUSD: g.cfg.DailyBudgetUSD - total}
	}

	return Decision{Allowed: true}
}
