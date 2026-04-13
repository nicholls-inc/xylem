# xylem

You have 40 GitHub issues labeled `ready-for-work`. They've been sitting there for two weeks. You'd fix them, but fixing one means opening Claude Code, writing the prompt, waiting, reviewing, pushing — and you have 39 more after that.

**xylem turns labeled GitHub issues into merged pull requests while you sleep.**

It runs Claude Code autonomously against your backlog: scanning issues, creating isolated worktrees, executing multi-phase workflows, running your test gates, and opening PRs — all without you watching. While you work on what matters, xylem drains the queue.

## What you can achieve

- **Backlog that actually shrinks** — label issues `ready-for-work`, start the daemon, come back to open PRs
- **Overnight bug fixes** — bugs analyzed, implemented, and submitted as PRs before your morning standup
- **Scheduled maintenance, unattended** — daily doc updates, dependency audits, changelog drafts, backlog refinement — run on cron without touching your keyboard
- **Multi-phase quality gates** — each PR passes your own `make test` (or any command) before it surfaces; no merging on blind faith
- **A self-improving codebase** — xylem can file its own issues and process them; the system gets better by running

## Quick start

```bash
# Install
go install github.com/nicholls-inc/xylem/cli/cmd/xylem@latest

# Bootstrap config and state directory in your repo
xylem init

# Edit .xylem.yml with your repo and source config
# Edit .xylem/HARNESS.md with your project-specific instructions

# Preview what would be queued without doing anything
xylem scan --dry-run

# Start the continuous daemon
xylem daemon
```

Label a GitHub issue `ready-for-work`. xylem picks it up within the next scan interval, creates a worktree, runs your configured workflow, and opens a PR. See the [Getting Started guide](docs/getting-started.md) for the full walkthrough.

## What xylem is not

- **Not a general agent framework** — xylem orchestrates Claude Code CLI sessions against a work queue; it is not a library for building agents
- **Not a replacement for Claude Code** — if you have one issue to fix, open Claude Code directly; xylem is for when you have a backlog
- **Not a chatbot or REPL** — there is no interactive session; xylem runs unattended
- **Not a GitHub Actions replacement** — xylem runs locally or on a server you control, talking to GitHub via `gh`; it does not run inside Actions workflows

## How it works

xylem is a two-layer system:

- **Go CLI** (`xylem`) — control plane that scans sources for actionable tasks, manages a persistent work queue, and launches isolated session runs in git worktrees
- **Workflows** — execution plane with multi-phase workflow definitions (e.g. `fix-bug`, `implement-feature`) that run inside each session with quality gates between phases

```
Sources              xylem CLI              Execution
┌────────────┐       ┌──────────┐           ┌──────────────────────────┐
│ GitHub     │─scan─>│  Queue   │──drain──> │ Worktree + Workflow      │
│ Manual     │       │ (JSONL)  │           │  analyze → plan →        │
└────────────┘       └──────────┘           │  implement → pr          │
                                            │     |            |       │
                                            │ label/cmd/live gates     │
                                            └──────────────────────────┘
```

Sources are pluggable. Built-in source types cover GitHub issues (`github`), pull requests (`github-pr`), pull-request events (`github-pr-events`), merged pull requests (`github-merge`), recurring scheduled workflows (`schedule`), and manual tasks via `xylem enqueue`. xylem handles scheduling, deduplication, concurrency, and worktree isolation across all sources.

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
  doc-gardener:
    type: schedule
    cadence: "@daily"
    workflow: doc-garden

concurrency:
  global: 2
  per_class:
    implement-feature: 1
    merge-pr: 2
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

This example covers the most common fields. `concurrency` still accepts the legacy scalar form (`concurrency: 2`); the mapping form adds a global ceiling plus optional per-workflow limits keyed by workflow name. Scheduled sources fire a synthetic vessel whenever their cadence elapses; `cadence` accepts Go durations like `1h`, cron descriptors like `@daily`, and standard cron expressions. Additional configuration is available for per-source provider overrides (`llm`, `model`), agent safety guardrails (`harness`), recurring harness reviews (`harness.review`), OpenTelemetry tracing (`observability`), and token budget enforcement (`cost`). See the [Configuration Reference](docs/configuration.md) for all fields, defaults, and validation rules.

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
| `live` | Verifies the running system via HTTP, browser, or command+assert checks and saves evidence artifacts |

```yaml
gate:
  type: live
  retries: 1
  live:
    mode: http
    http:
      base_url: "http://127.0.0.1:3000"
      steps:
        - name: health
          url: /health
          expect_status: 200
          expect_json:
            - path: $.status
              equals: ok
```

Use `noop.match` on a phase to let that phase complete the workflow early when its output contains a configured marker such as `XYLEM_NOOP`.

**Built-in workflows:**

