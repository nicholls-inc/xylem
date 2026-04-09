# 10 Principles Alignment for xylem

**Status:** Proposed
**Date:** 2026-04-09
**Parent issue:** #156

## Purpose

Issue [#156](https://github.com/nicholls-inc/xylem/issues/156) is a framework issue, not a direct implementation ticket. This note maps each of the ten principles to the current xylem codebase, records the evidence for that assessment, and identifies whether the next step is:

1. already covered by an existing issue,
2. a new recurring-vessel follow-up, or
3. a standards/documentation gap.

The common dependency for every new recurring vessel below is [#150](https://github.com/nicholls-inc/xylem/issues/150), which adds the missing scheduled source primitive. The four new follow-up issues required by #156 have now been filed and labeled `enhancement` and `ready-for-work`.

## Alignment summary

| Principle | Current state in xylem | Evidence | Outcome |
| --- | --- | --- | --- |
| **1. Hardening** | **Partially adopted.** xylem already replaces some high-risk prompt-only behavior with deterministic policy and surface checks. | `cli/internal/intermediary/intermediary.go`, `cli/internal/config/config.go`, `cli/internal/runner/runner.go`, parent issue #156, hardening issue #155 | Keep tracked under #155 rather than opening a new issue here. |
| **2. Context Hygiene** | **Dormant building blocks, not yet wired into the runner.** `ctxmgr` exists, but phase assembly still reads a prompt file, renders it, optionally prepends `HARNESS.md`, and sends it straight to the provider. | `cli/internal/ctxmgr/ctxmgr.go`, `cli/internal/runner/runner.go`, context integration issue #60 | Keep runner integration in #60 and track the recurring **context-weight audit** vessel in #160. |
| **3. Living Documentation** | **Docs are authoritative, but freshness is not enforced.** The SoTA docs explicitly call for doc gardeners, but the scanner still has no scheduled source and the live docs already disagree with the current default policy. | `docs/design/sota-agent-harness-spec.md`, `docs/best-practices/harness-engineering.md`, `docs/configuration.md`, `cli/internal/config/config.go`, `cli/internal/scanner/scanner.go`, scheduled source issue #150 | Track the recurring **doc-garden** vessel in #158. |
| **4. Disposable Blueprint** | **Phase artifacts exist, but disposable plan artifacts do not.** The runner persists prompts and outputs and can rebuild prior outputs, but there is no explicit `plan.md` or handoff artifact that downstream phases consume as the latest revision. | `cli/internal/runner/runner.go`, context/handoff issue #60 | Fold this into #60 instead of creating a separate vessel issue. |
| **5. Institutional Memory** | **Static memory exists; recurring synthesis does not.** xylem protects `.xylem/HARNESS.md`, supports recurring harness reviews, and documents the need for institutional memory, but it does not mine failed runs into new negative constraints. | `cli/internal/config/config.go`, `cli/cmd/xylem/review.go`, `docs/reports/harness-scorecard-report.md`, parent issue #156 | Track the recurring **lessons** vessel in #159. |
| **6. Specialized Review** | **Designed but unwired.** The evaluator package already models separate generator/evaluator roles, but the runner does not execute it yet. | `cli/internal/evaluator/evaluator.go`, evaluator integration issue #151 | Already covered by #151. |
| **7. Observability Imperative** | **Active workstream already underway.** xylem has an observability package and run artifacts, and the open roadmap already tracks the remaining OTel work. | `cli/internal/observability/observability.go`, `docs/design/xylem-harness-impl-spec.md`, parent issue #156 references #52, #91, and #137 | No new issue from #156. |
| **8. Strategic Human Gate** | **Mechanism exists, policy posture is unresolved.** `RequireApproval` is implemented, but the default policy allows `git_push` and `pr_create`, while `docs/configuration.md` still claims those actions require approval by default. | `cli/internal/intermediary/intermediary.go`, `cli/internal/config/config.go`, `cli/internal/runner/runner.go`, `docs/configuration.md` | Track the **human-gate policy audit** in #161. |
| **9. Token Economy** | **Cost and budget primitives exist; richer routing is deferred.** Budget config exists, per-phase token estimates are recorded, and the remaining work is already scoped. | `cli/internal/config/config.go`, `cli/internal/runner/runner.go`, issues #55 and #59 | No new issue from #156. |
| **10. Toolkit** | **Mostly aligned in shape, blocked on recurring scheduling.** xylem already prefers CLI/config surfaces for repeatable behavior, but recurring maintenance vessels still depend on scheduled sources. | `cli/cmd/xylem/review.go`, `cli/internal/intermediary/intermediary.go`, `cli/internal/scanner/scanner.go`, issues #150 and #155 | No separate issue beyond #150 and #155. |

## Principle-by-principle notes

### 1. Hardening

xylem has already started moving recurring safety-sensitive behavior out of prompt-only execution and into deterministic controls:

- `cli/internal/intermediary/intermediary.go` gives the runner an explicit action-policy boundary with `allow`, `deny`, and `require_approval`.
- `cli/internal/config/config.go` installs protected-surface rules by default.
- `cli/internal/runner/runner.go` snapshots protected surfaces and records audit evidence on drift.

That is enough evidence to treat hardening as an active workstream rather than a gap that needs another framework issue. The remaining hardening backlog should stay consolidated under #155.

### 2. Context Hygiene

`cli/internal/ctxmgr/ctxmgr.go` already models context as compiled segments with selection, compaction, and isolation strategies. The runner does not use it yet. Instead, `cli/internal/runner/runner.go` still:

- reads a prompt file,
- renders it directly,
- optionally prepends `.xylem/HARNESS.md`, and
- passes the result to the provider CLI.

That keeps the core integration work anchored in #60. The new work from #156 is the **recurring context-weight audit vessel** in #160: once #150 exists, xylem should regularly flag workflows whose average prompt and output footprint suggests degraded context hygiene.

### 3. Living Documentation

The docs already say this principle matters:

- `docs/design/sota-agent-harness-spec.md` calls for recurring doc gardeners as part of entropy management.
- `docs/best-practices/harness-engineering.md` calls out a dedicated doc-gardening agent.

The implementation gap is concrete:

- `cli/internal/scanner/scanner.go` only constructs event-driven sources today, so no recurring vessel can run yet.
- `docs/configuration.md` still says the default policy requires approval for `git_push` and `pr_create`, but `cli/internal/config/config.go` now allows both by default.

That combination makes **doc-garden** (#158) the clearest recurring-vessel follow-up from this issue.

### 4. Disposable Blueprint

The runner already writes phase artifacts under `.xylem/phases/<vessel>/`, but they are limited to current prompt/output style artifacts and summary data. `cli/internal/runner/runner.go` also rebuilds prior outputs from `<phase>.output` files only. There is no explicit, versioned, disposable `plan.md` handoff artifact that downstream phases treat as the current blueprint.

This belongs with the broader structured-handoff work already scoped in #60. It does not need its own recurring vessel issue.

### 5. Institutional Memory

xylem has the beginnings of institutional memory:

- `.xylem/HARNESS.md` is treated as a protected control surface in `cli/internal/config/config.go`.
- `xylem review` already supports recurring harness-review generation (`cli/cmd/xylem/review.go`).

What is missing is the recurring negative-constraint capture loop described in #156: mining recent `xylem-failed` vessels for repeat failures and turning them into explicit “Do Not” guidance with rationale. That is distinct from the existing harness review and warrants the dedicated **lessons** vessel issue in #159.

### 6. Specialized Review

`cli/internal/evaluator/evaluator.go` already models generator/evaluator separation, scoring, and bounded retry loops. The missing piece is runner integration, and that work is already explicitly tracked by #151. No additional framework issue is needed here.

### 7. Observability Imperative

`cli/internal/observability/observability.go` and `docs/design/xylem-harness-impl-spec.md` show that run-level observability is already treated as a first-class workstream. Issue #156 correctly treats this principle as covered by the observability backlog rather than as a new initiative.

### 8. Strategic Human Gate

The code already supports human gates mechanically:

- `cli/internal/intermediary/intermediary.go` implements `RequireApproval`.
- `cli/internal/runner/runner.go` classifies phase intents such as `git_push` and `pr_create` and turns `require_approval` decisions into vessel failures with explicit messaging.

The unresolved question is policy, not plumbing. `cli/internal/config/config.go` currently allows all actions other than protected-surface writes by default, while `docs/configuration.md` still documents the older approval-heavy posture. The follow-up **human-gate policy audit** in #161 should decide the intended approval set for destructive git actions, PR creation, deploy-class actions, and similar high-blast-radius operations.

### 9. Token Economy

Budget and cost primitives already exist:

- `cli/internal/config/config.go` exposes budget configuration.
- `cli/internal/runner/runner.go` records per-phase estimated token and cost usage and can fail a vessel when the budget is exceeded.

The remaining work — richer artifact surfacing and model ladders — is already captured by #55 and #59.

### 10. Toolkit

xylem is already toolkit-oriented in structure: the queue, scanner, runner, gates, review command, and intermediary policy layer all encode repeatable behavior into CLI/config surfaces. The remaining gap is not that the project relies too heavily on prose; it is that recurring maintenance work still needs the scheduled-source primitive from #150.

## New follow-up issues required by #156

Issue #156 now leaves behind exactly four new follow-ups, all already opened with the labels `enhancement` and `ready-for-work`:

1. #158 — `doc-garden` scheduled vessel
2. #159 — `lessons` scheduled vessel
3. #160 — `context-weight audit` scheduled vessel
4. #161 — human-gate policy audit

Those four issues are the delta that is not already covered by #60, #150, #151, #55, #59, #155, or the active observability backlog.
