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

Each phase produces output that subsequent phases can reference. Gates act as checkpoints -- if a gate fails and retries are exhausted, the vessel is marked as failed. If a gate is a label gate, the vessel enters a `waiting` state until a human applies the required label on GitHub. Live gates also persist step evidence under `.xylem/phases/<vessel-id>/evidence/`.

Phases can also publish their final output to GitHub Discussions by setting `output: discussion`. In that mode, xylem uses the phase output as the discussion body and renders the configured discussion title templates with the same Go-template data available to prompts. If the phase also declares a `noop` matcher and the output matches it, xylem skips publication and completes early just like any other no-op phase.

The built-in workflows scaffolded by `xylem init` are profile-driven. The core profile seeds delivery workflows such as `fix-bug` and `implement-feature`, plus recurring operator workflows such as `lessons`, `context-weight-audit`, `workflow-health-report`, and `security-compliance`. Repo-specific overlays such as `implement-harness` and `continuous-improvement` are not part of the base core scaffold and must be added through an overlay profile or checked-in repo assets. The workflow format also supports `type: command` phases for deterministic shell steps inside the same execution pipeline.

In this repository, `continuous-improvement` is a concrete example of mixing both styles: a deterministic `select_focus` command phase picks the next focus area and persists rotation state, then prompt phases analyze, plan, implement, and verify one small scheduled improvement.

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
      type: command                                # "command", "label", or "live"
      run: "make test"                             # Shell command to execute
      retries: 2                                   # Retry count on failure (default: 0)
      retry_delay: "10s"                           # Delay between retries (default: "10s")

  - name: pr
    prompt_file: .xylem/prompts/fix-bug/pr.md
    max_turns: 3

  - name: smoke_test
    type: command                                  # Optional shell-command phase
    run: "make smoke-test"

  - name: weekly_report
    type: command
    run: "cat .xylem/state/weekly-report.md"
    output: discussion
    discussion:
      category: Reports
      title_template: "Weekly Report — {{.Date}}"
      title_search_template: "Weekly Report"
