# Configuration Reference

This document covers every field in `.xylem.yml`, with defaults, validation rules, and annotated examples.

## File location

xylem looks for `.xylem.yml` in the root of your repository. Run `xylem init` to generate a starter config that auto-detects your GitHub remote and pre-fills the `repo` field.

```
your-repo/
  .xylem.yml          <-- configuration file
  .xylem/              <-- state directory (queue, workflows, prompts, phase outputs)
    queue.jsonl
    HARNESS.md
    workflows/
    prompts/
    phases/
```

## Minimal config

The smallest valid config for scanning GitHub issues:

```yaml
sources:
  bugs:
    type: github
    repo: owner/name
    tasks:
      fix-bugs:
        labels: [bug]
        workflow: fix-bug
```

Everything else falls back to defaults. You can also run xylem with no sources at all (for enqueue-only usage) -- an empty file with no `sources` key is valid.

## Full annotated config

```yaml
# ---------------------------------------------------------------------------
# Sources: where xylem finds work
# ---------------------------------------------------------------------------
sources:
  bugs:                                   # arbitrary name, used in logs
    type: github                          # source type
    repo: owner/name                      # GitHub repo in owner/name format
    exclude: [wontfix, duplicate, no-bot] # issues with these labels are skipped
    tasks:
      fix-bugs:                           # arbitrary task name
        labels: [bug, ready-for-work]     # all listed labels must be present
        workflow: fix-bug                 # workflow definition to invoke

  features:
    type: github
    repo: owner/name
    exclude: [wontfix, duplicate, no-bot]
    tasks:
      implement-features:
        labels: [enhancement, low-effort, ready-for-work]
        workflow: implement-feature

# ---------------------------------------------------------------------------
# Execution limits
# ---------------------------------------------------------------------------
concurrency: 2          # max simultaneous sessions
max_turns: 50           # max turns per prompt phase or prompt-only run
timeout: "30m"          # per-session timeout (Go duration string)

# ---------------------------------------------------------------------------
# State and branch management
# ---------------------------------------------------------------------------
state_dir: ".xylem"     # directory for queue, workflows, prompts, phase outputs
default_branch: "main"  # branch to create worktrees from (auto-detected if omitted)
cleanup_after: "168h"   # remove worktrees older than this (default: 7 days)

# ---------------------------------------------------------------------------
# Session runner settings
# ---------------------------------------------------------------------------
llm: claude
model: ""                                     # optional default model for prompt phases

claude:
  command: "claude"                           # Claude CLI binary name or path
  flags: "--bare --dangerously-skip-permissions"  # flags passed to every session
  default_model: ""                           # optional default model for Claude phases
  env:                                        # parsed config map for Claude-related environment values
    ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"  # supports shell variable substitution

copilot:
  command: "copilot"                          # Copilot CLI binary name or path
  flags: ""                                   # flags passed to every session
  default_model: ""                           # optional default model for Copilot phases
  env: {}                                     # parsed config map for Copilot-related environment values

# ---------------------------------------------------------------------------
# Daemon mode intervals
# ---------------------------------------------------------------------------
daemon:
  scan_interval: "60s"   # how often the daemon scans for new work
  drain_interval: "30s"  # how often the daemon drains pending vessels
```

## Field reference

### Top-level fields

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `sources` | map | -- | No | Map of source names to source configurations. Not required if you only use `xylem enqueue`. |
| `concurrency` | integer | `2` | No | Maximum number of simultaneous sessions. Must be greater than 0. |
| `max_turns` | integer | `50` | No | Maximum turns per prompt phase or prompt-only run. Must be greater than 0. |
| `timeout` | string | `"30m"` | No | Per-session timeout. Must be a valid Go duration string and at least `30s`. |
| `state_dir` | string | `".xylem"` | No | Directory for queue file, workflows, prompts, and phase outputs. |
| `default_branch` | string | auto-detected | No | Git branch to create worktrees from. If omitted, xylem detects it from the repository. |
| `cleanup_after` | string | `"168h"` | No | Age threshold for worktree cleanup. Must be a valid Go duration string. |
| `llm` | string | `"claude"` | No | Default LLM provider. Valid values: `claude`, `copilot`. |
| `model` | string | `""` | No | Default model for prompt phases. Provider-specific string (e.g., `claude-sonnet-4-5-20250514`). Overridden by source-level, workflow-level, or phase-level model settings. |
| `claude` | object | see below | No | Claude CLI session settings. |
| `copilot` | object | see below | No | GitHub Copilot CLI session settings. |
| `daemon` | object | see below | No | Daemon mode polling intervals. |
| `harness` | object | see below | No | Agent safety guardrails: protected file surfaces, policy rules, and audit logging. |
| `observability` | object | see below | No | OpenTelemetry instrumentation settings. |
| `cost` | object | see below | No | Token budget enforcement settings. |

