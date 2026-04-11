package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/nicholls-inc/xylem/cli/internal/signal"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

const (
	summaryFileName      = "summary.json"
	costReportFileName   = "cost-report.json"
	budgetAlertsFileName = "budget-alerts.json"
	evalReportFileName   = "quality-report.json"
	summaryDisclaimer    = "Token counts and costs are estimates (len/4 heuristic + static pricing). Not provider-reported values."
)

var safeSummaryPathComponent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// VesselSummary is the JSON artifact written after vessel completion or failure.
type VesselSummary struct {
	VesselID   string    `json:"vessel_id"`
	Source     string    `json:"source"`
	Workflow   string    `json:"workflow"`
	Ref        string    `json:"ref,omitempty"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	DurationMS int64     `json:"duration_ms"`

	Phases []PhaseSummary `json:"phases"`

	TotalInputTokensEst    int              `json:"total_input_tokens_est"`
	TotalOutputTokensEst   int              `json:"total_output_tokens_est"`
	TotalTokensEst         int              `json:"total_tokens_est"`
	TotalCostUSDEst        float64          `json:"total_cost_usd_est"`
	UsageSource            cost.UsageSource `json:"usage_source,omitempty"`
	UsageUnavailableReason string           `json:"usage_unavailable_reason,omitempty"`

	BudgetMaxCostUSD *float64 `json:"budget_max_cost_usd,omitempty"`
	BudgetMaxTokens  *int     `json:"budget_max_tokens,omitempty"`
	BudgetExceeded   bool     `json:"budget_exceeded"`
	BudgetWarning    bool     `json:"budget_warning,omitempty"`
	BudgetAlertCount int      `json:"budget_alert_count,omitempty"`

	EvidenceManifestPath string           `json:"evidence_manifest_path,omitempty"`
	CostReportPath       string           `json:"cost_report_path,omitempty"`
	BudgetAlertsPath     string           `json:"budget_alerts_path,omitempty"`
	EvalReportPath       string           `json:"eval_report_path,omitempty"`
	FailureReviewPath    string           `json:"failure_review_path,omitempty"`
	Trace                *TraceArtifacts  `json:"trace,omitempty"`
	ReviewArtifacts      *ReviewArtifacts `json:"review_artifacts,omitempty"`
	Recovery             *RecoverySummary `json:"recovery,omitempty"`

	Note string `json:"note"`
}

type TraceArtifacts struct {
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`
}

type ReviewArtifacts struct {
	EvidenceManifest string `json:"evidence_manifest,omitempty"`
	CostReport       string `json:"cost_report,omitempty"`
	BudgetAlerts     string `json:"budget_alerts,omitempty"`
	EvalReport       string `json:"eval_report,omitempty"`
	FailureReview    string `json:"failure_review,omitempty"`
}

type RecoverySummary struct {
	Class           string `json:"class,omitempty"`
	Action          string `json:"action,omitempty"`
	FollowUpRoute   string `json:"follow_up_route,omitempty"`
	RetrySuppressed bool   `json:"retry_suppressed"`
	RetryOutcome    string `json:"retry_outcome,omitempty"`
	UnlockDimension string `json:"unlock_dimension,omitempty"`
}

// PhaseSummary records the outcome of a single phase.
type PhaseSummary struct {
	Name                   string           `json:"name"`
	Type                   string           `json:"type"`
	Provider               string           `json:"provider,omitempty"`
	Model                  string           `json:"model,omitempty"`
	DurationMS             int64            `json:"duration_ms"`
	Status                 string           `json:"status"`
	EvalIterations         int              `json:"eval_iterations,omitempty"`
	EvalConverged          bool             `json:"eval_converged,omitempty"`
	EvalIntensity          string           `json:"eval_intensity,omitempty"`
	InputTokensEst         int              `json:"input_tokens_est"`
	OutputTokensEst        int              `json:"output_tokens_est"`
	CostUSDEst             float64          `json:"cost_usd_est"`
	UsageSource            cost.UsageSource `json:"usage_source,omitempty"`
	UsageUnavailableReason string           `json:"usage_unavailable_reason,omitempty"`
	GateType               string           `json:"gate_type,omitempty"`
	GatePassed             *bool            `json:"gate_passed,omitempty"`
	Error                  string           `json:"error,omitempty"`
}

type vesselRunState struct {
	startedAt   time.Time
	phases      []PhaseSummary
	evalReports map[string]PhaseEvaluationReport

	costTracker *cost.Tracker
	vesselID    string
	source      string
	workflow    string
	ref         string

	budgetMaxCostUSD *float64
	budgetMaxTokens  *int

	extraInputTokensEst  int
	extraOutputTokensEst int
	extraCostUSDEst      float64
	trace                *TraceArtifacts
}

