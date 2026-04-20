# 09: Retry-DAG Acyclicity Dafny-Verified Kernel

**Horizon:** Next (4–8 weeks)
**Status:** In progress (PR pending)
**Estimated cost:** 3 days
**Depends on:** #06 (pipeline pattern established), #08 (verify-kernel gate catches regressions)
**Unblocks:** nothing — this is a terminal item in the Next phase

## Context

`docs/invariants/queue.md` §I10 documents that retry-DAG acyclicity is **caller-responsibility**: the invariant doc explicitly warns that the queue itself does not check for cycles in the retry graph. A cycle would cause a vessel to retry itself indefinitely.

With the Dafny pipeline proven by #06, replacing caller-responsibility with a **verified checker** at the queue boundary is a 3-day item. The kernel is pure graph-algorithm code (DFS with termination measure), entirely sequential, and the post-condition ("if the function returns true, the graph is acyclic") is trivial to state.

## Scope

**In scope:**
- New Dafny source `cli/internal/queue/verified/retry_dag.dfy`:
  - A graph datatype (adjacency list over vessel IDs).
  - An `IsAcyclic(graph)` function with termination measure and post-condition.
- Generated Go file `cli/internal/queue/verified/retry_dag.go` extracted via `crosscheck:extract-code`.
- Wiring: call the verified checker at every queue entry point that accepts a retry reference (`Enqueue`, `UpdatePhase`, anywhere else that introduces a parent→child retry edge).

**Out of scope:**
- Other queue invariants (they are #06 or deferred).
- Runner-side retry logic — that stays Go.

## Deliverables

1. `cli/internal/queue/verified/retry_dag.dfy` — spec.
2. `cli/internal/queue/verified/retry_dag.go` — generated.
3. `cli/internal/queue/queue.go` — wiring at retry-edge entry points.
4. Updated `docs/invariants/queue.md` §I10 — remove the "caller responsibility" annotation.
5. Strengthened `TestPropQueueInvariant_I10_RetryDagAcyclic` — must exercise the verified checker's error path.

## Pipeline steps

Same as #06:
1. `crosscheck:spec-iterate` on `retry_dag.dfy`.
2. `crosscheck:generate-verified`.
3. `mcp__plugin_crosscheck_dafny__dafny_verify`.
4. `crosscheck:extract-code`.
5. Commit spec, generated Go, and wiring in **one** PR (smaller than #06 so bundling is OK).

## Acceptance criteria

- `dafny_verify` returns clean.
- Extracted Go passes property tests, including the strengthened I10 test.
- A deliberately cyclic retry reference inserted via test scaffolding is rejected.
- Invariant doc no longer calls I10 caller-responsibility.

## Files to touch

- **New:** `cli/internal/queue/verified/retry_dag.dfy`
- **New:** `cli/internal/queue/verified/retry_dag.go`
- **Modified:** `cli/internal/queue/queue.go`
- **Modified:** `docs/invariants/queue.md` (**PROTECTED** — governance note)
- **Modified:** `cli/internal/queue/queue_invariants_prop_test.go` (**PROTECTED** — strengthening)

## Risks

- **Graph datatype in Dafny requires a termination measure for DFS.** If the measure is not obvious, the spec will not verify. Standard technique: measure is the number of unvisited nodes; decrements on each recursive call.
- **Wiring at Enqueue may reject vessels that were previously accepted.** Audit existing queue data for any vessel that (after the fix) would be rejected. Likely none given the invariant is already documented, but verify.
- **Performance.** Per-Enqueue graph check is O(V + E) of the retry chain. Bounded by max retry depth, which is small. Not a concern.

## Kill criteria

- If Dafny termination measure is intractable after 2 days, fall back to a Go implementation with a property-test proof of correctness and file the Dafny version as a future aspirational item.

## Execution notes

**Protected surfaces:** Two — `queue.md` and the property test file. Both strengthening, both requiring governance notes.

**Same-LLM review concern:** Same as #06 — first time the pipeline runs on a different module member, so human review for the initial PR, then `pr-self-review` is sufficient for follow-ups.

**CI coverage:** Inherited from #08 at no additional cost. The verify-kernels CI job uses `git diff --name-only origin/main...HEAD | grep '\.dfy$'` to discover changed Dafny files on the PR branch. When #09's PR lands, `retry_dag.dfy` will appear in that diff and be verified by the existing job. Future PRs that touch `retry_dag.dfy` will also be caught automatically. No additional CI wiring is required for this item.

## References

- `docs/invariants/queue.md` §I10
- `cli/internal/queue/queue.go` (Enqueue and retry-edge entry points)
- `docs/assurance/next/06-queue-dafny-kernel.md` (pipeline precedent)
