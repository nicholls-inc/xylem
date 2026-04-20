Your sole deliverable is a file named `pr_draft.json` at the worktree root.
The next phase is a deterministic command that reads this file to create the PR.

## Steps

1. Stage and commit all changes:
   ```
   git add -A
   git commit -m "feat: {{.Issue.Title}}" -m "Implements {{.Issue.URL}}"
   ```
   If nothing to commit, skip the commit step.

2. Rebase on origin/main:
   ```
   git fetch origin main
   git rebase origin/main
   ```

3. Rerun validation: `{{ if .Validation.Test }}{{ .Validation.Test }}{{ else }}cd cli && go test ./...{{ end }}`

4. Push the rebased branch:
   ```
   git push -u origin HEAD
   ```
   Use `--force-with-lease` if the rebase rewrote commits.

5. Write `pr_draft.json` at the worktree root with this exact structure:

```json
{
  "title": "<descriptive PR title, e.g. feat(pkg): short description>",
  "body": "<PR body in markdown>"
}
```

The PR body must include:
- One-line summary linking to {{.Issue.URL}}
- Changes summary: files added/modified, key types and functions touched
- Test plan: commands run and what they verify

## Constraints

- You MUST create `pr_draft.json`. The next phase reads it — if missing, the workflow fails.
- Do NOT run `gh pr create` — a separate command handles PR creation.
- Do NOT include `Fixes #N` or `Closes #N` in the body — appended automatically.
- Do NOT narrate your process. Execute the steps and produce the file.

## Context

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

### Analysis
{{.PreviousOutputs.analyze}}

### Plan
{{.PreviousOutputs.plan}}
