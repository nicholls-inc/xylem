# 15: `intent-check` Calibration — FP Reduction & Eval Corpus

**Horizon:** Next (4–8 weeks)
**Status:** Not started
**Estimated cost:** 1–2 weeks (phased; Phase 1 is ~1 day)
**Depends on:** #07 (intent-check phase landed in PR #698)
**Unblocks:** sustained operation of the #07 kill criterion (real-PR FP rate < 30%)

## Context

The `xylem-intent-check` binary landed in PR #698 (roadmap item #07). Its FP kill criterion is **> 30% on real PRs after 2 weeks**. This item tracks the work needed to keep FPs well below that bar as usage scales, and to give us a measurable feedback loop on prompt and model changes.

First real-invariant exercise (2026-04-23, this session, `docs/invariants/queue.md`) produced 6 labelled findings:

| # | Finding | Ground truth |
|---|---|---|
| 1 | I2 `StateFailed` not sampled as terminal | Genuine |
| 2 | I2 mutation covers only 6/19 protected fields | Genuine |
| 3 | I3 (planted) — spec claims ✓ but code violates | Genuine (synthetic) |
| 4 | I6 test is concurrency-sanity, not linearizability | Genuine |
| 5 | I2 test exercises UpdateVessel but not ReplaceAll | Partial (framing also off) |
| 6 | I5b uses `vesselSetsEquivalentIgnoringClock` — not "exact" match | **Spurious** |

Observed FP rate: 1/6 (17%) — below kill threshold but above the level that builds trust. Root cause of the spurious finding is analysed in *Root-cause analysis* below.

## Root-cause analysis of FP #6

The test at `cli/internal/queue/queue_invariants_prop_test.go:452-457` carries a 6-line rationale block explaining *why* clock values are zeroed before equality comparison (wall-clock drift between reference run and crash run is a test-infrastructure artefact, not a semantic divergence). The back-translator described `vesselSetsEquivalentIgnoringClock`'s *behaviour* accurately but did not privilege the *rationale comment* as authoritative. The diff-checker then compared the literal behaviour ("ignores clock values") against the spec's literal wording ("pre-call or post-call state exactly") and declared a substantive gap.

Two systemic failure modes are implicated:

1. **Rationale blindness** — the pipeline does not treat in-source justification comments as first-class evidence.
2. **Carve-out blindness** — the diff-checker captured the spec's main claim but did not always weigh conditional scope modifiers ("caller-responsibility", "privileged path", "Not covered", "aspirational", "known violation") when deciding whether a gap was substantive. Finding #5's framing suggests this failure mode is latent even on findings that happen to be partially correct.

## Scope

**In scope:**

- Prompt tuning for rationale extraction and carve-out detection (Phase 1).
- A `confidence` field in `intentcheck.DiffResult` and the attestation schema (Phase 1).
- A labelled eval corpus under `cli/internal/intentcheck/testdata/eval/` seeded with this session's 6 findings (Phase 2).
- An `xylem-intent-check eval` subcommand that runs the pipeline against the corpus and reports precision/recall (Phase 2).
- CI regression gate on eval-corpus metrics so prompt/model changes cannot silently regress FP rate (Phase 2).
- Optional third-phase "judge-the-judge" spurious-finding review (Phase 3, only if Phase 1+2 do not hit targets).
- Optional spec/test annotation protocol for explicit author intent (Phase 3).

**Out of scope:**

- Layer 6 spec completeness — that is #14 (`spec-adversary`).
- Replacing the pipeline with a single-model alternative — the two-LLM structural adversariality is the core property; FP tuning does not abandon it.
- Tuning LLM provider routing beyond the existing `back-translate-model` / `diff-check-model` flags. Multi-provider routing is a separate concern.

## Deliverables (phased)

### Phase 1 — Prompt & schema tuning (~1 day)

1. **`back_translate.md`** (PROTECTED per `.claude/rules/protected-surfaces.md`): extend with a *Rationale* extraction step. Instruct the back-translator to lift any comment block longer than ~3 lines that justifies a design choice, and include those verbatim in a dedicated output section the diff-checker will consume.
2. **`diff.md`** (PROTECTED): add a carve-out pre-check listing the conditional-scope keywords the LLM must scan the invariant prose for before flagging a substantive gap (`exempt`, `caller-responsibility`, `privileged`, `Not covered`, `aspirational`, `known violation`). Any alleged gap falling inside such a clause must be classed as non-substantive.
3. **`intentcheck.DiffResult`**: add `Confidence string` field (`"high" | "medium" | "low"`). Update JSON schema, Go struct, `ParseDiffResult`, and the attestation payload. Binary exit code: fail on `high` confidence mismatches, warn (exit 0 with non-empty stderr note) on `medium`/`low`.

### Phase 2 — Eval corpus & regression gate (~3 days)

