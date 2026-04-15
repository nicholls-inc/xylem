File a GitHub issue for the CI failure diagnosed in the previous phase.

## Diagnosis
{{.PreviousOutputs.diagnose}}

## Instructions

### Step 1: Check for existing issues

Before creating anything, check for existing open issues that already track this failure:

```
gh issue list --repo nicholls-inc/xylem --label bug --state open --json number,title --limit 50
```

Look for issues with similar titles (matching the failing step or error pattern). If a match exists,
skip creation and output: "Existing issue #N already tracks this failure — skipping."

### Step 2: Decide whether to file

- **Regression** or **Infrastructure** with high/medium confidence → file an issue
- **Flake** with high confidence → file an issue but add `flaky-test` to the labels if the label exists
- Any failure class with **low confidence** → do not file, output: "Confidence too low to file — manual review recommended"

### Step 3: Create the issue

```
gh issue create \
  --repo nicholls-inc/xylem \
  --title "CI: <failing step> — <brief description>" \
  --label "bug" \
  --label "ready-for-work" \
  --body "<body>"
```

The issue body must include:
- Link to the failing CI run
- The breaking commit SHA and message
- The failure class and confidence
- The error summary from the diagnosis
- The recommended action

Keep the body under 2000 characters. Focus on the fix, not the full diagnosis.

### Step 4: Output

If an issue was created, output: "Filed #N: <title>"
If skipped (existing or low confidence), output why.
