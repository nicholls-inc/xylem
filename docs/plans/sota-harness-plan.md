# Agent Harness Scorecard and Implementation Plan

**Repository:** `xylem`
**Assessed:** 2026-03-31
**Overall Score:** **73/100 — Mature**
**Planning basis:** close the validated state-of-the-art (SoTA) harness gaps first, then add quality multipliers, and only then run experiments.

## Executive framing

xylem already has the hard-to-fake foundations of a serious harness: strong repo legibility, explicit workflows, durable phase artifacts, state-machine discipline, and unusually complete internal packages for context management, memory, observability, cost, evaluator loops, and policy enforcement. The scorecard shows that the main constraint is no longer repo understanding; it is operational trust.

Today, the highest-risk gaps sit in four places: weak containment, incomplete protection of control surfaces, thin run-level observability/evaluation, and imprecise verification trust boundaries. Those gaps matter because they determine whether more autonomy increases throughput or simply increases blast radius. The roadmap below therefore starts with required SoTA non-negotiables — containment, governed control surfaces, observability, verification, and evals — before graduating more of the standalone harness library into the production CLI path.

## Preserved scorecard context

### Score summary

| Category | Score | Max | Plan implication |
|----------|-------|-----|------------------|
| 1. Repository & Knowledge Layer | 10 | 10 | Keep current repo-legibility approach; not a roadmap bottleneck. |
| 2. Context Management Layer | 8 | 10 | Integrate `ctxmgr` and structured reset/handoff into the main CLI path. |
| 3. Tool Plane | 9 | 10 | Preserve clear tool contracts; improve governed execution and richer error surfaces. |
| 4. Memory & State Layer | 8 | 10 | Add startup ritual, retention policy, and CLI-path use of structured artifacts. |
| 5. Orchestration Layer | 9 | 10 | Keep simple-first orchestration; add evaluator separation only where it earns its keep. |
| 6. Verification Layer | 8 | 10 | Formalize evidence levels and add stronger live-system validation. |
| 7. Evaluation & Observability Layer | 2 | 10 | Highest priority operational gap: wire traces, costs, evals, and agent-readable artifacts. |
| 8. Security & Sandboxing Layer | 3 | 10 | Highest priority safety gap: protect control surfaces, add containment, restrict egress. |
| 9. Architectural Enforcement & Entropy Management | 10 | 10 | Preserve current invariant-enforcement discipline; use it to carry new harness rules. |
| 10. Cost & Efficiency Layer | 6 | 10 | Operationalize budgets, routing, compaction, and pruning. |
| **Total** | **73** | **100** | Mature base, but not yet a full SoTA harness. |

### Strengths to build on

1. **Repo legibility is already excellent.** `README.md`, `CLAUDE.md`, workflow docs, and architecture docs provide the progressive-disclosure map that many harnesses lack.
2. **Core invariants are mechanical, not folkloric.** Queue transitions, workflow validation, and dependency checks are encoded in code.
3. **Workflow execution is structured and evidence-preserving.** Phases, gates, retries, and persisted outputs make runs inspectable.
4. **The internal harness library is real, not aspirational.** `ctxmgr`, `memory`, `intermediary`, `observability`, `cost`, and `evaluator` already exist as implementation assets.
5. **The default orchestration posture is disciplined.** xylem stays simple unless explicit workflow dependencies justify more machinery.

### Gap-to-layer mapping

