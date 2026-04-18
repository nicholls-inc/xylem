# 05: Amend Assurance Hierarchy With Xylem Projection Section

**Horizon:** Immediate
**Status:** Done
**Estimated cost:** 2 hours
**Depends on:** nothing
**Unblocks:** everything that cites the hierarchy as its north star

## Context

`docs/research/assurance-hierarchy.md` describes the 6-layer hierarchy as a research framework. It deliberately does not tie itself to any specific ecosystem, so it does not state the current concrete tooling reach for xylem.

Without a xylem-specific projection, every downstream decision on this roadmap (and any future roadmap in this repo) has to re-derive the gap between the hierarchy's ambition and xylem's current Go-ecosystem reality. That derivation drifts quickly: new readers assume Layer 2 is in scope (it is not — there is no Go CompCert), or assume Layer 3 end-to-end subgraph verification is available near-term (it is not — #13 is aspirational).

The fix is to add a single **"Xylem projection"** section to the hierarchy doc that states the current reach per layer and explicitly names the tooling limits. This is a doc amendment, not a new doc.

## Scope

**In scope:**
- Add a new section titled "Xylem projection" to `docs/research/assurance-hierarchy.md`, placed after the hierarchy definition and before the "Supporting Workflow Elements" section.
- Each of the six layers gets one paragraph (or bullet) stating:
  - Current xylem reach.
  - Explicit tooling limit (e.g. "no Go CompCert exists; Layer 2 is trusted toolchain").
  - Cross-reference to the relevant roadmap items in `docs/assurance/`.

**Out of scope:**
- Changing the hierarchy definition itself.
- Changing the literature review.
- Adding new layers or removing existing ones.

## Deliverables

A single PR adding a "Xylem projection" section to `docs/research/assurance-hierarchy.md` with content approximately like:

```markdown
## Xylem projection

This section states the current concrete reach of each layer within the xylem
codebase (Go, concurrent daemon, no verified Go compiler available). It is
maintained alongside `docs/assurance/ROADMAP.md` and should be updated whenever
a roadmap item changes a layer's reach.

**Layer 1 (formally verified pure code).** Restricted to sequential pure logic
— queue state machine (I7), retry-DAG acyclicity (I10), budget-gate arithmetic,
class-slot accounting, dedup-key hashing. Tooling: Dafny via the `crosscheck`
plugin, compiled to Go via `crosscheck:extract-code`. See `docs/assurance/next/06-queue-dafny-kernel.md` and `09-retry-dag-dafny-kernel.md`.

**Layer 2 (compilation correctness).** No Go equivalent of CompCert exists. The
Go toolchain is part of the trusted computing base. This layer is **not
addressable** in the near term.

**Layer 3 (contract graph verification).** Pairwise contracts via Gobra are
near-term (item #10 — queue only). End-to-end subgraph verification (scanner →
queue → runner) is aspirational (item #13 — Go-native extension of the existing
Rust+Lean contract-graph-verifier PoC).

**Layer 4 (implementation-spec alignment).** Delivered by Dafny-verified
kernels as they land. `verify-kernel` workflow phase (#08) gates merges on
`dafny_verify` of any touched `.dfy` files.

**Layer 5 (spec-intent alignment).** `intent-check` workflow phase (#07) —
claimcheck-analog using two-LLM back-translation. Probabilistic; expect
~96% accuracy on curated benchmarks and unknown real-PR performance until
item #07 is operating.

**Layer 6 (spec completeness).** Best-effort. `acceptance-oracle` workflow
phase (#11) gives observable user-behavior assurance; `spec-adversary` phase
(#14) explores adversarial property discovery. No theorem proves spec
completeness; these are both iterative practices.
```

## Acceptance criteria

- Section is added to `docs/research/assurance-hierarchy.md`.
- Each layer paragraph names at least one tooling limit or cross-references a roadmap item.
- The section is discoverable from `docs/assurance/ROADMAP.md` (the ROADMAP already links to the hierarchy doc).

## Files to touch

- `docs/research/assurance-hierarchy.md` (additive section)
- `docs/assurance/ROADMAP.md` (minor cross-reference update if the section introduces new anchors worth linking to)

## Risks

None material. This is a doc amendment. The only risk is drift: if the section becomes stale, it misleads future readers. Mitigate with a single sentence at the top of the section naming its refresh trigger ("updated whenever a roadmap item changes a layer's reach").

## Kill criteria

None.

## Execution notes

**Same-LLM review concern:** Doc-only change. `pr-self-review` (claude-opus) is sufficient. No human review required unless the projection introduces a factual error (e.g. misstating what Dafny can verify).

**Protected surfaces:** `docs/research/assurance-hierarchy.md` is **not** in the protected list. It is a research document. Edits are permitted without governance note.

## References

- `docs/research/assurance-hierarchy.md` (the doc to amend)
- `docs/research/literature-review.md` (source of the tooling-reach facts)
- `docs/assurance/ROADMAP.md` (the cross-reference target)
