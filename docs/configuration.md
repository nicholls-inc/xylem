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

  doc-gardener:
    type: schedule
    cadence: "@daily"
    workflow: doc-garden

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
  stall_monitor:
    phase_stall_threshold: "10m"   # mark running vessels timed_out after no phase output activity
    scanner_idle_threshold: "5m"   # warn when queue stays idle while GitHub backlog exists
    orphan_check_enabled: true      # repair running vessels with no live tracked subprocess
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

### `daemon`

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `scan_interval` | string | `"60s"` | No | How often the daemon scans configured sources for new work. Must be a valid Go duration string. |
| `drain_interval` | string | `"30s"` | No | How often the daemon dequeues pending vessels. Must be a valid Go duration string. |
| `stall_monitor` | object | see below | No | Deterministic self-monitoring thresholds for phase stalls, idle-with-backlog detection, and orphan repair. |
| `auto_upgrade` | boolean | `false` | No | Enables periodic self-upgrade checks for the daemon binary. |
| `upgrade_interval` | string | `"5m"` | No | How often the daemon re-runs auto-upgrade checks while the loop is running. Must be a valid Go duration string. |
| `auto_merge` | boolean | `false` | No | Enables the merge-ready Copilot review + auto-merge cycle for xylem-authored PRs. |
| `auto_merge_repo` | string | current repo remote | No | Optional `owner/name` override for auto-merge GitHub operations. |

### `daemon.stall_monitor`

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `phase_stall_threshold` | string | `"10m"` | No | Maximum time since the most recent `*.output` activity for a running vessel before it is marked `timed_out`. Must be a valid Go duration string. |
| `scanner_idle_threshold` | string | `"5m"` | No | How long the queue may remain idle before xylem warns that GitHub backlog still exists. Must be a valid Go duration string. |
| `orphan_check_enabled` | boolean | `true` | No | When enabled, the daemon repairs running vessels that have no live tracked subprocess by transitioning them to `timed_out`. |

### Sources

Each key under `sources` is an arbitrary name (used in logs and vessel metadata). The value is a source configuration object.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `type` | string | -- | Yes | Source type. Supported values: `"github"`, `"github-pr"`, `"github-pr-events"`, `"github-merge"`, `"schedule"`, `"scheduled"`. |
| `repo` | string | -- | Yes (GitHub sources and `scheduled`) | GitHub repository in `owner/name` format. Validated strictly -- both owner and name must be non-empty. |
| `schedule` | string | -- | Required for `scheduled` | Recurring cadence for per-task scheduled sources. Supports `@hourly`, `@daily`, `@weekly`, or any positive Go duration like `168h`. |
| `cadence` | string | -- | Yes (`schedule`) | Recurrence for scheduled sources. Accepts Go durations like `1h`, cron descriptors like `@daily`, and standard 5-field cron expressions. |
| `workflow` | string | -- | Yes (`schedule`) | Workflow to enqueue each time a scheduled source fires. Scheduled sources define the workflow directly and do not use `tasks`. |
| `exclude` | list of strings | `[]` | No | Labels that prevent an issue from being queued. If an issue has any of these labels, it is skipped. |
| `llm` | string | `""` | No | Provider override for this source. Valid values: `claude`, `copilot`. When set, all tasks in this source use this provider instead of the top-level `llm`. |
| `model` | string | `""` | No | Model override for this source. When set, all tasks in this source use this model instead of the top-level or provider-default model. |
| `tasks` | map | -- | Yes (GitHub sources and `scheduled`) | Map of task names to task configurations. Required for GitHub-based sources and `scheduled`; not used by `schedule`. |

### Tasks

Each key under `tasks` is an arbitrary name. The value defines which issues match and which workflow runs.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `labels` | list of strings | -- | Required for `github` and `github-pr` | Labels that trigger this task. The item must have all listed labels to match. |
| `workflow` | string | -- | Yes | Name of the workflow to invoke (e.g., `fix-bug`, `implement-feature`). Must not be empty or whitespace-only. Corresponds to a YAML file in `<state_dir>/workflows/`. |
| `ref` | string | omitted | No | Optional stable task identifier stored in vessel metadata for recurring scheduled runs. Useful when downstream workflows want a task-level handle that remains stable across schedule windows. |
| `status_labels` | object | omitted | No | Optional labels to apply as a vessel moves through queue states. Supported for `github` and `github-pr`. |
| `label_gate_labels` | object | omitted | No | Optional labels to apply when a GitHub-backed vessel enters or exits a label-gate wait. Supported for `github` and `github-pr`. |
| `on` | object | omitted | Required for `github-pr-events` | Event triggers for pull-request event scanning. Must include at least one trigger. |

### Task fields by source type