- **fix-bug** — Analyze, Plan, Implement (with test gate), PR
- **implement-feature** — Analyze, Plan (with label gate for approval), Implement (with test gate), PR
- **doc-garden** — Daily documentation drift scan, targeted doc updates, verification, and PR drafting
- **security-compliance** — Daily scheduled secrets, static-analysis, dependency-audit, and synthesis pass with issue filing for actionable risk

See the [Workflows Guide](docs/workflows.md) for template variables, custom workflows, and prompt authoring tips.

## Commands

| Command | Description |
|---------|-------------|
| `xylem init` | Bootstrap config, workflows, prompts, and HARNESS.md |
| `xylem scan` | Query sources and enqueue matching issues |
| `xylem drain` | Dequeue pending vessels and launch sessions |
| `xylem review` | Roll up failures, evals, and pruning signals into a harness review |
| `xylem daemon` | Continuous scan-drain loop |
| `xylem daemon-supervisor` | Restart the daemon after unexpected exits |
| `xylem daemon stop` | Stop the daemon and suppress supervisor restarts |
| `xylem enqueue` | Manually enqueue a task |
| `xylem retry` | Retry a failed vessel with failure context, or restart from scratch |
| `xylem recovery refresh` | Refresh a suppressed failure-review decision so retries are allowed again |
| `xylem status` | Show queue state and vessel summary |
| `xylem pause` / `resume` | Pause and resume scanning |
| `xylem cancel` | Cancel a pending vessel |
| `xylem cleanup` | Remove stale worktrees, old phase outputs, and compact stale queue records |
| `xylem dtu ...` | Initialize a DTU manifest, materialize runtime state, and run xylem under DTU shims |

```bash
# Common patterns
xylem scan && xylem drain           # One-shot scan and process
xylem daemon                        # Continuous operation
xylem daemon-supervisor             # Auto-restart wrapper around xylem daemon
xylem daemon stop                   # Stop the daemon without triggering a restart
xylem enqueue --workflow fix-bug \
  --ref "https://github.com/owner/repo/issues/99"  # Ad-hoc task
xylem recovery refresh issue-158-fresh-retry-1     # Re-enable retry after reviewing a suppressed failure-review
xylem status --json | jq '.[] | select(.state == "failed")'  # Query failures
xylem dtu load --manifest cli/internal/dtu/testdata/issue-label-gate.yaml        # Seed DTU state from the repo's example fixture
xylem dtu materialize --manifest cli/internal/dtu/testdata/issue-label-gate.yaml # Prepare DTU runtime and shims
xylem dtu run --manifest /path/to/universe.yaml --workdir "$PWD" -- scan         # Run xylem inside DTU from the current repo
```

`xylem review` writes `.xylem/reviews/harness-review.json` and `.xylem/reviews/harness-review.md` by default. Enable recurring generation after drains with `harness.review` in `.xylem.yml`.

See the [CLI Reference](docs/cli-reference.md) for all flags, examples, and exit codes.

## Automation

**Daemon mode** (recommended):

```bash
xylem daemon-supervisor
# Loads .daemon-root/.env on each restart, waits 10s after unexpected exits,
# and hands off clean shutdowns to `xylem daemon stop`.
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

- **Go 1.22+** — to build the CLI
- **git** — must be on PATH
- **[claude](https://docs.anthropic.com/en/docs/claude-code)** or **GitHub Copilot CLI** — at least one supported LLM CLI
- **[gh](https://cli.github.com/)** — GitHub CLI, authenticated (`gh auth login`). Required for GitHub-based sources and PR creation.

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

- [Getting Started](docs/getting-started.md) — installation, first run, and setup walkthrough
- [Configuration Reference](docs/configuration.md) — all `.xylem.yml` fields, defaults, and validation
- [Workflows Guide](docs/workflows.md) — phases, gates, prompt templates, and custom workflows
- [CLI Reference](docs/cli-reference.md) — every command with flags, examples, and exit codes
- [DTU Guide 4A: Fixture Regression Tests](docs/dtu-fixture-regression-tests.md) — author deterministic DTU-backed Go regression tests and understand their trust boundary
- [DTU Guide 4B: Manual Smoke Tests](docs/dtu-manual-smoke-tests.md) — run the real xylem CLI under DTU shims and understand what those smoke tests do and do not prove
- [Architecture](docs/architecture.md) — system design, data flow, and package map

## Known limitations

- **Sequential correctness only** — no concurrency modeling in the workflows themselves
- **GitHub only** — built-in scanning sources target GitHub issues, PRs, PR events, and merged PRs; other integrations require manual enqueue
- **Cancel does not kill sessions** — only removes pending vessels; running sessions run to completion
- **No priority queues** — FIFO order only
- **No webhooks** — polling only (cron or daemon mode)
