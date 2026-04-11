package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

const (
	WorkflowHealthReportWorkflow            = "workflow-health-report"
	workflowHealthReportJSONName            = "workflow-health-report.json"
	workflowHealthReportMarkdownName        = "workflow-health-report.md"
	workflowHealthIssueStateName            = "workflow-health-report-issues.json"
	workflowHealthReportTitle               = "[xylem] weekly workflow health"
	workflowHealthEscalationTitle           = "[xylem] workflow health escalation"
	workflowHealthReportMarkerPrefix        = "<!-- xylem:workflow-health-report-week="
	workflowHealthEscalationMarkerPrefix    = "<!-- xylem:workflow-health-escalation-week="
	defaultWorkflowHealthWindow             = 7 * 24 * time.Hour
	defaultWorkflowHealthEscalationMinimum  = 2
	maxWorkflowHealthEvidencePerFinding     = 5
	maxWorkflowHealthRenderedCounts         = 5
	maxWorkflowHealthRenderedWorkflowIssues = 5
)

type WorkflowHealthOptions struct {
	LookbackRuns        int
	Window              time.Duration
	OutputDir           string
	Now                 time.Time
	EscalationThreshold int
}

type WorkflowHealthResult struct {
	Report       *WorkflowHealthReport
	JSONPath     string
	MarkdownPath string
	Markdown     string
	Published    []WorkflowHealthPublication
}

type WorkflowHealthReport struct {
	GeneratedAt         time.Time                `json:"generated_at"`
	WindowStart         time.Time                `json:"window_start"`
	WindowEnd           time.Time                `json:"window_end"`
	LookbackRuns        int                      `json:"lookback_runs"`
	EscalationThreshold int                      `json:"escalation_threshold"`
	TotalRunsObserved   int                      `json:"total_runs_observed"`
	ReviewedRuns        int                      `json:"reviewed_runs"`
	CurrentVessels      int                      `json:"current_vessels"`
	Fleet               runner.FleetStatusReport `json:"fleet"`
	AnomalyCounts       []WorkflowHealthCount    `json:"anomaly_counts,omitempty"`
	RetryOutcomes       []WorkflowHealthCount    `json:"retry_outcomes,omitempty"`
	Workflows           []WorkflowHealthWorkflow `json:"workflows,omitempty"`
	EscalationFindings  []WorkflowHealthFinding  `json:"escalation_findings,omitempty"`
	Warnings            []string                 `json:"warnings,omitempty"`
}

type WorkflowHealthCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type WorkflowHealthWorkflow struct {
	Workflow                  string                `json:"workflow"`
	Source                    string                `json:"source,omitempty"`
	Runs                      int                   `json:"runs"`
	Completed                 int                   `json:"completed"`
	Failed                    int                   `json:"failed"`
	TimedOut                  int                   `json:"timed_out"`
	Cancelled                 int                   `json:"cancelled"`
	HealthyRuns               int                   `json:"healthy_runs"`
	DegradedRuns              int                   `json:"degraded_runs"`
	UnhealthyRuns             int                   `json:"unhealthy_runs"`
	AverageDurationMS         int64                 `json:"average_duration_ms"`
	AveragePhaseCount         float64               `json:"average_phase_count"`
	AverageCostUSDEst         float64               `json:"average_cost_usd_est"`
	PreviousAverageCostUSDEst float64               `json:"previous_average_cost_usd_est,omitempty"`
	CostDeltaUSDEst           float64               `json:"cost_delta_usd_est,omitempty"`
	TopAnomalies              []WorkflowHealthCount `json:"top_anomalies,omitempty"`
}

