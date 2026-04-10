Cross-reference the failure patterns from the scan against existing GitHub issues.

## Context

The `scan` phase has written failure patterns to:

```
.xylem/state/audit/scan.md
```

## Your Task

For each failure pattern identified in the scan report:

1. Run `gh issue list --repo nicholls-inc/xylem --state open --search "<pattern key or symptom keywords>" --json number,title,labels` to check for existing open issues.
2. Also search closed issues: `gh issue list --repo nicholls-inc/xylem --state closed --search "<pattern key>" --json number,title,state --limit 5` to see if it was recently resolved.

Classify each pattern as one of:
- **tracked**: an open issue already covers this pattern (record the issue number)
- **resolved**: a recently closed issue addressed this (record the issue number and closure date)
- **untracked**: no existing issue found

Write your findings to `.xylem/state/audit/cross-reference.md` with:

- For each pattern: its key, severity, classification, and linked issue number (if any)
- A summary count: N tracked, N resolved, N untracked

After writing, print: `Cross-reference complete: .xylem/state/audit/cross-reference.md`.
