package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	metricsSchemaVersion = "1"
	reportFileName       = "xylem-eval-report.json"
)

type CheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

type TrialMetrics struct {
	SchemaVersion        string        `json:"schema_version"`
	TaskID               string        `json:"task_id"`
	Reward               float64       `json:"reward"`
	Success              bool          `json:"success"`
	LatencySeconds       float64       `json:"latency_seconds"`
	CostUSDEst           float64       `json:"cost_usd_est"`
	RetryCount           int           `json:"retry_count"`
	ToolFailureCount     int           `json:"tool_failure_count"`
	PolicyViolationCount int           `json:"policy_violation_count"`
	EvidenceScore        float64       `json:"evidence_score"`
	EvidenceLevel        string        `json:"evidence_level,omitempty"`
	BudgetExceeded       bool          `json:"budget_exceeded"`
	Checks               []CheckResult `json:"checks,omitempty"`
}

type TrialReport struct {
	TaskID           string        `json:"task_id"`
	TaskName         string        `json:"task_name,omitempty"`
	TrialName        string        `json:"trial_name"`
	Reward           float64       `json:"reward"`
	Success          bool          `json:"success"`
	LatencySeconds   float64       `json:"latency_seconds"`
	CostUSDEst       float64       `json:"cost_usd_est"`
	RetryCount       int           `json:"retry_count"`
	ToolFailureCount int           `json:"tool_failure_count"`
	PolicyViolations int           `json:"policy_violations"`
	EvidenceScore    float64       `json:"evidence_score"`
	EvidenceLevel    string        `json:"evidence_level,omitempty"`
	BudgetExceeded   bool          `json:"budget_exceeded"`
	Checks           []CheckResult `json:"checks,omitempty"`
	Error            string        `json:"error,omitempty"`
}

type AggregateSummary struct {
	TrialCount               int     `json:"trial_count"`
	SuccessCount             int     `json:"success_count"`
	SuccessRate              float64 `json:"success_rate"`
	AverageReward            float64 `json:"average_reward"`
	AverageLatencySeconds    float64 `json:"average_latency_seconds"`
	AverageCostUSDEst        float64 `json:"average_cost_usd_est"`
	AverageRetryCount        float64 `json:"average_retry_count"`
	AverageToolFailureCount  float64 `json:"average_tool_failure_count"`
	AveragePolicyViolations  float64 `json:"average_policy_violations"`
	AverageEvidenceScore     float64 `json:"average_evidence_score"`
	BudgetExceededTrialCount int     `json:"budget_exceeded_trial_count"`
}

type RubricCriterionSummary struct {
	Name               string  `json:"name"`
	PassCount          int     `json:"pass_count"`
	FailCount          int     `json:"fail_count"`
	NotApplicableCount int     `json:"not_applicable_count"`
	PassRate           float64 `json:"pass_rate"`
}

type RubricTrial struct {
	TrialName string            `json:"trial_name"`
	Summary   string            `json:"summary"`
	Checks    map[string]string `json:"checks"`
}

type RubricReport struct {
	Name       string                   `json:"name"`
	JobSummary string                   `json:"job_summary"`
	Criteria   []RubricCriterionSummary `json:"criteria"`
	Trials     []RubricTrial            `json:"trials"`
}

type RunReport struct {
	SchemaVersion string           `json:"schema_version"`
	GeneratedAt   time.Time        `json:"generated_at"`
	JobDir        string           `json:"job_dir"`
	Trials        []TrialReport    `json:"trials"`
	Aggregate     AggregateSummary `json:"aggregate"`
	Rubrics       []RubricReport   `json:"rubrics,omitempty"`
}

type AggregateDelta struct {
	SuccessRate             float64 `json:"success_rate"`
	AverageReward           float64 `json:"average_reward"`
	AverageLatencySeconds   float64 `json:"average_latency_seconds"`
	AverageCostUSDEst       float64 `json:"average_cost_usd_est"`
	AverageRetryCount       float64 `json:"average_retry_count"`
	AverageToolFailureCount float64 `json:"average_tool_failure_count"`
	AveragePolicyViolations float64 `json:"average_policy_violations"`
	AverageEvidenceScore    float64 `json:"average_evidence_score"`
}

