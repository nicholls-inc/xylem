You are running the `lean-squad-extract-impl` workflow (Task 4 of the Lean Squad system).

See `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md` for the upstream doctrine. The Lean Squad applies Lean 4 formal verification progressively and optimistically. You do **not** need prior formal-verification (FV) expertise — this phase only reads files and decides whether the workflow should proceed.

## Vessel context

- Vessel ID: {{.Vessel.ID}}
- Vessel ref: `{{.Vessel.Ref}}` (expected shape: `lean-squad://<target-slug>`)

## Your job in this phase

Decide whether there is work for this workflow to do for the target named in the vessel ref, and if so, collect the source paths the `implement` phase will need. Do not write Lean code in this phase.

### Step 1 — Parse the target slug

`{{.Vessel.Ref}}` is the authoritative source. Strip the `lean-squad://` prefix; what remains is the `<target-slug>`. A slug is a short kebab-case identifier (for example `parse-uri`, `hash-map-insert`). If the ref is empty, does not begin with `lean-squad://`, or the remainder is empty, stop and emit the exact standalone line `XYLEM_NOOP` with a one-line explanation that the ref is malformed.

### Step 2 — Resolve the target to a source path via `formal-verification/TARGETS.md`

Read `formal-verification/TARGETS.md`. It is a table produced by the `lean-squad-orient` workflow; each row lists a target name, its source path, and a status. Find the row whose name matches `<target-slug>` (case-insensitive; tolerate minor formatting such as spaces vs. hyphens). Record the source path.

If `formal-verification/TARGETS.md` does not exist, or no row matches the slug, emit `XYLEM_NOOP` with a one-line explanation ("TARGETS.md missing — lean-squad-orient has not run yet" or "slug <x> not listed in TARGETS.md"). Do not guess a source path.

### Step 3 — Check the Lean file exists

The Lean file for this target lives at `formal-verification/lean/FVSquad/<CamelCaseTarget>.lean`, where `<CamelCaseTarget>` is the CamelCase form of `<target-slug>` (for example `parse-uri` -> `ParseUri`). That file is the output of `lean-squad-formal-spec` (Task 3). Read the whole file.

If the Lean file does not exist, emit `XYLEM_NOOP` with a one-line explanation ("Lean file for <slug> not found — lean-squad-formal-spec has not run yet"). `lean-squad-extract-impl` is strictly downstream of `lean-squad-formal-spec` and must not fabricate a Lean scaffold.

### Step 4 — Decide whether there is a sorry-stubbed function body to fill

Inside the Lean file, there will typically be one or more `def` or `partial def` declarations (function bodies) and one or more `theorem` or `lemma` declarations (proof obligations). Both may currently be `sorry`-stubbed.

This workflow **only** fills function bodies. Theorem `sorry`s are owned by `lean-squad-prove` (Unit 6).

Scan the file and determine:
- Does at least one `def` / `partial def` / `def ... : ... :=` have a body of `sorry` (possibly wrapped in `by sorry`)?
- If **every** `sorry` in the file is attached to a `theorem` or `lemma` declaration (and not to a `def`), emit the exact standalone line `XYLEM_NOOP` with the one-line explanation "no sorry in function bodies — theorem sorries are owned by lean-squad-prove".

A quick textual heuristic is acceptable: grep for `sorry` and inspect the surrounding declaration keyword. When in doubt, lean conservative and emit `XYLEM_NOOP`; a later tick will redispatch when truly needed.

### Step 5 — Locate tests and comment sources

If you passed Step 4, briefly search for:
- Tests that exercise the source (sibling `*_test.*` file, `tests/` directory, etc.).
- Comment-bearing files (docstrings, adjacent READMEs, design notes). Worth noting but optional.

Do not fabricate paths.

### Step 6 — Emit a structured summary

If all prior steps succeeded without emitting `XYLEM_NOOP`, produce a plain-text summary that `implement` can read verbatim:

```
TARGET_SLUG: <slug>
SOURCE_PATH: <path-from-TARGETS.md>
LEAN_PATH: formal-verification/lean/FVSquad/<CamelCaseTarget>.lean
TEST_PATHS:
- <path>
- <path>
COMMENT_SOURCES:
- <path-or-locator>
FUNCTION_SORRIES:
- <short identifier of each def-level sorry found, e.g. `def parseUri` line N>
NOTES:
- <short note on the source language and any tricky bits the translator should know>
```

Do **not** start writing Lean function bodies in this phase. That is the job of `implement`.
