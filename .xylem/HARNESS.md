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

# PR auto-admin-merge contract

The daemon auto-merge loop applies to vessel-produced pull requests on
xylem-managed issue branches, plus mature `release-please` pull requests,
when they carry the required merge labels:

- `ready-to-merge` is the daemon's merge-readiness signal for vessel PRs and promoted `release-please` PRs.
- The `no-auto-admin-merge` label is an immediate opt-out and leaves the PR for manual handling.
- Auto-admin-merge only fires when the PR is `MERGEABLE`, CI is fully green, and there is no active `CHANGES_REQUESTED` review state.
- The scheduled `release-cadence` workflow is the only path that promotes a `release-please` PR into this merge loop; those PRs do not require `harness-impl`.
- Human-authored PRs that do not match the xylem issue-branch or promoted `release-please` contract remain outside this path and still require normal manual merge decisions.

Separately, the checked-in self-hosting `merge-pr` workflow remains scoped to
`harness-impl` pull requests, so self-hosted harness PRs carry both
`harness-impl` and `ready-to-merge`.

### Do not finish `merge-pr` work with phase `merge` still failing due to `exit status`. <!-- xylem-lesson:lesson-c1f590566a94 -->
- Rationale: This failure pattern recurred in 6 failed vessels for `merge-pr` and should be encoded as institutional memory instead of rediscovered in later runs.
- Example symptom: exit status 1
- Evidence:
  - `pr-366-merge-pr` (2026-04-11T12:39:49Z) — `phases/pr-366-merge-pr/merge.output`
  - `pr-366-merge-pr-retry-1` (2026-04-11T13:30:42Z) — `phases/pr-366-merge-pr-retry-1/merge.output`
  - `pr-372-merge-pr` (2026-04-11T15:15:23Z) — `phases/pr-372-merge-pr/merge.output`
  - `pr-366-merge-pr-retry-1-retry-1` (2026-04-11T15:30:18Z) — `phases/pr-366-merge-pr-retry-1-retry-1/merge.output`
  - `pr-366-merge-pr-retry-1-retry-1-retry-1` (2026-04-11T16:30:48Z) — `phases/pr-366-merge-pr-retry-1-retry-1-retry-1/merge.output`


### Do not finish `merge-pr` work with phase `merge` still failing due to `exit status`. <!-- xylem-lesson:lesson-c1f590566a94 -->
- Rationale: This failure pattern recurred in 2 failed vessels for `merge-pr` and should be encoded as institutional memory instead of rediscovered in later runs.
- Example symptom: exit status 1
- Evidence:
  - `pr-372-merge-pr-retry-1-retry-1` (2026-04-11T18:26:00Z) — `phases/pr-372-merge-pr-retry-1-retry-1/merge.output`
  - `pr-366-merge-pr-retry-1-retry-1-retry-1-retry-1` (2026-04-11T18:26:03Z) — `phases/pr-366-merge-pr-retry-1-retry-1-retry-1-retry-1/merge.output`
