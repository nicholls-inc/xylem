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
	attrs = append(attrs, VesselSpanAttributes("vessel-1", "github", "fix-bug", "refs/pull/1/head")...)
	attrs = append(attrs, PhaseSpanAttributes("analyse", 1, "prompt", "anthropic", "claude-sonnet")...)
	attrs = append(attrs, PhaseResultAttributes(10, 20, 1.25, 30)...)
	attrs = append(attrs, GateSpanAttributes("command", true, 1)...)
	return attrs
}

func TestPropVesselAttrsLengthRefPresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := genVesselString().Draw(t, "id")
		source := genVesselString().Draw(t, "source")
		workflow := genVesselString().Draw(t, "workflow")
		ref := genNonEmptyVesselString().Draw(t, "ref")

		attrs := VesselSpanAttributes(id, source, workflow, ref)
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

		attrs := VesselSpanAttributes(id, source, workflow, "")
		if len(attrs) != 3 {
			t.Fatalf("expected 3 attributes, got %d", len(attrs))
		}
	})
}

func TestPropVesselAttrsKeysNamespaced(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := VesselSpanAttributes(
			genVesselString().Draw(t, "id"),
			genVesselString().Draw(t, "source"),
			genVesselString().Draw(t, "workflow"),
			genVesselString().Draw(t, "ref"),
		)
		for _, attr := range attrs {
			if !strings.HasPrefix(attr.Key, "xylem.vessel.") {
				t.Fatalf("key %q does not use xylem.vessel namespace", attr.Key)
			}
		}
	})
}

func TestPropPhaseAttrsAlwaysFive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := PhaseSpanAttributes(
			genVesselString().Draw(t, "name"),
			rapid.IntRange(0, 100).Draw(t, "index"),
			genPhaseType().Draw(t, "phase_type"),
			genVesselString().Draw(t, "provider"),
			genVesselString().Draw(t, "model"),
		)
		if len(attrs) != 5 {
			t.Fatalf("expected 5 attributes, got %d", len(attrs))
		}
	})
}

func TestPropPhaseAttrsIndexStringified(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		index := rapid.IntRange(0, 10000).Draw(t, "index")
		attrs := PhaseSpanAttributes("phase", index, "prompt", "provider", "model")
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
		attrs := PhaseResultAttributes(
			rapid.IntRange(0, 1000000).Draw(t, "input_tokens"),
			rapid.IntRange(0, 1000000).Draw(t, "output_tokens"),
			rapid.Float64Range(0.0, 100.0).Draw(t, "cost"),
			int64(rapid.Int64Range(0, 600000).Draw(t, "duration_ms")),
		)
		if len(attrs) != 4 {
			t.Fatalf("expected 4 attributes, got %d", len(attrs))
		}
	})
}

func TestPropPhaseResultCostSixDecimals(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := PhaseResultAttributes(
			rapid.IntRange(0, 100).Draw(t, "input_tokens"),
			rapid.IntRange(0, 100).Draw(t, "output_tokens"),
			rapid.Float64Range(0.0, 100.0).Draw(t, "cost"),
			int64(rapid.Int64Range(0, 1000).Draw(t, "duration_ms")),
		)
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
		attrs := GateSpanAttributes(
			genGateType().Draw(t, "gate_type"),
			rapid.Bool().Draw(t, "passed"),
			rapid.IntRange(0, 10).Draw(t, "retry_attempt"),
		)
		if len(attrs) != 3 {
			t.Fatalf("expected 3 attributes, got %d", len(attrs))
		}
	})
}

func TestPropGatePassedIsLowercaseBool(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := GateSpanAttributes("command", rapid.Bool().Draw(t, "passed"), 0)
		got := attrMap(attrs)["xylem.gate.passed"]
		if got != "true" && got != "false" {
			t.Fatalf("expected true or false, got %q", got)
		}
	})
}

func TestPropAllVesselKeysLowercase(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		_ = t
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
			VesselSpanAttributes(id, source, workflow, ref),
			VesselSpanAttributes(id, source, workflow, ref),
		)
		checkDeterministic(
			"PhaseSpanAttributes",
			PhaseSpanAttributes(name, index, phaseType, provider, model),
			PhaseSpanAttributes(name, index, phaseType, provider, model),
		)
		checkDeterministic(
			"PhaseResultAttributes",
			PhaseResultAttributes(inputTokens, outputTokens, cost, duration),
			PhaseResultAttributes(inputTokens, outputTokens, cost, duration),
		)
		checkDeterministic(
			"GateSpanAttributes",
			GateSpanAttributes(gateType, passed, retryAttempt),
			GateSpanAttributes(gateType, passed, retryAttempt),
		)
	})
}
