Draft a pull request for this harness spec implementation.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

Stage and commit all changes with the message: `feat: {{.Issue.Title}}`

Reference the issue in the commit body: `Implements {{.Issue.URL}}`

Push the branch.

Then write a JSON file at the worktree root named `pr_draft.json` with this exact structure:

```json
{
  "title": "<descriptive PR title>",
  "body": "<PR body in markdown>"
}
```

The PR body must include:
- Summary linking to {{.Issue.URL}}
- Smoke scenarios covered (list IDs and titles)
- Changes summary (files added/modified, key types and functions)
- Test plan

Do NOT run `gh pr create` — a separate script handles PR creation.
Do NOT include `Fixes #N` or `Closes #N` in the body — that is appended automatically.
