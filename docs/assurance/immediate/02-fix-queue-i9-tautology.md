# 02: Fix Queue I9 Tautology and Enforce ID Uniqueness

**Horizon:** Immediate
**Status:** Not started
**Estimated cost:** 1 hour
**Depends on:** nothing
**Unblocks:** nothing, but provides a live example of the adversarial-test pattern for #04

## Context

`docs/invariants/queue.md` defines I9 as "Unique IDs — no two vessels in the queue share the same `ID`." The invariant doc explicitly notes that this is **not enforced in code today**: `Enqueue` checks only `Ref`, not `ID`.

The corresponding property test `TestPropQueueInvariant_I9_UniqueIDs` (in `cli/internal/queue/queue_invariants_prop_test.go`, around lines 700–760) passes — but it passes **vacuously**. It draws mutating ops via `drawMutatingOp`, which already relies on `Ref`-based dedup. The test never constructs the scenario it claims to cover (two vessels with same ID + different Ref). This is textbook test theatre: the test executes without failing, the coverage looks green, and the invariant is not actually checked.

This is the canonical concrete example of the concern that motivated the full roadmap. Fixing it is a one-hour change that also seeds the adversarial-test pattern for #04.

## Scope

**In scope:**
- Rewrite `TestPropQueueInvariant_I9_UniqueIDs` to **explicitly** construct a scenario that would violate I9 if the invariant were not enforced: enqueue two vessels with the same `ID` and different `Ref`, and assert that the second enqueue is rejected (or that `List()` still contains only one).
- Add ID-uniqueness enforcement to `queue.Enqueue` (one-line guard): if a vessel with the same `ID` already exists in the queue, return an error.
- Update `docs/invariants/queue.md` to remove the "not enforced in code" note on I9.

**Out of scope:**
- Re-auditing other invariants for similar tautologies. Item #04 covers runner audits; a follow-up could cover queue/scanner.
- Changing the `Enqueue` signature or semantics beyond adding the ID-uniqueness check.

## Deliverables

1. Modified `cli/internal/queue/queue_invariants_prop_test.go`: rewritten I9 test that is **RED** against the current `queue.Enqueue` implementation.
2. Modified `cli/internal/queue/queue.go`: one-line ID-uniqueness guard in `Enqueue`.
3. Modified `docs/invariants/queue.md`: remove the "not enforced in code" annotation on I9.

## Acceptance criteria

- With the test change alone (no `queue.go` change): `go test ./cli/internal/queue -run TestPropQueueInvariant_I9_UniqueIDs` fails deterministically.
- After the `queue.Enqueue` fix lands: the test passes.
- After merge: the invariant doc no longer states I9 is unenforced.

## Files to touch

- `cli/internal/queue/queue_invariants_prop_test.go` (**PROTECTED** — see note below)
- `cli/internal/queue/queue.go`
- `docs/invariants/queue.md` (**PROTECTED** — see note below)

## Protected-surface note

`.claude/rules/protected-surfaces.md` protects both `docs/invariants/queue.md` and `cli/internal/queue/queue_invariants_prop_test.go` against modifications that **relax** an invariant to make a failing test pass. This change is the opposite: **strengthening** a tautological test to actually verify the invariant, plus strengthening the invariant's enforcement in code to match the doc's intent.

The PR description must explicitly state:
1. The test is being strengthened, not relaxed.
2. The invariant doc update removes a note about unenforced behavior — it does not weaken any claim.
3. The code change (`queue.Enqueue` guard) aligns behavior with the invariant doc, not the other way around.

Include the queue.md §Governance heading in the PR body so reviewers confirm compliance.

## Risks

- **Existing callers may rely on `Enqueue` silently accepting duplicate IDs** (unlikely given the invariant-doc claim, but possible). Mitigate by grepping for `Enqueue` call sites and confirming none depend on duplicate-ID behavior.
- **Rapid generators for other I tests may now fail** if they relied on duplicate IDs being acceptable. Mitigate by running the full property-test suite after the `Enqueue` fix.

## Kill criteria

None — the change is small enough that risk is bounded. If the code fix causes unrelated failures, revert the code change and ship the test change alone (with the test marked RED and a TODO linking here).

## Execution notes

**Same-LLM review concern:** Because this touches a protected surface, `pr-self-review` (claude-opus) must explicitly verify that the test is stronger, not weaker, than before. The review prompt should cross-check the new test structure against §Governance.2 of queue.md.

**Manual-review override:** If the xylem workflow surfaces any doubt about the protected-surface check, route to human review before merge rather than merging on `pr-self-review` approval alone.

## References

- `docs/invariants/queue.md` §I9 (the invariant)
- `cli/internal/queue/queue_invariants_prop_test.go` `TestPropQueueInvariant_I9_UniqueIDs` (the test to fix)
- `cli/internal/queue/queue.go` `Enqueue` (the code to update)
- `.claude/rules/protected-surfaces.md` (governance classification)
