Create a pull request for this harness spec implementation.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

Stage and commit all changes with the message: `feat: {{.Issue.Title}}`

Reference the issue in the commit body: `Implements {{.Issue.URL}}`

Push the branch and create a PR:

```
gh pr create --title "<descriptive title>" --body "<body>"
```

The PR body must include:
- Summary linking to {{.Issue.URL}}
- Smoke scenarios covered (list IDs and titles)
- Changes summary (files added/modified, key types and functions)
- Test plan

After creating the PR, add the `harness-impl` label:

```
gh pr edit --add-label harness-impl
```
