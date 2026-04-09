Analyze the following GitHub issue and identify the relevant code.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

Read the codebase and identify:
1. Which files are relevant to this issue
2. The root cause (for bugs) or the requirements (for features)
3. Any dependencies or constraints

If you determine the issue is already resolved in the default branch or no code changes are needed, include the exact standalone line `XYLEM_NOOP` in your final output and explain why no further phases should run.

Write your analysis clearly and concisely.
