Resolve the merge conflicts identified in the analysis.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

{{if .GateResult}}
## Previous Gate Failure
The following gate check failed after the previous attempt. Fix the issues and try again:

{{.GateResult}}
{{end}}

Resolve each conflict using these strategies:

- **Additive**: both sides added non-overlapping code. Keep both additions.
- **Overlapping**: both sides modified the same code. Combine the intent of both changes.
- **Structural**: one side refactored while the other made localized edits. Apply the localized edits to the new structure.

Do not modify, stage, or delete anything under `.xylem/`. The xylem control plane is out of scope for conflict resolution.

For every conflicting file:

1. Remove all conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`)
2. Apply the appropriate resolution strategy
3. Verify the file is syntactically valid

After resolving all conflicts, run `cd cli && go vet ./... && go build ./cmd/xylem && go test ./...` to confirm the build and tests pass.
