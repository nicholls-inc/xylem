package cost

// Decision is the scanner-facing result of a budget-gate check.
type Decision struct {
	Allowed      bool
	Reason       string
	RemainingUSD float64
}

// BudgetGate is the scanner integration seam for budget-aware enqueue gating.
// This implementation is intentionally permissive until the real tracker-backed
// budget classifier lands.
type BudgetGate struct {
	budget *Budget
}

// NewBudgetGate builds a gate for the given budget. A nil budget always allows.
func NewBudgetGate(budget *Budget) *BudgetGate {
	return &BudgetGate{budget: budget}
}

// Check reports whether a candidate of the given class may be enqueued.
// The current implementation is a no-op stub so future budget wiring is a
// single-call change at the scanner boundary.
func (g *BudgetGate) Check(_ string) Decision {
	decision := Decision{Allowed: true}
	if g == nil || g.budget == nil {
		return decision
	}
	if g.budget.CostLimitUSD > 0 {
		decision.RemainingUSD = g.budget.CostLimitUSD
	}
	return decision
}
