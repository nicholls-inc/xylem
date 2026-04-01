package observability

import "fmt"

// VesselSpanAttributes returns span attributes for a vessel span.
// When ref is empty, the xylem.vessel.ref attribute is omitted.
func VesselSpanAttributes(id, source, workflow, ref string) []SpanAttribute {
	attrs := []SpanAttribute{
		{Key: "xylem.vessel.id", Value: id},
		{Key: "xylem.vessel.source", Value: source},
		{Key: "xylem.vessel.workflow", Value: workflow},
	}
	if ref != "" {
		attrs = append(attrs, SpanAttribute{Key: "xylem.vessel.ref", Value: ref})
	}
	return attrs
}

// PhaseSpanAttributes returns span attributes for a phase span.
// The index is stringified, not stored as a numeric attribute.
func PhaseSpanAttributes(name string, index int, phaseType, provider, model string) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.phase.name", Value: name},
		{Key: "xylem.phase.index", Value: fmt.Sprintf("%d", index)},
		{Key: "xylem.phase.type", Value: phaseType},
		{Key: "xylem.phase.provider", Value: provider},
		{Key: "xylem.phase.model", Value: model},
	}
}

// PhaseResultAttributes returns span attributes added to a phase span
// after execution completes. Cost is formatted to six decimal places.
func PhaseResultAttributes(inputTokensEst, outputTokensEst int, costUSDEst float64, durationMS int64) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.phase.input_tokens_est", Value: fmt.Sprintf("%d", inputTokensEst)},
		{Key: "xylem.phase.output_tokens_est", Value: fmt.Sprintf("%d", outputTokensEst)},
		{Key: "xylem.phase.cost_usd_est", Value: fmt.Sprintf("%.6f", costUSDEst)},
		{Key: "xylem.phase.duration_ms", Value: fmt.Sprintf("%d", durationMS)},
	}
}

// GateSpanAttributes returns span attributes for a gate span.
// The boolean is rendered as lowercase "true"/"false" via %t.
func GateSpanAttributes(gateType string, passed bool, retryAttempt int) []SpanAttribute {
	return []SpanAttribute{
		{Key: "xylem.gate.type", Value: gateType},
		{Key: "xylem.gate.passed", Value: fmt.Sprintf("%t", passed)},
		{Key: "xylem.gate.retry_attempt", Value: fmt.Sprintf("%d", retryAttempt)},
	}
}
