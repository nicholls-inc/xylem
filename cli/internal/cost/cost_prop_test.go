package cost

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// genAgentRole generates a valid AgentRole.
func genAgentRole() *rapid.Generator[AgentRole] {
	return rapid.SampledFrom([]AgentRole{
		RolePlanner, RoleGenerator, RoleEvaluator, RoleSubAgent,
	})
}

// genPurpose generates a valid Purpose.
func genPurpose() *rapid.Generator[Purpose] {
	return rapid.SampledFrom([]Purpose{
		PurposeContext, PurposeReasoning, PurposeToolCall,
		PurposeCompaction, PurposeEvaluation,
	})
}

// genModel generates a model name.
func genModel() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		"claude-sonnet", "claude-haiku", "claude-opus",
	})
}

// genUsageRecord generates a valid UsageRecord with non-negative values.
func genUsageRecord() *rapid.Generator[UsageRecord] {
	return rapid.Custom(func(t *rapid.T) UsageRecord {
		return UsageRecord{
			MissionID:    rapid.StringMatching(`m-[0-9]{1,4}`).Draw(t, "mission_id"),
			AgentRole:    genAgentRole().Draw(t, "role"),
			Purpose:      genPurpose().Draw(t, "purpose"),
			Model:        genModel().Draw(t, "model"),
			InputTokens:  rapid.IntRange(0, 100000).Draw(t, "input_tokens"),
			OutputTokens: rapid.IntRange(0, 100000).Draw(t, "output_tokens"),
			CostUSD:      float64(rapid.IntRange(0, 10000).Draw(t, "cost_cents")) / 100.0,
			Timestamp:    time.Now(),
		}
	})
}

// TestPropTotalCostEqualsSum verifies that TotalCost always equals
// the sum of all recorded CostUSD values.
func TestPropTotalCostEqualsSum(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := rapid.SliceOfN(genUsageRecord(), 1, 50).Draw(t, "records")
		tr := NewTracker(nil)

		var expectedCost float64
		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectedCost += r.CostUSD
		}

		if math.Abs(tr.TotalCost()-expectedCost) > 1e-6 {
			t.Fatalf("TotalCost() = %f, expected %f", tr.TotalCost(), expectedCost)
		}
	})
}

// TestPropTotalTokensEqualsSum verifies token sum invariant.
func TestPropTotalTokensEqualsSum(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := rapid.SliceOfN(genUsageRecord(), 1, 50).Draw(t, "records")
		tr := NewTracker(nil)

		var expectedTokens int
		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectedTokens += r.InputTokens + r.OutputTokens
		}

		if tr.TotalTokens() != expectedTokens {
			t.Fatalf("TotalTokens() = %d, expected %d", tr.TotalTokens(), expectedTokens)
		}
	})
}

// TestPropCostByRoleSumsToTotal verifies that per-role costs sum to total.
func TestPropCostByRoleSumsToTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := rapid.SliceOfN(genUsageRecord(), 1, 50).Draw(t, "records")
		tr := NewTracker(nil)

		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		var sum float64
		for _, v := range tr.CostByRole() {
			sum += v
		}

		if math.Abs(sum-tr.TotalCost()) > 1e-6 {
			t.Fatalf("CostByRole sum (%f) != TotalCost (%f)", sum, tr.TotalCost())
		}
	})
}

// TestPropCostByPurposeSumsToTotal verifies that per-purpose costs sum to total.
func TestPropCostByPurposeSumsToTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := rapid.SliceOfN(genUsageRecord(), 1, 50).Draw(t, "records")
		tr := NewTracker(nil)

		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		var sum float64
		for _, v := range tr.CostByPurpose() {
			sum += v
		}

		if math.Abs(sum-tr.TotalCost()) > 1e-6 {
			t.Fatalf("CostByPurpose sum (%f) != TotalCost (%f)", sum, tr.TotalCost())
		}
	})
}

// TestPropCostByModelSumsToTotal verifies that per-model costs sum to total.
func TestPropCostByModelSumsToTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := rapid.SliceOfN(genUsageRecord(), 1, 50).Draw(t, "records")
		tr := NewTracker(nil)

		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		var sum float64
		for _, v := range tr.CostByModel() {
			sum += v
		}

		if math.Abs(sum-tr.TotalCost()) > 1e-6 {
			t.Fatalf("CostByModel sum (%f) != TotalCost (%f)", sum, tr.TotalCost())
		}
	})
}