### Sources

Each key under `sources` is an arbitrary name (used in logs and vessel metadata). The value is a source configuration object.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `type` | string | -- | Yes | Source type. Supported values: `"github"`, `"github-pr"`, `"github-pr-events"`, `"github-merge"`. |
| `repo` | string | -- | Yes (GitHub sources) | GitHub repository in `owner/name` format. Validated strictly -- both owner and name must be non-empty. |
| `exclude` | list of strings | `[]` | No | Labels that prevent an issue from being queued. If an issue has any of these labels, it is skipped. |
| `llm` | string | `""` | No | Provider override for this source. Valid values: `claude`, `copilot`. When set, all tasks in this source use this provider instead of the top-level `llm`. |
| `model` | string | `""` | No | Model override for this source. When set, all tasks in this source use this model instead of the top-level or provider-default model. |
| `tasks` | map | -- | Yes | Map of task names to task configurations. At least one task is required per source. |

### Tasks

Each key under `tasks` is an arbitrary name. The value defines which issues match and which workflow runs.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `labels` | list of strings | -- | Required for `github` and `github-pr` | Labels that trigger this task. The item must have all listed labels to match. |
| `workflow` | string | -- | Yes | Name of the workflow to invoke (e.g., `fix-bug`, `implement-feature`). Must not be empty or whitespace-only. Corresponds to a YAML file in `<state_dir>/workflows/`. |
| `status_labels` | object | omitted | No | Optional labels to apply as a vessel moves through queue states. Supported for `github` and `github-pr`. |
| `on` | object | omitted | Required for `github-pr-events` | Event triggers for pull-request event scanning. Must include at least one trigger. |

### Task fields by source type

- `github`: requires `labels`, supports `status_labels`
- `github-pr`: requires `labels`, supports `status_labels`
- `github-pr-events`: requires `workflow` and `on`
- `github-merge`: requires `workflow`

### `status_labels`

When `status_labels` is set, xylem records the configured labels in vessel metadata and applies them during source lifecycle hooks.

```yaml
tasks:
  fix-bugs:
    labels: [bug, ready-for-work]
    workflow: fix-bug
    status_labels:
      queued: queued
      running: in-progress
      completed: done
      failed: bot-failed
      timed_out: timed-out
```

Behavior:

- `queued` is added when the vessel is enqueued
- `running` replaces `queued` when work starts
- `completed`, `failed`, and `timed_out` replace `running` on terminal states
- If `status_labels` is omitted entirely, `github` and `github-pr` keep the legacy fallback of adding `in-progress` on start
- If the block is present and a field is empty, xylem skips that specific label operation

### `on`

`github-pr-events` tasks use an `on` block to declare which PR events create vessels:

```yaml
tasks:
  review-followup:
    workflow: review-followup
    on:
      labels: [needs-agent]
      review_submitted: true
      checks_failed: true
      commented: true
      author_allow:
        - "copilot-pull-request-reviewer[bot]"
      author_deny: []
```

Supported triggers:

| Field | Type | Description |
|-------|------|-------------|
| `labels` | list of strings | Create a vessel when an open PR has any listed label. |
| `review_submitted` | bool | Create a vessel for each submitted review, deduped by review ID. |
| `checks_failed` | bool | Create a vessel when a PR has failed checks, deduped by head SHA. |
| `commented` | bool | Create a vessel for each issue comment on the PR, deduped by comment ID. |
| `author_allow` | list of strings | Allowlist of GitHub logins whose reviews/comments create vessels. If non-empty, events from any other login are skipped. Applied to `review_submitted` and `commented`. |
| `author_deny` | list of strings | Denylist of GitHub logins whose reviews/comments are always skipped. Takes precedence over `author_allow`. Applied to `review_submitted` and `commented`. |

