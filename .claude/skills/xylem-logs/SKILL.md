---
name: xylem-logs
description: Read xylem phase artifacts and summarize what happened in a specific vessel. Use when the user asks for logs, phase output, gate results, summaries, or the exact files under a vessel's phase directory.
argument-hint: "[vessel-id] [phase]"
disable-model-invocation: true
---

Treat the phase artifact directory as the source of truth.

1. Inspect `.xylem/phases/<vessel-id>` first and orient yourself with `.xylem/phases/<vessel-id>/summary.json`.
2. If the user also passed a phase name, narrow to the files associated with that phase before reading large outputs.
3. Summarize the important evidence: prompt input, model output, gate outcomes, timing, and the exact error text when present.
4. Never edit `.xylem/phases/<vessel-id>` artifacts; they are runtime outputs.
5. If the logs indicate an actionable next step, point to `/xylem-debug`, `/xylem-retry`, `/xylem-cancel`, or the relevant workflow/prompt file.
