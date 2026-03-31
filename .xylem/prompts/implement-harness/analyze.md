Analyze the following GitHub issue for a harness spec implementation step.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

## Instructions

1. Read the spec section referenced in the issue body from `docs/design/xylem-harness-impl-spec.md`. Identify the exact requirements for this step.

2. Read the smoke scenario file(s) referenced in the issue body (under `docs/smoke/`). Identify which smoke scenarios are assigned to this issue.

3. Read all files listed in the "Key Files" section of the issue body. Understand the current state of the code.

4. Read existing tests in the same packages to understand test patterns, stubs, and helpers already in use.

5. **Dependency check:** If the issue body contains a "Depends On" section, verify each dependency is closed:
   - For each referenced issue number, run: `gh issue view <N> --json state --jq '.state'`
   - If any dependency is still OPEN, output `XYLEM_NOOP` and explain which dependencies are blocking.

6. If the feature described is already implemented in the current code and no changes are needed, output `XYLEM_NOOP` and explain why.

Write your analysis clearly and concisely. Include:
- The spec requirements for this step
- The assigned smoke scenario IDs and titles
- Which files need changes and why
- Any constraints or risks
