Read `.xylem/state/backlog-refinement/summary.md` and tighten it for human review.

## Goal

Produce a concise final summary that emphasizes:

1. Which labels/comments were planned
2. Which issues may be ready for work
3. Which issues may be stale because a merged PR already references them
4. Which ambiguous cases were intentionally skipped

## Output

Rewrite `.xylem/state/backlog-refinement/summary.md` in place if it needs cleanup. Keep the same
section headings:

- `# Backlog Refinement Summary`
- `## Snapshot`
- `## Planned actions`
- `## Priority insights`
- `## Follow-up recommended`
- `## Skipped for caution`

Do not introduce any new GitHub mutations in this phase.
