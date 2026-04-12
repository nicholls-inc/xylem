package cost

import (
	"path/filepath"
	"time"
)

// SpentToday aggregates TotalCostUSD from all cost-report.json files under
// <stateDir>/phases/*/cost-report.json whose GeneratedAt falls within today
// (midnight UTC to now). Reports whose WorkflowClass is non-empty are matched
// against class; reports whose WorkflowClass is empty fall back to Workflow.
//
// Returns (totalSpend, classSpend, error). Unreadable or corrupt reports are
// skipped. A missing phases directory returns (0, 0, nil).
func SpentToday(stateDir, class string, now time.Time) (total float64, forClass float64, err error) {
	nowUTC := now.UTC()
	midnight := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	pattern := filepath.Join(stateDir, "phases", "*", "cost-report.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, 0, err
	}
	for _, path := range matches {
		r, loadErr := LoadReport(path)
		if loadErr != nil {
			continue // corrupt / unreadable reports are skipped
		}
		if r.GeneratedAt.Before(midnight) {
			continue
		}
		total += r.TotalCostUSD
		reportClass := r.WorkflowClass
		if reportClass == "" {
			reportClass = r.Workflow
		}
		if reportClass == class {
			forClass += r.TotalCostUSD
		}
	}
	return total, forClass, nil
}
