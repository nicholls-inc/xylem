Resolve the merge conflicts identified in the analysis.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Merge Attempt Output
{{.PreviousOutputs.merge_main}}

{{if .GateResult}}
## Previous Gate Failure
The following gate check failed after the previous attempt. Fix the issues and try again:

{{.GateResult}}
{{end}}

Resolve each conflict using these strategies:

- **Additive**: both sides added non-overlapping code. Keep both additions.
- **Overlapping**: both sides modified the same code. Combine the intent of both changes.
- **Structural**: one side refactored while the other made localized edits. Apply the localized edits to the new structure.

The workflow has already started a merge of `origin/{{ .Repo.DefaultBranch }}` into the PR branch. Resolve the current merge state in place; do not rely on self-reporting that a merge happened.

Do not modify, stage, or delete anything under `.xylem/`. The xylem control plane is out of scope for conflict resolution.

For every conflicting file:

1. Remove all conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`)
2. Apply the appropriate resolution strategy
3. Verify the file is syntactically valid

After resolving all conflicts, stage the resolved files and complete the in-progress merge before validation. Because the workflow already ran `git merge ... --no-commit --no-ff`, finish that merge in place (for example, `git add <resolved-files> && git commit --no-edit` while `MERGE_HEAD` exists).

Once the merge commit exists, run `(cd cli && go vet ./... && go build ./cmd/xylem && go test ./...)` to confirm the build and tests pass.