**Mandatory author filter for authored-event triggers.** Any task with `review_submitted: true` or `commented: true` **must** specify at least one of `author_allow` / `author_deny`. This is enforced at config-load time to prevent self-trigger feedback loops — when xylem posts a review in response to another review, the response itself is a new `review_submitted` event, which would trigger another vessel, and so on. The allowlist/denylist filters these out.

On top of the configured filters, xylem always drops any event authored by its own authenticated GitHub user (looked up via `gh api user`). This is a hard-coded safety net: even if the allowlist is misconfigured, xylem will never respond to itself.

**Quote bot logins for portability**. GitHub bot accounts have a `[bot]` suffix (e.g. `copilot-pull-request-reviewer[bot]`). Xylem's YAML parser (`gopkg.in/yaml.v3`) accepts the unquoted form as a plain scalar, but some other YAML 1.1 parsers (notably Python's PyYAML) reject it because `[` starts a flow sequence. Quote bot logins so the same config file is portable:

```yaml
author_allow:
  - "copilot-pull-request-reviewer[bot]"  # quoted for portability
```

### LLM provider settings

`llm` selects the default provider for prompt phases and prompt-only runs. Resolution order is:

1. phase-level `llm` in a workflow
2. workflow-level `llm`
3. source-level `llm` in `.xylem.yml`
4. top-level `.xylem.yml` `llm`
5. default `claude`

Valid values are `claude` and `copilot`.

**Model resolution** follows a similar chain:

1. phase-level `model`
2. workflow-level `model`
3. source-level `model`
4. top-level `model`
5. provider `default_model` (e.g., `claude.default_model`)

### Claude session settings

The `claude` section controls how xylem invokes the Claude CLI for each session.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `claude.command` | string | `"claude"` | No | Claude CLI binary name or absolute path. |
| `claude.flags` | string | `""` | No | Additional CLI flags passed to every session. Passed as a single string (e.g., `"--bare --dangerously-skip-permissions"`). |
| `claude.default_model` | string | `""` | No | Default Claude model for prompt phases when no workflow or phase model overrides it. |
| `claude.env` | map of string to string | `{}` | No | Claude-related environment map in config. `--bare` validation checks this map for `ANTHROPIC_API_KEY`. |

**Validation rules:**

- If `claude.flags` contains `--bare`, then `claude.env` must include a non-empty `ANTHROPIC_API_KEY`. The `--bare` flag disables Claude's built-in authentication, so you must provide your own API key.
- `claude.template` is no longer supported and produces a hard error if present. Migrate to phase-based workflows in `<state_dir>/workflows/`.
- `claude.allowed_tools` is no longer supported and produces a hard error if present. Define allowed tools in workflow phase definitions instead.

### Copilot session settings

The `copilot` section controls how xylem resolves the GitHub Copilot CLI command when the provider is `copilot`.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `copilot.command` | string | `"copilot"` | No | Copilot CLI binary name or absolute path. Must be non-empty if `llm: copilot`. |
| `copilot.flags` | string | `""` | No | Additional CLI flags passed to every Copilot session. |
| `copilot.default_model` | string | `""` | No | Default Copilot model for prompt phases when no workflow or phase model overrides it. |
| `copilot.env` | map of string to string | `{}` | No | Copilot-related environment map in config. |

### Daemon settings

The `daemon` section controls polling intervals when running `xylem daemon`.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `daemon.scan_interval` | string | `"60s"` | No | How often the daemon scans sources for new work. Must be a valid Go duration string. |
| `daemon.drain_interval` | string | `"30s"` | No | How often the daemon drains pending vessels from the queue. Must be a valid Go duration string. |

### Harness settings

The `harness` section configures agent safety guardrails: protected file surfaces, policy rules, and audit logging.