// TestPropBudgetExceededIsMonotonic verifies that once BudgetExceeded returns
// true, it never reverts to false.
func TestPropBudgetExceededIsMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		limit := float64(rapid.IntRange(1, 100).Draw(t, "limit_cents")) / 100.0
		budget := &Budget{CostLimitUSD: limit}
		tr := NewTracker(budget)

		records := rapid.SliceOfN(genUsageRecord(), 1, 30).Draw(t, "records")

		wasExceeded := false
		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			exceeded := tr.BudgetExceeded()
			if wasExceeded && !exceeded {
				t.Fatal("BudgetExceeded reverted from true to false")
			}
			wasExceeded = exceeded
		}
	})
}

// TestPropReportRoundTrip verifies that saving and loading a report preserves data.
func TestPropReportRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := rapid.SliceOfN(genUsageRecord(), 1, 20).Draw(t, "records")
		tr := NewTracker(nil)

		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		missionID := rapid.StringMatching(`m-[0-9]{1,4}`).Draw(t, "mission_id")
		report := tr.Report(missionID)

		dir, err := os.MkdirTemp("", "cost-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp error: %v", err)
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "report.json")

		if err := SaveReport(path, report); err != nil {
			t.Fatalf("SaveReport error: %v", err)
		}

		loaded, err := LoadReport(path)
		if err != nil {
			t.Fatalf("LoadReport error: %v", err)
		}

		if loaded.MissionID != report.MissionID {
			t.Fatalf("MissionID mismatch: %s != %s", loaded.MissionID, report.MissionID)
		}
		if loaded.TotalTokens != report.TotalTokens {
			t.Fatalf("TotalTokens mismatch: %d != %d", loaded.TotalTokens, report.TotalTokens)
		}
		if math.Abs(loaded.TotalCostUSD-report.TotalCostUSD) > 1e-9 {
			t.Fatalf("TotalCostUSD mismatch: %f != %f", loaded.TotalCostUSD, report.TotalCostUSD)
		}
		if loaded.RecordCount != report.RecordCount {
			t.Fatalf("RecordCount mismatch: %d != %d", loaded.RecordCount, report.RecordCount)
		}

		// Verify breakdown maps have the same keys and values.
		for role, expected := range report.ByRole {
			if actual, ok := loaded.ByRole[role]; !ok || math.Abs(actual-expected) > 1e-9 {
				t.Fatalf("ByRole[%s] mismatch: got %f, want %f", role, actual, expected)
			}
		}
		for purpose, expected := range report.ByPurpose {
			if actual, ok := loaded.ByPurpose[purpose]; !ok || math.Abs(actual-expected) > 1e-9 {
				t.Fatalf("ByPurpose[%s] mismatch: got %f, want %f", purpose, actual, expected)
			}
		}
		for model, expected := range report.ByModel {
			if actual, ok := loaded.ByModel[model]; !ok || math.Abs(actual-expected) > 1e-9 {
				t.Fatalf("ByModel[%s] mismatch: got %f, want %f", model, actual, expected)
			}
		}
	})
}

// TestPropAnomalyRatioAboveThreshold verifies that every anomaly
// reported has a ratio > 2.0.
func TestPropAnomalyRatioAboveThreshold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a current report with potentially high values.
		currentCost := float64(rapid.IntRange(1, 10000).Draw(t, "current_cost")) / 100.0
		currentTokens := rapid.IntRange(1, 100000).Draw(t, "current_tokens")

		current := &CostReport{
			MissionID:    "m-test",
			TotalCostUSD: currentCost,
			TotalTokens:  currentTokens,
			ByRole:       make(map[AgentRole]float64),
		}

		// Generate history.
		histSize := rapid.IntRange(1, 5).Draw(t, "hist_size")
		history := make([]*CostReport, histSize)
		for i := 0; i < histSize; i++ {
			hCost := float64(rapid.IntRange(1, 10000).Draw(t, "hist_cost")) / 100.0
			hTokens := rapid.IntRange(1, 100000).Draw(t, "hist_tokens")
			history[i] = &CostReport{
				TotalCostUSD: hCost,
				TotalTokens:  hTokens,
				ByRole:       make(map[AgentRole]float64),
			}
		}

		anomalies := DetectAnomalies(current, history)
		for _, a := range anomalies {
			if a.Ratio <= anomalyThreshold {
				t.Fatalf("anomaly %q has ratio %f which is <= threshold %f",
					a.Metric, a.Ratio, anomalyThreshold)
			}
		}
	})
}

