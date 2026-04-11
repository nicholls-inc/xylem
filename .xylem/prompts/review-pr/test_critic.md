Analyze tests in this pull request for test theatre — tests that look productive but verify nothing meaningful.

PR Number: {{.Issue.Number}}
Issue: {{.Issue.Title}}

## Analysis
{{.PreviousOutputs.analyze}}

## Instructions

Identify all `*_test.go` files from the "Test files changed" list in the analysis. If none, output "NO_TEST_FILES" and stop.

For each test file, read it in full and analyze every `Test*` function for these anti-patterns:

### Anti-Patterns

1. **Tautological Tests** (CRITICAL) — Assertions always true regardless of implementation. Signals: comparing a value to itself, asserting a mock returns what you configured it to return, asserting a variable has the value just assigned.

2. **Mock Soup** (HIGH) — So much is stubbed that only mock wiring is tested. Signals: more stub setup than assertions, all dependencies faked AND assertions only check call args.

3. **No Meaningful Assertions** (CRITICAL) — Code executes but outcomes aren't verified. Signals: zero `assert.*`/`require.*`/`t.Error`/`t.Fatal` calls, only checking `err == nil` without checking the actual result.

4. **Overly Loose Assertions** (MEDIUM) — Assertions too broad to catch bugs. Signals: `assert.Contains` when exact match is possible, `len(result) > 0` instead of checking contents, checking one struct field when multiple matter.

5. **Testing the Framework** (MEDIUM) — Verifying Go/library behavior, not application logic. Signals: testing `json.Marshal` produces JSON, testing standard library functions.

6. **Carbon Copy Tests** (LOW) — Near-duplicate tests without different code paths. Signals: table-driven tests where every row exercises the same branch. Note: table-driven tests exercising different branches are GOOD.

7. **Assertion-Free Side Effect Tests** (HIGH) — Relying on "no panic = pass." Signals: function called with no assertions, `// should not panic` comments.

## Output

```
## Test Critic Report

### Summary
- X test file(s) analyzed
- Y finding(s) across Z test function(s)
- Severity breakdown: N critical, N high, N medium, N low

### Findings

#### [file_path:line] `TestFunctionName`
**Verdict:** [Anti-pattern] (SEVERITY)
**Problem:** [One sentence]
**Recommendation:** [Specific fix]

### Final Verdict
[Are these tests adding real value?]
```

If tests look solid, confirm and explain why. Do not modify any files.
