# Xylem Assurance Roadmap

## Purpose

This directory tracks the execution of the **6-layer assurance hierarchy** (see `docs/research/assurance-hierarchy.md` and `docs/research/literature-review.md`) as applied pragmatically to xylem. It exists so the plan survives across long timeframes without depending on conversation context or ephemeral plan files.

Each numbered item below has its own standalone doc under `immediate/`, `next/`, `medium-term/`, or `aspirational/`. That doc is the source of truth for scope, acceptance criteria, and kill criteria — not any issue, PR, or plan file elsewhere.

## Strategic context

The long-term goal is **deterministic AI-driven software development**: formally verified kernels for critical pure logic, contract graphs for integration boundaries, and spec-intent alignment checks for everything else. The full hierarchy lives in `docs/research/assurance-hierarchy.md`.

xylem's current pragmatic projection of that hierarchy:

| Layer | Hierarchy reach | Xylem reach today |
|-------|-----------------|-------------------|
| 1: Formally verified pure code | Dafny / Lean 4 | Sequential pure kernels only (queue state machine, retry-DAG, budget gate). Concurrent code stays in Go + property tests. |
| 2: Compilation correctness | Verified compilation (CompCert) | **No Go CompCert exists.** Trusted Go toolchain. Not addressable. |
| 3: Contract graph verification | End-to-end subgraph verification | Pairwise contracts near-term. End-to-end subgraph verification is aspirational (item #13). |
| 4: Implementation–spec alignment | Deterministic (Dafny accepts/rejects) | Delivered by Dafny-verified kernels as they land (#06, #09). |
| 5: Spec–intent alignment | Probabilistic (claimcheck, ~96%) | `intent-check` workflow phase (#07). |
| 6: Spec completeness | Best-effort | `spec-adversary` workflow phase (#14), acceptance-oracle (#11). |

## Roadmap

### Immediate (start now)

| # | Item | Cost | Doc |
|---|------|------|-----|
| 1 | Enforce `// Invariant IN:` test coverage mapping in CI | 1 day | [immediate/01-invariant-test-coverage-ci.md](immediate/01-invariant-test-coverage-ci.md) |
| 2 | Fix Queue I9 tautology and enforce ID uniqueness | 1 hour | [immediate/02-fix-queue-i9-tautology.md](immediate/02-fix-queue-i9-tautology.md) |
| 3 | Naive-reference differential test for queue | 2 days | [immediate/03-naive-reference-differential-test.md](immediate/03-naive-reference-differential-test.md) |
| 4 | Close 14 partial-coverage gaps in runner property tests | 1–2 weeks | [immediate/04-close-partial-coverage-gaps.md](immediate/04-close-partial-coverage-gaps.md) |
| 5 | Amend assurance-hierarchy.md with xylem projection section | 2 hours | [immediate/05-hierarchy-xylem-projection.md](immediate/05-hierarchy-xylem-projection.md) |

### Next (4–8 weeks)

| # | Item | Cost | Doc |
|---|------|------|-----|
| 6 | Queue state machine Dafny-verified kernel | 1–2 weeks | [next/06-queue-dafny-kernel.md](next/06-queue-dafny-kernel.md) — **Complete**: IsTerminal (PR #685) + ValidTransition + lightweight-verify of protectedFieldsEqual (PR #687); queue.go wired to verified kernel (2026-04-20); Dafny-extract of protectedFieldsEqual deferred to #10 (Gobra) |
| 7 | `intent-check` workflow phase (claimcheck-analog) | 1 week | [next/07-intent-check-phase.md](next/07-intent-check-phase.md) |
| 8 | `verify-kernel` workflow phase | 2 days | [next/08-verify-kernel-phase.md](next/08-verify-kernel-phase.md) — **Complete** (2026-04-20): `scripts/verify-kernels.sh` + phase inserted in all 3 delivery workflows + CI job (`verify-kernels` in `.github/workflows/ci.yml`) using `dafny-lang/setup-dafny@v1` (hard enforcement, no Docker needed) |
| 9 | Retry-DAG acyclicity Dafny-verified kernel | 3 days | [next/09-retry-dag-dafny-kernel.md](next/09-retry-dag-dafny-kernel.md) |

**Planned execution order (2026-04-20):** #08 → #09 → #07. Item #08 is the fastest (2 days) and gates all future `.dfy` regressions, which unblocks #09. Item #07 has no hard dependencies but is scheduled after #09: it is the highest-risk item (FP kill criterion at 30%) and requires human-authored governance amendments to protected workflow YAMLs regardless.

### Medium-term (2–3 months)

| # | Item | Cost | Doc |
|---|------|------|-----|
| 10 | Gobra concurrency-safety specs for queue | 3–4 weeks | [medium-term/10-gobra-queue.md](medium-term/10-gobra-queue.md) |
| 11 | `acceptance-oracle` workflow phase | 2 weeks | [medium-term/11-acceptance-oracle-phase.md](medium-term/11-acceptance-oracle-phase.md) |
| 12 | Naive-reference implementations for gate and source | 1 week | [medium-term/12-gate-source-reference-impls.md](medium-term/12-gate-source-reference-impls.md) |

### Aspirational (scope and commit later)

| # | Item | Cost | Doc |
|---|------|------|-----|
| 13 | Go-native contract-graph-verifier (extend existing Rust+Lean PoC) | 2–3 months | [aspirational/13-go-native-cgv.md](aspirational/13-go-native-cgv.md) |
| 14 | `spec-adversary` workflow phase (Layer 6) | 2 weeks | [aspirational/14-spec-adversary-phase.md](aspirational/14-spec-adversary-phase.md) |

## How to use this directory

**To check status of a roadmap item** — open the item's doc. Each doc carries a `Status` field at the top: `Not started`, `In progress`, `Blocked`, or `Done`. When a PR lands for an item, update the doc's Status and cross-reference the PR number.

**To execute a roadmap item** — the doc is the spec. File a GitHub issue referencing the doc path and apply the appropriate label (`bug` / `enhancement` / `harness-impl`). The xylem daemon drains the issue through its isolated SDLC phases. Do not paste the full doc into the issue body; link to it.

**To propose a new item** — add a new file under the correct horizon directory, add a row to the roadmap table above, and open a PR. The strategic context section above constrains what belongs in this directory: items that move a layer of the assurance hierarchy, not general engineering work.

**To kill or defer an item** — do not delete the doc. Mark `Status: Deferred` with a `Reason:` line and, if applicable, a link to the item that supersedes it. The goal of this directory is that no decision disappears, including negative ones.

## Roadmap-level kill criteria

Stop and re-plan the entire roadmap if any of the following becomes true:

- **Dafny pipeline fails the first kernel (#06).** If crosscheck extraction produces unusable Go after 3 weeks, the whole "verified kernels in Go" thesis is invalidated — rescope before committing to #07–#09.
- **`intent-check` false-positive rate > 30% (#07).** claimcheck's reported 96.3% was on a curated benchmark. If real-PR FP rate is above 30% after 2 weeks, the Layer 5 strategy needs a different approach.
- **No Immediate item lands in 4 weeks.** If items #01–#05 are not all merged within 4 weeks of start, the problem is process-level (daemon reliability, model quality, review capacity) — fix that before continuing.

## References

- `docs/research/assurance-hierarchy.md` — the 6-layer hierarchy this roadmap operationalizes (includes "Xylem projection" section mapping layers to current Go-ecosystem reach)
- `docs/research/literature-review.md` — prior art (GS AI, Kleppmann, Midspiral, Axiom, Harmonic, de Moura, Skomarovsky)
- `docs/invariants/queue.md`, `scanner.md`, `runner.md` — load-bearing module invariant specs
- `.claude/rules/protected-surfaces.md` — governance for invariant docs and property tests
- `CLAUDE.md` — xylem architecture and testing patterns
