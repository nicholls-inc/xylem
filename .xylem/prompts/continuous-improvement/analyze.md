This is a scheduled continuous-improvement run for `{{.Repo.Slug}}`. There is no triggering GitHub issue.

## Focus brief
{{.PreviousOutputs.select_focus}}

Read the codebase and identify the best small improvement opportunity inside the selected focus area.

Requirements:

1. Read the relevant code, tests, and any directly related docs before concluding.
2. Prefer one focused, mergeable improvement over a broad refactor.
3. Weight repo-specific harness concerns higher than generic cleanup if both are similarly valuable.
4. Avoid repeating the exact same improvement area covered in the recent history unless the current focus brief explicitly chose a revisit.

If there is no worthwhile, low-risk change to ship for this focus area, output the exact standalone line `XYLEM_NOOP` and explain why.

Otherwise, provide:

- the specific problem to fix
- the exact files likely involved
- constraints, risks, and validation needs
- why this is the highest-value improvement for this scheduled run
