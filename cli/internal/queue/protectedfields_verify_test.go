package queue

// Lightweight-verification tests for protectedFieldsEqual (queue.go:98) and
// its helpers timePtrEqual (line 124) and stringMapEqual (line 131).
//
// These are the Phase 3 artifacts for roadmap #06. Full formal verification
// (Gobra) is deferred to #10. See cli/internal/queue/verified/protectedfields_verify.md
// for the complete contract analysis and verification gap notes.

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// baseTerminalVessel returns a fully-populated terminal vessel with every
// I2-protected field set to a non-zero value, suitable for mutation testing.
func baseTerminalVessel() Vessel {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)
	return Vessel{
		ID:             "lv-base-id",
		Source:         "github",
		Ref:            "https://github.com/example/repo/issues/99",
		Workflow:       "fix-bug",
		WorkflowDigest: "sha256:deadbeef",
		WorkflowClass:  "fix-bug",
		Tier:           "high",
		Prompt:         "fix the bug",
		Meta:           map[string]string{"meta-key": "meta-val"},
		State:          StateCompleted,
		CreatedAt:      t0,
		StartedAt:      &t0,
		EndedAt:        &t1,
		Error:          "",
		CurrentPhase:   3,
		PhaseOutputs:   map[string]string{"plan": "ok", "implement": "done"},
		GateRetries:    2,
		WaitingSince:   &t2,
		WaitingFor:     "review-label",
		WorktreePath:   "/tmp/xylem-worktree/lv-base-id",
		FailedPhase:    "",
		GateOutput:     "PASS",
		RetryOf:        "lv-original-id",
	}
}

// TestProtectedFieldsEqual_EachI2FieldMutationReturnsFalse checks C1:
// mutating any single I2-protected field makes protectedFieldsEqual return
// false. This catches field-omission regressions when new protected fields
// are added to the struct.
func TestProtectedFieldsEqual_EachI2FieldMutationReturnsFalse(t *testing.T) {
	alt := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		field  string
		mutate func(v *Vessel)
	}{
		{"State", func(v *Vessel) { v.State = StateFailed }},
		{"Ref", func(v *Vessel) { v.Ref = "https://github.com/x/y/issues/999" }},
		{"Source", func(v *Vessel) { v.Source = "manual" }},
		{"Workflow", func(v *Vessel) { v.Workflow = "implement-feature" }},
		{"WorkflowDigest", func(v *Vessel) { v.WorkflowDigest = "sha256:different" }},
		{"WorkflowClass", func(v *Vessel) { v.WorkflowClass = "implement-feature" }},
		{"Tier", func(v *Vessel) { v.Tier = "low" }},
		{"RetryOf", func(v *Vessel) { v.RetryOf = "different-id" }},
		{"Error", func(v *Vessel) { v.Error = "tampered error" }},
		{"CurrentPhase", func(v *Vessel) { v.CurrentPhase = 99 }},
		{"GateRetries", func(v *Vessel) { v.GateRetries = 99 }},
		{"WaitingFor", func(v *Vessel) { v.WaitingFor = "tampered" }},
		{"WorktreePath", func(v *Vessel) { v.WorktreePath = "/tampered" }},
		{"FailedPhase", func(v *Vessel) { v.FailedPhase = "tampered-phase" }},
		{"GateOutput", func(v *Vessel) { v.GateOutput = "FAIL" }},
		{"StartedAt", func(v *Vessel) { v.StartedAt = &alt }},
		{"EndedAt", func(v *Vessel) { v.EndedAt = &alt }},
		{"WaitingSince", func(v *Vessel) { v.WaitingSince = &alt }},
		{"PhaseOutputs", func(v *Vessel) { v.PhaseOutputs = map[string]string{"x": "y"} }},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			a := baseTerminalVessel()
			b := baseTerminalVessel()
			tc.mutate(&b)
			if protectedFieldsEqual(a, b) {
				t.Errorf("returned true after mutating I2-protected field %s — omission regression", tc.field)
			}
		})
	}
}

// TestProtectedFieldsEqual_ExcludedFieldsDontAffectResult checks C10/C11:
// mutating ID, Prompt, Meta, or CreatedAt must not change the result.
// These are intentionally excluded from I2 (see protectedfields_verify.md).
func TestProtectedFieldsEqual_ExcludedFieldsDontAffectResult(t *testing.T) {
	newTime := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		field  string
		mutate func(v *Vessel)
	}{
		{"ID", func(v *Vessel) { v.ID = "completely-different-id" }},
		{"Prompt", func(v *Vessel) { v.Prompt = "tampered prompt" }},
		{"Meta_changed", func(v *Vessel) { v.Meta = map[string]string{"tampered": "meta"} }},
		{"Meta_nil", func(v *Vessel) { v.Meta = nil }},
		{"CreatedAt", func(v *Vessel) { v.CreatedAt = newTime }},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			a := baseTerminalVessel()
			b := baseTerminalVessel()
			tc.mutate(&b)
			if !protectedFieldsEqual(a, b) {
				t.Errorf("returned false after mutating excluded field %s — this field must not affect I2 comparison", tc.field)
			}
		})
	}
}

// TestProtectedFieldsEqual_Reflexive checks C2: any vessel equals itself.
func TestProtectedFieldsEqual_Reflexive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := drawFreshVessel(t)
		if !protectedFieldsEqual(v, v) {
			t.Fatal("protectedFieldsEqual(v, v) returned false — violates reflexivity")
		}
	})
}

// TestProtectedFieldsEqual_Symmetric checks C3: argument order must not matter.
func TestProtectedFieldsEqual_Symmetric(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := drawFreshVessel(t)
		b := drawFreshVessel(t)
		if protectedFieldsEqual(a, b) != protectedFieldsEqual(b, a) {
			t.Fatal("protectedFieldsEqual not symmetric")
		}
	})
}

// TestTimePtrEqual checks contracts C5–C7 for the *time.Time comparison helper.
func TestTimePtrEqual(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	later := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	// Same instant, different timezones — time.Equal is zone-agnostic.
	utcTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	estTime := time.Date(2026, 1, 1, 7, 0, 0, 0, time.FixedZone("EST", -5*3600))

	cases := []struct {
		name string
		a, b *time.Time
		want bool
	}{
		{"both nil", nil, nil, true},
		{"a nil b set", nil, &now, false},
		{"a set b nil", &now, nil, false},
		{"same instant same ptr", &now, &now, true},
		{"same instant different ptr", &utcTime, &estTime, true},
		{"different instants", &now, &later, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := timePtrEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("timePtrEqual = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStringMapEqual checks contracts C8–C9 for the map[string]string helper.
func TestStringMapEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"nil vs empty", nil, map[string]string{}, true},
		{"empty vs nil", map[string]string{}, nil, true},
		{"both empty", map[string]string{}, map[string]string{}, true},
		{"equal single entry", map[string]string{"k": "v"}, map[string]string{"k": "v"}, true},
		{"equal multi entry", map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "1", "b": "2"}, true},
		{"key added in b", map[string]string{"k": "v"}, map[string]string{"k": "v", "x": "y"}, false},
		{"key removed in b", map[string]string{"k": "v", "x": "y"}, map[string]string{"k": "v"}, false},
		{"value changed", map[string]string{"k": "v"}, map[string]string{"k": "different"}, false},
		{"key mismatch same length", map[string]string{"k": "v"}, map[string]string{"other": "v"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stringMapEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("stringMapEqual = %v, want %v", got, tc.want)
			}
		})
	}
}
