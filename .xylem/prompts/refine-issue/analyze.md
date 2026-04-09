Assess the following GitHub issue to gather context for refinement.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}
Labels: {{.Issue.Labels}}

{{.Issue.Body}}

## Your Task

Read the codebase to understand what this issue requires, then produce:

1. **Summary** — What this issue is asking for in one sentence
2. **Relevant code** — Files and packages involved, with brief notes on what each does
3. **Requirements** — What specific changes are needed
4. **Acceptance criteria** — Testable criteria that define "done"
5. **Verification steps** — For each criterion, the specific command or check to verify it (e.g., `cd cli && go test ./internal/queue -run TestDequeue`, "confirm function X exists and handles Y")
6. **Scope boundaries** — What is in scope and what is explicitly out of scope
7. **Edge cases and constraints** — Anything an implementer needs to watch out for

Do not modify any files. This is a read-only analysis phase.
