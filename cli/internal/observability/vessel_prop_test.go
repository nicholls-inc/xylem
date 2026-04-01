package observability

import (
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func genVesselString() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-z0-9/_\.-]{0,40}`)
}

func genNonEmptyVesselString() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-z0-9/_\.-]{1,40}`)
}

func genPhaseType() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{"prompt", "command"})
}

func genGateType() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{"command", "label"})
}

func combinedAttrs() []SpanAttribute {
	attrs := make([]SpanAttribute, 0, 16)
	attrs = append(attrs, VesselSpanAttributes(VesselSpanData{
		ID:       "vessel-1",
		Source:   "github",
		Workflow: "fix-bug",
		Ref:      "refs/pull/1/head",
	})...)
	attrs = append(attrs, PhaseSpanAttributes(PhaseSpanData{
		Name:     "analyse",
		Index:    1,
		Type:     "prompt",
		Provider: "anthropic",
		Model:    "claude-sonnet",
	})...)
	attrs = append(attrs, PhaseResultAttributes(PhaseResultData{
		InputTokensEst:  10,
		OutputTokensEst: 20,
		CostUSDEst:      1.25,
		DurationMS:      30,
	})...)
	attrs = append(attrs, GateSpanAttributes(GateSpanData{
		Type:         "command",
		Passed:       true,
		RetryAttempt: 1,
	})...)
	return attrs
}

func TestPropVesselAttrsLengthRefPresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := genVesselString().Draw(t, "id")
		source := genVesselString().Draw(t, "source")
		workflow := genVesselString().Draw(t, "workflow")
		ref := genNonEmptyVesselString().Draw(t, "ref")

		attrs := VesselSpanAttributes(VesselSpanData{
			ID:       id,
			Source:   source,
			Workflow: workflow,
			Ref:      ref,
		})
		if len(attrs) != 4 {
			t.Fatalf("expected 4 attributes, got %d", len(attrs))
		}
	})
}

func TestPropVesselAttrsLengthRefAbsent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := genVesselString().Draw(t, "id")
		source := genVesselString().Draw(t, "source")
		workflow := genVesselString().Draw(t, "workflow")

		attrs := VesselSpanAttributes(VesselSpanData{
			ID:       id,
			Source:   source,
			Workflow: workflow,
		})
		if len(attrs) != 3 {
			t.Fatalf("expected 3 attributes, got %d", len(attrs))
		}
	})
}

func TestPropVesselAttrsKeysNamespaced(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := VesselSpanAttributes(VesselSpanData{
			ID:       genVesselString().Draw(t, "id"),
			Source:   genVesselString().Draw(t, "source"),
			Workflow: genVesselString().Draw(t, "workflow"),
			Ref:      genVesselString().Draw(t, "ref"),
		})
		for _, attr := range attrs {
			if !strings.HasPrefix(attr.Key, "xylem.vessel.") {
				t.Fatalf("key %q does not use xylem.vessel namespace", attr.Key)
			}
		}
	})
}

func TestPropPhaseAttrsAlwaysFive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := PhaseSpanAttributes(PhaseSpanData{
			Name:     genVesselString().Draw(t, "name"),
			Index:    rapid.IntRange(0, 100).Draw(t, "index"),
			Type:     genPhaseType().Draw(t, "phase_type"),
			Provider: genVesselString().Draw(t, "provider"),
			Model:    genVesselString().Draw(t, "model"),
		})
		if len(attrs) != 5 {
			t.Fatalf("expected 5 attributes, got %d", len(attrs))
		}
	})
}

func TestPropPhaseAttrsIndexStringified(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		index := rapid.IntRange(0, 10000).Draw(t, "index")
		attrs := PhaseSpanAttributes(PhaseSpanData{
			Name:     "phase",
			Index:    index,
			Type:     "prompt",
			Provider: "provider",
			Model:    "model",
		})
		got, ok := attrMap(attrs)["xylem.phase.index"]
		if !ok {
			t.Fatal("xylem.phase.index not found")
		}
		parsed, err := strconv.Atoi(got)
		if err != nil {
			t.Fatalf("failed to parse %q: %v", got, err)
		}
		if parsed != index {
			t.Fatalf("parsed index = %d, want %d", parsed, index)
		}
	})
}

func TestPropPhaseResultAttrsAlwaysFour(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := PhaseResultAttributes(PhaseResultData{
			InputTokensEst:  rapid.IntRange(0, 1000000).Draw(t, "input_tokens"),
			OutputTokensEst: rapid.IntRange(0, 1000000).Draw(t, "output_tokens"),
			CostUSDEst:      rapid.Float64Range(0.0, 100.0).Draw(t, "cost"),
			DurationMS:      int64(rapid.Int64Range(0, 600000).Draw(t, "duration_ms")),
		})
		if len(attrs) != 4 {
			t.Fatalf("expected 4 attributes, got %d", len(attrs))
		}
	})
}

