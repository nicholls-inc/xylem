Review the tests written for this harness spec step and fix any issues found.

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

Review all `*_test.go` files modified or created in this worktree. Check for:

1. **Tautological assertions** -- Tests that assert a value equals itself, or assertions that can never fail regardless of implementation correctness.

2. **Mock soup** -- Tests where mock setup is so complex it obscures what is actually being tested. The mock behavior effectively reimplements the production code.

3. **No meaningful assertions** -- Tests that exercise code paths but never assert anything about the output or side effects.

4. **Duplicated logic** -- Multiple tests that verify the same behavior with trivially different inputs, without adding coverage of distinct edge cases.

5. **Missing edge cases** -- Important boundary conditions, error paths, or nil/empty inputs that are not covered.

If you find any of these issues, fix them. If all tests are sound, confirm that with a brief summary of what each test validates.

Run `cd cli && go test ./...` to verify tests still pass after any changes.
