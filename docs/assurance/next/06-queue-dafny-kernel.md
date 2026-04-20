# 06: Queue State Machine Dafny-Verified Kernel

**Horizon:** Next (4–8 weeks)
**Status:** Complete (2026-04-20)
**Estimated cost:** 1–2 weeks
**Depends on:** #01 (coverage CI), #02 (I9 fix), #03 (naive reference — gives a property-test oracle against which the extracted Go can be validated)
**Unblocks:** #07 (intent-check has a concrete Dafny artifact to reason about), #08 (verify-kernel gate has something to verify), #09 (retry-DAG follows same pipeline)

## Context

The first real Dafny→Go extraction on xylem code. Its purpose is to prove the whole **spec-iterate → generate-verified → extract-code → wire into Go** pipeline end-to-end on production code before committing to items #07–#09. Success proves the thesis; failure invalidates the entire Dafny track and forces a fallback to "trusted-Go + stronger property tests."

The target is the narrowest, most contained kernel in xylem: the queue state machine. Specifically:
- `validTransitions` — the pure enum-to-enum mapping that says which `VesselState` can transition to which.
- `IsTerminal` — a pure structural predicate.
- `protectedFieldsEqual` (backing I2 — terminal-record immutability) — a pure structural comparison.

All three are sequential, pure, and small. They are the **best possible first target**: if Dafny cannot verify these, nothing else in xylem is verifiable via Dafny.

## Scope

**In scope:**
- A new Dafny source file `cli/internal/queue/verified/state_machine.dfy` specifying:
  - The `VesselState` datatype (mirroring the Go enum).
  - `validTransitions(from, to)` as a total function with a post-condition asserting a table-driven semantics.
  - `IsTerminal(state)` as a total function.
  - `protectedFieldsEqual(old, new)` as a structural comparison over the subset of fields I2 considers immutable.
- A generated Go file `cli/internal/queue/verified/state_machine.go` extracted via `crosscheck:extract-code`.
- Rewiring in `cli/internal/queue/queue.go` so existing callers invoke the verified implementations instead of the original Go.
- A README in `cli/internal/queue/verified/` explaining what Dafny is, which files are hand-written vs generated, and how to regenerate.