When `harness.policy.rules` is empty, xylem installs a default policy that denies writes to the protected control surfaces, requires approval for `git_push` and `pr_create`, and allows other actions. The runner classifies those high-risk actions from rendered command phases and from prompt phases that explicitly instruct an agent to push or open a pull request. The same `harness.protected_surfaces.paths` list also drives the worktree's read-only hardening and the runner's post-phase surface verification.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `harness.protected_surfaces.paths` | list of strings | `[".xylem/HARNESS.md", ".xylem.yml", ".xylem/workflows/*.yaml", ".xylem/prompts/*/*.md"]` | No | Glob patterns for files agents cannot modify. Set to `["none"]` to disable all surface protections. |
| `harness.policy.rules` | list of objects | `[]` | No | Policy rules for action authorization. Each rule has `action`, `resource`, and `effect`. |
| `harness.audit_log` | string | `"audit.jsonl"` | No | Path to the audit log file for policy decisions, relative to the state directory. |

**Policy rule fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `action` | string | Yes | The action to match (e.g., `file_write`, `git_push`, `pr_create`, `*`). |
| `resource` | string | Yes | The resource to match (e.g., `.xylem.yml`, `*`). Supports glob patterns. |
| `effect` | string | Yes | The effect when matched. Valid values: `allow`, `deny`, `require_approval`. |

```yaml
harness:
  protected_surfaces:
    paths:
      - ".xylem/HARNESS.md"
      - ".xylem.yml"
      - ".xylem/workflows/*.yaml"
      - ".xylem/prompts/*/*.md"
  policy:
    rules:
      - action: "file_write"
        resource: ".xylem.yml"
        effect: deny
      - action: "git_push"
        resource: "*"
        effect: require_approval
      - action: "pr_create"
        resource: "*"
        effect: require_approval
  audit_log: "audit.jsonl"
```

### Observability settings

The `observability` section controls OpenTelemetry instrumentation for tracing agent execution. Tracing requires an OTLP endpoint — when no endpoint is configured, tracing is silently disabled (no stdout fallback).

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `observability.enabled` | bool | `true` | No | Enable or disable OpenTelemetry tracing. |
| `observability.endpoint` | string | `""` | No | OTLP gRPC endpoint for trace export (e.g., `localhost:4317`). Tracing is disabled when empty. |
| `observability.insecure` | bool | `false` | No | Allow insecure (non-TLS) connections to the OTLP endpoint. |
| `observability.sample_rate` | float | `0.0` | No | Trace sampling rate between 0.0 and 1.0. Values outside this range are rejected. |

For local development, run Jaeger via the included Docker Compose stack:

```bash
docker compose -f dev/docker-compose.yml up -d
```

Then configure your `.xylem.yml`:

```yaml
observability:
  enabled: true
  endpoint: "localhost:4317"
  insecure: true
  sample_rate: 1.0
```

Traces are visible at `http://localhost:16686` (Jaeger UI) and queryable via the Jaeger API at `http://localhost:16686/api/`.

### Cost settings

The `cost` section configures token budget enforcement for agent sessions.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `cost.budget.max_cost_usd` | float | `0` | No | Maximum cost in USD. Must be >= 0. Set to 0 to disable cost-based limits. |
| `cost.budget.max_tokens` | integer | `0` | No | Maximum total tokens. Must be >= 0. Set to 0 to disable token-based limits. |

## Duration strings

Several fields accept Go duration strings. These are parsed by Go's `time.ParseDuration` and support the following units:

| Unit | Suffix | Example |
|------|--------|---------|
| Nanoseconds | `ns` | `500ns` |
| Microseconds | `us` or `\u00b5s` | `100us` |
| Milliseconds | `ms` | `500ms` |
| Seconds | `s` | `30s` |
| Minutes | `m` | `15m` |
| Hours | `h` | `168h` |

You can combine units: `1h30m`, `2m30s`. There is no `d` (day) suffix -- use `24h` for one day, `168h` for one week.

## Environment variable substitution

The config shape supports `${VAR}` placeholders in `claude.env` and `copilot.env` values:

```yaml
claude:
  env:
    ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
    GITHUB_TOKEN: "${GITHUB_TOKEN}"
    CUSTOM_FLAG: "static-value"
```

