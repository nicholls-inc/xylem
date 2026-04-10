---
name: xylem-cancel
description: Cancel a pending or running vessel cleanly through the queue state machine. Use when the user asks to stop queued work, abort a run, clear a blocked vessel, or cancel an incorrect enqueue.
argument-hint: "[vessel-id]"
disable-model-invocation: true
---

This skill performs a queue mutation.

1. Verify the target vessel with `cd cli && xylem status` or `cd cli && xylem status --json` before cancelling.
2. Run `cd cli && xylem cancel <vessel-id>`.
3. Confirm that the vessel is the intended pending or running vessel, then summarize the resulting state and any follow-up cleanup the operator may still need.
4. If the vessel is already terminal, explain that clearly instead of pretending the cancel succeeded.
