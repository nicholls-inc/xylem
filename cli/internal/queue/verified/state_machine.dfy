// state_machine.dfy — hand-written Dafny spec for the queue state machine kernel.
// Source of truth for cli/internal/queue/verified/state_machine.go.
//
// Verified by: Dafny 4.11.0 (mcp__plugin_crosscheck_dafny__dafny_verify: 1 verified, 0 errors)
// Extracted to: state_machine.go via crosscheck:extract-code
//
// To re-verify: run mcp__plugin_crosscheck_dafny__dafny_verify on this file.
// To re-extract: run the crosscheck:extract-code skill targeting Go.
//
// Scope: IsTerminal only (first kernel — pipeline proof-of-concept).
// Future targets: validTransitions, protectedFieldsEqual (roadmap #06).

// VesselState mirrors the Go enum in cli/internal/queue/queue.go.
// Constructor names map to Go string constants:
//   Pending   → "pending"
//   Running   → "running"
//   Completed → "completed"
//   Failed    → "failed"
//   Cancelled → "cancelled"
//   Waiting   → "waiting"
//   TimedOut  → "timed_out"
datatype VesselState =
  | Pending
  | Running
  | Completed
  | Failed
  | Cancelled
  | Waiting
  | TimedOut

// IsTerminal returns true iff the state is one of the four terminal states.
// Mirrors (VesselState).IsTerminal() in queue.go:82-84.
//
// Formally verified: for every VesselState constructor, the function returns
// true iff the constructor is one of {Completed, Failed, Cancelled, TimedOut}.
function IsTerminal(s: VesselState): bool
  ensures IsTerminal(s) <==> (s == Completed || s == Failed || s == Cancelled || s == TimedOut)
{
  s == Completed || s == Failed || s == Cancelled || s == TimedOut
}