Use this pattern if you want config values to mirror environment-variable names rather than hard-coded secrets.

## Default branch detection

If `default_branch` is omitted, xylem auto-detects the default branch from the repository's git configuration. You only need to set this explicitly if:

- Your repository uses a non-standard default branch name that git cannot detect
- You want worktrees to branch from a specific branch other than the default

```yaml
# Only needed if auto-detection does not work for your setup
default_branch: "develop"
```

## Cleanup settings

The `cleanup_after` field controls how long worktrees persist before `xylem cleanup` removes them.

```yaml
cleanup_after: "168h"   # 7 days (default)
cleanup_after: "24h"    # 1 day
cleanup_after: "720h"   # 30 days
```

If `cleanup_after` is empty or omitted, it defaults to `168h` (7 days). If the value is present but not a valid duration, validation fails with an error.

Run `xylem cleanup` to remove worktrees that exceed this age. Use `xylem cleanup --dry-run` to preview what would be removed.

## Exclude labels

The `exclude` list prevents issues from being queued, regardless of which task labels they match. This is useful for issues that are tagged for work but temporarily blocked.

Default exclude labels (applied when using the legacy format or when not explicitly overridden):

```yaml
exclude: [wontfix, duplicate, in-progress, no-bot]
```

In the multi-source format, each source has its own `exclude` list. There is no global exclude -- you set it per source.

## Multiple sources

You can configure multiple sources that scan different repositories or different label sets within the same repository:

```yaml
sources:
  frontend-bugs:
    type: github
    repo: myorg/frontend
    exclude: [wontfix, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, triaged]
        workflow: fix-bug

  backend-bugs:
    type: github
    repo: myorg/backend
    exclude: [wontfix, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, triaged]
        workflow: fix-bug

  backend-features:
    type: github
    repo: myorg/backend
    exclude: [wontfix, no-bot, needs-design]
    tasks:
      implement-features:
        labels: [enhancement, approved]
        workflow: implement-feature
```

Each source is scanned independently. Deduplication prevents the same issue from being queued twice.

## Additional source examples

### Scan pull requests by label

```yaml
sources:
  review-queue:
    type: github-pr
    repo: myorg/myrepo
    exclude: [no-bot]
    tasks:
      pr-followup:
        labels: [needs-agent]
        workflow: review-followup
        status_labels:
          queued: queued
          running: in-progress
          completed: done
```

### Scan PR events

```yaml
sources:
  pr-events:
    type: github-pr-events
    repo: myorg/myrepo
    tasks:
      investigate:
        workflow: investigate-pr
        on:
          labels: [needs-agent]
          review_submitted: true
          checks_failed: true
          commented: true
          # Required for review_submitted / commented.
          author_allow:
            - "copilot-pull-request-reviewer[bot]"
```

### Scan merged pull requests

```yaml
sources:
  post-merge:
    type: github-merge
    repo: myorg/myrepo
    tasks:
      followup:
        workflow: post-merge-followup
```

## Legacy config format

xylem still supports an older flat format with top-level `repo`, `tasks`, and `exclude` fields. On load, this format is automatically normalized into a single `github` source named `"github"`.

### Legacy format

```yaml
repo: owner/name

tasks:
  fix-bugs:
    labels: [bug, ready-for-work]
    workflow: fix-bug

concurrency: 2
max_turns: 50
timeout: "30m"
state_dir: ".xylem"
exclude: [wontfix, duplicate, in-progress, no-bot]
```

### What happens at load time

The `normalize()` function detects the legacy format (top-level `repo` is set, `sources` is empty, and `tasks` is non-empty) and rewrites it internally to:

```yaml
sources:
  github:
    type: github
    repo: owner/name
    exclude: [wontfix, duplicate, in-progress, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, ready-for-work]
        workflow: fix-bug
```

The migration is transparent -- you do not need to change your config file. Both formats are validated the same way after normalization.

### When to migrate

The legacy format only supports a single GitHub source. If you need multiple sources, different exclude lists per source, or sources scanning different repositories, migrate to the multi-source format.

## Removed fields

The following fields are no longer supported. If present in your config, xylem exits with an error and a migration message.

