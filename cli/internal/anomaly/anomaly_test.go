package anomaly

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// --- DetectFromVessel unit tests ---

func TestDetectGateExhausted(t *testing.T) {
	v := queue.Vessel{
		ID:          "v-gate",
		State:       queue.StateFailed,
		GateRetries: 0,
		FailedPhase: "implement",
		GateOutput:  "tests failed: 3 failures",
	}
	anomalies := DetectFromVessel(v)
	if len(anomalies) == 0 {
		t.Fatal("expected at least one anomaly, got none")
	}
	found := false
	for _, a := range anomalies {
		if a.Type == AnomalyGateExhausted {
			found = true
			if a.Severity != SeverityCritical {
				t.Errorf("expected Critical severity, got %s", a.Severity)
			}
			if a.VesselID != v.ID {
				t.Errorf("expected VesselID=%q, got %q", v.ID, a.VesselID)
			}
		}
	}
	if !found {
		t.Error("expected AnomalyGateExhausted in results")
	}
}

func TestDetectLabelTimeout(t *testing.T) {
	waitSince := time.Now().Add(-25 * time.Hour)
	v := queue.Vessel{
		ID:           "v-timeout",
		State:        queue.StateTimedOut,
		WaitingFor:   "approved",
		WaitingSince: &waitSince,
	}
	anomalies := DetectFromVessel(v)
	found := false
	for _, a := range anomalies {
		if a.Type == AnomalyLabelTimeout {
			found = true
			if a.Severity != SeverityCritical {
				t.Errorf("expected Critical severity, got %s", a.Severity)
			}
			if a.VesselID != v.ID {
				t.Errorf("expected VesselID=%q, got %q", v.ID, a.VesselID)
			}
		}
	}
	if !found {
		t.Error("expected AnomalyLabelTimeout in results")
	}
}

func TestDetectPolicyDenial_GateOutput(t *testing.T) {
	v := queue.Vessel{
		ID:         "v-policy",
		State:      queue.StateFailed,
		GateOutput: "Tool call disallowed by policy configuration",
	}
	anomalies := DetectFromVessel(v)
	found := false
	for _, a := range anomalies {
		if a.Type == AnomalyPolicyDenial {
			found = true
			if a.Severity != SeverityWarning {
				t.Errorf("expected Warning severity, got %s", a.Severity)
			}
		}
	}
	if !found {
		t.Error("expected AnomalyPolicyDenial in results")
	}
}

func TestDetectPolicyDenial_Error(t *testing.T) {
	v := queue.Vessel{
		ID:    "v-perm",
		State: queue.StateFailed,
		Error: "permission denied by administrator",
	}
	anomalies := DetectFromVessel(v)
	found := false
	for _, a := range anomalies {
		if a.Type == AnomalyPolicyDenial {
			found = true
		}
	}
	if !found {
		t.Error("expected AnomalyPolicyDenial in results")
	}
}

func TestDetectPolicyDenial_CaseInsensitive(t *testing.T) {
	v := queue.Vessel{
		ID:    "v-case",
		State: queue.StateFailed,
		Error: "PERMISSION DENIED: operation not permitted",
	}
	anomalies := DetectFromVessel(v)
	found := false
	for _, a := range anomalies {
		if a.Type == AnomalyPolicyDenial {
			found = true
		}
	}
	if !found {
		t.Error("expected AnomalyPolicyDenial for uppercase denial keyword")
	}
}

func TestDetectNone_Completed(t *testing.T) {
	v := queue.Vessel{
		ID:          "v-ok",
		State:       queue.StateCompleted,
		GateRetries: 2,
	}
	anomalies := DetectFromVessel(v)
	if len(anomalies) != 0 {
		t.Errorf("expected no anomalies for completed vessel, got %d: %+v", len(anomalies), anomalies)
	}
}

func TestDetectNone_FailedNoGate(t *testing.T) {
	v := queue.Vessel{
		ID:    "v-builderr",
		State: queue.StateFailed,
		Error: "build error: undefined: foo",
	}
	anomalies := DetectFromVessel(v)
	if len(anomalies) != 0 {
		t.Errorf("expected no anomalies for non-gate failure, got %d: %+v", len(anomalies), anomalies)
	}
}

