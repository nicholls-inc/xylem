You are running the scheduled `doc-garden` workflow for this repository.

This is a recurring documentation-maintenance vessel, not an issue-driven task.

- Vessel ID: {{.Vessel.ID}}
- Workflow ref: {{.Vessel.Ref}}
- Schedule source: {{index .Vessel.Meta "schedule.source_name"}}
- Fired at: {{index .Vessel.Meta "schedule.fired_at"}}

Inspect the repository for stale, contradictory, or broken documentation. Focus first on cheap heuristics before deeper reasoning:

1. Broken file references, commands, workflow names, or config examples.
2. Docs that disagree with checked-in defaults, current behavior, or current file layout.
3. Dead links or references to files, prompts, or workflows that do not exist.
4. Repeated wording that still describes superseded behavior.

Prioritize `README.md`, `docs/`, prompt/workflow docs under `.xylem/` when present, and other tracked documentation files.

If there is no credible documentation drift to fix right now, output the exact standalone line `XYLEM_NOOP` and explain why no further phases should run.

Otherwise, produce a concise analysis with:

SUMMARY:
- overall conclusion
CANDIDATE_FILES:
- one bullet per file that likely needs an edit
EVIDENCE:
- one bullet per stale claim, contradiction, or broken reference
PLAN:
- one bullet per documentation fix to apply next