```

### Field reference

**Top-level fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Workflow identifier. Must match the YAML filename. |
| `description` | No | Human-readable description of the workflow's purpose. |
| `llm` | No | Default provider for prompt phases in this workflow. Valid values: `claude`, `copilot`. |
| `model` | No | Default model for prompt phases in this workflow. Provider-specific string. |
| `allow_additive_protected_writes` | No | Permits this workflow to create new files that match configured protected-surface patterns without failing post-phase verification. Existing protected files are still immutable. |
| `allow_canonical_protected_writes` | No | Permits this workflow to modify existing protected files when the vessel's issue body explicitly names the same protected path being changed. |
| `phases` | Yes | Ordered list of phases. At least one is required. |

Protected-surface write allowances are intentionally narrow. `allow_additive_protected_writes` only covers new protected files, while `allow_canonical_protected_writes` still requires the triggering issue body to name the protected path being edited so a workflow cannot silently rewrite unrelated control-plane files.

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
| `output` | No | Optional post-phase output target. Supported values: `discussion`. |
| `discussion` | No | GitHub Discussions publishing config. Requires `output: discussion`. |
| `allowed_tools` | No | Tool restriction string for prompt phases. The runner resolves it through the harness tool catalog for the phase role, rejecting unauthorized tools and deriving the role default list when this field is omitted. That role comes from `harness.tool_permissions.phase_roles` when configured, otherwise from the workflow class (with a phase-name fallback only when no class is available). The resulting list is then passed to the provider CLI. |
| `gate` | No | Quality gate that must pass after this phase completes. |
| `depends_on` | No | List of phase names this phase depends on. Enables parallel execution -- phases without dependency relationships can execute concurrently. Validated for duplicate entries, self-references, references to unknown phase names, and dependency cycles. |

**No-op fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `match` | Yes | Substring marker that, when present in successful phase output, completes the workflow early. |

**Discussion output fields (when `output: discussion`):**

| Field | Required | Description |
|-------|----------|-------------|
| `discussion.category` | Yes | GitHub Discussions category name to post into. |
| `discussion.title_template` | Yes | Go template rendered into the discussion title. |
| `discussion.title_search_template` | No | Prefix used to find an existing discussion to comment on instead of creating a new one. Defaults to the rendered title. |

Discussion title templates can use the normal phase template data plus `{{.Date}}`, which resolves to the current UTC date in `YYYY-MM-DD` form.

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

**Gate fields (when `type: live`):**

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `type` | Yes | -- | Must be `"live"`. |
| `retries` | No | `0` | Number of times to retry the phase if the live verification fails. |
| `retry_delay` | No | `"10s"` | Go duration string for delay between retries. |
| `live.mode` | Yes | -- | Verification mode: `http`, `browser`, or `command+assert`. |
| `live.timeout` | No | inherited per mode | Overall timeout applied to the live verification. |

**Live HTTP fields (`live.mode: http`):**

| Field | Required | Description |
|-------|----------|-------------|
| `live.http.base_url` | No | Base URL used to resolve relative `steps[].url` values. |
| `live.http.steps` | Yes | Ordered HTTP verification steps. |
| `live.http.steps[].name` | No | Human-readable step name. Defaults to `method url`. |
| `live.http.steps[].method` | No | HTTP method. Defaults to `GET`. |
| `live.http.steps[].url` | Yes | Absolute URL or path relative to `base_url`. |
| `live.http.steps[].headers` | No | Request headers to send. |
| `live.http.steps[].body` | No | Request body. |
| `live.http.steps[].timeout` | No | Per-step timeout. |
| `live.http.steps[].expect_status` | No | Expected HTTP status code. |
| `live.http.steps[].expect_headers` | No | Response header assertions with `name` plus `equals` or `regex`. |
| `live.http.steps[].expect_json` | No | JSONPath assertions with `path` plus `equals`, `regex`, or `exists`. |
| `live.http.steps[].expect_body_regex` | No | Regular expression that must match the response body. |

**Live browser fields (`live.mode: browser`):**

| Field | Required | Description |
|-------|----------|-------------|
| `live.browser.base_url` | No | Base URL used to resolve `navigate` step URLs. |
| `live.browser.headless` | No | Run Chromium headless. Defaults to true. |
| `live.browser.steps` | Yes | Ordered browser actions/assertions. |
| `live.browser.steps[].action` | Yes | One of `navigate`, `click`, `type`, `wait_visible`, `assert_visible`, `assert_text`. |
| `live.browser.steps[].url` | For `navigate` | Absolute URL or path relative to `base_url`. |
| `live.browser.steps[].selector` | For selector-based actions | CSS selector to target. |
| `live.browser.steps[].value` | For `type` | Text to enter. |
| `live.browser.steps[].text` | For `assert_text` | Substring that must appear in the matched element. |
| `live.browser.steps[].timeout` | No | Per-step timeout. |

**Live command+assert fields (`live.mode: command+assert`):**

| Field | Required | Description |
|-------|----------|-------------|
| `live.command_assert.run` | Yes | Shell command to execute in the worktree. |
| `live.command_assert.timeout` | No | Command timeout. |
| `live.command_assert.expect_stdout_regex` | No | Regular expression that must match stdout. |
| `live.command_assert.expect_json` | No | JSONPath assertions evaluated against stdout interpreted as JSON. |

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

Live gates default their evidence level to `observed_in_situ`, checker to `live/<mode>`, and trust boundary to `Running system observation` unless you override those fields explicitly.

```yaml
gate:
  type: live
  retries: 1
  live:
    mode: http
    http:
      base_url: "http://127.0.0.1:3000"
      steps:
        - name: readiness
          url: /readyz
          expect_status: 200
        - name: health-json
          url: /health
          expect_status: 200
          expect_json:
            - path: $.status
              equals: ok
```

```yaml
gate:
  type: live
  live:
    mode: command+assert
    command_assert:
      run: "sqlite3 app.db 'select json_object(\"pending\", count(*)) from jobs where status = \"pending\";'"
      expect_json:
        - path: $.pending
          regex: "^[0-9]+$"
