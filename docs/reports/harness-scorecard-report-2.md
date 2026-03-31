# Agent Harness Scorecard Report

**Repository:** `xylem`
**Assessed:** 2026-03-31
**Overall Score:** **73/100 — Mature**

## Score Summary

| Category | Score | Max |
|----------|-------|-----|
| 1. Repository & Knowledge Layer | 10 | 10 |
| 2. Context Management Layer | 8 | 10 |
| 3. Tool Plane | 9 | 10 |
| 4. Memory & State Layer | 8 | 10 |
| 5. Orchestration Layer | 9 | 10 |
| 6. Verification Layer | 8 | 10 |
| 7. Evaluation & Observability Layer | 2 | 10 |
| 8. Security & Sandboxing Layer | 3 | 10 |
| 9. Architectural Enforcement & Entropy Management | 10 | 10 |
| 10. Cost & Efficiency Layer | 6 | 10 |
| **Total** | **73** | **100** |

## Detailed Scores

### Category 1: Repository & Knowledge Layer (10/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 1.1 | Bootstrap self-sufficiency | 2 | `README.md:25-44` gives a quick-start flow; `docs/getting-started.md:45-72` documents `xylem init` and the scaffold it creates; `docs/configuration.md:5-18` shows the expected repo-local file layout. |
| 1.2 | Canonical command discoverability | 2 | `README.md:135-159` lists core commands; `CLAUDE.md:5-29` gives exact build/test commands; `.github/workflows/ci.yml:11-27` confirms the canonical CI commands are `go vet`, `go build`, and `go test`. |
| 1.3 | Progressive-disclosure entry point | 2 | `README.md:210-217` links deeper docs; `CLAUDE.md:33-91` is a concise contributor/agent map; `.xylem/HARNESS.md:1-73` provides a short repo-local system prompt rather than one monolithic dump. |
| 1.4 | Architecture map and decision records | 2 | `docs/architecture.md:1-252` documents the control plane/execution plane split, data flow, package map, and isolation model; `CLAUDE.md:33-76` captures key architectural decisions and constraints. |
| 1.5 | Examples and task references in-repo | 2 | `README.md:97-117` and `docs/workflows.md:36-81` provide concrete workflow examples; `docs/getting-started.md:214-227` shows example dry-run output; `workflows/fix-bug/WORKFLOW.md:12-127` and `workflows/implement-feature/WORKFLOW.md:12-129` give task-specific operational references. |

### Category 2: Context Management Layer (8/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 2.1 | Context as a compiled view | 2 | `cli/internal/phase/phase.go:18-91` defines structured `TemplateData` instead of ad hoc string concatenation; `cli/internal/ctxmgr/ctxmgr.go:1-217` explicitly models context assembly as an ordered pipeline with durable vs working segments. |
| 2.2 | Write / select / compress / isolate discipline | 1 | Durable phase outputs are written to `.xylem/phases/<vessel-id>/` (`docs/workflows.md:149-162`, `cli/internal/runner/runner.go:282-337`); explicit dependency firewalls select only dependency outputs (`cli/internal/runner/schedule.go:124-149`). Compaction exists in `cli/internal/ctxmgr/ctxmgr.go:174-216`, but it is not wired into the primary CLI execution path. |
| 2.3 | Compaction or reset with structured handoff | 1 | `cli/internal/ctxmgr/ctxmgr.go:30-35` sets a concrete compaction threshold (`0.95`); `cli/internal/memory/memory.go:306-377` implements structured handoff artifacts. Good capability exists, but the main CLI path does not yet use a full reset/compaction handoff loop. |
| 2.4 | Structured prompt organization | 2 | Workflow prompts are split by phase and rendered from stable templates (`docs/workflows.md:32-109`, `cli/internal/workflow/workflow.go:19-57`); system context is isolated in `.xylem/HARNESS.md:1-73`. |
| 2.5 | Just-in-time retrieval and token-efficient outputs | 2 | `workflows/fix-bug/WORKFLOW.md:36-40` and `workflows/implement-feature/WORKFLOW.md:37-44` instruct runtime Grep/Glob exploration instead of prompt stuffing; `cli/internal/phase/phase.go:9-16,56-72` truncates issue bodies, prior outputs, and gate output to bounded sizes. |

