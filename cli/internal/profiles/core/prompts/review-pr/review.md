Post review comments on this pull request.

PR Number: {{.Issue.Number}}
Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Verification Findings
{{.PreviousOutputs.verify}}

## Test Critic Findings
{{.PreviousOutputs.test_critic}}

## Comment Quality Filter

**Include** comments about:
- Correctness issues (bugs, logic errors, missed edge cases)
- Missing error handling for real failure modes
- Test quality issues from the test-critic findings
- Security concerns
- Design issues with clear impact on correctness or maintainability

**Exclude** comments about:
- Naming, formatting, or style preferences (nit picks)
- Issues a linter would catch (unused imports, formatting, vet warnings)
- Minor documentation suggestions
- Opinions without a concrete "what goes wrong" scenario

Every comment must explain what breaks or degrades if the issue isn't addressed.

## Duplicate Avoidance

The analysis identified existing review state:
- Check the "Anchor comment ID" and "Existing inline comments" from the analysis
- Do NOT add an inline comment that covers the same issue as an existing unresolved comment on the same file and line range
- If an anchor comment already exists, UPDATE it — do not create a second one

## Step 1: Build the Anchor Comment

Format:

```
<!-- xylem-review -->
## Review Summary

**Verdict:** APPROVE / REQUEST_CHANGES / COMMENT

### Verification
[Key findings from verify phase, or "No issues found"]

### Test Quality
[Key findings from test-critic phase, or "Tests look solid" / "No test files changed"]

### Inline Comments
[N inline comment(s) posted on specific lines]
```

## Step 2: Post or Update the Anchor Comment

Determine the repo: `gh repo view --json nameWithOwner --jq '.nameWithOwner'`

- If an anchor comment ID exists in the analysis: update it with `gh api repos/{owner}/{repo}/issues/comments/{id} --method PATCH -f body='...'`
- If no anchor comment exists: create with `gh pr comment {{.Issue.Number}} --body '...'`

## Step 3: Post Inline Comments

Collect all findings from verify and test-critic that apply to specific lines. Remove any that duplicate existing unresolved comments.

If there are inline comments to post, submit them as a single PR review:
```
gh api repos/{owner}/{repo}/pulls/{{.Issue.Number}}/reviews \
  --method POST \
  -f event="COMMENT" \
  -f 'comments=[{"path":"file.go","line":N,"side":"RIGHT","body":"..."}]'
```

If there are no inline comments to post, skip this step.

Do not modify any code files. Only post review comments.
