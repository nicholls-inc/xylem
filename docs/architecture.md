# Architecture

This document describes the internal architecture of xylem for contributors working on the codebase. It covers the system's two-layer design, core abstractions, data flow, isolation model, and testing approach.

Architecture decisions are recorded in [docs/decisions/](decisions/).

## High-level overview

xylem is a two-layer system:

- **Control plane** (Go CLI) -- schedules and runs autonomous workflow execution. Handles source scanning, work queue management, concurrency control, worktree lifecycle, provider invocation, command phases, and phase-based execution.
- **Execution plane** (YAML workflows + prompt templates) -- defines what each workflow phase does. Multi-phase workflow definitions with quality gates between phases. Scaffolded into the target repository by `xylem init`.

The CLI never implements business logic itself. It orchestrates: it figures out what work exists, queues it, creates isolated environments, and drives workflow phases through a sequence of steps. Prompt phases delegate implementation work to the configured LLM provider. Command phases run deterministic shell commands inside the worktree.

```
                          xylem
                +-----------------------+
                |     Control Plane     |
                |       (Go CLI)        |
                |                       |
                |  scan -> queue ->     |
                |  drain -> worktree -> |
                |  phase execution      |
                +-----------+-----------+
                            |
                            | launches
                            v
                +-----------------------+
                |    Execution Plane    |
                |  (Workflow YAML +     |
                |   Prompt Templates)   |
                |                       |
                |  analyze -> plan ->   |
                |  implement -> pr      |
                +-----------------------+
```

## Data flow

The following diagram traces work from discovery through execution:

```
Sources                     xylem scan            Queue
+--------------+            +----------+          +----------------------+
| github       |--Scan()--> | Scanner  |--Enqueue>| .xylem/state/queue.jsonl |
| (manual)     |            +----------+          +----------+-----------+
+--------------+                                             |
                            xylem drain                      | Dequeue
                            +----------+          +----------v-----------+
                            | Runner   |<---------| Pending vessels      |
                            +----+-----+          +----------------------+
                                 |
                 +---------------+---------------+
                 v               v               v
          source.OnStart   worktree.Create   Phase execution
          (side effects)   (git worktree)    (workflow phases in worktree)
                                                  |
                                          +-------+-------+
                                          v       v       v
                                       analyze > plan > implement > pr
                                                  |              |
                                              label gate    command gate
                                             (wait for      (run tests,
                                              approval)      retry on fail)
```

**Step by step:**

1. **Scan** -- The scanner queries each configured source (currently GitHub issues by label). The GitHub source calls `gh search issues`, filters out excluded labels, deduplicates against the existing queue and remote branches, and returns candidate vessels.

2. **Enqueue** -- The scanner writes each new vessel to `state/queue.jsonl` in `pending` state. The queue is a JSONL file protected by file-level locking (`gofrs/flock`). Deduplication uses `HasRef()` to prevent the same issue URL from being enqueued twice.

3. **Dequeue** -- The runner atomically reads the queue, finds the first `pending` vessel that fits both the global and per-workflow concurrency limits, transitions it to `running`, sets `StartedAt`, and returns it. The runner enforces the global limit with a buffered-channel semaphore and tracks optional per-workflow caps in-memory.

4. **Worktree creation** -- The runner asks the source for a branch name (e.g. `fix/issue-42-login-crash`), then creates an isolated git worktree at `.claude/worktrees/<branch>` branched from `origin/<default-branch>`. Provider config files (`.claude/settings.json`, rules) are copied into the worktree.

5. **Phase execution** -- The runner loads the workflow YAML, reads `.xylem/HARNESS.md`, then iterates through phases. Prompt phases render a Go template with issue data and previous phase outputs, then invoke the resolved provider (`claude` or `copilot`). Command phases render and run a shell command directly in the worktree. Phase outputs are persisted to `.xylem/state/phases/<vessel-id>/<phase>.output`.