### Category 3: Tool Plane (9/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 3.1 | Clear tool contracts | 2 | `cli/internal/catalog/catalog.go:31-49,92-127` defines tool names, parameter types, return format, error conditions, and permission scopes; `docs/workflows.md:95-133` documents workflow phase and gate contracts precisely. |
| 3.2 | Low-overlap tool catalog | 2 | `cli/internal/catalog/catalog.go:191-226` includes overlap detection; the execution plane keeps agent-facing tool exposure narrow via phase-level `allowed_tools` (`docs/workflows.md:64-73,95-108`). |
| 3.3 | Intentional interface choice, not protocol fashion | 2 | The harness deliberately uses CLI providers (`claude`, `copilot`), `gh`, `git`, and shell command phases (`README.md:176-194`, `docs/workflows.md:7-31`), matching local repo automation rather than forcing MCP everywhere. |
| 3.4 | Just-in-time exploration through tools | 2 | Task workflows default to runtime exploration with `gh issue view`, `Grep`, and `Glob` (`workflows/fix-bug/WORKFLOW.md:20-40`; `workflows/implement-feature/WORKFLOW.md:20-44`). |
| 3.5 | Actionable error surfaces and scoped outputs | 1 | Config and workflow validation errors are explicit (`cli/internal/config/config.go:137-238`, `cli/internal/workflow/workflow.go:78-181`); gate failures are surfaced as retry context (`docs/workflows.md:175-198`). However, many command/tool failures still surface mostly as raw command output rather than richer remediation objects. |

### Category 4: Memory & State Layer (8/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 4.1 | Separation of procedural, semantic, and episodic state | 2 | `cli/internal/memory/memory.go:22-39` explicitly models `procedural`, `semantic`, and `episodic` memory; `.xylem/HARNESS.md:56-73` and `CLAUDE.md:77-91` hold procedural rules separately from runtime queue/task state. |
| 4.2 | Structured task artifacts | 2 | Queue state is persisted in JSONL (`docs/architecture.md:68-82`, `cli/internal/queue/queue.go:64-87`); phase outputs are persisted per vessel (`docs/workflows.md:149-162`); sprint contracts, handoffs, and progress files are structured JSON artifacts (`cli/internal/mission/contract.go:35-45,129-166`; `cli/internal/memory/memory.go:306-520`). |
| 4.3 | Startup and resume ritual | 1 | Resume is explicit for waiting vessels via `CurrentPhase`, `WorktreePath`, and `rebuildPreviousOutputs` (`docs/architecture.md:129-135`, `cli/internal/runner/runner.go:194-249`). Cross-session startup guidance exists, but there is not yet one repo-wide enforced “read prior state before acting” ritual across all harness modes. |
| 4.4 | Structured handoffs | 2 | `cli/internal/memory/memory.go:306-377` defines handoff artifacts with completed/failed/unresolved/next steps; `cli/internal/memory/memory.go:379-520` tracks structured progress across sessions. |
| 4.5 | Sanitization, retention, and isolation of persisted state | 1 | Memory values are sanitized and path components are validated (`cli/internal/memory/memory.go:41-61,102-126`); cross-mission access is denied (`cli/internal/memory/memory.go:221-246`). Isolation is good, but there is little evidence of memory retention/expiration policy beyond worktree cleanup (`docs/configuration.md:72-75`). |

### Category 5: Orchestration Layer (9/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 5.1 | Simple-first orchestration | 2 | The main CLI runs sequential workflow phases by default and only switches to orchestrated parallel waves when explicit dependencies exist (`cli/internal/runner/runner.go:242-252`; `cli/internal/runner/schedule.go:25-121`). |
| 5.2 | Explicit task decomposition and completion criteria | 2 | Workflows decompose execution into named phases with gates (`README.md:93-133`, `docs/workflows.md:32-109`); the mission package defines sprint contracts, criteria, and verification steps (`cli/internal/mission/contract.go:13-45`). |
| 5.3 | Generator / evaluator separation when needed | 1 | `cli/internal/evaluator/evaluator.go:127-222` enforces separate generator/evaluator identities and iterative feedback loops. This is implemented well as a library component, but it is not yet integrated into the core CLI workflow runner. |
| 5.4 | Retry limits, failure behaviors, and escalation | 2 | Command gates have bounded retries and delays (`docs/workflows.md:183-226`, `cli/internal/workflow/workflow.go:48-57,257-260`); label gates time out to `timed_out` (`docs/workflows.md:228-248`, `cli/internal/runner/runner.go:128-181`). |
| 5.5 | Delegation used for context isolation or specialization | 2 | `cli/internal/runner/schedule.go:124-149` explicitly narrows visible outputs to declared dependencies in parallel mode; `cli/internal/orchestrator/orchestrator.go:11-180` models specialization/topologies without making multi-agent complexity the default. |

