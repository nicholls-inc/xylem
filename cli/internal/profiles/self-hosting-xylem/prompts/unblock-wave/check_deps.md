Check whether a merged PR unblocks any dependent issues.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
PR Number: {{.Issue.Number}}

{{.Issue.Body}}

## Find blocked issues

List all open issues that are currently blocked:

```
gh issue list --repo nicholls-inc/xylem --label blocked --state open --json number,title,body --limit 100
```

## Check dependencies

For each blocked issue:

1. Parse the issue body for a "Depends On" section. Dependencies are listed as issue references (e.g., `#42`, `#43`).
2. For each dependency, check whether it is closed: `gh issue view <N> --repo nicholls-inc/xylem --json state --jq '.state'`
3. If ALL dependencies are closed, the issue is ready to unblock.

## Unblock ready issues

For each issue where all dependencies are now satisfied:

```
gh issue edit <N> --repo nicholls-inc/xylem --remove-label blocked --add-label ready-for-work
```

## Report

Summarize:
- **Unblocked** -- List issues that were unblocked with their numbers and titles
- **Still blocked** -- List issues that remain blocked, noting which dependencies are still open
