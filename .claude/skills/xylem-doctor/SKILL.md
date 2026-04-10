---
name: xylem-doctor
description: Run a comprehensive xylem health check across config, queue, workflows, and runtime artifacts. Use when the user asks for a doctor command, health check, sanity audit, readiness review, or to diagnose why xylem is unhealthy.
argument-hint: "[focus]"
disable-model-invocation: true
---

Use this skill to produce a structured health report, not just one command output.

1. Start with `cd cli && xylem status` to capture queue and daemon health.
2. Validate core control surfaces: `.xylem.yml`, `.xylem/HARNESS.md`, `.xylem/workflows/`, and `.xylem/prompts/`.
3. Run `cd cli && xylem visualize --format json` or another `xylem visualize` mode when you need to spot missing or orphaned workflows.
4. Inspect recent `.xylem/phases/*/summary.json` files when runtime failures or degraded health need concrete evidence.
5. Report findings in sections: configuration issues, queue/runtime issues, workflow graph issues, and recommended fixes.
6. If the repo needs deeper repository-shape auditing, you may also use `cd cli && xylem bootstrap audit-legibility --root ..` as a secondary signal, but keep the main diagnosis grounded in xylem runtime surfaces.
