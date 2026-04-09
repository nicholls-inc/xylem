# Project Overview

xylem is a Go CLI that schedules and runs autonomous Claude Code sessions. It scans pluggable sources (GitHub issues by label, manual enqueue), manages a persistent JSONL work queue, and executes multi-phase workflows in isolated git worktrees with quality gates between phases.

# Architecture

All Go source code lives under `cli/`. The rest of the repo is configuration and documentation.

```
cli/
  cmd/xylem/             # CLI entry point (cobra commands)
  internal/
    config/              # .xylem.yml loader with legacy format auto-migration
    queue/               # JSONL-backed persistent work queue (Vessel state machine)
    runner/              # Dequeues vessels, creates worktrees, executes phases
    scanner/             # Queries sources, enqueues Vessel records
    source/              # Source interface + GitHub/Manual implementations
    workflow/            # YAML workflow definition loader and validation
    gate/                # Inter-phase quality checks (command, label)
    phase/               # Prompt template rendering with Go templates
    worktree/            # Git worktree lifecycle management
    reporter/            # Phase result collection and output
    dtu/                 # Digital twin under-test: scenario scheduler, verification
    dtushim/             # DTU shim layer for test isolation
    evidence/            # Evidence collection and property-based validation
    surface/             # Surface-level analysis and property-based validation
    # Agent harness library (standalone, not wired into CLI commands):
    orchestrator/        # Multi-agent topologies with context firewalls
    mission/             # Task decomposition, complexity analysis
    memory/              # Typed memory, KV store, scratchpads
    signal/              # Behavioral heuristics (repetition, context thrash, etc.)
    evaluator/           # Generator-evaluator loops
    cost/                # Token tracking, budget enforcement, model ladders
    ctxmgr/              # Context window management with compaction
    intermediary/        # Intent validation, policy rules, permissions
    bootstrap/           # Repo analysis, AGENTS.md generation
    catalog/             # Tool catalog, overlap detection
    observability/       # OpenTelemetry span attributes
.xylem/                  # State directory (queue, workflows, prompts, phase outputs)
workflows/               # Workflow documentation (WORKFLOW.md files)
docs/                    # Project documentation
```

Key boundary: `cli/internal/` packages must not import from `cli/cmd/`.

# Build & Test

All commands run from the `cli/` directory:

```bash
cd cli
goimports -l .            # Check formatting — CI rejects unformatted code
goimports -w .            # Fix formatting
go vet ./...              # Static analysis
golangci-lint run         # Linter (errcheck, govet, staticcheck, unused)
go build ./cmd/xylem      # Build the binary
go test ./...             # Run all tests
go test -race ./...       # Race detector (recommended, not in CI)
```

CI runs, in order: `goimports -l .`, `go vet`, `golangci-lint`, `go build`, `go test`.

# Golden Principles

1. Always run commands from the `cli/` directory.
2. Never shell out to real subprocesses or git in tests — use existing interface stubs (`CommandRunner`, `WorktreeManager`).
3. Never modify files under `.xylem/phases/` — these are runtime outputs managed by the runner.
4. Property-based tests use `pgregory.net/rapid`, follow naming `TestProp*` in `*_prop_test.go` files.
5. Vessel state transitions follow the state machine: `pending → running → completed/failed/waiting/timed_out/cancelled`. Do not add transitions outside this graph.
6. Use Go templates for prompt rendering in the `phase` package — never string concatenation.
7. Run `goimports -w .` and `golangci-lint run` before committing — CI enforces both.
8. The `source` package has no unit tests by design — it relies on integration testing via `scanner` and `runner`.

# Dependencies

- **Go 1.25+** — version constraint in `go.mod`
- **goimports** — `go install golang.org/x/tools/cmd/goimports@v0.24.0`
- **golangci-lint v2.11** — CI runs via golangci-lint-action
- **git** — must be on PATH (worktree operations)
- **claude** — Claude Code CLI, for session execution
- **gh** — GitHub CLI, authenticated. Required only for `github` source scanning.
