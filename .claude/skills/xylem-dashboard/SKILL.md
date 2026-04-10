---
name: xylem-dashboard
description: Build a rich operator snapshot of queue activity and recent xylem outcomes. Use when the user asks for a dashboard, control-room view, recent completions, failure trends, throughput, or an at-a-glance queue summary.
argument-hint: "[state-or-time-window]"
disable-model-invocation: true
---

This skill synthesizes existing xylem surfaces into a dashboard-style report.

1. Start with `cd cli && xylem status --json` so you have structured queue data.
2. Inspect recent `.xylem/phases/*/summary.json` files when you need recent completion, failure, duration, or cost trends.
3. Present the result as a concise dashboard: state counts, running vessels, waiting gates, recent failures, recent completions, and any noteworthy throughput or anomaly patterns.
4. If `$ARGUMENTS` narrows the request to a state or window, apply that filter explicitly in the narrative even when the raw data comes from `xylem status --json`.
5. Close with the one or two operator actions that would most improve the dashboard state right now.
