# xylem

Autonomous Claude Code and GitHub Copilot session scheduler. Scans pluggable sources, queues work, and launches multi-phase workflows in isolated git worktrees with quality gates.

## How it works

xylem is a two-layer system:

- **Go CLI** (`xylem`) -- control plane that scans sources for actionable tasks, manages a persistent work queue, and launches isolated session runs in git worktrees
- **Workflows** -- execution plane with multi-phase workflow definitions (e.g. `fix-bug`, `implement-feature`) that run inside each session with quality gates between phases

```
Sources              xylem CLI              Execution
┌────────────┐       ┌──────────┐           ┌──────────────────────────┐
│ GitHub     │─scan─>│  Queue   │──drain──> │ Worktree + Workflow      │
│ Manual     │       │ (JSONL)  │           │  analyze → plan →        │
└────────────┘       └──────────┘           │  implement → pr          │
                                            │     |            |       │
                                            │  label gate  cmd gate    │
                                            └──────────────────────────┘
```

Sources are pluggable. Built-in source types cover GitHub issues (`github`), pull requests (`github-pr`), pull-request events (`github-pr-events`), merged pull requests (`github-merge`), and manual tasks via `xylem enqueue`. xylem handles scheduling, deduplication, concurrency, and worktree isolation across all sources.

## Quick start

```bash
# Install
go install github.com/nicholls-inc/xylem/cli/cmd/xylem@latest

# Bootstrap config and state directory
xylem init

# Edit .xylem.yml with your repo and task config
# Edit .xylem/HARNESS.md with your project details

# Preview what would be queued
xylem scan --dry-run

# Scan for work and process it
xylem scan && xylem drain
```

See the [Getting Started guide](docs/getting-started.md) for a full walkthrough.

## Configuration

Create `.xylem.yml` in your repository root, or run `xylem init` to generate one:

```yaml
sources:
  bugs:
    type: github
    repo: owner/name
    exclude: [wontfix, duplicate, in-progress, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, ready-for-work]
        workflow: fix-bug
  features:
    type: github
    repo: owner/name
    exclude: [wontfix, duplicate, in-progress, no-bot]
    tasks:
      implement-features:
        labels: [enhancement, low-effort, ready-for-work]
        workflow: implement-feature

concurrency: 2
max_turns: 50
timeout: "30m"
state_dir: ".xylem"

llm: claude

claude:
  command: "claude"
  flags: "--bare --dangerously-skip-permissions"
  env:
    ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"

copilot:
  command: "copilot"
  flags: ""

daemon:
  scan_interval: "60s"
  drain_interval: "30s"
```

This example covers the most common fields. Additional configuration is available for per-source provider overrides (`llm`, `model`), agent safety guardrails (`harness`), OpenTelemetry tracing (`observability`), and token budget enforcement (`cost`). See the [Configuration Reference](docs/configuration.md) for all fields, defaults, and validation rules.

Each completed, failed, or approval-paused vessel now writes a canonical runtime index at `.xylem/phases/<vessel-id>/runtime.json`. That file links the compact `summary.json` to detailed per-vessel artifacts such as `cost-report.json`, `budget-events.json`, `audit-events.json`, `trace.json`, and `evidence-manifest.json` when present.

## Workflows

Workflows are multi-phase execution plans defined in YAML. Prompt phases run against the configured LLM provider, and phases can have quality gates that must pass before the next phase begins.

```yaml
# .xylem/workflows/fix-bug.yaml
name: fix-bug
description: "Diagnose and fix a bug from a GitHub issue"
phases:
  - name: analyze
    prompt_file: .xylem/prompts/fix-bug/analyze.md
    max_turns: 5
    noop:
      match: XYLEM_NOOP
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

**Gate types:**

| Type | Description |
|------|-------------|
| `command` | Runs a shell command; phase retries if it fails |
| `label` | Polls a GitHub issue for a label; blocks until found (human-in-the-loop approval) |

Use `noop.match` on a phase to let that phase complete the workflow early when its output contains a configured marker such as `XYLEM_NOOP`.

**Built-in workflows:**

- **fix-bug** -- Analyze, Plan, Implement (with test gate), PR
- **implement-feature** -- Analyze, Plan (with label gate for approval), Implement (with test gate), PR

See the [Workflows Guide](docs/workflows.md) for template variables, custom workflows, and prompt authoring tips.

## Commands

| Command | Description |
|---------|-------------|
| `xylem init` | Bootstrap config, workflows, prompts, and HARNESS.md |
| `xylem scan` | Query sources and enqueue matching issues |
| `xylem drain` | Dequeue pending vessels and launch sessions |
| `xylem daemon` | Continuous scan-drain loop |
| `xylem enqueue` | Manually enqueue a task |
| `xylem retry` | Retry a failed vessel with failure context, or restart from scratch |
| `xylem status` | Show queue state and vessel summary |
| `xylem pause` / `resume` | Pause and resume scanning |
| `xylem cancel` | Cancel a pending vessel |
| `xylem cleanup` | Remove stale worktrees, old phase outputs, and compact stale queue records |
| `xylem dtu ...` | Initialize a DTU manifest, materialize runtime state, and run xylem under DTU shims |

```bash
# Common patterns
xylem scan && xylem drain           # One-shot scan and process
xylem daemon                        # Continuous operation
xylem enqueue --workflow fix-bug \
  --ref "https://github.com/owner/repo/issues/99"  # Ad-hoc task
