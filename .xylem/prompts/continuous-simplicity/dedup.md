Prepare duplicate-code candidates for the continuous-simplicity run.

Read:

1. `.xylem/state/continuous-simplicity/changed-files.json`
2. `cli/` production code relevant to the areas touched recently
3. `.xylem/state/continuous-simplicity/simplifications.json`

Goal:

Find **semantic duplication** that is worth extracting now. Focus on production Go code. Exclude:

1. `*_test.go`
2. `.xylem/workflows/**`
3. `.xylem/prompts/**`
4. tiny or cosmetic duplication

Only keep findings that plausibly exceed the planner thresholds:

1. at least ~10 duplicated lines
2. at least 3 locations

Write `.xylem/state/continuous-simplicity/duplications.json` with this shape:

```json
{
  "version": 1,
  "generated_at": "RFC3339",
  "findings": [
    {
      "id": "stable-kebab-id",
      "kind": "duplication",
      "title": "refactor: short PR title",
      "summary": "Why this duplication is worth removing now",
      "paths": ["path/one.go", "path/two.go", "path/three.go"],
      "confidence": 0.91,
      "duplicate_lines": 18,
      "location_count": 3,
      "implementation": "Concrete implementation instructions for the apply phase",
      "pull_request_body": "Markdown body for the eventual PR"
    }
  ]
}
```

Use a stable kebab-case `id` for each finding, and ensure it does not reuse any `id` already present in `.xylem/state/continuous-simplicity/simplifications.json`.

If no worthwhile duplication exists, write a valid empty file and print `No duplication candidates selected`.