| Scorecard gap | Current evidence | SoTA layer(s) | Why it matters | Covered by |
|---------------|------------------|---------------|----------------|------------|
| Control surfaces are mutable during autonomous execution | Category 8.3 scored 0; critical instruction/config surfaces are not protected | Security & Sandboxing, Tool Plane | An agent can rewrite its own rules or alter provider/workflow behavior mid-run | Workstreams 1-2 |
| Runtime containment is limited to git worktrees | Category 8.1 scored 1 and 8.2 scored 0 | Security & Sandboxing | Worktree isolation does not prevent exfiltration, ambient network use, or spawned-process escape | Workstream 2 |
| Observability and cost tracking are implemented but mostly latent | Category 7.1 scored 1; Category 10.1 scored 1 | Observability, Cost & Efficiency | Without default traces/costs, later evals and budget controls are blind | Workstream 3 |
| Verification language is too vague for completion claims | Category 6.2 scored 1 and 6.4 scored 1 | Verification | “Build passed” remains weaker than “artifact verified against explicit evidence” | Workstream 4 |
| Harness changes are not governed by a representative eval suite | Category 7.2-7.4 scored 0 | Evaluation | Prompt/model/tool/policy changes can regress quality without detection | Workstream 5 |
| Context compiler, handoffs, startup ritual, and retention are not in the main CLI path | Categories 2.2, 2.3, 4.3, and 4.5 are partial | Context Management, Memory & State | Long-running tasks will drift and the strongest library capabilities remain inert | Workstream 6 |
| Evaluator separation exists as a library but is not operationalized | Category 5.3 scored 1 | Orchestration, Evaluation | Subjective quality checks still rely too much on deterministic gates alone | Workstream 7 |
| Agent-readable observability, cost routing, and pruning are incomplete | Categories 7.5 and 10.3-10.5 are partial | Observability, Cost & Efficiency | The harness cannot yet self-debug efficiently or prove advanced features are worth their cost | Workstream 8 |

## Planning guardrails

### Required

These are **validated SoTA defaults / non-negotiables** from the reference spec and are required to claim a full SoTA harness posture in xylem:

- governed control surfaces and high-risk action mediation;
- OS-level or stronger execution containment with egress policy;
- default OpenTelemetry-style observability plus task-level cost telemetry;
- explicit verification contracts and evidence levels;
- a representative harness eval suite for prompt/model/tool/policy changes;
- structured session artifacts and context compilation in the production path.

### Recommended

These are **validated quality multipliers**. They should follow immediately after the non-negotiables, but only once measurement and safety foundations are in place:

- evaluator/generator separation for ambiguous tasks;
- agent-readable observability surfaces;
- model ladders, budget enforcement, and output summarization;
- recurring pruning reviews to remove scaffolding that no longer pays for itself.

## Prioritized roadmap

### Phase 1 — Ship the non-negotiables

| Order | Workstream | Priority | Classification | Main layers | Dependencies |
|------:|------------|----------|----------------|-------------|--------------|
| 1 | Protect control surfaces and mediate high-risk actions | P0 | Required | Security, Tool Plane | None |
| 2 | Add real runtime containment and network/secret policy | P0 | Required | Security, Sandboxing | 1 |
| 3 | Wire run-level observability and cost telemetry into the CLI path | P0 | Required | Observability, Cost | None |
| 4 | Formalize verification contracts, evidence levels, and live validation | P1 | Required | Verification | 3 |
| 5 | Build the harness eval suite, variant-comparison loop, and calibration flow | P1 | Required | Evaluation | 3, 4 |

**Phase 1 exit criteria:** autonomous runs cannot silently rewrite governing surfaces; execution is mediated and meaningfully contained; every vessel emits trace/cost/evidence artifacts; and any harness change can be compared against a representative baseline.

### Phase 2 — Add quality multipliers

| Order | Workstream | Priority | Classification | Main layers | Dependencies |
|------:|------------|----------|----------------|-------------|--------------|
| 6 | Integrate context compilation, structured handoffs, startup ritual, and retention | P2 | Required for full SoTA completion | Context Management, Memory | 3, 4 |
| 7 | Operationalize evaluator/generator separation where deterministic gates are insufficient | P2 | Recommended | Orchestration, Evaluation | 4, 5, 6 |
| 8 | Expose agent-readable observability, add budget/routing controls, and establish pruning reviews | P2 | Recommended | Observability, Cost & Efficiency | 3, 5, 6 |

**Phase 2 exit criteria:** the production runner uses structured state rather than only ad hoc prompt stitching, ambiguous tasks can opt into evaluator loops, and advanced harness features are measured against cost/quality outcomes.

## Detailed workstreams

### Workstream 1 — Protect control surfaces and mediate high-risk actions

