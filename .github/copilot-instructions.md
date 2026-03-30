# Code Review Guidelines

## Repository Context

xylem is a Go CLI that schedules and runs autonomous Claude Code sessions against GitHub issues. It uses a JSONL-backed queue of "vessels" (units of work) that are drained through multi-phase YAML workflows executed in isolated git worktrees.

## Architecture Awareness

- The codebase has two distinct layers: CLI commands (`cli/cmd/xylem/`) that wire things together, and internal packages (`cli/internal/`) that contain all business logic
- `cli/internal/` contains both the core CLI pipeline (queue, runner, scanner, worktree, config, gate, workflow, phase, source, reporter) and a standalone agent harness library (orchestrator, mission, memory, signal, evaluator, cost, ctxmgr, intermediary, bootstrap, catalog, observability) — these are separate systems
- All state flows through the `Vessel` struct and its state machine: `pending -> running -> completed/failed/waiting/timed_out/cancelled`. Verify state transitions use `validTransitions` and never bypass the state machine
- The queue uses file-level locking via `gofrs/flock`. Mutations must go through `withLock`, reads through `withRLock`

## Error Handling

- Errors must be wrapped with context using `fmt.Errorf("description: %w", err)` — never return bare errors from functions that call other functions
- Sentinel errors (like `ErrInvalidTransition`) should use `errors.New` and be checked with `errors.Is`
- Functions that distinguish "not found" from other errors should return a specific error, not nil

## Concurrency

- The runner uses bounded concurrency via semaphore channels (`sem := make(chan struct{}, n)`)
- Queue operations are protected by file locks, not mutexes — any new queue method must use `withLock` or `withRLock`
- Check for race conditions when vessels are accessed concurrently across goroutines

## Configuration

- `config.Load` provides defaults for all fields — PRs should not duplicate defaults in call sites
- Legacy config migration happens in `normalize()` — new fields must not break old `.xylem.yml` files
- `claude.template` and `claude.allowed_tools` at the top level are rejected with migration guidance; preserve this invariant

## Security

- Check for hardcoded secrets, API keys, or credentials
- The runner shells out to `claude` and `gh` CLIs — verify that user-supplied values (issue titles, branch names, labels) are never interpolated into shell commands without `shellQuote`
- File paths from config or queue entries must not enable path traversal outside the worktree
