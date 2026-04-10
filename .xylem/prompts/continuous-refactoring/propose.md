Turn the semantic-refactor analysis into issue drafts.

Mode: `{{index .Source.Params "mode"}}`
Previous analysis:

{{index .PreviousOutputs "analyze"}}

If the mode is not `semantic_refactor`, output exactly:

```
Semantic refactor issue drafting skipped for mode {{index .Source.Params "mode"}}.
```

If the analysis found no strong candidate, output exactly:

```
No semantic refactor issue drafts proposed.
```

Otherwise, produce up to `{{index .Source.Params "max_issues_per_run"}}` issue drafts. Do not call `gh` in this phase.

For each draft, use this exact format:

```
Title: [refactor] <succinct refactor title>
Labels: refactor, enhancement, ready-for-work
Body:
## Summary
...

## Why now
...

## Proposed change
...

## Files
- ...

## Acceptance criteria
- ...
```
