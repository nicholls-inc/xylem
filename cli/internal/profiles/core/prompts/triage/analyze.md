Assess the following GitHub issue to determine its type and whether it needs splitting.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
Labels: {{.Issue.Labels}}

{{.Issue.Body}}

## Your Task

Read the codebase to understand the scope of this issue, then produce:

1. **Summary** — What this issue is asking for in one sentence
2. **Type** — Classify as exactly one of: `bug`, `enhancement`, or `refactor`
   - `bug` — Something is broken or behaving incorrectly
   - `enhancement` — New functionality or capability
   - `refactor` — Restructuring existing code without changing behavior
3. **Relevant code** — List the files and packages involved
4. **Scope assessment** — How many distinct concerns? How many files would change?
5. **Split decision** — Answer YES if any of:
   - More than 3 distinct, independently-deliverable concerns
   - Changes span multiple unrelated packages with no shared dependency
   - It mixes refactoring with new functionality
   - A reasonable implementation would touch more than 15 files
6. **If splitting** — List each sub-issue with a one-line title, its type, and the files/packages it covers

Do not modify any files. This is a read-only analysis phase.
