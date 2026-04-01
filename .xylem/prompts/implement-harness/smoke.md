Implement smoke scenario tests for this harness spec step.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

{{if .GateResult}}
## Previous Gate Failure
The following gate check failed after the previous attempt. Fix the issues and try again:

{{.GateResult}}
{{end}}

Read the smoke scenario file(s) referenced in the issue body. Identify the scenarios assigned to this issue.

For each assigned scenario, implement a Go test named `TestSmoke_S{N}_{CamelCaseTitle}` where `{N}` is the scenario ID and `{CamelCaseTitle}` summarizes the scenario title.

For each scenario, follow the specification exactly:
- **Preconditions** — Set up the required state using temp directories, mock `CommandRunner`, mock `WorktreeManager`, or other existing test stubs.
- **Action** — Execute the operation described in the scenario.
- **Expected outcome** — Assert the exact outcomes listed. Use `require` for fatal checks and `assert` for non-fatal checks.
- **Verification** — Implement any additional verification steps specified.

Place smoke tests in the same `*_test.go` file as the package's existing unit tests. Use the same test patterns (interfaces, stubs, temp dirs) already present in the package.
