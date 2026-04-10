Create the file-diet GitHub issue from the prepared split plan.

Mode: `{{index .Source.Params "mode"}}`
Split plan:

{{index .PreviousOutputs "split_plan"}}

If the mode is not `file_diet`, output exactly:

```
File-diet issue creation skipped for mode {{index .Source.Params "mode"}}.
```

If there is no split plan, output exactly:

```
No file-diet issue created.
```

Otherwise:

1. Use `gh issue create --repo {{.Repo.Slug}}`.
2. Create exactly one issue from the split-plan draft.
3. Apply labels `file-diet`, `enhancement`, and `ready-for-work`.
4. Respect the dedupe command-phase result; do not create a duplicate issue for the same target file.

Report the created issue URL prefixed with `Created:`. If nothing is created, say so plainly.