- **Classification:** Required, validated SoTA non-negotiable
- **SoTA layers:** Security & Sandboxing; Tool Plane; Architectural Enforcement
- **Desired outcome:** an autonomous run cannot modify the instructions, workflow definitions, or provider-control files that govern it unless a policy explicitly allows that mutation and captures an audit trail.
- **Primary code/doc surfaces:** `cli/internal/runner/runner.go`, `cli/internal/worktree/worktree.go`, `cli/internal/intermediary/`, `cli/internal/config/`, `docs/workflows.md`, `docs/configuration.md`

**Concrete implementation steps**

1. Define the default protected-surface set: `.xylem/HARNESS.md`, `.xylem.yml`, `.xylem/workflows/**`, `.xylem/prompts/**`, provider config copied into worktrees, and any root instruction files used by agents.
2. Add a first-class policy model in config for protected files and high-risk actions (push, PR creation, control-file mutation, external writes), with sensible default deny/require-approval behavior.
3. Integrate `cli/internal/intermediary` into the runner path so high-risk actions are submitted as intents and cannot bypass policy evaluation.
4. Add immutable-copy or hash-verification checks around protected files so unexpected mutations are denied, surfaced, and auditable.
5. Update built-in workflows and docs so PR/push phases explicitly declare when approval is required.

**Dependencies**

- None. This is the first leverage point because it reduces blast radius before adding more autonomy.

**Acceptance criteria**

- Unapproved edits to protected surfaces are denied or moved to an approval state, never silently applied.
- High-risk actions generate intermediary audit entries with policy result and justification.
- Protected-surface policy can be configured, but default behavior is safe-by-default rather than permissive-by-default.

**Suggested evidence/tests**

- Unit tests for policy glob matching, denied/approval/allow flows, and audit-log persistence.
- Runner/worktree tests proving a phase cannot silently mutate `.xylem/HARNESS.md` or workflow YAML.
- A documented operator flow showing how approvals are granted and where the audit trail lives.

### Workstream 2 — Add real runtime containment and network/secret policy

- **Classification:** Required, validated SoTA non-negotiable
- **SoTA layers:** Security & Sandboxing
- **Desired outcome:** xylem executes autonomous work inside an intentionally constrained environment with workspace-only writes, scoped credentials, and explicit network policy instead of ambient host trust.
- **Primary code/doc surfaces:** `cli/internal/worktree/`, `cli/internal/runner/`, `cli/internal/config/`, `README.md`, `docs/configuration.md`, `docs/architecture.md`

**Concrete implementation steps**

1. Introduce a pluggable execution-isolation abstraction so the current worktree model can evolve to containerized execution without rewriting the runner.
2. Ship a near-term containment baseline: per-vessel process/user isolation, read-only mounts for protected surfaces, write access limited to the worktree/state artifacts, and secret injection scoped to the exact tools that need it.
3. Add an egress policy model with a secure default: deny arbitrary outbound access, then allow only the endpoints/protocols required for provider calls, GitHub operations, and explicitly approved integrations.
4. Separate scan-time/networked control-plane behavior from execution-time worktree behavior so the autonomous runtime has a narrower trust envelope than the overall CLI.
5. Document operator escape hatches explicitly (for self-hosted environments, local-only mode, or repos that need broader network access) rather than leaving broad access implicit.

**Dependencies**

- Depends on Workstream 1 to define protected surfaces and high-risk actions that the sandbox must enforce.

**Acceptance criteria**

- A vessel cannot write outside approved workspace/state locations.
- Outbound network access is blocked unless explicitly allowlisted.
- Secrets are not ambient across the whole runtime; they are scoped to the provider/action that needs them.

**Suggested evidence/tests**

- Integration tests or smoke scripts showing denied writes outside the worktree.
- Execution-wrapper tests for egress allow/deny behavior.
- Documentation for supported isolation modes and their trust boundaries.

### Workstream 3 — Wire run-level observability and cost telemetry into the CLI path

- **Classification:** Required, validated SoTA non-negotiable
- **SoTA layers:** Observability; Cost & Efficiency
- **Desired outcome:** every vessel, phase, gate, retry, wait/resume, and reporter action emits structured telemetry and cost data by default.
- **Primary code/doc surfaces:** `cli/internal/runner/`, `cli/internal/observability/`, `cli/internal/cost/`, `cli/internal/reporter/`, `cli/cmd/xylem/`

**Concrete implementation steps**

