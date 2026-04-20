package verified_test

// Property tests for state_machine.go, verifying that the extracted Go
// matches the spec's postcondition for every possible VesselState string.
//
// These tests are the abstraction-gap bridge: Dafny proved the spec over its
// own datatype; these tests confirm the string-typed Go extraction preserves
// that proof for all seven canonical values.

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue/verified"
	"pgregory.net/rapid"
)

// allStates enumerates the seven canonical VesselState string values,
// mirroring the Dafny datatype constructors in state_machine.dfy.
var allStates = []string{
	"pending",
	"running",
	"completed",
	"failed",
	"cancelled",
	"waiting",
	"timed_out",
}

// terminalStates is the set Dafny proved are terminal.
var terminalStates = map[string]bool{
	"completed": true,
	"failed":    true,
	"cancelled": true,
	"timed_out": true,
}

// TestIsTerminal_AllCanonicalValues checks the postcondition exhaustively over
// the seven canonical states. These are the exact inputs the Dafny spec covers.
func TestIsTerminal_AllCanonicalValues(t *testing.T) {
	for _, s := range allStates {
		got := verified.IsTerminal(s)
		want := terminalStates[s]
		if got != want {
			t.Errorf("IsTerminal(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestIsTerminal_UnknownStringIsFalse checks that any string outside the
// canonical set returns false. The verified spec is defined only over the
// seven constructors; the Go extraction treats unknown strings as non-terminal.
func TestIsTerminal_UnknownStringIsFalse(t *testing.T) {
	// Rapid property: any string not in the canonical set must return false.
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.StringMatching(`[a-z_]{1,20}`).Draw(t, "s")
		if terminalStates[s] {
			t.Skip() // in the canonical set; handled by exhaustive test above
		}
		for _, canonical := range allStates {
			if s == canonical {
				t.Skip()
			}
		}
		if verified.IsTerminal(s) {
			t.Fatalf("IsTerminal(%q) = true for non-canonical string", s)
		}
	})
}
