Create a pull request for the changes.

This phase crosses the highest-blast-radius publication boundary the runner
currently classifies. By default xylem allows `git_push` and `pr_create`
so autonomous drains can finish, but repositories may add a workflow gate or
`harness.policy.rules` that requires approval. Do not work around any configured
policy boundary.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

Commit all changes with a clear commit message. Push the branch and create a PR
only when the repository policy and workflow allow `git_push` and `pr_create`. Use:
gh pr create --title "<descriptive title>" --body "<summary of changes, linking to {{.Issue.URL}}>"
