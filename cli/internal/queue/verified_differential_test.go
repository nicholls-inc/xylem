package queue

// Differential tests: verified functions must agree with the hand-enumerated
// truth tables below and with the validTransitions map (used by property tests).
//
// After wiring, VesselState.IsTerminal() delegates to verified.IsTerminal, so a
// cross-check between the two would be tautological. TestIsTerminal_TruthTable
// instead checks the verified function (and its delegate) against an independent
// enumeration so a future regression in either direction is caught. The
// ValidTransition differential test remains meaningful: production code calls
// verified.ValidTransition while queue_invariants_prop_test.go checks the
// validTransitions map — these two sources of truth must stay consistent.

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue/verified"
)

func TestIsTerminal_TruthTable(t *testing.T) {
	want := map[string]bool{
		"pending":   false,
		"running":   false,
		"waiting":   false,
		"failed":    true,
		"completed": true,
		"cancelled": true,
		"timed_out": true,
	}
	for s, expected := range want {
		if got := VesselState(s).IsTerminal(); got != expected {
			t.Errorf("VesselState(%q).IsTerminal() = %v, want %v", s, got, expected)
		}
		if got := verified.IsTerminal(s); got != expected {
			t.Errorf("verified.IsTerminal(%q) = %v, want %v", s, got, expected)
		}
	}
}

// TestValidTransition_DifferentialWithMap guards that the validTransitions map
// (the oracle for queue_invariants_prop_test.go) and verified.ValidTransition
// (the production implementation after wiring) remain in sync. A divergence here
// means property tests are exercising a different state machine than production.
func TestValidTransition_DifferentialWithMap(t *testing.T) {
	canonical := []string{
		"pending",
		"running",
		"completed",
		"failed",
		"cancelled",
		"waiting",
		"timed_out",
	}
	// Test all 49 canonical pairs.
	for _, from := range canonical {
		for _, to := range canonical {
			want := validTransitions[VesselState(from)][VesselState(to)]
			got := verified.ValidTransition(from, to)
			if got != want {
				t.Errorf("ValidTransition(%q, %q): map=%v, verified=%v", from, to, want, got)
			}
		}
	}
	// Unknown from-state: map returns false (nil inner map), verified returns false.
	for _, to := range canonical {
		want := validTransitions["unknown"][VesselState(to)]
		got := verified.ValidTransition("unknown", to)
		if got != want {
			t.Errorf("ValidTransition(%q, %q): map=%v, verified=%v", "unknown", to, want, got)
		}
	}
	// Unknown to-state: map returns false, verified returns false.
	for _, from := range canonical {
		want := validTransitions[VesselState(from)]["unknown"]
		got := verified.ValidTransition(from, "unknown")
		if got != want {
			t.Errorf("ValidTransition(%q, %q): map=%v, verified=%v", from, "unknown", want, got)
		}
	}
}
