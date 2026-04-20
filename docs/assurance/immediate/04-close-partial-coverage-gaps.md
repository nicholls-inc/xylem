# 04: Close Partial-Coverage Gaps in Runner Property Tests

**Horizon:** Immediate
**Status:** In progress (PR #675 merged 2026-04-19 — gaps A+D done; PR #680 open 2026-04-19 — gaps B+C; gaps E+F in current PR; issues #677/#676/#679/#678)
**Estimated cost:** 1–2 weeks (6 small PRs)
**Depends on:** #01 (coverage CI helps catch regression after each PR lands)
**Unblocks:** #10 (Gobra needs a clean invariant-test baseline first)

## Context

An audit of `cli/internal/runner/runner_invariants_prop_test.go` (conducted during the research phase that produced this roadmap) found 14 partial-coverage gaps. "Partial" here means the property test exists and references the invariant via `// Invariant IN:` comment, but the test's generator or assertion does not exercise the full behavior the invariant describes.

The six highest-impact gaps, selected because each one is small enough to fit in a single PR, each corresponds to one invariant, and each is independently mergeable:

| Gap | Invariant | Symptom |
|-----|-----------|---------|
| A | Runner I1 (per-class concurrency) | Test exercises global concurrency cap but not per-class slot accounting. |
| B | Runner I3 (mid-phase cancellation) | Test cancels only at phase boundaries; cancel during phase execution is not covered. |
| C | Runner I6 (label gate waiting) | Test asserts `waiting` state transition but does not sample the label poll interval. |
| D | Runner I8 (mid-run sampling) | Test asserts pre/post state but does not sample state during a long-running vessel. |
| E | Runner I11 (`CheckStalledVessels`) | Test asserts output but never traverses the full stalled-vessel path that `CheckStalledVessels` guards. |
| F | Runner I14 (rehydration after restart) | Test does not exercise the rehydration code path — it asserts on state that would be reconstructed but was never dehydrated. |

## Scope

**In scope:**
- One PR per gap (A–F). Each PR:
  - Strengthens the corresponding property test so it actually exercises the invariant's full behavior.
  - Must be **RED** against the current `runner` code if the invariant is not enforced, or **GREEN** if the code is correct and only the test was too weak.
  - Must land independently — no cross-PR dependencies.

**Out of scope:**
- Queue invariant audit (I9 is #02; a broader queue audit is a follow-up item not yet scoped).
- Scanner invariant audit (S1–S8 are all covered per the initial audit; S4/S5 are correctly `t.Skip`'d against known code violations).
- Runner invariants I4, I7, I9 — these are correctly `t.Skip`'d against documented-pending code work. Do not re-enable them here.

## Deliverables

Six merged PRs, one per gap. Each PR must include:
1. The strengthened property test (touching `cli/internal/runner/runner_invariants_prop_test.go` — **PROTECTED**).
2. If the test reveals a code bug: the minimal fix in `cli/internal/runner/`.
3. If the test reveals only a gap in the test itself (no code bug): the PR description must explicitly state that no code change was required and why the weaker test was not catching the full invariant.
4. A reference to `docs/invariants/runner.md` § for the invariant in question, confirming the strengthened test now exercises the full scope.

## Acceptance criteria

Per PR:
- `go test ./cli/internal/runner -run <TestName>` against the test change alone (no code change) either: (a) fails RED because the invariant is not enforced, then passes GREEN after the code fix; or (b) passes because the invariant is already correctly enforced and the original test was too weak.
- The PR cannot be "passed by making the test trivially true." This is explicitly checked by `pr-self-review` in the governance-note section of the PR description.

Across all six:
- All six PRs merged within 2 weeks of start.
- No regressions in non-runner test suites.
- Coverage CI (#01, if landed) does not flag new uncovered invariants.

## Files to touch

- `cli/internal/runner/runner_invariants_prop_test.go` (**PROTECTED** — strengthening only, not relaxing)
- `cli/internal/runner/runner.go` and supporting files (as needed per gap)
- No doc changes expected unless an invariant's §"current limitations" section needs updating after a code fix lands.

## Protected-surface note

Each PR touches a protected property-test file. Same rules as #02:
- PR description must explicitly state the test is being strengthened.
- Must cite `docs/invariants/runner.md` §Governance.
- `pr-self-review` prompt must verify strengthening, not relaxation.

If `pr-self-review` (claude-opus) cannot confidently certify strengthening, the PR routes to human review.

## Risks

- **Gap B (mid-phase cancellation) may require runner-harness changes** to make mid-phase cancel observable. If so, factor out the harness change as a separate preparatory PR.
- **Gap F (rehydration after restart) is state-machine-heavy** and could overlap with #10 (Gobra). Keep it isolated — ship the property test as a rapid test, do not jump to formal specs in this item.
- **One PR may block another if they touch the same test-helper function.** Order by dependency: A → E → others (A is pure generator change; E touches the stalled-vessel helper; others are independent).

## Kill criteria

- If after 2 weeks fewer than 4 of the 6 PRs are merged, stop and re-plan — the remaining gaps may not be "partial" but actually "deferred by design."
- If any gap reveals an invariant that is not implementable without a significant refactor, close the gap as a separate feature issue and mark the roadmap item's status `Partially done — N/6`.

## Execution notes

**Parallelizable within xylem:** Each gap can be filed as its own GitHub issue with the `bug` label (the gap is a test bug — or a code bug if the strengthened test fails). The xylem daemon will pick them up in parallel via `fix-bug` workflow.

**Same-LLM review concern:** Runner code is more complex than queue; PRs that only strengthen tests are low-risk, but PRs that fix code bugs discovered by the strengthened test must go through `pr-self-review` AND a human eyeball on the code fix before merge.

## References

- `docs/invariants/runner.md` §I1, I3, I6, I8, I11, I14 (invariant definitions)
- `cli/internal/runner/runner_invariants_prop_test.go` (tests to strengthen)
- `.claude/rules/protected-surfaces.md` (governance)
- `docs/invariants/runner.md` §Governance (strengthening rules)