type TrialDelta struct {
	SuccessRate      float64 `json:"success_rate"`
	Reward           float64 `json:"reward"`
	LatencySeconds   float64 `json:"latency_seconds"`
	CostUSDEst       float64 `json:"cost_usd_est"`
	RetryCount       float64 `json:"retry_count"`
	ToolFailureCount float64 `json:"tool_failure_count"`
	PolicyViolations float64 `json:"policy_violations"`
	EvidenceScore    float64 `json:"evidence_score"`
}

type TaskSummary struct {
	TaskID                   string  `json:"task_id"`
	TaskName                 string  `json:"task_name,omitempty"`
	TrialCount               int     `json:"trial_count"`
	SuccessRate              float64 `json:"success_rate"`
	AverageReward            float64 `json:"average_reward"`
	AverageLatencySeconds    float64 `json:"average_latency_seconds"`
	AverageCostUSDEst        float64 `json:"average_cost_usd_est"`
	AverageRetryCount        float64 `json:"average_retry_count"`
	AverageToolFailureCount  float64 `json:"average_tool_failure_count"`
	AveragePolicyViolations  float64 `json:"average_policy_violations"`
	AverageEvidenceScore     float64 `json:"average_evidence_score"`
	BudgetExceededTrialCount int     `json:"budget_exceeded_trial_count"`
}

type TrialComparison struct {
	TaskID     string       `json:"task_id"`
	Baseline   *TaskSummary `json:"baseline,omitempty"`
	Candidate  *TaskSummary `json:"candidate,omitempty"`
	Delta      TrialDelta   `json:"delta"`
	Regression bool         `json:"regression"`
	Improved   bool         `json:"improved"`
}

type CriterionDelta struct {
	Name              string  `json:"name"`
	BaselinePassRate  float64 `json:"baseline_pass_rate"`
	CandidatePassRate float64 `json:"candidate_pass_rate"`
	Delta             float64 `json:"delta"`
}

type RubricComparison struct {
	Name             string           `json:"name"`
	BaselineSummary  string           `json:"baseline_summary"`
	CandidateSummary string           `json:"candidate_summary"`
	CriterionDeltas  []CriterionDelta `json:"criterion_deltas"`
}

type ComparisonReport struct {
	SchemaVersion string             `json:"schema_version"`
	GeneratedAt   time.Time          `json:"generated_at"`
	BaselineDir   string             `json:"baseline_dir"`
	CandidateDir  string             `json:"candidate_dir"`
	Baseline      AggregateSummary   `json:"baseline"`
	Candidate     AggregateSummary   `json:"candidate"`
	Delta         AggregateDelta     `json:"delta"`
	Trials        []TrialComparison  `json:"trials"`
	Rubrics       []RubricComparison `json:"rubrics,omitempty"`
	Verdict       string             `json:"verdict"`
	Regressions   []string           `json:"regressions,omitempty"`
	Improvements  []string           `json:"improvements,omitempty"`
}

