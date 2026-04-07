# Workflows Guide

Workflows are the execution plane of xylem. They define what happens after a vessel is dequeued -- which phases run, whether a phase invokes the configured LLM provider or a shell command, and what quality checks must pass between them.

This guide covers the workflow YAML format, phase execution, quality gates, prompt templates, the built-in workflows, and how to create your own.

## What is a workflow?

A workflow is a multi-phase execution plan stored as a YAML file. When xylem drains a vessel, it loads the workflow assigned to that vessel and runs its phases sequentially. Prompt phases run in headless sessions using the resolved provider (`claude` or `copilot`). Command phases render and execute a shell command in the worktree. Between phases, optional quality gates verify that the work meets a standard before proceeding.

The relationship between the moving parts:

```
Vessel (queued work item)
  |
  v
Workflow (YAML definition)
  |
  +-- Phase 1: analyze   -->  prompt template  -->  LLM session
  +-- Phase 2: plan       -->  prompt template  -->  LLM session
  +-- Phase 3: implement  -->  prompt template  -->  LLM session
  |       |
  |       +-- Gate: run tests (retry up to N times on failure)
  |
  +-- Phase 4: pr         -->  prompt template  -->  LLM session
```

Each phase produces output that subsequent phases can reference. Gates act as checkpoints -- if a gate fails and retries are exhausted, the vessel is marked as failed. If a gate is a label gate, the vessel enters a `waiting` state until a human applies the required label on GitHub.

The built-in workflows scaffolded by `xylem init` use prompt phases, but the workflow format also supports `type: command` phases for deterministic shell steps inside the same execution pipeline.

## Workflow YAML format

Workflow files live in `.xylem/workflows/` and are named after the workflow. The filename (minus extension) must match the `name` field inside the file. For example, `.xylem/workflows/fix-bug.yaml` must have `name: fix-bug`.

Here is an annotated example:

```yaml
# .xylem/workflows/fix-bug.yaml

# Required. Must match the filename (without .yaml extension).
name: fix-bug

# Optional. Human-readable description of what this workflow does.
description: "Diagnose and fix a bug from a GitHub issue"

# Optional. Default provider and model for prompt phases in this workflow.
llm: claude
# model: claude-sonnet-4.5

# Required. At least one phase. Phases execute in the order listed.
phases:
  - name: analyze                                  # Unique name within this workflow
    # type defaults to "prompt"
    prompt_file: .xylem/prompts/fix-bug/analyze.md # Path to the Go template file
    max_turns: 5                                   # Max turns for this prompt phase
    noop:                                          # Optional early-success completion rule
      match: XYLEM_NOOP                            # Complete the workflow if phase output contains this marker

  - name: plan
    prompt_file: .xylem/prompts/fix-bug/plan.md
    max_turns: 3

  - name: implement
    prompt_file: .xylem/prompts/fix-bug/implement.md
    max_turns: 15
    allowed_tools: "Bash, Read, Edit, Write, Grep, Glob"  # Optional tool restriction
    gate:                                          # Optional quality gate
      type: command                                # "command" or "label"
      run: "make test"                             # Shell command to execute
      retries: 2                                   # Retry count on failure (default: 0)
      retry_delay: "10s"                           # Delay between retries (default: "10s")

  - name: pr
    prompt_file: .xylem/prompts/fix-bug/pr.md
    max_turns: 3

  - name: smoke_test
    type: command                                  # Optional shell-command phase
    run: "make smoke-test"
```

### Field reference

**Top-level fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Workflow identifier. Must match the YAML filename. |
| `description` | No | Human-readable description of the workflow's purpose. |
| `llm` | No | Default provider for prompt phases in this workflow. Valid values: `claude`, `copilot`. |
| `model` | No | Default model for prompt phases in this workflow. Provider-specific string. |
| `phases` | Yes | Ordered list of phases. At least one is required. |

**Phase fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Unique name within the workflow. Used to key previous outputs in templates. |
| `type` | No | Phase type. Defaults to `prompt`. Supported values: `prompt`, `command`. |
| `prompt_file` | Yes, for `prompt` phases | Path to the prompt template file, relative to the repo root. |
| `run` | Yes, for `command` phases | Shell command to execute. Rendered as a Go template before execution. |
| `max_turns` | Yes, for `prompt` phases | Maximum number of turns for this phase. Must be greater than 0. |
| `llm` | No | Provider override for this prompt phase. Valid values: `claude`, `copilot`. |
| `model` | No | Model override for this prompt phase. Provider-specific string. |
| `noop` | No | Early-success completion rule checked against the phase output before any gate runs. |
| `allowed_tools` | No | Tool restriction string for prompt phases. Passed through to the provider CLI. Use this instead of top-level `claude.allowed_tools`, which is rejected by config validation. |
| `gate` | No | Quality gate that must pass after this phase completes. |
| `depends_on` | No | List of phase names this phase depends on. Enables parallel execution -- phases without dependency relationships can execute concurrently. Validated for duplicate entries, self-references, references to unknown phase names, and dependency cycles. |

