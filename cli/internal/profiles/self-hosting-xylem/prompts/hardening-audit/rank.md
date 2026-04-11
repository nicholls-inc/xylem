Use the deterministic hardening inventory and score report to draft up to three hardening issue proposals.

Read these files:

1. `.xylem/state/hardening-audit/inventory.json`
2. `.xylem/state/hardening-audit/scores.json`

Focus on `top_candidates` from `scores.json`. For each candidate you keep:

1. Read the current prompt file referenced by `prompt_path`.
2. Decide whether the phase should be hardened now.
3. Draft a stable workflow-qualified issue title so unrelated `analyze` or `implement` phases do not dedupe into one bucket.

Write `.xylem/state/hardening-audit/proposals.json` as a JSON array with at most 3 objects shaped like:

```json
[
  {
    "phase_id": "hardening-audit/rank",
    "workflow": "hardening-audit",
    "phase": "rank",
    "title": "[harden] hardening-audit/rank",
    "body": "## Why harden now\n...\n\n## Current prompt excerpt\n```text\n...\n```\n\n## Proposed CLI signature\n`xylem harden ...`\n\n## Suggested package location\n`cli/internal/...`\n\n## Estimated complexity\nmedium\n\n## Test cases\n1. ...\n2. ...",
    "cli_signature": "xylem harden ...",
    "package_location": "cli/internal/...",
    "estimated_complexity": "small|medium|large",
    "test_cases": ["...", "..."],
    "score": 9
  }
]
```

Requirements:

1. Keep the proposal count at 3 or fewer.
2. Every title must begin with `[harden] `.
3. Every body must include:
   - the current prompt excerpt
   - a proposed CLI signature
   - a suggested Go package location
   - an estimated complexity
   - concrete tests the deterministic CLI must pass
4. Prefer proposals whose score clearly beats the rest of the field.
5. If nothing should be filed this month, write `[]` to the proposals file instead of using `XYLEM_NOOP`.

After writing the file, print a short summary listing the chosen `phase_id` values and titles.
