---
name: xylem-workflow
description: Create or update xylem workflow YAML and prompt templates from a natural-language task description. Use when the user asks for a new workflow, phase plan, prompt set, gate sequence, or wants to adapt an existing workflow.
argument-hint: "[workflow-goal]"
disable-model-invocation: true
---

Work from the repository root and treat the workflow plus prompts as one unit.

1. Read `.xylem/HARNESS.md`, the current `.xylem/workflows/` directory, and the corresponding `.xylem/prompts/` directories before changing anything.
2. Reuse the nearest existing workflow shape instead of inventing a brand-new schema unless the task truly needs one.
3. Keep workflow YAML in `.xylem/workflows/<name>.yaml` and prompt templates in `.xylem/prompts/<name>/`.
4. Make sure every prompt or gate referenced by the workflow actually exists, and remove or rename stale prompt files when consolidating workflows.
5. Summarize the operator-facing behavior: triggers, phases, gates, and the likely follow-up command (`xylem scan`, `xylem drain`, or manual enqueue).
