Perform the final verification and ship this scheduled continuous-improvement change.

## Focus brief
{{.PreviousOutputs.select_focus}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

Review the final diff against the selected focus area and ensure the change is still small, coherent, and worth merging.

If you make additional edits in this phase, rerun the relevant validation commands before committing.

Then:

1. commit the changes with a clear message
2. push the branch
3. create a pull request

PR requirements:

- Prefix the title with `[continuous-improvement]`
- Mention the selected focus area in the body
- Note that this came from a scheduled continuous-improvement run
- Summarize the concrete improvement and the tests or validation performed

In your final output, include the PR URL and a short summary of what shipped.
