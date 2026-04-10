---
name: xylem-debug
description: Investigate a stuck, failed, or confusing xylem run. Use when the user asks why a vessel failed, why work is waiting, why a drain skipped work, or to debug queue, workflow, or gate behavior.
argument-hint: "[vessel-id-or-symptom]"
disable-model-invocation: true
---

Run from the repository root.

1. Begin with `cd cli && xylem status --json` to identify the target vessel, state, and any anomalies.
2. If `$ARGUMENTS` names a vessel ID, focus on that vessel. Otherwise use the status output to find the most relevant failed, waiting, or timed-out vessel and explain why you chose it.
3. Inspect `.xylem/phases/<vessel-id>` for phase outputs, especially `.xylem/phases/<vessel-id>/summary.json` and any prompt or gate artifacts referenced there.
4. Cross-check the vessel's workflow under `.xylem/workflows/` and the matching prompt files under `.xylem/prompts/` so you can separate workflow-definition problems from runtime failures.
5. Explain the failure mode concretely: state transition, gate wait, missing workflow asset, CLI error, or bad prompt/runtime output.
6. End with the narrowest next action: rerun with `/xylem-retry`, inspect a specific file with `/xylem-logs`, cancel blocked work with `/xylem-cancel`, or fix the relevant workflow/config surface.
