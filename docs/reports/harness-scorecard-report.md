# Agent Harness Scorecard Report

**Repository:** xylem
**Assessed:** 2026-03-30
**Overall Score:** 60/100 — Developing

## Score Summary

| Category | Score | Max |
|----------|-------|-----|
| 1. Repository Legibility | 10 | 14 |
| 2. Context Management | 7 | 10 |
| 3. Tool Design | 6 | 10 |
| 4. Memory & State Persistence | 8 | 10 |
| 5. Agent Loop & Orchestration | 4 | 10 |
| 6. Evaluation & Observability | 2 | 10 |
| 7. Security & Sandboxing | 6 | 10 |
| 8. Architectural Enforcement | 6 | 8 |
| 9. Error Handling & Recovery | 5 | 8 |
| 10. Cost & Efficiency | 6 | 10 |
| **Total** | **60** | **100** |

## Key finding

xylem contains a comprehensive harness library (`cli/internal/`) implementing many best practices — context management, cost tracking, signal-based evaluation, policy enforcement, multi-agent orchestration. However, these packages are **not wired into the production CLI**. The CLI itself has strong fundamentals (state machine, worktree isolation, gates, retry) but operates without the advanced capabilities sitting right next to it. The highest-impact improvements come from integrating what already exists, not building new things.

---

## Detailed Scores

### Category 1: Repository Legibility (10/14)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 1.1 | Bootstrap self-sufficiency | 1 | `go install` + `xylem init` scaffolds config/workflows/prompts, prerequisites documented in README. But no single-command bootstrap that installs deps (Go, git, claude, gh). |
| 1.2 | Task entry points | 1 | CLAUDE.md documents build, test, vet. CI confirms `go vet && go build && go test`. No formatter or dedicated linter — deliberate choice documented as "no Makefile, linter, or CI pipeline — just `go test`." |
| 1.3 | Validation harness | 2 | 33 test files across 22 packages, all green. Property-based tests with `pgregory.net/rapid`. CI pipeline (`.github/workflows/ci.yml`) runs `go vet`, `go build`, `go test` on push/PR. |
| 1.4 | Codebase map | 2 | CLAUDE.md: architecture, package map, testing patterns, terminology. `.xylem/HARNESS.md`: structural map + golden principles. `docs/architecture.md`: extensive control flow diagrams, isolation model, package map. `.github/copilot-instructions.md` + `.github/instructions/go.instructions.md`. |
| 1.5 | Linting and formatting | 1 | `go vet` enforced in CI. No linter config (.golangci.yml), no formatter, no pre-commit hooks. |
| 1.6 | Doc structure | 2 | `docs/` with architecture.md, workflows.md, getting-started.md, configuration.md, cli-reference.md. Subdirectories: `design/`, `best-practices/`, `ideas/`. CLAUDE.md and README link to all docs. |
| 1.7 | Decision records | 1 | `docs/design/agent-harness-user-stories.md`, `docs/best-practices/harness-engineering.md`, CHANGELOG.md with versioned changes. No formal ADRs. `docs/lessons-learned/` exists but is empty. |

### Category 2: Context Management (7/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 2.1 | Progressive disclosure | 2 | CLAUDE.md is ~90 lines serving as a table of contents pointing to `docs/architecture.md`, `docs/workflows.md`, etc. HARNESS.md provides a focused structural map with golden principles. Layered instruction files for different audiences. |
| 2.2 | Context compaction strategy | 1 | `ctxmgr` package: `DefaultCompactionThreshold = 0.95`, four strategies (Write/Select/Compress/Isolate), durable vs. working segment separation. **Not wired into CLI.** CLI uses character-level truncation (16K/32K/8K limits per field) as a crude form of compaction. |
| 2.3 | Structured prompt organization | 2 | All prompt templates use Markdown headers (`## Analysis`, `## Plan`, `## Previous Gate Failure`). Conditional blocks via Go template syntax. Agent instruction files use consistent Markdown structure throughout. |
| 2.4 | Token-efficient tool outputs | 1 | Phase outputs truncated to 16K chars, gate results to 8K, issue body to 32K before re-injection. Truncation suffix appended. But it's blunt character limits, not intelligent summarization — may still inject more context than needed. |
| 2.5 | Context isolation via sub-agents | 1 | `orchestrator` package has multi-agent topologies with context firewalls and condensed SubAgentResult. **Not wired into CLI.** Workflow phases provide natural isolation — each phase runs in a fresh Claude session with only prior outputs (truncated) passed forward. |

