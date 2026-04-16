You are running the always-on `lean-squad-report` workflow, part of the Lean Squad formal-verification system ported from https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md.

This vessel refreshes `formal-verification/REPORT.md` — the human-skimmable dashboard of progress across all Lean Squad workflows in this repo.

- Vessel ID: {{.Vessel.ID}}
- Workflow ref: {{.Vessel.Ref}}
- Schedule source: {{index .Vessel.Meta "schedule.source_name"}}
- Fired at: {{index .Vessel.Meta "schedule.fired_at"}}

## Doctrine

Lean Squad is progressive, optimistic, zero-FV-expertise. The report is the fastest way for a human with no Lean background to understand what has been attempted, what has landed, and where the squad is stuck. Keep it scannable and evidence-based — do NOT infer progress that is not reflected in committed artefacts.

## Gate: recent-update no-op

Check when `formal-verification/REPORT.md` was last modified on the current branch:

```bash
if [ -f formal-verification/REPORT.md ]; then
  LAST=$(git log -1 --format=%ct -- formal-verification/REPORT.md 2>/dev/null || echo 0)
  NOW=$(date +%s)
  AGE=$(( NOW - LAST ))
  if [ "$AGE" -lt 14400 ]; then
    echo "REPORT.md updated $AGE seconds ago — within 4h window"
  fi
fi
```

If the last modification to `formal-verification/REPORT.md` was less than 4 hours (14400 seconds) ago, emit the exact standalone line `XYLEM_NOOP` and explain that a fresh report already exists.

Also emit `XYLEM_NOOP` if `formal-verification/` does not exist (the system hasn't been bootstrapped in this repo).

## Otherwise — produce analysis

Survey the state the `implement` phase will need to read:

1. `formal-verification/TARGETS.md` — count entries and note status markers.
2. `formal-verification/repo-memory.json` — confirm it parses, note the `runs[]` length.
3. `formal-verification/lean/FVSquad/**/*.lean` — count files and note top-level directory structure for the mermaid diagram.
4. `formal-verification/specs/*_informal.md` — count informal specs present.
5. Open `[Lean Squad]` PRs and `[Lean Squad]` / `[finding]` issues via `gh pr list` and `gh issue list` (note counts only here; full body fetch is in `implement`).

Output:

SUMMARY:
- one line describing what will change in the report
INPUTS_FOUND:
- one bullet per input source with counts or "missing"
GAPS:
- one bullet per missing or malformed input (if any)
PLAN:
- one bullet per section of REPORT.md to refresh