type PhaseEvaluationReport struct {
	Phase       string                 `json:"phase"`
	Intensity   string                 `json:"intensity"`
	Signals     signal.SignalSet       `json:"signals"`
	Criteria    []evaluator.Criterion  `json:"criteria,omitempty"`
	Iterations  int                    `json:"iterations"`
	Converged   bool                   `json:"converged"`
	History     []evaluator.EvalResult `json:"history,omitempty"`
	FinalResult *evaluator.EvalResult  `json:"final_result,omitempty"`
}

type EvaluationArtifact struct {
	Phases []PhaseEvaluationReport `json:"phases"`
}

func newVesselRunState(cfg *config.Config, vessel queue.Vessel, startedAt time.Time) *vesselRunState {
	s := &vesselRunState{
		startedAt:   startedAt.UTC(),
		phases:      make([]PhaseSummary, 0),
		evalReports: make(map[string]PhaseEvaluationReport),
		vesselID:    vessel.ID,
		source:      vessel.Source,
		workflow:    vessel.Workflow,
		ref:         vessel.Ref,
	}

	if cfg == nil {
		return s
	}

	s.costTracker = cost.NewTracker(cfg.VesselBudget())
	if budget := cfg.VesselBudget(); budget != nil {
		if budget.CostLimitUSD > 0 {
			v := budget.CostLimitUSD
			s.budgetMaxCostUSD = &v
		}
		if budget.TokenLimit > 0 {
			v := budget.TokenLimit
			s.budgetMaxTokens = &v
		}
	}

	return s
}

func (s *vesselRunState) setTraceContext(data observability.TraceContextData) {
	if data.TraceID == "" && data.SpanID == "" {
		return
	}
	s.trace = &TraceArtifacts{
		TraceID: data.TraceID,
		SpanID:  data.SpanID,
	}
}

func (s *vesselRunState) addPhase(ps PhaseSummary) {
	s.phases = append(s.phases, ps)
}

func (s *vesselRunState) addEvaluationReport(report PhaseEvaluationReport) {
	if s == nil || strings.TrimSpace(report.Phase) == "" {
		return
	}
	if s.evalReports == nil {
		s.evalReports = make(map[string]PhaseEvaluationReport)
	}
	s.evalReports[report.Phase] = report
}

func (s *vesselRunState) buildSummary(state string, endedAt time.Time) *VesselSummary {
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	} else {
		endedAt = endedAt.UTC()
	}

	phases := make([]PhaseSummary, len(s.phases))
	copy(phases, s.phases)

	summary := &VesselSummary{
		VesselID:         s.vesselID,
		Source:           s.source,
		Workflow:         s.workflow,
		Ref:              s.ref,
		State:            state,
		StartedAt:        s.startedAt,
		EndedAt:          endedAt,
		DurationMS:       endedAt.Sub(s.startedAt).Milliseconds(),
		Phases:           phases,
		BudgetMaxCostUSD: s.budgetMaxCostUSD,
		BudgetMaxTokens:  s.budgetMaxTokens,
		Trace:            s.trace,
		Note:             summaryDisclaimer,
	}

	for _, p := range s.phases {
		summary.TotalInputTokensEst += p.InputTokensEst
		summary.TotalOutputTokensEst += p.OutputTokensEst
		summary.TotalCostUSDEst += p.CostUSDEst
	}

	summary.TotalInputTokensEst += s.extraInputTokensEst
	summary.TotalOutputTokensEst += s.extraOutputTokensEst
	summary.TotalCostUSDEst += s.extraCostUSDEst
	summary.TotalTokensEst = summary.TotalInputTokensEst + summary.TotalOutputTokensEst
	summary.UsageSource, summary.UsageUnavailableReason = summarizeUsageSource(summary.Phases, summary.TotalTokensEst, summary.TotalCostUSDEst)

	if s.costTracker != nil {
		summary.BudgetExceeded = s.costTracker.BudgetExceeded()
		alerts := s.costTracker.Alerts()
		summary.BudgetAlertCount = len(alerts)
		summary.BudgetWarning = hasBudgetWarning(alerts)
	}

	return summary
}

func (s *vesselRunState) recordLLMUsage(model, inputText, outputText string, recordedAt time.Time) (int, int, float64) {
	inputTokens := cost.EstimateTokens(inputText)
	outputTokens := cost.EstimateTokens(outputText)
	costUSDEst := cost.EstimateCost(inputTokens, outputTokens, cost.LookupPricing(model))

	if s.costTracker != nil {
		_ = s.costTracker.Record(cost.UsageRecord{
			MissionID:    s.vesselID,
			AgentRole:    cost.RoleGenerator,
			Purpose:      cost.PurposeReasoning,
			Model:        model,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      costUSDEst,
			Timestamp:    recordedAt.UTC(),
		})
	}

	return inputTokens, outputTokens, costUSDEst
}