### Category 3: Tool Design (6/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 3.1 | Tool descriptions and documentation | 1 | `catalog` package: full tool definitions with Name, Description, Parameters (Type, Required, Description), ReturnFormat, ErrorConditions, Scope. **Not wired into CLI.** Production CLI uses shell commands (claude, gh, git) via CommandRunner interfaces without self-documenting schemas. |
| 3.2 | Minimal overlap between tools | 2 | CLI tools are distinct: `claude` for sessions, `gh` for GitHub, `git` for worktrees. No overlapping functionality. `catalog` package adds overlap detection (Similarity scoring). |
| 3.3 | Error handling in tools | 2 | Gate package distinguishes system errors from gate failures (non-zero exit = gate fail, not error). Wrapped errors throughout: `fmt.Errorf("run gate command: %w", err)`. Sentinel errors (`ErrInvalidTransition`). Structured `DrainResult`. |
| 3.4 | MCP or standardized tool integration | 0 | No MCP server. Tools integrated via shell commands to external CLIs. No centralized tool registry in production. |
| 3.5 | Custom lint/error messages as teaching | 1 | Config validation rejects `claude.template` and `claude.allowed_tools` at top level with specific migration guidance. Workflow validator checks prompt file existence, phase name uniqueness, gate field validity. Not full educational lint rules. |

### Category 4: Memory & State Persistence (8/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 4.1 | Progress tracking artifacts | 2 | JSONL queue persists full vessel state machine. Phase outputs persisted to `.xylem/phases/<vessel-id>/<phase>.output`. `CurrentPhase` tracked in vessel for resume. `PhaseOutputs` map carried across phases. |
| 4.2 | Git as checkpoint mechanism | 2 | Each vessel runs on a named branch (`fix/issue-42-login-crash`). Conventional commit messages: `feat:`, `fix:` prefixes with descriptions. Branch-per-vessel provides clear rollback points. |
| 4.3 | Session initialization ritual | 1 | Runner loads workflow YAML, fetches issue data (cached in Meta), reads HARNESS.md, rebuilds previous outputs. Phase templates inject this context. But no explicit agent-level "read these files first" startup ritual. |
| 4.4 | Structured handoff between sessions/agents | 2 | `PreviousOutputs` map carries phase-to-phase context. `GateResult` carries failure context for retries. Vessel persists `CurrentPhase`, `WorktreePath`, `FailedPhase`, `GateOutput` for resume from waiting/failed states. |
| 4.5 | Execution plans as first-class artifacts | 1 | Workflow YAML definitions versioned in `.xylem/workflows/`. Phase outputs (including plans from plan phases) persisted. CHANGELOG.md tracks releases. But no systematic plan/debt tracking as first-class repo artifacts. |

### Category 5: Agent Loop & Orchestration (4/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 5.1 | Generator-evaluator separation | 1 | `evaluator` package: generator-evaluator loops with signal-gated intensity, QualityScore, WeightedScore. **Not wired into CLI.** CLI has partial separation: implement phase generates, verify phase + command gates evaluate. Same prompt author though. |
| 5.2 | Sprint contracts or task decomposition | 1 | `mission` package: complexity analysis, task decomposition with dependencies, constraints. **Not wired into CLI.** Workflows provide phase-level decomposition with gates as checkpoints but no explicit acceptance criteria per phase. |
| 5.3 | Live system evaluation | 1 | Command gates run `go test ./...` on the actual changed code in the worktree. More than code review, but limited to unit/package tests. No browser automation, API testing, or E2E evaluation. |
| 5.4 | Retry limits and escalation | 1 | Gate retries configurable (`retries: 2`). Label gate has `timeout` for time-bounded waiting. Vessel timeout (`30m`). On exhaustion, vessel fails or times out. No circuit breakers, no clear human escalation beyond label gates. |
| 5.5 | Harness simplification tracking | 0 | `docs/best-practices/harness-engineering.md` discusses the concept. No evidence of component removal, model capability tracking, or systematic simplification. CHANGELOG shows only additions. |