4. **`cli/internal/intentcheck/testdata/eval/`**: seed with this session's 6 fixtures. Each fixture is a directory containing the spec snippet, code snippet, test snippet, and an `expected.json` file recording the ground-truth label (`genuine`, `spurious`, `partial`, `planted`) and the mismatch text.
5. **`xylem-intent-check eval` subcommand**: runs the full pipeline against each fixture, computes precision/recall/confusion matrix, reports to stdout and writes a JSON summary.
6. **CI regression gate**: GitHub Actions job that runs `xylem-intent-check eval` on PRs touching `.xylem/prompts/intent-check/*.md` or `cli/cmd/xylem-intent-check/**` or `cli/internal/intentcheck/**`. Fail the PR if FP rate on the corpus regresses.

### Phase 3 — Structural changes (trigger only if Phase 1+2 do not hit targets)

7. **Judge-the-judge phase**: optional third LLM call, scoped to spurious-finding detection. Input: the mismatch reason + the raw test code. Output: `{spurious: bool, reason: string}`. A `spurious: true` verdict flips the final verdict to pass with an attested note.
8. **Spec/test annotation protocol**: define `<!-- intent-check: ... -->` (spec) and `// intent-check: rationale: ...` (test) markers. The pipeline strips and forwards these to the diff-checker as high-priority evidence.

## Acceptance criteria

| Metric | Baseline (this session, n=6) | Phase 1 exit | Phase 2 exit |
|---|---|---|---|
| FP rate on eval corpus | 17% (1/6) | < 10% | < 5% |
| Recall on genuine gaps | 100% (4/4 including plant) | ≥ 95% | ≥ 95% |
| FP tracker live entries | 0 | ≥ 10 | ≥ 25 |
| Attestation carries confidence | No | **Yes** | Yes |
| Attestation carries rationale extract | No | **Yes** | Yes |
| Eval CI gate present | No | No | **Yes** |

Phase 1 is considered "complete enough to continue" when FP #6's fixture no longer flips the intent-check to fail on unmodified code.

## Kill criteria

- Phase 1 prompt changes do not reduce FP rate on the eval corpus — escalate directly to Phase 3 without waiting for Phase 2.
- Eval corpus is itself miscalibrated (the model is sandbagging known-genuine findings to improve apparent precision). Detected via spot-check on real-PR runs diverging from corpus metrics.
- Confidence field correlates trivially with match (e.g. all matches are `high`, all non-matches are `low`). Would mean the LLM is not using the field as a genuine calibration signal; reconsider schema.

## Files to touch

- **New:** `docs/assurance/next/15-intent-check-calibration.md` (this doc)
- **Modified (PROTECTED, requires human-signed commit):** `.xylem/prompts/intent-check/back_translate.md`
- **Modified (PROTECTED, requires human-signed commit):** `.xylem/prompts/intent-check/diff.md`
- **Modified:** `cli/internal/intentcheck/intentcheck.go` (DiffResult schema + ParseDiffResult)
- **Modified:** `cli/cmd/xylem-intent-check/main.go` (attestation payload, `eval` subcommand, JSON schema)
- **New:** `cli/internal/intentcheck/testdata/eval/*/` (6 seed fixtures)
- **Modified:** `.github/workflows/ci.yml` (eval regression gate)
- **Modified:** `docs/assurance/ROADMAP.md` (add row for #15)
- **Modified:** `docs/assurance/next/07-fp-tracker.csv` (seed 6 entries from this session)

## Risks

- **Prompt changes on a protected surface.** Both prompt files are in `.claude/rules/protected-surfaces.md`. Phase 1 must go in as a human-signed PR. Agent-authored draft is permitted; merge requires a signed commit.
- **Corpus curation risk.** If the eval corpus is authored by the same people/models that produced the findings it grades, it inherits their blind spots. Mitigation: add independently-sourced fixtures over time, and require ground-truth labels on eval fixtures to be human-reviewed before landing.
- **Schema migration.** Adding `Confidence` to `DiffResult` changes the attestation format. Old attestations must either round-trip safely or be re-generated. Choose: (a) treat absence as `high` for back-compat, or (b) bump attestation schema version and reject legacy files in the pre-commit hook.
- **Judge-the-judge overhead.** A third LLM call per run adds latency and cost. Gate it on a confidence threshold rather than running it unconditionally.

## Execution notes

- Phase 1 can be cut as a single PR. Phase 2 is ~3 days and should be its own PR chain. Phase 3 is scoped only if Phase 1+2 fall short.
- The 6 fixtures from this session are not hypothetical — they come from a real `docs/invariants/queue.md` run. Seed them before doing anything else; they anchor the acceptance criteria.
- Coordinate with issue #700–#703: as those PRs land and close the underlying gaps, the eval-corpus fixtures for findings #1/#2/#4/#5 remain useful (they exercise the detector regardless of whether the gap is closed in prod).

## References

- `docs/assurance/next/07-intent-check-phase.md` — parent item (complete via PR #698)
- `docs/assurance/next/07-fp-tracker.csv` — live FP tracker (seeded by this item)
- `cli/cmd/xylem-intent-check/` — binary source
- `cli/internal/intentcheck/` — pure-logic package + invariant spec
- `.xylem/prompts/intent-check/` — prompt templates (protected)
- Issues #700, #701, #702, #703 — genuine findings from the calibration session
