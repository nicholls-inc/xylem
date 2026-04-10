Apply the approved classification plan to the repository's currently unlabeled issues.

Repository: {{.Repo.Slug}}

## Classification plan
{{.PreviousOutputs.classify}}

For each planned issue:

1. Re-fetch the live issue before editing:

   `gh issue view <number> --repo {{.Repo.Slug}} --json number,title,url,labels`

2. If the issue already has one or more labels, skip it and record that it was already triaged elsewhere.
3. If the issue is still unlabeled, apply the planned labels with:

   `gh issue edit <number> --repo {{.Repo.Slug}} --add-label "label-a,label-b"`

4. Re-fetch the issue labels after editing and verify the requested labels are present.
5. If any `gh issue edit` or verification command fails, stop immediately and surface the failing command/output instead of pretending the run succeeded.

Write the final output as a markdown audit report with these sections:

## Auto-triage summary
- Processed: <n>
- Labeled: <n>
- Skipped: <n>

## Applied
- #<number> — `<comma-separated labels>` — <short rationale>

## Skipped
- #<number> — <reason>

## Follow-up recommendations
- Call out any issues that were labeled only with `needs-triage` and why they need human review.

The final markdown report is the persistent audit trail for this scheduled run, so keep it concrete and repo-specific.
