Apply the validated `adapt-plan.json` to the worktree.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

Inputs:
- `.xylem/state/bootstrap/adapt-plan.json`
- `.xylem/state/bootstrap/repo-analysis.json`
- `.xylem/state/bootstrap/legibility-report.json`

Hard constraints:
- Only edit files under `.xylem/`, `.xylem.yml`, `AGENTS.md`, or minimal `docs/` stubs.
- Do not write anywhere else.
- Do not use `git`, Bash, network tools, or package-install commands.
- Use the `Edit` tool only.
- Preserve a reviewable PR-sized diff focused on harness changes.

If the validated plan results in no material file changes, print `XYLEM_NOOP` and explain why.