**Out of scope:**
- Concurrency properties. Dafny has no concurrency model.
- I/O durability (fsync, atomic rename). Dafny hard wall.
- Retry-DAG acyclicity (that is #09).
- `Enqueue`/`Dequeue`/`List` — too stateful and too wide an interface for a first kernel.

## Deliverables

1. `cli/internal/queue/verified/state_machine.dfy` — hand-written Dafny, committed as source of truth.
2. `cli/internal/queue/verified/state_machine.go` — machine-generated Go, committed alongside.
3. Updated `cli/internal/queue/queue.go` — calls the generated kernel.
4. `cli/internal/queue/verified/README.md` — pipeline instructions.
5. Updated `docs/invariants/queue.md` — reference the verified kernel in the relevant invariant entries (I2, I7).

## Pipeline steps

Executed by a human operator (not the xylem daemon — first kernel requires manual oversight):

1. Write the `.dfy` spec by hand, iteratively, using `crosscheck:spec-iterate` until it is well-formed and the intended post-conditions are stated.
2. Run `crosscheck:generate-verified` to produce a verified Dafny body.
3. Run `mcp__plugin_crosscheck_dafny__dafny_verify` to confirm no verification errors.
4. Run `crosscheck:extract-code` to compile to Go.
5. Inspect the generated Go. If larger than 200 lines or non-idiomatic, simplify the spec before re-extracting. (Plugin's own docs recommend human review at that size.)
6. Commit all three files (`.dfy`, `.go`, `README.md`) in one PR.
7. Rewire `queue.go` callers in a **separate** PR so the rewiring is reviewable independently of the verification work.

## Acceptance criteria

- `mcp__plugin_crosscheck_dafny__dafny_verify` returns clean on `state_machine.dfy`.
- Extracted `state_machine.go` compiles and passes all existing queue property tests.
- The naive-reference differential test (#03) continues to pass after rewiring.
- The PR adding the kernel is **not** merged by `pr-self-review` alone — human review is required for the first kernel because it establishes the pattern.

## Files to touch

- **New:** `cli/internal/queue/verified/state_machine.dfy`
- **New:** `cli/internal/queue/verified/state_machine.go` (generated)
- **New:** `cli/internal/queue/verified/README.md`
- **Modified:** `cli/internal/queue/queue.go` (rewiring — in a follow-up PR)
- **Modified:** `docs/invariants/queue.md` (reference the kernel — **PROTECTED**, governance note required)

## Risks

- **Dafny→Go extraction uses `interface{}` type erasure.** Callers may need type assertions. Mitigate by keeping the verified API small (a handful of pure functions with simple inputs).
- **Dafny `real` → `BigRational`, not `float64`.** Not an issue for queue state machine (no numeric logic). Worth noting for future kernels that touch budgets.
- **Generated Go may not be idiomatic.** Plugin docs say ≥200 lines warrants review. Simpler spec → simpler output.
- **First kernel sets the pattern for all future kernels.** Over-engineering or under-engineering this one propagates. Err on the side of simple.

## Kill criteria

- If the spec cannot be made to verify cleanly within 3 days of dedicated work, re-scope the target (perhaps only `IsTerminal` first).
- If the extracted Go breaks existing callers in ways that require more than a day of rewiring, the interface is wrong — re-spec.
- If this item takes more than 3 weeks end-to-end (not elapsed time — actual engineering days), fall back to "trusted Go + property tests" and mark items #07–#09 as blocked pending a re-assessment of the Dafny track.

## Execution notes

**Same-LLM review concern:** Must be human-reviewed. `pr-self-review` can catch style issues but cannot validate that a Dafny spec correctly captures a Go implementation's intended semantics. The first kernel is a governance moment, not a merge-automation moment.

**Protected surfaces:** `docs/invariants/queue.md` update requires governance note per `.claude/rules/protected-surfaces.md`. The kernel files themselves are new and unprotected.

## References

- `docs/research/assurance-hierarchy.md` §Layer 1
- `docs/research/literature-review.md` §Axiom, §Harmonic, §Midspiral (prior art on Dafny/Lean extraction)
- Crosscheck plugin at `~/.claude/plugins/cache/nicholls/crosscheck/2.1.0/`
- Dafny Go compilation docs: https://dafny.org/latest/Compilation/Go
- `cli/internal/queue/queue.go` (the code being replaced)
- `docs/invariants/queue.md` §I2, §I7 (the invariants being verified)

## Progress

**Phase 1 — PR #685 (2026-04-20):** IsTerminal delivered and verified.
- `cli/internal/queue/verified/state_machine.dfy` — Dafny source, 1 verified 0 errors (Dafny 4.11.0)
- `cli/internal/queue/verified/state_machine.go` — clean Go extraction, _dafny boilerplate stripped
- `cli/internal/queue/verified/state_machine_test.go` — exhaustive + rapid property tests
- `cli/internal/queue/verified_differential_test.go` — abstraction-gap check vs queue.IsTerminal
- `.crosscheck/specs.json` — spec registry entry added

**Phase 2 — (2026-04-20):** ValidTransition delivered and verified.
- `cli/internal/queue/verified/state_machine.dfy` — extended with `ValidTransition`; now 2 verified, 0 errors (Dafny 4.11.0)
- `cli/internal/queue/verified/state_machine.go` — `ValidTransition(from, to string) bool` added; same hand-extraction pattern
- `cli/internal/queue/verified_differential_test.go` — `TestValidTransition_DifferentialWithMap` added; covers all 49 canonical pairs + unknown from-state + unknown to-state
- `.crosscheck/specs.json` — `valid-transition` entry added; `is-terminal` hash updated to reflect .dfy change
- `docs/invariants/queue.md` — I2 status row updated ✗→✓ (protectedFieldsEqual guard already present at queue.go:472; stale line reference corrected); summary updated; governance amendment per user direction 2026-04-20
- `cli/internal/queue/queue_invariants_prop_test.go` — file-header comment updated: I2 removed from skip list (no t.Skip in TestPropQueueInvariant_I2_TerminalImmutability)

**Phase 4 — Wiring queue.go (2026-04-20):** Production callers now invoke the verified kernel.
- `cli/internal/queue/queue.go` — `IsTerminal()` method delegates to `verified.IsTerminal(string(s))`; three `validTransitions` map look-ups replaced with `verified.ValidTransition(string(from), string(to))`. The `validTransitions` map is retained (it is the oracle for `queue_invariants_prop_test.go`, which is a protected surface).
- `cli/internal/queue/verified_differential_test.go` — `TestIsTerminal_DifferentialWithVerified` repurposed as `TestIsTerminal_TruthTable` (independent ground truth, not tautological after delegation); `TestValidTransition_DifferentialWithMap` retained to guard that the map used by property tests stays in sync with the verified function used by production.
- All queue tests pass (`go test ./internal/queue/` clean).

**Scoping decision — protectedFieldsEqual:**
`protectedFieldsEqual` is deferred to **#10 (Gobra)**, which handles Go-native `*time.Time` and `map[string]string` without extraction gymnastics. Reason for deferral from #06: the function operates on the 19-field `Vessel` struct which has `*time.Time` and `map[string]string` fields. Modelling these in Dafny requires either abstract ghost types (no extractable code) or a full Vessel datatype whose Go extraction doesn't interoperate with the real `queue.Vessel` without a conversion shim — which defeats the purpose of extraction. The existing Go implementation is already a compile-time-explicit field enumeration (not reflection), providing adequate assurance for I2. Kill criteria were not triggered; this is an intentional rescope per the kill-criteria guidance ("perhaps only `IsTerminal` first").

**Phase 3 — Lightweight verification of `protectedFieldsEqual` (2026-04-20):** delivered alongside Phase 2 in PR #687.
- `cli/internal/queue/verified/protectedfields_verify.md` — semi-formal contract analysis: 11 contracts (C1–C11), 19-field coverage table, helper analysis for `timePtrEqual` and `stringMapEqual`, verification gaps, upgrade path to #10 (Gobra)
- `cli/internal/queue/protectedfields_verify_test.go` — companion tests: per-field mutation coverage (19 fields), exclusion tests (4 excluded fields), reflexivity and symmetry property tests (rapid), unit tests for `timePtrEqual` (6 cases) and `stringMapEqual` (10 cases)
- `docs/assurance/medium-term/10-gobra-queue.md` — updated: `protectedFieldsEqual` added to Gobra scope with rationale; acceptance criterion added; read-only file list updated with correct line references (queue.go:98, 124)
