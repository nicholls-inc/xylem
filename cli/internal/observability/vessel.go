package observability

import (
	"fmt"
	"strings"
)

// VesselSpanData holds vessel information for attribute extraction.
type VesselSpanData struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Workflow string `json:"workflow"`
	Ref      string `json:"ref"`
}

// VesselSpanAttributes returns span attributes for a vessel span.
// When ref is empty, the xylem.vessel.ref attribute is omitted.
func VesselSpanAttributes(data VesselSpanData) []SpanAttribute {
	attrs := []SpanAttribute{
		{Key: "xylem.vessel.id", Value: data.ID},
		{Key: "xylem.vessel.source", Value: data.Source},
		{Key: "xylem.vessel.workflow", Value: data.Workflow},
	}
	if data.Ref != "" {
		attrs = append(attrs, SpanAttribute{Key: "xylem.vessel.ref", Value: data.Ref})
	}
	return attrs
}

// VesselHealthData holds derived vessel health and anomaly data.
type VesselHealthData struct {
	State        string   `json:"state"`
	Health       string   `json:"health"`
	AnomalyCount int      `json:"anomaly_count"`
	Anomalies    []string `json:"anomalies,omitempty"`
}

// VesselHealthAttributes returns span attributes for derived vessel health.
func VesselHealthAttributes(data VesselHealthData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.vessel.state", Value: data.State},
		{Key: "xylem.vessel.health", Value: data.Health},
		{Key: "xylem.vessel.anomaly_count", Value: fmt.Sprintf("%d", data.AnomalyCount)},
		{Key: "xylem.vessel.anomalies", Value: strings.Join(data.Anomalies, ",")},
	}
}

// DrainSpanData holds drain-run information for attribute extraction.
type DrainSpanData struct {
	Concurrency int    `json:"concurrency"`
	Timeout     string `json:"timeout"`
}

// DrainSpanAttributes returns span attributes for a drain span.
func DrainSpanAttributes(data DrainSpanData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.drain.concurrency", Value: fmt.Sprintf("%d", data.Concurrency)},
		{Key: "xylem.drain.timeout", Value: data.Timeout},
	}
}

// DrainHealthData holds aggregate health information for a drain run.
type DrainHealthData struct {
	Healthy   int    `json:"healthy"`
	Degraded  int    `json:"degraded"`
	Unhealthy int    `json:"unhealthy"`
	Patterns  string `json:"patterns,omitempty"`
}

// DrainHealthAttributes returns span attributes for aggregate vessel health.
func DrainHealthAttributes(data DrainHealthData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.drain.healthy_vessels", Value: fmt.Sprintf("%d", data.Healthy)},
		{Key: "xylem.drain.degraded_vessels", Value: fmt.Sprintf("%d", data.Degraded)},
		{Key: "xylem.drain.unhealthy_vessels", Value: fmt.Sprintf("%d", data.Unhealthy)},
		{Key: "xylem.drain.unhealthy_patterns", Value: data.Patterns},
	}
}