**No-op fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `match` | Yes | Substring marker that, when present in successful phase output, completes the workflow early. |

**Gate fields (when `type: command`):**

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `type` | Yes | -- | Must be `"command"`. |
| `run` | Yes | -- | Shell command to execute. Runs in the worktree directory. |
| `retries` | No | `0` | Number of times to retry the phase if the gate fails. |
| `retry_delay` | No | `"10s"` | Go duration string for delay between retries. |

**Gate fields (when `type: label`):**

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `type` | Yes | -- | Must be `"label"`. |
| `wait_for` | Yes | -- | GitHub label name to poll for. |
| `timeout` | No | `"24h"` | Maximum time to wait for the label. |
| `poll_interval` | No | `"60s"` | How often to check for the label. |

**Gate evidence fields (optional metadata):**

Gates can optionally carry verification evidence metadata describing the assurance level of the gate check.

| Field | Required | Description |
|-------|----------|-------------|
| `evidence.claim` | No | Description of what the gate verifies. |
| `evidence.level` | No | Assurance level. Valid values: `proved`, `mechanically_checked`, `behaviorally_checked`, `observed_in_situ`. |
| `evidence.checker` | No | Tool or person that performed the verification. |
| `evidence.trust_boundary` | No | Description of the trust boundary this gate enforces. |

```yaml
gate:
  type: command
  run: "make test"
  retries: 2
  evidence:
    claim: "All unit tests pass"
    level: mechanically_checked
    checker: "go test"
    trust_boundary: "code correctness"
```

## Phases

A phase is a single step within a workflow. Phases run sequentially -- phase 2 does not start until phase 1 finishes (and its gate passes, if one exists).

There are two phase types:

- **`prompt`** (default) -- Reads `prompt_file`, renders it with template data, then invokes the resolved provider (`claude` or `copilot`) in the worktree. `max_turns` applies here.
- **`command`** -- Renders `run` with the same template data, then executes the resulting shell command in the worktree. Command phases do not use `prompt_file`, `max_turns`, `llm`, `model`, or `allowed_tools`.

Provider resolution for prompt phases is: `phase.llm` -> `workflow.llm` -> `.xylem.yml llm` -> default `claude`. Model resolution follows the same override pattern: phase, then workflow, then provider config in `.xylem.yml`.

### HARNESS.md

`xylem init` scaffolds `.xylem/HARNESS.md`. Before each prompt phase, the runner reads that file and passes it to the selected provider as the system prompt. Use it for repo-specific architecture notes, exact build/test commands, and non-negotiable rules the agent should follow. Command phases do not read `HARNESS.md` directly.

### How a phase executes

1. The runner builds `TemplateData` for the current phase (issue details, previous phase outputs, gate results, vessel metadata).
2. If the phase is `prompt`, the runner reads `prompt_file`, renders it, writes `.xylem/phases/<vessel-id>/<phase>.prompt`, then invokes the resolved provider CLI in the worktree.
3. If the phase is `command`, the runner renders `run`, writes `.xylem/phases/<vessel-id>/<phase>.command`, then executes the rendered shell command in the worktree.
4. The phase output is captured and persisted to `.xylem/phases/<vessel-id>/<phase>.output`.
5. If the phase has a `noop` rule and the successful phase output contains `noop.match`, the vessel is marked `completed`, remaining phases are skipped, and no gate is evaluated for that phase.
6. Otherwise, if the phase has a gate, the gate is evaluated. If it fails and retries remain, the phase re-executes with the gate failure output injected into the template via `{{.GateResult}}`.

### Output persistence

Each phase's output is saved to disk under `.xylem/phases/<vessel-id>/`. These outputs are then available to subsequent phases via `{{.PreviousOutputs.<phase-name>}}` in their prompt templates.

No-op-triggering outputs are persisted the same way, so you can inspect exactly why the workflow stopped early.

### Turn limits

For prompt phases, the `max_turns` field controls how many turns the selected provider gets within a single phase. Set this based on the complexity of the task:

