Push the CI fixes and update the pull request.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Diagnosis
{{.PreviousOutputs.diagnose}}

## Fixes Applied
{{.PreviousOutputs.fix}}

## Step 1: Commit and push

Stage and commit all changes:

```
git add -A && git commit -m "fix: resolve CI failures on #{{.Issue.Number}}"
```

Push to the remote branch:

```
git push
```

## Step 2: Post a summary comment

Post a comment on the PR summarizing what was fixed. Reference the diagnosis and the specific changes made:

```
gh pr comment {{.Issue.Number}} --body "## CI Fixes Applied

<summarize the failures that were found and the fixes applied, referencing the diagnosis>

All CI checks should now pass."
```

## Step 3: Final check

If the branch changed underneath you, explain the conflict clearly and stop instead of forcing through an outdated push.