```

```yaml
gate:
  type: live
  live:
    mode: browser
    browser:
      base_url: "http://127.0.0.1:3000"
      steps:
        - action: navigate
          url: /login
        - action: type
          selector: 'input[name="email"]'
          value: test@example.com
        - action: type
          selector: 'input[name="password"]'
          value: secret
        - action: click
          selector: 'button[type="submit"]'
        - action: assert_text
          selector: "[data-test=dashboard]"
          text: Welcome
```

Live gate runs persist a machine-readable summary in `.xylem/phases/<vessel-id>/evidence/<phase-name>/live-gate.json`. Each step also saves evidence artifacts alongside that report under `.xylem/phases/<vessel-id>/evidence/<phase-name>/`, including:

- HTTP traces for HTTP steps
- stdout captures for command+assert steps
- DOM snapshots and screenshots for browser steps

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

For `github` and `github-pr` tasks, you can also set `label_gate_labels` in `.xylem.yml` so the runner applies deterministic `gh issue edit` / `gh pr edit` updates alongside the queue transition:

```yaml
tasks:
  fix-bugs:
    labels: [bug, ready-for-work]
    workflow: fix-bug
    label_gate_labels:
      waiting: blocked
      ready: ready-for-implementation
```

With that config, entering `waiting` adds `blocked`, resuming back to `pending` swaps `blocked` for `ready-for-implementation`, and terminal exits clean up any leftover label-gate labels so GitHub stays aligned with queue state.

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

This template is the first phase in the built-in workflows below. It receives the issue data and asks the agent to analyze the codebase:

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

This template uses outputs from multiple previous phases to provide full context when creating the pull request. It is a generic prompt-style example: a repo-specific workflow can instead use a deterministic command phase for PR creation. The YAML below mirrors the checked-in `.xylem/workflows/implement-harness.yaml`. The checked-in `implement-harness` workflow includes an explicit, deterministic `pr_create` command phase that validates `pr_draft.json`, appends `Fixes #{{.Issue.Number}}` to the PR body when appropriate, and passes repository-specific flags to `gh pr create`. This section documents the actual checked-in behavior (not only a generic example).

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

## Workflow reference

`xylem init` scaffolds the core profile into your repo: delivery workflows such as `fix-bug` and `implement-feature`, recurring operator workflows such as `lessons`, `context-weight-audit`, `workflow-health-report`, and `security-compliance`, plus `.xylem/HARNESS.md` and matching prompt templates. This repository also documents repo-specific overlay workflows such as `implement-harness`, but those are checked in here rather than generated by the base core profile alone.

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
4. **pr** -- Commits changes, pushes the branch, and creates a pull request linking to the issue. Under the default harness policy, `git_push` and `pr_create` are classified publication actions but still allowed so autonomous runs can finish. Add a workflow review gate or stricter `harness.policy.rules` if you want a human checkpoint before publication.

**When to use:** Assign this workflow to tasks triggered by `bug`-labeled GitHub issues. It works best for well-described bugs with clear reproduction steps.

**Customization:** After running `xylem init`, edit the `run` field in the implement phase's gate to match your project's test command. The scaffolded default is `make test`, but you might need `go test ./...`, `npm test`, `pytest`, or something else. If you want human review before publication, add a gate before the scaffolded `pr` phase or policy rules that require approval for `git_push` and `pr_create`.

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
4. **pr** -- Commits, pushes, and creates a pull request. Under the default harness policy, `git_push` and `pr_create` are classified publication actions but still allowed so autonomous runs can finish. Add a workflow review gate or stricter `harness.policy.rules` if you want a human checkpoint before publication.

**When to use:** Assign this workflow to tasks triggered by `enhancement`-labeled issues that have been refined and marked as ready for autonomous implementation.

**Customization:** After running `xylem init`, update the label gate and test command to match your process. For example, you might use a different approval label than `plan-approved`, or replace `make test` with `go test ./...`, `npm test`, or `pytest`. If you want human review before publication, add a gate before the scaffolded `pr` phase or policy rules that require approval for `git_push` and `pr_create`.

### security-compliance

Runs a scheduled, read-heavy repository security review in 4 phases and files follow-up issues when actionable risk is found.