| Removed field | Error message | Migration path |
|---------------|---------------|----------------|
| `claude.template` | `claude.template is no longer supported; migrate to phase-based workflows in .xylem/workflows/` | Define prompt templates per phase in your workflow YAML files under `<state_dir>/workflows/`. See the [README](../README.md) for workflow definition format. |
| `claude.allowed_tools` | `claude.allowed_tools is no longer supported; use allowed_tools in workflow phase definitions` | Move tool allowlists into individual workflow phase definitions. |

## Validation summary

The following rules are enforced when loading `.xylem.yml`. If any rule fails, `Load()` returns an error and xylem does not start.

| Rule | Error |
|------|-------|
| `concurrency` must be > 0 | `concurrency must be greater than 0` |
| `max_turns` must be > 0 | `max_turns must be greater than 0` |
| `timeout` must be a valid Go duration | `timeout must be a valid duration: ...` |
| `timeout` must be >= 30s | `timeout must be at least 30s` |
| `cleanup_after`, if set, must be a valid Go duration | `cleanup_after must be a valid duration: ...` |
| `llm`, if set, must be `claude` or `copilot` | `llm must be "claude" or "copilot"` |
| `copilot.command` must be non-empty when `llm: copilot` | `copilot.command must be non-empty` |
| `claude.template` must not be present | `claude.template is no longer supported...` |
| `claude.allowed_tools` must not be present | `claude.allowed_tools is no longer supported...` |
| `--bare` in flags requires `ANTHROPIC_API_KEY` in env | `--bare requires ANTHROPIC_API_KEY in claude.env` |
| `daemon.scan_interval`, if set, must be a valid Go duration | `daemon.scan_interval must be a valid duration: ...` |
| `daemon.drain_interval`, if set, must be a valid Go duration | `daemon.drain_interval must be a valid duration: ...` |
| Each source must have a `type` | `source "<name>" must specify a type` |
| `github` and `github-pr` sources must have a `repo` in `owner/name` format | `source "<name>" (github): repo must be in owner/name format` |
| `github-pr-events` sources must have a `repo` in `owner/name` format | `source "<name>" (github-pr-events): repo must be in owner/name format` |
| `github-merge` sources must have a `repo` in `owner/name` format | `source "<name>" (github-merge): repo must be in owner/name format` |
| `github`, `github-pr`, `github-pr-events`, and `github-merge` sources must have at least one task | `source "<name>" ...: at least one task is required` |
| `github` and `github-pr` tasks must have at least one label | `source "<name>" task "<task>": must include at least one labels entry` |
| `github-pr-events` tasks must include an `on` block | `source "<name>" task "<task>": must include an 'on' block...` |
| `github-pr-events` `on` blocks must include at least one trigger | `source "<name>" task "<task>": 'on' block must specify at least one trigger...` |
| `github-pr-events` tasks with `review_submitted` or `commented` must specify an author filter | `source "<name>" task "<task>": tasks with review_submitted or commented must specify author_allow or author_deny to prevent self-trigger loops` |
| Every task must have a non-empty workflow | `source "<name>" task "<task>": must include a workflow` |
| Source-level `llm`, if set, must be `claude` or `copilot` | `source "<name>": llm must be "claude" or "copilot", got "<value>"` |
| `harness.protected_surfaces.paths` entries must be valid globs | `harness.protected_surfaces.paths: invalid glob "<pattern>": ...` |
| `harness.policy.rules[].action` must be non-empty | `harness.policy.rules[N]: action is required` |
| `harness.policy.rules[].resource` must be non-empty | `harness.policy.rules[N]: resource is required` |
| `harness.policy.rules[].effect` must be `allow`, `deny`, or `require_approval` | `harness.policy.rules[N]: invalid effect "<value>" (must be allow, deny, or require_approval)` |
| `observability.sample_rate` must be in [0.0, 1.0] | `observability.sample_rate must be in [0.0, 1.0]` |
| `cost.budget.max_cost_usd` must be >= 0 | `cost.budget.max_cost_usd must be non-negative` |
| `cost.budget.max_tokens` must be >= 0 | `cost.budget.max_tokens must be non-negative` |
