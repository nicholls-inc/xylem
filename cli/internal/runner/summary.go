package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

const (
	summaryFileName   = "summary.json"
	summaryDisclaimer = "Best-available cost telemetry. Use usage_source fields to distinguish provider-reported, estimated fallback, and unavailable values. Legacy *_est fields remain estimate-only for compatibility."
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

	TotalInputTokens       int              `json:"total_input_tokens,omitempty"`
	TotalOutputTokens      int              `json:"total_output_tokens,omitempty"`
	TotalTokens            int              `json:"total_tokens,omitempty"`
	TotalCostUSD           float64          `json:"total_cost_usd,omitempty"`
	UsageSource            cost.UsageSource `json:"usage_source,omitempty"`
	UsageUnavailableReason string           `json:"usage_unavailable_reason,omitempty"`

	TotalInputTokensEst  int     `json:"total_input_tokens_est"`
	TotalOutputTokensEst int     `json:"total_output_tokens_est"`
	TotalTokensEst       int     `json:"total_tokens_est"`
	TotalCostUSDEst      float64 `json:"total_cost_usd_est"`

	BudgetMaxCostUSD *float64           `json:"budget_max_cost_usd,omitempty"`
	BudgetMaxTokens  *int               `json:"budget_max_tokens,omitempty"`
	BudgetExceeded   bool               `json:"budget_exceeded"`
	BudgetAlerts     []cost.BudgetAlert `json:"budget_alerts,omitempty"`

	EvidenceManifestPath string `json:"evidence_manifest_path,omitempty"`

	Note string `json:"note"`
}

// PhaseSummary records the outcome of a single phase.
type PhaseSummary struct {
	Name                   string           `json:"name"`
	Type                   string           `json:"type"`
	Provider               string           `json:"provider,omitempty"`
	Model                  string           `json:"model,omitempty"`
	DurationMS             int64            `json:"duration_ms"`
	Status                 string           `json:"status"`
	ArtifactPath           string           `json:"artifact_path,omitempty"`
	InputTokens            int              `json:"input_tokens,omitempty"`
	OutputTokens           int              `json:"output_tokens,omitempty"`
	TotalTokens            int              `json:"total_tokens,omitempty"`
	CostUSD                float64          `json:"cost_usd,omitempty"`
	UsageSource            cost.UsageSource `json:"usage_source,omitempty"`
	UsageUnavailableReason string           `json:"usage_unavailable_reason,omitempty"`
	InputTokensEst         int              `json:"input_tokens_est"`
	OutputTokensEst        int              `json:"output_tokens_est"`
	CostUSDEst             float64          `json:"cost_usd_est"`
	GateType               string           `json:"gate_type,omitempty"`
	GatePassed             *bool            `json:"gate_passed,omitempty"`
	Error                  string           `json:"error,omitempty"`
}

type vesselRunState struct {
	startedAt time.Time
	phases    []PhaseSummary

	costTracker *cost.Tracker
	vesselID    string
	source      string
	workflow    string
	ref         string

	budgetMaxCostUSD *float64
	budgetMaxTokens  *int

	extraInputTokens            int
	extraOutputTokens           int
	extraCostUSD                float64
	extraInputTokensEst         int
	extraOutputTokensEst        int
	extraCostUSDEst             float64
	extraUsageSource            cost.UsageSource
	extraUsageUnavailableReason string
	extraProvider               string
	extraModel                  string
	alertCursor                 int
}

