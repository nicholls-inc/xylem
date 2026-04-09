package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

const (
	defaultLookbackRuns = 50
	defaultMinSamples   = 3
	defaultOutputDir    = "reviews"
	reportJSONName      = "harness-review.json"
	reportMarkdownName  = "harness-review.md"
	runPhaseName        = "[run]"
)

type Options struct {
	LookbackRuns int
	MinSamples   int
	OutputDir    string
	Now          time.Time
}

type Result struct {
	Report       *Report
	JSONPath     string
	MarkdownPath string
	Markdown     string
}

type Report struct {
	GeneratedAt       time.Time     `json:"generated_at"`
	LookbackRuns      int           `json:"lookback_runs"`
	MinSamples        int           `json:"min_samples"`
	TotalRunsObserved int           `json:"total_runs_observed"`
	ReviewedRuns      int           `json:"reviewed_runs"`
	Summary           Summary       `json:"summary"`
	Groups            []GroupReview `json:"groups"`
	CostAnomalies     []CostAnomaly `json:"cost_anomalies,omitempty"`
	Warnings          []string      `json:"warnings,omitempty"`
}

type Summary struct {
	KeepCount             int `json:"keep_count"`
	InvestigateCount      int `json:"investigate_count"`
	PruneCandidateCount   int `json:"prune_candidate_count"`
	InsufficientDataCount int `json:"insufficient_data_count"`
}

type GroupReview struct {
	Source            string         `json:"source"`
	Workflow          string         `json:"workflow"`
	Phase             string         `json:"phase"`
	PhaseType         string         `json:"phase_type"`
	Samples           int            `json:"samples"`
	FailureCount      int            `json:"failure_count"`
	BudgetAlertRuns   int            `json:"budget_alert_runs"`
	EvalIssueRuns     int            `json:"eval_issue_runs"`
	EvidenceFailures  int            `json:"evidence_failures"`
	CostAnomalyRuns   int            `json:"cost_anomaly_runs"`
	AverageCostUSDEst float64        `json:"average_cost_usd_est"`
	Recommendation    Recommendation `json:"recommendation"`
	Reasons           []string       `json:"reasons,omitempty"`
}

type CostAnomaly struct {
	VesselID   string         `json:"vessel_id"`
	Source     string         `json:"source"`
	Workflow   string         `json:"workflow"`
	DetectedAt time.Time      `json:"detected_at"`
	Metrics    []cost.Anomaly `json:"metrics"`
}

type groupKey struct {
	Source   string
	Workflow string
	Phase    string
}

type groupAccumulator struct {
	GroupReview
	totalCost float64
}

func Generate(stateDir string, opts Options) (*Result, error) {
	if opts.LookbackRuns <= 0 {
		opts.LookbackRuns = defaultLookbackRuns
	}
	if opts.MinSamples <= 0 {
		opts.MinSamples = defaultMinSamples
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		opts.OutputDir = defaultOutputDir
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}

	runs, totalRuns, warnings, err := LoadRuns(stateDir, opts.LookbackRuns)
	if err != nil {
		return nil, fmt.Errorf("generate review: %w", err)
	}

	report := &Report{
		GeneratedAt:       opts.Now,
		LookbackRuns:      opts.LookbackRuns,
		MinSamples:        opts.MinSamples,
		TotalRunsObserved: totalRuns,
		ReviewedRuns:      len(runs),
		Warnings:          append([]string(nil), warnings...),
	}
	report.Groups, report.CostAnomalies = buildGroupReviews(runs, opts.MinSamples)
	report.Summary = summarizeRecommendations(report.Groups)

	outputDir := filepath.Join(stateDir, opts.OutputDir)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("generate review: create output dir: %w", err)
	}

	jsonPath := filepath.Join(outputDir, reportJSONName)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("generate review: marshal report: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("generate review: write json report: %w", err)
	}

	markdown := renderMarkdown(report)
	markdownPath := filepath.Join(outputDir, reportMarkdownName)
	if err := os.WriteFile(markdownPath, []byte(markdown), 0o644); err != nil {
		return nil, fmt.Errorf("generate review: write markdown report: %w", err)
	}

	return &Result{
		Report:       report,
		JSONPath:     jsonPath,
		MarkdownPath: markdownPath,
		Markdown:     markdown,
	}, nil
}

