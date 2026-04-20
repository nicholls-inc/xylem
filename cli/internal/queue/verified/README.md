# cli/internal/queue/verified

Formally verified implementations of pure queue state-machine functions.

## What's here

| File | Status | Description |
|---|---|---|
| `state_machine.dfy` | Hand-written | Dafny spec — source of truth |
| `state_machine.go` | Generated | Go extraction, boilerplate stripped |

## What is Dafny?

[Dafny](https://dafny.org) is a verification-aware language. You write a function with `ensures` (postcondition) clauses, and the Dafny verifier — backed by the Z3 SMT solver — proves the body satisfies those clauses for every possible input. If verification passes, the spec is machine-checked, not just tested.

## Current scope (roadmap #06, phases 1–2)

`IsTerminal(s string) bool` — returns true iff `s` is one of the four terminal vessel states (`"completed"`, `"failed"`, `"cancelled"`, `"timed_out"`). Phase 1 kernel.

`ValidTransition(from, to string) bool` — returns true iff transitioning from `from` to `to` is permitted by the state machine. Encodes the full transition table from `queue.go:validTransitions`. Phase 2 kernel.

**Deferred:** `protectedFieldsEqual` — requires modelling the 19-field `Vessel` struct in Dafny, with abstract types for `*time.Time` and `map[string]string`. The extracted Go would not interoperate with the real `Vessel` type without a conversion shim that defeats the purpose. Scoping decision documented in roadmap #06.

## How to re-verify

Requires the `crosscheck` plugin with Docker:

```bash
# Verify the spec (requires crosscheck MCP plugin)
# In Claude Code: use mcp__plugin_crosscheck_dafny__dafny_verify on state_machine.dfy
# Expected: "1 verified, 0 errors"
```

## How to re-extract

```bash
# In Claude Code, invoke the skill:
/crosscheck:extract-code to go
# Then strip _dafny.* boilerplate and adapt types per the mapping table in README.
```

**Type mapping used for this extraction:**

| Dafny Type | Go Type |
|---|---|
| `datatype VesselState` (discriminated union) | `string` |
| `VesselState_Completed`, etc. | `"completed"`, etc. |
| `(s).Equals(Companion_VesselState_.Create_Completed_())` | `s == "completed"` |
| Dafny `match` on `VesselState` | Go `switch` on `string` |

## Wiring into queue.go

The rewiring PR (roadmap #06 step 7) replaces the inline implementations in `queue.go` with calls to this package. Example for `IsTerminal`:

```go
import "github.com/nicholls-inc/xylem/cli/internal/queue/verified"

// Before:
func (s VesselState) IsTerminal() bool {
    return s == StateCompleted || s == StateFailed || s == StateCancelled || s == StateTimedOut
}

// After:
func (s VesselState) IsTerminal() bool {
    return verified.IsTerminal(string(s))
}
```

`ValidTransition` replaces the `validTransitions` map look-up in `Update`, `UpdateVessel`, and `Cancel`. The rewiring is a **separate PR** from the kernel files, so verification is reviewable independently of wiring.

## Governance

- `state_machine.dfy` is the source of truth. The `.go` file is derived from it; never edit the `.go` by hand.
- Kernels require **human review** before merge — see roadmap item #06 governance note.
- `protectedFieldsEqual` is deferred — see scoping note in README and roadmap #06.
