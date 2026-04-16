Open a pull request with the refreshed `formal-verification/REPORT.md`.

## Analysis
{{.PreviousOutputs.analyze}}

## Implementation summary
{{.PreviousOutputs.implement}}

## Branch and PR

- Branch: `lean-squad-report/{{.Vessel.ID}}`
- Base: `{{.Repo.DefaultBranch}}`
- Title: `[Lean Squad] Update report`
- Labels: `lean-squad` (apply if the repo has this label)

## PR body

Include:

1. A 🔬 disclosure line noting this PR was produced by the scheduled `lean-squad-report` workflow, linking to the upstream agentics doc: https://github.com/githubnext/agentics/blob/main/docs/lean-squad.md
2. A short summary of what changed in REPORT.md (target count, findings count, run-history length, open PR/issue counts).
3. A note that REPORT.md is regenerated every tick and that manual edits will be overwritten by the next run.
4. `Workflow ref: {{.Vessel.Ref}}` for traceability.

## Only-this-file rule

This PR must change `formal-verification/REPORT.md` only. If `git status` shows any other file modified, revert those changes before pushing.

If the working tree has no changes to `formal-verification/REPORT.md` after `implement` ran (e.g. the generated content was byte-identical to the existing file), skip PR creation and explain in your final message.
