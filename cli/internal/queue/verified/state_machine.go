// Derived from state_machine.dfy; DO NOT EDIT by hand.
// Verified by Dafny 4.11.0 — 1 verified, 0 errors.
// To regenerate: compile state_machine.dfy to Go, then strip _dafny.* boilerplate
// and map Dafny discriminated-union equality to string comparisons per the type
// mapping table in README.md.
//
// The verified postcondition is preserved as a doc-comment on each function.
package verified

// IsTerminal reports whether s is a terminal vessel state.
//
// Verified postcondition:
//
//	IsTerminal(s) <==> s is one of {"completed", "failed", "cancelled", "timed_out"}
//
// Dafny source: IsTerminal(s: VesselState): bool in state_machine.dfy.
// Caller contract: s must be one of the seven canonical VesselState string values.
// Passing an unrecognised string returns false (not-terminal), consistent with the spec.
func IsTerminal(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled" || s == "timed_out"
}
