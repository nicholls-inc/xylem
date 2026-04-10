Diagnose failing CI checks on a pull request.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

## Step 1: Check out the PR branch

Run `gh pr checkout {{.Issue.Number}}` to switch to the PR branch.

## Step 2: Read CI check results

Run `gh pr checks {{.Issue.Number}}` to list all CI checks and their statuses.

For each failing check, read its full log output to identify the root cause. Use `gh run view <run-id> --log-failed` or download logs as needed.

## Step 3: Write diagnosis

Produce a structured diagnosis listing each failure:

- **Check name**: The name of the failing CI check
- **Root cause**: What caused the failure, with evidence from the logs
- **Planned fix**: The specific change needed to resolve it

Do not modify any files. This is a read-only diagnostic phase.
