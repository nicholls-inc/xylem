Analyze the following harness spec implementation issue.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

Read and analyze:

1. Read the spec section referenced in the issue body (`docs/design/xylem-harness-impl-spec.md`). Identify the exact spec step assigned.
2. Read the smoke scenario file(s) referenced in the issue body. Identify the specific scenario IDs assigned to this issue.
3. Read every file listed in the "Key Files" section of the issue body. Understand the current state of each file.
4. Read existing tests in those packages. Note the testing patterns used: interfaces, stubs, temp directories, property-based tests.

Check dependencies: if the issue has a "Depends On" section, verify each dependency issue is closed or merged:

```
gh issue view <N> --json state
```

If any dependency is still open, output `XYLEM_NOOP` on a standalone line and explain which dependencies are not yet met.

If the issue is already resolved or no code changes are needed, include the exact standalone line `XYLEM_NOOP` in your output and explain why.

Write your analysis including:
- Spec step details and acceptance criteria
- Assigned smoke scenario IDs with their titles
- Current state of each key file
- Testing patterns found in existing tests
- Dependencies status (all met, or which are blocking)
