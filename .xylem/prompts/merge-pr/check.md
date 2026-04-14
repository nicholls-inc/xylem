Verify that the following pull request is safe for xylem's automatic admin-merge path.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
PR Number: {{.Issue.Number}}

{{.Issue.Body}}

## Checks

Run all of the following checks and report each result:

1. **Eligibility** -- Run `gh pr view {{.Issue.Number}} --json state,labels --jq '{state: .state, labels: [.labels[].name]}'` and confirm:
   - the PR is still `OPEN`
   - it does **not** have the `no-auto-admin-merge` label

2. **CI status** -- Run `gh pr checks {{.Issue.Number}}` and confirm every required check has passed. If any check is failing or pending, record which ones.

3. **Review state** -- Run `gh pr view {{.Issue.Number}} --json reviewDecision,reviewThreads,reviews` and confirm:
   - `reviewDecision` is **not** `CHANGES_REQUESTED`
   - every review thread is resolved (`isResolved == true`)
   - there is no outstanding review signal that should block an admin merge

4. **Mergeable state** -- Run `gh pr view {{.Issue.Number}} --json mergeable --jq '.mergeable'` and confirm the value is `MERGEABLE`.

## Decision

If ANY check fails, or the PR is already merged/closed, or the PR has the `no-auto-admin-merge` label, output the exact standalone line:

XYLEM_NOOP

Then explain which checks failed and why the PR cannot be merged yet.

If ALL checks pass, confirm the PR is ready for admin merge and summarize the passing results.
