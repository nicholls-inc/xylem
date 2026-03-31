Implement the changes according to the plan for this harness spec step.

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

1. Follow the plan precisely. Implement production code first, then unit tests.

2. Write unit tests for all new and changed functionality. Include table-driven tests where appropriate.

3. Write property-based tests using `pgregory.net/rapid` for any functions with interesting invariants. Use the `TestProp*` naming convention in `*_prop_test.go` files.

4. Run `goimports -w .` from the `cli/` directory to fix formatting before finishing.

5. Verify your changes compile and pass: `cd cli && go vet ./... && go build ./cmd/xylem && go test ./...`
