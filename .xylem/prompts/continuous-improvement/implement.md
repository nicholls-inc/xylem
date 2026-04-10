Implement the scheduled continuous-improvement plan.

## Focus brief
{{.PreviousOutputs.select_focus}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

{{if .GateResult}}
## Previous gate failure
The previous validation gate failed. Fix the issues below and continue:

{{.GateResult}}
{{end}}

Rules:

1. Keep the change set small, focused, and easy to review.
2. Add or update tests for the chosen improvement.
3. Do not widen scope into unrelated cleanup.
4. Leave PR creation for the next phase.
5. Run any narrow, local checks you need while editing; the workflow gate will run the full configured validation commands.
