Create an implementation plan for this harness spec step.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Previous Analysis
{{.PreviousOutputs.analyze}}

Write a step-by-step plan that includes:

1. **File-by-file changes** — For each file to create or modify, list exact function signatures and type definitions. Reference the specific code blocks from the spec.
2. **Test cases mapped to smoke scenarios** — Map each assigned smoke scenario ID to a test function name (e.g., S1 maps to `TestSmoke_S1_FullConfigLoadsWithHarnessSection`).
3. **Property-based tests** — Identify properties to test using `pgregory.net/rapid`. Follow `TestProp*` naming and place in `*_prop_test.go` files.
4. **Implementation order** — Production code first, then unit tests, then property-based tests.
5. **Risks and edge cases** — Backwards compatibility, nil handling, error paths, boundary conditions.