func LoadLatestReport(stateDir, outputDir string) (*Report, error) {
	if strings.TrimSpace(outputDir) == "" {
		outputDir = defaultOutputDir
	}
	path := filepath.Join(stateDir, outputDir, reportJSONName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load latest report: read: %w", err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("load latest report: unmarshal: %w", err)
	}
	return &report, nil
}

func buildGroupReviews(runs []LoadedRun, minSamples int) ([]GroupReview, []CostAnomaly) {
	groups := make(map[groupKey]*groupAccumulator)
	costAnomalies := detectCostAnomalies(runs)
	anomalousRuns := make(map[string]CostAnomaly, len(costAnomalies))
	for _, anomaly := range costAnomalies {
		anomalousRuns[anomaly.VesselID] = anomaly
	}

	for _, run := range runs {
		runGroup := ensureGroup(groups, run.Summary.Source, run.Summary.Workflow, runPhaseName, "run")
		runGroup.Samples++
		runGroup.totalCost += run.Summary.TotalCostUSDEst
		if run.Summary.State != "completed" {
			runGroup.FailureCount++
		}
		if len(run.BudgetAlerts) > 0 {
			runGroup.BudgetAlertRuns++
		}
		if evalHasIssues(run.EvalReport) {
			runGroup.EvalIssueRuns++
		}
		if _, ok := anomalousRuns[run.Summary.VesselID]; ok {
			runGroup.CostAnomalyRuns++
		}

		for _, claim := range manifestClaims(run.Evidence) {
			if claim.Passed {
				continue
			}
			phaseName := claim.Phase
			phaseType := "run"
			if phaseName == "" {
				phaseName = runPhaseName
			} else {
				phaseType = phaseTypeFor(run.Summary, phaseName)
			}
			ensureGroup(groups, run.Summary.Source, run.Summary.Workflow, phaseName, phaseType).EvidenceFailures++
		}

		for _, phase := range run.Summary.Phases {
			group := ensureGroup(groups, run.Summary.Source, run.Summary.Workflow, phase.Name, phase.Type)
			group.Samples++
			group.totalCost += phase.CostUSDEst
			if phase.Status != "completed" && phase.Status != "no-op" {
				group.FailureCount++
			}
			if phase.GatePassed != nil && !*phase.GatePassed {
				group.FailureCount++
			}
		}
	}

	reviews := make([]GroupReview, 0, len(groups))
	for _, group := range groups {
		if group.Samples > 0 {
			group.AverageCostUSDEst = group.totalCost / float64(group.Samples)
		}
		group.Recommendation, group.Reasons = recommendGroup(aggregateStats{
			Samples:          group.Samples,
			FailureCount:     group.FailureCount,
			BudgetAlertRuns:  group.BudgetAlertRuns,
			EvalIssueRuns:    group.EvalIssueRuns,
			EvidenceFailures: group.EvidenceFailures,
			CostAnomalyRuns:  group.CostAnomalyRuns,
		}, minSamples)
		reviews = append(reviews, group.GroupReview)
	}

	sort.Slice(reviews, func(i, j int) bool {
		if reviews[i].Source != reviews[j].Source {
			return reviews[i].Source < reviews[j].Source
		}
		if reviews[i].Workflow != reviews[j].Workflow {
			return reviews[i].Workflow < reviews[j].Workflow
		}
		return reviews[i].Phase < reviews[j].Phase
	})
	sort.Slice(costAnomalies, func(i, j int) bool {
		if costAnomalies[i].Source != costAnomalies[j].Source {
			return costAnomalies[i].Source < costAnomalies[j].Source
		}
		if costAnomalies[i].Workflow != costAnomalies[j].Workflow {
			return costAnomalies[i].Workflow < costAnomalies[j].Workflow
		}
		return costAnomalies[i].DetectedAt.Before(costAnomalies[j].DetectedAt)
	})
	return reviews, costAnomalies
}

