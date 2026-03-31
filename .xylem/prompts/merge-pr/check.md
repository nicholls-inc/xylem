Verify that the following pull request is ready to merge.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
PR Number: {{.Issue.Number}}

{{.Issue.Body}}

## Checks

Run all of the following checks and report each result:

1. **CI status** -- Run `gh pr checks {{.Issue.Number}}` and confirm every required check has passed. If any check is failing or pending, record which ones.

2. **Review state** -- Run `gh pr view {{.Issue.Number}} --json reviews --jq '.reviews[] | {state: .state, author: .author.login}'` and check for any `CHANGES_REQUESTED` reviews that have not been followed by an `APPROVED` review from the same author.

3. **Mergeable state** -- Run `gh pr view {{.Issue.Number}} --json mergeable --jq '.mergeable'` and confirm the value is `MERGEABLE`.

## Decision

If ANY check fails, output the exact standalone line:

XYLEM_NOOP

Then explain which checks failed and why the PR cannot be merged yet.

If ALL checks pass, confirm the PR is ready to merge and summarize the passing results.
