Analyze a pull request for review.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
PR Number: {{.Issue.Number}}

{{.Issue.Body}}

## Instructions

1. Verify this is a pull request: `gh pr view {{.Issue.Number}} --json number`. If it fails, output "NOT_A_PR" and stop.

2. Checkout the PR branch: `gh pr checkout {{.Issue.Number}}`

3. Read the full diff: `gh pr diff {{.Issue.Number}}`

4. Read every changed file in full to understand the context around the changes.

5. Check for an existing xylem review anchor comment. Search all PR comments for one containing the HTML marker `<!-- xylem-review -->`. Record its comment ID if found.

6. Fetch all existing unresolved inline review comments on the PR. For each, record the path, line, and body.

## Output

- **Summary**: What this PR does (1-2 sentences)
- **Changed files**: List with modification type (added/modified/deleted)
- **Test files changed**: List of *_test.go files in the diff
- **Intent**: What problem/feature this addresses
- **Scope assessment**: Is the change well-scoped?
- **Anchor comment ID**: Existing anchor comment ID, or "none"
- **Existing inline comments**: List of (path, line, body) for unresolved comments

Do not modify any files. Read-only phase.
