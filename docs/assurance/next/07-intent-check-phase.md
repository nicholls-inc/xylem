# 07: `intent-check` Workflow Phase (claimcheck-analog)

**Horizon:** Next (4–8 weeks)
**Status:** Complete — PR #698 merged 2026-04-23. Delivered: `xylem-intent-check` binary, prompts, attestation hook, seeded mismatch fixture, workflow YAML integration (fix-bug, implement-feature, implement-harness). FP tracking active: `docs/assurance/next/07-fp-tracker.csv`.
**Estimated cost:** 1 week
**Depends on:** nothing hard — can run in parallel with #06/#08/#09; benefits from #06 landing first (concrete artifact to reason about)
**Planned order:** after #09 (2026-04-20 decision — highest-risk item; #08 and #09 are faster and should land first)
**Unblocks:** #14 (spec-adversary reuses the same two-LLM plumbing)

## Context

Midspiral's `claimcheck` addresses Layer 5 (spec-intent alignment) using **round-trip informalization**: send the formal artifact to one LLM that is blind to the original intent, ask it to describe the guarantee in plain English, then send both the original intent and the back-translation to a second LLM that checks for semantic match. In Midspiral's testing this caught planted errors and unexpected gaps that a single-model review missed, reporting ~96.3% accuracy on a curated benchmark.

The pattern is language-agnostic. For xylem, the "formal artifact" is an invariant-doc entry plus its property test plus any relevant code. The "original intent" is the invariant doc's prose description. The two-LLM separation gives structural adversariality that single-model review cannot.

This item addresses the concern that motivated the whole roadmap: *the same LLM writes both the invariant doc and its covering property test.* `intent-check` forces a different LLM, blind to the original prose, to describe what the test actually guarantees — and a third model to diff those descriptions.

## Scope

**In scope:**
- New workflow phase `intent-check` defined in `.xylem/workflows/phases/intent-check.md` (prompt template).
- Integration into the three core-profile workflows (`fix-bug`, `implement-feature`, `implement-harness`).
- The phase runs **after** `implement` and **before** `pr_draft`. It reads the invariant doc(s) touched by the change, reads the covering property test(s) and code changes, asks the blind back-translator to describe the guarantees, and asks a second model to diff the back-translation against the original invariant prose.
- If the diff flags a mismatch above a configurable threshold, the phase fails the gate — the PR does not proceed to `pr_draft`.
- The back-translator and the diff-checker must use **different models** from both the original writer (implement phase) and from each other. Concretely: implement on `med` tier (gpt-5-mini), back-translator on `high` tier (claude-opus), diff-checker on a third provider if available — or the same tier but with an adversarial prompt if not.
- **Pre-commit attestation check** (per the dual-track enforcement principle): a fast, cheap pre-commit hook that verifies `xylem-intent-check` was run and passed before a commit touching protected-surface files is accepted. See attestation design below.

**Out of scope:**
- Layer 6 (spec completeness). This phase only checks *if the spec captures the intent*, not *if the intent covers all relevant properties*. That is #14.
- Replacing `test_critic`. `test_critic` catches test theatre at the code level; `intent-check` catches drift at the prose-vs-code level. They are complementary.
- Running `intent-check` on every PR unconditionally. It runs only when the change touches a file listed under `.claude/rules/protected-surfaces.md` (invariant docs or property tests), where spec-intent drift is most dangerous.

### Attestation design

The pre-commit hook must be fast (< 1 s) and must not invoke LLMs. It enforces that `xylem-intent-check` was run and passed by verifying a content-addressed attestation file.

**How it works:**

1. `xylem-intent-check` (the Go binary) runs the two-LLM pipeline and, on a passing result, writes `.xylem/intent-check-attestation.json` containing:
   - `protected_files`: sorted list of protected-surface file paths that were changed.
   - `content_hash`: SHA-256 of the concatenated contents of those files in sorted order.
   - `verdict`: `"pass"` or `"fail"`.
   - `checked_at`: RFC3339 timestamp.
   - `pipeline_output`: the back-translation and diff-checker verdict (human-readable — included to make the attestation non-trivially forgeable and useful for review).

2. The pre-commit hook (`scripts/check-intent-attestation.sh`):
   - Detects whether any staged files match the protected-surface patterns.
   - If no protected files changed: exits 0 immediately.
   - If protected files changed: reads `.xylem/intent-check-attestation.json`.
   - Recomputes the `content_hash` from the current on-disk state of the changed protected files.
   - Fails if: the attestation is absent, the `verdict` is not `"pass"`, or the recomputed hash does not match the stored hash.
   - On failure, emits: `"intent-check attestation missing or stale. Run: xylem intent-check — then re-stage .xylem/intent-check-attestation.json and retry the commit."`