6. **Gate evaluation** -- After each phase, if a gate is defined:
   - **Command gate**: runs a shell command (e.g. `make test`). On failure, the same phase re-runs with gate output appended to the prompt, up to `retries` times.
   - **Label gate**: transitions the vessel to `waiting` state and returns. A separate `CheckWaitingVessels` loop polls GitHub for the expected label and resumes the vessel when found.

7. **Completion** -- After all phases pass, the vessel transitions to `completed`. On any unrecoverable failure, it transitions to `failed` with an error message.

## Core abstractions

### Vessel

A vessel is the unit of work in xylem. It represents a single task to be processed -- typically a GitHub issue, but also ad-hoc prompts submitted via `xylem enqueue`.

**State machine:**

```
                     +------------+
                     |  pending   |
                     +-----+------+
                           |
                  +--------+--------+
                  v                 v
             +---------+     +-----------+
             | running |     | cancelled |
             +----+----+     +-----------+
                  |
      +-----------+-----------+-----------+
      v           v           v           v
+----------+ +--------+ +---------+ +-----------+
| completed| | failed | | waiting | | cancelled |
+----------+ +---+----+ +----+----+ +-----------+
                 |            |
                 v            +--------+---------+
            +---------+       v                  v
            | pending |  +---------+       +-----------+
            +---------+  | running |       | timed_out |
            (retry)      +---------+       +-----------+
                         (label found)
```

Terminal states: `completed`, `cancelled`, `timed_out`. The `failed -> pending` transition supports the `xylem retry` command.

**Key fields on a Vessel:**

| Field | Purpose |
|-------|---------|
| `ID` | Unique identifier (e.g. `issue-42`) |
| `Source` | Origin identifier (`github-issue`, `manual`) |
| `Ref` | External reference (issue URL, ticket ID) |
| `Workflow` | Workflow name to execute (e.g. `fix-bug`) |
| `Prompt` | Direct prompt text (bypasses workflow phases) |
| `Meta` | Key-value metadata (e.g. `issue_num`, cached issue data) |
| `State` | Current state in the state machine |
| `CurrentPhase` | Index of the next phase to execute (supports resume) |
| `PhaseOutputs` | Map of phase name to output file path |
| `WorktreePath` | Path to the git worktree (persisted for resume from `waiting`) |
| `FailedPhase` | Name of the phase that failed (for retry context) |
| `GateOutput` | Output from the last gate failure (for retry context) |
| `RetryOf` | ID of the original vessel this is retrying |

### Source

The `Source` interface defines how xylem discovers work. Each source implementation knows how to find tasks and how to set up side effects when work begins.

```go
type Source interface {
    Name() string
    Scan(ctx context.Context) ([]queue.Vessel, error)
    OnEnqueue(ctx context.Context, vessel queue.Vessel) error
    OnStart(ctx context.Context, vessel queue.Vessel) error
    OnComplete(ctx context.Context, vessel queue.Vessel) error
    OnFail(ctx context.Context, vessel queue.Vessel) error
    OnTimedOut(ctx context.Context, vessel queue.Vessel) error
    BranchName(vessel queue.Vessel) string
}
```

**Methods:**

- `Scan()` -- Discover new tasks and return candidate vessels. Called by the scanner.
- `OnEnqueue()` -- Side effects when a vessel is enqueued. The GitHub source uses this to add a configured `queued` status label.
- `OnStart()` -- Side effects when a vessel starts running. The GitHub source adds a configured `running` status label (or falls back to `in-progress`).
- `OnComplete()` -- Side effects when a vessel completes. The GitHub source adds a configured `completed` status label.
- `OnFail()` -- Side effects when a vessel fails. The GitHub source adds a configured `failed` status label.
- `OnTimedOut()` -- Side effects when a vessel's gate times out. The GitHub source adds a configured `timed_out` status label.
- `BranchName()` -- Generate the git branch name for a vessel's worktree.

**Implementations:**