- **Analysis phases** (reading code, identifying issues): 5-20 turns
- **Planning phases** (writing plans, no code changes): 3-20 turns
- **Implementation phases** (writing and editing code): 15-60 turns
- **PR phases** (committing and pushing): 3-10 turns

If the provider exhausts its turn limit, the phase ends with whatever output was produced. The workflow continues to the next phase (or gate) regardless.

### Phase dependencies and parallel execution

By default, phases execute sequentially in the order they are listed. The `depends_on` field enables parallel execution by declaring explicit dependency relationships between phases.

When any phase in a workflow declares `depends_on`, the runner uses the dependency graph to schedule phases. Phases whose dependencies have all completed can execute concurrently, up to the configured concurrency limit.

```yaml
name: parallel-workflow
description: "Workflow with parallel phases"
phases:
  - name: analyze
    prompt_file: .xylem/prompts/my-workflow/analyze.md
    max_turns: 5

  - name: implement_api
    prompt_file: .xylem/prompts/my-workflow/implement-api.md
    max_turns: 15
    depends_on: [analyze]

  - name: implement_ui
    prompt_file: .xylem/prompts/my-workflow/implement-ui.md
    max_turns: 15
    depends_on: [analyze]

  - name: integrate
    prompt_file: .xylem/prompts/my-workflow/integrate.md
    max_turns: 10
    depends_on: [implement_api, implement_ui]
```

In this example, `analyze` runs first. Once it completes, `implement_api` and `implement_ui` run in parallel. After both finish, `integrate` runs.

**Context firewall:** When `depends_on` is used, each phase's `{{.PreviousOutputs}}` template variable is restricted to outputs from its declared dependencies only. A phase that depends on `[analyze]` will only see `{{.PreviousOutputs.analyze}}` -- not outputs from sibling phases in the same wave. In sequential mode (no `depends_on`), all previous phase outputs remain visible for backward compatibility.

**Validation rules for `depends_on`:**

- Phase names in `depends_on` must reference phases defined in the same workflow.
- Self-references are rejected (a phase cannot depend on itself).
- Duplicate entries within a single `depends_on` list are rejected.
- Circular dependencies are detected and rejected (e.g., A depends on B, B depends on A).

If no phase in a workflow uses `depends_on`, phases execute sequentially as before.

## Gates

Gates are quality checkpoints between phases. They answer the question: "Did this phase produce acceptable work?" A gate is evaluated after its phase completes. If the gate fails, the phase is retried (up to `retries` times) with the failure output fed back into template context via `{{.GateResult}}`.

### Command gates

A command gate runs a shell command in the worktree directory. If the command exits with code 0, the gate passes. Any non-zero exit means the gate failed.

```yaml
gate:
  type: command
  run: "make test"
  retries: 2
  retry_delay: "10s"
```

The command runs via `sh -c` in the worktree directory. You can use any shell command -- test suites, linters, type checkers, custom validation scripts.

When a command gate fails:

1. The command's stdout/stderr output is captured.
2. If retries remain, the phase re-executes. The gate's output is available in the prompt template as `{{.GateResult}}`, so the agent can see what failed and attempt a fix.
3. If no retries remain, the vessel is marked as `failed`.

Common command gate examples:

```yaml
# Run Go tests
gate:
  type: command
  run: "cd cli && go test ./..."
  retries: 2

# Run npm test suite
gate:
  type: command
  run: "npm test"
  retries: 1

# Run a linter
gate:
  type: command
  run: "eslint src/ --max-warnings 0"
  retries: 1

# Run multiple checks
gate:
  type: command
  run: "make lint && make test"
  retries: 2
  retry_delay: "5s"
```

### Label gates

A label gate polls a GitHub issue for a specific label. This enables human-in-the-loop approval workflows. When a label gate is encountered, the vessel enters the `waiting` state and xylem periodically checks whether the label has been applied.

```yaml
gate:
  type: label
  wait_for: "plan-approved"
  timeout: "24h"
  poll_interval: "60s"
```

When a label gate is evaluated:

1. Xylem queries GitHub using `gh issue view` to check if the label exists on the issue.
2. If the label is present, the gate passes and the workflow continues.
3. If the label is not present, the vessel enters `waiting` state.
4. Xylem polls at `poll_interval` intervals until the label appears or `timeout` is reached.
5. If the timeout expires without the label, the vessel is marked as `timed_out`.

Label gates are useful when you want a human to review an intermediate artifact (like an implementation plan) before the agent proceeds with execution.

