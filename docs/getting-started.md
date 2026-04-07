# Getting Started with xylem

This guide walks you through installing xylem, configuring it for your repository, and running your first autonomous sessions. By the end, you will have xylem scanning GitHub work sources and launching multi-phase workflows in isolated git worktrees.

## Prerequisites

You need the following tools installed and available on your PATH:

| Tool | Version | Purpose |
|------|---------|---------|
| [Go](https://go.dev/dl/) | 1.22+ | Build the xylem CLI |
| [git](https://git-scm.com/) | any recent | Worktree creation and branch management |
| [claude](https://docs.anthropic.com/en/docs/claude-code) or GitHub Copilot CLI | latest | Session runner for LLM phases |
| [gh](https://cli.github.com/) | any recent | GitHub CLI, used by GitHub-based sources and PR creation |

The `gh` CLI must be authenticated before xylem can scan GitHub sources:

```bash
gh auth login
```

If you use Claude with `--bare`, you also need an Anthropic API key. Set it in your environment or pass it through the xylem config.

## Installation

Install the xylem Go CLI binary:

```bash
go install github.com/nicholls-inc/xylem/cli/cmd/xylem@latest
```

You also need at least one supported LLM CLI on your PATH:

- **Claude Code**: Install from [docs.anthropic.com](https://docs.anthropic.com/en/docs/claude-code)
- **GitHub Copilot CLI**: Install separately and ensure `copilot` is on your PATH

Verify the installation:

```bash
xylem --help
```

## Initialize your project

Navigate to the root of the repository you want xylem to manage, then run `init`:

```bash
cd /path/to/your/repo
xylem init
```

This creates the following files and directories:

```
.xylem.yml                                  # Main config file
.xylem/                                     # State directory (queue, outputs, workflows)
  HARNESS.md                                # Project context for launched sessions
  workflows/
    fix-bug.yaml                            # Bug-fixing workflow definition
    implement-feature.yaml                  # Feature implementation workflow definition
  prompts/
    fix-bug/
      analyze.md, plan.md, implement.md, pr.md
    implement-feature/
      analyze.md, plan.md, implement.md, pr.md
```

The `init` command auto-detects your GitHub remote and pre-fills the `repo` field in `.xylem.yml`. If the remote is not a GitHub URL, it defaults to `owner/name` as a placeholder.

**Tip**: The `.xylem/` directory contains a `.gitignore` that excludes everything except itself. Runtime state (the queue, phase outputs, worktrees) stays out of version control. You should commit `.xylem.yml`, `.xylem/HARNESS.md`, the workflow YAML files, and the prompt templates.

## Edit the config

Open `.xylem.yml` in your editor. Here is the scaffolded config with annotations:

```yaml
sources:
  bugs:
    type: github
    repo: owner/name             # Your GitHub repo — auto-detected by init
    exclude: [wontfix, duplicate, in-progress, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, ready-for-work]       # Issues with BOTH labels are picked up
        workflow: fix-bug                    # Maps to .xylem/workflows/fix-bug.yaml

concurrency: 2          # Max simultaneous sessions
max_turns: 50           # Max turns per session
timeout: "30m"          # Per-session timeout

state_dir: ".xylem"     # Where queue and state files live

llm: claude             # Global default provider: claude or copilot

claude:
  command: "claude"
  flags: "--bare --dangerously-skip-permissions"
  env:
    ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"

# copilot:
#   command: "copilot"
#   flags: ""
#   default_model: ""
#   env: {}
```

### What to change

1. **`repo`** -- Replace `owner/name` with your actual GitHub repository (e.g., `myorg/myapp`).
2. **`labels`** -- Match the label names you use in your issue tracker. An issue must have *all* listed labels to be picked up by that task.
3. **`exclude`** -- Labels that prevent an issue from being queued, even if it matches a task's labels.
4. **`workflow`** -- The workflow name to execute. Must match a file in `.xylem/workflows/`.
5. **`llm`** -- Set the default provider to `claude` or `copilot`. If omitted, xylem defaults to `claude`.
6. **`claude.flags`** -- Adjust as needed. Remove `--dangerously-skip-permissions` if you want Claude to ask for confirmation before running commands.
7. **`claude.env`** -- Set your `ANTHROPIC_API_KEY` here if you use Claude with `--bare`.
8. **`copilot`** -- Configure the Copilot CLI command, flags, optional default model, and environment if you use `llm: copilot`.

### Adding a second source

To handle feature requests in addition to bugs, uncomment the `features` source block or add your own:

```yaml
sources:
  bugs:
    type: github
    repo: myorg/myapp
    exclude: [wontfix, duplicate, in-progress, no-bot]
    tasks:
      fix-bugs:
        labels: [bug, ready-for-work]
        workflow: fix-bug
  features:
    type: github
    repo: myorg/myapp
    exclude: [wontfix, duplicate, in-progress, no-bot]
    tasks:
      implement-features:
        labels: [enhancement, low-effort, ready-for-work]
        workflow: implement-feature
```

Each source scans independently. xylem deduplicates across sources so the same issue is never queued twice.

### Other built-in GitHub source types

In addition to issue scanning, xylem supports these GitHub source types:

- `github-pr` — scans open pull requests by label
- `github-pr-events` — scans open pull requests for event triggers
- `github-merge` — scans merged pull requests

For `github-pr-events`, tasks use an `on` block instead of `labels`:

```yaml
sources:
  review-events:
    type: github-pr-events
    repo: myorg/myapp
    exclude: [no-bot]
    tasks:
      respond-to-pr-events:
        workflow: review-followup
        on:
          labels: [needs-agent]
          review_submitted: true
          checks_failed: true
          commented: true
```

At least one trigger is required in the `on` block.

## Write a HARNESS.md

The harness file at `.xylem/HARNESS.md` is appended to the session runner's system prompt for every session xylem launches. It gives the agent the project context it needs to work autonomously: what the project does, how to build and test it, and what rules to follow.

Open `.xylem/HARNESS.md` and fill in each section:

```markdown
# Project Overview
<!-- What does this project do? One paragraph. -->

# Architecture
<!-- Describe the codebase structure: key directories, main entry points,
     how the pieces fit together. -->

# Build & Test
<!-- List the exact commands to build, test, and lint. Be specific:
     cd api && go test ./...
     npm run lint
      These commands are what the session runner will run. -->

# Golden Principles
<!-- Rules the agent must always follow. Examples:
     - Always run go vet before committing
     - Never modify generated files under pkg/gen/
     - All new endpoints require integration tests -->

# Dependencies
<!-- External services or tools needed at runtime:
     - PostgreSQL 15+ on localhost:5432
     - Redis for session storage
     - gh CLI for PR creation -->
```

A well-written harness is the single biggest factor in session quality. Be specific. Include exact commands, file paths, and constraints. If you would tell a new team member "never do X," put it in the harness.

## Preview work with a dry-run scan

Before letting xylem modify anything, preview what it would queue:

```bash
xylem scan --dry-run
```

This queries your configured GitHub sources and prints the matching issues without writing to the queue:

```
ID              Source          Workflow              Ref
----            ------          -----                 ---
issue-42        bugs            fix-bug               https://github.com/myorg/myapp/issues/42
issue-55        bugs            fix-bug               https://github.com/myorg/myapp/issues/55

2 candidate(s) would be queued (dry-run -- no changes made)
```

If you see `No new issues found`, check that your label configuration matches actual labels on open issues in your repository.

## Scan and drain

Once you are satisfied with the dry-run output, scan for real and then drain the queue:

```bash
# Enqueue matching issues
xylem scan

# Launch sessions for pending vessels
xylem drain
```

Or combine them:

```bash
xylem scan && xylem drain
```

Here is what happens during drain:

1. xylem dequeues a pending vessel.
2. It creates an isolated git worktree from your default branch.
3. It executes the workflow phases sequentially (e.g., analyze, plan, implement, pr).
4. Between phases, quality gates run (shell commands or label checks).
5. If a command gate fails, the phase retries up to the configured limit.
6. Phase outputs are persisted to `.xylem/phases/<vessel-id>/`.

Drain respects the `concurrency` setting. If you set `concurrency: 2`, at most two sessions run in parallel.

Drain handles SIGINT and SIGTERM gracefully: running sessions finish, but no new vessels are started.

### Continuous mode with daemon

Instead of running scan and drain manually, you can start a daemon that runs both on a loop:

```bash
xylem daemon
```

The daemon scans at `scan_interval` (default: 60s) and drains at `drain_interval` (default: 30s). Configure these in `.xylem.yml`:

```yaml
daemon:
  scan_interval: "2m"
  drain_interval: "30s"
```

## Check status

View the current state of the queue:

```bash
xylem status
```

```
ID              Source          Workflow              State       Info                            Started       Duration
----            ------          -----                 -----       ----                            -------       --------
issue-42        bugs            fix-bug               completed                                   10:30 UTC     12m
issue-55        bugs            fix-bug               running                                     10:45 UTC     3m

Summary: 0 pending, 1 running, 1 completed, 0 failed, 0 cancelled, 0 waiting, 0 timed_out
```

Filter by state:

```bash
xylem status --state failed
```

Get machine-readable output:

```bash
xylem status --json
```

## Manual enqueue for ad-hoc tasks

You do not need GitHub issues for every task. Use `enqueue` to queue work directly.

### Enqueue with a workflow and issue reference

```bash
xylem enqueue --workflow fix-bug --ref "https://github.com/myorg/myapp/issues/99"
```

This runs the full `fix-bug` workflow, with the issue URL passed to prompt templates via `{{.Issue.URL}}`.

### Enqueue with a direct prompt

```bash
xylem enqueue --prompt "Refactor the auth middleware to use JWT instead of session cookies"
```

Direct prompts bypass workflow phases entirely. The prompt is passed straight to a single prompt-only session using the resolved provider.

### Enqueue from a file

```bash
xylem enqueue --prompt-file task.md --workflow implement-feature
```

### Other enqueue options

```bash
# Custom vessel ID (instead of auto-generated)
xylem enqueue --workflow fix-bug --ref "#42" --id "hotfix-42"

# Custom source tag
xylem enqueue --workflow fix-bug --ref "#42" --source "jira"
```

At least one of `--workflow` or `--prompt`/`--prompt-file` is required. When both `--workflow` and `--prompt` are provided, the workflow phases run with the prompt as additional context.

## Next steps

You now have a working xylem setup. Here are the areas to explore next:

- **[Configuration reference](configuration.md)** -- Full reference for every `.xylem.yml` field, including legacy format migration, provider settings, and daemon tuning.
- **[Workflows](workflows.md)** -- How to create custom workflows, write prompt templates using Go template variables, and configure command and label gates.
- **[CLI reference](cli-reference.md)** -- Complete documentation for every xylem command: `scan`, `drain`, `daemon`, `enqueue`, `retry`, `status`, `pause`, `resume`, `cancel`, `cleanup`.
- **[Architecture](architecture.md)** -- How the control plane and execution plane work together, the vessel state machine, source interface design, and worktree lifecycle.
