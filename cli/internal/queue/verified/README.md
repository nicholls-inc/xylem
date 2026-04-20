# cli/internal/queue/verified

Formally verified implementations of pure queue state-machine functions.

## What's here

| File | Status | Description |
|---|---|---|
| `state_machine.dfy` | Hand-written | Dafny spec — source of truth |
| `state_machine.go` | Generated | Go extraction, boilerplate stripped |

## What is Dafny?

[Dafny](https://dafny.org) is a verification-aware language. You write a function with `ensures` (postcondition) clauses, and the Dafny verifier — backed by the Z3 SMT solver — proves the body satisfies those clauses for every possible input. If verification passes, the spec is machine-checked, not just tested.

## Current scope (roadmap #06, phase 1)

`IsTerminal(s string) bool` — returns true iff `s` is one of the four terminal vessel states (`"completed"`, `"failed"`, `"cancelled"`, `"timed_out"`). Chosen as the first kernel because it is the smallest pure function in the queue package.

**Not yet extracted:** `validTransitions`, `protectedFieldsEqual`. These are planned for subsequent phases after this pipeline is validated.

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

## Wiring into queue.go

The rewiring PR (roadmap #06 step 7) replaces the inline expression in `queue.go` with a call to this package:

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

The rewiring is a **separate PR** from this one, so the kernel files are reviewable independently of the wiring change.

## Governance

- `state_machine.dfy` is the source of truth. The `.go` file is generated from it; never edit the `.go` by hand.
- The first kernel requires **human review** before merge — see roadmap item #06 governance note.
- Future kernels (`validTransitions`, `protectedFieldsEqual`) follow the same pipeline.