## Prompt templates

Prompt files are Go templates used by `prompt` phases. They live in `.xylem/prompts/<workflow-name>/` and are referenced by `prompt_file` in the workflow YAML. Command phases do not use `prompt_file`; instead, their `run` field is rendered as a Go template with the same template data.

### Template syntax

Prompt templates use Go's `text/template` syntax. The most common operations are variable interpolation and conditionals.

**Variable interpolation:**

```
{{.Issue.Title}}
```

**Conditionals:**

```
{{if .GateResult}}
## Previous Gate Failure
{{.GateResult}}
{{end}}
```

**Accessing map values:**

```
{{.PreviousOutputs.analyze}}
```

Missing keys produce an empty string rather than an error (the templates use the `missingkey=zero` option).

### Available variables

All prompt templates receive a `TemplateData` struct with these fields:

| Variable | Type | Description |
|----------|------|-------------|
| `{{.Issue.Title}}` | string | The issue title. |
| `{{.Issue.URL}}` | string | The issue URL. |
| `{{.Issue.Body}}` | string | The issue body text. Truncated to 32,000 characters. |
| `{{.Issue.Labels}}` | []string | Labels on the issue. |
| `{{.Issue.Number}}` | int | The issue number. |
| `{{.Phase.Name}}` | string | Name of the current phase. |
| `{{.Phase.Index}}` | int | Zero-based index of the current phase. |
| `{{.PreviousOutputs.<name>}}` | string | Output from a previous phase, keyed by phase name. Truncated to 16,000 characters per phase. |
| `{{.GateResult}}` | string | Output from the most recent gate failure (for retries). Truncated to 8,000 characters. Empty on first execution. |
| `{{.Vessel.ID}}` | string | The vessel identifier. |
| `{{.Vessel.Source}}` | string | The source that created this vessel. |

### Truncation limits

Large outputs are automatically truncated to prevent prompt templates from exceeding the provider's context window:

| Field | Max characters |
|-------|---------------|
| `Issue.Body` | 32,000 |
| Each `PreviousOutputs` entry | 16,000 |
| `GateResult` | 8,000 |

When truncation occurs, a suffix is appended: `[... output truncated at N characters]`.

### Example: analysis prompt

This template is the first phase in both built-in workflows. It receives the issue data and asks the agent to analyze the codebase:

```
Analyze the following GitHub issue and identify the relevant code.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

Read the codebase and identify:
1. Which files are relevant to this issue
2. The root cause (for bugs) or the requirements (for features)
3. Any dependencies or constraints

Write your analysis clearly and concisely.
```

### Example: implementation prompt with gate retry

This template demonstrates how to use `PreviousOutputs` and `GateResult` together. On the first execution, the `GateResult` block is skipped. On retries after a gate failure, the agent sees what went wrong:

```
Implement the changes according to the plan.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

{{if .GateResult}}
## Previous Gate Failure
The following gate check failed after the previous attempt. Fix the issues and try again:

{{.GateResult}}
{{end}}

Implement the changes now. Follow the plan precisely.
```

### Example: PR creation prompt

This template uses outputs from multiple previous phases to provide full context when creating the pull request:

```
Create a pull request for the changes.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Analysis
{{.PreviousOutputs.analyze}}

## Plan
{{.PreviousOutputs.plan}}

Commit all changes with a clear commit message, push the branch, and create a PR using:
gh pr create --title "<descriptive title>" --body "<summary of changes, linking to {{.Issue.URL}}>"
```

## Built-in workflows

xylem ships with two workflows, scaffolded into your repo by `xylem init` alongside `.xylem/HARNESS.md` and matching prompt templates.

### fix-bug

Diagnoses and fixes a bug from a GitHub issue in 4 phases.

```yaml
name: fix-bug
description: "Diagnose and fix a bug from a GitHub issue"
phases:
  - name: analyze
    prompt_file: .xylem/prompts/fix-bug/analyze.md
    max_turns: 5
    noop:
      match: XYLEM_NOOP
  - name: plan
    prompt_file: .xylem/prompts/fix-bug/plan.md
    max_turns: 3
  - name: implement
    prompt_file: .xylem/prompts/fix-bug/implement.md
    max_turns: 15
    gate:
      type: command
      run: "make test"
      retries: 2
  - name: pr
    prompt_file: .xylem/prompts/fix-bug/pr.md
    max_turns: 3
```

**Phase flow:**

