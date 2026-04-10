---
name: xylem-enqueue
description: Manually enqueue ad-hoc xylem work without waiting for a GitHub issue scan. Use when the user asks to queue a one-off task, test a workflow, enqueue a prompt directly, or create a manual vessel.
argument-hint: "[workflow-or-prompt]"
disable-model-invocation: true
---

This skill performs a manual queue insertion.

1. Prefer `cd cli && xylem enqueue --workflow <workflow> --ref "<reference>"` when the task maps to an existing workflow.
2. Use `cd cli && xylem enqueue --prompt "<prompt>" --ref "<reference>"` only when the user explicitly wants a direct prompt or there is no matching workflow.
3. Mention important flags when they help: `--source`, `--id`, and `--prompt-file`.
4. After enqueueing, confirm the new vessel ID and recommend `cd cli && xylem status` or `/xylem-drain` so the user can watch it move.
5. If the workflow name is uncertain, inspect `.xylem/workflows/` first instead of guessing.