1. Initialize tracer lifecycle from CLI startup and create spans around drain runs, individual vessels, phase execution, gate execution, wait/resume transitions, and worktree lifecycle.
2. Record consistent attributes for workflow, phase, provider, model, retries, duration, output artifact paths, and terminal state.
3. Bridge provider/model usage into `cli/internal/cost` and persist per-vessel cost reports in the state directory, even when only partial token/cost data is available.
4. Emit a structured per-vessel summary artifact (trace/cost/evidence index) so later workstreams do not need to scrape raw phase files.
5. Add anomaly hooks for budget warnings, repeated gate failures, timeout spikes, and unexpected policy denials.

**Dependencies**

- None for basic tracing; later workstreams depend on this telemetry foundation.

**Acceptance criteria**

- Every vessel creates a traceable run artifact even on failure.
- Cost and latency are visible at least at vessel and phase level.
- Telemetry works with stdout/local development and can be exported to OTLP-compatible backends.

**Suggested evidence/tests**

- Runner tests asserting trace/cost artifacts are emitted for completed, failed, and waiting vessels.
- Snapshot tests for per-vessel summary artifacts.
- Manual sanity check of local stdout traces and configured exporter behavior.

### Workstream 4 — Formalize verification contracts, evidence levels, and live validation

- **Classification:** Required, validated SoTA non-negotiable
- **SoTA layers:** Verification
- **Desired outcome:** xylem stops treating “verified” as a vague status and starts recording what proposition was checked, by which mechanism, with which trust boundary and evidence artifact.
- **Primary code/doc surfaces:** `cli/internal/workflow/`, `cli/internal/reporter/`, `docs/workflows.md`, `README.md`, built-in workflow docs under `workflows/`

**Concrete implementation steps**

1. Define a verification evidence model aligned to the spec: `proved`, `mechanically_checked`, `behaviorally_checked`, and `observed_in_situ`.
2. Extend workflow/report artifacts so phases and final vessel results can declare expected evidence type, checker/method, trust boundary, and evidence path.
3. Update built-in workflows to distinguish deterministic command gates from live-system checks and to capture richer evidence where runtime behavior matters.
4. Add a small verification-levels section to docs and reporter output so operators can tell the difference between “compiled,” “tested,” and “observed running.”
5. Use the new evidence manifest in completion comments/status output so ambiguous claims are replaced with explicit verification facts.

**Dependencies**

- Depends on Workstream 3 because evidence should reference the artifacts and telemetry emitted by the runtime.

**Acceptance criteria**

- Final run artifacts include an evidence manifest naming checker, evidence level, trust boundary, and file path.
- Built-in workflows document which claims require live validation versus deterministic checks.
- Reporter output no longer uses an undifferentiated notion of “verified.”

**Suggested evidence/tests**

- Unit tests for evidence-manifest generation and reporter formatting.
- Workflow-validation tests ensuring declared evidence metadata is structurally valid.
- Documentation examples showing the same task reported at different evidence levels.

### Workstream 5 — Build the harness eval suite, variant-comparison loop, and calibration flow

- **Classification:** Required, validated SoTA non-negotiable
- **SoTA layers:** Evaluation
- **Desired outcome:** prompt, model, tool, routing, or policy changes are compared against a representative benchmark set before they are adopted.
- **Primary code/doc surfaces:** new eval fixtures under repo state or test data, `cli/internal/evaluator/`, `cli/cmd/xylem/`, CI workflow/docs

**Concrete implementation steps**

1. Define a small but durable evaluation corpus representing xylem’s real workload: scan/drain behavior, workflow execution, waiting/resume, failure recovery, plan-quality tasks, and PR/reporting paths.
2. Create a result schema that tracks task success, latency, cost, retries, tool failures, policy violations, and evidence quality.
3. Add a reproducible command or CI path that runs the corpus against a baseline and a candidate harness variant.
4. Build a human-calibrated rubric for ambiguous outputs (for example plan quality or PR/report quality), then use `cli/internal/evaluator` where deterministic checks are insufficient.
5. Make harness changes conditional on comparison output: prompts, tool contracts, routing, model defaults, and policy changes should all produce an eval report.

**Dependencies**

