Review all tests created or modified for this issue and identify test theatre.

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

Identify all `*_test.go` files created or modified in this worktree. Read each file in full.

For every `Test*` function, check for these anti-patterns:

1. **Tautological assertions** — Asserting a mock returns what it was configured to return. Asserting a variable has the value just assigned. Comparing a value to itself.
2. **Mock soup** — More stub setup lines than assertion lines. All dependencies faked and assertions only check call arguments, not outcomes.
3. **No meaningful assertions** — Only checking `err == nil` without verifying the actual result. Zero `assert.*`/`require.*`/`t.Error`/`t.Fatal` calls on return values.
4. **Duplicated test logic** — Multiple tests exercising the same code path with trivially different inputs. Table-driven tests where every row hits the same branch.
5. **Missing edge cases** — Obvious boundary conditions not covered: nil inputs, empty slices, zero values, max lengths, concurrent access.

For each finding:
- Identify the test function and file
- Explain why the test is theatre
- Implement the fix

If all tests are sound, state that explicitly and move on without changes.
