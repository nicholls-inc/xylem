Create the adaptation PR.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

Use the title `[xylem] adapt harness to this repository`.

The PR body must:
- Link the seeding issue and the bootstrap artifacts under `.xylem/state/bootstrap/`.
- Inline every `planned_changes` entry from `adapt-plan.json`.
- Inline every `skipped` entry from `adapt-plan.json`.
- Explain that the changes are restricted to harness/control-plane files and remain PR-gated.

Do not broaden scope beyond the validated adaptation plan.

Create the PR with the merge-ready label so the daemon auto-merge path can pick
up this vessel-produced PR once checks are green:

```
gh pr create --title "[xylem] adapt harness to this repository" --body "<body>" --label "ready-to-merge"
```
