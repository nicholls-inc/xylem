Review a pull request for quality issues before it reaches external reviewers.

PR: #{{.Issue.Number}}
URL: {{.Issue.URL}}

## Instructions

### Step 1: Fetch the PR diff and context

```
gh pr view {{.Issue.Number}} --repo {{.Repo.Slug}} --json title,body,labels,files,additions,deletions
gh pr diff {{.Issue.Number}} --repo {{.Repo.Slug}}
```

### Step 2: Review for these specific issues

For each changed file, check:

1. **Scope creep** — Changes that are unrelated to the PR title/description. Look for modifications to files not mentioned in the PR body, unrelated refactoring, or feature additions beyond the stated goal.

2. **Unnecessary formatting changes** — Whitespace-only changes, import reordering without functional purpose, comment reformatting in code the PR doesn't otherwise touch.

3. **Weak tests** — Test functions that:
   - Only assert `err == nil` without checking return values
   - Assert a mock returns what it was configured to return (tautological)
   - Have more mock setup lines than assertion lines
   - Are missing obvious edge cases (nil inputs, empty slices, zero values)

4. **Debug leftovers** — `fmt.Println`, `fmt.Printf` used for debugging (not logging), `log.Printf("DEBUG` patterns, `TODO`/`FIXME`/`HACK` comments added in this PR, commented-out code.

5. **Hardcoded values** — Magic numbers, hardcoded paths, embedded credentials or tokens, hardcoded repo names (should use template variables).

### Step 3: Output

If **no issues found**, output exactly:
```
XYLEM_NOOP: PR #{{.Issue.Number}} passes self-review — no issues found
```

If **issues found**, output a structured report:

```
## Self-Review Findings for PR #<number>

### <Category>
- **File:** `<path>` (line <N>)
  **Issue:** <description>
  **Fix:** <what to change>
```

List findings by severity (most impactful first). Be specific — include file paths and line numbers.
Do not modify any files in this phase. This is read-only.
