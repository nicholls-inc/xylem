---
name: xylem-init
description: Scaffold xylem into a repository and populate the harness surfaces afterward. Use when the user asks to initialize xylem, bootstrap `.xylem/`, fill in `.xylem/HARNESS.md`, or adapt the harness after `xylem init`.
disable-model-invocation: true
---

Use this when the repository needs its initial xylem scaffolding or a freshly generated harness needs real project details.

1. If `.xylem.yml` and `.xylem/` are missing, run `cd cli && xylem init` first.
2. Review the generated `.xylem/HARNESS.md`, `.xylem.yml`, `.xylem/workflows/`, and `.xylem/prompts/` so you can tell which surfaces still need project-specific edits.
3. Populate `.xylem/HARNESS.md` with concrete architecture, build, test, and operational details drawn from the repo rather than generic advice.
4. If initialization already happened, treat this as a harness-population pass instead of re-running `xylem init` unnecessarily.
5. End with the next operational command the maintainer should run, usually `cd cli && xylem scan --dry-run` or `cd cli && xylem scan && xylem drain`.
