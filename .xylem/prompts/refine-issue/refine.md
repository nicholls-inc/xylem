Refine the GitHub issue based on the analysis from the previous phase.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
Issue Number: {{.Issue.Number}}
Labels: {{.Issue.Labels}}

## Analysis
{{.PreviousOutputs.analyze}}

## Issue Template

Rewrite the issue description using this exact format:

## Goal
<What needs to happen, in one sentence>

## Background
<Why this matters. Current vs expected behavior for bugs, or motivation for features>

## Acceptance Criteria
- [ ] <Specific, testable criterion>
- [ ] <Each criterion must be independently verifiable>

## Scope
- **In scope:** <files, packages, or areas to modify>
- **Out of scope:** <explicit boundaries>

## Verification
Steps the implementing agent must follow to verify each acceptance criterion:
1. <Exact command or check, e.g., `cd cli && go test ./internal/queue -run TestName`>
2. <Exact command or check>

## Technical Notes
<Edge cases, constraints, dependencies, related code>

---

## Instructions

1. Rewrite the issue description using the template above, filling in all sections from the analysis.
2. Create the state directory and write the refined body to a file:
   ```
   mkdir -p .xylem/state/refine-issue
   ```
   Write the complete rewritten issue body (plain Markdown, all sections filled in) to:
   `.xylem/state/refine-issue/{{.Issue.Number}}-body.md`

Do not call `gh`. Write the body file only. If information is missing, make reasonable inferences from the codebase and note assumptions in Technical Notes.