func TestPropPhaseResultCostSixDecimals(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := PhaseResultAttributes(PhaseResultData{
			InputTokensEst:  rapid.IntRange(0, 100).Draw(t, "input_tokens"),
			OutputTokensEst: rapid.IntRange(0, 100).Draw(t, "output_tokens"),
			CostUSDEst:      rapid.Float64Range(0.0, 100.0).Draw(t, "cost"),
			DurationMS:      int64(rapid.Int64Range(0, 1000).Draw(t, "duration_ms")),
		})
		cost := attrMap(attrs)["xylem.phase.cost_usd_est"]
		parts := strings.Split(cost, ".")
		if len(parts) != 2 {
			t.Fatalf("expected decimal cost, got %q", cost)
		}
		if len(parts[1]) != 6 {
			t.Fatalf("expected 6 decimal places, got %q", cost)
		}
	})
}

func TestPropGateAttrsAlwaysThree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := GateSpanAttributes(GateSpanData{
			Type:         genGateType().Draw(t, "gate_type"),
			Passed:       rapid.Bool().Draw(t, "passed"),
			RetryAttempt: rapid.IntRange(0, 10).Draw(t, "retry_attempt"),
		})
		if len(attrs) != 3 {
			t.Fatalf("expected 3 attributes, got %d", len(attrs))
		}
	})
}

func TestPropGatePassedIsLowercaseBool(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := GateSpanAttributes(GateSpanData{
			Type:   "command",
			Passed: rapid.Bool().Draw(t, "passed"),
		})
		got := attrMap(attrs)["xylem.gate.passed"]
		if got != "true" && got != "false" {
			t.Fatalf("expected true or false, got %q", got)
		}
	})
}

func TestPropAllVesselKeysLowercase(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		for _, attr := range combinedAttrs() {
			if attr.Key != strings.ToLower(attr.Key) {
				t.Fatalf("key %q is not lowercase", attr.Key)
			}
		}
	})
}

func TestPropAttributesDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := genVesselString().Draw(t, "id")
		source := genVesselString().Draw(t, "source")
		workflow := genVesselString().Draw(t, "workflow")
		ref := genVesselString().Draw(t, "ref")
		name := genVesselString().Draw(t, "name")
		index := rapid.IntRange(0, 10000).Draw(t, "index")
		phaseType := genPhaseType().Draw(t, "phase_type")
		provider := genVesselString().Draw(t, "provider")
		model := genVesselString().Draw(t, "model")
		inputTokens := rapid.IntRange(0, 1000000).Draw(t, "input_tokens")
		outputTokens := rapid.IntRange(0, 1000000).Draw(t, "output_tokens")
		cost := rapid.Float64Range(0.0, 100.0).Draw(t, "cost")
		duration := int64(rapid.Int64Range(0, 600000).Draw(t, "duration"))
		gateType := genGateType().Draw(t, "gate_type")
		passed := rapid.Bool().Draw(t, "passed")
		retryAttempt := rapid.IntRange(0, 10).Draw(t, "retry_attempt")

		checkDeterministic := func(name string, a, b []SpanAttribute) {
			t.Helper()
			if len(a) != len(b) {
				t.Fatalf("%s length mismatch: %d vs %d", name, len(a), len(b))
			}
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("%s mismatch at %d: %#v vs %#v", name, i, a[i], b[i])
				}
			}
		}

		checkDeterministic(
			"VesselSpanAttributes",
			VesselSpanAttributes(VesselSpanData{
				ID:       id,
				Source:   source,
				Workflow: workflow,
				Ref:      ref,
			}),
			VesselSpanAttributes(VesselSpanData{
				ID:       id,
				Source:   source,
				Workflow: workflow,
				Ref:      ref,
			}),
		)
		checkDeterministic(
			"PhaseSpanAttributes",
			PhaseSpanAttributes(PhaseSpanData{
				Name:     name,
				Index:    index,
				Type:     phaseType,
				Provider: provider,
				Model:    model,
			}),
			PhaseSpanAttributes(PhaseSpanData{
				Name:     name,
				Index:    index,
				Type:     phaseType,
				Provider: provider,
				Model:    model,
			}),
		)
		checkDeterministic(
			"PhaseResultAttributes",
			PhaseResultAttributes(PhaseResultData{
				InputTokensEst:  inputTokens,
				OutputTokensEst: outputTokens,
				CostUSDEst:      cost,
				DurationMS:      duration,
			}),
			PhaseResultAttributes(PhaseResultData{
				InputTokensEst:  inputTokens,
				OutputTokensEst: outputTokens,
				CostUSDEst:      cost,
				DurationMS:      duration,
			}),
		)
		checkDeterministic(
			"GateSpanAttributes",
			GateSpanAttributes(GateSpanData{
				Type:         gateType,
				Passed:       passed,
				RetryAttempt: retryAttempt,
			}),
			GateSpanAttributes(GateSpanData{
				Type:         gateType,
				Passed:       passed,
				RetryAttempt: retryAttempt,
			}),
		)
	})
}