// PhaseSpanData holds phase information for attribute extraction.
type PhaseSpanData struct {
	Name         string `json:"name"`
	Index        int    `json:"index"`
	Type         string `json:"type"`
	Workflow     string `json:"workflow"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Tier         string `json:"tier,omitempty"`
	RetryAttempt int    `json:"retry_attempt"`
	SandboxMode  string `json:"sandbox_mode"`
}

// PhaseSpanAttributes returns span attributes for a phase span.
// The index is stringified, not stored as a numeric attribute.
func PhaseSpanAttributes(data PhaseSpanData) []SpanAttribute {
	attrs := []SpanAttribute{
		{Key: "xylem.phase.name", Value: data.Name},
		{Key: "xylem.phase.index", Value: fmt.Sprintf("%d", data.Index)},
		{Key: "xylem.phase.type", Value: data.Type},
		{Key: "xylem.phase.workflow", Value: data.Workflow},
		{Key: "xylem.phase.provider", Value: data.Provider},
		{Key: "xylem.phase.model", Value: data.Model},
		{Key: "xylem.phase.retry_attempt", Value: fmt.Sprintf("%d", data.RetryAttempt)},
		{Key: "xylem.phase.sandbox_mode", Value: data.SandboxMode},
	}
	if data.Provider != "" {
		attrs = append(attrs, SpanAttribute{Key: "llm.provider", Value: data.Provider})
	}
	if data.Tier != "" {
		attrs = append(attrs, SpanAttribute{Key: "llm.tier", Value: data.Tier})
	}
	return attrs
}

// PhaseResultData holds phase result information for attribute extraction.
type PhaseResultData struct {
	InputTokensEst         int     `json:"input_tokens_est"`
	OutputTokensEst        int     `json:"output_tokens_est"`
	CostUSDEst             float64 `json:"cost_usd_est"`
	DurationMS             int64   `json:"duration_ms"`
	Status                 string  `json:"status"`
	LLMProvider            string  `json:"llm_provider,omitempty"`
	LLMModel               string  `json:"llm_model,omitempty"`
	LLMTier                string  `json:"llm_tier,omitempty"`
	UsageSource            string  `json:"usage_source,omitempty"`
	UsageUnavailableReason string  `json:"usage_unavailable_reason,omitempty"`
	OutputArtifactPath     string  `json:"output_artifact_path,omitempty"`
}

// PhaseResultAttributes returns span attributes added to a phase span
// after execution completes. Cost is formatted to six decimal places.
func PhaseResultAttributes(data PhaseResultData) []SpanAttribute {
	attrs := []SpanAttribute{
		{Key: "xylem.phase.input_tokens_est", Value: fmt.Sprintf("%d", data.InputTokensEst)},
		{Key: "xylem.phase.output_tokens_est", Value: fmt.Sprintf("%d", data.OutputTokensEst)},
		{Key: "xylem.phase.cost_usd_est", Value: fmt.Sprintf("%.6f", data.CostUSDEst)},
		{Key: "xylem.phase.duration_ms", Value: fmt.Sprintf("%d", data.DurationMS)},
		{Key: "xylem.phase.status", Value: data.Status},
		{Key: "xylem.phase.usage_source", Value: data.UsageSource},
		{Key: "xylem.phase.usage_unavailable_reason", Value: data.UsageUnavailableReason},
		{Key: "xylem.phase.output_artifact_path", Value: data.OutputArtifactPath},
	}
	if data.LLMProvider != "" {
		attrs = append(attrs, SpanAttribute{Key: "xylem.phase.provider", Value: data.LLMProvider})
		attrs = append(attrs, SpanAttribute{Key: "llm.provider", Value: data.LLMProvider})
	}
	if data.LLMModel != "" {
		attrs = append(attrs, SpanAttribute{Key: "xylem.phase.model", Value: data.LLMModel})
	}
	if data.LLMTier != "" {
		attrs = append(attrs, SpanAttribute{Key: "llm.tier", Value: data.LLMTier})
	}
	return attrs
}

// VesselCostData holds vessel-level cost telemetry attributes.
type VesselCostData struct {
	TotalTokens            int     `json:"total_tokens"`
	TotalCostUSDEst        float64 `json:"total_cost_usd_est"`
	UsageSource            string  `json:"usage_source,omitempty"`
	UsageUnavailableReason string  `json:"usage_unavailable_reason,omitempty"`
	BudgetExceeded         bool    `json:"budget_exceeded"`
	BudgetWarning          bool    `json:"budget_warning"`
}

// VesselCostAttributes returns span attributes for vessel-level cost telemetry.
func VesselCostAttributes(data VesselCostData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.vessel.total_tokens_est", Value: fmt.Sprintf("%d", data.TotalTokens)},
		{Key: "xylem.vessel.total_cost_usd_est", Value: fmt.Sprintf("%.6f", data.TotalCostUSDEst)},
		{Key: "xylem.vessel.usage_source", Value: data.UsageSource},
		{Key: "xylem.vessel.usage_unavailable_reason", Value: data.UsageUnavailableReason},
		{Key: "xylem.vessel.budget_exceeded", Value: fmt.Sprintf("%t", data.BudgetExceeded)},
		{Key: "xylem.vessel.budget_warning", Value: fmt.Sprintf("%t", data.BudgetWarning)},
	}
}

// GateSpanData holds gate information for attribute extraction.
type GateSpanData struct {
	Type         string `json:"type"`
	Passed       bool   `json:"passed"`
	RetryAttempt int    `json:"retry_attempt"`
}

// GateSpanAttributes returns span attributes for a gate span.
// The boolean is rendered as lowercase "true"/"false" via %t.
func GateSpanAttributes(data GateSpanData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.gate.type", Value: data.Type},
		{Key: "xylem.gate.passed", Value: fmt.Sprintf("%t", data.Passed)},
		{Key: "xylem.gate.retry_attempt", Value: fmt.Sprintf("%d", data.RetryAttempt)},
	}
}

// GateStepSpanData holds verification-step information for live gates.
type GateStepSpanData struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Passed bool   `json:"passed"`
}

// GateStepSpanAttributes returns span attributes for a gate step span.
func GateStepSpanAttributes(data GateStepSpanData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.gate.step.name", Value: data.Name},
		{Key: "xylem.gate.step.mode", Value: data.Mode},
		{Key: "xylem.gate.step.passed", Value: fmt.Sprintf("%t", data.Passed)},
	}
}

// CommandSpanData holds CLI command attributes.
type CommandSpanData struct {
	Name     string `json:"name"`
	DryRun   bool   `json:"dry_run"`
	StateDir string `json:"state_dir,omitempty"`
}

// CommandSpanAttributes returns span attributes for a CLI command.
func CommandSpanAttributes(data CommandSpanData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.command.name", Value: data.Name},
		{Key: "xylem.command.dry_run", Value: fmt.Sprintf("%t", data.DryRun)},
		{Key: "xylem.command.state_dir", Value: data.StateDir},
	}
}

// WaitSpanData holds wait/resume timeout transition attributes.
type WaitSpanData struct {
	Transition string `json:"transition"`
	PhaseName  string `json:"phase_name,omitempty"`
	Label      string `json:"label,omitempty"`
	WaitedMS   int64  `json:"waited_ms"`
}

// WaitSpanAttributes returns attributes for wait and resume transitions.
func WaitSpanAttributes(data WaitSpanData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.wait.transition", Value: data.Transition},
		{Key: "xylem.wait.phase", Value: data.PhaseName},
		{Key: "xylem.wait.label", Value: data.Label},
		{Key: "xylem.wait.waited_ms", Value: fmt.Sprintf("%d", data.WaitedMS)},
	}
}

// ReporterSpanData holds GitHub reporter action attributes.
type ReporterSpanData struct {
	Action    string `json:"action"`
	Repo      string `json:"repo,omitempty"`
	IssueNum  int    `json:"issue_num"`
	PhaseName string `json:"phase_name,omitempty"`
}

// ReporterSpanAttributes returns attributes for reporter spans.
func ReporterSpanAttributes(data ReporterSpanData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.reporter.action", Value: data.Action},
		{Key: "xylem.reporter.repo", Value: data.Repo},
		{Key: "xylem.reporter.issue_number", Value: fmt.Sprintf("%d", data.IssueNum)},
		{Key: "xylem.reporter.phase", Value: data.PhaseName},
	}
}

// WorktreeSpanData holds worktree lifecycle attributes.
type WorktreeSpanData struct {
	Action string `json:"action"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path,omitempty"`
}