### Category 6: Verification Layer (8/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 6.1 | Deterministic checks | 2 | CI runs `go vet`, `go build`, and `go test` (`.github/workflows/ci.yml:11-27`); workflow command gates support deterministic repo-specific checks (`docs/workflows.md:175-226`). I also verified the current repo with `cd cli && go build ./cmd/xylem && go test ./...` during assessment. |
| 6.2 | Live-system validation | 1 | The harness can run command phases/gates against a live worktree artifact (`docs/workflows.md:78-81,179-226`), but this repository does not include strong in-repo browser/API/end-to-end scenarios for the harness itself. |
| 6.3 | Evidence capture | 2 | The runner persists rendered prompts/commands and outputs under `.xylem/phases/<vessel-id>/` (`docs/workflows.md:149-162`; `cli/internal/runner/runner.go:282-337`), which gives durable execution evidence beyond an agent’s claim. |
| 6.4 | Explicit evidence levels and trust boundaries | 1 | The repo names checkers (`go vet`, `go build`, `go test`, command gates, label gates) in `CLAUDE.md:5-29`, `.github/workflows/ci.yml:25-27`, and `docs/workflows.md:175-248`, but it does not explicitly formalize evidence levels or trust-boundary language when saying something is “verified.” |
| 6.5 | Boundary and contract checks | 2 | Boundary-focused validation is strong: workflow schema validation and dependency-cycle checks (`cli/internal/workflow/workflow.go:78-181,194-247`), queue state-transition enforcement (`cli/internal/queue/queue.go:29-62,158-208`), and property tests for memory/orchestrator behavior (`cli/internal/memory/memory_prop_test.go:28-167`; `cli/internal/orchestrator/orchestrator_prop_test.go:14-180`). |

### Category 7: Evaluation & Observability Layer (2/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 7.1 | Run-level instrumentation | 1 | `cli/internal/observability/tracer.go:14-145` implements OTel tracing and `cli/internal/cost/cost.go:32-71,281-417` implements cost reporting. These are real implementations, but there is little evidence they are wired into the main CLI run loop. |
| 7.2 | Automated evaluation suites | 0 | The repo has extensive software tests, but I found no representative prompt/tool/routing eval suite, trace benchmark set, or scenario dataset used to compare harness behavior over time. |
| 7.3 | Harness-variant comparison | 0 | No evidence of systematic comparison between prompt variants, tool sets, routing policies, or model choices before adopting changes. |
| 7.4 | Evaluator calibration against human judgment | 0 | `cli/internal/evaluator/` exists, but I found no calibration artifacts, examples, or human-labeled comparison sets. |
| 7.5 | Agent-readable observability | 1 | Agents can inspect durable phase outputs and queue state (`docs/workflows.md:149-162`; `cli/internal/queue/queue.go:211-253`), but there is not yet a coherent integrated observability surface exposed through the main CLI path. |

### Category 8: Security & Sandboxing Layer (3/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 8.1 | Workspace and runtime isolation | 1 | Each vessel runs in an isolated git worktree (`README.md:3`, `docs/architecture.md:74-81`, `cli/internal/worktree/worktree.go:94-138`). This is meaningful workspace isolation, but not container/VM-grade containment. |
| 8.2 | Network egress restrictions by default | 0 | No evidence of default network egress restriction. The harness intentionally uses `gh`, `git fetch`, and provider CLIs with ambient network access (`cli/internal/worktree/worktree.go:50-80,105-108`). |
| 8.3 | Protection of instructions, config, and secret-bearing surfaces | 0 | I found no explicit mechanism preventing agents from modifying their governing instruction/config surfaces. `worktree.copyClaudeConfig` allowlists what gets copied (`cli/internal/worktree/worktree.go:146-184`), but that is not the same as mutation protection. |
| 8.4 | Least privilege and scoped authority | 1 | Phase-level `allowed_tools` exists (`docs/workflows.md:64-73,95-108`), repo scope is constrained in `.xylem.yml` (`README.md:46-91`), and `cli/internal/intermediary/intermediary.go:18-275` provides a strong scoped-authority model. The main CLI path, however, does not yet enforce intermediary policies. |
| 8.5 | Human approval and auditability for high-risk actions | 1 | Label gates provide real human approval in feature workflows (`README.md:119-126`; `docs/workflows.md:228-248`), and `cli/internal/intermediary/intermediary.go:62-153,181-229` implements audit logging. Approval/audit are present, but not consistently enforced across all high-risk paths. |

