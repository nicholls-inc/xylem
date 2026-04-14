Format the autonomy review findings into a polished markdown report for a GitHub Discussion.

## Input

Read the previous phase output:

{{.PreviousOutputs.review}}

Also read the structured findings file: `.xylem/state/autonomy-review/findings.json`

## Output

Write `.xylem/state/autonomy-review/report.md` with the following sections. Use clear markdown
formatting suitable for GitHub Discussions rendering.

### Report structure

```markdown
# Autonomy Review — {{.Date}}

## Current State Summary

- **Active workflows:** (count and list names)
- **Source types:** (github, scheduled, github-pr, github-pr-events, github-merge — list which are configured)
- **Scheduled tasks:** (list each with schedule interval)
- **Daily throughput:** N vessels/day (past 7 days)
- **Failure rate:** N% (failed + timed_out / total)

## Coverage Gap Analysis

For each gap found:

### Gap: (short title)
- **What's missing:** (description)
- **Impact:** high/medium/low
- **Suggested fix:** (concrete change with file path)

## Throughput Recommendations

| Parameter | Current | Suggested | Reason |
|-----------|---------|-----------|--------|
| ... | ... | ... | ... |

## Quality Gate Review

For each issue:
- **Workflow:** (name)
- **Issue:** (description)
- **Suggestion:** (what to add or change)
- **Trade-off:** (what could go wrong with the change)

## Prompt Quality

List specific prompt files that need changes, ordered by impact:

1. **`path/to/prompt.md`** — (issue and suggested fix)

## Quick Wins

Immediate config or workflow changes that can be applied now:

- [ ] (change with exact file path and what to modify)

## Proposed Changes

Larger changes that need design or new workflows:

### (Change title)
- **Justification:** (why)
- **Scope:** (what files/workflows are affected)
- **Risk:** (what could go wrong)
```

### Guidelines

- Be specific: reference exact file paths, config keys, and workflow names
- Be actionable: every finding should have a concrete next step
- Be honest: if throughput is good, say so; do not manufacture problems
- Keep the report under 3000 words — density over length
- Do not include raw JSON dumps; synthesize the data into readable prose and tables

After writing the file, print: `Report written: .xylem/state/autonomy-review/report.md`
