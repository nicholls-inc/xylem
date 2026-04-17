# 13: Go-Native Contract-Graph-Verifier (Extend Existing PoC)

**Horizon:** Aspirational (2–3 months engineering time; scope and commit after #06–#09 validate broader thesis)
**Status:** Not started
**Estimated cost:** 2–3 months engineering time
**Depends on:** #06–#09 (Dafny track must prove the broader thesis before investing in CGV)
**Unblocks:** Layer 3 end-to-end subgraph verification

## Context

A Rust+Lean contract-graph-verifier proof-of-concept exists at `~/repos/contract-graph-verifier/`. It has:

- A Rust extractor (`src/`) — ruff-based Python AST extraction, Django ORM edge discovery, SQLite-backed contract storage.
- A Lean checker (`prover/`) — `Types.lean`, `BehaviorModel.lean`, `DependentExpr.lean`, `Translation.lean`, `Checker.lean`, `Composition.lean` — including machine-checked soundness theorems (`checkEdge_sound`, `checkPath_sound`).

The Django-specific bits (Python extractor, `BehaviorModel.lean`) do not port to xylem. The **Lean machinery does**. Specifically: `Types.lean`, `Checker.lean`, `Composition.lean`, and the soundness theorems are contract-level abstractions independent of Django.

Extending the PoC for xylem means:
- Swap the ruff-based Python extractor for a `go/ast`-based Go extractor.
- Replace `BehaviorModel.lean` with a Go-specific behavior model (smaller than the Django version — no ORM, no field precision rules; just interface contracts and invariant references).
- Target the `scanner → queue → runner` 3-node graph.

**Important framing:** this is not a greenfield research project. The Lean soundness work is done and proven. This item is an engineering extension of existing work.

## Scope

**In scope:**
- `go/ast`-based extractor replacing the Python extractor.
- Go behavior model replacing `BehaviorModel.lean`.
- End-to-end verification on the 3-node graph (scanner → queue → runner).

**Out of scope:**
- New Lean theorems (`checkEdge_sound` and `checkPath_sound` are already proven).
- Full-repo verification (3 nodes is the target scope).
- Replacing Dafny kernels (#06, #09) — CGV targets composition boundaries, not leaf kernels.

## Deliverables

Work happens in an external repo (fork of `contract-graph-verifier` — name TBD, e.g. `cgv-xylem`):

1. Rust extractor rewrite targeting `go/ast`.
2. `prover/GoBehaviorModel.lean` replacing `BehaviorModel.lean`.
3. End-to-end verification run produces sound-or-counterexample output on the 3-node graph.
4. Integration with xylem — a `verify-graph` workflow phase invoking the CGV, gating merges on Layer 3 contract compliance.

## Acceptance criteria

- Lean checker returns sound-or-counterexample output on `scanner → queue → runner`.
- A deliberately introduced contract violation (e.g. `scanner` writing a state `queue` does not accept) is caught.
- CGV integrates as a workflow phase without exceeding reasonable latency budgets.

## Files to touch

External repo:
- `src/` — Rust extractor (rewritten).
- `prover/GoBehaviorModel.lean` — new.
- `prover/Checker.lean`, `Composition.lean` — reused, unchanged.

Xylem repo (only at the final integration step):
- New `.xylem/prompts/verify-graph/*.md` and workflow YAML updates.

## Risks

- **Multi-week scope.** Commit to this item only after #06–#09 prove the broader deterministic-assurance thesis.
- **Impedance mismatch between Go types and Lean types.** Needs careful translation in `GoBehaviorModel.lean`.
- **Maintenance burden of a separate repo.** Keep extractor output stable; only the behavior model evolves with xylem.

## Kill criteria

- If #06–#09 fail or are abandoned, re-assess whether CGV is worth the investment.
- If after 6 weeks the Go extractor cannot produce a usable graph, back out.

## Execution notes

**Same-LLM review concern:** Lean work is out of scope for any LLM-assisted track. Rust extractor rewrite is in scope but must be human-reviewed given the soundness-of-extraction implications.

**Protected surfaces:** Eventual workflow YAML integration would require governance notes; the majority of work happens in the external repo.

## References

- `~/repos/contract-graph-verifier/README.md` (the PoC)
- `~/repos/contract-graph-verifier/prover/Composition.lean` (reusable soundness theorems)
- `docs/research/assurance-hierarchy.md` §Layer 3
- `docs/research/literature-review.md` §de Moura (Veil as precedent for protocol-level graph verification)
