Push the resolved merge conflicts and post a summary.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Resolution
{{.PreviousOutputs.resolve}}

Push the already-completed merge resolution:

```
git push
```

Post a comment on the PR summarizing the conflicts that were resolved and the strategy used for each file:
gh pr comment {{.Issue.Number}} --body "<summary of resolved conflicts>"