- `github`: requires `labels`, supports `status_labels`
- `github-pr`: requires `labels`, supports `status_labels`
- `github` and `github-pr` also support `label_gate_labels`
- `github-pr-events`: requires `workflow` and `on`
- `github-merge`: requires `workflow`
- `scheduled`: requires `workflow`; `ref` is optional metadata, and the source also requires `schedule`
- `schedule`: does not use `tasks`; configure `cadence` and `workflow` directly on the source
- `scheduled`: uses `tasks` plus `schedule`; optimized for recurring task-backed hygiene workflows such as `context-weight-audit`

### `schedule`

Scheduled sources create a synthetic vessel when their cadence elapses. Xylem persists the last-fired timestamp under `<state_dir>/state/schedule.json`, so the cadence survives daemon restarts and repeated `scan` invocations.

```yaml
sources:
  doctor:
    type: schedule
    cadence: "6h"
    workflow: doctor

  doc-gardener:
    type: schedule
    cadence: "@daily"
    workflow: doc-garden

  lessons:
    type: schedule
    cadence: "@daily"
    workflow: lessons
```

Behavior:

- The first scan fires immediately.
- Later scans enqueue only when the cadence boundary has elapsed since the last successful enqueue.
- Scheduled sources do not use `tasks`.
- Vessel metadata includes `schedule.cadence`, `schedule.fired_at`, and the configured source name.

### `scheduled`

`scheduled` sources create recurring task-backed vessels on a fixed cadence. Xylem persists the last-enqueued schedule bucket under `<state_dir>/schedules/` so repeated scans in the same window do not duplicate work, and the computed vessel ref also dedupes against already-queued work.

```yaml
sources:
  sota-gap:
    type: scheduled
    repo: owner/repo
    schedule: "@weekly"
    tasks:
      weekly-self-gap-analysis:
        workflow: sota-gap-analysis
        ref: sota-gap-analysis
```

The built-in `context-weight-audit` workflow is another `scheduled` use case: it reads persisted run summaries from `<state_dir>/phases/`, writes `context-weight-audit.{json,md}` under `<state_dir>/<harness.review.output_dir>/`, and opens de-duplicated GitHub hygiene issues for repeated high-footprint findings.

`harness-gap-analysis` is a sibling built-in scheduled workflow for xylem self-hosting. It reads existing daemon telemetry (for example `<state_dir>/daemon.log`), current GitHub state, and git drift, then writes `harness-gap-analysis.{json,md}` plus a durable issue-dedup state file under `<state_dir>/<harness.review.output_dir>/`.

```yaml
sources:
  harness-gap-analysis:
    type: scheduled
    repo: nicholls-inc/xylem
    schedule: "4h"
    tasks:
      analyze-gaps:
        workflow: harness-gap-analysis
        ref: harness-gap-analysis
```

To opt out, omit or delete the scheduled `harness-gap-analysis` source from your config; no separate feature flag is required.
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

### `label_gate_labels`

When `label_gate_labels` is set, xylem records the configured labels in vessel metadata and applies them from deterministic runner code when a label gate blocks or resumes a GitHub-backed vessel.

```yaml
tasks:
  fix-bugs:
    labels: [bug, ready-for-work]
    workflow: fix-bug
    label_gate_labels:
      waiting: blocked
      ready: ready-for-implementation
```

Behavior:

- `waiting` is added when the runner transitions a vessel into `waiting` for a label gate
- `ready` replaces `waiting` when the gate passes and the vessel returns to `pending`
- `ready` is removed again when the resumed vessel starts running, and both labels are cleaned up on terminal exits like `failed`, `timed_out`, and `completed`
- If `label_gate_labels` is omitted entirely, the runner performs no extra label edits for label-gate waits
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
| `daemon.auto_upgrade` | bool | `false` | No | When true, periodically syncs the daemon's current worktree to `origin/main`, rebuilds the binary, and `exec()`s into the new binary if it changed. |
| `daemon.upgrade_interval` | string | `"5m"` | No | How often to rerun the auto-upgrade check while the daemon is running. Must be a valid Go duration string. |

When `daemon.auto_upgrade` is enabled, start the daemon from the **root of a dedicated git worktree on branch `main`**. Xylem upgrades the current working tree in place before rebuilding, so the worktree's `.xylem/workflows/` and prompt files become the authoritative control-plane inputs for the upgraded daemon.

### Harness settings

The `harness` section configures agent safety guardrails: protected file surfaces, policy rules, and audit logging.

When `harness.policy.rules` is empty, xylem installs a default policy that denies writes to protected control surfaces and otherwise allows the actions the runner currently classifies, so autonomous drains can finish without a built-in approval pause. Today that boundary is narrow: every phase is classified as `phase_execute` or `external_command`, and the runner may additionally emit `git_commit`, `git_push`, and `pr_create` when it detects those publication steps in rendered prompts or commands. The same `harness.protected_surfaces.paths` list also drives the worktree's read-only hardening and the runner's post-phase surface verification.