```yaml
name: security-compliance
class: ops
description: "Run a scheduled security posture review and open follow-up issues for actionable risk"
phases:
  - name: scan_secrets
    prompt_file: .xylem/prompts/security-compliance/scan_secrets.md
    max_turns: 25
    gate:
      type: command
      run: |
        set -eu
        out=".xylem/phases/{{.Vessel.ID}}/scan_secrets.output"
        test -s "$out"
        grep -q '^RESULT:' "$out"
        grep -q '^FINDINGS:' "$out"
  - name: static_analysis
    prompt_file: .xylem/prompts/security-compliance/static_analysis.md
    max_turns: 25
    gate:
      type: command
      run: |
        set -eu
        out=".xylem/phases/{{.Vessel.ID}}/static_analysis.output"
        test -s "$out"
        grep -q '^RESULT:' "$out"
        grep -q '^FINDINGS:' "$out"
  - name: dependency_audit
    prompt_file: .xylem/prompts/security-compliance/dependency_audit.md
    max_turns: 25
    gate:
      type: command
      run: |
        set -eu
        out=".xylem/phases/{{.Vessel.ID}}/dependency_audit.output"
        test -s "$out"
        grep -q '^RESULT:' "$out"
        grep -q '^FINDINGS:' "$out"
  - name: synthesize
    prompt_file: .xylem/prompts/security-compliance/synthesize.md
    max_turns: 30
```

**Phase flow:**

1. **scan_secrets** -- Uses available secret scanners or targeted git/diff inspection to review recent activity for leaked credentials and records both findings and tooling gaps.
2. **static_analysis** -- Runs security-relevant analyzers that fit the repo (for example `actionlint`, `go vet`, `semgrep`, or `zizmor`) and emits a structured report. A command gate validates that the phase produced the required report headings under `.xylem/phases/<vessel-id>/`.
3. **dependency_audit** -- Audits dependency ecosystems for known CVEs and overdue remediation work, again with a command gate that validates the output artifact shape.
4. **synthesize** -- Combines all prior findings into one operator-ready report and, when `gh` is available, creates or updates `security`-labeled GitHub issues for actionable HIGH or CRITICAL findings instead of silently reporting them.

**When to use:** Assign this workflow to a `schedule` source for daily or weekly repository security posture reviews that should not depend on an issue trigger.

**Configuration example:**

```yaml
sources:
  security-compliance:
    type: schedule
    cadence: "@daily"
    workflow: security-compliance
```

### continuous-improvement (repo-specific)

Runs a scheduled self-hosting maintenance loop in 5 phases. The first phase is deterministic: it rotates through repo-specific, standard, and revisit focus buckets, persists durable state, and emits a focus brief for the later prompt phases.

```yaml
name: continuous-improvement
class: harness-maintenance
description: "Scheduled self-improvement loop for small, high-signal xylem maintenance changes"
phases:
  - name: select_focus
    type: command
    run: |
      set -euo pipefail
      go run ./cli/cmd/xylem --config .xylem.yml continuous-improvement select \
        --state .xylem/state/continuous-improvement/state.json \
        --selection .xylem/state/continuous-improvement/current-selection.json
  - name: analyze
    prompt_file: .xylem/prompts/continuous-improvement/analyze.md
    max_turns: 40
    noop:
      match: XYLEM_NOOP
  - name: plan
    prompt_file: .xylem/prompts/continuous-improvement/plan.md
    max_turns: 30
    noop:
      match: XYLEM_NOOP
  - name: implement
    prompt_file: .xylem/prompts/continuous-improvement/implement.md
    max_turns: 80
    gate:
      type: command
      run: |
        set -euo pipefail
        {{if .Validation.Format}}{{.Validation.Format}}{{else}}true{{end}}
        {{if .Validation.Lint}}{{.Validation.Lint}}{{end}}
        {{if .Validation.Build}}{{.Validation.Build}}{{end}}
        {{if .Validation.Test}}{{.Validation.Test}}{{else}}true{{end}}
      retries: 2
  - name: verify
    prompt_file: .xylem/prompts/continuous-improvement/verify.md
    max_turns: 40
```