3. The attestation file must be staged alongside the protected-surface change so the pre-commit hook sees the committed version.

**Why LLMs cannot bypass this without running the binary:**
- An LLM would need to (a) compute the correct SHA-256 over the exact file contents in the correct sort order, (b) produce plausible `pipeline_output` matching the actual back-translation the binary would generate, and (c) correctly JSON-encode the attestation. The `pipeline_output` field is the key friction: forging it requires essentially running the pipeline. An LLM following the error message instructions will run `xylem intent-check` rather than attempt to forge the attestation.

## Deliverables

1. `.xylem/prompts/intent-check/back_translate.md` — prompt for the blind back-translator (sees code and test only, never the invariant doc prose).
2. `.xylem/prompts/intent-check/diff.md` — prompt for the diff-checker (sees both the original invariant prose and the back-translation).
3. Phase block added to `.xylem/workflows/fix-bug.yaml`, `.xylem/workflows/implement-feature.yaml`, `.xylem/workflows/implement-harness.yaml`.
4. `cli/cmd/xylem-intent-check/main.go` — Go binary that runs the two-LLM pipeline and writes the attestation file on pass.
5. `scripts/check-intent-attestation.sh` — pre-commit hook script (fast, no LLMs). Registered in `.pre-commit-config.yaml` or equivalent. Error message must include the exact fix command.
6. Documentation in `docs/workflows.md` describing the phase, when it runs, and the attestation workflow.

## Acceptance criteria

- **Seeded mismatch test case.** Construct a test fixture where the invariant doc prose and the code/test diverge in a way claimcheck's paper documented (e.g. "adding a ballot cannot decrease the tally" — lemma proves only non-negativity). The phase must flag this mismatch. This is the minimum proof that the plumbing works.
- **Real-PR false-positive rate < 30%** after 2 weeks of live operation. Track with a simple CSV of every phase run: invariant touched, phase verdict, eventual human verdict.
- **No false negatives on the seeded case.** Missing the planted mismatch kills the item.

## Files to touch

- **New:** `.xylem/prompts/intent-check/*.md`
- **Modified:** three workflow YAMLs (**PROTECTED** per `.claude/rules/protected-surfaces.md` — requires human-authored amendment)
- **New:** `cli/cmd/xylem-intent-check/main.go`
- **New:** `scripts/check-intent-attestation.sh`
- **Modified:** `.pre-commit-config.yaml` (or equivalent hook registration)
- **Modified:** `docs/workflows.md`

## Risks

- **False-positive rate.** Claimcheck's 96.3% was on a curated benchmark; real-world operation on xylem's invariant docs will be noisier. Enforce the kill criterion strictly.
- **Latency.** A two-LLM pipeline adds two extra model calls per PR that touches a protected surface. Keep the back-translator prompt tight (< 1k tokens context) to bound latency.
- **Model separation may be illusory.** If both models are Claude Opus from Anthropic, a shared bias may pass them both. Mitigate by running the back-translator on claude-opus and the diff-checker on a different provider (or a different Claude model if no suitable alternative is configured).
- **Gate failure mode.** If the phase errors out (model unavailable, parse failure), it must not silently pass. Explicit fail-open vs fail-closed decision required — **default to fail-closed** for protected-surface PRs.

## Kill criteria

- FP rate > 30% after 2 weeks of real-PR runs.
- FN on the seeded mismatch fixture (fundamental plumbing failure).
- Latency > 10 minutes per invocation after tuning.

## Execution notes

**Protected surfaces:** The three workflow YAMLs are in the protected list. Amendments require human-authored changes with a governance note. Plan for two PRs: one for the Go helper + prompts (not protected); one for the workflow YAML integration (protected).

**Same-LLM review concern:** This *is* the solution to the same-LLM-review concern for protected-surface PRs. Meta-risk: if `intent-check` is written by gpt-5-mini and reviewed by claude-opus `pr-self-review`, the review is checking the tool that checks review separation. Human review recommended for the first PR introducing this phase.

## References

- `docs/research/literature-review.md` §Midspiral / claimcheck
- Midspiral claimcheck blog: https://midspiral.com/blog/claimcheck-narrowing-the-gap-between-proof-and-intent/
- `docs/research/assurance-hierarchy.md` §Layer 5
- `.claude/rules/protected-surfaces.md` (list of files that trigger intent-check)