| Action class | Current runner classification | Default effect | Notes |
|--------------|-------------------------------|----------------|-------|
| Protected-surface writes | `file_write` on matched path | `deny` | Prevents agents from mutating xylem control files. |
| Git commit | `git_commit` | `allow` | Commit creation is classified separately but kept autonomous by default. |
| Git push | `git_push` | `allow` | Publication is allowed by default so daemon runs can complete end-to-end. |
| Pull request creation | `pr_create` | `allow` | PR creation stays autonomous unless the operator adds a stricter rule or workflow gate. |
| Destructive git operations (`reset --hard`, branch deletion, force-push) | No separate action today; `git push --force` still collapses to `git_push`, other cases remain `phase_execute`/`external_command` | No separate default effect | If you need review here today, gate the phase or add stricter rules around the enclosing action class. |
| Deploy or production-impacting actions | No separate action today; they remain `phase_execute`/`external_command` | No separate default effect | Add an explicit workflow gate or policy rule for the deploy phase until xylem grows deploy-specific classification. |

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `harness.protected_surfaces.paths` | list of strings | `[".xylem/HARNESS.md", ".xylem.yml", ".xylem/workflows/*.yaml", ".xylem/prompts/*/*.md"]` | No | Glob patterns for files agents cannot modify. Set to `["none"]` to disable all surface protections. |
| `harness.policy.rules` | list of objects | `[]` | No | Policy rules for action authorization. Each rule has `action`, `resource`, and `effect`. |
| `harness.audit_log` | string | `"audit.jsonl"` | No | Path to the audit log file for policy decisions, relative to the state directory. |
| `harness.review.enabled` | bool | `false` | No | Enables recurring harness review generation after drain runs. Manual `xylem review` works regardless. |
| `harness.review.cadence` | string | `"manual"` | No | Automatic review cadence. Valid values: `manual`, `every_drain`, `every_n_runs`. |
| `harness.review.every_n_runs` | integer | `10` | No | When cadence is `every_n_runs`, regenerate after this many new reviewed runs. |
| `harness.review.lookback_runs` | integer | `50` | No | Maximum number of historical run summaries to include in each review. |
| `harness.review.min_samples` | integer | `3` | No | Minimum samples required before xylem will recommend keeping or pruning a surface. |
| `harness.review.output_dir` | string | `"reviews"` | No | Output directory for review reports, relative to the state directory. |

**Policy rule fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `action` | string | Yes | The action to match (e.g., `file_write`, `git_push`, `pr_create`, `*`). |
| `resource` | string | Yes | The resource to match (e.g., `.xylem.yml`, `*`). Supports glob patterns. |
| `effect` | string | Yes | The effect when matched. Valid values: `allow`, `deny`, `require_approval`. |

The example below opts into human review before publication:

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
  review:
    enabled: true
    cadence: every_n_runs
    every_n_runs: 10
    lookback_runs: 50
    min_samples: 3
    output_dir: "reviews"
```

`xylem review` writes `harness-review.json` and `harness-review.md` under `<state_dir>/<output_dir>/`. Automatic reviews are best-effort: failed review generation never fails `drain` or `daemon`. Built-in context-weight audits also write `context-weight-audit.json`, `context-weight-audit.md`, and a durable issue-dedup state file in the same directory when a scheduled `context-weight-audit` vessel runs. Built-in `harness-gap-analysis` runs do the same for `harness-gap-analysis.{json,md}` while surfacing recurring daemon-operation gaps such as drift, idle backlog episodes, stale conflict labels, and parked failed backlog. When failed or timed-out runs also have `<state_dir>/phases/<vessel-id>/failure-review.json`, the review loader reconstructs those recovery decisions alongside the existing evidence/cost/eval artifacts.

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

### Schedule recurring vessels

```yaml
sources:
  lessons:
    type: schedule
    cadence: "@daily"
    workflow: lessons
```

`schedule` sources persist their last-fired state under `<state_dir>/schedule-state.json`. The generated vessel metadata includes:

- `schedule_name`
- `schedule_cadence`
- `schedule_fired_at`
- `schedule_next_due_at`

The built-in `lessons` workflow is designed for this source type: it synthesizes recurring failures into `.xylem/HARNESS.md` proposals, records them under `<state_dir>/reviews/lessons.{json,md}`, and opens reviewable PRs instead of editing the default branch directly. When recovery artifacts are present, the lessons report also carries forward the persisted `recovery_class`, `recovery_action`, and `follow_up_route` fields so operators can inspect why a failure clustered the way it did.

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
| `scheduled` sources must have a `repo` in `owner/name` format | `source "<name>" (scheduled): repo must be in owner/name format` |
| `scheduled` sources must declare a valid cadence | `source "<name>" (scheduled): schedule is invalid: ...` |
| `github`, `github-pr`, `github-pr-events`, `github-merge`, and `scheduled` sources must have at least one task | `source "<name>" ...: at least one task is required` |
| `github` and `github-pr` tasks must have at least one label | `source "<name>" task "<task>": must include at least one labels entry` |
| `scheduled` tasks must include a `ref` | `source "<name>" task "<task>": ref is required for scheduled tasks` |
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
