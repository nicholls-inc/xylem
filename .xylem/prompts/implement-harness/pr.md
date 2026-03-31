Create a pull request for this harness spec implementation step.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

Commit all changes with a clear commit message referencing the issue. Push the branch and create a PR using:

gh pr create --title "<descriptive title>" --body "<summary of changes, linking to {{.Issue.URL}}. List the smoke scenarios implemented and their IDs.>" --label "harness-impl"
