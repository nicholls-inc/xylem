package queue

// Differential tests: verified functions must agree with the original Go
// implementations for all canonical inputs. These are abstraction-gap checks —
// same result from Dafny-extracted Go as from the original inline logic.
//
// Lives in package queue (internal) so it can reference unexported types and
// vars (VesselState.IsTerminal, validTransitions map). The wiring PR will flip
// the dependency direction; until then queue does not import verified.

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue/verified"
)

func TestIsTerminal_DifferentialWithVerified(t *testing.T) {
	canonical := []string{
		"pending",
		"running",
		"completed",
		"failed",
		"cancelled",
		"waiting",
		"timed_out",
	}
	for _, s := range canonical {
		want := VesselState(s).IsTerminal()
		got := verified.IsTerminal(s)
		if got != want {
			t.Errorf("state %q: VesselState.IsTerminal()=%v, verified.IsTerminal()=%v", s, want, got)
		}
	}
}

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
	// Test unknown from-state: map returns false (nil inner map), verified returns false.
	for _, to := range canonical {
		want := validTransitions["unknown"][VesselState(to)]
		got := verified.ValidTransition("unknown", to)
		if got != want {
			t.Errorf("ValidTransition(%q, %q): map=%v, verified=%v", "unknown", to, want, got)
		}
	}
	// Test unknown to-state with each known from-state: map returns false, verified returns false.
	for _, from := range canonical {
		want := validTransitions[VesselState(from)]["unknown"]
		got := verified.ValidTransition(from, "unknown")
		if got != want {
			t.Errorf("ValidTransition(%q, %q): map=%v, verified=%v", from, "unknown", want, got)
		}
	}
}
