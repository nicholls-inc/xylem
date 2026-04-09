package observability

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestSmoke_S3_PhaseSpanAttributesIncludesAllFields(t *testing.T) {
	attrs := PhaseSpanAttributes(PhaseSpanData{
		Name:         "analyse",
		Index:        0,
		Type:         "prompt",
		Workflow:     "fix-bug",
		Provider:     "anthropic",
		Model:        "claude-sonnet-4-20250514",
		RetryAttempt: 2,
		SandboxMode:  "dangerously-skip-permissions",
	})
	got := attrMap(attrs)

	if len(attrs) != 8 {
		t.Fatalf("expected 8 attributes, got %d", len(attrs))
	}

	want := map[string]string{
		"xylem.phase.name":          "analyse",
		"xylem.phase.index":         "0",
		"xylem.phase.type":          "prompt",
		"xylem.phase.workflow":      "fix-bug",
		"xylem.phase.provider":      "anthropic",
		"xylem.phase.model":         "claude-sonnet-4-20250514",
		"xylem.phase.retry_attempt": "2",
		"xylem.phase.sandbox_mode":  "dangerously-skip-permissions",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestSmoke_S4_PhaseResultAttributesFormatsTokensAndCost(t *testing.T) {
	attrs := PhaseResultAttributes(PhaseResultData{
		InputTokensEst:     1200,
		OutputTokensEst:    300,
		CostUSDEst:         0.0081,
		DurationMS:         4500,
		Status:             "completed",
		OutputArtifactPath: "phases/vessel-1/analyse.output",
	})
	got := attrMap(attrs)

	if len(attrs) != 6 {
		t.Fatalf("expected 6 attributes, got %d", len(attrs))
	}

	want := map[string]string{
		"xylem.phase.input_tokens_est":     "1200",
		"xylem.phase.output_tokens_est":    "300",
		"xylem.phase.cost_usd_est":         "0.008100",
		"xylem.phase.duration_ms":          "4500",
		"xylem.phase.status":               "completed",
		"xylem.phase.output_artifact_path": "phases/vessel-1/analyse.output",
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

func TestGateStepSpanAttributesExposeStepMetadata(t *testing.T) {
	attrs := GateStepSpanAttributes(GateStepSpanData{
		Name:   "health",
		Mode:   "http",
		Passed: true,
	})
	got := attrMap(attrs)

	if len(attrs) != 3 {
		t.Fatalf("expected 3 attributes, got %d", len(attrs))
	}
	if got["xylem.gate.step.name"] != "health" {
		t.Fatalf("xylem.gate.step.name = %q, want %q", got["xylem.gate.step.name"], "health")
	}
	if got["xylem.gate.step.mode"] != "http" {
		t.Fatalf("xylem.gate.step.mode = %q, want %q", got["xylem.gate.step.mode"], "http")
	}
	if got["xylem.gate.step.passed"] != "true" {
		t.Fatalf("xylem.gate.step.passed = %q, want %q", got["xylem.gate.step.passed"], "true")
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

func TestVesselHealthAttributesIncludeDerivedHealth(t *testing.T) {
	attrs := VesselHealthAttributes(VesselHealthData{
		State:        "failed",
		Health:       "unhealthy",
		AnomalyCount: 2,
		Anomalies:    []string{"run_failed", "budget_exceeded"},
	})
	got := attrMap(attrs)

	if got["xylem.vessel.state"] != "failed" {
		t.Fatalf("xylem.vessel.state = %q, want failed", got["xylem.vessel.state"])
	}
	if got["xylem.vessel.health"] != "unhealthy" {
		t.Fatalf("xylem.vessel.health = %q, want unhealthy", got["xylem.vessel.health"])
	}
	if got["xylem.vessel.anomaly_count"] != "2" {
		t.Fatalf("xylem.vessel.anomaly_count = %q, want 2", got["xylem.vessel.anomaly_count"])
	}
	if got["xylem.vessel.anomalies"] != "run_failed,budget_exceeded" {
		t.Fatalf("xylem.vessel.anomalies = %q, want joined anomalies", got["xylem.vessel.anomalies"])
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
	if got["xylem.phase.status"] != "" {
		t.Fatalf("xylem.phase.status = %q, want empty string", got["xylem.phase.status"])
	}
}

func TestDrainSpanAttributes_FormatAllFields(t *testing.T) {
	tests := []struct {
		name        string
		concurrency int
		timeout     string
	}{
		{name: "typical values", concurrency: 4, timeout: "30m"},
		{name: "two digit concurrency", concurrency: 17, timeout: "90s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := DrainSpanAttributes(DrainSpanData{
				Concurrency: tt.concurrency,
				Timeout:     tt.timeout,
			})
			got := attrMap(attrs)

			if len(attrs) != 2 {
				t.Fatalf("expected 2 attributes, got %d", len(attrs))
			}
			wantConcurrency := strconv.Itoa(tt.concurrency)
			if got["xylem.drain.concurrency"] != wantConcurrency {
				t.Fatalf("xylem.drain.concurrency = %q, want %q", got["xylem.drain.concurrency"], wantConcurrency)
			}
			if got["xylem.drain.timeout"] != tt.timeout {
				t.Fatalf("xylem.drain.timeout = %q, want %q", got["xylem.drain.timeout"], tt.timeout)
			}
		})
	}
}

func TestDrainHealthAttributesIncludePatterns(t *testing.T) {
	attrs := DrainHealthAttributes(DrainHealthData{
		Healthy:   1,
		Degraded:  2,
		Unhealthy: 3,
		Patterns:  "budget_exceeded=2, run_failed=1",
	})
	got := attrMap(attrs)

	if got["xylem.drain.healthy_vessels"] != "1" {
		t.Fatalf("xylem.drain.healthy_vessels = %q, want 1", got["xylem.drain.healthy_vessels"])
	}
	if got["xylem.drain.degraded_vessels"] != "2" {
		t.Fatalf("xylem.drain.degraded_vessels = %q, want 2", got["xylem.drain.degraded_vessels"])
	}
	if got["xylem.drain.unhealthy_vessels"] != "3" {
		t.Fatalf("xylem.drain.unhealthy_vessels = %q, want 3", got["xylem.drain.unhealthy_vessels"])
	}
	if got["xylem.drain.unhealthy_patterns"] != "budget_exceeded=2, run_failed=1" {
		t.Fatalf("xylem.drain.unhealthy_patterns = %q, want joined patterns", got["xylem.drain.unhealthy_patterns"])
	}
}

func TestSmoke_S6_RecoveryAttributesIncludeDecisionFields(t *testing.T) {
	attrs := RecoveryAttributes(RecoveryData{
		Class:           "spec_gap",
		Action:          "refine",
		RetrySuppressed: "true",
		RetryOutcome:    "suppressed",
		UnlockDimension: "decision",
	})
	got := attrMap(attrs)

	require.Len(t, attrs, 5)
	assert.Equal(t, "spec_gap", got["xylem.recovery.class"])
	assert.Equal(t, "refine", got["xylem.recovery.action"])
	assert.Equal(t, "true", got["xylem.recovery.retry_suppressed"])
	assert.Equal(t, "suppressed", got["xylem.recovery.retry_outcome"])
	assert.Equal(t, "decision", got["xylem.recovery.unlock_dimension"])
}

func TestSmoke_S7_RecoveryAttributesOmitEmptyRecoveryData(t *testing.T) {
	attrs := RecoveryAttributes(RecoveryData{})
	assert.Nil(t, attrs)
}