func (s *vesselRunState) recordPromptOnlyUsage(model, prompt, output string, recordedAt time.Time) (int, int, float64) {
	inputTokens, outputTokens, costUSDEst := s.recordLLMUsage(model, prompt, output, recordedAt)
	s.extraInputTokensEst += inputTokens
	s.extraOutputTokensEst += outputTokens
	s.extraCostUSDEst += costUSDEst
	return inputTokens, outputTokens, costUSDEst
}

func (s *vesselRunState) recordEvaluationUsage(model, prompt, output string, recordedAt time.Time) (int, int, float64) {
	inputTokens := cost.EstimateTokens(prompt)
	outputTokens := cost.EstimateTokens(output)
	costUSDEst := cost.EstimateCost(inputTokens, outputTokens, cost.LookupPricing(model))

	if s.costTracker != nil {
		_ = s.costTracker.Record(cost.UsageRecord{
			MissionID:    s.vesselID,
			AgentRole:    cost.RoleEvaluator,
			Purpose:      cost.PurposeEvaluation,
			Model:        model,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      costUSDEst,
			Timestamp:    recordedAt.UTC(),
		})
	}

	s.extraInputTokensEst += inputTokens
	s.extraOutputTokensEst += outputTokens
	s.extraCostUSDEst += costUSDEst
	return inputTokens, outputTokens, costUSDEst
}

// recordPhaseTokens records LLM usage for a prompt-type phase and returns the
// estimated token counts and cost. Command phases do not consume model tokens.
func (s *vesselRunState) recordPhaseTokens(
	p workflow.Phase, model, renderedPrompt, output string, recordedAt time.Time,
) (inputTokensEst, outputTokensEst int, costUSDEst float64) {
	if p.Type == "command" {
		return 0, 0, 0.0
	}

	return s.recordLLMUsage(model, renderedPrompt, output, recordedAt)
}

func (s *vesselRunState) phaseSummary(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, harnessContent string, inputTokensEst, outputTokensEst int, costUSDEst float64, duration time.Duration, status string, gatePassed *bool, errMsg string) PhaseSummary {
	return s.phaseSummaryWithLLM(cfg, srcCfg, wf, p, harnessContent, inputTokensEst, outputTokensEst, costUSDEst, duration, status, gatePassed, errMsg, "", "")
}

func (s *vesselRunState) phaseSummaryWithLLM(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, harnessContent string, inputTokensEst, outputTokensEst int, costUSDEst float64, duration time.Duration, status string, gatePassed *bool, errMsg, provider, model string) PhaseSummary {
	summary := PhaseSummary{
		Name:       p.Name,
		Type:       phaseTypeLabel(p),
		DurationMS: duration.Milliseconds(),
		Status:     status,
		Error:      errMsg,
	}
	summary.UsageSource, summary.UsageUnavailableReason = phaseUsageSource(summary.Type)

	if p.Gate != nil {
		summary.GateType = p.Gate.Type
		summary.GatePassed = gatePassed
	}

	if summary.Type != "prompt" {
		return summary
	}

	if provider == "" {
		provider = resolveLegacyProvider(cfg, srcCfg, wf, &p)
	}
	if model == "" {
		model = resolveLegacyModel(cfg, srcCfg, wf, &p, provider)
	}
	summary.Provider = provider
	summary.Model = model
	summary.InputTokensEst = inputTokensEst
	summary.OutputTokensEst = outputTokensEst
	summary.CostUSDEst = costUSDEst

	return summary
}

func (s *vesselRunState) buildCostReport(summary *VesselSummary) *cost.CostReport {
	if s == nil || s.costTracker == nil || summary == nil {
		return nil
	}

	report := s.costTracker.Report(s.vesselID)
	report.Source = summary.Source
	report.Workflow = summary.Workflow
	report.Ref = summary.Ref
	report.State = summary.State
	report.TotalDurationMS = summary.DurationMS
	report.UsageSource = summary.UsageSource
	report.UsageUnavailableReason = summary.UsageUnavailableReason
	if report.RecordCount > 0 && report.UsageSource == cost.UsageSourceNotApplicable {
		report.UsageSource = cost.UsageSourceEstimated
		report.UsageUnavailableReason = ""
	}
	report.BudgetExceeded = summary.BudgetExceeded
	report.BudgetWarning = summary.BudgetWarning
	report.BudgetAlertCount = summary.BudgetAlertCount
	if summary.Trace != nil {
		report.TraceID = summary.Trace.TraceID
		report.SpanID = summary.Trace.SpanID
	}
	report.EvidenceManifestPath = summary.EvidenceManifestPath
	report.Phases = make([]cost.PhaseReport, 0, len(summary.Phases))
	for _, phase := range summary.Phases {
		report.Phases = append(report.Phases, cost.PhaseReport{
			Name:                   phase.Name,
			Type:                   phase.Type,
			Provider:               phase.Provider,
			Model:                  phase.Model,
			DurationMS:             phase.DurationMS,
			Status:                 phase.Status,
			InputTokens:            phase.InputTokensEst,
			OutputTokens:           phase.OutputTokensEst,
			TotalTokens:            phase.InputTokensEst + phase.OutputTokensEst,
			CostUSD:                phase.CostUSDEst,
			UsageSource:            phase.UsageSource,
			UsageUnavailableReason: phase.UsageUnavailableReason,
		})
	}
	return report
}

