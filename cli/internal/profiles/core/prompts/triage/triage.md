Triage the GitHub issue based on the analysis from the previous phase.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
Issue Number: {{.Issue.Number}}
Labels: {{.Issue.Labels}}

## Analysis
{{.PreviousOutputs.analyze}}

## Instructions

**If the analysis says DO NOT split:**

1. Add the type label and "needs-refinement":
   `gh issue edit {{.Issue.Number}} --add-label "<type>,needs-refinement"`
2. Remove "needs-triage":
   `gh issue edit {{.Issue.Number}} --remove-label "needs-triage"`

**If the analysis says SPLIT:**

1. For each sub-issue identified in the analysis, create it with:
   `gh issue create --title "<title>" --label "<type>,needs-triage" --body "<relevant section of the original issue body>"`
   Keep the body minimal — the refinement workflow will flesh it out.
2. Update the original issue body to reference the created sub-issues with a "Split into:" prefix
3. Close the original:
   `gh issue close {{.Issue.Number}} --reason "not planned" --comment "Split into focused sub-issues."`

Do not ask for user input. If the type is ambiguous, pick the best fit and note the reasoning in a comment.
