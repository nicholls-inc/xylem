Analyze the merge conflicts on the following PR branch.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

## Merge Attempt Output
{{.PreviousOutputs.merge_main}}

The workflow has already checked out the PR branch and attempted `git merge origin/{{ .Repo.DefaultBranch }} --no-commit --no-ff`.
Do not rerun the merge. Inspect the current conflicted worktree state produced by that command.

Do not modify, stage, or delete anything under `.xylem/`. The xylem control plane is out of scope for conflict resolution.

If there are conflicts:

1. List every conflicting file
2. Read each file and locate all conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`)
3. For each conflict, describe what the two sides changed and why they conflict

Write your analysis clearly and concisely.
