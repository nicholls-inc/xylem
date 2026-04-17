# 12: Naive-Reference Implementations for Gate and Source

**Horizon:** Medium-term (2–3 months)
**Status:** Not started
**Estimated cost:** 1 week
**Depends on:** #03 (pattern established on queue)
**Unblocks:** nothing — incremental Layer-1-adjacent assurance spread across more modules

## Context

Item #03 applies de Moura's "inefficient program as its own spec" pattern to queue. Once that pattern is proven, applying it to two more modules — `gate` and `source` (specifically the dedup-key computation) — is mechanical. Both are pure, sequential, and small. Both are under-tested today relative to their criticality: `gate.BudgetGate.Check` gates every cost-sensitive operation; `source` dedup-key logic gates every scheduled dispatch.

Runner is **explicitly excluded**. Runner's concurrency surface is large enough that a naive single-threaded reference would model only a small subset of its behavior, giving a false sense of assurance. Runner stays with property tests + `go test -race` + acceptance oracle (#11).

## Scope

**In scope:**
- `cli/internal/gate/reference/` — naive reference for `BudgetGate.Check` (pure decision against remaining USD + per-class limits).
- `cli/internal/source/reference/` — naive reference for dedup-key computation.
- Differential tests against real implementations under rapid.

**Out of scope:**
- Runner.
- Scanner (dedup is part of source; runner logic is concurrent).
- Workflow loader (deeply tied to YAML parsing — not a good reference target).

## Deliverables

Two PRs, one per module, each mirroring #03's structure:

1. **gate:**
   - `cli/internal/gate/reference/reference.go`
   - `cli/internal/gate/reference/differential_test.go`
   - README linking to #03.

2. **source:**
   - `cli/internal/source/reference/reference.go` (dedup-key only)
   - `cli/internal/source/reference/differential_test.go`
   - README explaining per-source-type variations.

## Acceptance criteria

- Rapid finds no divergence across 10k+ iterations per module.
- Each reference under 200 lines.
- Deliberately introduced bug in real implementation caught by differential test.

## Files to touch

- **New:** `cli/internal/gate/reference/*`
- **New:** `cli/internal/source/reference/*`
- **Read-only:** `cli/internal/gate/gate.go`, `cli/internal/source/*.go`

## Risks

- **Source has multiple source types** (github, github-pr, scheduled, etc.). Dedup logic may vary per type. Mitigate by scoping reference to the common `dedupKey` computation and noting per-type variations.
- **Gate has external dependencies** (real USD tracking, time). Mitigate by making reference depend only on pure inputs — pass time and budget as arguments.

## Kill criteria

- If source dedup logic cannot be modeled cleanly in < 200 lines, ship gate-only.

## Execution notes

**Same-LLM review concern:** Same as #03 — reference correctness is the main risk. Human eyeball recommended for each PR.

**Protected surfaces:** None.

## References

- `docs/assurance/immediate/03-naive-reference-differential-test.md` (the precedent)
- `docs/research/literature-review.md` §de Moura
- `cli/internal/gate/gate.go`, `cli/internal/source/*.go`
