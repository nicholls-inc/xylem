Create GitHub issues for the active xylem failures identified in the diagnosis.

## Diagnosis
{{.PreviousOutputs.diagnose}}

## Step 1: Check for existing issues

Before creating anything, fetch existing issues to avoid duplicates:

```
gh issue list --label bug --state open --json number,title,body
gh issue list --label bug --state closed --json number,title,body --limit 50
```

For each active failure pattern in the diagnosis, check whether an existing open or recently
closed issue already covers it. Matching criteria: same component/package AND same failure
mode. If a match exists:
- Open match → skip creation, note "#<n> already tracks this" in your output
- Closed match → skip creation, note "#<n> was previously fixed" in your output

## Step 2: Create issues

<!-- CONFIGURE: Change the number below to set the maximum number of issues filed per run -->
Create at most **5** new issues — one per distinct active failure pattern not already tracked.

Prioritize by severity: High first, then Medium, then Low. If the number of untracked patterns
exceeds the limit, file the most severe ones. Add a comment on the last created issue listing
any skipped patterns so they are not lost.

For each new issue:

```
gh issue create \
  --title "<specific, actionable title>" \
  --body "<failure pattern, affected vessel IDs, root cause, and recommended fix>" \
  --label "bug" \
  --label "needs-refinement"
```

## Step 3: Output summary

Output a summary in this exact format (the next phase depends on it):

## Created Issues
- #<number>: <title>

## Skipped (already tracked)
- #<existing-number>: <reason>

If no new issues were created and no failures were active, output:

## Created Issues
(none — no untracked active failures found)

Do not create placeholder issues. Do not file issues for patterns listed as stale in the
diagnosis.