**Phase flow:**

1. **select_focus** -- Runs `xylem continuous-improvement select`, applies the 60/30/10 weighting across repo-specific, standard, and revisit buckets, persists durable state under `.xylem/state/continuous-improvement/`, and prints the markdown brief consumed by later phases.
2. **analyze** -- Reads the focus brief plus the codebase, then chooses one small, mergeable improvement inside that slice. It can exit early with `XYLEM_NOOP` when the selected area has nothing safe and worthwhile to ship.
3. **plan** -- Converts the selected improvement into a file-by-file implementation plan and test strategy, again allowing `XYLEM_NOOP` if the analysis already established that the run should skip publication.
4. **implement** -- Makes the focused change and passes a command gate that reuses the repo's configured format/lint/build/test validation commands.
5. **verify** -- Rechecks that the diff still matches the selected focus, then commits, pushes, and opens the scheduled maintenance PR.

**Rotation strategy:** The selector keeps a durable history in `.xylem/state/continuous-improvement/state.json`, chooses repo-specific focus areas for 6 of every 10 runs, standard categories for 3 of every 10 runs, and a revisit slot for the 10th run. Candidate focus areas are part of the checked-in helper so the schedule can rotate predictably without needing a triggering GitHub issue.

**Configuration example:**

```yaml
sources:
  continuous-improvement:
    type: scheduled
    repo: nicholls-inc/xylem
    schedule: "@daily"
    tasks:
      daily-rotation:
        workflow: continuous-improvement
        ref: continuous-improvement
```

### implement-harness (repo-specific)

Implements a harness spec step with verification, testing, and smoke scenarios in 8 phases. Uses mixed LLM providers: Copilot (`gpt-5.4`) for implementation-heavy phases, and Claude for planning and verification.
The YAML below mirrors the checked-in `.xylem/workflows/implement-harness.yaml`, including the deterministic `pr_create` command phase.

```yaml
name: implement-harness
description: "Implement a harness spec step with verification, testing, and smoke scenarios"
phases:
  - name: analyze
    prompt_file: .xylem/prompts/implement-harness/analyze.md
    max_turns: 30
    llm: copilot
    model: gpt-5.4
    noop:
      match: XYLEM_NOOP
  - name: plan
    prompt_file: .xylem/prompts/implement-harness/plan.md
    max_turns: 30
    llm: claude
  - name: implement
    prompt_file: .xylem/prompts/implement-harness/implement.md
    max_turns: 80
    llm: copilot
    model: gpt-5.4
    gate:
      type: command
      run: "cd cli && go vet ./... && go build ./cmd/xylem && go test ./..."
      retries: 3
  - name: verify
    prompt_file: .xylem/prompts/implement-harness/verify.md
    max_turns: 80
    llm: claude
    gate:
      type: command
      run: "cd cli && go test ./..."
      retries: 2
  - name: test_critic
    prompt_file: .xylem/prompts/implement-harness/test_critic.md
    max_turns: 30
    llm: copilot
    model: gpt-5.4
    gate:
      type: command
      run: "cd cli && go test ./..."
      retries: 1
  - name: smoke
    prompt_file: .xylem/prompts/implement-harness/smoke.md
    max_turns: 60
    llm: copilot
    model: gpt-5.4
    gate:
      type: command
      run: "cd cli && go test ./..."
      retries: 2
  - name: pr_draft
    prompt_file: .xylem/prompts/implement-harness/pr_draft.md
    max_turns: 15
    llm: copilot
    model: gpt-5.4
  - name: pr_create
    type: command
    run: |
      set -euo pipefail

      git fetch origin main
      if ! git merge-base --is-ancestor origin/main HEAD; then
        echo "ERROR: branch is behind origin/main; rebase before creating the PR"
        exit 1
      fi

      DRAFT_FILE="pr_draft.json"
      if [ ! -f "$DRAFT_FILE" ]; then
        echo "ERROR: pr_draft.json not found in worktree root"
        exit 1
      fi

      TITLE=$(jq -r '.title' "$DRAFT_FILE")
      BODY=$(jq -r '.body' "$DRAFT_FILE")

      if [ -z "$TITLE" ] || [ "$TITLE" = "null" ]; then
        echo "ERROR: title is missing or null in pr_draft.json"
        exit 1
      fi

      BODY="${BODY}

      Fixes #{{.Issue.Number}}"

      gh pr create \
        --repo nicholls-inc/xylem \
        --title "$TITLE" \
        --body "$BODY" \
        --label "harness-impl" \
        --label "ready-to-merge"
```

