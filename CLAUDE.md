# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and test commands

All Go code lives under `cli/`. Run commands from that directory:

```bash
cd cli

# Build
go build ./cmd/xylem

# Run all tests
go test ./...

# Run a single package's tests
go test ./internal/queue

# Run a specific test
go test ./internal/queue -run TestDequeue

# Run property-based tests (uses pgregory.net/rapid)
go test ./internal/memory -run TestProp

# Run tests with race detector
go test -race ./...

# Check formatting (mirrors CI)
goimports -l .

# Fix formatting
goimports -w .
```

There is no Makefile or linter config. CI runs `goimports` (format check), `go vet`, `go build`, and `go test`.

## Architecture

xylem is a two-layer system: a Go CLI (control plane) and YAML workflow definitions (execution plane).

### Control plane (`cli/`)

The CLI schedules and runs autonomous Claude Code sessions. Core flow:

1. **scan** — `scanner` queries each configured `source` (GitHub issues by label, or manual enqueue) and writes `Vessel` records to a JSONL queue
2. **drain** — `runner` dequeues pending vessels, creates isolated git worktrees via `worktree`, then executes workflow phases sequentially in each worktree
3. **daemon** — continuous scan-drain loop combining both

Key types:
- **Vessel** (`queue` package) — a unit of work with state machine: `pending → running → completed/failed/waiting/timed_out/cancelled`
- **Source** (`source` package) — interface with `Scan()`, `OnStart()`, `BranchName()`. Implementations: `GitHub`, `Manual`
- **Workflow** (`workflow` package) — YAML-defined multi-phase execution plan loaded from `.xylem/workflows/`
- **Gate** (`gate` package) — inter-phase quality checks: `command` (run shell command) or `label` (poll GitHub for label, vessel enters `waiting` state)

The `runner` drives phase execution: renders Go templates into prompts, pipes them to `claude -p` via stdin, persists outputs to `.xylem/phases/<id>/`, and handles gate retries and label-wait suspension.

### Agent harness library (`cli/internal/`)

Standalone packages for building agent orchestration systems. Not wired into the CLI commands but designed as composable building blocks:

- `orchestrator` — multi-agent topologies (sequential, parallel, orchestrator-workers, handoff) with context firewalls between sub-agents
- `mission` — task decomposition, complexity analysis, sprint contracts, persona constraints
- `memory` — typed memory (procedural/semantic/episodic), KV store, scratchpads, handoff artifacts
- `signal` — behavioral heuristics (repetition, tool failure rate, context thrash, efficiency, task stall)
- `evaluator` — generator-evaluator loops with signal-gated intensity
- `cost` — token tracking by agent role, budget enforcement, model ladders
- `ctxmgr` — context window management with compaction strategies
- `intermediary` — intent validation with policy rules, audit logging, glob-based permissions
- `bootstrap` — repo analysis and AGENTS.md generation
- `catalog` — tool catalog with overlap detection and permission scopes
- `observability` — OpenTelemetry span attributes for missions, agents, and signals

### Execution plane (`workflows/`)

Contains `WORKFLOW.md` files documenting the built-in workflows (`fix-bug`, `implement-feature`). Actual workflow YAML and prompt templates are scaffolded into the target repo's `.xylem/` directory by `xylem init`.

### Config (`cli/internal/config`)

Loads `.xylem.yml`. Supports a legacy flat format (top-level `repo`/`tasks`/`exclude`) that `normalize()` auto-migrates to the multi-source format. `claude.template` and `claude.allowed_tools` at the top level are rejected with migration guidance.

## Testing patterns

- Tests use interfaces and stubs extensively (e.g., `CommandRunner`, `WorktreeManager`) — no real subprocesses or git operations in tests
- Property-based tests use `pgregory.net/rapid` and follow the naming convention `TestProp*` in `*_prop_test.go` files
- Queue tests use temp directories for JSONL files; worktree tests use temp directories for git repos
- The `source` package has no tests (relies on integration testing via `scanner` and `runner`)

## Terminology

- **Vessel** — a queued unit of work (analogous to a job/task)
- **Workflow** — a multi-phase execution definition loaded from YAML (recently renamed from "skill")
- **Phase** — one step within a workflow, executed as a single Claude session
- **Gate** — a quality check between phases (command or label)
- **Harness** — project-specific instructions appended to Claude's system prompt (`.xylem/HARNESS.md`)
- **Drain** — process of dequeuing and executing pending vessels
