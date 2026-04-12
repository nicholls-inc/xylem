Review the current implementation draft for the feature before the workflow proceeds.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

## Evaluation Iteration
{{.Evaluation.Iteration}}

## Criteria
{{.Evaluation.Criteria}}

## Candidate Output
{{.Evaluation.Output}}

Return **JSON only** with this shape:
{"pass":true,"score":{"overall":0.0,"criteria":{"criterion_name":0.0},"issues":[{"severity":0,"description":"","location":"","suggestion":""}]},"feedback":[{"severity":0,"description":"","location":"","suggestion":""}]}

Rules:
- `severity` must be 0=low, 1=medium, 2=high, or 3=critical.
- Set `pass` to true only if the implementation meets the configured thresholds.
- When `pass` is false, include concrete issues and actionable suggestions in both `score.issues` and `feedback`.
- Do not wrap the JSON in Markdown fences or add extra prose.
