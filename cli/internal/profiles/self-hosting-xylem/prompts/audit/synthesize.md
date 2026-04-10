Synthesize untracked failure patterns into GitHub issues ready for filing.

## Context

The `cross_reference` phase has written its findings to:

```
.xylem/state/audit/cross-reference.md
```

Also available:
- `.xylem/state/audit/scan.md` — full pattern details from the scan phase

## Your Task

1. Read both files.
2. Identify all patterns classified as **untracked** (no existing open GitHub issue).
3. For each untracked pattern with severity **medium** or **high**:
   - Draft a GitHub issue title: concise, descriptive, prefixed with `[audit]`
   - Draft a GitHub issue body: root cause summary, affected vessels, recommended fix, severity

If there are no untracked patterns of medium or higher severity, output the exact standalone line:

```
XYLEM_NOOP
```

and stop — do not write the issues file.

Otherwise, write a JSON file to `.xylem/state/audit/issues-to-file.json` with this structure:

```json
[
  {
    "title": "[audit] <concise description>",
    "body": "## Root Cause\n...\n\n## Affected Vessels\n...\n\n## Recommended Fix\n...\n\n## Severity\n<high|medium>\n\n_Filed by xylem audit workflow_"
  }
]
```

Include one object per untracked medium/high-severity pattern.

After writing, print: `Synthesized N issues to .xylem/state/audit/issues-to-file.json`.
