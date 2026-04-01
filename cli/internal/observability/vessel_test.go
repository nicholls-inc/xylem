package observability

import "testing"

func TestSmoke_S1_VesselSpanAttributesIncludesIDSourceWorkflowRef(t *testing.T) {
	attrs := VesselSpanAttributes(VesselSpanData{
		ID:       "vessel-123",
		Source:   "github",
		Workflow: "fix-bug",
		Ref:      "refs/pull/42/head",
	})
	got := attrMap(attrs)

	if len(attrs) != 4 {
		t.Fatalf("expected 4 attributes, got %d", len(attrs))
	}

	want := map[string]string{
		"xylem.vessel.id":       "vessel-123",
		"xylem.vessel.source":   "github",
		"xylem.vessel.workflow": "fix-bug",
		"xylem.vessel.ref":      "refs/pull/42/head",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestSmoke_S2_VesselSpanAttributesOmitsEmptyRef(t *testing.T) {
	attrs := VesselSpanAttributes(VesselSpanData{
		ID:       "vessel-456",
		Source:   "manual",
		Workflow: "implement-feature",
	})
	got := attrMap(attrs)

	if len(attrs) != 3 {
		t.Fatalf("expected 3 attributes, got %d", len(attrs))
	}
	if _, ok := got["xylem.vessel.ref"]; ok {
		t.Fatal("did not expect xylem.vessel.ref attribute when ref is empty")
	}
}

func TestSmoke_S3_PhaseSpanAttributesIncludesAllFive(t *testing.T) {
	attrs := PhaseSpanAttributes(PhaseSpanData{
		Name:     "analyse",
		Index:    0,
		Type:     "prompt",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
	})
	got := attrMap(attrs)

	if len(attrs) != 5 {
		t.Fatalf("expected 5 attributes, got %d", len(attrs))
	}

	want := map[string]string{
		"xylem.phase.name":     "analyse",
		"xylem.phase.index":    "0",
		"xylem.phase.type":     "prompt",
		"xylem.phase.provider": "anthropic",
		"xylem.phase.model":    "claude-sonnet-4-20250514",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestSmoke_S4_PhaseResultAttributesFormatsTokensAndCost(t *testing.T) {
	attrs := PhaseResultAttributes(PhaseResultData{
		InputTokensEst:  1200,
		OutputTokensEst: 300,
		CostUSDEst:      0.0081,
		DurationMS:      4500,
	})
	got := attrMap(attrs)

	if len(attrs) != 4 {
		t.Fatalf("expected 4 attributes, got %d", len(attrs))
	}

	want := map[string]string{
		"xylem.phase.input_tokens_est":  "1200",
		"xylem.phase.output_tokens_est": "300",
		"xylem.phase.cost_usd_est":      "0.008100",
		"xylem.phase.duration_ms":       "4500",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestSmoke_S5_GateSpanAttributesFormatsBooleanAndIntAsStrings(t *testing.T) {
	attrs := GateSpanAttributes(GateSpanData{
		Type:         "command",
		Passed:       true,
		RetryAttempt: 2,
	})
	got := attrMap(attrs)

	if len(attrs) != 3 {
		t.Fatalf("expected 3 attributes, got %d", len(attrs))
	}
	if got["xylem.gate.type"] != "command" {
		t.Fatalf("xylem.gate.type = %q, want %q", got["xylem.gate.type"], "command")
	}
	if got["xylem.gate.passed"] != "true" {
		t.Fatalf("xylem.gate.passed = %q, want %q", got["xylem.gate.passed"], "true")
	}
	if got["xylem.gate.passed"] == "True" || got["xylem.gate.passed"] == "1" {
		t.Fatalf("xylem.gate.passed should be lowercase bool string, got %q", got["xylem.gate.passed"])
	}
	if got["xylem.gate.retry_attempt"] != "2" {
		t.Fatalf("xylem.gate.retry_attempt = %q, want %q", got["xylem.gate.retry_attempt"], "2")
	}
}

func TestVesselSpanAttributesAllowsEmptyCoreFields(t *testing.T) {
	attrs := VesselSpanAttributes(VesselSpanData{})
	got := attrMap(attrs)

	if len(attrs) != 3 {
		t.Fatalf("expected 3 attributes, got %d", len(attrs))
	}
	if got["xylem.vessel.id"] != "" || got["xylem.vessel.source"] != "" || got["xylem.vessel.workflow"] != "" {
		t.Fatalf("expected empty values to be preserved, got %#v", got)
	}
}

func TestPhaseResultAttributesFormatsNegativeDuration(t *testing.T) {
	attrs := PhaseResultAttributes(PhaseResultData{
		InputTokensEst:  1,
		OutputTokensEst: 2,
		CostUSDEst:      3.5,
		DurationMS:      -1,
	})
	got := attrMap(attrs)

	if got["xylem.phase.duration_ms"] != "-1" {
		t.Fatalf("xylem.phase.duration_ms = %q, want %q", got["xylem.phase.duration_ms"], "-1")
	}
	if got["xylem.phase.cost_usd_est"] != "3.500000" {
		t.Fatalf("xylem.phase.cost_usd_est = %q, want %q", got["xylem.phase.cost_usd_est"], "3.500000")
	}
}
