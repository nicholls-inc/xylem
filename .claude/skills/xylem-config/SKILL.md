---
name: xylem-config
description: Generate or revise `.xylem.yml` for this repository. Use when the user asks to set up xylem, change labels or sources, add workflows, tune daemon settings, or validate xylem configuration.
argument-hint: "[goal-or-repo-slug]"
disable-model-invocation: true
---

Treat `.xylem.yml` as the primary output surface.

1. Read the current `.xylem.yml`, `.xylem/HARNESS.md`, and the available `.xylem/workflows/*.yaml` files before proposing changes.
2. If the repo is not initialized yet, anchor the plan around `cd cli && xylem init` and then tailor `.xylem.yml` instead of inventing an unrelated layout.
3. Prefer existing source and daemon patterns already used in this repo; only introduce new sections when the user asked for new behavior.
4. After editing, validate the result with `cd cli && xylem scan --dry-run` when that is enough, or explain which follow-up command should be used if the change affects draining rather than scanning.
5. Call out any required companion edits under `.xylem/workflows/`, `.xylem/prompts/`, or `.xylem/HARNESS.md`.
