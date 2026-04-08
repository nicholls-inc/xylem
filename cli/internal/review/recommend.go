package review

import "fmt"

type Recommendation string

const (
	RecommendationKeep             Recommendation = "keep"
	RecommendationInvestigate      Recommendation = "investigate"
	RecommendationPruneCandidate   Recommendation = "prune-candidate"
	RecommendationInsufficientData Recommendation = "insufficient-data"
)

type aggregateStats struct {
	Samples          int
	FailureCount     int
	BudgetAlertRuns  int
	EvalIssueRuns    int
	EvidenceFailures int
	CostAnomalyRuns  int
}

func recommendGroup(stats aggregateStats, minSamples int) (Recommendation, []string) {
	if stats.Samples < minSamples {
		return RecommendationInsufficientData, []string{
			fmt.Sprintf("only %d sample(s); need at least %d", stats.Samples, minSamples),
		}
	}

	reasons := make([]string, 0, 5)
	if stats.FailureCount > 0 {
		reasons = append(reasons, fmt.Sprintf("%d failure(s) in reviewed runs", stats.FailureCount))
	}
	if stats.BudgetAlertRuns > 0 {
		reasons = append(reasons, fmt.Sprintf("%d run(s) triggered budget alerts", stats.BudgetAlertRuns))
	}
	if stats.EvalIssueRuns > 0 {
		reasons = append(reasons, fmt.Sprintf("%d run(s) had eval issues", stats.EvalIssueRuns))
	}
	if stats.EvidenceFailures > 0 {
		reasons = append(reasons, fmt.Sprintf("%d failed evidence claim(s)", stats.EvidenceFailures))
	}
	if stats.CostAnomalyRuns > 0 {
		reasons = append(reasons, fmt.Sprintf("%d run(s) showed cost anomalies", stats.CostAnomalyRuns))
	}
	if len(reasons) > 0 {
		return RecommendationInvestigate, reasons
	}

	if stats.Samples >= minSamples*2 {
		return RecommendationPruneCandidate, []string{
			fmt.Sprintf("%d clean samples with no recurring failures, eval issues, or budget alerts", stats.Samples),
		}
	}

	return RecommendationKeep, []string{
		fmt.Sprintf("%d clean sample(s), but keep collecting evidence before pruning", stats.Samples),
	}
}
