---
name: xylem-prompts
description: Improve existing xylem prompt templates for clarity, coverage, and better outcomes. Use when the user asks to refine prompt wording, reduce failures, tighten guardrails, or tune prompts for a workflow.
argument-hint: "[workflow-or-prompt]"
disable-model-invocation: true
---

Focus on prompt assets, not unrelated workflow plumbing.

1. Read the target prompt files under `.xylem/prompts/` and the parent workflow YAML so the prompt edits stay consistent with the actual phase contract.
2. If the user mentions failures or regressions, inspect `.xylem/phases/<vessel-id>/summary.json` or other `.xylem/phases/<vessel-id>` artifacts first so the edits address a real failure mode.
3. Prefer tightening instructions, inputs, success criteria, and output contracts over adding generic verbosity.
4. Keep naming and file layout aligned with the corresponding workflow YAML.
5. In your summary, mention which prompt files changed and what execution behavior those edits are meant to improve.
