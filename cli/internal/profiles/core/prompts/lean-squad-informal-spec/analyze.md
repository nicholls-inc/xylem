You are running the `lean-squad-informal-spec` workflow (Task 2 of the Lean Squad system).

See `https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md` for the upstream doctrine. The Lean Squad applies Lean 4 formal verification progressively and optimistically. You do **not** need prior formal-verification (FV) expertise — this phase writes plain English, no Lean syntax at all.

## Vessel context

- Vessel ID: {{.Vessel.ID}}
- Vessel ref: `{{.Vessel.Ref}}` (expected shape: `lean-squad://<target-slug>`)

## Your job in this phase

Determine **which target** this vessel is scoped to and whether an informal spec already exists for it. Nothing else.

### Step 1 — Parse the target slug

`{{.Vessel.Ref}}` is the authoritative source. Strip the `lean-squad://` prefix; what remains is the `<target-slug>`. A slug is a short kebab-case identifier (for example `parse-uri`, `hash-map-insert`). If the ref is empty, does not begin with `lean-squad://`, or the remainder is empty, stop and emit the exact standalone line `XYLEM_NOOP` with a one-line explanation that the ref is malformed.

### Step 2 — Resolve the target to a source path via `formal-verification/TARGETS.md`

Read `formal-verification/TARGETS.md`. It is a table produced by the `lean-squad-orient` workflow; each row lists a target name, its source path, and a status. Find the row whose name matches `<target-slug>` (case-insensitive; tolerate minor formatting such as spaces vs. hyphens). Record the source path you found.

If `formal-verification/TARGETS.md` does not exist, or no row matches the slug, emit `XYLEM_NOOP` with a one-line explanation (for example: "TARGETS.md missing — lean-squad-orient has not run yet" or "slug <x> not listed in TARGETS.md"). Do not guess a source path.

### Step 3 — Check whether this target is already specified

Check for an existing file at `formal-verification/specs/<target-slug>_informal.md`. If it exists and has non-trivial content (more than just a placeholder), emit the exact standalone line `XYLEM_NOOP` with a one-line explanation ("spec already exists for <target-slug>"). The tick will pick another target next run.

### Step 4 — Emit a structured summary

If all three steps above succeeded without emitting `XYLEM_NOOP`, produce a plain-text summary that later phases can read verbatim:

```
TARGET_SLUG: <slug>
SOURCE_PATH: <path-from-TARGETS.md>
TEST_PATHS:
- <path>
- <path>
COMMENT_SOURCES:
- <path-or-locator>
NOTES:
- <short note on where the function lives, its language, and any obvious edge cases to remember>
```

For `TEST_PATHS`, briefly search the repo for tests that exercise the source (for example, a sibling `*_test.go` file, a `tests/` directory, or a test path the TARGETS.md row hints at). It is fine to list zero or multiple entries; do not fabricate paths. For `COMMENT_SOURCES`, note any file that contains design commentary relevant to the target (docstrings, READMEs, adjacent design docs). Again, do not fabricate.

Do **not** start writing the informal spec in this phase. That is the job of `implement`.