// WorktreeSpanAttributes returns attributes for worktree spans.
func WorktreeSpanAttributes(data WorktreeSpanData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.worktree.action", Value: data.Action},
		{Key: "xylem.worktree.branch", Value: data.Branch},
		{Key: "xylem.worktree.path", Value: data.Path},
	}
}

// RecoveryData holds recovery classification attributes for failed or timed out vessels.
type RecoveryData struct {
	Class           string `json:"class,omitempty"`
	Action          string `json:"action,omitempty"`
	RetrySuppressed string `json:"retry_suppressed,omitempty"`
	RetryOutcome    string `json:"retry_outcome,omitempty"`
	UnlockDimension string `json:"unlock_dimension,omitempty"`
}

// RecoveryAttributes returns span attributes for recovery classification and routing.
func RecoveryAttributes(data RecoveryData) []SpanAttribute {
	if data.Class == "" && data.Action == "" && data.RetrySuppressed == "" && data.RetryOutcome == "" && data.UnlockDimension == "" {
		return nil
	}
	return []SpanAttribute{
		{Key: "xylem.recovery.class", Value: data.Class},
		{Key: "xylem.recovery.action", Value: data.Action},
		{Key: "xylem.recovery.retry_suppressed", Value: data.RetrySuppressed},
		{Key: "xylem.recovery.retry_outcome", Value: data.RetryOutcome},
		{Key: "xylem.recovery.unlock_dimension", Value: data.UnlockDimension},
	}
}
