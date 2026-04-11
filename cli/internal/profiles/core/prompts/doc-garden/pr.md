Create a pull request for the documentation maintenance changes.

## Analysis
{{.PreviousOutputs.analyze}}

## Verification
{{.PreviousOutputs.verify}}

Open a concise fix-up PR that:

1. Clearly states this was produced by the scheduled `doc-garden` workflow.
2. Summarizes the stale or broken documentation claims that were corrected.
3. Notes any remaining documentation gaps that still need human follow-up.
4. Links the schedule ref `{{.Vessel.Ref}}` in the body for traceability.

Use a title in the form `[xylem] refresh repository documentation`.