func newVesselRunState(cfg *config.Config, vessel queue.Vessel, startedAt time.Time) *vesselRunState {
	s := &vesselRunState{
		startedAt:   startedAt.UTC(),
		phases:      make([]PhaseSummary, 0),
		costTracker: cost.NewTracker(nil),
		vesselID:    vessel.ID,
		source:      vessel.Source,
		workflow:    vessel.Workflow,
		ref:         vessel.Ref,
	}

	if cfg == nil {
		return s
	}

	if budget := cfg.VesselBudget(); budget != nil {
		s.costTracker = cost.NewTracker(budget)
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

func (s *vesselRunState) addPhase(ps PhaseSummary) {
	s.phases = append(s.phases, ps)
}

func aggregateUsageBreakdowns(phases []cost.PhaseCostBreakdown) []cost.PhaseCostBreakdown {
	filtered := make([]cost.PhaseCostBreakdown, 0, len(phases))
	for _, phase := range phases {
		if phase.Type == "command" {
			continue
		}
		filtered = append(filtered, phase)
	}
	return filtered
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
		Note:             summaryDisclaimer,
	}

	for _, p := range s.phases {
		summary.TotalInputTokens += p.InputTokens
		summary.TotalOutputTokens += p.OutputTokens
		summary.TotalCostUSD += p.CostUSD
		summary.TotalInputTokensEst += p.InputTokensEst
		summary.TotalOutputTokensEst += p.OutputTokensEst
		summary.TotalCostUSDEst += p.CostUSDEst
	}

	summary.TotalInputTokens += s.extraInputTokens
	summary.TotalOutputTokens += s.extraOutputTokens
	summary.TotalCostUSD += s.extraCostUSD
	summary.TotalInputTokensEst += s.extraInputTokensEst
	summary.TotalOutputTokensEst += s.extraOutputTokensEst
	summary.TotalCostUSDEst += s.extraCostUSDEst
	summary.TotalTokens = summary.TotalInputTokens + summary.TotalOutputTokens
	summary.TotalTokensEst = summary.TotalInputTokensEst + summary.TotalOutputTokensEst

	usageSources := make([]cost.PhaseCostBreakdown, 0, len(s.phases)+1)
	for _, p := range s.phases {
		usageSources = append(usageSources, cost.PhaseCostBreakdown{
			Type:                   p.Type,
			UsageSource:            p.UsageSource,
			UsageUnavailableReason: p.UsageUnavailableReason,
		})
	}
	if s.extraUsageSource != "" {
		usageSources = append(usageSources, cost.PhaseCostBreakdown{
			Type:                   "prompt",
			UsageSource:            s.extraUsageSource,
			UsageUnavailableReason: s.extraUsageUnavailableReason,
		})
	}
	usageSources = aggregateUsageBreakdowns(usageSources)
	summary.UsageSource = cost.DeriveUsageSource(usageSources)
	summary.UsageUnavailableReason = cost.FirstUsageUnavailableReason(usageSources)

	if s.costTracker != nil {
		summary.BudgetExceeded = s.costTracker.BudgetExceeded()
		summary.BudgetAlerts = s.costTracker.Alerts()
	}

	return summary
}

type usageDetails struct {
	inputTokens            int
	outputTokens           int
	totalTokens            int
	costUSD                float64
	inputTokensEst         int
	outputTokensEst        int
	totalTokensEst         int
	costUSDEst             float64
	usageSource            cost.UsageSource
	usageUnavailableReason string
}

func (s *vesselRunState) recordLLMUsage(model, inputText, outputText string, recordedAt time.Time, providerUsage executionUsage) usageDetails {
	inputTokens := cost.EstimateTokens(inputText)
	outputTokens := cost.EstimateTokens(outputText)
	costUSDEst := cost.EstimateCost(inputTokens, outputTokens, cost.LookupPricing(model))

	details := usageDetails{
		inputTokensEst:         inputTokens,
		outputTokensEst:        outputTokens,
		totalTokensEst:         inputTokens + outputTokens,
		costUSDEst:             costUSDEst,
		usageUnavailableReason: providerUsage.UnavailableReason,
	}

	recordInputTokens := inputTokens
	recordOutputTokens := outputTokens
	recordCostUSD := costUSDEst

	switch providerUsage.Source {
	case cost.UsageSourceReported:
		details.inputTokens = providerUsage.InputTokens
		details.outputTokens = providerUsage.OutputTokens
		details.totalTokens = providerUsage.InputTokens + providerUsage.OutputTokens
		details.costUSD = costUSDEst
		if providerUsage.CostUSD != nil {
			details.costUSD = *providerUsage.CostUSD
		}
		details.usageSource = cost.UsageSourceReported
		recordInputTokens = details.inputTokens
		recordOutputTokens = details.outputTokens
		recordCostUSD = details.costUSD
	case cost.UsageSourceUnavailable:
		details.inputTokens = inputTokens
		details.outputTokens = outputTokens
		details.totalTokens = inputTokens + outputTokens
		details.costUSD = costUSDEst
		details.usageSource = cost.UsageSourceEstimated
	default:
		details.inputTokens = inputTokens
		details.outputTokens = outputTokens
		details.totalTokens = inputTokens + outputTokens
		details.costUSD = costUSDEst
		details.usageSource = cost.UsageSourceEstimated
	}

	if s.costTracker != nil {
		_ = s.costTracker.Record(cost.UsageRecord{
			MissionID:    s.vesselID,
			AgentRole:    cost.RoleGenerator,
			Purpose:      cost.PurposeReasoning,
			Model:        model,
			InputTokens:  recordInputTokens,
			OutputTokens: recordOutputTokens,
			CostUSD:      recordCostUSD,
			Timestamp:    recordedAt.UTC(),
		})
	}

	return details
}

func (s *vesselRunState) recordPromptOnlyUsageWithUsage(provider, model, prompt, output string, recordedAt time.Time, providerUsage executionUsage) usageDetails {
	details := s.recordLLMUsage(model, prompt, output, recordedAt, providerUsage)
	s.extraInputTokens += details.inputTokens
	s.extraOutputTokens += details.outputTokens
	s.extraCostUSD += details.costUSD
	s.extraInputTokensEst += details.inputTokensEst
	s.extraOutputTokensEst += details.outputTokensEst
	s.extraCostUSDEst += details.costUSDEst
	s.extraUsageSource = details.usageSource
	s.extraUsageUnavailableReason = details.usageUnavailableReason
	s.extraProvider = provider
	s.extraModel = model
	return details
}

func (s *vesselRunState) recordPromptOnlyUsage(model, prompt, output string, recordedAt time.Time) (int, int, float64) {
	details := s.recordPromptOnlyUsageWithUsage("", model, prompt, output, recordedAt, executionUsage{
		Source:            cost.UsageSourceUnavailable,
		UnavailableReason: "provider output did not include structured usage metadata",
	})
	return details.inputTokensEst, details.outputTokensEst, details.costUSDEst
}

// recordPhaseTokens records LLM usage for a prompt-type phase and returns the
// estimated token counts and cost. Command phases do not consume model tokens.
func (s *vesselRunState) recordPhaseUsage(
	p workflow.Phase, model, renderedPrompt, output string, recordedAt time.Time, providerUsage executionUsage,
) usageDetails {
	if p.Type == "command" {
		return usageDetails{
			usageSource:            cost.UsageSourceUnavailable,
			usageUnavailableReason: "command phase does not consume model tokens",
		}
	}

	return s.recordLLMUsage(model, renderedPrompt, output, recordedAt, providerUsage)
}

func (s *vesselRunState) recordPhaseTokens(
	p workflow.Phase, model, renderedPrompt, output string, recordedAt time.Time,
) (inputTokensEst, outputTokensEst int, costUSDEst float64) {
	details := s.recordPhaseUsage(p, model, renderedPrompt, output, recordedAt, executionUsage{
		Source:            cost.UsageSourceUnavailable,
		UnavailableReason: "provider output did not include structured usage metadata",
	})
	return details.inputTokensEst, details.outputTokensEst, details.costUSDEst
}

func (s *vesselRunState) phaseSummaryWithUsage(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, harnessContent string, usage usageDetails, duration time.Duration, status string, gatePassed *bool, errMsg string) PhaseSummary {
	summary := PhaseSummary{
		Name:                   p.Name,
		Type:                   phaseTypeLabel(p),
		DurationMS:             duration.Milliseconds(),
		Status:                 status,
		Error:                  errMsg,
		ArtifactPath:           phaseArtifactRelativePath(s.vesselID, p.Name),
		InputTokens:            usage.inputTokens,
		OutputTokens:           usage.outputTokens,
		TotalTokens:            usage.totalTokens,
		CostUSD:                usage.costUSD,
		UsageSource:            usage.usageSource,
		UsageUnavailableReason: usage.usageUnavailableReason,
		InputTokensEst:         usage.inputTokensEst,
		OutputTokensEst:        usage.outputTokensEst,
		CostUSDEst:             usage.costUSDEst,
	}

	if p.Gate != nil {
		summary.GateType = p.Gate.Type
		summary.GatePassed = gatePassed
	}

	if summary.Type != "prompt" {
		return summary
	}

	provider := resolveProvider(cfg, srcCfg, wf, &p)
	model := resolveModel(cfg, srcCfg, wf, &p, provider)
	summary.Provider = provider
	summary.Model = model

	return summary
}

func (s *vesselRunState) phaseSummary(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, harnessContent string, inputTokensEst, outputTokensEst int, costUSDEst float64, duration time.Duration, status string, gatePassed *bool, errMsg string) PhaseSummary {
	return s.phaseSummaryWithUsage(cfg, srcCfg, wf, p, harnessContent, usageDetails{
		inputTokensEst:  inputTokensEst,
		outputTokensEst: outputTokensEst,
		totalTokensEst:  inputTokensEst + outputTokensEst,
		costUSDEst:      costUSDEst,
		usageSource:     cost.UsageSourceEstimated,
	}, duration, status, gatePassed, errMsg)
}

func (s *vesselRunState) consumeBudgetAlerts() []cost.BudgetAlert {
	if s == nil || s.costTracker == nil {
		return nil
	}
	alerts := s.costTracker.Alerts()
	if s.alertCursor >= len(alerts) {
		return nil
	}
	out := append([]cost.BudgetAlert(nil), alerts[s.alertCursor:]...)
	s.alertCursor = len(alerts)
	return out
}

func (s *vesselRunState) buildCostReport(state string, endedAt time.Time, join cost.ArtifactJoin) *cost.CostReport {
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	} else {
		endedAt = endedAt.UTC()
	}

	phases := make([]cost.PhaseCostBreakdown, 0, len(s.phases)+1)
	for _, phase := range s.phases {
		phases = append(phases, cost.PhaseCostBreakdown{
			Name:                   phase.Name,
			Type:                   phase.Type,
			Provider:               phase.Provider,
			Model:                  phase.Model,
			Status:                 phase.Status,
			DurationMS:             phase.DurationMS,
			ArtifactPath:           phase.ArtifactPath,
			InputTokens:            phase.InputTokens,
			OutputTokens:           phase.OutputTokens,
			TotalTokens:            phase.TotalTokens,
			CostUSD:                phase.CostUSD,
			UsageSource:            phase.UsageSource,
			UsageUnavailableReason: phase.UsageUnavailableReason,
		})
	}
	if s.extraUsageSource != "" {
		phases = append(phases, cost.PhaseCostBreakdown{
			Name:                   "prompt-only",
			Type:                   "prompt",
			Provider:               s.extraProvider,
			Model:                  s.extraModel,
			Status:                 state,
			InputTokens:            s.extraInputTokens,
			OutputTokens:           s.extraOutputTokens,
			TotalTokens:            s.extraInputTokens + s.extraOutputTokens,
			CostUSD:                s.extraCostUSD,
			UsageSource:            s.extraUsageSource,
			UsageUnavailableReason: s.extraUsageUnavailableReason,
			ArtifactPath:           phaseArtifactRelativePath(s.vesselID, "prompt-only"),
		})
	}

	aggregatePhases := aggregateUsageBreakdowns(phases)

	report := &cost.CostReport{
		MissionID:              s.vesselID,
		VesselID:               s.vesselID,
		Source:                 s.source,
		Workflow:               s.workflow,
		Ref:                    s.ref,
		State:                  state,
		ByRole:                 map[cost.AgentRole]float64{},
		ByPurpose:              map[cost.Purpose]float64{},
		ByModel:                map[string]float64{},
		GeneratedAt:            endedAt,
		Phases:                 phases,
		UsageSource:            cost.DeriveUsageSource(aggregatePhases),
		UsageUnavailableReason: cost.FirstUsageUnavailableReason(aggregatePhases),
		BudgetMaxCostUSD:       s.budgetMaxCostUSD,
		BudgetMaxTokens:        s.budgetMaxTokens,
		Join:                   join,
	}

	for _, phase := range phases {
		report.TotalInputTokens += phase.InputTokens
		report.TotalOutputTokens += phase.OutputTokens
		report.TotalTokens += phase.TotalTokens
		report.TotalCostUSD += phase.CostUSD
		if phase.Model != "" {
			report.ByModel[phase.Model] += phase.CostUSD
		}
		if phase.UsageSource != "" {
			report.RecordCount++
		}
	}
	if s.costTracker != nil {
		report.Alerts = s.costTracker.Alerts()
		report.BudgetExceeded = s.costTracker.BudgetExceeded()
		report.ByRole = s.costTracker.CostByRole()
		report.ByPurpose = s.costTracker.CostByPurpose()
		report.ByModel = s.costTracker.CostByModel()
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

func evidenceManifestRelativePath(vesselID string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, "evidence-manifest.json"))
}

func phaseArtifactRelativePath(vesselID, phaseName string) string {
	return filepath.ToSlash(filepath.Join("phases", vesselID, phaseName+".output"))
}

// SaveVesselSummary writes a summary to <stateDir>/phases/<vesselID>/summary.json.
func SaveVesselSummary(stateDir string, summary *VesselSummary) error {
	if summary == nil {
		return fmt.Errorf("save vessel summary: summary must not be nil")
	}
	if err := validateSummaryPathComponent(summary.VesselID); err != nil {
		return fmt.Errorf("save vessel summary: invalid vessel ID: %w", err)
	}

	path := filepath.Join(stateDir, "phases", summary.VesselID, summaryFileName)
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
