Push the self-review fixes to the PR branch.

PR: #{{.Issue.Number}}
URL: {{.Issue.URL}}

## Instructions

1. Check what was changed:
   ```
   git status
   git diff --stat
   ```

2. If there are no changes (the review found no issues or apply_feedback made no changes), do nothing and output: "No changes to push."

3. If there are changes:
   a. Stage all modified files: `git add -A`
   b. Commit with message: `fix: address self-review findings for PR #{{.Issue.Number}}`
   c. Push to the PR branch: `git push`

4. Output a summary of what was pushed.

Do not create a new PR. Do not modify the PR description. Just push the fixes to the existing branch.
