🔬 *Lean Squad — `lean-squad-status` / `analyze` phase.*

This phase is `type: command`, so the logic lives inline in
`.xylem/workflows/lean-squad-status.yaml`. This file is a human-readable companion: it
documents what the shell step does and why, so a new contributor can read the scaffolded
workflow directory and understand the dashboard without tracing bash.

## Purpose

Locate (or create) the single rolling GitHub issue titled
`[Lean Squad] Formal Verification Status`. Subsequent runs of the `update` phase replace
its body with the current dashboard. This is the only source of truth for a human who
wants to click one link and see the state of formal verification in the repo.

## Behaviour

1. Search for an existing issue by exact title match, preferring the OPEN instance if
   more than one exists (sorted highest-numbered first as a tiebreak).
2. If none exists, create it with the `lean-squad` and `lean-squad-status` labels and a
   placeholder body carrying the 🔬 disclosure and a "Getting started" opt-in footer.
3. Emit a single line `ISSUE: <number>` on stdout. The `update` phase reads this from
   the captured analyze output at `.xylem/phases/<vessel-id>/analyze.output`.

## Teaching notes

New contributors: the 🔬 emoji is a consistent Lean Squad convention — every artefact
the system maintains carries it, so you can tell at a glance that a file, issue, or PR
is machine-maintained and will be overwritten on the next run. Do not edit by hand.

## Error handling

The step uses `set -euo pipefail`. `gh issue list` failures are tolerated by defaulting
to an empty JSON array — the step falls through to creation. A hard `gh issue create`
failure (e.g. missing repo permissions, network outage) propagates and fails the phase;
the vessel will retry on the next scheduled run.
