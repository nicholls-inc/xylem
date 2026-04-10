Analyze open issues and produce an initiative status report.

## Data

Read all JSON files under `.xylem/state/initiative/` — each file contains an array of issues for a specific label. Also read `.xylem/state/initiative/wave-labels.txt` for the list of wave labels.

## Analysis

Group issues into initiatives by label. For each group:

1. Count total open issues
2. Identify stale issues (no update in over 14 days)
3. Note any issues that appear in multiple groups (cross-cutting work)

## Output format

Write a structured markdown report to `.xylem/state/initiative/initiative-report.md` with:

### Executive summary
- Total open issues across all labels
- Number of stale issues (>14 days)
- Most active initiative (by recent updates)
- Least active initiative (most stale)

### Initiative breakdown

For each label group, a section with:
- Open issue count
- List of issues: `#NUMBER — TITLE` (mark stale ones with `[stale]`)
- Brief assessment: active/stagnant/blocked

### Stale issues

Dedicated section listing all issues with no activity in 14+ days, sorted by staleness. Include the issue number, title, and days since last update.

### Recommended actions

2-3 concrete suggestions based on the data (e.g., close abandoned issues, re-prioritize stale work, combine overlapping initiatives).

Keep the report concise and actionable. Do NOT create or modify any GitHub issues — this is a read-only analysis.