### Category 9: Architectural Enforcement & Entropy Management (10/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 9.1 | Mechanical invariant enforcement | 2 | Queue state transitions are mechanically constrained (`cli/internal/queue/queue.go:29-62,170-177`); workflow/schema invariants are validated (`cli/internal/workflow/workflow.go:78-181`); CI enforces build/test viability (`.github/workflows/ci.yml:11-27`). |
| 9.2 | Invariants over implementations | 2 | The repo encodes correctness properties rather than trivial style: valid phase names for template access, dependency acyclicity, mission/path sanitization, and allowed state transitions (`cli/internal/workflow/workflow.go:14-18,117-177`; `cli/internal/mission/contract.go:50-61`; `cli/internal/queue/queue.go:29-62`). |
| 9.3 | Golden principles encoded in-repo | 2 | `CLAUDE.md:77-91`, `.github/copilot-instructions.md:1-37`, and `.xylem/HARNESS.md:56-73` encode review and implementation principles directly where agents can consume them. |
| 9.4 | Entropy management and cleanup loops | 2 | The product exposes explicit cleanup/compaction mechanisms: `README.md:148-149` documents `xylem cleanup`; `docs/configuration.md:72-75` defines `cleanup_after`; `cli/internal/queue/queue.go:302-317` compacts stale queue records. |
| 9.5 | Legibility-oriented structure and technology choices | 2 | The repo cleanly separates CLI wiring from internal packages and documents the split (`docs/architecture.md:213-252`; `CLAUDE.md:35-76`); Go + YAML + repo-local docs keep the system inspectable. |

### Category 10: Cost & Efficiency Layer (6/10)

| # | Criterion | Score | Evidence |
|---|-----------|-------|----------|
| 10.1 | Run-level token, latency, and cost measurement | 1 | `cli/internal/cost/cost.go:32-71,281-417` tracks token/cost usage and reports by role/purpose/model; `cli/internal/orchestrator/orchestrator.go:110-180,231-280` connects cost tracking to sub-agent orchestration. The gap is operational integration into the primary CLI path. |
| 10.2 | Explicit context budgets and compaction triggers | 2 | `cli/internal/ctxmgr/ctxmgr.go:30-35,124-216` defines explicit token-budget utilization and a `0.95` compaction threshold; `cli/internal/phase/phase.go:9-16` sets hard truncation limits for prompt context components. |
| 10.3 | Caching, prefix stability, or deduplication | 1 | The harness deduplicates queue entries and scanner results (`README.md:23`, `cli/internal/queue/queue.go:98-126`), and phase outputs are reusable on resume (`cli/internal/runner/runner.go:248-249`). I found little evidence of broader caching or reusable prefix optimization. |
| 10.4 | Model routing or ladders by task difficulty | 1 | Workflow/phase/provider override precedence is explicit (`docs/configuration.md:197-234`, `docs/workflows.md:143-144`); `cli/internal/cost/cost.go:306-315` defines a model ladder by role. There is not yet strong evidence of active runtime routing in the core CLI loop. |
| 10.5 | Harness pruning and complexity management | 1 | The repo repeatedly documents that the standalone harness packages are separate from the core CLI and not wired in by default (`README.md:176-194`; `docs/architecture.md:235-252`), which is good complexity discipline. I found less evidence of active simplification/removal over time. |

## Verification Posture

