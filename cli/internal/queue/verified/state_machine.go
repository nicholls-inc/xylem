// CI test: no-.dfy change to exercise verify-kernels skip path (PR #691 test plan item 3).
// Derived from state_machine.dfy; DO NOT EDIT by hand.
// Verified by Dafny 4.11.0 — 2 verified, 0 errors.
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

// ValidTransition reports whether transitioning from vessel state `from` to `to` is permitted.
//
// Verified postcondition:
//
//	ValidTransition(from, to) <==> (from, to) is one of the allowed transition pairs:
//	  "pending"   → {"running", "cancelled"}
//	  "running"   → {"pending", "completed", "failed", "cancelled", "waiting", "timed_out"}
//	  "waiting"   → {"pending", "timed_out", "cancelled"}
//	  "failed"    → {"pending"}
//	  "completed", "cancelled", "timed_out" → {} (no outgoing transitions)
//
// Dafny source: ValidTransition(from, to: VesselState): bool in state_machine.dfy.
// Caller contract: from and to must be canonical VesselState string values.
// An unknown from-state returns false; an unknown to-state returns false.
func ValidTransition(from, to string) bool {
	switch from {
	case "pending":
		return to == "running" || to == "cancelled"
	case "running":
		return to == "pending" || to == "completed" || to == "failed" ||
			to == "cancelled" || to == "waiting" || to == "timed_out"
	case "waiting":
		return to == "pending" || to == "timed_out" || to == "cancelled"
	case "failed":
		return to == "pending"
	default:
		return false
	}
}
