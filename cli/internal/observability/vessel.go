package observability

import "fmt"

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

// PhaseSpanData holds phase information for attribute extraction.
type PhaseSpanData struct {
	Name     string `json:"name"`
	Index    int    `json:"index"`
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// PhaseSpanAttributes returns span attributes for a phase span.
// The index is stringified, not stored as a numeric attribute.
func PhaseSpanAttributes(data PhaseSpanData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.phase.name", Value: data.Name},
		{Key: "xylem.phase.index", Value: fmt.Sprintf("%d", data.Index)},
		{Key: "xylem.phase.type", Value: data.Type},
		{Key: "xylem.phase.provider", Value: data.Provider},
		{Key: "xylem.phase.model", Value: data.Model},
	}
}

// PhaseResultData holds phase result information for attribute extraction.
type PhaseResultData struct {
	InputTokensEst  int     `json:"input_tokens_est"`
	OutputTokensEst int     `json:"output_tokens_est"`
	CostUSDEst      float64 `json:"cost_usd_est"`
	DurationMS      int64   `json:"duration_ms"`
}

// PhaseResultAttributes returns span attributes added to a phase span
// after execution completes. Cost is formatted to six decimal places.
func PhaseResultAttributes(data PhaseResultData) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.phase.input_tokens_est", Value: fmt.Sprintf("%d", data.InputTokensEst)},
		{Key: "xylem.phase.output_tokens_est", Value: fmt.Sprintf("%d", data.OutputTokensEst)},
		{Key: "xylem.phase.cost_usd_est", Value: fmt.Sprintf("%.6f", data.CostUSDEst)},
		{Key: "xylem.phase.duration_ms", Value: fmt.Sprintf("%d", data.DurationMS)},
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