type harborTrialResult struct {
	TaskName    string     `json:"task_name"`
	TrialName   string     `json:"trial_name"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	AgentResult *struct {
		CostUSD *float64 `json:"cost_usd"`
	} `json:"agent_result"`
	VerifierResult *struct {
		Rewards map[string]float64 `json:"rewards"`
	} `json:"verifier_result"`
	ExceptionInfo *struct {
		ExceptionType    string `json:"exception_type"`
		ExceptionMessage string `json:"exception_message"`
	} `json:"exception_info"`
}

type harborAnalysis struct {
	JobSummary string `json:"job_summary"`
	Trials     []struct {
		TrialName string `json:"trial_name"`
		Summary   string `json:"summary"`
		Checks    map[string]struct {
			Outcome string `json:"outcome"`
		} `json:"checks"`
	} `json:"trials"`
}

func ReportPath(jobDir string) string {
	return filepath.Join(jobDir, reportFileName)
}

func LoadOrBuildRunReport(jobDir string) (*RunReport, error) {
	report, err := ReadRunReport(ReportPath(jobDir))
	if err == nil {
		return report, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return BuildRunReport(jobDir)
}

func ReadRunReport(path string) (*RunReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var report RunReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parse run report %q: %w", path, err)
	}
	return &report, nil
}

func WriteRunReport(path string, report *RunReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run report: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write run report: %w", err)
	}
	return nil
}

func BuildRunReport(jobDir string) (*RunReport, error) {
	absJobDir, err := filepath.Abs(jobDir)
	if err != nil {
		return nil, fmt.Errorf("resolve job directory %q: %w", jobDir, err)
	}

	entries, err := os.ReadDir(absJobDir)
	if err != nil {
		return nil, fmt.Errorf("read job directory %q: %w", absJobDir, err)
	}

	var trials []TrialReport
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		trialPath := filepath.Join(absJobDir, entry.Name())
		resultPath := filepath.Join(trialPath, "result.json")
		if _, err := os.Stat(resultPath); err != nil {
			continue
		}
		trial, err := loadTrialReport(trialPath)
		if err != nil {
			return nil, err
		}
		trials = append(trials, *trial)
	}

	sort.Slice(trials, func(i, j int) bool {
		if trials[i].TaskID == trials[j].TaskID {
			return trials[i].TrialName < trials[j].TrialName
		}
		return trials[i].TaskID < trials[j].TaskID
	})

	rubrics, err := loadRubricReports(absJobDir)
	if err != nil {
		return nil, err
	}

	return &RunReport{
		SchemaVersion: metricsSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		JobDir:        absJobDir,
		Trials:        trials,
		Aggregate:     aggregateTrials(trials),
		Rubrics:       rubrics,
	}, nil
}

func CompareReports(baseline, candidate *RunReport) *ComparisonReport {
	baselineTrials := groupTrialsByTask(baseline.Trials)
	candidateTrials := groupTrialsByTask(candidate.Trials)

	taskIDs := make([]string, 0, len(baselineTrials)+len(candidateTrials))
	seen := make(map[string]bool, len(baselineTrials)+len(candidateTrials))
	for taskID := range baselineTrials {
		seen[taskID] = true
		taskIDs = append(taskIDs, taskID)
	}
	for taskID := range candidateTrials {
		if !seen[taskID] {
			taskIDs = append(taskIDs, taskID)
		}
	}
	sort.Strings(taskIDs)

	comparisons := make([]TrialComparison, 0, len(taskIDs))
	var regressions []string
	var improvements []string
	for _, taskID := range taskIDs {
		comparison := compareTrial(
			taskID,
			aggregateTaskTrials(taskID, baselineTrials[taskID]),
			aggregateTaskTrials(taskID, candidateTrials[taskID]),
			seenTask(baselineTrials, taskID),
			seenTask(candidateTrials, taskID),
		)
		comparisons = append(comparisons, comparison)
		if comparison.Regression {
			regressions = append(regressions, fmt.Sprintf("%s regressed", taskID))
		}
		if comparison.Improved {
			improvements = append(improvements, fmt.Sprintf("%s improved", taskID))
		}
	}

	delta := AggregateDelta{
		SuccessRate:             candidate.Aggregate.SuccessRate - baseline.Aggregate.SuccessRate,
		AverageReward:           candidate.Aggregate.AverageReward - baseline.Aggregate.AverageReward,
		AverageLatencySeconds:   candidate.Aggregate.AverageLatencySeconds - baseline.Aggregate.AverageLatencySeconds,
		AverageCostUSDEst:       candidate.Aggregate.AverageCostUSDEst - baseline.Aggregate.AverageCostUSDEst,
		AverageRetryCount:       candidate.Aggregate.AverageRetryCount - baseline.Aggregate.AverageRetryCount,
		AverageToolFailureCount: candidate.Aggregate.AverageToolFailureCount - baseline.Aggregate.AverageToolFailureCount,
		AveragePolicyViolations: candidate.Aggregate.AveragePolicyViolations - baseline.Aggregate.AveragePolicyViolations,
		AverageEvidenceScore:    candidate.Aggregate.AverageEvidenceScore - baseline.Aggregate.AverageEvidenceScore,
	}

	if delta.SuccessRate < 0 {
		regressions = append(regressions, "aggregate success rate decreased")
	}
	if delta.AverageReward < 0 {
		regressions = append(regressions, "aggregate reward decreased")
	}
	if delta.AverageToolFailureCount > 0 {
		regressions = append(regressions, "tool failure rate increased")
	}
	if delta.AveragePolicyViolations > 0 {
		regressions = append(regressions, "policy violations increased")
	}
	if delta.AverageEvidenceScore < 0 {
		regressions = append(regressions, "evidence quality decreased")
	}
	if delta.SuccessRate > 0 {
		improvements = append(improvements, "aggregate success rate increased")
	}
	if delta.AverageReward > 0 {
		improvements = append(improvements, "aggregate reward increased")
	}
	if delta.AverageLatencySeconds < 0 {
		improvements = append(improvements, "average latency decreased")
	}
	if delta.AverageCostUSDEst < 0 {
		improvements = append(improvements, "average cost decreased")
	}
	if delta.AverageEvidenceScore > 0 {
		improvements = append(improvements, "evidence quality increased")
	}

	return &ComparisonReport{
		SchemaVersion: metricsSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		BaselineDir:   baseline.JobDir,
		CandidateDir:  candidate.JobDir,
		Baseline:      baseline.Aggregate,
		Candidate:     candidate.Aggregate,
		Delta:         delta,
		Trials:        comparisons,
		Rubrics:       compareRubrics(baseline.Rubrics, candidate.Rubrics),
		Verdict:       compareVerdict(regressions, improvements),
		Regressions:   dedupeAndSort(regressions),
		Improvements:  dedupeAndSort(improvements),
	}
}

func loadTrialReport(trialPath string) (*TrialReport, error) {
	var trialResult harborTrialResult
	if err := readJSONFile(filepath.Join(trialPath, "result.json"), &trialResult); err != nil {
		return nil, fmt.Errorf("load harbor trial result from %q: %w", trialPath, err)
	}

	metricsPath := filepath.Join(trialPath, "verifier", "reward.json")
	var metrics TrialMetrics
	if err := readJSONFile(metricsPath, &metrics); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load trial metrics from %q: %w", metricsPath, err)
	}

	taskID := firstNonEmpty(metrics.TaskID, trialResult.TaskName, trialResult.TrialName)
	reward := metrics.Reward
	if reward == 0 && trialResult.VerifierResult != nil {
		if value, ok := trialResult.VerifierResult.Rewards["reward"]; ok {
			reward = value
		}
	}
	success := metrics.Success
	if !success {
		success = reward >= 1.0 && trialResult.ExceptionInfo == nil
	}
	latencySeconds := metrics.LatencySeconds
	if latencySeconds == 0 && trialResult.StartedAt != nil && trialResult.FinishedAt != nil {
		latencySeconds = trialResult.FinishedAt.Sub(*trialResult.StartedAt).Seconds()
	}
	costUSDEst := metrics.CostUSDEst
	if costUSDEst == 0 && trialResult.AgentResult != nil && trialResult.AgentResult.CostUSD != nil {
		costUSDEst = *trialResult.AgentResult.CostUSD
	}
	report := &TrialReport{
		TaskID:           taskID,
		TaskName:         trialResult.TaskName,
		TrialName:        trialResult.TrialName,
		Reward:           reward,
		Success:          success,
		LatencySeconds:   latencySeconds,
		CostUSDEst:       costUSDEst,
		RetryCount:       metrics.RetryCount,
		ToolFailureCount: metrics.ToolFailureCount,
		PolicyViolations: metrics.PolicyViolationCount,
		EvidenceScore:    metrics.EvidenceScore,
		EvidenceLevel:    metrics.EvidenceLevel,
		BudgetExceeded:   metrics.BudgetExceeded,
		Checks:           metrics.Checks,
	}
	if trialResult.ExceptionInfo != nil {
		report.Error = strings.TrimSpace(strings.Join([]string{
			trialResult.ExceptionInfo.ExceptionType,
			trialResult.ExceptionInfo.ExceptionMessage,
		}, ": "))
	}
	return report, nil
}

func loadRubricReports(jobDir string) ([]RubricReport, error) {
	analysisDir := filepath.Join(jobDir, "analysis")
	entries, err := os.ReadDir(analysisDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read analysis directory %q: %w", analysisDir, err)
	}

	var reports []RubricReport
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var analysis harborAnalysis
		path := filepath.Join(analysisDir, entry.Name())
		if err := readJSONFile(path, &analysis); err != nil {
			return nil, fmt.Errorf("load rubric analysis from %q: %w", path, err)
		}
		reports = append(reports, buildRubricReport(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), analysis))
	}

	sort.Slice(reports, func(i, j int) bool { return reports[i].Name < reports[j].Name })
	return reports, nil
}

func buildRubricReport(name string, analysis harborAnalysis) RubricReport {
	criteriaTotals := map[string]*RubricCriterionSummary{}
	trials := make([]RubricTrial, 0, len(analysis.Trials))
	for _, trial := range analysis.Trials {
		checks := make(map[string]string, len(trial.Checks))
		for criterion, check := range trial.Checks {
			checks[criterion] = check.Outcome
			summary := criteriaTotals[criterion]
			if summary == nil {
				summary = &RubricCriterionSummary{Name: criterion}
				criteriaTotals[criterion] = summary
			}
			switch check.Outcome {
			case "pass":
				summary.PassCount++
			case "fail":
				summary.FailCount++
			default:
				summary.NotApplicableCount++
			}
		}
		trials = append(trials, RubricTrial{
			TrialName: trial.TrialName,
			Summary:   trial.Summary,
			Checks:    checks,
		})
	}

	criteria := make([]RubricCriterionSummary, 0, len(criteriaTotals))
	for _, summary := range criteriaTotals {
		denominator := summary.PassCount + summary.FailCount
		if denominator > 0 {
			summary.PassRate = float64(summary.PassCount) / float64(denominator)
		}
		criteria = append(criteria, *summary)
	}
	sort.Slice(criteria, func(i, j int) bool { return criteria[i].Name < criteria[j].Name })
	sort.Slice(trials, func(i, j int) bool { return trials[i].TrialName < trials[j].TrialName })

	return RubricReport{
		Name:       name,
		JobSummary: analysis.JobSummary,
		Criteria:   criteria,
		Trials:     trials,
	}
}

func aggregateTrials(trials []TrialReport) AggregateSummary {
	if len(trials) == 0 {
		return AggregateSummary{}
	}

	var aggregate AggregateSummary
	aggregate.TrialCount = len(trials)
	for _, trial := range trials {
		aggregate.AverageReward += trial.Reward
		aggregate.AverageLatencySeconds += trial.LatencySeconds
		aggregate.AverageCostUSDEst += trial.CostUSDEst
		aggregate.AverageRetryCount += float64(trial.RetryCount)
		aggregate.AverageToolFailureCount += float64(trial.ToolFailureCount)
		aggregate.AveragePolicyViolations += float64(trial.PolicyViolations)
		aggregate.AverageEvidenceScore += trial.EvidenceScore
		if trial.Success {
			aggregate.SuccessCount++
		}
		if trial.BudgetExceeded {
			aggregate.BudgetExceededTrialCount++
		}
	}

	count := float64(len(trials))
	aggregate.SuccessRate = float64(aggregate.SuccessCount) / count
	aggregate.AverageReward /= count
	aggregate.AverageLatencySeconds /= count
	aggregate.AverageCostUSDEst /= count
	aggregate.AverageRetryCount /= count
	aggregate.AverageToolFailureCount /= count
	aggregate.AveragePolicyViolations /= count
	aggregate.AverageEvidenceScore /= count
	return aggregate
}

func compareTrial(taskID string, baseline TaskSummary, candidate TaskSummary, hasBaseline, hasCandidate bool) TrialComparison {
	comparison := TrialComparison{
		TaskID: taskID,
	}
	if hasBaseline {
		copy := baseline
		comparison.Baseline = &copy
	}
	if hasCandidate {
		copy := candidate
		comparison.Candidate = &copy
	}
	if hasBaseline && hasCandidate {
		comparison.Delta = TrialDelta{
			SuccessRate:      candidate.SuccessRate - baseline.SuccessRate,
			Reward:           candidate.AverageReward - baseline.AverageReward,
			LatencySeconds:   candidate.AverageLatencySeconds - baseline.AverageLatencySeconds,
			CostUSDEst:       candidate.AverageCostUSDEst - baseline.AverageCostUSDEst,
			RetryCount:       candidate.AverageRetryCount - baseline.AverageRetryCount,
			ToolFailureCount: candidate.AverageToolFailureCount - baseline.AverageToolFailureCount,
			PolicyViolations: candidate.AveragePolicyViolations - baseline.AveragePolicyViolations,
			EvidenceScore:    candidate.AverageEvidenceScore - baseline.AverageEvidenceScore,
		}
		comparison.Regression = comparison.Delta.Reward < 0 ||
			comparison.Delta.SuccessRate < 0 ||
			comparison.Delta.ToolFailureCount > 0 ||
			comparison.Delta.PolicyViolations > 0 ||
			comparison.Delta.EvidenceScore < 0
		comparison.Improved = comparison.Delta.Reward > 0 ||
			comparison.Delta.SuccessRate > 0 ||
			comparison.Delta.LatencySeconds < 0 ||
			comparison.Delta.CostUSDEst < 0 ||
			comparison.Delta.EvidenceScore > 0
		return comparison
	}
	comparison.Regression = hasBaseline && !hasCandidate
	comparison.Improved = hasCandidate && !hasBaseline
	return comparison
}

func compareRubrics(baseline, candidate []RubricReport) []RubricComparison {
	baselineByName := make(map[string]RubricReport, len(baseline))
	for _, rubric := range baseline {
		baselineByName[rubric.Name] = rubric
	}
	candidateByName := make(map[string]RubricReport, len(candidate))
	for _, rubric := range candidate {
		candidateByName[rubric.Name] = rubric
	}

	var names []string
	seen := map[string]bool{}
	for name := range baselineByName {
		seen[name] = true
		names = append(names, name)
	}
	for name := range candidateByName {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	comparisons := make([]RubricComparison, 0, len(names))
	for _, name := range names {
		comparison := RubricComparison{Name: name}
		if baselineRubric, ok := baselineByName[name]; ok {
			comparison.BaselineSummary = baselineRubric.JobSummary
		}
		if candidateRubric, ok := candidateByName[name]; ok {
			comparison.CandidateSummary = candidateRubric.JobSummary
		}
		comparison.CriterionDeltas = compareCriterionSummaries(baselineByName[name].Criteria, candidateByName[name].Criteria)
		comparisons = append(comparisons, comparison)
	}
	return comparisons
}

func compareCriterionSummaries(baseline, candidate []RubricCriterionSummary) []CriterionDelta {
	baselineByName := make(map[string]RubricCriterionSummary, len(baseline))
	for _, criterion := range baseline {
		baselineByName[criterion.Name] = criterion
	}
	candidateByName := make(map[string]RubricCriterionSummary, len(candidate))
	for _, criterion := range candidate {
		candidateByName[criterion.Name] = criterion
	}

	var names []string
	seen := map[string]bool{}
	for name := range baselineByName {
		seen[name] = true
		names = append(names, name)
	}
	for name := range candidateByName {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	deltas := make([]CriterionDelta, 0, len(names))
	for _, name := range names {
		b := baselineByName[name]
		c := candidateByName[name]
		deltas = append(deltas, CriterionDelta{
			Name:              name,
			BaselinePassRate:  b.PassRate,
			CandidatePassRate: c.PassRate,
			Delta:             c.PassRate - b.PassRate,
		})
	}
	return deltas
}

func compareVerdict(regressions, improvements []string) string {
	switch {
	case len(regressions) > 0:
		return "candidate_regressed"
	case len(improvements) > 0:
		return "candidate_improved"
	default:
		return "no_material_change"
	}
}

func dedupeAndSort(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse json %q: %w", path, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func groupTrialsByTask(trials []TrialReport) map[string][]TrialReport {
	grouped := make(map[string][]TrialReport, len(trials))
	for _, trial := range trials {
		grouped[trial.TaskID] = append(grouped[trial.TaskID], trial)
	}
	return grouped
}

func aggregateTaskTrials(taskID string, trials []TrialReport) TaskSummary {
	if len(trials) == 0 {
		return TaskSummary{TaskID: taskID}
	}
	aggregate := aggregateTrials(trials)
	taskName := trials[0].TaskName
	if taskName == "" {
		taskName = taskID
	}
	return TaskSummary{
		TaskID:                   taskID,
		TaskName:                 taskName,
		TrialCount:               aggregate.TrialCount,
		SuccessRate:              aggregate.SuccessRate,
		AverageReward:            aggregate.AverageReward,
		AverageLatencySeconds:    aggregate.AverageLatencySeconds,
		AverageCostUSDEst:        aggregate.AverageCostUSDEst,
		AverageRetryCount:        aggregate.AverageRetryCount,
		AverageToolFailureCount:  aggregate.AverageToolFailureCount,
		AveragePolicyViolations:  aggregate.AveragePolicyViolations,
		AverageEvidenceScore:     aggregate.AverageEvidenceScore,
		BudgetExceededTrialCount: aggregate.BudgetExceededTrialCount,
	}
}

func seenTask(trials map[string][]TrialReport, taskID string) bool {
	_, ok := trials[taskID]
	return ok
}

func RoundMetric(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*10000) / 10000
}
