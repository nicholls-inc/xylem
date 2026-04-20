# 11: `acceptance-oracle` Workflow Phase

**Horizon:** Medium-term (2–3 months)
**Status:** Not started
**Estimated cost:** 2 weeks
**Depends on:** #07 (intent-check pattern established — acceptance-oracle reuses the phase-plumbing)
**Unblocks:** nothing — Layer 6 proxy

## Context

The assurance-hierarchy doc describes an **external acceptance oracle** as a practice that sits alongside the hierarchy: deterministic user-perspective verification steps written before development starts and stored outside the coding-agent-accessible repo. Its purpose is to measure whether the software works *from a user's perspective*, not from the author's. It complements formal verification: formal tools prove the spec is satisfied; the oracle measures whether the spec was the right spec.

xylem already has a DTU smoke environment (see `docs/dtu-manual-smoke-tests.md`, `docs/dtu-fixture-regression-tests.md`). The `acceptance-oracle` workflow phase wires the DTU environment into the core profile so that every PR touching observable user behavior runs against scenarios the coding agent cannot modify.

## Scope

**In scope:**
- New workflow phase `acceptance-oracle` run after `pr_draft` and before merge.
- External oracle harness — scenarios live in a separate directory or repo that the coding agent does not have write access to.
- Coverage of the top-10 user-observable flows (CLI commands, daemon behaviors, GitHub-integration flows).
- Gate failure blocks merge.
- **CI job** that runs oracle scenarios on every PR touching user-observable source paths — regardless of whether the change came through xylem, a human author, or an emergency patch (per the dual-track enforcement principle). Path filter targets `cli/` and `workflows/`. Emits a human-readable failure message with the exact command to reproduce the scenario locally.

**Out of scope:**
- Unit-level acceptance tests — those are within-repo and covered by existing property tests.
- Subjective criteria ("the UX feels good") — oracle scenarios must be **mechanically verifiable**. Anything subjective is quantified or rejected.
- Writing new DTU fixtures — item reuses existing fixtures plus any gaps identified during scoping.
- A pre-commit hook — oracle scenarios require a full runtime environment (DTU) and are too slow for pre-commit. CI is the correct enforcement boundary for this check.

## Deliverables

1. External oracle harness — scenarios as JSON or YAML files, plus a runner script.
2. `.xylem/prompts/acceptance-oracle/run.md` — phase prompt (may just be a command gate).
3. Phase block in `fix-bug`, `implement-feature`, `implement-harness`.
4. Top-10 flows scoped and documented in `docs/assurance/medium-term/11-acceptance-oracle-scenarios.md`.
5. CI job in `.github/workflows/ci.yml` — `acceptance-oracle` job, path-filtered to `cli/**` and `workflows/**`. Invokes the oracle runner script. On failure, emits the oracle scenario name(s) that failed and the command to reproduce locally: `scripts/run-oracle.sh <scenario>`.

## Acceptance criteria

- Phase runs on every `implement-feature` vessel; failures block merge.
- At least 3 scenarios active within 4 weeks (kill criterion), 10 within 12 weeks.
- A deliberately introduced regression in a CLI command triggers the gate.

## Files to touch

- **New:** external oracle directory (location TBD — could be a sibling directory outside the main working tree, or a separate repo).
- **New:** `scripts/run-oracle.sh` — oracle runner invoked by both the CI job and the xylem phase.
- **New:** `.xylem/prompts/acceptance-oracle/*.md`
- **Modified:** three workflow YAMLs (**PROTECTED**).
- **Modified:** `.github/workflows/ci.yml` (new `acceptance-oracle` job).
- **New:** `docs/assurance/medium-term/11-acceptance-oracle-scenarios.md`.

## Risks

- **Oracle drift.** Scenarios go stale as features evolve. Mitigate with quarterly review and a clear ownership model.
- **External harness access.** The coding agent must NOT be able to modify the oracle scenarios. Enforce via filesystem permissions or separate repo — not via convention.
- **Phase latency.** If scenarios run for more than a few minutes, PRs stall. Budget accordingly; parallelize where possible.

## Kill criteria

- If fewer than 3 scenarios are active after 4 weeks of implementation work, re-scope — the top-10 list was wrong.
- If the oracle flags more than 30% false positives, the scenarios are too brittle; simplify.

## Execution notes

**Protected surfaces:** Workflow YAMLs. Requires governance note.

**Same-LLM review concern:** Oracle scenarios are authored by humans (or designed by humans and filled in by LLM), not by the coding agent. This is the whole point — external to the write/review loop.

## References

- `docs/research/assurance-hierarchy.md` §Supporting Workflow Elements (External Acceptance Oracle)
- `docs/dtu-manual-smoke-tests.md`
- `docs/dtu-fixture-regression-tests.md`
- `docs/assurance/next/07-intent-check-phase.md` (plumbing precedent)
