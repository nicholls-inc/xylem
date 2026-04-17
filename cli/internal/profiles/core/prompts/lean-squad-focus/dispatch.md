# `dispatch` phase (reference)

The `dispatch` phase of the `lean-squad-focus` workflow is a `type: command` phase whose
shell body lives inline in `lean-squad-focus.yaml`. This file is a human-readable
reference — the runner does **not** load it at execution time.

## What the shell does

1. Reads `.xylem/phases/<vessel-id>/analyze.output` (written by the preceding prompt
   phase).
2. Extracts lines starting with `PLAN: task=<name> target=<slug>` (capped at 4) and
   `UNKNOWN: <raw>` (unlimited, for transparency in the summary comment).
3. For each valid PLAN line, runs:

   ```bash
   xylem enqueue \
     --workflow lean-squad-<task> \
     --ref lean-squad://<target> \
     --id lean-squad-<task>-<slug>-focus-<unix-ts>-<index>
   ```

4. Posts a summary comment on the issue via `gh issue comment` listing dispatched
   vessels and any unrecognised requests, prefixed with :microscope:.
5. Removes the `lean-squad-focus` label with `gh issue edit`.

## Error tolerance

- If no PLAN lines are present, dispatches zero vessels and still removes the label plus
  posts the summary comment (the run was a no-op, the label should not re-trigger).
- If a PLAN line is malformed (missing `task=` or `target=`), it is silently skipped —
  the analyze prompt is responsible for well-formed output.
- If `xylem enqueue` rejects a workflow name because the sibling workflow is not
  present in the composed config, the shell halts via `set -eu` and the vessel is
  retried. Keep sibling workflows landed before relying on focus dispatch.

## Cap rationale

The 4-vessel cap prevents a single issue body from spawning a runaway batch. Operators
wanting more vessels can invoke the label multiple times with distinct bodies.
