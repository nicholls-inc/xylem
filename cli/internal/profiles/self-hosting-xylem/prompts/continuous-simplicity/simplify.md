Prepare the simplification candidates for the continuous-simplicity run.

Read:

1. `.xylem/state/continuous-simplicity/changed-files.json`
2. The recently changed source files listed there

Goal:

Identify only **behavior-preserving** simplifications in recently changed production code. Prefer:

1. extracting small helpers
2. early returns that remove nesting
3. simplifying boolean branches
4. replacing hand-rolled code with stdlib or already-present helpers
5. consolidating repetitive error wrapping / validation

Do **not** propose:

1. feature changes
2. speculative cleanups without clear payoff
3. test-only changes
4. edits under `.xylem/workflows/` or `.xylem/prompts/`

Write `.xylem/state/continuous-simplicity/simplifications.json` with this shape:

```json
{
  "version": 1,
  "generated_at": "RFC3339",
  "findings": [
    {
      "id": "stable-kebab-id",
      "kind": "simplification",
      "title": "refactor: short PR title",
      "summary": "One-paragraph explanation of the simplification",
      "paths": ["path/to/file.go"],
      "confidence": 0.93,
      "implementation": "Concrete implementation instructions for the apply phase",
      "pull_request_body": "Markdown body for the eventual PR"
    }
  ]
}
```

Use a stable kebab-case `id` for each finding, and keep every `id` unique across the entire continuous-simplicity run.

Keep the list tight. If nothing clearly worthwhile exists, still write a valid file with an empty `findings` array and print `No simplifications selected`.