func detectCostAnomalies(runs []LoadedRun) []CostAnomaly {
	historyByWorkflow := make(map[string][]*cost.CostReport)
	anomalies := make([]CostAnomaly, 0)
	for _, run := range runs {
		if run.CostReport == nil {
			continue
		}
		key := run.Summary.Source + "\x00" + run.Summary.Workflow
		history := historyByWorkflow[key]
		if detected := cost.DetectAnomalies(run.CostReport, history); len(detected) > 0 {
			anomalies = append(anomalies, CostAnomaly{
				VesselID:   run.Summary.VesselID,
				Source:     run.Summary.Source,
				Workflow:   run.Summary.Workflow,
				DetectedAt: run.Summary.EndedAt,
				Metrics:    detected,
			})
		}
		historyByWorkflow[key] = append(historyByWorkflow[key], run.CostReport)
	}
	return anomalies
}

func ensureGroup(groups map[groupKey]*groupAccumulator, source, workflow, phase, phaseType string) *groupAccumulator {
	key := groupKey{Source: source, Workflow: workflow, Phase: phase}
	group, ok := groups[key]
	if ok {
		return group
	}
	group = &groupAccumulator{
		GroupReview: GroupReview{
			Source:    source,
			Workflow:  workflow,
			Phase:     phase,
			PhaseType: phaseType,
		},
	}
	groups[key] = group
	return group
}

func phaseTypeFor(summary runner.VesselSummary, phaseName string) string {
	for _, phase := range summary.Phases {
		if phase.Name == phaseName {
			return phase.Type
		}
	}
	return "prompt"
}

func manifestClaims(manifest *evidence.Manifest) []evidence.Claim {
	if manifest == nil {
		return nil
	}
	return manifest.Claims
}

func evalHasIssues(result *evaluator.LoopResult) bool {
	if result == nil || result.FinalResult == nil {
		return false
	}
	return !result.FinalResult.Pass ||
		len(result.FinalResult.Feedback) > 0 ||
		len(result.FinalResult.Score.Issues) > 0
}

func summarizeRecommendations(groups []GroupReview) Summary {
	var summary Summary
	for _, group := range groups {
		switch group.Recommendation {
		case RecommendationKeep:
			summary.KeepCount++
		case RecommendationInvestigate:
			summary.InvestigateCount++
		case RecommendationPruneCandidate:
			summary.PruneCandidateCount++
		case RecommendationInsufficientData:
			summary.InsufficientDataCount++
		}
	}
	return summary
}

func renderMarkdown(report *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Harness review\n\n")
	fmt.Fprintf(&b, "- Generated: %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Reviewed runs: %d of %d available\n", report.ReviewedRuns, report.TotalRunsObserved)
	fmt.Fprintf(&b, "- Recommendations: investigate=%d, prune-candidate=%d, keep=%d, insufficient-data=%d\n\n",
		report.Summary.InvestigateCount,
		report.Summary.PruneCandidateCount,
		report.Summary.KeepCount,
		report.Summary.InsufficientDataCount,
	)

	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "## Warnings\n\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Group recommendations\n\n")
	if len(report.Groups) == 0 {
		fmt.Fprintf(&b, "No historical run summaries were available.\n")
		return b.String()
	}
	for _, group := range report.Groups {
		fmt.Fprintf(&b, "- `%s / %s / %s` → **%s**", group.Source, group.Workflow, group.Phase, group.Recommendation)
		if len(group.Reasons) > 0 {
			fmt.Fprintf(&b, " — %s", strings.Join(group.Reasons, "; "))
		}
		fmt.Fprintf(&b, "\n")
	}

	if len(report.CostAnomalies) > 0 {
		fmt.Fprintf(&b, "\n## Cost anomalies\n\n")
		for _, anomaly := range report.CostAnomalies {
			metrics := make([]string, 0, len(anomaly.Metrics))
			for _, metric := range anomaly.Metrics {
				metrics = append(metrics, fmt.Sprintf("%s %.2fx expected", metric.Metric, metric.Ratio))
			}
			fmt.Fprintf(&b, "- `%s / %s / %s` — %s\n",
				anomaly.Source, anomaly.Workflow, anomaly.VesselID, strings.Join(metrics, ", "))
		}
	}

	return b.String()
}
