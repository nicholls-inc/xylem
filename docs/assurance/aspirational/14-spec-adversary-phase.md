# 14: `spec-adversary` Workflow Phase (Layer 6)

**Horizon:** Aspirational
**Status:** Not started
**Estimated cost:** 2 weeks (implementation) + ongoing iteration
**Depends on:** #07 (intent-check plumbing reused)
**Unblocks:** nothing — Layer 6 is open-ended

## Context

Layer 6 of the assurance hierarchy — spec completeness — has no production tool that works well today. Kleppmann and de Moura both frame it as the unresolved frontier. The idea in the literature-review companion work: an adversarial LLM proposes candidate invariants the current spec lacks; a human reviews; ratified invariants become new entries in `docs/invariants/*.md`.

This is explicitly an iterative practice, not a deterministic tool. The phase exists to lower the cost of discovering "properties we should have specified but did not." Success is not "zero missed properties" — success is "the adversary proposes at least one non-obvious candidate on each meaningful change."

## Scope

**In scope:**
- New workflow phase `spec-adversary` run during `analyze` for protected-surface changes (or as a standalone workflow triggered by a `needs-adversary-review` label).
- Adversary prompt — gets the touched module's invariant doc and current code; asked to propose properties that could hold but are not documented.
- PR comment format — proposed properties listed with "accept / reject / defer" radio buttons the human fills in during review.

**Out of scope:**
- Automated promotion into invariant docs — always requires human review and governance note.
- Adversary proposals on green-path PRs — only runs when protected surfaces are touched.
- Replacing `intent-check`. `intent-check` verifies the spec matches the code. `spec-adversary` asks what the spec is missing. Complementary.

## Deliverables

1. `.xylem/prompts/spec-adversary/propose.md`.
2. Phase integration in the core profile (likely as a post-implement sub-phase or a standalone workflow).
3. PR comment template.
4. Acceptance criterion tracker — log of proposals, acceptance rate, signal-to-noise ratio.

## Acceptance criteria

- Seeded-gap test case: given a module with a known missing invariant, adversary proposes it (or something close).
- Real-PR signal-to-noise ratio ≥ 1:5 after 4 weeks of operation. Kill otherwise.
- At least one ratified new invariant promoted into `docs/invariants/*.md` via the adversary within the first 8 weeks.

## Files to touch

- **New:** `.xylem/prompts/spec-adversary/*.md`
- **Modified:** workflow YAMLs (**PROTECTED**).
- **Modified over time:** `docs/invariants/*.md` as ratified proposals land (**PROTECTED** — each requires governance note).

## Risks

- **Low signal-to-noise.** Likely. Kill criterion is strict.
- **Promotion bypass.** If a ratified proposal is promoted without sufficient review, the invariant doc is polluted. Mitigate by requiring `pr-self-review` (claude-opus) AND human review for every new invariant entry.
- **Reviewer fatigue.** If adversary proposes 20 properties per PR, humans will rubber-stamp. Cap adversary output at 3 proposals per PR.

## Kill criteria

- Signal-to-noise < 1:5 after 4 weeks.
- No ratified proposals land within 8 weeks.

## Execution notes

**Protected surfaces:** Invariant docs and workflow YAMLs. Multiple governance notes required.

**Same-LLM review concern:** This phase *is* a form of adversarial review. Its output must still be reviewed by a claude-opus `pr-self-review` or a human, not accepted blindly.

## References

- `docs/research/literature-review.md` §closing paragraphs on Layer 6 and spec completeness
- `docs/research/assurance-hierarchy.md` §Layer 6
- `docs/assurance/next/07-intent-check-phase.md` (plumbing precedent)