Unlike the scaffolded `pr` prompt phases above, this repo-specific `pr_create` step is a deterministic command phase: it refuses to create the PR if the branch has fallen behind `origin/main`, validates `pr_draft.json`, appends `Fixes #{{.Issue.Number}}` so GitHub auto-closes the issue, targets `nicholls-inc/xylem` explicitly, and applies both `harness-impl` and `ready-to-merge` at PR creation so the merge workflow can pick the PR up immediately once checks are green.

**Phase flow:**

1. **analyze** -- Reads the spec step, the issue, and the codebase to understand what must change and why. If the output contains `XYLEM_NOOP`, the workflow exits early -- nothing to implement.
2. **plan** -- Produces a file-by-file implementation plan: function signatures, test cases mapped to smoke scenarios, implementation order, and edge cases.
3. **implement** -- Executes the plan: writes production code, then unit tests, then property-based tests. Gated on `go vet + go build + go test` with 3 retries, feeding failure output back via `{{.GateResult}}` each time.
4. **verify** -- Independent second pass: checks correctness of tests, looks for uncovered paths, and fixes any issues missed by the implementer. Gated on `go test` with 2 retries.
5. **test_critic** -- Reviews the test suite for test theatre: tests that pass but do not exercise real behavior. Gated on `go test` with 1 retry.
6. **smoke** -- Executes the assigned smoke scenarios end-to-end and writes evidence to `.xylem/phases/<id>/smoke/`. Gated on `go test` with 2 retries.
7. **pr_draft** -- Writes `pr_draft.json` (a `{"title": "...", "body": "..."}` file) for review before PR creation.
8. **pr_create** -- Reads `pr_draft.json` and calls `gh pr create`. No turns are consumed; this phase cannot be retried via gate. If `pr_draft.json` is missing or malformed, the checked-in workflow fails with a clear error.

**When to use:** Assign this workflow to issues labeled `harness-impl` -- spec steps that require code changes, tests, and smoke scenarios verified in CI. It is designed for this repo's own development process, not for general use in target repos.

**Customization:** The gate command (`cd cli && go vet ./... && go build ./cmd/xylem && go test ./...`) is specific to this repository. If you adapt this workflow for a different project, update the `run` fields in each gate to match that project's build and test commands. The `pr_create` phase reads `pr_draft.json` from the worktree root -- if your PR process requires additional flags (for example, a base branch or reviewer assignment), extend the `gh pr create` call there.

### verify-kernel (repo-specific)

A thin deterministic command phase that re-verifies any Dafny spec (`.dfy` file) touched by the current branch. Inserted between `implement` and `verify` in all three delivery workflows (`fix-bug`, `implement-feature`, `implement-harness`). Part of assurance roadmap item #08.

```yaml
# verify-kernel: roadmap #08 — governance amendment 2026-04-20
- name: verify_kernel
  type: command
  run: |
    set -euo pipefail
    scripts/verify-kernels.sh
```

**How it works:**

1. Runs `git fetch origin main` to ensure the comparison base is available.
2. Computes `git diff --name-only origin/main...HEAD` (3-dot diff) and filters for `.dfy` files. The 3-dot form includes only branch-local changes, not commits that landed on `main` after the branch diverged.
3. If no `.dfy` files changed, exits 0 immediately (typically under 1 second).
4. If `.dfy` files changed, runs `docker run --rm --network=none --memory=512m --cpus=1 crosscheck-dafny:latest verify` on each file in sequence with a 130-second timeout.
5. Exits 1 if any file fails verification; exits 0 if all pass.

**Soft fallbacks:**

