Push review response fixes and post reply comments on the pull request.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Fixes and Replies
{{.PreviousOutputs.respond}}

## Step 1: Commit and push

Stage and commit all changes:

```
git add -A && git commit -m "fix: address review feedback on #{{.Issue.Number}}"
```

Push to the remote branch:

```
git push
```

## Step 2: Post reply comments

For each review comment that was addressed (code fix or explanation), post a reply using the GitHub API:

```
gh api repos/{owner}/{repo}/pulls/{{.Issue.Number}}/comments/{comment_id}/replies -f body="<reply text>"
```

For code-fix comments, include a brief note about what was changed. For explanation comments, post the drafted reply from the respond phase.

## Step 3: Resolve completed threads

For review comments where the fix has been applied, resolve the conversation thread if the API supports it. Use the GraphQL API to resolve review threads:

```
gh api graphql -f query='mutation { resolveReviewThread(input: {threadId: "<thread_id>"}) { thread { isResolved } } }'
```

If thread resolution is not possible via API, skip this step — the reviewer can resolve manually.

## Step 4: Final check

If the branch changed underneath you, explain the conflict clearly and stop instead of forcing through an outdated push.
