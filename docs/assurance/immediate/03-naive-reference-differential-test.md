# 03: Naive-Reference Differential Test for Queue

**Horizon:** Immediate
**Status:** Done (PR #664, merged 2026-04-19, commit 75b4c2b)
**Estimated cost:** 2 days
**Depends on:** nothing (independent of #01–#02, though benefits from #01 landing first)
**Unblocks:** #12 (same pattern applied to gate and source)

## Context

de Moura's observation — *"an inefficient program that is obviously correct can serve as its own specification"* — gives xylem a Layer-1-adjacent assurance step that requires **zero new tooling**. Write a stupid O(n), single-file, in-memory reference queue that models every documented invariant naively, and differential-test the real queue against it under `rapid`-generated op sequences. Any divergence in observable state (`List()` output, `Dequeue` result, `Stats()` counters) indicates either a real-queue bug or a reference-queue bug, and the naive reference is easy enough to eyeball-verify that the burden falls on the real queue.

This is the single highest-value, lowest-cost intervention in the whole roadmap. It does not require Dafny, Gobra, claimcheck, or any new workflow phase. It catches everything the partial-coverage property tests miss — because the reference encodes the full specification, not a hand-picked set of invariants.

## Scope

**In scope:**
- A new package `cli/internal/queue/reference/` with a single-file, in-memory, O(n) correct-by-construction reference queue.
- The reference must implement the same public interface as `cli/internal/queue/queue.go` (or a strict subset: `Enqueue`, `Dequeue`, `List`, `UpdateState`, `UpdatePhase`, `Cancel`, plus whatever else is needed to mirror the real queue's externally observable behavior).
- A differential test `cli/internal/queue/reference/differential_test.go` using `pgregory.net/rapid` that:
  - Generates a sequence of mutating ops.
  - Applies each op to both the real queue (under a temp dir) and the reference.
  - Compares `List()` output (and any other accessors touched) after every mutation.
  - Fails if outputs diverge.

**Out of scope:**
- Applying the pattern to other modules (gate, source) — that is #12.
- Replacing the existing invariant property tests — this is additive.
- Modeling concurrency — the reference is single-threaded. Differential test serializes ops.

## Deliverables

1. `cli/internal/queue/reference/reference.go` — naive implementation. No optimizations. Clarity dominates performance.
2. `cli/internal/queue/reference/differential_test.go` — `TestProp_DifferentialAgainstReal` using rapid.
3. `cli/internal/queue/reference/README.md` — explains why the reference exists (de Moura pattern), what it does NOT model (concurrency, durability, crash recovery), and when to update it.

## Acceptance criteria

- Rapid finds no divergence across 10,000+ iterations (`rapid.Check` default) with a non-trivial op mix.
- The reference implementation is under 200 lines of Go (if larger, it is not naive enough).
- The differential test runs in under 30 seconds in CI.
- A deliberately introduced bug in `cli/internal/queue/queue.go` (e.g. swapping the order of two operations in `UpdateState`) is caught by the differential test.

## Files to touch

- **New:** `cli/internal/queue/reference/reference.go`
- **New:** `cli/internal/queue/reference/differential_test.go`
- **New:** `cli/internal/queue/reference/README.md`
- **Read-only (input):** `cli/internal/queue/queue.go` (the interface to mirror)
- **Read-only (input):** `docs/invariants/queue.md` (the invariants the reference must honor)

## Risks

- **The reference itself has bugs.** This is the main risk. Mitigate by (a) keeping it absurdly simple and short, (b) requiring code review focused specifically on reference correctness (not the differential test), (c) cross-checking reference behavior against the invariant doc line-by-line in the review.
- **Interface drift between real queue and reference.** Mitigate by defining a small test-only interface in the reference package that both types satisfy, and asserting at compile time.
- **Concurrency surface not modeled.** This is documented as out of scope. The reference cannot catch race conditions — those remain covered by `go test -race` and the real queue's property tests. Call this out explicitly in `README.md`.
- **Mutating-op generator (`drawMutatingOp`) may already have tautological shortcuts** (see I9 for a concrete example). Use a fresh generator inside `differential_test.go` that does **not** short-circuit via the real queue's dedup.

## Kill criteria

- If after 3 days the differential test still cannot be made deterministic (for reasons other than finding real bugs), re-scope to a smaller interface subset or drop the item.
- If more than 200 lines of reference code are required to cover the interface honestly, the real queue's interface is too wide — split first, then reference.

## Execution notes

**Same-LLM review concern:** This is the most sensitive Immediate item from a review-model standpoint. The whole point is that the reference is "obviously correct" — but *to whom?* If gpt-5-mini writes the reference and claude-opus reviews it, we are relying on the review catching any subtle errors. For this item specifically, surface the reference code in the PR body and recommend a human eyeball-check before merge rather than relying solely on `pr-self-review`.

**Protected surfaces:** None directly. The reference is new code and does not touch the existing property tests.

## References

- `docs/research/assurance-hierarchy.md` §Layer 1 (formally-verified pure code)
- `docs/research/literature-review.md` §de Moura (2026) — source of the "obviously correct is its own spec" pattern
- `cli/internal/queue/queue.go` (the system under test)
- `cli/internal/queue/queue_invariants_prop_test.go` (existing property tests the differential test complements)
