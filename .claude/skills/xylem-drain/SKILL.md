---
name: xylem-drain
description: Trigger a xylem drain cycle and report what moved through the queue. Use when the user asks to run pending work, kick the daemon manually, process queued vessels, or monitor a fresh drain.
disable-model-invocation: true
---

Use this for an operator-driven drain.

1. If the user wants a preview first, run `cd cli && xylem drain --dry-run`.
2. Otherwise run `cd cli && xylem drain`.
3. Follow the drain with `cd cli && xylem status` so you can report state transitions, remaining blockers, and whether failures or waiting gates need intervention.
4. Highlight completed, failed, skipped, and waiting counts, not just the final exit code.
5. If a specific vessel becomes the focus, hand off to `/xylem-debug`, `/xylem-logs`, `/xylem-retry`, or `/xylem-cancel`.
