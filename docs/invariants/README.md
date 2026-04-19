# Module invariant specifications

This directory holds the load-bearing behavioral contracts for xylem's core modules:

- [`queue.md`](queue.md) — the Vessel queue (enqueue/dequeue/state transitions/recovery)
- [`runner.md`](runner.md) — phase execution, gates, retries, timeouts
- [`scanner.md`](scanner.md) — source polling, dedup, enqueue

Each module's invariants are numbered (`I1`, `I1a`, `I2`, …; scanner uses `S1`, `S2`, …) and tested in the matching `cli/internal/<module>/*_invariants_prop_test.go` file.

These documents and their property tests are **protected surfaces** (see [`.claude/rules/protected-surfaces.md`](../../.claude/rules/protected-surfaces.md)): they must not be modified, deleted, or weakened without an explicit human-authored amendment.

## Coverage enforcement

Every invariant declared in a module spec **must** be referenced by at least one `// Invariant <ID>: <Name>` comment in the module's property-test file, and every such comment **must** refer to an invariant declared in that module's spec.

This is enforced mechanically by [`scripts/check_invariant_coverage.py`](../../scripts/check_invariant_coverage.py), which runs:

- as a **pre-commit hook** (see `.pre-commit-config.yaml`, hook id `invariant-coverage`)
- as a **CI job** (see `.github/workflows/ci.yml`, job `invariant-coverage`)

The check fails the build if a declared invariant has no covering comment (missing coverage) or if a test comment refers to an invariant that is not declared in the module doc (orphan comment).

### Header format (in the doc)

Invariants are declared with a bold-wrapped header at the start of a line:

```markdown
**I1. At-most-one active per ref.** …prose…
**I1a. Enqueue of an active ref is a no-op.** …prose…
**S3. Pause marker aborts the tick before any side effect.** …prose…
```

The regex is strict: `^\*\*([A-Z]+\d+[a-z]?)\.\s` — anything that doesn't match is not recognised as an invariant declaration.

### Comment format (in the test)

Above the test that enforces the invariant:

```go
// Invariant I1: At-most-one active per ref.
func TestPropQueueInvariant_I1_AtMostOneActivePerRef(t *testing.T) { … }
```

The regex is strict: `^//\s*Invariant\s+([A-Z]+\d+[a-z]?):\s`. Variations like `// Invariant: I1`, `// I1:`, or IDs without a digit (e.g. `Ix`) are not recognised.

IDs must contain a digit by construction. A typo that drops the digit (e.g. renaming a test comment from `I1` to `Ix`) is invisible to the regex: the check will surface it as **missing coverage on the original ID** (`I1`), not as an orphan comment on `Ix`. This is intentional — the regex refuses to guess at malformed labels — but it means test comments should be grepped whenever an invariant is renamed.

### Coverage scoping

IDs are scoped **per module**. `queue.md`'s `I1` and `runner.md`'s `I1` are different invariants; each is expected to be covered in its own package's property test.

### Aspirational opt-out

If an invariant is declared in the spec but is intentionally not yet covered by a property test, mark the header with the literal HTML comment `<!-- aspirational -->` on the same line:

```markdown
**I14. Planned but not yet enforced.** <!-- aspirational -->
```

Aspirational invariants are skipped by the missing-coverage check. **Orphan-comment checks still apply** — a test comment whose ID is not declared in the module doc is always an error.

Use this marker sparingly: every aspirational entry is a standing IOU against the module's guarantees.

## Adding a new invariant

1. Write the prose in the module's spec with a `**IN. Name.**` header.
2. Add a property test in `cli/internal/<module>/*_invariants_prop_test.go` with a `// Invariant IN: Name.` comment above it.
3. Run `python3 scripts/check_invariant_coverage.py` locally (or let the pre-commit hook run it).
4. Commit both files together.

If you cannot yet write the test, mark the header with `<!-- aspirational -->` and file an issue to cover it.

## Removing an invariant

Removing an invariant is a human-authored amendment, not a mechanical change. Propose the removal in a PR description, justify it against the module's guarantees, and obtain human review before merging. The enforcement check does **not** prevent deletion — it only prevents silent drift between the spec and the tests.
