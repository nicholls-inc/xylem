Implement the smoke scenario tests assigned to this harness spec step.

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

## Instructions

1. Read the smoke scenario file(s) referenced in the issue body (under `docs/smoke/`). Identify the scenarios assigned to this issue.

2. Implement each assigned scenario as a test function named `TestSmoke_S{N}_{CamelCaseTitle}`, where `{N}` is the scenario number and `{CamelCaseTitle}` is derived from the scenario title.

3. Follow each scenario's structure exactly:
   - **Preconditions** -- Set up the required state before the action.
   - **Action** -- Execute the operation described in the scenario.
   - **Expected** -- Assert the expected outcomes.
   - **Verification** -- Perform any additional verification steps specified.

4. Use existing test stubs, helpers, and interfaces from the package. Do not create real subprocesses or git operations in tests.

5. Run `cd cli && go test ./...` to verify all tests pass.
