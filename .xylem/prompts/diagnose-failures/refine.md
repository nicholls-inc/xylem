Refine the GitHub issues created in the previous phase.

## Created Issues
{{.PreviousOutputs.create_issues}}

## Instructions

If "Created Issues" shows "(none)", output a summary noting no issues were filed and stop.

For each issue number listed under "Created Issues" above, apply the full refinement process:

1. Read the issue: `gh issue view <number>`
2. Read the relevant source files in `cli/` to understand the context for that failure
3. Rewrite the issue body using this exact template:

---
## Goal
<What needs to happen, in one sentence>

## Background
<Why this matters. Current vs expected behavior>

## Acceptance Criteria
- [ ] <Specific, testable criterion>
- [ ] <Each criterion must be independently verifiable>

## Scope
- **In scope:** <files, packages, or areas to modify>
- **Out of scope:** <explicit boundaries>

## Verification
Steps the implementing agent must follow to verify each acceptance criterion:
1. <Exact command, e.g., `cd cli && go test ./internal/queue -run TestName`>
2. <Exact command>

## Technical Notes
<Edge cases, constraints, dependencies, related code>
---

4. Update the issue: `gh issue edit <number> --body "<new body>"`
5. Swap labels: `gh issue edit <number> --remove-label "needs-refinement" --add-label "ready-for-work"`

Process every issue in the "Created Issues" list. If a section cannot be filled from the
codebase, make reasonable inferences and note them in Technical Notes. Do not ask for user
input.