| Source | `Name()` | Branch pattern | `OnStart()` side effect |
|--------|----------|----------------|------------------------|
| `GitHub` | `github-issue` | `fix/issue-<N>-<slug>` or `feat/issue-<N>-<slug>` | Adds status label (default: `in-progress`) |
| `GitHubPR` | `github-pr` | `review/pr-<N>-<slug>` | Adds status label (default: `in-progress`) |
| `GitHubPREvents` | `github-pr-events` | `event/pr-<N>-<eventType>-<slug>` | None |
| `GitHubMerge` | `github-merge` | `merge/pr-<N>-<slug>` | None |
| `Schedule` | `schedule` | `chore/<source-name>-<tick>` | Persists cadence state in `.xylem/state/schedule-state.json` |
| `Scheduled` | `scheduled` | `scheduled/<task>-<vessel>` | Persists per-task schedule buckets in `.xylem/schedules/` |
| `Manual` | `manual` | `task/<id>-<slug>` | None |

GitHub-backed sources perform source-specific deduplication during scanning rather than one uniform set of checks. `GitHub` and `GitHubPR` check excluded labels, existing queue entries with the same ref, remote branches matching the issue/PR number, and open PRs with matching branch prefixes. `GitHubMerge` primarily deduplicates via merge commit OID. `GitHubPREvents` deduplicates via event-specific ref fragments (label, review ID, comment ID, or head SHA for check failures).

### Workflow

A workflow is a multi-phase execution plan loaded from a YAML file in `.xylem/workflows/`. Phases can be prompt-driven LLM invocations or command phases that execute shell commands in the worktree.