func phaseTypeLabel(p workflow.Phase) string {
	if p.Type == "command" {
		return "command"
	}
	return "prompt"
}

func gatePassedPointer(passed bool) *bool {
	v := passed
	return &v
}

func phaseUsageSource(phaseType string) (cost.UsageSource, string) {
	switch phaseType {
	case "prompt":
		return cost.UsageSourceEstimated, ""
	default:
		return cost.UsageSourceNotApplicable, "non-llm phase"
	}
}

func summarizeUsageSource(phases []PhaseSummary, totalTokens int, totalCost float64) (cost.UsageSource, string) {
	if totalTokens > 0 || totalCost > 0 {
		return cost.UsageSourceEstimated, ""
	}

	for _, phase := range phases {
		switch phase.UsageSource {
		case cost.UsageSourceEstimated, cost.UsageSourceProvider:
			return phase.UsageSource, ""
		}
	}

	if len(phases) == 0 {
		return cost.UsageSourceNotApplicable, "run did not execute an llm phase"
	}

	return cost.UsageSourceNotApplicable, "run did not execute an llm phase"
}

func hasBudgetWarning(alerts []cost.BudgetAlert) bool {
	for _, alert := range alerts {
		if alert.Type == "warning" {
			return true
		}
	}
	return false
}

func (s *vesselRunState) evaluationArtifact() *EvaluationArtifact {
	if s == nil || len(s.evalReports) == 0 {
		return nil
	}
	phases := make([]PhaseEvaluationReport, 0, len(s.evalReports))
	for _, report := range s.evalReports {
		phases = append(phases, report)
	}
	sort.Slice(phases, func(i, j int) bool {
		return phases[i].Phase < phases[j].Phase
	})
	return &EvaluationArtifact{Phases: phases}
}

func evidenceManifestRelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, "evidence-manifest.json"))
}

func costReportRelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, costReportFileName))
}

func budgetAlertsRelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, budgetAlertsFileName))
}

func evalReportRelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, evalReportFileName))
}

func failureReviewRelativePath(vesselID string) string {
	return recovery.RelativePath(vesselID)
}

func phaseArtifactRelativePath(vesselID, phaseName string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, phaseName+".output"))
}

func recoverySummaryFromArtifact(artifact *recovery.Artifact) *RecoverySummary {
	if artifact == nil {
		return nil
	}
	return &RecoverySummary{
		Class:           string(artifact.RecoveryClass),
		Action:          string(artifact.RecoveryAction),
		FollowUpRoute:   artifact.FollowUpRoute,
		RetrySuppressed: artifact.RetrySuppressed,
		RetryOutcome:    artifact.RetryOutcome,
		UnlockDimension: artifact.UnlockDimension,
	}
}

// SaveVesselSummary writes a summary to <stateDir>/phases/<vesselID>/summary.json.
func SaveVesselSummary(stateDir string, summary *VesselSummary) error {
	if summary == nil {
		return fmt.Errorf("save vessel summary: summary must not be nil")
	}
	if err := validateSummaryPathComponent(summary.VesselID); err != nil {
		return fmt.Errorf("save vessel summary: invalid vessel ID: %w", err)
	}

	path := summaryPath(stateDir, summary.VesselID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save vessel summary: create dir: %w", err)
	}

	summary.Note = summaryDisclaimer
	if summary.Phases == nil {
		summary.Phases = []PhaseSummary{}
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("save vessel summary: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save vessel summary: write: %w", err)
	}

	return nil
}

func summaryPath(stateDir, vesselID string) string {
	return config.RuntimePath(stateDir, "phases", vesselID, summaryFileName)
}

func validateSummaryPathComponent(component string) error {
	if component == "" {
		return fmt.Errorf("path component must not be empty")
	}
	if strings.Contains(component, "..") {
		return fmt.Errorf("path component must not contain %q", "..")
	}
	if !safeSummaryPathComponent.MatchString(component) {
		return fmt.Errorf("path component %q contains invalid characters (allowed: a-zA-Z0-9._-)", component)
	}
	return nil
}
