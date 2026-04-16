You are running the `implement` phase of `lean-squad-informal-spec` (Task 2 of the Lean Squad).

See `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md` for the upstream doctrine. The informal spec is the **first rung on the FV ladder**: a carefully worded English description of what the target is supposed to do, written so that a later phase (`lean-squad-formal-spec`) can translate it into Lean 4 without having to re-read the source from scratch. No Lean syntax appears in this file.

## Vessel context

- Vessel ID: {{.Vessel.ID}}
- Vessel ref: `{{.Vessel.Ref}}`

## Prior-phase output (authoritative)

The `analyze` phase parsed the slug, located the source, and listed tests and comment sources. Use its output below verbatim; do not re-derive these paths.

```
{{.PreviousOutputs.analyze}}
```

From that output you have:
- `TARGET_SLUG` — the kebab-case identifier for this target
- `SOURCE_PATH` — the implementation file
- `TEST_PATHS` — zero or more test files
- `COMMENT_SOURCES` — docs or comment-bearing files

## Your job in this phase

Write a single file at `formal-verification/specs/<TARGET_SLUG>_informal.md`. Overwrite any placeholder that might exist. The file is **plain English mathematical prose**. No Lean, no pseudo-code blocks masquerading as Lean.

### How to read the source

1. Read `SOURCE_PATH` end to end first.
2. Read each `TEST_PATHS` file. Tests are the richest source of worked examples — when a test says `f(3, 4) == 7`, that is a concrete input/output pair that belongs in the spec's "Worked examples" section.
3. Skim the `COMMENT_SOURCES`. Pull out design constraints that are only stated there (for example, "this function must be idempotent", "callers rely on stable iteration order").

If something is unclear — an edge case the source does not handle explicitly, an invariant the tests hint at but do not prove — write "Unclear — see [open question]" and add an "Open questions" subsection at the bottom. A spec with honest gaps is far more useful than a spec that guesses.

### File structure (use these exact headings)

```markdown
# Informal specification: <target-slug>

## Source

- Implementation: `<SOURCE_PATH>`
- Tests consulted: `<path>`, `<path>`, ...
- Other comment sources: `<path>`, ... (or "none")

## Summary

One or two sentences describing, in plain English, what the function or module does. A reader with no Lean knowledge should understand this line.

## Preconditions

What the caller must guarantee before calling. One bullet per precondition. Be explicit about types and ranges. A precondition is a promise **about the input** that the function does not re-check.

- Example: "The input list must be non-empty."
- Example: "`capacity` is a positive integer not exceeding `2^31 - 1`."

If no preconditions apply, write "None — any well-typed input is accepted."

## Postconditions

What the function promises to return, assuming preconditions held. A postcondition is a promise **about the output** (or side effects). One bullet each.

- Example: "The returned list is a permutation of the input list."
- Example: "No element of the returned list exceeds the maximum of the input."

## Invariants

Properties that hold throughout the function's execution or across successive calls. An invariant can be a data-structure invariant ("the internal buffer is never longer than `capacity`") or a temporal one ("calling `insert` twice with the same key is idempotent").

If the target has no interesting invariants, write "None beyond the preconditions and postconditions above."

## Edge cases

A bulleted list of inputs at the boundary of the precondition space and what the function does for each. These are the scenarios where bugs hide.

- Empty input, zero-valued input, maximum-sized input, duplicate input, already-sorted input, etc. — pick whatever is relevant.
- For each bullet, state the expected behaviour concretely.

## Worked examples

At least two concrete input/output pairs drawn from the tests where possible. Prefer tests because they are already verified by the existing test suite. Use a simple format:

- Input: `<literal>` &rarr; Output: `<literal>` — short sentence of why.

If no tests exist, write examples you can verify by tracing through the source by hand, and mark them "(traced, not test-verified)".

## Open questions

Only include if `analyze` or your reading of the source surfaced ambiguity. Each bullet names the ambiguity in one line. Keep this short and honest.

## 🔬 Disclosure

This informal specification was produced by the xylem `lean-squad-informal-spec` workflow, a port of the agentics Lean Squad system (see `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md`). It is a plain-English description intended for human review and for downstream translation into Lean 4 by `lean-squad-formal-spec`. It has not been mechanically checked; inaccuracies are possible.
```

### Reminders

- **Plain prose only.** No `theorem`, no `def`, no `∀`, no backticked Lean. If you feel the urge to write Lean, resist — that is Task 3's job.
- **Cite tests.** Every bullet in "Worked examples" should correspond to a test case where one exists.
- **Be conservative.** If the source seems to have a bug or a surprising behaviour, describe what the source **actually does**, and flag the surprise in "Open questions". Do not silently rewrite the contract.
- **One file only.** Write exactly `formal-verification/specs/<TARGET_SLUG>_informal.md`. Do not touch any other file — the `lean-squad-informal-spec` workflow is the sole writer of this path.
- Do **not** commit, push, or open a PR in this phase. That is `pr`'s job.
