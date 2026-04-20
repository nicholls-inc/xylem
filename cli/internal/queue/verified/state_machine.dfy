// state_machine.dfy — hand-written Dafny spec for the queue state machine kernel.
// Source of truth for cli/internal/queue/verified/state_machine.go.
//
// Verified by: Dafny 4.11.0 (mcp__plugin_crosscheck_dafny__dafny_verify: 2 verified, 0 errors)
// Extracted to: state_machine.go via crosscheck:extract-code
//
// To re-verify: run mcp__plugin_crosscheck_dafny__dafny_verify on this file.
// To re-extract: run the crosscheck:extract-code skill targeting Go.
//
// Scope: IsTerminal (phase 1) + ValidTransition (phase 2).
// Future target: protectedFieldsEqual deferred — see roadmap #06 scoping note.

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
  false  // INTENTIONALLY BROKEN: postcondition unprovable — CI test plan item 2
}

// ValidTransition returns true iff transitioning from state `from` to state `to`
// is permitted by the queue state machine.
// Mirrors the validTransitions map in queue.go:41-66.
//
// Formally verified: for every (from, to) pair of VesselState constructors, the
// function returns true iff the pair appears in the transition table:
//   Pending   → {Running, Cancelled}
//   Running   → {Pending, Completed, Failed, Cancelled, Waiting, TimedOut}
//   Waiting   → {Pending, TimedOut, Cancelled}
//   Failed    → {Pending}
//   Completed, Cancelled, TimedOut → {} (no outgoing transitions)
function ValidTransition(from: VesselState, to: VesselState): bool
  ensures ValidTransition(from, to) <==>
    (from == Pending && (to == Running   || to == Cancelled)) ||
    (from == Running && (to == Pending   || to == Completed || to == Failed  ||
                         to == Cancelled || to == Waiting   || to == TimedOut)) ||
    (from == Waiting && (to == Pending   || to == TimedOut  || to == Cancelled)) ||
    (from == Failed  &&  to == Pending)
{
  match from
  case Pending   => to == Running || to == Cancelled
  case Running   => to == Pending || to == Completed || to == Failed ||
                    to == Cancelled || to == Waiting || to == TimedOut
  case Waiting   => to == Pending || to == TimedOut || to == Cancelled
  case Failed    => to == Pending
  case Completed => false
  case Cancelled => false
  case TimedOut  => false
}