### Category 6: Evaluation & Observability (2/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 6.1 | Instrumentation and tracing | 1 | `observability` package: OTel span attributes for missions, agents, signals. OTLP gRPC export. `go.mod` includes full OTel stack. **Not wired into CLI.** Production CLI uses basic `log.Printf`. |
| 6.2 | Automated evaluation pipeline | 1 | CI runs `go vet` + `go build` + `go test` on every push/PR. Property-based tests. But no automated evaluation of agent output quality, no benchmarks, no ground truth datasets. |
| 6.3 | Evaluator calibration | 0 | `evaluator` package has scoring infrastructure. No calibration logs, human judgment comparison, few-shot examples, or rubric tuning records. |
| 6.4 | Quality scoring or grading | 0 | `evaluator` package has QualityScore types. No quality tracking over time in the production system. |
| 6.5 | Agent-accessible observability | 0 | Phase outputs are persisted and readable. No structured metrics, LogQL/PromQL, or agent-queryable health checks. |

### Category 7: Security & Sandboxing (6/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 7.1 | Execution sandboxing | 1 | Each vessel runs in an isolated git worktree (filesystem isolation). `.claude/settings.local.json` scopes WebFetch to specific domains. No Docker/VM isolation. Config uses `--dangerously-skip-permissions`. |
| 7.2 | Least privilege / permission scoping | 1 | Per-phase `allowed_tools` in workflow YAML. `.claude/settings.local.json` restricts WebFetch domains. `intermediary` and `catalog` packages have policy rules and permission scopes. **Not wired into CLI.** |
| 7.3 | Human-in-the-loop gates | 2 | Label gates: vessel enters `waiting` state, polls GitHub for human-applied label before proceeding. implement-feature workflow has plan approval gate. PRs created for human review. Mechanically enforced via state machine. |
| 7.4 | Prompt injection defenses | 1 | `shellQuote()` in gate.go prevents shell injection. Go template rendering separates instructions from data. `.github/copilot-instructions.md` warns against interpolating user input. Workflow phases create natural plan-then-execute flow. No full architectural containment. |
| 7.5 | Memory and data isolation | 1 | Per-vessel worktrees and queue entries. `memory` package: per-mission scoping, `sanitizePathComponent`, `maxValueLen` (1MB). **Not wired into CLI.** No expiration policies in production. |

### Category 8: Architectural Enforcement (6/8)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 8.1 | Mechanical invariant enforcement | 1 | CI enforces `go vet` + `go build` + `go test`. State machine transitions enforced by `validTransitions` map in code. Import boundary (`cli/internal/` must not import `cli/cmd/`) documented but not mechanically enforced. |
| 8.2 | Entropy management / cleanup agents | 1 | `xylem cleanup` removes stale worktrees older than `cleanup_after`. Manual invocation. No automated drift detection, consistency scanners, or background cleanup agents. |
| 8.3 | Technology choices optimized for agent legibility | 2 | Minimal deps by design: `go.instructions.md` says "flag any new third-party imports." Stdlib preferred. Well-known libraries only: cobra, viper, yaml.v3, flock. `docs/best-practices/harness-engineering.md` covers boring tech preference. |
| 8.4 | Golden principles encoded in repo | 2 | HARNESS.md has explicit "Golden Principles" section (8 rules). `go.instructions.md`: testing patterns, naming, error patterns, state machine discipline, template usage, dependency policy. `copilot-instructions.md`: architecture awareness, concurrency, security. |

