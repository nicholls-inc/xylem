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
