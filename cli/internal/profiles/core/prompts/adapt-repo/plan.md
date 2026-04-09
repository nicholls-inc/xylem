Produce the deterministic adaptation plan for this repository.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

Read the following inputs before writing anything:
- .xylem/state/bootstrap/repo-analysis.json
- .xylem/state/bootstrap/legibility-report.json
- .xylem.yml
- AGENTS.md, README.md, and nearby docs when present

Your only allowed write in this phase is `.xylem/state/bootstrap/adapt-plan.json`.
Do not edit any other file. If no harness changes are needed, print `XYLEM_NOOP` and explain why.

Write `.xylem/state/bootstrap/adapt-plan.json` with exactly this schema and no unknown fields:

```json
{
  "schema_version": 1,
  "detected": {
    "languages": ["go", "typescript"],
    "build_tools": ["go", "pnpm"],
    "test_runners": ["go test", "vitest"],
    "linters": ["goimports", "eslint"],
    "has_frontend": true,
    "has_database": false,
    "entry_points": ["cli/cmd/myapp", "web/src/main.ts"]
  },
  "planned_changes": [
    {
      "path": ".xylem.yml",
      "op": "patch",
      "rationale": "add Go + TS validation gates",
      "diff_summary": "validation.format: goimports; validation.build: go build + pnpm build"
    }
  ],
  "skipped": [
    {
      "path": ".xylem/workflows/db-migrate.yaml",
      "reason": "no database detected"
    }
  ]
}
```

Rules:
- `planned_changes[].op` must be one of `patch`, `replace`, `create`, or `delete`.
- `planned_changes[].path` must stay within `.xylem/`, `.xylem.yml`, `AGENTS.md`, or `docs/`.
- Use `skipped` for intentionally omitted changes.
- Fail closed: if you cannot produce a valid schema-conformant plan, explain the blocker instead of guessing.