### Category 9: Error Handling & Recovery (5/8)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 9.1 | Failure-as-signal feedback loop | 1 | Gate failures feed back into prompts via `{{.GateResult}}`. `xylem retry` re-runs from failed phase with failure context (`FailedPhase`, `GateOutput`). No systematic harness-improvement pipeline from repeated failures. |
| 9.2 | Feature verification before declaring done | 2 | Command gates run test suites before phase advancement. implement-feature has explicit verify phase. Vessel can only reach `completed` after all phases and gates pass. State machine enforces this. |
| 9.3 | Graceful degradation and fallbacks | 1 | Gate retries with configurable count and delay. Vessel states for different failure modes (failed, timed_out, waiting). No fallback to simpler approaches or degraded-mode operation. |
| 9.4 | Rollback capability | 1 | Branch-per-vessel via worktrees provides rollback points. `xylem retry` re-runs from failed phase. Phase outputs preserved for debugging. But no automated rollback on failure — vessel stays in failed state until manual retry. |

### Category 10: Cost & Efficiency (6/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 10.1 | Compaction thresholds | 1 | `ctxmgr` package: `DefaultCompactionThreshold = 0.95`, configurable, with durable/working segment separation. **Not wired into CLI.** CLI uses character truncation (16K/32K/8K). |
| 10.2 | Model selection strategy | 2 | Config supports per-source model selection: triage uses `claude-haiku-4-5` (cheaper), main work uses `claude-sonnet-4-6` (capable). Per-workflow model config supported. Multiple LLM providers (claude, copilot). `cost` package has ModelLadder. |
| 10.3 | Token budget awareness | 1 | `cost` package: token tracking by role/purpose, budget enforcement, cost reports, anomaly detection. **Not wired into CLI.** CLI has `max_turns` per phase and `timeout` per vessel as rough cost limiters. |
| 10.4 | Caching and deduplication | 1 | Scanner deduplicates: checks existing refs, remote branches, open PRs. Issue data cached in vessel Meta. Phase outputs persisted and reused on resume. No LLM response caching or plan reuse. |
| 10.5 | Efficient multi-agent token usage | 1 | `orchestrator` package: SubAgentResult returns condensed output, context firewalls between sub-agents. **Not wired into CLI.** CLI passes truncated phase outputs (16K) between sessions, not full context. |

---

## Top 5 Strengths

1. **Exceptional codebase legibility for agents** — Multiple layered instruction files (CLAUDE.md, HARNESS.md, copilot-instructions.md, go.instructions.md) with progressive disclosure. Agents can orient quickly and find exactly what they need.

2. **Robust state persistence and handoff** — JSONL queue with full state machine, phase output persistence, structured handoff via `PreviousOutputs` and `GateResult`, resume from waiting/failed states. Work is never lost.

3. **Mechanical quality enforcement** — Command gates run real test suites on changed code. Verify phase after implement. State machine ensures vessels can only complete after all gates pass. No premature "done."

4. **Golden principles and coding standards encoded in-repo** — HARNESS.md golden principles, Go-specific review standards, copilot review guidelines. Human taste captured once, available to every agent.

5. **Deliberate boring technology choices** — Minimal dependencies by policy, stdlib preference, well-known Go libraries. Every dep justified. Agent-friendly by design.

## Top 5 Gaps (highest-impact improvements)

1. **Harness library not integrated into CLI** — The `cli/internal/` tree contains production-quality implementations of context management (`ctxmgr`), cost tracking (`cost`), behavioral signals (`signal`), evaluation (`evaluator`), observability (`observability`), and policy enforcement (`intermediary`). None are wired into the runner. Integrating even 2-3 of these would jump the score by 10-15 points.

2. **No observability in production** — The runner is a black box. No structured tracing, no token usage tracking, no latency metrics. The OTel infrastructure exists in the `observability` package but isn't connected. Without observability, you can't diagnose why vessels fail or how to improve prompts.