xylem status --json | jq '.[] | select(.state == "failed")'  # Query failures
xylem dtu load --manifest cli/internal/dtu/testdata/issue-label-gate.yaml        # Seed DTU state from the repo's example fixture
xylem dtu materialize --manifest cli/internal/dtu/testdata/issue-label-gate.yaml # Prepare DTU runtime and shims
xylem dtu run --manifest /path/to/universe.yaml --workdir "$PWD" -- scan         # Run xylem inside DTU from the current repo
```

See the [CLI Reference](docs/cli-reference.md) for all flags, examples, and exit codes.

## Automation

**Daemon mode** (recommended):

```bash
xylem daemon
# Or as a background service with systemd, launchd, etc.
```

**Cron**:

```cron
*/15 * * * * cd /path/to/repo && xylem scan && xylem drain >> /tmp/xylem.log 2>&1
```

## Agent harness library

The CLI includes standalone packages under `cli/internal/` implementing foundational agent harness components. These are composable building blocks for mission-scoped agent orchestration:

| Package | Purpose |
|---------|---------|
| `orchestrator` | Multi-agent topologies (sequential, parallel, orchestrator-workers, handoff) |
| `mission` | Complexity analysis, task decomposition, sprint contracts |
| `memory` | Typed memory (procedural/semantic/episodic), KV store, scratchpads |
| `signal` | Behavioral heuristics (repetition, tool failure rate, context thrash) |
| `evaluator` | Generator-evaluator loops with signal-gated intensity |
| `cost` | Token tracking by agent role, budget enforcement, model ladders |
| `ctxmgr` | Context window management with compaction strategies |
| `intermediary` | Intent validation with policy rules, audit logging, permissions |
| `bootstrap` | Repo analysis and AGENTS.md generation |
| `catalog` | Tool catalog with overlap detection and permission scopes |
| `observability` | OpenTelemetry span attributes for missions, agents, and signals |
| `evidence` | Verification claims and evidence manifests with typed assurance levels |
| `surface` | SHA256 surface integrity for protected files |

See the [Architecture](docs/architecture.md) document for details on the control flow, isolation model, and how these packages fit together.

## Prerequisites

- **Go 1.22+** -- to build the CLI
- **git** -- must be on PATH
- **[claude](https://docs.anthropic.com/en/docs/claude-code)** or **GitHub Copilot CLI** -- at least one supported LLM CLI
- **[gh](https://cli.github.com/)** -- GitHub CLI, authenticated (`gh auth login`). Required for GitHub-based sources and PR creation.

## Installation

```bash
# Install the Go CLI
go install github.com/nicholls-inc/xylem/cli/cmd/xylem@latest
```

## Development checks

Optional local pre-commit hooks catch formatting, lint, and build problems before you push without running the test suite.

```bash
pre-commit install
pre-commit run --all-files
```

The configured hooks run `goimports`, `golangci-lint`, and `go build ./cmd/xylem`. `go test` is intentionally excluded from pre-commit checks.

The formatting and lint hooks use the system `goimports` and `golangci-lint` binaries, so install them separately and ensure they are on your `PATH`.

## Documentation

- [Getting Started](docs/getting-started.md) -- installation, first run, and setup walkthrough
- [Configuration Reference](docs/configuration.md) -- all `.xylem.yml` fields, defaults, and validation
- [Workflows Guide](docs/workflows.md) -- phases, gates, prompt templates, and custom workflows
- [CLI Reference](docs/cli-reference.md) -- every command with flags, examples, and exit codes
- [DTU Guide 4A: Fixture Regression Tests](docs/dtu-fixture-regression-tests.md) -- author deterministic DTU-backed Go regression tests and understand their trust boundary
- [DTU Guide 4B: Manual Smoke Tests](docs/dtu-manual-smoke-tests.md) -- run the real xylem CLI under DTU shims and understand what those smoke tests do and do not prove
- [Architecture](docs/architecture.md) -- system design, data flow, and package map

## Known limitations

- **Sequential correctness only** -- no concurrency modeling in the workflows themselves
- **GitHub only** -- built-in scanning sources target GitHub issues, PRs, PR events, and merged PRs; other integrations require manual enqueue
- **Cancel does not kill sessions** -- only removes pending vessels; running sessions run to completion
- **No priority queues** -- FIFO order only
- **No webhooks** -- polling only (cron or daemon mode)
