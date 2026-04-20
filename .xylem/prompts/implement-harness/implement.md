**Your FIRST action MUST be a concrete tool call (Read, Grep, or Edit). Do not describe, narrate, or restate the plan — act immediately.**

Implement the harness spec step according to the plan.

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

Follow the plan precisely:

1. **Production code first** — Implement all type definitions, functions, and methods specified in the plan.
2. **Unit tests** — Write tests covering core functionality, error paths, and edge cases.
3. **Property-based tests** — Add property-based tests in `*_prop_test.go` files using `pgregory.net/rapid` with `TestProp*` naming.
4. **Format** — Run `goimports -w .` from the `cli/` directory to fix formatting.

Do not deviate from the plan unless a gate failure requires it.
