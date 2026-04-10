Convert the continuous-style inventory into the committed findings format.

Inputs to use:

1. `{{.PreviousOutputs.ingest}}`
2. the live source under `cli/cmd/xylem/`, `cli/internal/reporter/`, `cli/internal/review/`, and `cli/internal/lessons/`

Write exactly one JSON file at:

`.xylem/state/continuous-style/findings.json`

The JSON must have this shape:

```json
{
  "version": 1,
  "repo": "nicholls-inc/xylem",
  "generated_at": "2026-04-10T00:00:00Z",
  "target_surface": "cli terminal output",
  "findings": [
    {
      "id": "stable-kebab-case-id",
      "title": "Human-readable issue title",
      "category": "consistency|stderr-routing|scannability|accessibility|output-contracts",
      "summary": "What is wrong today and why it matters.",
      "recommendation": "Specific direction for the follow-up issue.",
      "priority": 8,
      "paths": ["cli/cmd/xylem/status.go"],
      "evidence": [
        {
          "path": "cli/cmd/xylem/status.go",
          "line_start": 123,
          "line_end": 140,
          "summary": "Precise evidence for the finding."
        }
      ]
    }
  ]
}
```

Requirements:

1. Keep `findings` focused and high-signal. Prefer `0-5` findings total.
2. Only include findings that are worth a dedicated GitHub issue.
3. Use stable IDs and precise evidence line numbers.
4. Favor consistency, stderr/stdout routing, scan-friendly tables/status output, and clear output contracts over purely cosmetic nitpicks.
5. Do not recommend new third-party terminal UI dependencies unless the repository already uses them on the exact surface under discussion and the recommendation is clearly justified.
6. If the current CLI output is already in good shape for this run, emit an empty `findings` array rather than inventing weak work.

After writing the file, print a short summary with the finding count and the highest-priority IDs.
