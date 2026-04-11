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
	attrs := make([]SpanAttribute, 0, 18)
	attrs = append(attrs, VesselSpanAttributes(VesselSpanData{
		ID:       "vessel-1",
		Source:   "github",
		Workflow: "fix-bug",
		Ref:      "refs/pull/1/head",
	})...)
	attrs = append(attrs, PhaseSpanAttributes(PhaseSpanData{
		Name:         "analyse",
		Index:        1,
		Type:         "prompt",
		Workflow:     "fix-bug",
		Provider:     "anthropic",
		Model:        "claude-sonnet",
		Tier:         "med",
		RetryAttempt: 1,
		SandboxMode:  "default",
	})...)
	attrs = append(attrs, PhaseResultAttributes(PhaseResultData{
		InputTokensEst:     10,
		OutputTokensEst:    20,
		CostUSDEst:         1.25,
		DurationMS:         30,
		Status:             "completed",
		OutputArtifactPath: "phases/vessel-1/analyse.output",
	})...)
	attrs = append(attrs, GateSpanAttributes(GateSpanData{
		Type:         "command",
		Passed:       true,
		RetryAttempt: 1,
	})...)
	return attrs
}

func TestProp_DrainSpanAttributesAlwaysTwoKeys(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		concurrency := rapid.IntRange(0, 1024).Draw(t, "concurrency")
		timeout := rapid.StringMatching(`[a-zA-Z0-9:._-]{0,40}`).Draw(t, "timeout")

		attrs := DrainSpanAttributes(DrainSpanData{
			Concurrency: concurrency,
			Timeout:     timeout,
		})
		if len(attrs) != 2 {
			t.Fatalf("expected 2 attributes, got %d", len(attrs))
		}
		if attrs[0].Key != "xylem.drain.concurrency" {
			t.Fatalf("attrs[0].Key = %q, want %q", attrs[0].Key, "xylem.drain.concurrency")
		}
		if attrs[1].Key != "xylem.drain.timeout" {
			t.Fatalf("attrs[1].Key = %q, want %q", attrs[1].Key, "xylem.drain.timeout")
		}
	})
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

func TestPropPhaseAttrsCarryResolvedLLMFieldsWhenProviderAndTierSet(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := genVesselString().Draw(t, "name")
		index := rapid.IntRange(0, 100).Draw(t, "index")
		phaseType := genPhaseType().Draw(t, "phase_type")
		workflow := genVesselString().Draw(t, "workflow")
		provider := genNonEmptyVesselString().Draw(t, "provider")
		model := genVesselString().Draw(t, "model")
		tier := genNonEmptyVesselString().Draw(t, "tier")
		retryAttempt := rapid.IntRange(0, 10).Draw(t, "retry_attempt")
		sandboxMode := genVesselString().Draw(t, "sandbox_mode")
		attrs := PhaseSpanAttributes(PhaseSpanData{
			Name:         name,
			Index:        index,
			Type:         phaseType,
			Workflow:     workflow,
			Provider:     provider,
			Model:        model,
			Tier:         tier,
			RetryAttempt: retryAttempt,
			SandboxMode:  sandboxMode,
		})
		got := attrMap(attrs)
		if got["xylem.phase.name"] != name {
			t.Fatalf("xylem.phase.name = %q, want %q", got["xylem.phase.name"], name)
		}
		if got["xylem.phase.index"] != strconv.Itoa(index) {
			t.Fatalf("xylem.phase.index = %q, want %q", got["xylem.phase.index"], strconv.Itoa(index))
		}
		if got["xylem.phase.type"] != phaseType {
			t.Fatalf("xylem.phase.type = %q, want %q", got["xylem.phase.type"], phaseType)
		}
		if got["xylem.phase.workflow"] != workflow {
			t.Fatalf("xylem.phase.workflow = %q, want %q", got["xylem.phase.workflow"], workflow)
		}
		if got["xylem.phase.provider"] != provider {
			t.Fatalf("xylem.phase.provider = %q, want %q", got["xylem.phase.provider"], provider)
		}
		if got["xylem.phase.model"] != model {
			t.Fatalf("xylem.phase.model = %q, want %q", got["xylem.phase.model"], model)
		}
		if got["xylem.phase.retry_attempt"] != strconv.Itoa(retryAttempt) {
			t.Fatalf("xylem.phase.retry_attempt = %q, want %q", got["xylem.phase.retry_attempt"], strconv.Itoa(retryAttempt))
		}
		if got["xylem.phase.sandbox_mode"] != sandboxMode {
			t.Fatalf("xylem.phase.sandbox_mode = %q, want %q", got["xylem.phase.sandbox_mode"], sandboxMode)
		}
		if got["llm.provider"] != provider {
			t.Fatalf("llm.provider = %q, want %q", got["llm.provider"], provider)
		}
		if got["llm.tier"] != tier {
			t.Fatalf("llm.tier = %q, want %q", got["llm.tier"], tier)
		}
	})
}

func TestPropPhaseAttrsOmitLLMKeysWhenProviderAndTierEmpty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := PhaseSpanAttributes(PhaseSpanData{
			Name:         genVesselString().Draw(t, "name"),
			Index:        rapid.IntRange(0, 100).Draw(t, "index"),
			Type:         genPhaseType().Draw(t, "phase_type"),
			Workflow:     genVesselString().Draw(t, "workflow"),
			Model:        genVesselString().Draw(t, "model"),
			RetryAttempt: rapid.IntRange(0, 10).Draw(t, "retry_attempt"),
			SandboxMode:  genVesselString().Draw(t, "sandbox_mode"),
		})
		got := attrMap(attrs)
		if _, ok := got["llm.provider"]; ok {
			t.Fatalf("llm.provider should be omitted, got %#v", got)
		}
		if _, ok := got["llm.tier"]; ok {
			t.Fatalf("llm.tier should be omitted, got %#v", got)
		}
	})
}

func TestPropPhaseAttrsIndexStringified(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		index := rapid.IntRange(0, 10000).Draw(t, "index")
		attrs := PhaseSpanAttributes(PhaseSpanData{
			Name:         "phase",
			Index:        index,
			Type:         "prompt",
			Workflow:     "workflow",
			Provider:     "provider",
			Model:        "model",
			RetryAttempt: 1,
			SandboxMode:  "default",
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

func TestPropPhaseResultAttrsAlwaysEight(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := PhaseResultAttributes(PhaseResultData{
			InputTokensEst:     rapid.IntRange(0, 1000000).Draw(t, "input_tokens"),
			OutputTokensEst:    rapid.IntRange(0, 1000000).Draw(t, "output_tokens"),
			CostUSDEst:         rapid.Float64Range(0.0, 100.0).Draw(t, "cost"),
			DurationMS:         int64(rapid.Int64Range(0, 600000).Draw(t, "duration_ms")),
			Status:             genVesselString().Draw(t, "status"),
			OutputArtifactPath: genVesselString().Draw(t, "output_artifact_path"),
		})
		if len(attrs) != 8 {
			t.Fatalf("expected 8 attributes, got %d", len(attrs))
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