1. **analyze** -- Reads the issue and the codebase to identify relevant files, the root cause, and constraints. If the output contains `XYLEM_NOOP`, the workflow completes early.
2. **plan** -- Takes the analysis output and produces a step-by-step implementation plan: which files to change, in what order, what tests to update, and what risks exist.
3. **implement** -- Executes the plan. After implementation, a command gate runs `make test`. If tests fail, the phase retries up to 2 times with the test output fed back via `{{.GateResult}}`.
4. **pr** -- Commits changes, pushes the branch, and creates a pull request linking to the issue.

**When to use:** Assign this workflow to tasks triggered by `bug`-labeled GitHub issues. It works best for well-described bugs with clear reproduction steps.

**Customization:** After running `xylem init`, edit the `run` field in the implement phase's gate to match your project's test command. The scaffolded default is `make test`, but you might need `go test ./...`, `npm test`, `pytest`, or something else.

### implement-feature

Implements a feature from a GitHub issue in 4 phases.

```yaml
name: implement-feature
description: "Implement a feature from a GitHub issue"
phases:
  - name: analyze
    prompt_file: .xylem/prompts/implement-feature/analyze.md
    max_turns: 5
    noop:
      match: XYLEM_NOOP
  - name: plan
    prompt_file: .xylem/prompts/implement-feature/plan.md
    max_turns: 3
    gate:
      type: label
      wait_for: "plan-approved"
      timeout: "24h"
  - name: implement
    prompt_file: .xylem/prompts/implement-feature/implement.md
    max_turns: 15
    gate:
      type: command
      run: "make test"
      retries: 2
  - name: pr
    prompt_file: .xylem/prompts/implement-feature/pr.md
    max_turns: 3
```

**Phase flow:**

1. **analyze** -- Reads the issue and the codebase to identify requirements, affected modules, and existing patterns to follow.
2. **plan** -- Produces an implementation plan with file changes, ordering, test strategy, and risk assessment. A label gate then waits for `plan-approved` before implementation continues.
3. **implement** -- Executes the approved plan. Gated on `make test` with 2 retries in the scaffolded workflow.
4. **pr** -- Commits, pushes, and creates a pull request.

**When to use:** Assign this workflow to tasks triggered by `enhancement`-labeled issues that have been refined and marked as ready for autonomous implementation.

**Customization:** After running `xylem init`, update the label gate and test command to match your process. For example, you might use a different approval label than `plan-approved`, or replace `make test` with `go test ./...`, `npm test`, or `pytest`.

## Prompt file organization

Prompt files are organized in `.xylem/prompts/` under a subdirectory named after the workflow:

```
.xylem/
  HARNESS.md
  workflows/
    fix-bug.yaml
    implement-feature.yaml
  prompts/
    fix-bug/
      analyze.md
      plan.md
      implement.md
      pr.md
    implement-feature/
      analyze.md
      plan.md
      implement.md
      pr.md
```

This convention is not enforced -- `prompt_file` can point anywhere relative to the repo root. But grouping prompts by workflow keeps things navigable as you add more workflows.

`xylem init` scaffolds this structure with working defaults for both built-in workflows and a starter `HARNESS.md`.

## Creating a custom workflow

Follow these steps to create a workflow from scratch.

### Step 1: Create the workflow YAML

Create a new file in `.xylem/workflows/`. The filename determines the workflow name.

```yaml
# .xylem/workflows/review-code.yaml
name: review-code
description: "Review a PR for code quality and correctness"
phases:
  - name: read
    prompt_file: .xylem/prompts/review-code/read.md
    max_turns: 10
  - name: review
    prompt_file: .xylem/prompts/review-code/review.md
    max_turns: 15
  - name: comment
    prompt_file: .xylem/prompts/review-code/comment.md
    max_turns: 5
```

### Step 2: Create the prompt directory

```bash
mkdir -p .xylem/prompts/review-code
```

### Step 3: Write the prompt templates

Create a prompt file for each phase. Start each prompt with clear context about the task, then provide the issue data, then give specific instructions.

`.xylem/prompts/review-code/read.md`:

```
Read the codebase relevant to this issue and understand the existing patterns.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

{{.Issue.Body}}

Identify:
1. The files and modules that this change will touch
2. Existing code patterns and conventions in those areas
3. The test coverage for affected code

Summarize your findings.
```

`.xylem/prompts/review-code/review.md`:

```
Review the changes for correctness, readability, and adherence to project conventions.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Codebase Context
{{.PreviousOutputs.read}}

Check for:
1. Logic errors or edge cases
2. Deviation from existing patterns found in the read phase
3. Missing tests for new or changed behavior
4. Security concerns (input validation, injection, auth)

Produce a structured review with findings categorized by severity.
```

`.xylem/prompts/review-code/comment.md`:

```
Post a review comment on the pull request.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Review Findings
{{.PreviousOutputs.review}}

Post a constructive review comment on the PR using the gh CLI.
Focus on the most important findings. Be specific about what to change and why.
```

### Step 4: Wire the workflow into your config

In `.xylem.yml`, add a task that uses your new workflow:

```yaml
sources:
  reviews:
    type: github
    repo: owner/name
    tasks:
      code-reviews:
        labels: [needs-review, ready]
        workflow: review-code
```

### Step 5: Validate

Run `xylem scan --dry-run` to verify that xylem can load and validate your workflow. The validator checks that:

- The `name` field matches the filename.
- At least one phase is defined.
- Every phase has a non-empty `name`.
- Prompt phases have a `prompt_file` that exists on disk and a `max_turns` greater than 0.
- Command phases have a non-empty `run` command.
- Phase names are unique within the workflow.
- Phase `llm` values, if set, are valid.
- `allowed_tools`, if set, is not empty.
- Gate fields are valid for their type.
- Duration strings (`retry_delay`, `timeout`, `poll_interval`) parse correctly.
- `depends_on` entries are valid: no duplicates, no self-references, no unknown phase names, no dependency cycles.

## Tips for writing effective prompts

Workflow prompts run in headless, autonomous LLM sessions. There is no human in the loop to answer questions or clarify ambiguity. Write your prompts with that constraint in mind.

### Be explicit about the task boundaries

Tell the agent exactly what it should and should not do. Autonomous sessions that lack clear boundaries tend to make sweeping changes or get stuck asking questions that nobody will answer.

```
You are running in a non-interactive session. Do NOT ask for user input at any point.
Do not create new branches -- work on the current worktree branch.
Do not modify CI/CD, deployment configs, or unrelated files.
```

### Provide the issue context early

Put `{{.Issue.Title}}`, `{{.Issue.URL}}`, and `{{.Issue.Body}}` near the top of every prompt. The agent needs to understand what it is working on before it can follow instructions.

### Use previous outputs to build continuity

Each prompt phase starts a fresh session with no memory of prior phases. The only way to carry context forward is through `{{.PreviousOutputs.<name>}}`. Reference earlier phase outputs explicitly:

```
## Analysis from Phase 1
{{.PreviousOutputs.analyze}}

## Plan from Phase 2
{{.PreviousOutputs.plan}}

Implement the plan above. Do not deviate from it.
```

### Handle gate retries gracefully

If your prompt phase has a command gate, include a conditional block for `{{.GateResult}}` so the agent understands what went wrong on retry:

```
{{if .GateResult}}
## Previous Attempt Failed
The following check failed after your last attempt:

{{.GateResult}}

Fix the issues identified above before proceeding.
{{end}}
```

Without this, a retried phase has no information about why the previous attempt was rejected.

### Keep phases focused

Each phase should do one thing well. Resist the temptation to combine analysis, implementation, and PR creation into a single phase. Smaller phases are easier to debug, produce cleaner outputs for downstream phases, and allow you to place gates at meaningful checkpoints.

### Set turn limits based on task complexity

A phase that reads code and writes a plan needs fewer turns than a phase that implements changes across multiple files. Underprovision turns and the work gets cut short. Overprovision and you waste tokens on a session that finished early anyway (the provider will stop when it is done, regardless of the limit).

### Specify tool usage when appropriate

If a prompt phase should only read code (no edits), use the `allowed_tools` field to restrict what the agent can do:

```yaml
- name: analyze
  prompt_file: .xylem/prompts/my-workflow/analyze.md
  max_turns: 10
  allowed_tools: "Read, Grep, Glob"
```

This prevents an analysis phase from accidentally making code changes.

The value is passed directly to the selected provider CLI (`--allowedTools` for Claude, `--allowed-tools` for Copilot), so use the syntax your provider expects.

### Test your prompts manually

Before relying on a workflow in production, test each prompt template by running it manually with `xylem enqueue`:

```bash
xylem enqueue --workflow my-workflow --ref "https://github.com/owner/repo/issues/1"
xylem drain
```

Check the phase outputs in `.xylem/phases/` to see whether the agent followed your instructions, whether the gate caught real problems, and whether the phase-to-phase handoff carried enough context.
