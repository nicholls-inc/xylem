Analyze the merge conflicts on the following PR branch.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

Check out the PR branch and attempt a merge from main:

1. Run `gh pr checkout {{.Issue.Number}}`
2. Run `git fetch origin main && git merge origin/main --no-commit`

If the merge completes with no conflicts, include the exact standalone line `XYLEM_NOOP` in your final output and explain that no conflict resolution is needed.

If there are conflicts:

1. List every conflicting file
2. Read each file and locate all conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`)
3. For each conflict, describe what the two sides changed and why they conflict

Write your analysis clearly and concisely.
