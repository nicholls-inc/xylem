🔬 *Lean Squad — `lean-squad-status` / `update` phase.*

This phase is `type: command`, so the logic lives inline in
`.xylem/workflows/lean-squad-status.yaml`. This file is a human-readable companion.

## Purpose

Replace the body of the dashboard issue identified by the `analyze` phase with a
compact, up-to-date snapshot of formal-verification state in this repo.

## Dashboard contents

- **Targets** — top portion of `formal-verification/TARGETS.md` (sole writer:
  `lean-squad-orient`). If the file is absent, a hint points contributors at
  `lean-squad-orient`.
- **Open [Lean Squad] PRs** — `gh pr list --label lean-squad --state open`, rendered
  as a markdown table with issue number, title, and updated-at timestamp.
- **Open [finding] issues** — `gh issue list --label lean-squad --label finding
  --state open`, same table shape. These are the counterexamples and spec gaps
  surfaced by `lean-squad-prove`.
- **Latest report excerpt** — first ~40 lines of `formal-verification/REPORT.md` (sole
  writer: `lean-squad-report`) if it exists.
- **Next run** — a pointer at the tick coordinator schedule in
  `docs/workflows/lean-squad.md`.
- **Getting started** — the opt-in triad (`formal-verification/` dir,
  `.github/lean-squad.yml`, or `lean-squad-opt-in` label).

## Teaching notes

The dashboard is itself a teaching artefact. New contributors click it, see the table
of open PRs and findings, and learn the vocabulary (targets, findings, specs) by
reading the linked items. Keep the layout stable — outside consumers (other docs, the
`lean-squad-report` run history) link into specific sections.

## Sole-writer guarantee

This workflow is the sole writer of the dashboard issue body. Every other lean-squad
workflow is read-only with respect to it. If two `lean-squad-status` vessels somehow
run concurrently, the last `gh issue edit` wins — acceptable because both computed the
body from the same upstream sources.

## Error handling

- A missing `ISSUE: <number>` marker in the `analyze` output is a hard error (the
  phase fails so the vessel can be retried).
- Missing `TARGETS.md` / `REPORT.md` is treated as a valid, empty state and rendered
  as an encouragement for the relevant task to run.
- `gh pr list` / `gh issue list` failures fall through to empty arrays so the
  dashboard degrades gracefully instead of failing the whole run.
