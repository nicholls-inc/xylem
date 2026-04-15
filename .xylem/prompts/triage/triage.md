Triage the GitHub issue based on the analysis from the previous phase.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
Issue Number: {{.Issue.Number}}
Labels: {{.Issue.Labels}}

## Analysis
{{.PreviousOutputs.analyze}}

## Instructions

Based on the analysis above, write a JSON actions file to `.xylem/state/triage/actions.json` describing what should be done. Do NOT run any `gh` commands — the next phase will execute them deterministically.

First, create the directory:
```
mkdir -p .xylem/state/triage
```

Then write the JSON file with this exact schema:

```json
{
  "issue_number": {{.Issue.Number}},
  "decision": "no_split",
  "add_labels": ["bug", "needs-refinement"],
  "remove_labels": ["needs-triage"],
  "close_original": false,
  "close_reason": "",
  "close_comment": "",
  "sub_issues": []
}
```

**Schema rules:**

- `issue_number` — must be `{{.Issue.Number}}` (integer)
- `decision` — either `"no_split"` or `"split"`
- `add_labels` — labels to add to the original issue (always include the type label; include `"needs-refinement"` when not splitting)
- `remove_labels` — labels to remove from the original issue (always include `"needs-triage"`)
- `close_original` — `true` only when `decision` is `"split"`
- `close_reason` — `"not planned"` when closing, otherwise `""`
- `close_comment` — brief comment when closing (e.g. `"Split into focused sub-issues."`), otherwise `""`
- `sub_issues` — array of sub-issues to create; empty when `decision` is `"no_split"`

**Sub-issue object schema:**
```json
{
  "title": "Short descriptive title",
  "labels": ["bug", "needs-triage"],
  "body": "Relevant section of the original issue body. Keep it minimal — the refinement workflow will flesh it out."
}
```

**Decision guidance:**

- **DO NOT split** — Add the type label and `needs-refinement` to the original issue. Remove `needs-triage`. Leave `close_original` false and `sub_issues` empty.
- **SPLIT** — Create sub-issues for each focused piece identified in the analysis. Set `close_original` to `true`. Add the type label and remove `needs-triage` from the original. Each sub-issue gets `needs-triage` so it enters the triage queue.

If the type is ambiguous, pick the best fit. Do not ask for user input.

Write only the JSON file. Do not execute any GitHub CLI commands.
