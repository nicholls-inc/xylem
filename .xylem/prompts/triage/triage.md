Triage the GitHub issue based on the analysis from the previous phase.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
Issue Number: {{.Issue.Number}}
Labels: {{.Issue.Labels}}

## Analysis
{{.PreviousOutputs.analyze}}

## Instructions

Write a JSON decision file to `.xylem/state/triage/{{.Issue.Number}}-decision.json`.

Create the state directory first:
```
mkdir -p .xylem/state/triage
```

**If the analysis says DO NOT split:**

Write this schema:
```json
{
  "action": "label",
  "type_label": "<type>"
}
```

Where `<type>` is the appropriate type label (e.g. `bug`, `enhancement`, `chore`, `documentation`).

**If the analysis says SPLIT:**

Write this schema:
```json
{
  "action": "split",
  "type_label": "<type>",
  "sub_issues": [
    {"title": "<sub-issue title>", "body": "<minimal body — refinement will flesh it out>"},
    {"title": "<sub-issue title>", "body": "<minimal body>"}
  ]
}
```

Each sub-issue body should be a single line or short paragraph. Do not use literal newlines inside the `body` values — use `\n` if you need a line break.

Do not call `gh`. Write the decision file only. If the type label is ambiguous, pick the best fit.