- Depends on Workstreams 3 and 4 so the eval suite can consume real telemetry and evidence rather than ad hoc logs.

**Acceptance criteria**

- The repo contains a documented, reproducible eval flow with representative scenarios.
- Baseline-versus-candidate comparison is possible for prompts/models/tooling/policies.
- At least one ambiguous quality dimension is calibrated against human judgment rather than self-grading alone.

**Suggested evidence/tests**

- A committed eval schema and scenario manifest.
- CI or scripted regression output comparing two harness variants.
- A small set of human-labeled examples used to validate evaluator behavior.

### Workstream 6 — Integrate context compilation, structured handoffs, startup ritual, and retention

- **Classification:** Required for full SoTA completion, but sequenced after control and measurement foundations
- **SoTA layers:** Context Management; Memory & State
- **Desired outcome:** the production runner uses explicit context assembly, durable structured artifacts, and resume-aware startup behavior instead of relying primarily on raw prompt files and prior phase outputs.
- **Primary code/doc surfaces:** `cli/internal/phase/`, `cli/internal/ctxmgr/`, `cli/internal/memory/`, `cli/internal/runner/`, `docs/architecture.md`, `docs/workflows.md`

**Concrete implementation steps**

1. Define the durable artifact set per vessel: plan, progress, unresolved items, feature/checkpoint list, verification state, and last known operator approvals.
2. Integrate `cli/internal/ctxmgr` into phase assembly so each provider call gets a compiled context view with selection, compaction, and dependency-scoped inputs.
3. Add structured handoff artifacts for long-running or resumed work so the runner can reset context safely without losing state.
4. Enforce a startup/resume ritual: before a resumed phase runs, the harness must load structured prior state and rebuild the context manifest deliberately.
5. Add retention/expiration rules for structured artifacts so cleanup preserves what future runs need and expires what no longer has operational value.
6. Upgrade tool/gate failure persistence from mostly raw text blobs toward structured remediation objects when feasible.

**Dependencies**

- Depends on Workstreams 3 and 4 so context/handoff artifacts can include telemetry and verification state.

**Acceptance criteria**

- Prompt assembly produces an inspectable context manifest rather than only opaque rendered prompts.
- Resumed runs consume structured handoff artifacts instead of relying solely on historical files and queue fields.
- Retention behavior is documented and tested.

**Suggested evidence/tests**

- Unit tests for `ctxmgr` integration and compaction thresholds in the runner path.
- Resume tests showing correct reconstruction of context from handoff artifacts.
- Artifact-schema tests for structured progress/handoff documents.

### Workstream 7 — Operationalize evaluator/generator separation where deterministic gates are insufficient

- **Classification:** Recommended, validated quality multiplier
- **SoTA layers:** Orchestration; Evaluation
- **Desired outcome:** xylem uses separate generator and evaluator roles for ambiguous outputs instead of assuming deterministic gates alone are enough.
- **Primary code/doc surfaces:** `cli/internal/evaluator/`, `cli/internal/runner/`, workflow docs/templates

**Concrete implementation steps**

1. Identify the workflows/phases where deterministic checks are weakest: plan generation, review synthesis, PR narratives, and other artifact-quality tasks.
2. Add workflow-level opt-in for evaluator loops so the feature is used intentionally rather than globally.
3. Feed evaluator criteria from the human-calibrated rubric created in Workstream 5.
4. Persist evaluator history and rejection reasons as first-class artifacts so failures are inspectable and reusable.
5. Keep the separation honest: different evaluator/generator identities, bounded iteration counts, and explicit stop conditions.

**Dependencies**

- Depends on Workstreams 4, 5, and 6.

**Acceptance criteria**

- At least one ambiguous workflow can run with a generator/evaluator loop and emit loop history.
- Evaluator results are tied to explicit criteria and not just a freeform thumbs-up.
- The feature remains opt-in and measured against cost/quality impact.

**Suggested evidence/tests**

- Runner integration tests for evaluator-enabled phases.
- Evaluator-loop snapshots showing criteria, iterations, and terminal decision.
- Comparison data proving the loop improves quality enough to justify cost on targeted workflows.

### Workstream 8 — Expose agent-readable observability, add budget/routing controls, and establish pruning reviews

