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
	summaryDisclaimer = "Token counts and costs are estimates (len/4 heuristic + static pricing). Not provider-reported values."
)

var safeSummaryPathComponent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ArtifactRetention records the retention policy for structured runner artifacts.
type ArtifactRetention struct {
	CleanupAfter string   `json:"cleanup_after"`
	Preserved    []string `json:"preserved"`
	Expiring     []string `json:"expiring"`
}

func defaultArtifactRetention() ArtifactRetention {
	return ArtifactRetention{
		CleanupAfter: "168h",
		Preserved: []string{
			"progress_<vessel>.json",
			"handoff_<vessel>_latest.json",
			"summary.json",
			"*.output",
			"*.prompt",
		},
		Expiring: []string{
			"*" + contextManifestSuffix,
			"handoff_<vessel>_<snapshot>.json",
		},
	}
}

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

	TotalInputTokensEst  int     `json:"total_input_tokens_est"`
	TotalOutputTokensEst int     `json:"total_output_tokens_est"`
	TotalTokensEst       int     `json:"total_tokens_est"`
	TotalCostUSDEst      float64 `json:"total_cost_usd_est"`

	BudgetMaxCostUSD *float64 `json:"budget_max_cost_usd,omitempty"`
	BudgetMaxTokens  *int     `json:"budget_max_tokens,omitempty"`
	BudgetExceeded   bool     `json:"budget_exceeded"`

	EvidenceManifestPath string            `json:"evidence_manifest_path,omitempty"`
	ProgressPath         string            `json:"progress_path,omitempty"`
	HandoffPath          string            `json:"handoff_path,omitempty"`
	Retention            ArtifactRetention `json:"retention"`

	Note string `json:"note"`
}

// PhaseSummary records the outcome of a single phase.
type PhaseSummary struct {
	Name                string  `json:"name"`
	Type                string  `json:"type"`
	Provider            string  `json:"provider,omitempty"`
	Model               string  `json:"model,omitempty"`
	DurationMS          int64   `json:"duration_ms"`
	Status              string  `json:"status"`
	InputTokensEst      int     `json:"input_tokens_est"`
	OutputTokensEst     int     `json:"output_tokens_est"`
	CostUSDEst          float64 `json:"cost_usd_est"`
	GateType            string  `json:"gate_type,omitempty"`
	GatePassed          *bool   `json:"gate_passed,omitempty"`
	ContextManifestPath string  `json:"context_manifest_path,omitempty"`
	Error               string  `json:"error,omitempty"`
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

	extraInputTokensEst  int
	extraOutputTokensEst int
	extraCostUSDEst      float64

	progressPath string
	handoffPath  string
	retention    ArtifactRetention
}

func newVesselRunState(cfg *config.Config, vessel queue.Vessel, startedAt time.Time) *vesselRunState {
	s := &vesselRunState{
		startedAt: startedAt.UTC(),
		phases:    make([]PhaseSummary, 0),
		vesselID:  vessel.ID,
		source:    vessel.Source,
		workflow:  vessel.Workflow,
		ref:       vessel.Ref,
		retention: defaultArtifactRetention(),
	}

	if cfg == nil {
		return s
	}

	if cfg.CleanupAfter != "" {
		s.retention.CleanupAfter = cfg.CleanupAfter
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
		ProgressPath:     s.progressPath,
		HandoffPath:      s.handoffPath,
		Retention:        s.retention,
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

	if s.costTracker != nil {
		summary.BudgetExceeded = s.costTracker.BudgetExceeded()
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
	return s.phaseSummaryWithContext(cfg, srcCfg, wf, p, harnessContent, "", inputTokensEst, outputTokensEst, costUSDEst, duration, status, gatePassed, errMsg)
}

func (s *vesselRunState) phaseSummaryWithContext(cfg *config.Config, srcCfg *config.SourceConfig, wf *workflow.Workflow, p workflow.Phase, harnessContent, contextManifestPath string, inputTokensEst, outputTokensEst int, costUSDEst float64, duration time.Duration, status string, gatePassed *bool, errMsg string) PhaseSummary {
	summary := PhaseSummary{
		Name:                p.Name,
		Type:                phaseTypeLabel(p),
		DurationMS:          duration.Milliseconds(),
		Status:              status,
		ContextManifestPath: contextManifestPath,
		Error:               errMsg,
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
	summary.InputTokensEst = inputTokensEst
	summary.OutputTokensEst = outputTokensEst
	summary.CostUSDEst = costUSDEst

	return summary
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
	if summary.Retention.CleanupAfter == "" {
		summary.Retention = defaultArtifactRetention()
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
