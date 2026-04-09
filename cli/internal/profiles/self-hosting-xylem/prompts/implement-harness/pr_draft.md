Your sole deliverable is a file named `pr_draft.json` at the worktree root. If this file does not exist when you finish, the workflow fails. Everything else (commit, push) is preparation for creating that file.

## Steps

1. Stage and commit all changes with the message: `feat: {{.Issue.Title}}`
   Reference the issue in the commit body: `Implements {{.Issue.URL}}`

2. Before pushing, run `git fetch origin main && git rebase origin/main`.

3. After the rebase, rerun `go vet ./... && go build ./cmd/xylem && go test ./...` from `cli/`.

4. Push the rebased branch. If the rebase rewrote commits, use `git push --force-with-lease`.

5. Write `pr_draft.json` at the worktree root with this exact structure:

```json
{
  "title": "<descriptive PR title>",
  "body": "<PR body in markdown>"
}
```

The PR body MUST include:
- Summary linking to {{.Issue.URL}}
- Smoke scenarios covered (list IDs and titles)
- Changes summary (files added/modified, key types and functions)
- Test plan

## Constraints

- You MUST create `pr_draft.json`. The next phase reads it — if missing, the entire workflow fails.
- Do NOT run `gh pr create` — a separate script handles PR creation.
- Do NOT include `Fixes #N` or `Closes #N` in the body — that is appended automatically.
- Do NOT narrate your process. Execute the steps and produce the file.

## Context

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

### Analysis
{{.PreviousOutputs.analyze}}

### Plan
{{.PreviousOutputs.plan}}
