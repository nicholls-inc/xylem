# `xylem doctor` — Preflight Validation CLI Command

## Summary

A deterministic CLI command that validates the entire xylem setup before running any vessels. Catches misconfigurations that would otherwise surface as expensive runtime failures (wasting Claude sessions).

## Checks

1. **Prerequisites** — `claude`, `gh`, `git` installed and authenticated; `ANTHROPIC_API_KEY` present if `--bare` mode configured
2. **Config** — `.xylem.yml` parses and validates; referenced repos accessible via `gh`; all labels exist on the repo; all referenced workflow names resolve to files in `.xylem/workflows/`
3. **Workflows** — YAML parses; all prompt files exist at referenced paths; phase names unique; gates well-formed (command gates have `run`, label gates have `wait_for`)
4. **Prompts** — Valid Go template syntax; template variables reference real phase names (e.g., `{{.PreviousOutputs.plan}}` only valid if an earlier phase is named `plan`); gate retry phases handle `{{.GateResult}}`; no prompts that ask for user input (headless execution)
5. **HARNESS.md** — File exists and is populated (not just the template)
6. **Dry run** — `xylem scan --dry-run` to preview what would be queued; explain if nothing matches

## Output

Checklist with pass/fail/warn per check. For each failure, print the exact fix (command to run, config to change, file to edit).

## Why CLI, Not Skill

All checks are deterministic — no codebase analysis or judgment needed. A skill could sit on top of this command to interpret results and fix issues, but the validation logic belongs in Go where it can be tested and run in CI.

## Notes

- Template variable validation is particularly high-value: rendering failures only surface at runtime, wasting a Claude session. Catching them upfront saves significant cost.
- Could integrate with `xylem init` as an optional `--validate` flag, or run automatically before `xylem scan`/`xylem drain`.
