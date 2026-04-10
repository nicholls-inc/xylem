Implement code fixes for the review comments identified in the analysis.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

{{if .GateResult}}
## Previous Gate Failure
The following gate check failed after the previous attempt. Fix the issues and try again:

{{.GateResult}}
{{end}}

## Fix procedure

Work through each comment categorized as "code fix needed" in the analysis.

### 1. Apply code fixes

For each code-fix comment:

1. Read the file referenced in the comment
2. Understand the reviewer's concern
3. Implement the fix, making the minimal change that addresses the feedback
4. If the fix conflicts with other review comments, note the conflict

### 2. Verify fixes compile

Run the repository's lint/build commands so the branch is ready to push.

### 3. Run tests

Run the repository's test command to confirm no checks are broken by the changes.

### 4. Prepare explanation replies

For each comment categorized as "explanation needed", draft a clear, concise reply that:

- Directly answers the reviewer's question
- References specific code or design decisions where helpful
- Is professional and constructive in tone

Include all drafted replies in your output so the push phase can post them.