// TestPropZeroCostRecordPreservesTotal verifies that recording a zero-cost
// event never changes the running total.
func TestPropZeroCostRecordPreservesTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := rapid.SliceOfN(genUsageRecord(), 1, 20).Draw(t, "records")
		tr := NewTracker(nil)

		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		before := tr.TotalCost()

		zeroRecord := UsageRecord{
			CostUSD:      0,
			InputTokens:  0,
			OutputTokens: 0,
			Timestamp:    time.Now(),
		}
		if err := tr.Record(zeroRecord); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		after := tr.TotalCost()
		if math.Abs(before-after) > 1e-9 {
			t.Fatalf("zero-cost record changed TotalCost: %f -> %f", before, after)
		}
	})
}

// TestPropWindowedTotalCostEqualsSum verifies that TotalCost always
// equals the sum of all CostUSD values regardless of window rotation.
func TestPropWindowedTotalCostEqualsSum(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		window := time.Duration(rapid.IntRange(1, 60).Draw(t, "window_minutes")) * time.Minute
		budget := Budget{CostLimitUSD: 1000.0, Window: window}
		wt, err := NewWindowedTracker(budget)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		records := rapid.SliceOfN(genUsageRecord(), 1, 50).Draw(t, "records")

		var expectedCost float64
		for i, r := range records {
			// Space records out so some cross window boundaries.
			r.Timestamp = base.Add(time.Duration(i) * time.Duration(rapid.IntRange(1, 120).Draw(t, "offset_minutes")) * time.Minute)
			if err := wt.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectedCost += r.CostUSD
		}

		if math.Abs(wt.TotalCost()-expectedCost) > 1e-6 {
			t.Fatalf("TotalCost() = %f, expected %f", wt.TotalCost(), expectedCost)
		}
	})
}

// TestPropWindowedCostsNeverNegative verifies that both window cost and total
// cost are never negative.
func TestPropWindowedCostsNeverNegative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		window := time.Duration(rapid.IntRange(1, 60).Draw(t, "window_minutes")) * time.Minute
		budget := Budget{CostLimitUSD: 1000.0, Window: window}
		wt, err := NewWindowedTracker(budget)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		records := rapid.SliceOfN(genUsageRecord(), 1, 50).Draw(t, "records")

		for i, r := range records {
			r.Timestamp = base.Add(time.Duration(i) * time.Duration(rapid.IntRange(1, 120).Draw(t, "offset_minutes")) * time.Minute)
			if err := wt.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if wt.WindowCost() < 0 {
				t.Fatalf("WindowCost() = %f, must be non-negative", wt.WindowCost())
			}
			if wt.TotalCost() < 0 {
				t.Fatalf("TotalCost() = %f, must be non-negative", wt.TotalCost())
			}
		}
	})
}

// TestPropWindowedExceededResetsOnRotation verifies that the exceeded flag
// resets to false when the window rotates.
func TestPropWindowedExceededResetsOnRotation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		window := time.Duration(rapid.IntRange(1, 10).Draw(t, "window_minutes")) * time.Minute
		// Use a small limit so we exceed easily.
		limit := float64(rapid.IntRange(1, 10).Draw(t, "limit_cents")) / 100.0
		budget := Budget{CostLimitUSD: limit, Window: window}
		wt, err := NewWindowedTracker(budget)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

		// Exceed in window 1.
		bigCost := limit + 1.0
		if err := wt.Record(UsageRecord{CostUSD: bigCost, Timestamp: base}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !wt.WindowExceeded() {
			t.Fatal("expected WindowExceeded after exceeding budget")
		}

		// Record a small amount in a new window — exceeded must reset.
		newWindowTime := base.Add(window + time.Second)
		smallCost := float64(rapid.IntRange(0, 1).Draw(t, "small_cost_cents")) / 100.0
		if err := wt.Record(UsageRecord{CostUSD: smallCost, Timestamp: newWindowTime}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if smallCost < limit && wt.WindowExceeded() {
			t.Fatal("WindowExceeded should reset to false after window rotation with sub-limit cost")
		}
	})
}

// TestPropBudgetUtilizationMatchesCost verifies utilization = totalCost / limit.
func TestPropBudgetUtilizationMatchesCost(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		limit := float64(rapid.IntRange(1, 1000).Draw(t, "limit_cents")) / 100.0
		budget := &Budget{CostLimitUSD: limit}
		tr := NewTracker(budget)

		records := rapid.SliceOfN(genUsageRecord(), 1, 20).Draw(t, "records")
		for _, r := range records {
			if err := tr.Record(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		expected := tr.TotalCost() / limit
		actual := tr.BudgetUtilization()

		if math.Abs(actual-expected) > 1e-9 {
			t.Fatalf("BudgetUtilization() = %f, expected %f (cost=%f, limit=%f)",
				actual, expected, tr.TotalCost(), limit)
		}
	})
}