func TestDetectPolicyDenial_OnlyOnce(t *testing.T) {
	// Even if multiple keywords match, PolicyDenial should appear only once.
	v := queue.Vessel{
		ID:         "v-multi",
		State:      queue.StateFailed,
		GateOutput: "permission denied and also not allowed",
	}
	anomalies := DetectFromVessel(v)
	count := 0
	for _, a := range anomalies {
		if a.Type == AnomalyPolicyDenial {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 PolicyDenial anomaly, got %d", count)
	}
}

// --- Emit + Read round-trip tests ---

func TestEmitReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second) // truncate for JSON round-trip

	input := []Anomaly{
		{Type: AnomalyGateExhausted, Severity: SeverityCritical, VesselID: "v-1", Timestamp: now, Detail: "retries exhausted"},
		{Type: AnomalyLabelTimeout, Severity: SeverityCritical, VesselID: "v-2", Timestamp: now, Detail: "timed out"},
	}

	if err := Emit(dir, input); err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if len(got) != len(input) {
		t.Fatalf("expected %d records, got %d", len(input), len(got))
	}
	for i, a := range got {
		if a.Type != input[i].Type {
			t.Errorf("[%d] Type: got %q, want %q", i, a.Type, input[i].Type)
		}
		if a.Severity != input[i].Severity {
			t.Errorf("[%d] Severity: got %q, want %q", i, a.Severity, input[i].Severity)
		}
		if a.VesselID != input[i].VesselID {
			t.Errorf("[%d] VesselID: got %q, want %q", i, a.VesselID, input[i].VesselID)
		}
		if a.Detail != input[i].Detail {
			t.Errorf("[%d] Detail: got %q, want %q", i, a.Detail, input[i].Detail)
		}
	}
}

func TestEmitAppends(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	a1 := []Anomaly{{Type: AnomalyGateExhausted, VesselID: "v-1", Timestamp: now}}
	a2 := []Anomaly{{Type: AnomalyLabelTimeout, VesselID: "v-2", Timestamp: now}}

	if err := Emit(dir, a1); err != nil {
		t.Fatalf("first Emit: %v", err)
	}
	if err := Emit(dir, a2); err != nil {
		t.Fatalf("second Emit: %v", err)
	}

	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 records after two Emits, got %d", len(got))
	}
}

func TestEmitNoOp_Empty(t *testing.T) {
	dir := t.TempDir()
	if err := Emit(dir, nil); err != nil {
		t.Fatalf("Emit(nil) should be no-op, got: %v", err)
	}
	// File should not exist.
	if _, err := os.Stat(filepath.Join(dir, "anomalies.jsonl")); !os.IsNotExist(err) {
		t.Error("expected anomalies.jsonl to not exist after no-op Emit")
	}
}

func TestReadMissingFile(t *testing.T) {
	dir := t.TempDir()
	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read on missing file should return no error, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d records", len(got))
	}
}

func TestReadMalformedLines(t *testing.T) {
	dir := t.TempDir()
	// Write one valid and one malformed line.
	path := filepath.Join(dir, "anomalies.jsonl")
	valid := Anomaly{Type: AnomalyGateExhausted, VesselID: "v-x", Timestamp: time.Now().UTC()}
	b, _ := json.Marshal(valid)
	content := string(b) + "\nnot-valid-json\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 valid record, got %d", len(got))
	}
}

func TestEmitNonExistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	err := Emit(dir, []Anomaly{{Type: AnomalyGateExhausted, VesselID: "x", Timestamp: time.Now()}})
	if err == nil {
		t.Error("expected error when emitting to non-existent dir")
	}
}

// --- Property-based tests ---

func TestPropDetectFromVessel_NoCrash(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		v := queue.Vessel{
			ID:          rapid.String().Draw(rt, "id"),
			State:       queue.VesselState(rapid.SampledFrom([]string{"pending", "running", "completed", "failed", "cancelled", "waiting", "timed_out"}).Draw(rt, "state")),
			GateRetries: rapid.Int().Draw(rt, "gate_retries"),
			FailedPhase: rapid.String().Draw(rt, "failed_phase"),
			GateOutput:  rapid.String().Draw(rt, "gate_output"),
			Error:       rapid.String().Draw(rt, "error"),
			WaitingFor:  rapid.String().Draw(rt, "waiting_for"),
		}

		anomalies := DetectFromVessel(v)

		// All returned anomalies must carry the same VesselID.
		for _, a := range anomalies {
			if a.VesselID != v.ID {
				rt.Fatalf("anomaly VesselID=%q != vessel.ID=%q", a.VesselID, v.ID)
			}
		}

		// Timestamp must not be zero.
		for _, a := range anomalies {
			if a.Timestamp.IsZero() {
				rt.Fatal("anomaly Timestamp is zero")
			}
		}
	})
}
