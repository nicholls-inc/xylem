Implement the changes according to the plan.

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

{{if .Evaluation.Feedback}}
## Evaluator Feedback
Address the following evaluator feedback before finalizing the implementation:

{{.Evaluation.Feedback}}
{{end}}

Implement the changes now. Follow the plan precisely.