- If `docker` is not in PATH, the phase exits 0 with a warning. Pre-commit is then the only enforcement path.
- If the `crosscheck-dafny:latest` image has not been built, the phase exits 0 with a warning and a pointer to `scripts/build-docker.sh` in the crosscheck plugin directory.

These fallbacks mean the gate is a no-op on machines or CI environments where the Dafny image is absent. Until the image is bootstrapped in the daemon environment, pre-commit enforcement is the primary line of defense.

**The gate logic lives in `scripts/verify-kernels.sh`.** The `DAFNY_DOCKER_IMAGE` environment variable overrides the default image name (`crosscheck-dafny:latest`) for testing with a pinned version.

**Evidence metadata:** Command phases have no `gate` block and therefore cannot attach formal `evidence:` metadata in the xylem workflow format. The verification result is implicit in the phase exit code. If a future workflow format supports top-level evidence annotations on command phases, add:

```yaml
evidence:
  claim: "All changed .dfy specs verify under Dafny"
  level: proved
  checker: "dafny_verify (crosscheck-dafny:latest)"
  trust_boundary: "formal verification of pure functions"
```

**When to use:** This phase fires automatically in all three delivery workflows. No configuration required. The phase is a no-op on PRs that do not touch `.dfy` files, so it adds no latency to the common case.

### The `intent-check` phase (assurance roadmap #07)

The `intent-check` phase addresses Layer 5 spec-intent alignment (see `docs/research/assurance-hierarchy.md`). It runs automatically after `implement` and before `pr_draft` whenever the change touches a protected-surface file listed in `.claude/rules/protected-surfaces.md`.

The phase uses a two-LLM pipeline modelled on Midspiral's claimcheck technique:

1. **Back-translator** — given the changed code and property test (but *not* the invariant doc), describes what guarantees the implementation actually enforces.
2. **Diff-checker** — given the original invariant prose and the back-translation, determines whether the back-translation captures the invariant's claimed guarantee.

If the diff-checker reports a mismatch, the phase fails. The PR does not proceed to `pr_draft` until the mismatch is resolved.

#### Attestation enforcement

`intent-check` writes `.xylem/intent-check-attestation.json` on pass. A pre-commit hook (`scripts/check-intent-attestation.sh`) verifies this attestation before accepting any commit that touches protected surfaces. The hook fails if the attestation is absent, the verdict is not `pass`, or the content hash no longer matches the staged files.

To run intent-check manually: `xylem-intent-check`

#### Kill criteria

- False-positive rate > 30% after 2 weeks of live operation (tracked in a CSV of every run)
- False negative on the seeded mismatch fixture in `cli/cmd/xylem-intent-check/testdata/seeded-mismatch/`
- Latency > 10 minutes per invocation after tuning

## Prompt file organization

Prompt files are usually organized in `.xylem/prompts/` under a subdirectory named after the workflow. This repository's checked-in layout looks like:

```
.xylem/
  HARNESS.md
  workflows/
    fix-bug.yaml
    implement-feature.yaml
    implement-harness.yaml
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
    implement-harness/
      analyze.md
      plan.md
      implement.md
      verify.md
      test_critic.md
      smoke.md
      pr_draft.md
```

This convention is not enforced -- `prompt_file` can point anywhere relative to the repo root. But grouping prompts by workflow keeps things navigable as you add more workflows.

`xylem init` scaffolds the base `.xylem` layout with working defaults for `fix-bug` and `implement-feature` plus a starter `HARNESS.md`; additional workflow and prompt directories, such as the repo-specific `implement-harness` example above, are added separately.

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

The runner first resolves the phase's effective tool list through the harness tool catalog and role permissions, then passes that list to the selected provider CLI (`--allowedTools` for Claude, `--available-tools` for Copilot).

### Test your prompts manually

Before relying on a workflow in production, test each prompt template by running it manually with `xylem enqueue`:

```bash
xylem enqueue --workflow my-workflow --ref "https://github.com/owner/repo/issues/1"
xylem drain
```

Check the phase outputs in `.xylem/phases/` to see whether the agent followed your instructions, whether the gate caught real problems, and whether the phase-to-phase handoff carried enough context.
