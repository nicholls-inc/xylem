Fix the quality issues identified in the self-review.

PR: #{{.Issue.Number}}
URL: {{.Issue.URL}}

## Review Findings
{{.PreviousOutputs.review}}

{{if .GateResult}}
## Previous Gate Failure
The following gate check failed after the previous attempt. Fix the issues and try again:

{{.GateResult}}
{{end}}

## Instructions

For each finding in the review:

1. Read the file at the specified path and line number
2. Implement the suggested fix
3. Verify the fix doesn't break anything by running `cd cli && go test ./...` locally

After fixing all issues:

1. Run `goimports -w .` from the `cli/` directory to fix any formatting
2. Run `cd cli && go vet ./...` to check for issues
3. Run `cd cli && go test ./...` to verify nothing is broken

Do not make any changes beyond what the review findings specify. Do not add new features, refactor surrounding code, or "improve" things that weren't flagged.
