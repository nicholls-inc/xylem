---
name: xylem-retry
description: Requeue a failed or timed-out vessel with the existing recovery context. Use when the user asks to retry a failed vessel, rerun timed-out work, resume a broken run, or start over from scratch.
argument-hint: "[vessel-id] [--from-scratch]"
disable-model-invocation: true
---

Only retry vessels that are actually retryable.

1. Confirm the vessel is in `failed` or `timed_out` state with `cd cli && xylem status --json` before retrying.
2. Use `cd cli && xylem retry <vessel-id>` for the normal recovery path.
3. Use `cd cli && xylem retry <vessel-id> --from-scratch` only when the user explicitly wants to discard the resumed worktree path and restart all phases.
4. Explain that xylem copies forward failure context and phase artifacts, including `.xylem/phases/<vessel-id>/summary.json`, unless the retry is forced from scratch.
5. After the retry vessel is created, report the new vessel ID and suggest `/xylem-drain`, `/xylem-status`, or `/xylem-logs`.