type WorkflowHealthFinding struct {
	Fingerprint     string   `json:"fingerprint"`
	Workflow        string   `json:"workflow"`
	Source          string   `json:"source,omitempty"`
	Pattern         string   `json:"pattern"`
	Count           int      `json:"count"`
	VesselIDs       []string `json:"vessel_ids,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
	Recommendations []string `json:"recommendations,omitempty"`
}

type WorkflowHealthPublication struct {
	Kind        string `json:"kind"`
	Week        string `json:"week"`
	IssueNumber int    `json:"issue_number"`
	Title       string `json:"title"`
	Created     bool   `json:"created"`
}

type workflowHealthIssueState struct {
	Weeks map[string]workflowHealthWeekIssueState `json:"weeks,omitempty"`
}

type workflowHealthWeekIssueState struct {
	ReportIssueNumber     int       `json:"report_issue_number,omitempty"`
	ReportTitle           string    `json:"report_title,omitempty"`
	EscalationIssueNumber int       `json:"escalation_issue_number,omitempty"`
	EscalationTitle       string    `json:"escalation_title,omitempty"`
	FirstReportedAt       time.Time `json:"first_reported_at,omitempty"`
	LastObservedAt        time.Time `json:"last_observed_at,omitempty"`
}

type workflowHealthIssueSet struct {
	Report     contextWeightIssue
	Escalation contextWeightIssue
}

type workflowHealthWorkflowAccumulator struct {
	source          string
	workflow        string
	runs            int
	completed       int
	failed          int
	timedOut        int
	cancelled       int
	healthy         int
	degraded        int
	unhealthy       int
	totalDurationMS int64
	totalPhaseCount int
	totalCostUSDEst float64
	anomalyCounts   map[string]int
}

type workflowHealthTrendAccumulator struct {
	runs            int
	totalCostUSDEst float64
}

type workflowHealthFindingAccumulator struct {
	source       string
	workflow     string
	pattern      string
	vesselIDs    []string
	evidence     []string
	recommenders map[string]struct{}
}

func GenerateWorkflowHealthReport(stateDir string, opts WorkflowHealthOptions) (*WorkflowHealthResult, error) {
	if opts.LookbackRuns <= 0 {
		opts.LookbackRuns = defaultLookbackRuns
	}
	if opts.Window <= 0 {
		opts.Window = defaultWorkflowHealthWindow
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		opts.OutputDir = defaultOutputDir
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}
	if opts.EscalationThreshold <= 0 {
		opts.EscalationThreshold = defaultWorkflowHealthEscalationMinimum
	}

	windowStart := opts.Now.Add(-opts.Window)
	allRuns, totalRuns, warnings, err := LoadRuns(stateDir, opts.LookbackRuns)
	if err != nil {
		return nil, fmt.Errorf("generate workflow-health report: %w", err)
	}

	currentVessels, fleet, err := loadWorkflowHealthFleet(stateDir)
	if err != nil {
		return nil, fmt.Errorf("generate workflow-health report: %w", err)
	}

	recentRuns, previousRuns := splitWorkflowHealthRuns(allRuns, windowStart, opts.Now, opts.Window)
	anomalyCounts, workflows, escalationFindings := buildWorkflowHealthInsights(recentRuns, previousRuns, opts.EscalationThreshold)
	retryOutcomes := workflowHealthRetryOutcomes(recentRuns)

	report := &WorkflowHealthReport{
		GeneratedAt:         opts.Now,
		WindowStart:         windowStart,
		WindowEnd:           opts.Now,
		LookbackRuns:        opts.LookbackRuns,
		EscalationThreshold: opts.EscalationThreshold,
		TotalRunsObserved:   totalRuns,
		ReviewedRuns:        len(recentRuns),
		CurrentVessels:      currentVessels,
		Fleet:               fleet,
		AnomalyCounts:       anomalyCounts,
		RetryOutcomes:       retryOutcomes,
		Workflows:           workflows,
		EscalationFindings:  escalationFindings,
		Warnings:            append([]string(nil), warnings...),
	}

	outputDir := config.RuntimePath(stateDir, opts.OutputDir)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("generate workflow-health report: create output dir: %w", err)
	}

	jsonPath := filepath.Join(outputDir, workflowHealthReportJSONName)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("generate workflow-health report: marshal report: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("generate workflow-health report: write json report: %w", err)
	}

	markdown := renderWorkflowHealthMarkdown(report)
	markdownPath := filepath.Join(outputDir, workflowHealthReportMarkdownName)
	if err := os.WriteFile(markdownPath, []byte(markdown), 0o644); err != nil {
		return nil, fmt.Errorf("generate workflow-health report: write markdown report: %w", err)
	}

	return &WorkflowHealthResult{
		Report:       report,
		JSONPath:     jsonPath,
		MarkdownPath: markdownPath,
		Markdown:     markdown,
	}, nil
}

func RunWorkflowHealthReport(ctx context.Context, stateDir, repo string, cmdRunner issueRunner, opts WorkflowHealthOptions) (*WorkflowHealthResult, error) {
	result, err := GenerateWorkflowHealthReport(stateDir, opts)
	if err != nil {
		return nil, err
	}
	published, err := PublishWorkflowHealthIssues(ctx, stateDir, repo, cmdRunner, result.Report, opts.OutputDir, opts.Now)
	if err != nil {
		return nil, err
	}
	result.Published = published
	return result, nil
}

func PublishWorkflowHealthIssues(ctx context.Context, stateDir, repo string, cmdRunner issueRunner, report *WorkflowHealthReport, outputDir string, now time.Time) ([]WorkflowHealthPublication, error) {
	if report == nil {
		return nil, nil
	}
	if strings.TrimSpace(outputDir) == "" {
		outputDir = defaultOutputDir
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	weekKey := workflowHealthWeekKey(report.WindowEnd)
	statePath := config.RuntimePath(stateDir, outputDir, workflowHealthIssueStateName)
	state, err := loadWorkflowHealthIssueState(statePath)
	if err != nil {
		return nil, err
	}

	openIssues := map[string]workflowHealthIssueSet{}
	if cmdRunner != nil && strings.TrimSpace(repo) != "" {
		openIssues, err = loadOpenWorkflowHealthIssues(ctx, cmdRunner, repo)
		if err != nil {
			return nil, err
		}
	}

	weekState := state.Weeks[weekKey]
	if weekState.FirstReportedAt.IsZero() {
		weekState.FirstReportedAt = now
	}
	weekState.LastObservedAt = now

	publications := make([]WorkflowHealthPublication, 0, 2)

	reportIssue, ok := openIssues[weekKey]
	switch {
	case weekState.ReportIssueNumber > 0:
		publications = append(publications, WorkflowHealthPublication{
			Kind:        "report",
			Week:        weekKey,
			IssueNumber: weekState.ReportIssueNumber,
			Title:       workflowHealthReportTitle,
			Created:     false,
		})
	case ok && reportIssue.Report.Number > 0:
		weekState.ReportIssueNumber = reportIssue.Report.Number
		weekState.ReportTitle = reportIssue.Report.Title
		publications = append(publications, WorkflowHealthPublication{
			Kind:        "report",
			Week:        weekKey,
			IssueNumber: reportIssue.Report.Number,
			Title:       reportIssue.Report.Title,
			Created:     false,
		})
	case cmdRunner != nil && strings.TrimSpace(repo) != "":
		body := renderWorkflowHealthIssueBody(report, weekKey)
		out, err := cmdRunner.RunOutput(ctx, "gh", "issue", "create", "--repo", repo, "--title", workflowHealthReportTitle, "--body", body)
		if err != nil {
			return nil, fmt.Errorf("publish workflow-health report issue: %w", err)
		}
		issueNumber, err := parseIssueNumberFromCreateOutput(string(out))
		if err != nil {
			return nil, fmt.Errorf("publish workflow-health report issue: %w", err)
		}
		weekState.ReportIssueNumber = issueNumber
		weekState.ReportTitle = workflowHealthReportTitle
		publications = append(publications, WorkflowHealthPublication{
			Kind:        "report",
			Week:        weekKey,
			IssueNumber: issueNumber,
			Title:       workflowHealthReportTitle,
			Created:     true,
		})
	}

	if len(report.EscalationFindings) > 0 {
		switch {
		case weekState.EscalationIssueNumber > 0:
			publications = append(publications, WorkflowHealthPublication{
				Kind:        "escalation",
				Week:        weekKey,
				IssueNumber: weekState.EscalationIssueNumber,
				Title:       workflowHealthEscalationTitle,
				Created:     false,
			})
		case ok && reportIssue.Escalation.Number > 0:
			weekState.EscalationIssueNumber = reportIssue.Escalation.Number
			weekState.EscalationTitle = reportIssue.Escalation.Title
			publications = append(publications, WorkflowHealthPublication{
				Kind:        "escalation",
				Week:        weekKey,
				IssueNumber: reportIssue.Escalation.Number,
				Title:       reportIssue.Escalation.Title,
				Created:     false,
			})
		case cmdRunner != nil && strings.TrimSpace(repo) != "":
			body := renderWorkflowHealthEscalationIssueBody(report, weekKey)
			out, err := cmdRunner.RunOutput(ctx, "gh", "issue", "create", "--repo", repo, "--title", workflowHealthEscalationTitle, "--body", body)
			if err != nil {
				return nil, fmt.Errorf("publish workflow-health escalation issue: %w", err)
			}
			issueNumber, err := parseIssueNumberFromCreateOutput(string(out))
			if err != nil {
				return nil, fmt.Errorf("publish workflow-health escalation issue: %w", err)
			}
			weekState.EscalationIssueNumber = issueNumber
			weekState.EscalationTitle = workflowHealthEscalationTitle
			publications = append(publications, WorkflowHealthPublication{
				Kind:        "escalation",
				Week:        weekKey,
				IssueNumber: issueNumber,
				Title:       workflowHealthEscalationTitle,
				Created:     true,
			})
		}
	}

	state.Weeks[weekKey] = weekState
	if err := saveWorkflowHealthIssueState(statePath, state); err != nil {
		return nil, err
	}

	return publications, nil
}

func loadWorkflowHealthFleet(stateDir string) (int, runner.FleetStatusReport, error) {
	q := queue.New(config.RuntimePath(stateDir, "queue.jsonl"))
	vessels, err := q.List()
	if err != nil {
		return 0, runner.FleetStatusReport{}, fmt.Errorf("load queue: %w", err)
	}
	ids := make([]string, 0, len(vessels))
	for _, vessel := range vessels {
		ids = append(ids, vessel.ID)
	}
	summaries, err := runner.LoadVesselSummaries(stateDir, ids)
	if err != nil {
		return 0, runner.FleetStatusReport{}, fmt.Errorf("load queue summaries: %w", err)
	}
	return len(vessels), runner.AnalyzeFleetStatus(vessels, summaries), nil
}

func splitWorkflowHealthRuns(runs []LoadedRun, windowStart, now time.Time, window time.Duration) ([]LoadedRun, []LoadedRun) {
	recent := make([]LoadedRun, 0, len(runs))
	previous := make([]LoadedRun, 0, len(runs))
	previousStart := windowStart.Add(-window)
	for _, run := range runs {
		endedAt := run.Summary.EndedAt.UTC()
		switch {
		case !endedAt.Before(windowStart) && !endedAt.After(now):
			recent = append(recent, run)
		case !endedAt.Before(previousStart) && endedAt.Before(windowStart):
			previous = append(previous, run)
		}
	}
	return recent, previous
}

func buildWorkflowHealthInsights(recentRuns, previousRuns []LoadedRun, escalationThreshold int) ([]WorkflowHealthCount, []WorkflowHealthWorkflow, []WorkflowHealthFinding) {
	anomalyCounts := map[string]int{}
	workflowStats := map[string]*workflowHealthWorkflowAccumulator{}
	previousCost := map[string]*workflowHealthTrendAccumulator{}
	findingStats := map[string]*workflowHealthFindingAccumulator{}

	for _, run := range previousRuns {
		key := workflowHealthWorkflowKey(run.Summary.Source, run.Summary.Workflow)
		if previousCost[key] == nil {
			previousCost[key] = &workflowHealthTrendAccumulator{}
		}
		previousCost[key].runs++
		previousCost[key].totalCostUSDEst += run.Summary.TotalCostUSDEst
	}

	for _, run := range recentRuns {
		status := workflowHealthRunStatus(run)
		key := workflowHealthWorkflowKey(run.Summary.Source, run.Summary.Workflow)
		group := workflowStats[key]
		if group == nil {
			group = &workflowHealthWorkflowAccumulator{
				source:        run.Summary.Source,
				workflow:      run.Summary.Workflow,
				anomalyCounts: make(map[string]int),
			}
			workflowStats[key] = group
		}
		group.runs++
		group.totalDurationMS += run.Summary.DurationMS
		group.totalPhaseCount += len(run.Summary.Phases)
		group.totalCostUSDEst += run.Summary.TotalCostUSDEst
		switch run.Summary.State {
		case string(queue.StateCompleted):
			group.completed++
		case string(queue.StateFailed):
			group.failed++
		case string(queue.StateTimedOut):
			group.timedOut++
		case string(queue.StateCancelled):
			group.cancelled++
		}
		switch status.Health {
		case runner.VesselHealthHealthy:
			group.healthy++
		case runner.VesselHealthDegraded:
			group.degraded++
		case runner.VesselHealthUnhealthy:
			group.unhealthy++
		}

		codes := runner.AnomalyCodes(status.Anomalies)
		for _, code := range codes {
			anomalyCounts[code]++
			group.anomalyCounts[code]++
		}

		finding := workflowHealthFindingForRun(run, status, escalationThreshold)
		if finding == nil {
			continue
		}
		cluster := findingStats[finding.Fingerprint]
		if cluster == nil {
			cluster = &workflowHealthFindingAccumulator{
				source:       finding.Source,
				workflow:     finding.Workflow,
				pattern:      finding.Pattern,
				recommenders: make(map[string]struct{}),
			}
			findingStats[finding.Fingerprint] = cluster
		}
		cluster.vesselIDs = append(cluster.vesselIDs, finding.VesselIDs...)
		cluster.evidence = append(cluster.evidence, finding.Evidence...)
		for _, recommendation := range finding.Recommendations {
			cluster.recommenders[recommendation] = struct{}{}
		}
	}

	workflows := make([]WorkflowHealthWorkflow, 0, len(workflowStats))
	for key, group := range workflowStats {
		prevAvg := 0.0
		if prev := previousCost[key]; prev != nil && prev.runs > 0 {
			prevAvg = prev.totalCostUSDEst / float64(prev.runs)
		}
		entry := WorkflowHealthWorkflow{
			Workflow:                  group.workflow,
			Source:                    group.source,
			Runs:                      group.runs,
			Completed:                 group.completed,
			Failed:                    group.failed,
			TimedOut:                  group.timedOut,
			Cancelled:                 group.cancelled,
			HealthyRuns:               group.healthy,
			DegradedRuns:              group.degraded,
			UnhealthyRuns:             group.unhealthy,
			AverageDurationMS:         avgInt64(group.totalDurationMS, group.runs),
			AveragePhaseCount:         avgFloat64(float64(group.totalPhaseCount), group.runs),
			AverageCostUSDEst:         avgFloat64(group.totalCostUSDEst, group.runs),
			PreviousAverageCostUSDEst: prevAvg,
			CostDeltaUSDEst:           avgFloat64(group.totalCostUSDEst, group.runs) - prevAvg,
			TopAnomalies:              topWorkflowHealthCounts(group.anomalyCounts, maxWorkflowHealthRenderedCounts),
		}
		workflows = append(workflows, entry)
	}
	sort.Slice(workflows, func(i, j int) bool {
		if workflows[i].UnhealthyRuns != workflows[j].UnhealthyRuns {
			return workflows[i].UnhealthyRuns > workflows[j].UnhealthyRuns
		}
		if workflows[i].Failed != workflows[j].Failed {
			return workflows[i].Failed > workflows[j].Failed
		}
		if workflows[i].CostDeltaUSDEst != workflows[j].CostDeltaUSDEst {
			return workflows[i].CostDeltaUSDEst > workflows[j].CostDeltaUSDEst
		}
		return workflows[i].Workflow < workflows[j].Workflow
	})

	findings := make([]WorkflowHealthFinding, 0, len(findingStats))
	for fingerprint, cluster := range findingStats {
		if len(cluster.vesselIDs) < escalationThreshold {
			continue
		}
		findings = append(findings, WorkflowHealthFinding{
			Fingerprint:     fingerprint,
			Workflow:        cluster.workflow,
			Source:          cluster.source,
			Pattern:         cluster.pattern,
			Count:           len(cluster.vesselIDs),
			VesselIDs:       append([]string(nil), cluster.vesselIDs...),
			Evidence:        limitStrings(cluster.evidence, maxWorkflowHealthEvidencePerFinding),
			Recommendations: sortedKeys(cluster.recommenders),
		})
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Count != findings[j].Count {
			return findings[i].Count > findings[j].Count
		}
		if findings[i].Workflow != findings[j].Workflow {
			return findings[i].Workflow < findings[j].Workflow
		}
		return findings[i].Pattern < findings[j].Pattern
	})

	return topWorkflowHealthCounts(anomalyCounts, 0), workflows, findings
}

func workflowHealthRunStatus(run LoadedRun) runner.VesselStatusReport {
	vesselState := queue.VesselState(run.Summary.State)
	vessel := queue.Vessel{
		ID:          run.Summary.VesselID,
		Source:      run.Summary.Source,
		Workflow:    run.Summary.Workflow,
		Ref:         run.Summary.Ref,
		State:       vesselState,
		CreatedAt:   run.Summary.StartedAt,
		StartedAt:   &run.Summary.StartedAt,
		EndedAt:     &run.Summary.EndedAt,
		FailedPhase: "",
	}
	return runner.AnalyzeVesselStatus(vessel, &run.Summary)
}

func workflowHealthRetryOutcomes(runs []LoadedRun) []WorkflowHealthCount {
	counts := make(map[string]int)
	for _, run := range runs {
		outcome := strings.TrimSpace(summaryRetryOutcome(run.Summary))
		if outcome == "" && run.Recovery != nil {
			outcome = strings.TrimSpace(run.Recovery.RetryOutcome)
		}
		if outcome == "" && run.Summary.Recovery != nil && run.Summary.Recovery.RetrySuppressed {
			outcome = "suppressed"
		}
		if outcome == "" {
			continue
		}
		counts[outcome]++
	}
	return topWorkflowHealthCounts(counts, 0)
}

func workflowHealthFindingForRun(run LoadedRun, status runner.VesselStatusReport, escalationThreshold int) *WorkflowHealthFinding {
	if escalationThreshold <= 1 {
		escalationThreshold = defaultWorkflowHealthEscalationMinimum
	}
	if status.Health == runner.VesselHealthHealthy {
		return nil
	}
	codes := runner.AnomalyCodes(status.Anomalies)
	if len(codes) == 0 {
		return nil
	}
	sort.Strings(codes)
	failedPhase := failedPhaseForSummary(run.Summary)
	pattern := strings.Join(codes, ", ")
	if failedPhase != "" {
		pattern = fmt.Sprintf("%s (failed phase: %s)", pattern, failedPhase)
	}
	fingerprint := workflowHealthPatternFingerprint(run.Summary.Workflow, codes, failedPhase, retryOutcomeForRun(run))
	recommendations := workflowHealthRecommendations(codes)
	evidence := fmt.Sprintf("%s ended %s with anomalies: %s", run.Summary.VesselID, run.Summary.State, pattern)
	return &WorkflowHealthFinding{
		Fingerprint:     fingerprint,
		Workflow:        run.Summary.Workflow,
		Source:          run.Summary.Source,
		Pattern:         pattern,
		Count:           1,
		VesselIDs:       []string{run.Summary.VesselID},
		Evidence:        []string{evidence},
		Recommendations: recommendations,
	}
}

func workflowHealthPatternFingerprint(workflow string, anomalyCodes []string, failedPhase, retryOutcome string) string {
	sortedCodes := append([]string(nil), anomalyCodes...)
	sort.Strings(sortedCodes)
	h := sha256.Sum256([]byte(strings.Join([]string{
		workflow,
		strings.Join(sortedCodes, ","),
		strings.TrimSpace(failedPhase),
		strings.TrimSpace(retryOutcome),
	}, "\x00")))
	return fmt.Sprintf("%x", h[:8])
}

func workflowHealthRecommendations(anomalyCodes []string) []string {
	recs := make(map[string]struct{})
	for _, code := range anomalyCodes {
		switch code {
		case "run_failed", "phase_failed":
			recs["Inspect the repeated failing workflow and compare the affected phase outputs side by side."] = struct{}{}
		case "gate_failed":
			recs["Review the validation gate for flaky or overly strict checks before rerunning the workflow."] = struct{}{}
		case "timed_out":
			recs["Profile the slow phase and tighten prompt/tool scope before increasing timeouts."] = struct{}{}
		case "budget_exceeded", "budget_warning":
			recs["Audit token footprint and model tier selection for the affected workflow."] = struct{}{}
		case "waiting_on_gate":
			recs["Clear or automate the blocking gate condition so waiting vessels do not accumulate."] = struct{}{}
		case "cancelled":
			recs["Check for operator or daemon interruptions that are cancelling runs mid-flight."] = struct{}{}
		default:
			recs["Review the recent vessel summaries for the repeated anomaly pattern and file a targeted fix."] = struct{}{}
		}
	}
	return sortedKeys(recs)
}

func renderWorkflowHealthMarkdown(report *WorkflowHealthReport) string {
	if report == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Weekly workflow health\n\n")
	if report.ReviewedRuns == 0 {
		fmt.Fprintf(&b, "No completed or failed vessel runs landed in the reporting window (%s to %s).\n\n",
			report.WindowStart.Format(time.RFC3339), report.WindowEnd.Format(time.RFC3339))
	} else {
		fmt.Fprintf(&b, "Reviewed **%d** recent runs across **%d** currently tracked vessels. Fleet health is **%d healthy / %d degraded / %d unhealthy**.\n\n",
			report.ReviewedRuns, report.CurrentVessels, report.Fleet.Healthy, report.Fleet.Degraded, report.Fleet.Unhealthy)
	}

	fmt.Fprintf(&b, "## Key metrics\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n")
	fmt.Fprintf(&b, "|--------|-------|\n")
	fmt.Fprintf(&b, "| Window | %s to %s |\n", report.WindowStart.Format("2006-01-02"), report.WindowEnd.Format("2006-01-02"))
	fmt.Fprintf(&b, "| Runs reviewed | %d |\n", report.ReviewedRuns)
	fmt.Fprintf(&b, "| Current vessels | %d |\n", report.CurrentVessels)
	fmt.Fprintf(&b, "| Fleet health | %d healthy / %d degraded / %d unhealthy |\n\n",
		report.Fleet.Healthy, report.Fleet.Degraded, report.Fleet.Unhealthy)

	fmt.Fprintf(&b, "## Anomaly counts\n\n")
	if len(report.AnomalyCounts) == 0 {
		fmt.Fprintf(&b, "No recurring anomaly codes were detected in the reporting window.\n\n")
	} else {
		for _, count := range limitWorkflowHealthCounts(report.AnomalyCounts, maxWorkflowHealthRenderedCounts) {
			fmt.Fprintf(&b, "- `%s`: %d runs\n", count.Name, count.Count)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Highest-risk workflows\n\n")
	if len(report.Workflows) == 0 {
		fmt.Fprintf(&b, "No workflow history was available in the reporting window.\n\n")
	} else {
		fmt.Fprintf(&b, "| Workflow | Runs | Failed | Timed out | Unhealthy | Avg cost | Cost delta |\n")
		fmt.Fprintf(&b, "|----------|------|--------|-----------|-----------|----------|------------|\n")
		for _, workflow := range limitWorkflowHealthWorkflows(report.Workflows, maxWorkflowHealthRenderedWorkflowIssues) {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | $%.2f | $%.2f |\n",
				workflow.Workflow, workflow.Runs, workflow.Failed, workflow.TimedOut, workflow.UnhealthyRuns, workflow.AverageCostUSDEst, workflow.CostDeltaUSDEst)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Retry outcomes\n\n")
	if len(report.RetryOutcomes) == 0 {
		fmt.Fprintf(&b, "No retry review outcomes were captured in the reporting window.\n\n")
	} else {
		for _, outcome := range report.RetryOutcomes {
			fmt.Fprintf(&b, "- `%s`: %d\n", outcome.Name, outcome.Count)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Escalation candidates\n\n")
	if len(report.EscalationFindings) == 0 {
		fmt.Fprintf(&b, "No repeated workflow failure pattern crossed the escalation threshold.\n\n")
	} else {
		for _, finding := range report.EscalationFindings {
			fmt.Fprintf(&b, "- **%s** — %d runs matched `%s`\n", finding.Workflow, finding.Count, finding.Pattern)
		}
		fmt.Fprintf(&b, "\n")
	}

	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "## Incomplete local state\n\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}

	return b.String()
}

func renderWorkflowHealthIssueBody(report *WorkflowHealthReport, weekKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s -->\n\n", workflowHealthReportMarkerPrefix, weekKey)
	fmt.Fprintf(&b, "## Executive summary\n\n")
	if report.ReviewedRuns == 0 {
		fmt.Fprintf(&b, "No recent completed or failed vessel runs were recorded for this weekly reporting window.\n\n")
	} else {
		fmt.Fprintf(&b, "Reviewed %d runs from %s through %s. Current fleet health is %d healthy, %d degraded, and %d unhealthy vessels.\n\n",
			report.ReviewedRuns,
			report.WindowStart.Format("2006-01-02"),
			report.WindowEnd.Format("2006-01-02"),
			report.Fleet.Healthy,
			report.Fleet.Degraded,
			report.Fleet.Unhealthy,
		)
	}

	fmt.Fprintf(&b, "## Key metrics\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n")
	fmt.Fprintf(&b, "|--------|-------|\n")
	fmt.Fprintf(&b, "| Runs reviewed | %d |\n", report.ReviewedRuns)
	fmt.Fprintf(&b, "| Current vessels | %d |\n", report.CurrentVessels)
	fmt.Fprintf(&b, "| Escalation threshold | %d matching runs |\n\n", report.EscalationThreshold)

	fmt.Fprintf(&b, "## Anomaly counts\n\n")
	if len(report.AnomalyCounts) == 0 {
		fmt.Fprintf(&b, "- None in this reporting window.\n\n")
	} else {
		for _, count := range limitWorkflowHealthCounts(report.AnomalyCounts, maxWorkflowHealthRenderedCounts) {
			fmt.Fprintf(&b, "- `%s`: %d runs\n", count.Name, count.Count)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Highest-risk workflows\n\n")
	if len(report.Workflows) == 0 {
		fmt.Fprintf(&b, "No workflow history was available.\n\n")
	} else {
		fmt.Fprintf(&b, "| Workflow | Failed | Timed out | Unhealthy | Avg cost |\n")
		fmt.Fprintf(&b, "|----------|--------|-----------|-----------|----------|\n")
		for _, workflow := range limitWorkflowHealthWorkflows(report.Workflows, maxWorkflowHealthRenderedWorkflowIssues) {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | $%.2f |\n",
				workflow.Workflow, workflow.Failed, workflow.TimedOut, workflow.UnhealthyRuns, workflow.AverageCostUSDEst)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Retry outcomes\n\n")
	if len(report.RetryOutcomes) == 0 {
		fmt.Fprintf(&b, "- None recorded.\n\n")
	} else {
		for _, outcome := range report.RetryOutcomes {
			fmt.Fprintf(&b, "- `%s`: %d\n", outcome.Name, outcome.Count)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Recommended follow-ups\n\n")
	if len(report.EscalationFindings) == 0 {
		fmt.Fprintf(&b, "- No escalation issue was required this week.\n")
	} else {
		fmt.Fprintf(&b, "- Review the grouped escalation findings below and route the follow-up issue if it has not already been created.\n")
		for _, finding := range report.EscalationFindings {
			fmt.Fprintf(&b, "- %s: %d runs repeated `%s`\n", finding.Workflow, finding.Count, finding.Pattern)
		}
	}
	fmt.Fprintf(&b, "\n")

	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "## Incomplete local state\n\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}

	return b.String()
}

func renderWorkflowHealthEscalationIssueBody(report *WorkflowHealthReport, weekKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s -->\n\n", workflowHealthEscalationMarkerPrefix, weekKey)
	fmt.Fprintf(&b, "## Escalation summary\n\n")
	fmt.Fprintf(&b, "%d repeated workflow failure patterns crossed the weekly escalation threshold of %d matching runs.\n\n",
		len(report.EscalationFindings), report.EscalationThreshold)
	fmt.Fprintf(&b, "## Findings\n\n")
	for _, finding := range report.EscalationFindings {
		fmt.Fprintf(&b, "### %s\n\n", finding.Workflow)
		fmt.Fprintf(&b, "- Pattern: `%s`\n", finding.Pattern)
		fmt.Fprintf(&b, "- Matching runs: %d\n", finding.Count)
		if len(finding.Evidence) > 0 {
			fmt.Fprintf(&b, "- Evidence:\n")
			for _, evidence := range finding.Evidence {
				fmt.Fprintf(&b, "  - %s\n", evidence)
			}
		}
		if len(finding.Recommendations) > 0 {
			fmt.Fprintf(&b, "- Recommended actions:\n")
			for _, recommendation := range finding.Recommendations {
				fmt.Fprintf(&b, "  - %s\n", recommendation)
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

func loadOpenWorkflowHealthIssues(ctx context.Context, cmdRunner issueRunner, repo string) (map[string]workflowHealthIssueSet, error) {
	out, err := cmdRunner.RunOutput(ctx, "gh", "issue", "list", "--repo", repo, "--state", "open", "--limit", "100", "--json", "number,title,body")
	if err != nil {
		return nil, fmt.Errorf("load open workflow-health issues: %w", err)
	}
	var issues []contextWeightIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("load open workflow-health issues: parse gh issue list output: %w", err)
	}
	byWeek := make(map[string]workflowHealthIssueSet)
	for _, issue := range issues {
		if week := parseMarkedValue(issue.Body, workflowHealthReportMarkerPrefix); week != "" {
			set := byWeek[week]
			set.Report = issue
			byWeek[week] = set
			continue
		}
		if week := parseMarkedValue(issue.Body, workflowHealthEscalationMarkerPrefix); week != "" {
			set := byWeek[week]
			set.Escalation = issue
			byWeek[week] = set
		}
	}
	return byWeek, nil
}

func loadWorkflowHealthIssueState(path string) (*workflowHealthIssueState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &workflowHealthIssueState{Weeks: make(map[string]workflowHealthWeekIssueState)}, nil
		}
		return nil, fmt.Errorf("load workflow-health issue state: %w", err)
	}
	var state workflowHealthIssueState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("load workflow-health issue state: unmarshal: %w", err)
	}
	if state.Weeks == nil {
		state.Weeks = make(map[string]workflowHealthWeekIssueState)
	}
	return &state, nil
}

func saveWorkflowHealthIssueState(path string, state *workflowHealthIssueState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save workflow-health issue state: create dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("save workflow-health issue state: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save workflow-health issue state: write: %w", err)
	}
	return nil
}

func workflowHealthWeekKey(t time.Time) string {
	year, week := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

func workflowHealthWorkflowKey(source, workflow string) string {
	return source + "\x00" + workflow
}

func failedPhaseForSummary(summary runner.VesselSummary) string {
	for _, phase := range summary.Phases {
		if phase.Status == "failed" {
			return phase.Name
		}
	}
	return ""
}

func retryOutcomeForRun(run LoadedRun) string {
	outcome := strings.TrimSpace(summaryRetryOutcome(run.Summary))
	if outcome == "" && run.Recovery != nil {
		outcome = strings.TrimSpace(run.Recovery.RetryOutcome)
	}
	if outcome == "" && run.Summary.Recovery != nil && run.Summary.Recovery.RetrySuppressed {
		outcome = "suppressed"
	}
	return outcome
}

func topWorkflowHealthCounts(counts map[string]int, limit int) []WorkflowHealthCount {
	items := make([]WorkflowHealthCount, 0, len(counts))
	for name, count := range counts {
		if count <= 0 {
			continue
		}
		items = append(items, WorkflowHealthCount{Name: name, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Name < items[j].Name
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func limitWorkflowHealthCounts(counts []WorkflowHealthCount, limit int) []WorkflowHealthCount {
	if limit <= 0 || len(counts) <= limit {
		return counts
	}
	return counts[:limit]
}

func limitWorkflowHealthWorkflows(workflows []WorkflowHealthWorkflow, limit int) []WorkflowHealthWorkflow {
	if limit <= 0 || len(workflows) <= limit {
		return workflows
	}
	return workflows[:limit]
}

func avgInt64(total int64, count int) int64 {
	if count == 0 {
		return 0
	}
	return total / int64(count)
}

func avgFloat64(total float64, count int) float64 {
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return append([]string(nil), values...)
	}
	return append([]string(nil), values[:limit]...)
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func parseMarkedValue(body, prefix string) string {
	start := strings.Index(body, prefix)
	if start == -1 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(body[start:], " -->")
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(body[start : start+end])
}

func summaryRetryOutcome(summary runner.VesselSummary) string {
	if summary.Recovery == nil {
		return ""
	}
	return summary.Recovery.RetryOutcome
}
