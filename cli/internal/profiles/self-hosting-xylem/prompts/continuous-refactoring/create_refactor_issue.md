Create semantic-refactor GitHub issues from the prepared drafts.

Mode: `{{index .Source.Params "mode"}}`
Drafts:

{{index .PreviousOutputs "propose"}}

If the mode is not `semantic_refactor`, output exactly:

```
Semantic refactor issue creation skipped for mode {{index .Source.Params "mode"}}.
```

If there are no drafts, output exactly:

```
No semantic refactor issues created.
```

Otherwise:

1. Re-read the drafts from the previous phase.
2. Use `gh issue create --repo {{.Repo.Slug}}` to file each draft.
3. Apply labels `refactor`, `enhancement`, and `ready-for-work`.
4. Create at most `{{index .Source.Params "max_issues_per_run"}}` issues.
5. Do not create duplicates; if the command phase already reported an open issue, stop without filing anything.

Report the created issue URLs, one per line, prefixed with `Created:`. If nothing is created, say so plainly.
