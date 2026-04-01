Push the resolved merge conflicts and post a summary.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Resolution
{{.PreviousOutputs.resolve}}

Commit and push the resolved conflicts:

```
git add -A && git commit -m "fix: resolve merge conflicts on #{{.Issue.Number}}"
git push
```

Post a comment on the PR summarizing the conflicts that were resolved and the strategy used for each file:
gh pr comment {{.Issue.Number}} --body "<summary of resolved conflicts>"
