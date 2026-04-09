Check whether a merged PR unblocks any dependent harness issues.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
PR Number: {{.Issue.Number}}

{{.Issue.Body}}

## Guard

First, check if this PR carries the `harness-impl` label:

```
gh pr view {{.Issue.Number}} --json labels --jq '.labels[].name'
```

If `harness-impl` is NOT among the labels, this PR is not part of the harness implementation wave. Output the exact standalone line:

XYLEM_NOOP

And stop. No further work is needed.

## Find blocked issues

List all open harness issues that are currently blocked:

```
gh issue list --repo nicholls-inc/xylem --label harness-impl --label blocked --state open --json number,title,body --limit 100
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