| Claim Area | Strongest Evidence Level | Checker / Method | Trust Boundary Notes | Evidence |
|------------|--------------------------|------------------|----------------------|----------|
| Repository build health | Mechanically checked | `go vet`, `go build` | Confirms compilation/static checks, not end-to-end runtime success in a real target repo. | `.github/workflows/ci.yml:25-27`; assessment run `cd cli && go build ./cmd/xylem` |
| Core behavior of queue/workflow/runner packages | Behaviorally checked | `go test ./...` plus unit/property tests | Good package-level behavioral evidence; still bounded by mocks/stubs rather than full live GitHub/provider execution. | `CLAUDE.md:15-28`; assessment run `cd cli && go test ./...`; `cli/internal/queue/queue_test.go:65-220`; `cli/internal/runner/runner_test.go:24-220` |
| Workflow schema and state-machine invariants | Mechanically checked | Validation logic at load/update time | Covers workflow files and queue transitions accepted by the harness; does not prove user-authored commands are safe or sufficient. | `cli/internal/workflow/workflow.go:78-181`; `cli/internal/queue/queue.go:29-62,158-208` |
| Memory isolation and sanitization | Behaviorally checked | Unit/property tests against mission isolation and sanitization | Strong evidence for the library package itself; not evidence that all CLI persistence uses the same memory subsystem. | `cli/internal/memory/memory.go:102-126,221-246`; `cli/internal/memory/memory_prop_test.go:28-124` |
| Context isolation for dependency-based orchestration | Mechanically checked | Dependency graph + `dependencyOutputs` filtering | Ensures only declared dependency outputs are visible in parallel mode; does not validate the semantic adequacy of the dependency graph. | `cli/internal/runner/schedule.go:25-149` |

## Non-Applicable Criteria

- None. The repository is broad enough that all scorecard criteria were applicable.

## Top 5 Strengths

1. **Exceptional repo legibility and onboarding.** `README.md`, `CLAUDE.md`, `docs/getting-started.md`, and `docs/architecture.md` form a strong progressive-disclosure stack.
2. **Mechanical invariants are encoded in code, not left to folklore.** Queue state transitions, workflow validation, phase dependency checks, and config validation are all enforced in code.
3. **The execution plane is structured and evidence-preserving.** Multi-phase workflows, gates, retries, and persisted phase outputs make agent work inspectable and replayable.
4. **The standalone harness library is unusually complete.** Context management, typed memory, evaluator loops, observability, cost tracking, permissions, and orchestration are implemented as real packages, not just ideas in docs.
5. **The harness is right-sized by default.** The CLI path stays simple and only adds orchestration complexity when explicit workflow dependencies justify it.

## Top 5 Gaps (highest-impact improvements)

1. **Containment is weak.** Git worktrees are useful, but there is no default network egress restriction or stronger runtime sandbox.
2. **Observability and cost tracking are mostly latent.** Good packages exist, but the main CLI run loop does not appear to emit integrated traces/cost data by default.
3. **Harness evaluation is thin.** There are many software tests, but no prompt/tool/model eval suite, variant comparison loop, or evaluator calibration workflow.
4. **Trust-boundary language is imprecise.** The repo names checkers, but does not yet formalize what “verified” means for different claims.
5. **Protection of control surfaces is incomplete.** There is no clear policy/mechanism preventing agents from editing instruction or config files that govern them.

## Recommendations by Priority

### Quick Wins

- Wire `cli/internal/observability` and `cli/internal/cost` into the main `runner` path so every vessel emits trace/cost artifacts by default.
- Add a small “verification levels” section to docs explaining what is mechanically checked vs behaviorally checked vs merely observed.
- Protect critical control files (`.xylem/HARNESS.md`, workflow YAML, provider config) behind an explicit approval path or immutable-copy strategy during task execution.

### Medium-Term

- Add a lightweight harness eval suite: a fixed set of representative repo tasks with tracked outcomes across workflow/prompt/model changes.
- Expose agent-readable observability artifacts through a first-class command or structured directory layout, not just raw phase output files.
- Integrate the intermediary policy/audit model into the CLI execution path for high-risk actions such as push/PR creation and control-file mutation.

### Strategic

- Add stronger containment for autonomous runs: containerized or VM-backed execution, plus intentional network egress policy.
- Decide which standalone harness-library capabilities should graduate into the production CLI path, especially context compaction, handoff artifacts, evaluator loops, and scoped authority.
- Establish a recurring comparison/pruning loop so advanced harness features stay justified and do not remain inert complexity.