```yaml
name: fix-bug
description: "Diagnose and fix a bug from a GitHub issue"
phases:
  - name: analyze
    prompt_file: .xylem/prompts/fix-bug/analyze.md
    max_turns: 5
    noop:
      match: XYLEM_NOOP
  - name: plan
    prompt_file: .xylem/prompts/fix-bug/plan.md
    max_turns: 3
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

The workflow name must match the YAML filename. Phase names must be unique within a workflow. Prompt files are Go templates rendered with issue data, previous phase outputs, and gate results.

Prompt phases can also override the LLM provider and model at the workflow or phase level, and can set per-phase `allowed_tools` restrictions. The runner resolves those tool requests through the harness catalog's role permissions first, then forwards the effective list to the selected provider CLI. Provider resolution is `phase.llm` -> `workflow.llm` -> `.xylem.yml llm`, with support for both `claude` and `copilot`. `xylem init` also scaffolds `.xylem/HARNESS.md`; the runner reads that file and passes it as a system prompt for prompt phases.

**Built-in workflows:**

- `fix-bug` -- Analyze, Plan, Implement (with test gate), PR
- `implement-feature` -- Analyze, Plan (with label gate for human approval), Implement, PR

### Gate

Gates are inter-phase quality checks. They run after a phase completes and must pass before the next phase begins.

| Type | Behavior | Key fields |
|------|----------|------------|
| `command` | Runs a shell command in the worktree. Exit 0 = pass. Non-zero = fail, triggering a retry of the same phase with gate output as context. | `run`, `retries`, `retry_delay` |
| `label` | Polls a GitHub issue for a specific label. Transitions the vessel to `waiting` state until the label appears or the gate times out. | `wait_for`, `timeout`, `poll_interval` |

Command gates enable automated quality enforcement (run tests, lint, type-check). Label gates enable human-in-the-loop approval between phases.

## Package map

All Go code lives under `cli/`. The packages divide into three groups: **CLI packages** that power the `xylem` binary, **agent harness packages** that are standalone building blocks not yet wired into CLI commands, and **test infrastructure packages** that support the DTU offline testing framework.

### CLI packages

These packages are used by the CLI commands (`scan`, `drain`, `daemon`, `enqueue`, etc.):

| Package | Purpose |
|---------|---------|
| `cmd/xylem` | CLI entry point (Cobra commands) |
| `config` | Loads `.xylem.yml`, validates configuration, auto-migrates legacy format |
| `queue` | JSONL-backed persistent work queue with file locking and vessel state machine |
| `scanner` | Queries configured sources, deduplicates, enqueues vessels |
| `runner` | Dequeues vessels, creates worktrees, executes workflow phases, handles gates |
| `source` | `Source` interface + `GitHub`, `GitHubPR`, `GitHubPREvents`, `GitHubMerge`, and `Manual` implementations |
| `workflow` | YAML workflow definition loader and validation |
| `phase` | Go template rendering for prompt files with truncation limits |
| `gate` | Command execution and GitHub label polling for inter-phase quality checks |
| `worktree` | Git worktree create/remove/list lifecycle management |
| `reporter` | Phase result collection and GitHub issue comment output |

### Agent harness packages

These packages are standalone, composable building blocks for agent orchestration systems. They are **not wired into the CLI commands** but live in the same `cli/internal/` tree. They have no import dependencies on the CLI packages above (and vice versa).

| Package | Purpose |
|---------|---------|
| `orchestrator` | Multi-agent topologies (sequential, parallel, orchestrator-workers, handoff) with context firewalls between sub-agents, failure handling, and cost tracking |
| `mission` | Complexity analysis, task decomposition into sub-tasks, constraint validation (token budgets, time budgets, blast radius glob patterns), persona scope enforcement |
| `memory` | Mission-scoped typed memory (procedural, semantic, episodic), KV store, scratchpads, progress files, handoff artifacts |
| `signal` | Behavioral heuristics computed from agent traces without LLM involvement: repetition (bigram similarity), tool failure rate, efficiency score, context thrash, task stall. Classifies into Normal/Warning/Critical with aggregate health levels |
| `evaluator` | Generator-evaluator loops with signal-gated intensity and configurable iterations |
| `cost` | Token tracking by agent role and purpose, budget enforcement, model ladders, anomaly detection |
| `ctxmgr` | Context window management with named ordered processors, compaction strategies, durable/working segment separation |
| `intermediary` | Deterministic intent validation with policy rules, audit logging, glob-based file permissions |
| `bootstrap` | Repository analysis, AGENTS.md generation, documentation scaffolding, convention detection |
| `catalog` | Tool catalog with descriptions, parameter types, overlap detection, permission scopes |
| `evidence` | Verification claims and evidence manifests with typed assurance levels (proved, mechanically_checked, behaviorally_checked, observed_in_situ) |
| `surface` | SHA256 surface integrity snapshots for protected files, violation detection against baseline |
| `observability` | OpenTelemetry span attributes for missions, agents, and signals with OTLP gRPC export |

### Test infrastructure packages

These packages support the Digital Twin Universe (DTU) framework for offline scenario testing:

| Package | Purpose |
|---------|---------|
| `dtu` | DTU manifest loading, clock simulation, event replay, verification suites, provider script generation |
| `dtushim` | Shim dispatch for replacing external commands (`gh`, `git`, `claude`, `copilot`) with DTU-controlled behavior |

## Control flow

### Scanner

```
scanner.Scan(ctx)
  |
  +-- Check pause marker (.xylem/paused) -> return early if paused
  |
  +-- buildSources() -> construct Source implementations from config
  |
  +-- For each source:
  |     |
  |     +-- source.Scan(ctx)
  |     |     GitHub: gh search issues --repo --label --state open
  |     |     Filter: excluded labels, existing refs, remote branches, open PRs
  |     |
  |     +-- For each candidate vessel:
  |           |
  |           +-- queue.HasRef(ref) -> skip if already enqueued
  |           +-- queue.Enqueue(vessel) -> append to state/queue.jsonl
  |
  +-- Return ScanResult{Added, Skipped, Paused}
```

### Runner

```
runner.Drain(ctx)
  |
  +-- Parse timeout from config
  +-- Create semaphore (buffered channel, size = Config.Concurrency)
  |
  +-- Loop:
  |     +-- Check ctx.Done() -> break if cancelled
  |     +-- queue.Dequeue() -> get next pending vessel (atomic: pending -> running)
  |     +-- If nil, break (queue empty)
  |     +-- Acquire semaphore slot
  |     +-- Launch goroutine: runVessel(ctx, vessel)
  |
  +-- Wait for all goroutines
  +-- Return DrainResult{Completed, Failed, Skipped, Waiting}