3. **No automated agent output evaluation** — CI tests the Go code but nothing evaluates the quality of what Claude produces. No eval datasets, no scoring of prompt effectiveness, no regression tracking of agent output quality. The `evaluator` package has the types but isn't used.

4. **No container sandboxing** — Vessels run on the host with `--dangerously-skip-permissions`. Git worktrees provide filesystem isolation but not process or network isolation. A misbehaving Claude session could affect the host system.

5. **No formal linter or ADRs** — `go vet` catches basic issues but no `golangci-lint` or equivalent catches deeper problems (unused params, error shadowing, etc.). Architectural decisions live in docs and people's heads, not in formal ADRs.

---

## Recommendations by Priority

### Quick Wins (< 1 day effort, high impact)

- **Add golangci-lint to CI** — Create `.golangci.yml` with standard linters (errcheck, govet, staticcheck, unused). Add `golangci-lint run` to the CI workflow. Catches more issues than `go vet` alone. (Criterion 1.5: 1→2, Source: OpenAI harness engineering)

- **Wire `observability` into the runner** — Add OTel spans around `runVessel`, phase execution, and gate evaluation. The package already exists with the right attribute types. Connect the dots. (Criterion 6.1: 1→2, Source: AWS bedrock-agentcore)

- **Add a `goimports` or `gofumpt` format step** — Enforce formatting in CI. Zero-config improvement to repo legibility. (Criterion 1.2: 1→2, Source: OpenAI harness engineering)

- **Create a formal ADR directory** — `docs/decisions/` with even 2-3 ADRs for key choices (JSONL over SQLite, worktree isolation model, harness library vs CLI separation). (Criterion 1.7: 1→2, Source: OpenAI harness engineering)

### Medium-Term (1–5 days effort)

- **Integrate `cost` tracking into the runner** — Record token usage per phase via the existing `UsageRecord` type. Generate `CostReport` per vessel. Surface budget warnings. This is the single highest-value integration. (Criteria 10.3: 1→2, 6.1 improves further, Source: Anthropic harness design)

- **Integrate `signal` monitoring into the runner** — Compute behavioral signals from phase outputs (repetition, efficiency). Use thresholds to flag unhealthy vessels in `xylem status`. (Criterion 5.1: 1→2, Source: Anthropic harness design)

- **Build an eval dataset** — Create 5-10 test issues with known-good outcomes. Run workflows against them. Compare outputs to ground truth. Track scores over time. (Criteria 6.2: 1→2, 6.4: 0→1, Source: AWS bedrock-agentcore)

- **Add Docker-based execution option** — Wrap `claude -p` invocation in a Docker container with network restrictions and filesystem mount. The worktree isolation is good; container isolation makes it production-grade. (Criterion 7.1: 1→2, Source: NVIDIA sandboxing agentic workflows)

- **Implement `xylem doctor`** — The design already exists in `docs/ideas/doctor-cli.md`. A preflight validation command catches misconfigurations before wasting Claude sessions. High cost-saving potential. (Criterion 9.3: 1→2, Source: AWS bedrock-agentcore)

### Strategic (requires architectural changes)

- **Wire the orchestrator into the runner** — Replace single-phase-at-a-time execution with orchestrator-driven execution. Enable parallel phases where dependencies allow. Use context firewalls for token efficiency. This is the biggest architectural shift but unlocks the full harness library. (Criteria 2.5, 5.1, 5.2, 10.5 all improve, Source: Anthropic effective context engineering)

- **Integrate the intermediary for policy enforcement** — Route all external actions (gh CLI calls, file writes outside worktree) through the intermediary's intent validation. This provides architectural prompt injection defense and permission enforcement. (Criteria 7.2: 1→2, 7.4: 1→2, Source: OWASP AI Agent Security)

- **Build a failure-to-harness-improvement pipeline** — When vessels fail repeatedly on similar issues, surface patterns and propose harness changes (prompt improvements, new gates, tool restrictions). This closes the feedback loop from agent failures to harness evolution. (Criterion 9.1: 1→2, Source: OpenAI harness engineering)
