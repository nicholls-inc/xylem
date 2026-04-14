Analyze review comments on a pull request and plan responses.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

## Step 1: Check out the PR branch

Run `gh pr checkout {{.Issue.Number}}` to switch to the PR branch.

## Step 2: Mutex — prevent concurrent vessels

Check whether the PR already has the `pr-vessel-active` label:

```
gh pr view {{.Issue.Number}} --json labels --jq '.labels[].name' | grep -q pr-vessel-active
```

If `pr-vessel-active` is present, another vessel is already working on this PR. Output `XYLEM_NOOP` on its own line and stop.

Otherwise, apply the mutex label immediately:

```
gh pr edit {{.Issue.Number}} --add-label pr-vessel-active
```

## Step 3: Fetch all review comments

Retrieve all review comments on the PR:

```
gh api repos/{owner}/{repo}/pulls/{{.Issue.Number}}/comments
```

Also retrieve top-level PR reviews for any body text:

```
gh api repos/{owner}/{repo}/pulls/{{.Issue.Number}}/reviews
```

## Step 4: Categorize each comment

For each review comment, classify it into one of three categories:

1. **Code fix needed** — the reviewer identified a real issue that requires a code change. Note the file, line, and what needs to change.
2. **Explanation needed** — the reviewer asked a question or expressed confusion that can be resolved with a reply. Draft the reply text.
3. **Already resolved / outdated** — the comment refers to code that has already been changed or a thread that was resolved. Note why it can be skipped.

## Step 5: Read affected files

For each comment requiring a code fix, read the file at the referenced path. Understand the surrounding context so fixes are correct.

## Step 6: Produce the response plan

Output a structured plan listing each comment with:

- **Comment ID**: The API comment ID (needed for replies in the push phase)
- **Category**: code-fix / explanation / resolved
- **File and line** (if applicable)
- **Planned action**: The specific code change or reply text
