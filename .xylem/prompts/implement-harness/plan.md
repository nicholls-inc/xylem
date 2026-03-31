Based on the analysis from the previous phase, create an implementation plan for this harness spec step.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Previous Analysis
{{.PreviousOutputs.analyze}}

Write a detailed plan that includes:

1. **File-by-file changes** -- For each file to be modified or created, list the specific changes with function/method signatures.

2. **Test cases** -- Map each test to its corresponding smoke scenario ID using the naming convention `TestSmoke_S{N}_{CamelCaseTitle}`. List all unit tests to add or update.

3. **Property-based tests** -- Identify opportunities for property-based tests using `pgregory.net/rapid`. These must follow the naming convention `TestProp*` and live in `*_prop_test.go` files.

4. **Spec code blocks** -- Reference the exact code blocks from the spec that define expected behavior or interfaces.

5. **Implementation order** -- Sequence the changes to minimize broken intermediate states. Production code first, then tests.

6. **Risks** -- Identify edge cases, backward compatibility concerns, or areas where the spec is ambiguous.
