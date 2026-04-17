# 01: Enforce Invariant↔Test Coverage Mapping in CI

**Horizon:** Immediate
**Status:** Not started
**Estimated cost:** 1 day
**Depends on:** nothing
**Unblocks:** #02, #03, #04 (coverage gate catches regressions for all subsequent invariant work)

## Context

`docs/invariants/queue.md` §Governance.2 already requires every property test to carry a `// Invariant IN: <Name>` comment linking it to the spec entry. There is no CI check that enforces this. The scanner, runner, and queue invariant docs all have the same governance rule, and it is silently unenforced across all three.

This is the cheapest intervention that structurally closes the "silent test drop" failure mode: if an invariant exists in the doc but has no corresponding `// Invariant IN:` comment in a property test, the AI can claim coverage that does not exist. A mechanical check removes that possibility.

## Scope

**In scope:**
- A script (Go tool under `cli/cmd/` or Python script under `scripts/`) that parses all invariant IDs from `docs/invariants/*.md` and verifies each ID is referenced by at least one `// Invariant IN: <ID>` comment in a `*_invariants_prop_test.go` file.
- The script must also fail if a property test comment references an invariant ID that does not exist in any invariant doc.
- Pre-commit hook that runs the script on `docs/invariants/*` or `*_invariants_prop_test.go` changes.
- CI workflow step that runs the script on every PR.

**Out of scope:**
- Fixing missing coverage — that is item #04 for runner, and will be identified per-module as the check surfaces gaps.
- Changing the `// Invariant IN:` comment format.
- Running the property tests themselves — unit test execution stays as-is.

## Deliverables

1. New file `scripts/check_invariant_coverage.py` (or Go equivalent under `cli/cmd/xylem-check-invariants/main.go`). Prefer Go if the existing toolchain makes it easy to expose as a `go run` target.
2. Pre-commit hook entry under `.pre-commit-config.yaml` (if one exists) or `.git/hooks/pre-commit` invocation via whatever mechanism this repo uses.
3. CI workflow addition in `.github/workflows/*.yml` — a step that calls the script.
4. Documentation update in `docs/invariants/queue.md` (or a new `docs/invariants/README.md`) pointing to the enforcement mechanism so the governance rule is no longer stating an unenforced requirement.

## Acceptance criteria

- CI fails on a PR that adds a new invariant to any doc without a covering test comment.
- CI fails on a PR that deletes or renames a `// Invariant IN:` comment while the invariant still exists in the doc.
- CI fails on a PR that adds a `// Invariant IN: IX` comment where `IX` is not in any doc.
- Script runs in under 10 seconds across all three modules.
- The invariant docs no longer state a governance rule that is not mechanically enforced.

## Files to touch

- **New:** `scripts/check_invariant_coverage.py` or `cli/cmd/xylem-check-invariants/main.go`
- **New:** CI workflow step (likely in `.github/workflows/go-checks.yml` or similar)
- **Modified:** pre-commit hook config if one exists
- **Modified (minor, doc-only):** `docs/invariants/queue.md` §Governance.2 (pointer to mechanical check)
- **Read-only (input):** `docs/invariants/*.md`, `cli/internal/*/invariants_prop_test.go`, `cli/internal/*/*_invariants_prop_test.go`

## Risks

- **False positives on comment format variations.** Mitigate with a strict regex and documented format. Reject variations like `// Invariant: IN` or `// IN:` — the format must be exactly `// Invariant IN: <Name>`.
- **Invariant IDs with sub-labels (e.g. `I1a`, `I5b`) may confuse naive parsing.** Mitigate by parsing header lines in the docs strictly — anything matching `^### I\d+[a-z]?: .+` is an invariant header.
- **New invariants that are intentionally not-yet-covered (e.g. aspirational) will fail CI.** Mitigate by supporting an opt-out marker in the doc (e.g. `<!-- aspirational -->` on the header line) and documenting it.

## Kill criteria

Stop and re-plan if:
- After 4 weeks, the check produces more noise than signal (false positives, flakiness, slow builds).
- The script takes more than 2 days to build, indicating the problem is more complex than assumed and needs different tooling.

## Execution notes

**Same-LLM review concern:** This item is low-risk — the script is mechanical. A gpt-5-mini implementation followed by claude-opus `pr-self-review` is sufficient separation for this scope. No need for human review of the generated script if the acceptance criteria (which are mechanical) all pass.

**Protected surfaces:** None. Only adds new files and non-invasive CI steps. `.claude/rules/protected-surfaces.md` is not touched.

## References

- `docs/invariants/queue.md` §Governance.2 (source of the enforcement requirement)
- `docs/invariants/scanner.md`, `docs/invariants/runner.md` (parallel docs with same rule)
- `.claude/rules/protected-surfaces.md` (classifies invariant docs and property tests as protected)