- **Classification:** Recommended, validated quality multiplier
- **SoTA layers:** Observability; Cost & Efficiency
- **Desired outcome:** agents and operators can inspect the same run artifacts, budgets become enforceable, and advanced harness components must keep proving their value.
- **Primary code/doc surfaces:** `cli/internal/cost/`, `cli/internal/observability/`, `cli/cmd/xylem/`, `cli/internal/runner/`, docs and operational playbooks

**Concrete implementation steps**

1. Expose a first-class agent-readable artifact surface — either a structured directory contract or CLI command — for traces, cost reports, evidence manifests, and policy/audit events.
2. Wire budget configuration into execution so vessels can warn, require approval, or fail when cost/token thresholds are exceeded.
3. Add a simple model ladder/routing policy based on workflow phase or task difficulty before attempting dynamic routing.
4. Summarize verbose tool output and gate failures into structured artifacts so context and cost stay bounded.
5. Establish a recurring pruning review: compare advanced features (`ctxmgr`, evaluator loops, extra routing, sandbox modes) against actual eval and cost data, then remove or simplify what no longer pays for itself.

**Implementation note:** the first slice should ship as a read-only review loop built on persisted run artifacts. Use `summary.json` as the index plus `evidence-manifest.json`, `cost-report.json`, `budget-alerts.json`, and optional `quality-report.json`, then expose the aggregate through `xylem review` and best-effort post-drain regeneration into `.xylem/reviews/harness-review.{json,md}`.

**Dependencies**

- Depends on Workstreams 3, 5, and 6. Budget/routing decisions are only useful once telemetry and eval data exist.

**Acceptance criteria**

- Agents can consume structured run artifacts without scraping raw output files.
- Budget events are visible and enforceable.
- Routing/pruning decisions are documented against measured outcomes rather than intuition.

**Suggested evidence/tests**

- CLI/output tests for the agent-readable artifact surface.
- Cost-budget tests for warn/deny/approval behavior.
- Periodic review template showing which features were retained, simplified, or removed and why.

## Coverage of all meaningful scorecard gaps

| Scorecard category with gap | Concrete gap(s) addressed | Planned workstream(s) |
|----------------------------|---------------------------|-----------------------|
| 2. Context Management | CLI path lacks integrated compaction/reset/handoff | 6 |
| 3. Tool Plane | High-risk actions are insufficiently governed; many failures surface as raw output | 1, 6, 8 |
| 4. Memory & State | Startup ritual, retention/expiration policy, and structured operational state are partial | 6 |
| 5. Orchestration | Generator/evaluator separation exists but is not wired into production paths | 7 |
| 6. Verification | Live-system validation and explicit evidence levels are incomplete | 4 |
| 7. Evaluation & Observability | Telemetry, eval suites, variant comparison, calibration, and agent-readable observability are thin | 3, 5, 8 |
| 8. Security & Sandboxing | Containment, egress restrictions, protected surfaces, least privilege, and mediated approvals are incomplete | 1, 2 |
| 10. Cost & Efficiency | Run-level cost integration, routing, summarization, and pruning are incomplete | 3, 8 |

## Sequencing notes

- **Do not start with multi-agent cleverness.** The validated SoTA spec is clear that containment, verification, and observability create more value than premature orchestration complexity.
- **Use the internal library as an implementation source, not as justification by existence.** `ctxmgr`, `memory`, `intermediary`, `observability`, `cost`, and `evaluator` should graduate into the CLI only when they are wired, tested, and measured.
- **Treat Categories 1 and 9 as assets to preserve.** Repo legibility and invariant enforcement are already strengths; future work should extend those habits rather than replacing them.
- **Keep the roadmap validated.** This plan intentionally excludes prediction-based work so sequencing stays tied to current, evidence-backed SoTA requirements.

## Final position

xylem does not need a fresh architecture; it needs operationalization of the strongest pieces it already has. The plan above turns the scorecard from “mature but uneven” into a dependency-ordered build program: first lock down control surfaces and containment, then instrument and verify everything, then govern harness changes with evals, and only after that graduate the context/memory/evaluator/cost multipliers into the production path. That is the shortest path from the current 73/100 state to a defensible SoTA harness.
