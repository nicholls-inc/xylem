---
name: xylem-status
description: Inspect xylem queue state, daemon health, and vessel summaries. Use when the user asks for xylem status, running vessels, pending work, failed jobs, waiting gates, throughput, or overall health.
argument-hint: "[state]"
disable-model-invocation: true
---

Run from the repository root.

1. Start with `cd cli && xylem status`.
2. If `$ARGUMENTS` is a vessel state such as `pending`, `running`, `failed`, `waiting`, `completed`, `cancelled`, or `timed_out`, rerun as `cd cli && xylem status --state "$ARGUMENTS"`.
3. Prefer `cd cli && xylem status --json` when you need to aggregate counts, sort vessels, or compare multiple states before answering.
4. Summarize the queue counts, daemon health block, running or waiting bottlenecks, and any anomalous failed or timed-out vessels.
5. If the output points to one problematic vessel, suggest following up with `/xylem-debug`, `/xylem-logs`, `/xylem-retry`, or `/xylem-cancel` as appropriate.
