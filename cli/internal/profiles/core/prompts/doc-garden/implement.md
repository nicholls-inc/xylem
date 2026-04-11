Implement the documentation fixes identified by the `doc-garden` analysis.

## Analysis
{{.PreviousOutputs.analyze}}

Apply the smallest set of documentation edits that resolves the confirmed drift.

## Rules

1. Prefer documentation-only changes.
2. Do not change production behavior just to match stale docs.
3. Keep claims evidence-based and aligned with the checked-in repository state.
4. Preserve intentional nuance instead of flattening everything into generic wording.
5. If an analyzed item turns out not to need a fix after inspection, leave it untouched rather than making speculative edits.

When you finish, summarize the files changed and the stale claims you corrected.