runner.runVessel(ctx, vessel)
  |
  +-- resolveSource(vessel.Source) -> look up Source by name
  +-- source.OnStart(ctx, vessel) -> side effects (add label, etc.)
  |
  +-- Worktree:
  |     +-- If vessel.WorktreePath is set -> reuse (resuming from waiting)
  |     +-- Otherwise: source.BranchName(vessel) -> worktree.Create(ctx, branch)
  |     +-- Persist WorktreePath to queue
  |
  +-- If vessel has Prompt but no Workflow -> runPromptOnly (single provider invocation)
  |
  +-- loadWorkflow(vessel.Workflow) -> parse .xylem/workflows/<name>.yaml
  +-- fetchIssueData(ctx, vessel) -> gh issue view (cached in Meta)
  +-- readHarness() -> read .xylem/HARNESS.md
  +-- rebuildPreviousOutputs(vesselID, workflow) -> read .xylem/state/phases/<id>/*.output
  |
  +-- For each phase (starting from vessel.CurrentPhase):
        |
        +-- If prompt phase:
        |     +-- Render prompt template with issue data, previous outputs, gate result
        |     +-- Write rendered prompt to .xylem/state/phases/<id>/<phase>.prompt
        |     +-- Resolve provider + model, add harness + allowed_tools
        |     +-- runner.RunPhase(ctx, worktreePath, stdin, <provider-cmd>, args...)
        +-- If command phase:
        |     +-- Render phase.run as a template
        |     +-- Write rendered command to .xylem/state/phases/<id>/<phase>.command
        |     +-- Run shell command in worktree
        +-- Write output to .xylem/state/phases/<id>/<phase>.output
        +-- Persist CurrentPhase + PhaseOutputs to queue
        |
        +-- Gate evaluation:
              +-- No gate -> proceed to next phase
              +-- Command gate -> RunCommandGate(ctx, dir, command)
              |     Pass -> proceed
              |     Fail + retries left -> re-run phase with gate output context
              |     Fail + no retries -> vessel fails
              +-- Label gate -> transition to waiting, return "waiting"
```

### Daemon

The daemon combines scan and drain in a continuous loop with configurable intervals:

```
daemon.Run(ctx)
  |
  +-- Parse scan_interval, drain_interval from config
  +-- Start scan ticker + drain ticker
  |
  +-- Loop:
        +-- On scan tick:
        |     scanner.Scan(ctx)
        |     runner.CheckWaitingVessels(ctx)
        |
        +-- On drain tick:
        |     runner.Drain(ctx)
        |
        +-- On ctx.Done() (SIGINT/SIGTERM):
              Graceful shutdown: running sessions finish, no new work starts
```

## Isolation model

Every vessel runs in its own git worktree. This provides filesystem isolation between concurrent workflow runs -- each vessel works on a separate branch in a separate directory, so there is no risk of file conflicts.

**Worktree lifecycle:**

1. **Create** -- `git fetch origin <default-branch>` then `git worktree add .claude/worktrees/<branch> -B <branch> origin/<default-branch>`. The worktree starts from a clean copy of the default branch.

2. **Config copy** -- `.claude/settings.json`, `.claude/settings.local.json`, and `.claude/rules/` are copied from the main repo into the worktree so provider-backed prompt phases have the correct tool permissions and rules.

3. **Execution** -- Prompt phases and command phases both run inside the worktree directory. All file changes are isolated to the worktree's branch.

4. **Cleanup** -- `xylem cleanup` removes worktrees older than `cleanup_after` (default 7 days) using `git worktree remove --force` followed by best-effort branch deletion.

**Branch naming conventions:**

| Source | Pattern |
|--------|---------|
| GitHub | `fix/issue-<N>-<slug>` or `feat/issue-<N>-<slug>` (based on workflow name) |
| GitHubPR | `review/pr-<N>-<slug>` |
| GitHubPREvents | `event/pr-<N>-<eventType>-<slug>` |
| GitHubMerge | `merge/pr-<N>-<slug>` |
| Manual | `task/<id>-<slug>` |

The slug is derived from the last path component of the reference URL, lowercased, non-alphanumeric characters replaced with hyphens, and truncated to 20 characters.

**Default branch detection** uses a cascade of methods: `gh repo view --json defaultBranchRef`, then `git symbolic-ref refs/remotes/origin/HEAD`, then `git symbolic-ref HEAD`, then `git remote show origin`. The `default_branch` config field can override all of these.

## Agent harness library

The `cli/internal/` tree contains a second group of packages that are **not used by the CLI commands**. These implement foundational building blocks for mission-scoped agent orchestration -- a layer above what the current CLI provides.

The distinction matters for contributors:

- **CLI packages** (queue, runner, scanner, source, workflow, gate, phase, worktree, reporter, config) are the production system. Changes here affect the `xylem` binary directly.
- **Harness packages** (orchestrator, mission, memory, signal, evaluator, cost, ctxmgr, intermediary, bootstrap, catalog, evidence, surface, observability) are standalone libraries. They have their own test suites and no runtime coupling to the CLI.
- **Test infrastructure packages** (dtu, dtushim) support offline scenario testing via the Digital Twin Universe framework.

### What the harness enables

The CLI today executes a linear sequence of phases per vessel. The harness packages provide primitives for more sophisticated orchestration:

**Multi-agent coordination** (`orchestrator`) -- Instead of a single prompt-driven agent session per phase, an orchestrator can dispatch sub-agents in parallel, sequential, orchestrator-workers, or handoff topologies. Sub-agents have context firewalls (they only see their task and results from upstream agents, not the full orchestrator context). The orchestrator tracks agent status, token usage, wall clock time, and dependency edges with cycle detection.

**Mission decomposition** (`mission`) -- A mission (the harness equivalent of a vessel) can be analyzed for complexity (simple/moderate/complex based on file count, domain count, and description length) and decomposed into sub-tasks with dependency ordering. Constraints enforce token budgets, time budgets, and blast radius (glob patterns that restrict which files an agent may modify).

**Behavioral monitoring** (`signal`) -- Lightweight heuristics computed from agent execution traces without calling an LLM. Five signals are currently implemented:

| Signal | What it detects | How it works |
|--------|----------------|--------------|
| Repetition | Agent producing the same output repeatedly | Bigram similarity (Dice coefficient) between consecutive content events |
| ToolFailureRate | High rate of failed tool calls | Ratio of failed to total tool calls |
| EfficiencyScore | Agent using too many turns | Actual turns / expected baseline |
| ContextThrash | Frequent context resets | Ratio of compaction events to total events |
| TaskStall | No progress within a time window | Checks for successful tool calls in the most recent window |

Each signal is classified into Normal/Warning/Critical thresholds. The aggregate health assessment (Excellent/Good/Neutral/Poor/Severe) determines whether to trigger more expensive LLM-based evaluation via the `evaluator` package.

**Cost tracking** (`cost`) -- Token usage tracking by agent role, budget enforcement that can halt execution when a budget is exceeded, and model ladders for stepping down to cheaper models as budgets are consumed.

**Context management** (`ctxmgr`) -- Named, ordered context processors with compaction strategies. Separates durable context (instructions, constraints) from working context (conversation history) so compaction can reclaim working context without losing critical instructions.

**Policy enforcement** (`intermediary`) -- Deterministic intent validation with policy rules and glob-based file permissions. An audit log records all validation decisions.

### Relationship to CLI

The harness packages and CLI packages share no runtime imports. They exist in the same repository because they serve the same domain (agent orchestration) and are expected to converge as the system evolves. A future version of the runner might delegate phase execution to the orchestrator, use mission constraints for budget enforcement, or feed runner output into the signal package for health monitoring.

For now, treat them as two independent codebases that happen to share a Go module.

## Queue persistence

The queue is a single JSONL file (`<state_dir>/state/queue.jsonl` in the standard `.xylem` layout). Each line is a JSON-encoded `Vessel`. Reads and writes are protected by a file lock (`gofrs/flock`) stored at `<state_dir>/state/queue.jsonl.lock`.

- **Read operations** (`List`, `FindByID`, `ListByState`, `HasRef`) acquire a read lock.
- **Write operations** (`Enqueue`, `Dequeue`, `Update`, `UpdateVessel`, `Cancel`) acquire an exclusive write lock.
- Every write operation reads the entire file, modifies the in-memory slice, and rewrites the entire file.

This design is simple and correct for low-throughput workloads (tens to hundreds of vessels). It would need replacing for high-throughput use cases.

The queue supports **legacy migration**: old entries with `issue_url`/`issue_num` fields are automatically migrated to the current `Source`/`Ref`/`Meta` format on read.

## Prompt rendering

Phase prompts are Go `text/template` files rendered with a `TemplateData` struct. Available template variables:

| Variable | Source |
|----------|--------|
| `{{.Issue.Title}}`, `{{.Issue.Body}}`, `{{.Issue.URL}}`, `{{.Issue.Number}}`, `{{.Issue.Labels}}` | Fetched from GitHub via `gh issue view` (cached in vessel `Meta`) |
| `{{.PreviousOutputs.<phase>}}` | Output text from a completed earlier phase |
| `{{.GateResult}}` | Output from the last gate failure (populated on gate retry) |
| `{{.Phase.Name}}`, `{{.Phase.Index}}` | Current phase metadata |
| `{{.Vessel.ID}}`, `{{.Vessel.Source}}` | Vessel metadata |

To prevent context window overflow, all fields are truncated before rendering:

| Field | Limit |
|-------|-------|
| Previous phase output | 16,000 characters |
| Gate result | 8,000 characters |
| Issue body | 32,000 characters |

Templates use `missingkey=zero` so referencing an undefined key produces a zero value instead of an error.

## Testing approach

### Interface-driven testing

Every package that interacts with external systems (git, `gh`, `claude`, the filesystem) defines a `CommandRunner` interface and accepts it as a dependency. Tests provide stub implementations that return canned responses. No test in the codebase spawns a real subprocess or performs real git operations.

Key interfaces:

| Interface | Package | Abstracts |
|-----------|---------|-----------|
| `CommandRunner` | `source`, `scanner`, `worktree`, `gate` | Shell command execution (`Run()`) |
| `CommandRunner` | `runner` | Extended: `RunOutput()`, `RunProcess()`, `RunPhase()` |
| `WorktreeManager` | `runner` | Worktree creation (`Create()`) |

### Property-based tests

The `memory` and other harness packages use `pgregory.net/rapid` for property-based testing. These tests:

- Follow the naming convention `TestProp*` in files named `*_prop_test.go`
- Generate random inputs and verify invariants hold across all generated cases
- Test properties like "KV store get after put always returns the stored value" rather than specific input/output pairs

### Test infrastructure

- Queue tests create temp directories for JSONL files, ensuring test isolation
- Worktree tests create temp directories for mock git repositories
- The `source` package includes unit tests for source-specific behavior and is also exercised through the scanner and runner integration tests
- CI runs for pushes and PRs to `main` when changes touch `cli/**` or `.golangci.yml` via `.github/workflows/ci.yml`. It checks formatting (`goimports`), runs `go vet`, lints with `golangci-lint`, builds the binary (`go build ./cmd/xylem`), and runs the test suite (`go test ./...`). Additional workflows handle releases (`release.yml`) and DTU canary checks (`dtu-canary.yml`). There is no Makefile

### Running tests

```bash
cd cli
go test ./...                              # all tests
go test ./internal/queue                   # single package
go test ./internal/queue -run TestDequeue  # single test
go test ./internal/memory -run TestProp    # property-based tests
go test -race ./...                        # with race detector
```
