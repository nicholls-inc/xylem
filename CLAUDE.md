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

## Local observability

A Jaeger all-in-one container provides trace collection, UI, and API for development:

```bash
# Start Jaeger (OTLP on :4317, UI on :16686)
docker compose -f dev/docker-compose.yml up -d

# Stop
docker compose -f dev/docker-compose.yml down
```

`.xylem.yml` must have `observability.endpoint` set to `localhost:4317` for traces to export. Without an endpoint, tracing is silently disabled. Jaeger in this dev stack is trace-only; xylem keeps daemon logs on stderr and `daemon.log` instead of exporting OTLP logs.

- **UI**: http://localhost:16686
- **API**: http://localhost:16686/api/
  - `GET /api/services` — list services
  - `GET /api/traces?service=xylem&limit=10` — recent traces
  - `GET /api/traces/{traceID}` — full trace detail

## Multi-repo operation

Multiple xylem daemons can coexist across different repositories with no code changes. Each daemon is fully isolated.

Key facts for agents operating in this context:

- **CWD-scoped**: every daemon is bound to the directory it was started from. All state paths (`StateDir`, defaulting to `.xylem`) are relative to that repo's root. Never read or write another repo's `.xylem/` directory.
- **No shared state**: separate flock on `state/daemon.pid` (which also stores the PID), separate queue (`state/queue.jsonl`), and separate phase outputs. There is no cross-repo IPC or global semaphore.
- **Per-repo pause lever**: `state/paused` is the pause marker for a single daemon. Writing or removing it affects only the daemon whose state directory contains it.
- **Concurrency and cost are per-daemon**: `concurrency` caps only the sessions spawned by one daemon; `cost.daily_budget_usd` tracks only the spend that daemon has incurred. Multiple running daemons multiply both without coordination.
- **PID signaling caveat**: `xylem daemon stop` sends SIGTERM to the PID stored in `state/daemon.pid` without verifying the target is actually a xylem daemon. Do not signal PIDs from a different repo's PID file.

See [docs/multi-repo.md](docs/multi-repo.md) for the full user-facing guide.

## Foxguard protocol

`foxguard` is a fast security scanner that runs in pre-commit and CI. It catches command injection, SSRF, path traversal, hardcoded secrets, and similar patterns. It is **intentionally noisy**: known-safe patterns (argv-style `exec.Command`, hardcoded-host URLs, test fixtures) show up as findings and must be explicitly suppressed.

**Suppression uses a fingerprint-keyed baseline:**
- `.foxguard/baseline.json` — foxguard-managed, fingerprint-keyed list of suppressed findings
- `.foxguard/justifications.md` — human-readable rationale for each baseline entry
- `scripts/check_foxguard_justifications.py` — enforces 1:1 coverage (pre-commit + CI)

### Workflow for a new foxguard finding

1. **Run foxguard locally.** `foxguard --baseline .foxguard/baseline.json .` — or just `git commit`, which triggers the pre-commit hook.
2. **Verify the finding.** Use the `crosscheck:byfuglien` agent (or the `crosscheck:reason` skill) to reason about whether the finding is genuine in the threat model of this codebase. Do **not** guess. Check:
   - Does the dangerous sink actually receive untrusted input?
   - For Go `exec.Command`: is a shell involved, or is it pure argv? (argv = safe)
   - For URLs: is the host hardcoded, or attacker-controllable?
   - For path APIs: is the path joined with trusted roots only?
3. **Resolve:**
   - **Genuine issue** → fix the code. Do not baseline.
   - **False positive** → add the fingerprint to `.foxguard/baseline.json` AND write a corresponding entry in `.foxguard/justifications.md` with a non-empty `**Rationale:**` and `**Verified by:**` line. The pre-commit/CI check will fail the build if either is missing.
4. **Commit both files together.** Never commit a baseline addition without its justification.

### Do not

- **Do not** silently add findings to the baseline "to unblock CI". The justifications check will fail the build. The point of the baseline is to be an auditable list of known-safe patterns, not an escape hatch.
- **Do not** loosen the severity threshold, remove rules, or disable foxguard as a workaround.
- **Do not** edit `.foxguard/baseline.json` by hand when suppressing a new finding — run `foxguard baseline .` (or `foxguard --write-baseline …`) to regenerate it with a correct fingerprint, then add the justification entry.

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
