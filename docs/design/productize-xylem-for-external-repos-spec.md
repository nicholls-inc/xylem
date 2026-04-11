# Productize Xylem for External Repositories Spec

**Status:** Proposed
**Date:** 2026-04-09
**Scope:** Generalize xylem for use on arbitrary repositories via a layered profile system, workflow classes, an adapt-repo workflow, and PR-gated self-modification — without losing the xylem-on-xylem self-hosting loop.
**Inputs:** Research note "using xylem on other projects without losing xylem-on-xylem" (2026-04-09); [SoTA Agent Harness Spec](sota-agent-harness-spec.md); [Xylem Harness Impl Spec](xylem-harness-impl-spec.md); [Failed Vessel Recovery Spec](failed-vessel-recovery-spec.md); [10 Principles Alignment](10-principles-alignment.md)

## 1. Purpose

xylem is currently optimized for a single tenant: the `nicholls-inc/xylem` repository that hosts it. `xylem init` scaffolds a minimal two-workflow harness (`cli/cmd/xylem/init.go:58-67`), but the real production control plane lives in-tree under `.xylem/` and is tracked via root `.gitignore` overrides (lines 12-24 of `.gitignore`). Every richer workflow — `triage`, `refine-issue`, `review-pr`, `merge-pr`, `resolve-conflicts`, `lessons`, `implement-harness`, `sota-gap-analysis`, `unblock-wave`, `fix-pr-checks`, `respond-to-pr-review`, `diagnose-failures` — exists only as a checked-in artifact in the xylem repo's own `.xylem/workflows/` directory, not in anything `xylem init` emits. A new user running `xylem init` on an arbitrary repository gets `fix-bug` and `implement-feature` and nothing else.

This spec defines a path from that single-tenant state to a product shape where:

1. `xylem init` in an arbitrary repository MUST produce a working harness the daemon can run immediately.
2. xylem MUST adapt that harness to the target repository's stack, tooling, and validation commands within the first daemon cycle, via a reviewable PR.
3. xylem MAY continue to self-modify its own control plane (`.xylem.yml`, `.xylem/HARNESS.md`, `.xylem/workflows/*.yaml`, `.xylem/prompts/*/*.md`), but only through a PR-gated path and only when the running workflow is explicitly authorized to do so.
4. The xylem-on-xylem self-hosting loop (`sota-gap-analysis`, `implement-harness`, `unblock-wave`, daemon auto-upgrade, auto-merge) MUST remain intact and MUST be recoverable as an opt-in overlay.

The mechanism is a **three-layer architecture**: a `core` profile shipped in the binary, an `adapt-repo` workflow that personalizes that profile to the target repo, and a `self-hosting-xylem` overlay that re-enables xylem-specific workflows for the xylem repository itself. Sections 5-7 cover the architecture and the first two layers; sections 8-18 (separate drafts) cover the adapt-repo workflow, workflow classes, daemon reload, PR lifecycle triggers, recovery integration, observability, security, cost, migration, testing, rollout, and open questions.

## 2. Goals

1. **Immediate usability.** `xylem init` on a blank target repository MUST produce enough control-plane state that `xylem daemon` can run its scan-drain loop against real GitHub issues/PRs without further manual edits.
2. **First-cycle adaptation.** The first daemon tick against a freshly-initialized repository MUST seed an `xylem-adapt-repo` issue that, when drained, produces a reviewable PR updating the harness to match the detected stack.
3. **PR-gated self-modification.** Any vessel that edits `.xylem/HARNESS.md`, `.xylem.yml`, `.xylem/workflows/*.yaml`, or `.xylem/prompts/*/*.md` MUST do so only via a branch that reaches the default branch through a reviewed PR. Direct commits to the default branch from a `harness-maintenance`-class vessel MUST be rejected by the runner.
4. **Preserved self-hosting.** The xylem repository MUST retain every workflow, source, and daemon behavior it has today (auto-upgrade, auto-merge, sota-gap-analysis, implement-harness, unblock-wave) by opting into a `self-hosting-xylem` overlay.
5. **Tracked control plane.** External repositories MUST track `HARNESS.md`, workflows, prompts, and profile metadata under source control, and MUST NOT track runtime state (queue JSONL, phase outputs, schedules, reviews, traces, locks).
6. **No xylem-branded defaults in core.** Label taxonomies (`harness-impl`, `xylem-failed`, `pr-vessel-active`, `harness-post-merge`, `[sota-gap]`), the hard-coded `nicholls-inc/xylem` repo slug in `merge-pr`, Go-only validation gates in `fix-pr-checks` / `resolve-conflicts`, and daemon auto-upgrade MUST NOT appear in the generic `core` profile.
7. **Validation surface.** `xylem init` output MUST pass `xylem config validate` and `xylem workflow validate` (new commands) on every fixture in the test matrix (§15).

## 3. Non-goals

- This spec does **not** propose changes to the vessel state machine, the JSONL queue format, or the `source.Source` interface.
- This spec does **not** propose first-class support for non-GitHub forges (GitLab, Gitea, Bitbucket). GitHub remains the only supported source backend in v1.
- This spec does **not** propose multi-tenant daemons. A running `xylem daemon` continues to own exactly one repository's control plane.
- This spec does **not** adapt the output of `xylem init` to the target repository at init time. Adaptation happens via the subsequent `adapt-repo` workflow, not via interactive prompts or repository introspection inside the `init` command.
- This spec does **not** redesign the workflow YAML format beyond the minimal fields required for classes and overlays. Existing workflow files continue to parse unchanged.
- This spec does **not** propose a UI; everything stays CLI + YAML + in-repo control plane.
- This spec does **not** replace the `lessons` workflow, the `review` command, or the `failure-review.json` artifact introduced by the Failed Vessel Recovery Spec Phase 1 (already landed on `origin/main` as of commit ad92e03).

## 4. Current baseline (what exists on origin/main today)

This section enumerates the primitives xylem has shipped and where they live in the tree. Every proposed change in §5-§7 and later sections is framed against this baseline. Where the source research note makes claims that diverge from reality, this section is the source of truth.

### 4.1 What is already wired and production-ready

The following internal packages are imported by the runner, the drain path, or a top-level CLI command, and are exercised every time a vessel runs:

| Package | Entry point | Role |
| --- | --- | --- |
| `orchestrator` | `cli/internal/runner/runner.go:27`; `schedule.go` | Sub-agent topology and scheduling primitives consumed by the runner |
| `cost` | `cli/internal/runner/runner.go:21`; `cli/internal/runner/summary.go:13` | Token/cost tracking and budget enforcement per vessel |
| `intermediary` | `cli/internal/runner/drain.go:131` | Glob-based intent policy evaluation; rules come from `.xylem.yml` |
| `observability` | `cli/internal/runner/drain.go:15` | OpenTelemetry spans for drain/vessel/phase; tracer is a `Runner` struct field |
| `evidence` | `cli/internal/runner/runner.go:23` | Per-vessel claim manifests persisted alongside phase outputs |
| `surface` | `cli/internal/runner/runner.go:33` | Before/after file snapshots used by `Runner.verifyProtectedSurfaces` (`cli/internal/runner/runner.go:2694`) |
| `recovery` (Failed Vessel Recovery Phase 1) | `cli/internal/recovery/recovery.go` | `failure-review.json` artifact persisted per terminal vessel |

The `failure-review.json` artifact landed via commit ad92e03 / PR #219 and is now the first-class recovery record for every `failed` / `timed_out` vessel. Phases 2-4 of the Failed Vessel Recovery Spec remain unimplemented; external rollout (§14-§18 in the larger spec) MUST assume only Phase 1 semantics.

### 4.2 What is dormant but present

The following packages exist in `cli/internal/` but are **not** imported by the runner, the scanner, or any top-level CLI command (`cli/cmd/xylem/root.go:82-101`). They compile, have tests, and could be wired — but today they are library code only:

| Package | Notable surface | Why it matters for this spec |
| --- | --- | --- |
| `bootstrap` | `AnalyzeRepo`, `AuditLegibility`, `DetectLanguages`, `DetectFrameworks`, `DetectBuildTools`, `DiscoverEntryPoints`, `InstructionSet` | Dormant dependency of the proposed `adapt-repo` workflow; no `cli/cmd/xylem/bootstrap.go` exists |
| `mission` | Task decomposition, sprint contracts, persona constraints | Reserved for future orchestrator wiring |
| `memory` | Typed procedural/semantic/episodic memory, KV store, scratchpads | Reserved |
| `signal` | Repetition, tool failure rate, context thrash heuristics | Reserved |
| `evaluator` | Generator-evaluator loops with signal-gated intensity | Reserved for evaluator-optimizer phase in `adapt-repo` |
| `ctxmgr` | Context window compaction strategies | Reserved |
| `catalog` | Tool catalog with overlap detection and permission scopes | Reserved for `adapt-repo` tool discovery |

The package named `policy` referenced in the research note does **not** exist. Any class/policy enforcement proposal MUST create `cli/internal/policy/` as a new package; it MUST NOT assume the package is already on disk.

### 4.3 What is missing entirely

The following capabilities are assumed by later sections of this spec and do not exist on `origin/main`:

- **Embedded profile filesystem.** `cli/internal/profiles/` does not exist. `xylem init` writes workflow YAML and prompt Markdown from inline Go string constants (`cli/cmd/xylem/init.go:56-67`), not from `embed.FS`.
- **Workflow class taxonomy.** The `Workflow` struct (`cli/internal/workflow/workflow.go:21-29`) has fields `Name`, `Description`, `LLM`, `Model`, `AllowAdditiveProtectedWrites`, `AllowCanonicalProtectedWrites`, and `Phases`. There is **no** `Class` field. The two `Allow*` booleans are today's per-workflow knobs for harness-maintenance; § 5.5 treats them as the implicit precursor to a formal class taxonomy.
- **`adapt-repo` workflow.** No such workflow exists in `.xylem/workflows/`; no such prompt directory exists in `.xylem/prompts/`.
- **Daemon reload.** `cli/cmd/xylem/daemon.go` has no generic config reload path. Binary auto-upgrade uses `exec()` and is xylem-specific (`cli/cmd/xylem/upgrade.go`).
- **Bootstrap CLI surface.** There is no `xylem bootstrap analyze-repo` or `xylem bootstrap audit-legibility` command. The functions exist inside the `bootstrap` package but are not reachable from any `cli/cmd/xylem/*.go` entry point.
- **`pr_opened` / `pr_head_updated` triggers.** `github-pr-events` supports `review_submitted`, `checks_failed`, and `commented` (`cli/internal/config/config.go:109-122`) but cannot fire on a newly opened PR or a head-SHA update.
- **Parameterized PR workflows.** `merge-pr`, `fix-pr-checks`, and `resolve-conflicts` hard-code the `nicholls-inc/xylem` repo slug and Go-specific validation commands inside workflow YAML and prompt Markdown.
- **`xylem config validate` / `xylem workflow validate` / `xylem doctor` / `xylem audit` / `xylem bootstrap` / `xylem adapt-repo` / `xylem daemon reload`.** None exist. `cli/cmd/xylem/root.go:82-101` registers `init`, `dtu`, `shim-dispatch`, `scan`, `drain`, `review`, `gap-report`, `lessons`, `enqueue`, `status`, `pause`, `resume`, `cancel`, `cleanup`, `daemon`, `retry`, `visualize`, `version` — and nothing else.

### 4.4 What `xylem init` produces today vs what external repos need

Today (`cli/cmd/xylem/init.go:17-94`):

| Artifact | Produced by init? | Source |
| --- | --- | --- |
| `.xylem.yml` (scaffold config) | Yes | `writeScaffoldConfig`, inline string |
| `.xylem/` directory | Yes | `os.MkdirAll(defaultStateDir, 0o755)` (`init.go:44`) |
| `.xylem/.gitignore` = `"*\n!.gitignore\n"` | Yes (unconditional, every run) | `init.go:49-53` |
| `.xylem/HARNESS.md` | Yes | `harnessContent` inline constant |
| `.xylem/workflows/fix-bug.yaml` | Yes | `fixBugWorkflowContent` inline constant |
| `.xylem/workflows/implement-feature.yaml` | Yes | `implementFeatureWorkflowContent` inline constant |
| `.xylem/prompts/fix-bug/{analyze,plan,implement,pr}.md` | Yes | `promptContent(workflow, phase)` |
| `.xylem/prompts/implement-feature/{analyze,plan,implement,pr}.md` | Yes | `promptContent(workflow, phase)` |
| Any `triage`, `refine-issue`, `review-pr`, `merge-pr`, `resolve-conflicts`, `fix-pr-checks`, `respond-to-pr-review`, `lessons`, `adapt-repo`, `context-weight-audit`, `workflow-health-report` workflow | **No** | — |
| `.xylem/profile.lock` | **No** | — |
| `AGENTS.md` stub | **No** | — |
| Seeded `xylem-adapt-repo` issue | **No** | — |

The `.xylem/.gitignore` default — `*\n!.gitignore\n` — hides the entire `.xylem/` subtree from git. For the xylem repository this is harmless because root `.gitignore` lines 12-24 override the hide-all with explicit allowlist entries for `HARNESS.md`, `eval/`, `prompts/`, `workflows/`, and `state/sota-gap-snapshot.json`. In an external repository with no such root-level override, the default actively **prevents** the control plane from being tracked, which is the opposite of what a productized harness needs.

### 4.5 Where xylem-specific assumptions are hard-coded

The following assumptions appear in checked-in files and MUST be parameterized or relocated into the `self-hosting-xylem` overlay before external rollout:

| Assumption | Location | Notes |
| --- | --- | --- |
| Repo slug `nicholls-inc/xylem` | `.xylem.yml` lines 7, 20, 33, 48, 61, 79, 101, 115, 127, 141; workflow YAML / prompt Markdown | Every source declares `repo: nicholls-inc/xylem` |
| Go-only gates (`goimports -l .`, `go vet ./...`, `go build ./cmd/xylem`, `go test ./...`) | `.xylem/workflows/fix-pr-checks.yaml`, `.xylem/workflows/resolve-conflicts.yaml` | Repo-declared validation commands MUST replace these |
| Label taxonomy: `harness-impl`, `xylem-failed`, `pr-vessel-active`, `pr-vessel-merged`, `harness-post-merge`, `[sota-gap]`, `ready-to-merge + harness-impl` combos | `.xylem.yml` lines 64-143 | Generic labels in §6.2 replace these in core |
| Branch naming (`xylem/<issue>-<slug>`) | Source implementations, `merge-pr` workflow, `resolve-conflicts` workflow | Overlay-only |
| `daemon.auto_upgrade: true` | `.xylem.yml:153` | Overlay-only; relies on `upgrade.go` pulling `origin/main` for `github.com/nicholls-inc/xylem` |
| `daemon.auto_merge: true` + `auto_merge_repo: nicholls-inc/xylem` | `.xylem.yml:154-155` | Overlay-only |
| Ten source blocks (`bugs`, `features`, `triage`, `refinement`, `harness-impl`, `harness-pr-lifecycle`, `harness-merge`, `conflict-resolution`, `sota-gap`, `harness-post-merge`) | `.xylem.yml:4-144` | Core ships a generic subset; overlay adds the xylem-specific half |
| `DefaultProtectedSurfaces = []string{}` | `cli/internal/config/config.go:55` | Empty since PR #194; rationale in the comment at lines 20-54. Opt-in enforcement via `harness.protected_surfaces.paths`, consumed through `Config.EffectiveProtectedSurfaces()` (`config.go:424-433`) and enforced by `Runner.verifyProtectedSurfaces` (`runner.go:2694`). This is load-bearing for self-hosting (every harness-maintenance vessel edits `.xylem/*`) and a dangerous default for external repos unless reframed via workflow classes (§9). |

## 5. Proposed architecture

### 5.1 Three-layer model

xylem SHOULD be packaged as three layers rather than one monolithic control plane:

```
┌────────────────────────────────────────────────────────────────────┐
│ Layer 1 — core profile (embedded, default)                         │
│   workflows: fix-bug, implement-feature, triage, refine-issue,     │
│              review-pr, respond-to-pr-review, fix-pr-checks,       │
│              merge-pr, resolve-conflicts, lessons,                 │
│              context-weight-audit, workflow-health-report,         │
│              adapt-repo                                            │
│   labels: generic (bug, enhancement, ready-for-work,               │
│           ready-to-merge, needs-triage, needs-refinement,          │
│           needs-conflict-resolution, xylem-adapt-repo)             │
│   default class: delivery                                          │
└───────────────┬────────────────────────────────────────────────────┘
                │ layered on top
                ▼
┌────────────────────────────────────────────────────────────────────┐
│ Layer 2 — adapt-repo loop (runs once, then idempotently)           │
│   reads: .xylem.yml, HARNESS.md, AGENTS.md, README.md, docs/,      │
│          package manifests, CI config                              │
│   writes: patches to .xylem.yml, repo-specific validation block,   │
│           repo-tailored workflow gates, HARNESS additions          │
│   class: harness-maintenance (PR-only, runner rejects direct       │
│          commits to default branch)                                │
└───────────────┬────────────────────────────────────────────────────┘
                │ optional, opt-in
                ▼
┌────────────────────────────────────────────────────────────────────┐
│ Layer 3 — self-hosting-xylem overlay (xylem repo only)             │
│   workflows: implement-harness, sota-gap-analysis, unblock-wave,   │
│              diagnose-failures                                     │
│   sources:   harness-impl, harness-pr-lifecycle, harness-merge,    │
│              conflict-resolution, sota-gap, harness-post-merge     │
│   daemon:    auto_upgrade, auto_merge, auto_merge_repo             │
│   labels:    harness-impl, xylem-failed, pr-vessel-active, ...     │
└────────────────────────────────────────────────────────────────────┘
```

The three layers compose strictly bottom-up: layer 1 MUST run standalone; layer 2 MUST require layer 1; layer 3 MUST require both layers 1 and 2. Users MAY add repo-local workflows on top of any composition without invalidating the stack.

### 5.2 Control plane vs runtime state split

`.xylem/` currently stores two categories of data with different durability and review requirements:

| Category | Files | Durability | Review required |
| --- | --- | --- | --- |
| **Control plane** | `HARNESS.md`, `workflows/*.yaml`, `prompts/*/*.md`, `profile.lock` | Durable; defines behavior | Yes |
| **Runtime state** | `queue*.jsonl`, `phases/`, `schedules/`, `reviews/`, `locks/`, `traces/`, `state/bootstrap/`, `daemon.pid`, `dtu/` | Ephemeral; rebuildable | No |

The xylem repository already maintains this split *de facto* through root `.gitignore` allowlist entries (`.gitignore:12-24`), which explicitly track `HARNESS.md`, `eval/`, `prompts/`, `workflows/`, and `state/sota-gap-snapshot.json` while excluding everything else under `.xylem/`. External repositories initialized by `xylem init` do **not** inherit these overrides because `init` writes a `.xylem/.gitignore` containing only `*\n!.gitignore\n` (`cli/cmd/xylem/init.go:51`), which hides the entire subtree.

The proposed layout after the split:

```
.xylem/
├── HARNESS.md                 # tracked
├── workflows/                 # tracked
├── prompts/                   # tracked
├── profile.lock               # tracked: records resolved profile versions
├── .gitignore                 # tracked: selectively ignore state/
└── state/                     # gitignored
    ├── queue.jsonl
    ├── phases/
    ├── schedules/
    ├── reviews/
    ├── locks/
    ├── traces/
    └── bootstrap/             # adapt-repo analysis artifacts
```

Proposed `.xylem/.gitignore`, written by `xylem init` instead of the current hide-all default:

```
# Generated by xylem init. Edit to suit your project.
state/
*.lock
!profile.lock
```

Every code path that reads or writes queue, phase, schedule, review, lock, trace, or daemon PID files under `.xylem/` MUST be updated to use `.xylem/state/` as the runtime root. Backwards-compatible migration is required because the xylem repository has run against the flat layout for its entire history; the migration path is covered in §14.

**Normative:** if a user adopts this spec and implements only one change from it, they MUST implement the control-plane / runtime-state split first. Every other section in this spec assumes the split is in place.

### 5.3 Embedded profile filesystem

Profiles MUST live in `cli/internal/profiles/` (new package) as an `embed.FS`, replacing the inline string constants in `cli/cmd/xylem/init.go:58-67`:

```
cli/internal/profiles/
├── profiles.go                  # embed.FS and loader
├── core/
│   ├── xylem.yml.tmpl
│   ├── HARNESS.md.tmpl
│   ├── profile.lock.tmpl
│   ├── workflows/
│   │   ├── fix-bug.yaml
│   │   ├── implement-feature.yaml
│   │   ├── triage.yaml
│   │   ├── refine-issue.yaml
│   │   ├── review-pr.yaml
│   │   ├── respond-to-pr-review.yaml
│   │   ├── fix-pr-checks.yaml
│   │   ├── merge-pr.yaml
│   │   ├── resolve-conflicts.yaml
│   │   ├── lessons.yaml
│   │   ├── context-weight-audit.yaml
│   │   ├── workflow-health-report.yaml
│   │   └── adapt-repo.yaml
│   └── prompts/
│       └── <workflow>/<phase>.md
└── self-hosting-xylem/
    ├── xylem.overlay.yml        # sources and daemon block merged into .xylem.yml
    ├── profile.lock.tmpl
    ├── workflows/
    │   ├── implement-harness.yaml
    │   ├── sota-gap-analysis.yaml
    │   ├── unblock-wave.yaml
    │   └── diagnose-failures.yaml
    └── prompts/
        └── <workflow>/<phase>.md
```

Proposed package surface for `cli/internal/profiles/`:

```go
// cli/internal/profiles/profiles.go (new file)
package profiles

import (
    "embed"
    "fmt"
    "io/fs"
)

//go:embed core/** self-hosting-xylem/**
var profileFS embed.FS

// Profile represents a named, versioned bundle of templates.
type Profile struct {
    Name    string
    Version int
    FS      fs.FS
}

// Load returns the named profile, or an error if it does not exist.
func Load(name string) (*Profile, error) { /* ... */ }

// Compose merges profiles left-to-right; later profiles override earlier ones
// on workflow name conflicts and extend sources/daemon blocks additively.
func Compose(names ...string) (*ComposedProfile, error) { /* ... */ }
```

`xylem init` MUST consume profiles via this package rather than writing inline constants. The CLI MUST accept `--profile core` (default) and `--profile core,self-hosting-xylem` (xylem repo); additional overlay names are reserved for future extensibility and MUST return a validation error if passed in v1.

### 5.4 Profile composition model

The `.xylem.yml` schema MUST gain a new top-level `profiles` field:

```yaml
profiles: [core, self-hosting-xylem]   # left-to-right precedence
```

Semantics:

1. **Left-to-right precedence.** Later profiles override earlier profiles on workflow name conflicts. A workflow named `review-pr` in `self-hosting-xylem` replaces the `review-pr` from `core`.
2. **Additive source merging.** Source blocks are merged by top-level source key. If `core` defines `bugs` and `self-hosting-xylem` defines `harness-impl`, the composed config contains both. Conflicting source keys are a validation error.
3. **Additive daemon merging.** The `daemon` block is merged field-by-field; later profiles win on scalar conflicts. `core` SHOULD NOT set `auto_upgrade`, `auto_merge`, or `auto_merge_repo`.
4. **On-disk `.xylem.yml` wins.** A user's checked-in `.xylem.yml` is layered on top of the composed profile output; any field present in the user's file overrides the composed value. This preserves the current "user's `.xylem.yml` is the source of truth" contract.
5. **`profile.lock` is tracked.** `xylem init` MUST write `.xylem/profile.lock` recording the resolved profile names and versions. Subsequent `xylem init --force` or `adapt-repo` runs MUST compare the on-disk lock against the embedded profile version and surface drift as actionable warnings (not silent overwrites).

Proposed minimal `profile.lock` shape:

```yaml
profile_version: 1
profiles:
  - name: core
    version: 1
  # - name: self-hosting-xylem
  #   version: 1
locked_at: 2026-04-09T00:00:00Z
```

Validation on conflict: `xylem config validate` MUST reject a composition where two profiles declare the same workflow name with different `phases`, and MUST reject a composition where two profiles declare the same source key. The error MUST name both profiles and the conflicting artifact so the user can resolve manually.

### 5.5 Workflow class precursor already in the tree

The `Workflow` struct at `cli/internal/workflow/workflow.go:21-29` has two boolean fields that are the de-facto harness-maintenance capability model today:

```go
type Workflow struct {
    Name                          string  `yaml:"name"`
    Description                   string  `yaml:"description,omitempty"`
    LLM                           *string `yaml:"llm,omitempty"`
    Model                         *string `yaml:"model,omitempty"`
    AllowAdditiveProtectedWrites  bool    `yaml:"allow_additive_protected_writes,omitempty"`
    AllowCanonicalProtectedWrites bool    `yaml:"allow_canonical_protected_writes,omitempty"`
    Phases                        []Phase `yaml:"phases"`
}
```

`AllowAdditiveProtectedWrites` allows a workflow to add new lines to a protected file without triggering `Runner.verifyProtectedSurfaces` (`runner.go:2694`); `AllowCanonicalProtectedWrites` allows a workflow to fully rewrite a protected file. Both default to `false`. This is a two-level capability model per workflow — not yet organized as a named class taxonomy, but already the mechanism xylem uses to let `implement-harness` edit its own prompts while denying `fix-bug` the same privilege.

The formal class taxonomy proposed in §9 MUST be a strict extension of this existing model: the `delivery` class MUST correspond to `AllowAdditiveProtectedWrites=false && AllowCanonicalProtectedWrites=false`; the `harness-maintenance` class MUST correspond to at least `AllowCanonicalProtectedWrites=true`; and migration MUST preserve behavioral equivalence for every existing workflow that uses the bool fields today.

## 6. The `core` profile

### 6.1 Workflows shipped in core

| Workflow | Ship in core? | Parameterization needed | Source today |
| --- | --- | --- | --- |
| `fix-bug` | Yes | None — workflow is already generic | `.xylem/workflows/fix-bug.yaml` |
| `implement-feature` | Yes | None — workflow is already generic | `.xylem/workflows/implement-feature.yaml` |
| `triage` | Yes | None — workflow is already generic | `.xylem/workflows/triage.yaml` |
| `refine-issue` | Yes | None — workflow is already generic | `.xylem/workflows/refine-issue.yaml` |
| `review-pr` | Yes | Inject repo slug via template | `.xylem/workflows/review-pr.yaml` |
| `respond-to-pr-review` | Yes | Rename xylem-branded labels; default `author_allow` to empty | `.xylem/workflows/respond-to-pr-review.yaml` |
| `fix-pr-checks` | Yes, **after parameterization** | Replace Go-specific gate with `validation:` block lookup (§6.4) | `.xylem/workflows/fix-pr-checks.yaml` |
| `merge-pr` | Yes, **after parameterization** | Remove hard-coded `nicholls-inc/xylem`; read repo slug from config | `.xylem/workflows/merge-pr.yaml` |
| `resolve-conflicts` | Yes, **after parameterization** | Replace Go-specific gate with `validation:` block lookup | `.xylem/workflows/resolve-conflicts.yaml` |
| `lessons` | Yes (scheduled) | None — already repo-agnostic | `cli/cmd/xylem/lessons.go` |
| `context-weight-audit` | Yes (scheduled) | None — already repo-agnostic | Not yet in-tree as a workflow |
| `workflow-health-report` | Yes (scheduled) | **New** — wraps `runner/health` output into a weekly GitHub issue | **New** |
| `adapt-repo` | Yes (first-run + scheduled quarterly) | **New** — see §5 of the larger spec | **New** |
| `implement-harness` | **No** — overlay only | n/a | `.xylem/workflows/implement-harness.yaml` |
| `sota-gap-analysis` | **No** — overlay only | n/a | `.xylem/workflows/sota-gap-analysis.yaml` |
| `unblock-wave` | **No** — overlay only | n/a | `.xylem/workflows/unblock-wave.yaml` |
| `diagnose-failures` | **No** — overlay only in v1; MAY promote to core after Failed Vessel Recovery Phase 3 lands | n/a | `.xylem/workflows/diagnose-failures.yaml` |

Every "Yes, after parameterization" workflow MUST consume its validation commands via a top-level `validation:` block (§6.4); it MUST NOT hard-code shell invocations in prompt Markdown or gate definitions.

### 6.2 Generic sources block

The `core` profile's `xylem.yml.tmpl` MUST produce a sources section of this shape:

```yaml
sources:
  bugs:
    type: github
    repo: {{ .Repo }}
    tasks:
      fix:
        labels: [bug, ready-for-work]
        workflow: fix-bug

  features:
    type: github
    repo: {{ .Repo }}
    tasks:
      implement:
        labels: [enhancement, ready-for-work]
        workflow: implement-feature

  triage:
    type: github
    repo: {{ .Repo }}
    tasks:
      triage:
        labels: [needs-triage]
        workflow: triage

  refinement:
    type: github
    repo: {{ .Repo }}
    tasks:
      refine:
        labels: [needs-refinement]
        workflow: refine-issue

  adaptation:
    type: github
    repo: {{ .Repo }}
    tasks:
      adapt:
        labels: [xylem-adapt-repo]
        workflow: adapt-repo

  pr-lifecycle:
    type: github-pr-events
    repo: {{ .Repo }}
    tasks:
      review:
        workflow: review-pr
        on:
          pr_opened: true
          pr_head_updated: true
      respond:
        workflow: respond-to-pr-review
        on:
          review_submitted: true
      fix-checks:
        workflow: fix-pr-checks
        on:
          checks_failed: true

  merge:
    type: github-pr
    repo: {{ .Repo }}
    tasks:
      merge:
        labels: [ready-to-merge]
        workflow: merge-pr

  conflicts:
    type: github-pr
    repo: {{ .Repo }}
    tasks:
      resolve:
        labels: [needs-conflict-resolution]
        workflow: resolve-conflicts
```

The `pr_opened` and `pr_head_updated` triggers are **new** for `github-pr-events` and MUST be implemented as part of shipping the core profile. Their semantics are covered in §11.

The label vocabulary `bug`, `enhancement`, `ready-for-work`, `ready-to-merge`, `needs-triage`, `needs-refinement`, `needs-conflict-resolution`, and `xylem-adapt-repo` MUST be the generic defaults. The xylem-specific labels `harness-impl`, `xylem-failed`, `pr-vessel-active`, `pr-vessel-merged`, `harness-post-merge`, and `[sota-gap]` MUST NOT appear anywhere in `cli/internal/profiles/core/`.

### 6.3 Scheduled hygiene sources

The `core` profile MUST include a `scheduled-hygiene` source block running three recurring workflows out of the box:

```yaml
  scheduled-hygiene:
    type: scheduled
    repo: {{ .Repo }}
    tasks:
      lessons:
        schedule: "@daily"
        workflow: lessons
      context-audit:
        schedule: "@weekly"
        workflow: context-weight-audit
      health-report:
        schedule: "@weekly"
        workflow: workflow-health-report
```

Rationale:

- **`lessons` daily** gives the harness an institutional-memory loop from day one; this is what turns failed-vessel artifacts (including the new `failure-review.json` from Failed Vessel Recovery Phase 1) into actionable HARNESS updates.
- **`context-weight-audit` weekly** catches prompt and HARNESS drift before it becomes a silent cost and quality problem.
- **`workflow-health-report` weekly** wraps the existing `runner/health` output into a GitHub issue in the target repo so operators see fleet-level anomalies without polling the CLI.

All three MUST be idempotent: each SHOULD exit with `XYLEM_NOOP` if an open report/review issue already exists, and SHOULD rate-limit to at most one open issue per workflow per week.

### 6.4 Validation config block

A new top-level `validation:` block in `.xylem.yml` MUST describe the repo's validation commands once, so that `fix-pr-checks`, `resolve-conflicts`, and `adapt-repo`'s `verify` phase can consume them from a single source of truth:

```yaml
validation:
  format: "goimports -l ."
  lint:   "go vet ./..."
  build:  "go build ./..."
  test:   "go test ./..."
```

Proposed Go type (added to `cli/internal/config/config.go`):

```go
// ValidationConfig declares the repo's canonical validation commands.
// Consumed by fix-pr-checks, resolve-conflicts, and adapt-repo verify phases.
type ValidationConfig struct {
    Format string `yaml:"format,omitempty"`
    Lint   string `yaml:"lint,omitempty"`
    Build  string `yaml:"build,omitempty"`
    Test   string `yaml:"test,omitempty"`
}

type Config struct {
    // ... existing fields unchanged ...
    Validation ValidationConfig `yaml:"validation,omitempty"`
}
```

Semantics:

- Every field is optional; empty means "skip this step".
- Commands MUST run from the worktree root.
- `fix-pr-checks`, `resolve-conflicts`, and `adapt-repo verify` MUST template these commands into their gate definitions; they MUST NOT hard-code Go-specific invocations in workflow YAML or prompt Markdown.
- `adapt-repo` MUST populate this block on first run based on `bootstrap.DetectLanguages` / `DetectBuildTools` output (§8).
- `xylem config validate` MUST reject a configuration where `fix-pr-checks` / `resolve-conflicts` / `adapt-repo` are active but `validation:` is completely empty.

## 7. The `self-hosting-xylem` overlay

### 7.1 Inventory of xylem-specific workflows and sources

Everything the xylem repository currently depends on that is **not** generic enough for the core profile belongs in `cli/internal/profiles/self-hosting-xylem/`. Inventory:

| Artifact | Current location | Why overlay-only |
| --- | --- | --- |
| `implement-harness.yaml` | `.xylem/workflows/implement-harness.yaml` | Tied to xylem's internal harness-implementation flow, Go validation commands, xylem-specific PR body templates |
| `sota-gap-analysis.yaml` | `.xylem/workflows/sota-gap-analysis.yaml` | Compares xylem against its own SoTA reference docs and files `[sota-gap]` issues in `nicholls-inc/xylem` |
| `unblock-wave.yaml` | `.xylem/workflows/unblock-wave.yaml` | Encodes xylem's post-merge dependency-unblocking loop |
| `diagnose-failures.yaml` | `.xylem/workflows/diagnose-failures.yaml` | Consumes xylem-specific failure label taxonomy; v1 ships in overlay only and MAY promote to core after Failed Vessel Recovery Phase 3 |
| Source `bugs` (xylem version with `xylem-failed` status labels) | `.xylem.yml:4-16` | Overlay extends `core`'s `bugs` with xylem-specific `exclude` and `status_labels` |
| Source `features` (xylem version) | `.xylem.yml:18-29` | Same extension pattern |
| Source `triage` (xylem version) | `.xylem.yml:31-44` | Same extension pattern |
| Source `refinement` (xylem version) | `.xylem.yml:46-56` | Same extension pattern |
| Source `harness-impl` | `.xylem.yml:58-71` | Gated on the `harness-impl` label, workflow is `implement-harness` |
| Source `harness-pr-lifecycle` | `.xylem.yml:77-97` | `respond-to-pr-review` + `fix-pr-checks`, filtered to xylem's harness PRs |
| Source `harness-merge` | `.xylem.yml:100-111` | `merge-pr` gated on `ready-to-merge + harness-impl` |
| Source `conflict-resolution` | `.xylem.yml:114-124` | `resolve-conflicts` gated on `needs-conflict-resolution + harness-impl` |
| Source `sota-gap` | `.xylem.yml:126-134` | Scheduled weekly gap-analysis |
| Source `harness-post-merge` | `.xylem.yml:139-144` | `github-merge` → `unblock-wave` |
| `daemon.auto_upgrade: true` | `.xylem.yml:153` | Binary auto-upgrade is xylem-specific (pulls `origin/main` for `github.com/nicholls-inc/xylem`, rebuilds, `exec()`s) |
| `daemon.auto_merge: true` + `auto_merge_repo: nicholls-inc/xylem` | `.xylem.yml:154-155` | Hard-coded to xylem's own label taxonomy and branch naming |
| Xylem branch naming (`xylem/<n>-<slug>`) | Source implementations + PR workflow prompts | Overlay only |
| Label taxonomy: `harness-impl`, `xylem-failed`, `pr-vessel-active`, `pr-vessel-merged`, `harness-post-merge` | All xylem-specific sources + workflows | Overlay only |

### 7.2 Migration target

After migration, the xylem repository's on-disk `.xylem/workflows/` directory MUST contain **only** workflows that are either (a) identical to the `core` profile (and therefore redundant, candidates for deletion) or (b) xylem-specific overrides that must exist on disk because they diverge from the overlay defaults.

The migration target for xylem's own `.xylem/`:

| Path | After migration |
| --- | --- |
| `.xylem/workflows/fix-bug.yaml` | Deleted; resolved via composition from `core` |
| `.xylem/workflows/implement-feature.yaml` | Deleted; resolved via composition from `core` |
| `.xylem/workflows/triage.yaml` | Deleted; resolved via composition from `core` |
| `.xylem/workflows/refine-issue.yaml` | Deleted; resolved via composition from `core` |
| `.xylem/workflows/review-pr.yaml` | Deleted; resolved via composition from `core` |
| `.xylem/workflows/respond-to-pr-review.yaml` | Deleted; resolved via composition from `core` |
| `.xylem/workflows/fix-pr-checks.yaml` | Deleted; resolved via composition from `core`, consuming `validation:` block |
| `.xylem/workflows/merge-pr.yaml` | Deleted; resolved via composition from `core` |
| `.xylem/workflows/resolve-conflicts.yaml` | Deleted; resolved via composition from `core`, consuming `validation:` block |
| `.xylem/workflows/implement-harness.yaml` | Moved to `cli/internal/profiles/self-hosting-xylem/workflows/implement-harness.yaml` |
| `.xylem/workflows/sota-gap-analysis.yaml` | Moved to `cli/internal/profiles/self-hosting-xylem/workflows/sota-gap-analysis.yaml` |
| `.xylem/workflows/unblock-wave.yaml` | Moved to `cli/internal/profiles/self-hosting-xylem/workflows/unblock-wave.yaml` |
| `.xylem/workflows/diagnose-failures.yaml` | Moved to `cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml` |
| `.xylem.yml` | Trimmed to `profiles: [core, self-hosting-xylem]`, `validation:` block, `observability`, `daemon`, `llm`, `model`, `claude`, `copilot`, and any repo-local overrides that differ from the overlay |

The daemon `auto_upgrade` behavior MUST remain overlay-only. `cli/cmd/xylem/upgrade.go`'s assumption that the binary is `github.com/nicholls-inc/xylem` MUST NOT leak into the `core` profile. Xylem-specific branch naming and label taxonomy MUST live only in `self-hosting-xylem/`.

### 7.3 Overlay activation and acceptance criterion

Activation of the overlay MUST be through the `profiles` field in `.xylem.yml`:

```yaml
# xylem's own .xylem.yml after migration
profiles: [core, self-hosting-xylem]

validation:
  format: "goimports -l ."
  lint:   "go vet ./cli/..."
  build:  "go build ./cli/cmd/xylem"
  test:   "go test ./cli/..."

observability:
  enabled: true
  endpoint: "localhost:4317"
  insecure: true
  sample_rate: 1.0

daemon:
  auto_upgrade: true
  auto_merge: true
  auto_merge_repo: nicholls-inc/xylem

# ... llm, model, claude, copilot blocks as today ...
```

The acceptance criterion for the overlay migration is **behavioral equivalence with the current xylem repo state**. Specifically:

1. The composed `.xylem.yml` (core + self-hosting-xylem + on-disk overrides) MUST load to a `config.Config` struct whose `Sources`, `Daemon`, `Harness`, `Observability`, and `Cost` fields match the current in-tree `.xylem.yml` field-for-field (modulo ordering).
2. The composed workflow set (core + self-hosting-xylem overrides + on-disk overrides) MUST contain every workflow name currently present in `.xylem/workflows/` with equivalent phase definitions.
3. `xylem review` output SHOULD match pre-migration output within noise tolerance over a one-week window.
4. The daemon's auto-upgrade and auto-merge behaviors MUST continue to fire on the same labels and branch patterns.
5. Vessels that currently pass on the xylem repo MUST continue to pass after migration; vessels that currently fail MUST fail in the same way (no new regressions introduced by profile composition).

The migration path that achieves this criterion is covered in §14.
## 8. The adapt-repo workflow

### 8.1 Purpose and scope

`adapt-repo` is the first-run repo-aware adaptation loop that closes the gap between a generic `core` profile (section 6) and a harness that actually understands the target repository. Its only job is to produce a **reviewable pull request** that updates `.xylem.yml`, selected `.xylem/` workflows, and optional repo documentation (`AGENTS.md`, minimal `docs/`) so that subsequent vessels execute against repo-aware validation gates, labels, and entry points.

`adapt-repo` MUST NOT be used to implement application changes, file bug issues, or modify source code outside the control plane. It is a **harness-maintenance** workflow (see §9) and inherits the full policy surface of that class.

Scope boundaries:

- In scope: `.xylem.yml`, `.xylem/workflows/*.yaml`, `.xylem/prompts/*/*.md`, `.xylem/HARNESS.md`, `AGENTS.md`, new files under `docs/` (stubs only).
- Out of scope: any file under `cli/`, `src/`, `internal/`, `pkg/`, `app/`, or any path matching the target repo's language-detected source roots; package installs; secret reads; direct commits to the default branch.

### 8.2 Seeding: deterministic, one-time, idempotent

`adapt-repo` MUST be seeded deterministically. There are three supported seeding paths; operators MAY use any combination.

1. **Daemon startup check** (recommended default). On daemon boot, before entering the scan-drain loop, the daemon MUST check for `.xylem/state/bootstrap/adapt-repo-seeded.json`. If the marker is absent, the daemon MUST call the GitHub API to create an issue titled `[xylem] adapt harness to this repository` with labels `xylem-adapt-repo, ready-for-work`, then write the marker file. The call MUST dedupe by title — if a matching open or closed issue already exists in the target repo, no new issue is created and the marker is still written.
2. **`xylem init --seed` post-hook**. The existing `xylem init` subcommand MUST gain a `--seed` flag that performs the same issue-creation step synchronously after scaffolding the profile. Useful for operators who want explicit control and for CI bootstrap scripts.
3. **Manual fallback**. An operator MAY create the issue manually with the `xylem-adapt-repo` label; the scanner picks it up on the next tick. Marker writing is skipped in this path.

Marker placement: `.xylem/state/bootstrap/adapt-repo-seeded.json` MUST live under the runtime-state subtree (gitignored per the core profile layout). Re-seeding from a fresh clone is desirable when the original adaptation issue has been deleted; a tracked marker would prevent that.

Marker schema:

```json
{
  "seeded_at": "2026-04-09T12:00:00Z",
  "issue_number": 1234,
  "issue_url": "https://github.com/owner/repo/issues/1234",
  "profile_version": 1,
  "seeded_by": "daemon|init|manual"
}
```

### 8.3 Phase design

`adapt-repo` is a 7-phase workflow with strictly alternating deterministic and LLM phases. Deterministic phases MUST run first in each half so the LLM phases start from a concrete, auditable analysis rather than a blank canvas.

| # | Phase        | Type    | Purpose |
| - | ------------ | ------- | ------- |
| 1 | `analyze`    | command | Run `xylem bootstrap analyze-repo` → `.xylem/state/bootstrap/repo-analysis.json` |
| 2 | `legibility` | command | Run `xylem bootstrap audit-legibility` → `.xylem/state/bootstrap/legibility-report.json` |
| 3 | `plan`       | prompt  | LLM reads analysis + legibility + existing `.xylem.yml` + nearby docs; emits `adapt-plan.json` |
| 4 | `validate`   | command | Run `xylem config validate --proposed` + `xylem workflow validate --proposed` |
| 5 | `apply`      | prompt  | LLM applies the validated plan to the worktree via the `Edit` tool |
| 6 | `verify`     | command | Run `xylem validation run --from-config` against the parameterized validation block (§11.4) |
| 7 | `pr`         | prompt  | LLM opens a PR titled `[xylem] adapt harness to this repository` with the evidence bundle |

Phase 1 (`analyze`) MUST invoke a new CLI subcommand `xylem bootstrap analyze-repo`. That subcommand is currently missing; the underlying functions exist in `cli/internal/bootstrap/` but are dormant (no `cli/cmd/xylem/bootstrap.go` entry point). The subcommand MUST wrap `bootstrap.AnalyzeRepo`, `bootstrap.DetectLanguages`, `bootstrap.DetectFrameworks`, `bootstrap.DetectBuildTools`, `bootstrap.DiscoverEntryPoints`, and `bootstrap.DefaultDimensions`, and emit their combined output as JSON to the path named on the command line.

Phase 2 (`legibility`) MUST invoke `xylem bootstrap audit-legibility`, wrapping `bootstrap.AuditLegibility` and `bootstrap.DetectConventionFiles`. The output is a structured report of missing conventional files (`AGENTS.md`, `README.md`, `CONTRIBUTING.md`, entry-point docs, decision records) with severity and remediation hints.

Phase 3 (`plan`) is an LLM phase whose only allowed write is `.xylem/state/bootstrap/adapt-plan.json`. The prompt template MUST forbid edits to any other path and MUST list the exact JSON schema of §8.4 inline. The phase MUST fail closed if the emitted file does not match the schema.

Phase 4 (`validate`) MUST invoke two new CLI subcommands, both currently missing: `xylem config validate --proposed <plan.json>` and `xylem workflow validate --proposed <plan.json>`. `--proposed` tells each validator to apply the plan to a copy of the current config in memory, validate the result, and exit non-zero on error without touching disk.

Phase 5 (`apply`) is an LLM phase constrained to the `Edit` tool and the path allowlist in §8.1. The phase MUST NOT invoke `git`, `Bash`, or any tool that performs network I/O.

Phase 6 (`verify`) MUST invoke `xylem validation run --from-config`. That subcommand is also currently missing and MUST read the `validation:` block from `.xylem.yml` (see §11.4), run every configured command sequentially, and exit non-zero on first failure.

Phase 7 (`pr`) opens the PR via `gh pr create` and MUST include links to every `.xylem/state/bootstrap/*.json` artifact produced by the run. The PR body MUST list `planned_changes` and `skipped` entries inline so reviewers can scan the full adaptation surface without opening artifact files.

### 8.4 adapt-plan.json schema

Phase 3 MUST emit a JSON file conforming exactly to the schema below. Unknown fields are a validation error.

```json
{
  "schema_version": 1,
  "detected": {
    "languages": ["go", "typescript"],
    "build_tools": ["go", "pnpm"],
    "test_runners": ["go test", "vitest"],
    "linters": ["goimports", "eslint"],
    "has_frontend": true,
    "has_database": false,
    "entry_points": ["cli/cmd/myapp", "web/src/main.ts"]
  },
  "planned_changes": [
    {
      "path": ".xylem.yml",
      "op": "patch",
      "rationale": "add Go + TS validation gates",
      "diff_summary": "validation.format: goimports; validation.build: go build + pnpm build"
    },
    {
      "path": ".xylem/workflows/fix-pr-checks.yaml",
      "op": "replace",
      "rationale": "swap Go-only gate for templated validation block"
    },
    {
      "path": "AGENTS.md",
      "op": "create",
      "rationale": "no agent map detected; legibility report score below threshold"
    }
  ],
  "skipped": [
    {
      "path": ".xylem/workflows/db-migrate.yaml",
      "reason": "no database detected"
    }
  ]
}
```

Field contracts:

- `schema_version` MUST equal `1` for this spec. Future versions require a migration and MUST NOT be emitted by agents without a spec update.
- `planned_changes[].op` MUST be one of `patch`, `replace`, `create`, `delete`. `delete` is permitted only under `.xylem/workflows/` and only for workflows the detection phase marked as inapplicable (for example, deleting `db-migrate.yaml` in a repo with no database).
- `planned_changes[].path` MUST match the path allowlist in §8.1.
- `skipped` is required — an empty array is valid but the field MUST be present so reviewers can distinguish "no skips" from "malformed plan".

### 8.5 Example adaptations

| Repo shape               | Detected                                           | Plan highlights |
| ------------------------ | -------------------------------------------------- | --------------- |
| Go library/service       | `go`, `goimports`, `go vet`, `go build`, `go test` | Replace template validation with Go gates; enable scheduled `context-weight-audit` |
| Python (pyproject)       | `python`, `ruff`, `mypy`, `pytest`                 | Validation block with `ruff check .`, `mypy .`, `pytest`; add venv-aware entry-point detection |
| Frontend (pnpm/Next.js)  | `typescript`, `pnpm`, `vitest`, `eslint`           | Enable browser live gates; `pnpm lint && pnpm typecheck && pnpm test && pnpm build` |
| Database-heavy           | `postgres`, `sqlc`, migration dir                  | Add a `db-migrate` workflow; add HTTP or command-assert live gates for migration acceptance |
| Poorly documented        | Missing `AGENTS.md`/`README.md`/`docs/`            | Generate stubs via `bootstrap.InstructionSet`; add `legibility` as a scheduled weekly source |
| Mixed-stack (Go + TS)    | Both stacks detected                               | Propose per-stack gates — do NOT try to unify under one runner |

The `plan` phase MUST NOT attempt to unify validation across stacks. Mixed-stack repos get two validation blocks, one per stack, with explicit `working_dir` (proposed addition, §11.4).

### 8.6 Idempotency and rate limiting

`adapt-repo` MUST be safely re-runnable. The workflow MUST enforce the following guards:

1. If `planned_changes` is empty after phase 3, phases 5–7 MUST be skipped and the phase output MUST emit `XYLEM_NOOP` so the existing noop-match early-exit fires.
2. If the seeding issue already has an open associated PR (detected via `gh pr list --search "adapt harness"`), phase 1 MUST exit with `XYLEM_NOOP` before any analysis runs.
3. Every run MUST append a row to `.xylem/state/bootstrap/adaptation-log.jsonl` with `{started_at, finished_at, result, planned_changes_count, pr_url}`.
4. Rate limit: one `adapt-repo` vessel per repo per rolling 7 days unless the operator explicitly re-enqueues via a new issue with the `xylem-adapt-repo-force` label. The scanner MUST consult the adaptation log on every scan tick and suppress enqueue if the last completed run was less than 7 days ago.

### 8.7 Hard constraints on LLM phases

Phases 3, 5, and 7 are LLM phases and inherit the **harness-maintenance** class (§9). The class policy matrix is the primary enforcement point; the per-phase prompts are the secondary enforcement. Both MUST agree.

Hard constraints (enforced by class, not by prompt):

- No writes outside `.xylem/`, `.xylem.yml`, `AGENTS.md`, and a minimal `docs/` stub set.
- No package installs — phase prompts MUST NOT contain any instruction that would resolve to `npm install`, `pip install`, `go get`, `brew install`, or equivalent.
- No direct commits to the default branch. All writes land on the vessel worktree branch and reach the target repo only via the phase-7 PR.
- No reads of `.env*`, `~/.aws`, `~/.ssh`, `~/.gnupg`, `~/.netrc`, `~/.docker/config.json`, or any file listed under `secrets` in `.xylem.yml`.

Violations MUST be treated as a `harness-maintenance` class policy denial and MUST surface as a phase failure with a structured error citing the matched rule (§9.5).

### 8.8 Required new CLI surface

The audit confirms the following subcommands do not exist today and are prerequisites for `adapt-repo`. Each MUST ship before phase 1 of the rollout (§16 in the companion sections).

| Subcommand                           | Wraps                                                                                                                              | Target file                          |
| ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| `xylem bootstrap analyze-repo`       | `bootstrap.AnalyzeRepo`, `bootstrap.DetectLanguages`, `bootstrap.DetectFrameworks`, `bootstrap.DetectBuildTools`, `bootstrap.DiscoverEntryPoints` | New: `cli/cmd/xylem/bootstrap.go`    |
| `xylem bootstrap audit-legibility`   | `bootstrap.AuditLegibility`, `bootstrap.DetectConventionFiles`                                                                     | New: `cli/cmd/xylem/bootstrap.go`    |
| `xylem config validate [--proposed]` | Existing `Config.Validate()` in `cli/internal/config/config.go`; `--proposed` applies a plan first                                 | New: `cli/cmd/xylem/config.go`       |
| `xylem workflow validate [--proposed]` | Existing `workflow.Load` + `Workflow.Validate`                                                                                  | New: `cli/cmd/xylem/workflow.go`     |
| `xylem validation run --from-config` | Reads `validation:` block and executes each command                                                                                | New: `cli/cmd/xylem/validation.go`   |

None of these subcommands existed at the time of spec drafting (validated against `cli/cmd/xylem/` — no `bootstrap.go`, no `validation.go`, no standalone `config.go` / `workflow.go` subcommand entry points). All MUST be added.

---

## 9. Workflow classes and the policy matrix

### 9.1 Motivation

The current protected-write knob is a pair of booleans on `workflow.Workflow` — `AllowAdditiveProtectedWrites` and `AllowCanonicalProtectedWrites` (`cli/internal/workflow/workflow.go:26-27`). In practice xylem ships with `DefaultProtectedSurfaces = []string{}` (`cli/internal/config/config.go:55`); the rationale comment at lines 20-54 explains that runtime protection interfered with xylem's self-improvement loop and that PR review is the real security gate.

The empty default is a symptom, not a solution. It works for xylem-on-xylem because the repo is actively maintained and every merged PR is reviewed, but it violates the SoTA §4.9 invariant ("no writes to agent configuration files, even inside the workspace") the moment xylem is pointed at an arbitrary external repo where the reviewer discipline is unknown.

The right abstraction is **workflow classes**. A class is a single authoritative label on a workflow that determines what the workflow is allowed to do. The two existing booleans become class-specific fine-grained modifiers (§9.6); the class is the authority.

### 9.2 Proposed Workflow.Class field

Add a `Class` field to the `Workflow` struct:

```go
// cli/internal/workflow/workflow.go

type Class string

const (
    ClassDelivery           Class = "delivery"
    ClassHarnessMaintenance Class = "harness-maintenance"
    ClassOps                Class = "ops"
)

type Workflow struct {
    Name                          string  `yaml:"name"`
    Description                   string  `yaml:"description,omitempty"`
    Class                         Class   `yaml:"class,omitempty"`
    LLM                           *string `yaml:"llm,omitempty"`
    Model                         *string `yaml:"model,omitempty"`
    AllowAdditiveProtectedWrites  bool    `yaml:"allow_additive_protected_writes,omitempty"`
    AllowCanonicalProtectedWrites bool    `yaml:"allow_canonical_protected_writes,omitempty"`
    Phases                        []Phase `yaml:"phases"`
}
```

When `Class` is omitted, the loader MUST default it to `ClassDelivery` — the strictest class. No workflow MAY be loaded with a `Class` value outside the three constants above; loading MUST fail closed.

### 9.3 Policy matrix

The policy matrix is authoritative. It MUST be encoded in Go constants (§9.4) and MUST NOT be overridable except by tightening (§9.8).

| Operation                           | delivery | harness-maintenance | ops            |
| ----------------------------------- | -------- | ------------------- | -------------- |
| Write `.xylem/HARNESS.md`           | deny     | allow (audited)     | deny           |
| Write `.xylem.yml`                  | deny     | allow (audited)     | deny           |
| Write `.xylem/workflows/*.yaml`     | deny     | allow (audited)     | deny           |
| Write `.xylem/prompts/*/*.md`       | deny     | allow (audited)     | deny           |
| Commit to default branch            | deny     | deny                | deny           |
| Push to a feature branch             | allow    | allow               | allow          |
| `gh pr create`                       | allow    | allow               | allow          |
| `gh pr merge`                        | deny     | deny                | allow (label-gated) |
| Send daemon reload signal            | deny     | deny                | allow (post-merge only) |
| Read secrets / `.env*`               | deny     | deny                | deny           |

Three properties of this matrix are load-bearing:

1. **harness-maintenance is strictly PR-gated**. Even though the class CAN write the control plane inside a worktree, it CANNOT commit to the default branch. The human review at merge time is the real security boundary. This matches the existing rationale for the empty `DefaultProtectedSurfaces` but enforces it mechanically instead of by convention.
2. **ops is the only class that can merge or reload**. `gh pr merge` and daemon reload are the two operations that mutate the running control plane. They get their own class so the audit log can answer "who flipped the switch?" with a single grep.
3. **delivery is the default for any new workflow**. A new workflow file authored tomorrow without an explicit `class:` value inherits the strictest policy.

### 9.4 New package `cli/internal/policy/`

Introduce a new package whose only job is to encode the matrix.

```go
// New: cli/internal/policy/class.go
package policy

type Class string

const (
    Delivery           Class = "delivery"
    HarnessMaintenance Class = "harness-maintenance"
    Ops                Class = "ops"
)

type Operation string

const (
    OpWriteControlPlane   Operation = "write_control_plane"
    OpCommitDefaultBranch Operation = "commit_default_branch"
    OpPushBranch          Operation = "push_branch"
    OpCreatePR            Operation = "create_pr"
    OpMergePR             Operation = "merge_pr"
    OpReloadDaemon        Operation = "reload_daemon"
    OpReadSecrets         Operation = "read_secrets"
)

type Decision struct {
    Allowed bool
    Rule    string // human-readable rule identifier, e.g. "delivery.write_control_plane.deny"
    Audit   bool   // true => record an audit entry even on allow
}

// Evaluate returns the policy decision for a (class, operation) pair.
// Unknown classes or operations return Decision{Allowed: false, Rule: "unknown"}.
func Evaluate(class Class, op Operation) Decision
```

The package MUST export only these symbols plus `AllClasses()` and `AllOperations()` helpers for tests. Callers MUST NOT construct `Decision` values directly.

### 9.5 Intermediary extension

The intermediary is already wired into phase execution — `cli/internal/runner/drain.go:131-133` creates the audit log and intermediary at drain start. The extension is scoped: load the class from the workflow YAML at phase launch, pass it as Intent metadata, and extend the audit entry.

Extension points:

1. **Runner → intermediary handoff**. The runner MUST set `intent.Metadata["workflow_class"]` to the effective class string on every `Submit` call. The runner already owns the workflow reference at phase launch; no new plumbing is required.
2. **Policy check shim**. The intermediary SHOULD gain a thin helper `EvaluateClass(ctx, class, op)` that delegates to `policy.Evaluate` and appends a decision entry. The existing glob-rule machinery (`cli/internal/intermediary/intermediary.go:41-69`) is preserved for user-defined fine-grained overrides; the class matrix is consulted first and fails closed before glob rules run.
3. **Audit log schema extension**. `AuditEntry` at `cli/internal/intermediary/intermediary.go:62-69` MUST gain three fields:

   ```go
   type AuditEntry struct {
       Intent        Intent    `json:"intent"`
       Decision      Effect    `json:"decision"`
       Timestamp     time.Time `json:"timestamp"`
       ApprovedBy    string    `json:"approved_by,omitempty"`
       Error         string    `json:"error,omitempty"`
       WorkflowClass string    `json:"workflow_class,omitempty"`
       PolicyRule    string    `json:"policy_rule,omitempty"`
       RuleMatched   string    `json:"rule_matched,omitempty"`
   }
   ```

   `WorkflowClass` is the class string at the time of decision. `PolicyRule` is the class-matrix rule identifier (e.g. `delivery.write_control_plane.deny`). `RuleMatched` is the glob rule that matched, if any. The existing write path at lines 87-113 is unchanged — the new fields ride along in JSON.

### 9.6 Interaction with existing AllowAdditive / AllowCanonical booleans

The two existing booleans at `cli/internal/workflow/workflow.go:26-27` are NOT removed. They are reinterpreted as class-specific capability modifiers:

| Class                  | `AllowAdditiveProtectedWrites` | `AllowCanonicalProtectedWrites` |
| ---------------------- | ------------------------------ | ------------------------------- |
| `delivery`             | MUST be `false` (enforced)     | MUST be `false` (enforced)      |
| `harness-maintenance`  | MAY be `true`                  | MAY be `true`                   |
| `ops`                  | MUST be `false` (enforced)     | MUST be `false` (enforced)      |

Migration for legacy workflows (workflows loaded from disk without a `class:` field):

- If both booleans are `false` → `class: delivery`.
- If either boolean is `true` → `class: harness-maintenance`.
- Never auto-promote to `ops` — `ops` MUST be set explicitly in YAML.

The migration MUST happen at workflow load time and MUST NOT persist back to disk. `xylem workflow validate` SHOULD report workflows that lack an explicit `class` field and MAY propose a patch via `xylem workflow validate --fix-class`.

### 9.7 Reconciling with SoTA §4.9

SoTA §4.9 forbids writes to agent configuration files in the workspace. The workflow-class model reconciles this by construction:

1. The **live config** — the one the running daemon consults — is never written by a vessel. It lives in the target repo's default branch (or wherever the daemon's CWD points, see §10.4 for the snapshot mechanism).
2. All control-plane writes go to a **feature branch in an isolated worktree**. Worktrees are already isolated via the existing `worktree` package.
3. The merge from feature branch to default branch is a separate `ops`-class action, label-gated on `ready-to-merge`, subject to human or policy review.
4. The daemon reload fires **only after a merge**, never at write time (§10).

The SoTA invariant is preserved because "configuration" means "the running configuration", and the running configuration is only ever changed by a merge that a human (or an explicit `ops`-class workflow acting on an explicit label) approved. Reviewers MUST cite this reasoning in every harness-maintenance PR.

### 9.8 User override via `.xylem.yml`

Users MAY tighten the baseline matrix but MUST NOT loosen it. Override schema:

```yaml
harness:
  policy:
    class_overrides:
      delivery:
        push_branch: deny    # tightens baseline (was allow)
      harness-maintenance:
        write_control_plane: deny  # effectively disables the class
```

Loading rules:

- If an override specifies `allow` or `audited` where the baseline specifies `deny`, config load MUST fail with a clear error message citing the offending class and operation.
- Overrides are merged per (class, operation) pair; unspecified pairs inherit the baseline.
- `xylem config validate` MUST surface effective policy values so operators can see the computed matrix.

---

## 10. Daemon reload mechanism

### 10.1 Motivation

There is no generic config reload today. The audit confirms:

- No SIGHUP handler anywhere under `cli/cmd/xylem/`.
- No file watcher on `.xylem/workflows/` or `.xylem.yml`.
- No `xylem daemon reload` subcommand.
- Workflows are loaded fresh for each phase by the runner, not snapshotted at daemon startup.
- Auto-upgrade (`cli/cmd/xylem/upgrade.go:107,131,71`) is binary-level: it fetches `origin main`, rebuilds `./cmd/xylem`, and execs the new binary at the captured executable path. It is xylem-specific and MUST NOT ship as the core profile's reload mechanism.

External repos need a generic, observable, safely-failing reload path. Without one, every `adapt-repo` merge requires an operator to restart the daemon by hand — which defeats the point of productizing xylem.

### 10.2 Options and recommendation

| Option | Pros                                                              | Cons |
| ------ | ----------------------------------------------------------------- | ---- |
| A      | File watch on `.xylem.yml` + `.xylem/workflows/**`. Simple.       | Fires during partial writes and on developer-local edits not meant for the daemon. |
| B      | Merge-triggered: reload on `github-merge` events touching control plane. Safe by default. | Requires a watching `github-merge` source; doesn't help CLI operators. |
| C      | Explicit `xylem daemon reload` + SIGHUP. User-controlled.         | Requires an operator or a vessel to actively request reload. |
| D      | Hybrid B + C. Merge-triggered primary path plus CLI/SIGHUP escape hatch. | Two code paths; slightly more surface area. |

**Recommendation: D (Hybrid).** The primary path MUST be merge-triggered: when a `github-merge` event fires for a PR that touched `.xylem.yml` or any file under `.xylem/workflows/`, `.xylem/prompts/`, or `.xylem/HARNESS.md`, the daemon MUST perform a reload. The escape hatch MUST be `xylem daemon reload` (CLI) and SIGHUP (POSIX signal).

Option A (file watch) is explicitly rejected: daemon operators and vessels co-edit these files, and partial-write races would fire spurious reloads during normal vessel execution.

### 10.3 Reload semantics

Every reload path MUST implement the same semantics:

1. **In-flight vessels continue against their launch-time workflow snapshot** (§10.4). A reload MUST NOT swap workflow files under an already-running phase. The runner MUST consult the snapshot for the remainder of the vessel's phases.
2. **The scanner picks up new config on the next tick**, not mid-scan. The reload blocks queue writes for the duration of the config swap (expected < 10ms for realistic configs).
3. **Invalid config fails closed**. The reload path MUST run `xylem config validate` on the new config before swapping. On failure, the daemon MUST log the error, emit a trace, keep running with the previous config, and append a failure row to `.xylem/state/reload-log.jsonl`.
4. **Brief queue write pause on final swap**. The queue MUST be flushed and write-locked for the swap window so no vessel is enqueued against a half-swapped config.
5. **Trace span**. The daemon MUST emit a `daemon.reload.applied` span (using the existing OpenTelemetry tracer wired at `cli/internal/runner/drain.go:142-160`) with attributes `trigger=merge|signal|cli`, `workflow_digest_before`, `workflow_digest_after`, `sources_added`, `sources_removed`, `result=applied|rejected|rolled_back`.
6. **Manual rollback**. Operators MAY run `xylem daemon reload --rollback` to restore the previous snapshot. This is a **manual** path — there is no auto-heal because automatic rollback would fight with the `adapt-repo` cycle.

`reload-log.jsonl` schema:

```json
{
  "timestamp": "2026-04-09T12:00:00Z",
  "trigger": "merge|signal|cli",
  "result": "applied|rejected|rolled_back",
  "workflow_digest_before": "sha256:...",
  "workflow_digest_after": "sha256:...",
  "sources_added": ["pr-lifecycle"],
  "sources_removed": [],
  "validation_error": ""
}
```

### 10.4 Workflow snapshot

The runner currently loads workflow YAML fresh for every phase. This is safe today only because workflow files rarely change mid-vessel; once reload exists, it becomes unsafe.

Add a new `WorkflowDigest` field to the `Vessel` struct:

```go
// cli/internal/queue/vessel.go (proposed addition)

type Vessel struct {
    // ... existing fields ...
    WorkflowDigest string `json:"workflow_digest,omitempty"` // sha256 of frozen workflow YAML at launch
}
```

Launch-time behavior (proposed):

1. When the runner launches a vessel, it MUST compute a sha256 digest of the effective workflow YAML (including any overlay composition) and set `WorkflowDigest` on the vessel.
2. The runner MUST copy the frozen workflow YAML to `.xylem/state/vessels/<id>/workflow.snapshot.yaml`. Subsequent phase executions within the vessel MUST load workflow definitions from this snapshot rather than from `.xylem/workflows/`.
3. The intermediary MUST read the class from the snapshot, not from disk, so class assignment is immune to mid-vessel workflow edits.
4. On vessel completion (any terminal state), the snapshot MUST be preserved for the lifetime of the vessel's phase artifacts so `review` and `lessons` can reconstruct the exact workflow the vessel ran against.

This snapshot is also a prerequisite for the remediation-aware gating in the failed-vessel recovery spec §6.4 — `workflow_digest` is one of the four inputs to the composite remediation fingerprint. The two features share the field.

Backward compatibility: vessels created before this field exists treat `WorkflowDigest == ""` as "no snapshot; load from disk" and continue to behave as they do today. No migration is required.

### 10.5 Merge trigger integration

Extend the existing `github-merge` source to act as a reload trigger:

1. The `github-merge` source MUST expose a new `on_control_plane_merge` callback that fires when a merged PR touched any file matching `.xylem.yml`, `.xylem/workflows/**`, `.xylem/prompts/**`, or `.xylem/HARNESS.md`.
2. When the callback fires, the daemon MUST emit a reload intent via an internal channel (preferred) or a file sentinel at `.xylem/state/reload.pending` (fallback for cross-process scenarios).
3. The reload consumer MUST debounce reload intents so multiple rapid merges within a 30-second window collapse to a single reload.
4. The reload intent MUST carry the merge commit SHA and PR number so the reload-log entry is traceable back to the originating merge.

Failed-vessel recovery Phase 1 (PR #219, landed on main) already persists failure artifacts under `cli/internal/recovery/recovery.go`. The reload trigger MUST NOT interfere with failure artifact writes; reload runs as a daemon-level operation and does not touch `.xylem/state/phases/`.

---

## 11. PR lifecycle generalization

### 11.1 Missing triggers in github-pr-events

The `github-pr-events` source at `cli/internal/source/github_pr_events.go:132-267` currently supports `labels`, `review_submitted`, `checks_failed`, and `commented`. It does NOT support `pr_opened` or `pr_head_updated`, which means xylem cannot proactively review a newly opened PR without label choreography.

Proposed extensions to `PREventsTask` (`cli/internal/source/github_pr_events.go:15-27`):

```go
type PREventsTask struct {
    Workflow        string
    Labels          []string
    ReviewSubmitted bool
    ChecksFailed    bool
    Commented       bool
    PROpened        bool      // NEW
    PRHeadUpdated   bool      // NEW
    AuthorAllow     []string
    AuthorDeny      []string
    Debounce        time.Duration // NEW (see §11.2)
}
```

And to `PREventsConfig` (`cli/internal/config/config.go:109-122`):

```go
type PREventsConfig struct {
    Labels          []string `yaml:"labels,omitempty"`
    ReviewSubmitted bool     `yaml:"review_submitted,omitempty"`
    ChecksFailed    bool     `yaml:"checks_failed,omitempty"`
    Commented       bool     `yaml:"commented,omitempty"`
    PROpened        bool     `yaml:"pr_opened,omitempty"`       // NEW
    PRHeadUpdated   bool     `yaml:"pr_head_updated,omitempty"` // NEW
    AuthorAllow     []string `yaml:"author_allow,omitempty"`
    AuthorDeny      []string `yaml:"author_deny,omitempty"`
    Debounce        string   `yaml:"debounce,omitempty"`        // NEW, Go duration string
}
```

Dedup contracts:

- `pr_opened` MUST dedupe per `pr_number` (at most one event per PR open, ever).
- `pr_head_updated` MUST dedupe per `(pr_number, head_sha)` pair. The existing `checks_failed` dedup by head SHA at `cli/internal/source/github_pr_events.go:314-320` is the reference pattern; the new trigger MUST reuse the same state file with a distinct key namespace.

### 11.2 Debouncing

Add per-trigger debouncing so rapid push sequences do not trigger one vessel per push:

- Default debounce for `pr_head_updated` MUST be `10m`.
- Default debounce for `pr_opened` MUST be `0` (fire immediately).
- Default debounce for `review_submitted`, `checks_failed`, `commented` MUST be `0` (fire immediately).
- Users MAY override any default via the task's `debounce:` field.

Debounce state MUST be persisted per `(source, trigger, pr_number)` in `.xylem/state/pr-events/debounce.json`. The scanner MUST check this file on every tick and skip any trigger whose last emit time plus debounce interval is in the future.

### 11.3 Scanner budget gating

Before enqueuing a new vessel, the scanner MUST consult the cost budget (§12). The cost package is already wired into the runner (`cli/internal/runner/runner.go:21`, budget alerts and cost-report persistence at line 1459, token estimation at lines 2380-2382). The extension is a new `cost.BudgetGate.Check(class)` helper exported from `cli/internal/cost/`:

```go
// cli/internal/cost/budgetgate.go (proposed)
package cost

type BudgetGate struct {
    tracker *Tracker
    cfg     *BudgetConfig
}

func (g *BudgetGate) Check(class string) Decision
// returns Decision{Allowed bool, Reason string, Remaining float64}
```

Scanner behavior (before any source-level enqueue):

1. Compute the workflow class for the candidate vessel.
2. Call `BudgetGate.Check(class)`.
3. If exhausted, emit a `budget.skipped` audit event with `{class, vessel_source, reason, remaining_usd}` and leave the source unchanged. The source's dedup state MUST NOT advance so the candidate is re-evaluated on the next scan tick.
4. If allowed, proceed with the normal enqueue path.

### 11.4 Parameterized validation block

Current state: three workflows hard-code Go-specific validation gates.

- `.xylem/workflows/fix-pr-checks.yaml:18` — `cd cli && goimports -l . | grep -c . | xargs test 0 -eq && go vet ./... && go build ./cmd/xylem && go test ./...`
- `.xylem/workflows/resolve-conflicts.yaml:18` — `git fetch origin main && ... && cd cli && go vet ./... && go build ./cmd/xylem && go test ./...` (also assumes the default branch is `main`)

These MUST be replaced with a parameterized block in `.xylem.yml`:

```yaml
validation:
  working_dir: "."         # optional; defaults to repo root
  default_branch: "main"   # optional; consumed by resolve-conflicts
  format:
    - "goimports -l ."
  lint:
    - "go vet ./..."
  build:
    - "go build ./..."
  test:
    - "go test ./..."
```

Each slot is a list so mixed-stack repos (Go backend + TS frontend) can declare both. Workflows render these via Go templates:

```yaml
gate:
  type: command
  run: |
    {{- range .Validation.Format }}{{ . }} && {{ end -}}
    {{- range .Validation.Lint   }}{{ . }} && {{ end -}}
    {{- range .Validation.Build  }}{{ . }} && {{ end -}}
    {{- range .Validation.Test   }}{{ . }}{{ if not (last) }} && {{ end }}{{ end -}}
  retries: 3
```

`fix-pr-checks`, `resolve-conflicts`, and `adapt-repo`'s phase 6 (`verify`) MUST all consume `{{.Validation.*}}` rather than hard-coding Go commands. The core profile MUST ship blank validation lists; `adapt-repo` populates them based on detection.

### 11.5 Parameterized merge

Current state: `merge-pr.yaml:18` hard-codes `gh pr merge {{.Issue.Number}} --repo nicholls-inc/xylem --squash --delete-branch --auto`. The `--repo` flag MUST be templated.

Proposed template variable:

```yaml
# merge-pr.yaml (updated)
phases:
  - name: merge
    type: command
    run: "gh pr merge {{.Issue.Number}} --repo {{.Repo.Slug}} --squash --delete-branch --auto"
```

The new `.Repo.Slug` variable MUST be rendered by the runner from the PR's source `repo` config field. When multiple GitHub sources are configured, the runner MUST use the source that owns the current vessel.

Automerge extraction: the daemon auto-merge path at `cli/cmd/xylem/automerge.go:22-23` hard-codes `harnessImplLabel = "harness-impl"` and `readyToMergeLabel = "ready-to-merge"`, and the `xylemBranchPattern` regex at line 16 (`^(feat|fix|chore)/issue-\d+`) hard-codes xylem's branch naming. These MUST be replaced by three new `DaemonConfig` fields:

```go
// cli/internal/config/config.go (proposed additions to DaemonConfig at lines 151-167)

type DaemonConfig struct {
    // ... existing fields unchanged ...
    AutoMergeLabels        []string `yaml:"auto_merge_labels,omitempty"`        // NEW
    AutoMergeBranchPattern string   `yaml:"auto_merge_branch_pattern,omitempty"` // NEW, regex
    AutoMergeReviewer      string   `yaml:"auto_merge_reviewer,omitempty"`      // NEW
}
```

The current xylem-specific constants (`harnessImplLabel`, `readyToMergeLabel`, `xylemBranchPattern`, `copilotReviewerLogin` at `cli/cmd/xylem/automerge.go:19`) MUST NOT appear anywhere in the core profile. They MAY ship as the default values of these fields inside the `self-hosting-xylem` overlay (§7) for backward compatibility with the xylem repo itself.

### 11.6 Default merge policy

The core profile MUST ship with merge strictly label-gated. Specifically:

- `auto_merge` MUST default to `false` in `DaemonConfig` (already the case per `cli/internal/config/config.go:163`).
- Enabling `auto_merge` requires the `ops` class to exist and the target workflow to declare `class: ops`. Config validation MUST reject `auto_merge: true` on any profile where no workflow declares `class: ops`.
- The `ready-to-merge` label MUST remain the primary merge trigger. No other label may be silently treated as a merge signal.
- Merge workflows MUST NOT be scheduled — they are always event-driven.

### 11.7 Review-pr PR-open trigger

With `pr_opened` and `pr_head_updated` available, the core profile's PR review story becomes:

- `review-pr` workflow MUST be hooked to `pr_opened` with `debounce: 0` — exactly one vessel per PR open.
- `review-pr` MAY also be hooked to `pr_head_updated` with `debounce: 10m` — at most one review per 10-minute window per head SHA.
- `fix-pr-checks` MUST be hooked to `checks_failed` (unchanged) and SHOULD also be hooked to `pr_head_updated` with `debounce: 10m` for repos that want preemptive fixes before CI runs.
- `respond-to-pr-review` MUST be hooked to `review_submitted` with mandatory `author_deny` of the configured auto-review bot (reviewer loop prevention — the existing contract).

The core profile's `.xylem.yml` snippet:

```yaml
sources:
  pr-lifecycle:
    type: github-pr-events
    tasks:
      review:
        workflow: review-pr
        on:
          pr_opened: true
          pr_head_updated: true
          debounce: "10m"
      fix-checks:
        workflow: fix-pr-checks
        on:
          checks_failed: true
          pr_head_updated: true
          debounce: "10m"
      respond:
        workflow: respond-to-pr-review
        on:
          review_submitted: true
          author_deny: ["copilot-pull-request-reviewer[bot]"]
```

---

## 12. Cost and budget controls

### 12.1 Per-repo daily budget

Add a top-level `cost.daily_budget_usd` block to `.xylem.yml`:

```yaml
cost:
  daily_budget_usd: 50
  per_class_limit:
    delivery: 40
    harness-maintenance: 5
    ops: 5
  on_exceeded: drain_only   # drain in-flight, stop enqueuing
```

Semantics:

- `daily_budget_usd` is a **soft** ceiling. `0` means "no limit" (fail-safe default for unmetered API keys).
- `per_class_limit` values MUST sum to `<= daily_budget_usd` when the total budget is non-zero. Config validation MUST reject an oversubscribed per-class allocation.
- `on_exceeded` MUST be one of: `drain_only` (default: allow in-flight to finish, block new enqueue), `pause` (freeze everything until next reset), `alert` (emit alert but continue enqueuing). Unknown values are a validation error.
- Budget rollover is daily at UTC midnight. Operators MAY override the reset timezone via `cost.reset_timezone` (proposed; default UTC).

### 12.2 Scanner budget check

The scanner integration point is the new `cost.BudgetGate.Check(class)` helper introduced in §11.3. Reuse, don't duplicate.

Ordering constraint: the budget gate MUST run **before** any source-level dedup state advances so that a budget-skipped candidate is re-evaluated cleanly on the next tick. This matches the existing source contract where dedup advancement is tied to successful enqueue.

### 12.3 PR event debouncing as soft budget control

The per-trigger debouncing from §11.2 is the primary cost control for the PR lifecycle loop. In practice, debouncing costs nothing when PR activity is low and saves tens of vessels per day on active PRs. Budget gating is the backstop for the pathological case where debouncing is insufficient.

### 12.4 `xylem adapt-repo --dry-run`

`xylem adapt-repo` is not yet a CLI subcommand — `adapt-repo` runs as a workflow under the scanner today. This spec proposes adding `xylem adapt-repo` as an operator-facing CLI that enqueues a single adapt-repo vessel and tails its output. The `--dry-run` flag MUST:

1. Run phases 1–4 only (`analyze`, `legibility`, `plan`, `validate`).
2. Skip phases 5–7 (`apply`, `verify`, `pr`).
3. Print the final `adapt-plan.json` to stdout.
4. Write nothing to `.xylem/` state beyond the bootstrap subtree.

`--dry-run` MUST be safe to run against a repo with an active adapt-repo run in progress — the dry run creates a distinct vessel lineage and does not share state with the in-flight run.

### 12.5 Per-workflow cost alerts

When a single workflow class exceeds 2x its weekly moving average for 3 consecutive days, the daemon SHOULD file an alert issue via the existing alert mechanism (the same path used by `workflow-health-report` in §6 of the companion sections). Alert issue contract:

- Title: `[xylem] cost anomaly: <class> exceeded 2x weekly average for 3 days`
- Labels: `xylem-alert`, `needs-triage`
- Body: moving-average chart (plain-text), per-workflow breakdown, link to `.xylem/state/cost/history.json`.
- Dedup: at most one open alert issue per (class, week). Closed alerts MUST NOT be re-filed within the same week.

The alert is advisory only. It does not block enqueue; `on_exceeded` remains the enforcement knob.

### 12.6 Fail-safe defaults

The core profile MUST ship with:

- `cost.daily_budget_usd: 0` (no limit).
- No `per_class_limit` entries.
- `cost.on_exceeded: drain_only` as the default behavior if ever enabled.

`xylem init` SHOULD prompt for a budget on first run if an interactive TTY is attached. The prompt is optional — non-interactive environments (CI) MUST NOT block on it.

Budget exhaustion is a **soft** failure at all levels:

- Vessels already running continue to completion. The budget check is at enqueue time only.
- New vessels are blocked with a structured reason that is visible in `xylem status` output.
- Operators MAY raise the limit without restarting the daemon — the budget value MUST be read on each scan tick, not cached at startup.
- A raised limit MUST take effect on the next tick without any reload or signal.

This is the opposite of the class policy matrix (which is a hard safety boundary). Budget is an operational throttle, not a security control.
## 13. Security, audit, blast radius

### 13.1 Primary risk

The biggest new attack surface introduced by this plan is that `harness-maintenance` class workflows can rewrite the agent's own control plane. Without guardrails, a prompt-injected or miscalibrated vessel running `adapt-repo`, `implement-harness`, or `lessons` could ship a PR that relaxes its own permissions, changes its own workflows, or disables its own observability.

The primary mitigation is the workflow class policy matrix defined in §9. Everything in §13 is a supporting control layered underneath that matrix — defence in depth, not an alternative.

### 13.2 Blast radius controls

| Control | Mechanism | File reference |
|---|---|---|
| Worktree isolation | Filesystem writes are contained to a per-vessel worktree until a PR is pushed; `failed` and `cancelled` vessels never mutate the default branch | `cli/internal/worktree/` |
| No direct default-branch commits from harness-maintenance | Git layer MUST refuse a `git push` targeting the default branch when the running workflow declares `class: harness-maintenance`; runner evaluates via `policy.Evaluate(class, OpCommitDefaultBranch)` from §9 | runner push step, new `cli/internal/policy/class.go` from §18 A.3 |
| Secret read denylist | Sandbox denies reads of `.env*`, `~/.aws`, `~/.ssh`, `~/.gnupg`, `~/.password-store`, `~/.config/op`, `~/.docker/config.json`, `~/.netrc`, `~/.kube/config`, and any path matching a `secrets` category in the repo's `.gitignore` | Mirrors the existing top-level Claude Code sandbox denylist in this repo's CLAUDE.md |
| Restricted network egress | Harness engineering default is a small allowlist: `github.com`, `api.github.com`, `proxy.golang.org`, language-specific package registries required by the adapted stack. Everything else MUST be blocked | Profile `core` egress defaults (§5) |
| Default merge policy is human-only | `daemon.auto_merge` defaults to `false` in the `core` profile. Only the `ops` class can enable it, and only when the operator explicitly opts in per repository. The xylem self-hosting overlay (§7) is the only profile that flips the default, and it remains opt-in | `cli/internal/config/config.go` `Daemon.AutoMerge` |
| Harness-maintenance MUST PR | Enforced mechanically via policy matrix (§9) at `OpCommitDefaultBranch`; cannot be bypassed by workflow YAML tweaks | §9 policy matrix |

Every control above has a fail-closed default: if the policy package cannot be loaded, every operation MUST be treated as denied for `harness-maintenance` and `ops` classes.

### 13.3 Audit extensions

The existing intermediary audit log at `cli/internal/intermediary/intermediary.go:71-113` already provides an append-only JSONL log with file locking and an `AuditEntry` struct (`intermediary.go:63-69`). This spec extends it without breaking the existing format.

**New fields on `intermediary.AuditEntry`:**

| Field | Type | Purpose |
|---|---|---|
| `workflow_class` | `string` | `delivery` / `harness-maintenance` / `ops` — which class the vessel was running |
| `decision` | `string` | Matrix outcome: `allow` / `deny` / `require_approval` |
| `rule_matched` | `string` | Name of the policy rule that produced the decision, from `policy.Decision.Rule` |
| `file_path` | `string` | For file operations, the absolute path targeted |
| `operation` | `string` | `policy.Operation` value: `write_control_plane`, `commit_default_branch`, `push_branch`, `create_pr`, `merge_pr`, `reload_daemon`, `read_secrets` |
| `vessel_id` | `string` | Queue vessel identifier, for cross-reference against `.xylem/state/phases/<vessel>/summary.json` |

All new fields are `omitempty` so legacy entries parse unchanged.

**New CLI command: `xylem audit`**

- Grep-friendly, not a dashboard.
- `xylem audit tail` streams the last N entries.
- `xylem audit denied --since 24h` lists denied operations in a window.
- `xylem audit counts --by class` shows per-class operation counts.
- `xylem audit rule <rule-name>` shows violation rate for one policy rule.

Output is plain lines, suitable for piping to `jq`, `rg`, or shell filters.

**Daemon reload audit entries.** Every daemon reload (§10) MUST emit one audit entry with the following fields: `operation: reload_daemon`, `trigger` (`merge`, `manual`, `rollback`), `before_digest`, `after_digest`, `diff_summary` (short human-readable bullet list of changed workflows and config keys). Failed reloads MUST emit an audit entry with `decision: deny` and an explanation.

**adapt-repo audit entries.** Every `adapt-repo` run MUST emit one audit entry per planned change in `adapt-plan.json`, including changes that were skipped for idempotency or for failing validation. Skipped entries carry `decision: allow` with `rule_matched: adapt_repo.idempotency`. This makes the full plan reviewable from the audit log alone, not just from the PR diff.

### 13.4 Reconciling with SoTA §4.9

SoTA §4.9 says: "no writes to agent configuration/instruction files, even inside the workspace." On its face this is incompatible with a self-modifying harness. This spec resolves the tension by construction — the same resolution appears from the configuration-management angle in §9.7, repeated here from the security angle for clarity.

The invariant preserved is: **the running configuration is never rewritten by a vessel**. The full argument:

1. The **live** config — the one the running daemon is currently using — is loaded once at daemon start (§10) and is not rewritten by any vessel.
2. All config-touching writes from `harness-maintenance` vessels go to a feature branch inside a per-vessel worktree (§13.2).
3. The merge of that feature branch is a separate, `ops`-class, label-gated action. An operator (or, in the self-hosting overlay only, the `ops`-class merge workflow acting on an explicit `ready-to-merge` label) authorizes it.
4. The daemon reload (§10) happens only after the merge lands, and it swaps scanner config and workflow definitions atomically with a frozen snapshot for in-flight vessels.

"Configuration" in §4.9 is therefore interpreted as "the running configuration". The running configuration is mutated only by a merge that a human explicitly approved (or, in the self-hosting overlay, by an `ops` workflow acting on an explicit human-defined label). No vessel has the authority to mutate the live config on its own. This is enforced **mechanically** via the policy matrix (§9), not by convention.

### 13.5 Threat scenarios and response

| Scenario | Detection | Response |
|---|---|---|
| Prompt-injected `delivery`-class vessel tries to edit `.xylem.yml` to relax policy | `policy.Evaluate(Delivery, OpWriteControlPlane)` returns `{Allowed: false, Rule: "delivery.no_control_plane_writes"}` at the intermediary layer | Write is blocked; audit entry written with `decision: deny`; vessel fails fast in the current phase; recovery classifier (§16 Phase 6) marks the failure as `harness_gap` if the pattern repeats |
| `harness-maintenance` vessel tries to `git push origin main` directly | Runner refuses the push based on `policy.Evaluate(HarnessMaintenance, OpCommitDefaultBranch) == deny` | Push fails; audit entry written; vessel fails with a clear error; `failure-review.json` (§16 Phase 6) records the policy rule |
| Compromised upstream changes `adapt-repo` prompts | Change arrives via a normal PR; policy matrix denies any in-vessel write to `.xylem/prompts/adapt-repo/`; change is visible in `xylem audit denied` and in the PR diff | Rollback via `git revert` on the merged commit plus `xylem daemon reload --rollback` to the previous profile snapshot |
| Runaway cost scenario (e.g., misconfigured debounce causes `pr_head_updated` storm) | Scanner budget gate (§12) halts new enqueues; per-class budget exhaustion triggers an alert | Existing in-flight vessels drain; operator raises budget or fixes the misconfiguration; no daemon restart required (budget is consulted on each scan tick per §12) |
| Fail-open on policy package load failure | Runner boot-time self-check MUST call `policy.Evaluate` with known-bad inputs and assert the expected `deny` answer | If the self-check fails, the daemon refuses to start for any class that is not `delivery` |

Every row in this table MUST be covered by a test case in §15.

---

## 14. Migration plan for the xylem repo itself

The xylem repo's existing `.xylem.yml` is the most complex test case for this spec. Migration happens in-tree, incrementally, without breaking the running daemon and without freezing self-hosted development.

### 14.1 Step sequence

Each step is its own PR. Steps 1 and 2 are safe to land ahead of the rest of the plan and do not block parallel work. Steps 3 through 7 SHOULD land in order.

| Step | Action | Branch / PR scope |
|---|---|---|
| 1 | Land control plane / runtime state split (§5.2). Move runtime state from `.xylem/` flat layout to `.xylem/state/` subdirectory; keep the existing daemon compatible with both paths for one release | Feature branch; merge after smoke verification |
| 2 | Add `profile.lock` and the `profiles:` field to the config schema as no-op config (`cli/internal/config/config.go`). Neither is consumed by the loader yet | Small additive PR |
| 3 | Implement profile loading with precedence rules (§5.3). Run the legacy loader and the profile-composed loader in parallel and diff the resulting effective configs until the diff is clean on the xylem repo and all fixture repos (§15.2) | Separate PR; parallel run ships behind a feature flag |
| 4 | Extract xylem-specific workflows (`implement-harness.yaml`, `sota-gap-analysis.yaml`, `unblock-wave.yaml`, plus the xylem-only sources `harness-impl`, `harness-pr-lifecycle`, `harness-merge`, `conflict-resolution`, `sota-gap`, `harness-post-merge`) to `cli/internal/profiles/self-hosting-xylem/workflows/` and the overlay's embed.FS. Leave copies in `.xylem/workflows/` temporarily; the legacy loader still picks them up | One PR per workflow is acceptable |
| 5 | Flip the xylem repo to profile-driven config. Set `profiles: [core, self-hosting-xylem]` in `.xylem.yml`, delete the duplicate workflow copies from `.xylem/workflows/`, verify in a worktree first. This is the point of no return for the legacy loader on this repo | Single PR; gated on step-3 parallel diff being clean for 7 days |
| 6 | Verify self-hosting overlay reproduces current behavior. Run `xylem review` before and after the flip and diff the reports; any delta outside noise is a regression | Observability-only; no config changes |
| 7 | Remove the compatibility shim for the old `.xylem/` flat layout. After this lands, the legacy loader is gone | Cleanup PR |

### 14.2 Per-step acceptance criterion

Each step has a single, measurable acceptance criterion that MUST be satisfied before the next step begins:

| Step | Acceptance criterion |
|---|---|
| 1 | `xylem daemon` runs unchanged against both the legacy `.xylem/` layout and the new `.xylem/state/` layout; existing tests pass; no daemon restart needed during the flip |
| 2 | `xylem config validate` parses `profile.lock` and `profiles:` without error; no behavior change |
| 3 | Effective-config diff between legacy loader and profile composer is empty on the xylem repo and all 8 fixtures from §15.2 for 7 consecutive days of CI runs |
| 4 | Each extracted workflow is loadable via both paths (legacy `.xylem/workflows/` and `profiles/self-hosting-xylem/workflows/`) and produces identical `workflow_digest` values |
| 5 | Queue drained to zero in-flight vessels immediately before the flip; daemon reloads cleanly; first post-flip scan enqueues identical vessels to what the legacy path would have enqueued |
| 6 | `xylem review` parity within tolerance defined in §15.3 test case 7 |
| 7 | Legacy loader code paths removed from `cli/internal/config/` and `cli/internal/workflow/`; `go test ./...` passes |

### 14.3 Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Profile composer precedence diverges from runtime merge semantics, self-modification silently breaks | High | Step 3 parallel-run diff gate; a 7-day clean-diff window; refuse to proceed to step 4 until clean |
| In-flight vessels at the moment of flip hold stale workflow refs | Medium | Drain queue to zero before step 5; freeze scanner by pausing `daemon.auto_upgrade` for the duration of the flip |
| `daemon.auto_upgrade` pulls a binary that expects the old layout mid-flip | Medium | Gate the flip on a xylem version tag; the binary MUST contain both layouts before the flip PR can merge; post-flip, the compatibility shim stays in for step 7 |
| Self-hosting-specific label taxonomy (`harness-impl`, `xylem-failed`, `pr-vessel-active`, `harness-post-merge`, `[sota-gap]`) gets moved out of the core profile but the scanner still expects it | Medium | Overlay profile MUST re-declare the label taxonomy; parallel-run diff in step 3 catches any label drift |
| Step 5 flip triggers daemon scanner stall or queue corruption | Low | Every step is individually revertible; worktree-first verification in step 5; rollback command `xylem daemon reload --rollback` available |

### 14.4 Rollback

Every step is individually revertible by `git revert`. No step makes irreversible state changes to the xylem repo:

- Steps 1, 2, 3, 6 are pure additions or read-only verifications.
- Step 4 leaves duplicate copies, so reverting the extraction PR leaves the repo functional.
- Step 5 deletes the duplicate copies; reverting the PR restores them from git, and the legacy loader (still present until step 7) picks them up.
- Step 7 is the only irreversible step. It MUST land only after step 6 has demonstrated clean parity for at least one full xylem self-hosting cycle (scan → drain → merge → reload).

If a step-5 flip fails in production, the operator runs `xylem daemon reload --rollback` to revert to the pre-flip profile snapshot, then git-reverts the PR.

---

## 15. Test strategy

### 15.1 Hosting

The existing smoke-scenario framework at `docs/design/harness-smoke-scenarios/` is the right host for this spec's end-to-end tests. That directory already exists and defines the fixture pattern used by prior harness work. This spec extends it with a profiles-specific fixture set and a new workflow-class enforcement scenario.

Unit tests live under `cli/internal/profiles/`, `cli/internal/policy/`, and the extended packages in `cli/internal/runner/`, `cli/internal/scanner/`, and `cli/internal/intermediary/`.

### 15.2 Fixture repos

A new testdata directory `cli/internal/profiles/testdata/fixtures/` holds 8 representative repositories:

| Fixture | Stack | Expected `adapt-repo` output |
|---|---|---|
| `blank/` | Empty git repo with only a README | Minimal `AGENTS.md`; empty validation commands; warning issue filed |
| `go-lib/` | Single Go package | `goimports`, `go vet`, `go build`, `go test` gates |
| `go-cli/` | Multi-module Go | `go-lib` gates plus `go test ./...` with race detector |
| `python-pyproject/` | `pyproject.toml` + pytest | `ruff`, `mypy`, `pytest` gates |
| `node-nextjs/` | Next.js + pnpm | `pnpm lint`, `pnpm typecheck`, `pnpm build`, `pnpm test`, live browser gates |
| `mixed-go-ts/` | Go backend + TS frontend | Both stacks; separate per-stack gates in one workflow |
| `database-heavy/` | Postgres migrations + Go | Migration workflow plus DB live gates |
| `poorly-documented/` | Code with no `AGENTS.md`, `README.md`, or `docs/` | Generated stubs via `bootstrap/instructions.go` |

Each fixture MUST carry a committed golden file for the plan phase output, enabling `adapt-repo` regression tests.

### 15.3 Test cases

| # | Name | What it proves |
|---|---|---|
| 1 | Core profile smoke | `xylem init --profile=core` on each fixture produces the expected files; the new `xylem config validate` passes; `.xylem/profile.lock` contains the pinned profile version |
| 2 | Adapt-repo golden files | Running `adapt-repo` up to the `plan` phase on each fixture produces an `adapt-plan.json` that matches the committed golden; any drift is a diff in CI |
| 3 | Adapt-repo end-to-end | All 7 phases of `adapt-repo` run on a clone of each fixture; the resulting PR diff is inspected and compared to the golden PR manifest |
| 4 | Workflow class enforcement | A `delivery`-class workflow that attempts to edit `.xylem.yml` is denied by the policy matrix and writes the new audit entry fields from §13.3; the inverse case (`harness-maintenance` class writing to `.xylem.yml` inside a worktree) is allowed |
| 5 | Daemon reload | Daemon is started, a PR touching `.xylem.yml` is merged, reload fires, an in-flight vessel started before the reload completes against its frozen workflow snapshot |
| 6 | Budget gating | `cost.daily_budget_usd: 1` is set, multiple vessels are enqueued, the scanner drops the second with a `budget.skipped` audit entry as defined in §12 |
| 7 | Self-hosting parity | The xylem repo is loaded against `profiles: [core, self-hosting-xylem]`; `xylem review` output matches the pre-flip snapshot within documented noise tolerance (see §14.2 step 6) |
| 8 | Regression | Existing smoke scenarios in `docs/design/harness-smoke-scenarios/` pass unchanged |

Test case 4 is load-bearing for §13: failure to enforce the policy matrix makes the whole security model void. It MUST run on every PR, not just nightly.

### 15.4 Property-based tests

Property tests use `pgregory.net/rapid` and follow the `TestProp*` naming convention defined in CLAUDE.md. At minimum:

| Property | Package | Statement |
|---|---|---|
| `TestProp_ProfileCompositionCommutes` | `cli/internal/profiles` | For keys where composition is defined, profile composition commutes with `.xylem.yml` overrides: `compose(profiles) ⊕ overrides == overrides ⊕ compose(profiles)` |
| `TestProp_PolicyStableUnderReorder` | `cli/internal/policy` | Policy matrix decisions are invariant under rule reordering for any `(class, operation)` pair — there is no rule ordering ambiguity |
| `TestProp_AdaptPlanIdempotent` | `cli/internal/profiles` | `adapt-plan.json` is idempotent: planning twice on an already-adapted repo produces empty `planned_changes` |
| `TestProp_AuditEntryRoundTrip` | `cli/internal/intermediary` | The extended `AuditEntry` (§13.3) JSON-roundtrips for arbitrary field combinations, and omitting new fields preserves legacy parseability |

### 15.5 CI integration

All tests MUST pass under both `go test ./...` and `go test -race ./...` per CLAUDE.md conventions. `goimports -l .` MUST report no diffs. `go vet` MUST pass. Fixtures MUST NOT make real network calls; `adapt-repo` end-to-end tests use stubbed GitHub clients and local git operations only, matching the existing stub-based testing pattern in `cli/internal/source/` and `cli/internal/worktree/`.

---

## 16. Rollout plan with success criteria

### 16.1 Phase table

Each phase exits when its success criteria are met, not on a date. Phases 1 and 2 are on the critical path; 3 through 6 MAY overlap if implementer bandwidth permits; 7 is a hard gate before 8.

| # | Phase | Deliverable | Success criteria | Blocks |
|---|---|---|---|---|
| 1 | Control plane / runtime state split | §5.2 | `xylem daemon` runs unchanged on the xylem repo using new `.xylem/state/` layout; all existing tests pass; compatibility shim for legacy layout holds for one release cycle | All subsequent phases |
| 2 | Embedded `core` profile and richer `xylem init` | §5.3, §6 | `xylem init --profile=core` produces the 12 core workflows listed in §18 A.1; `xylem config validate` passes on all 8 fixtures from §15.2; smoke test on `blank/` fixture passes | Phases 3, 4 |
| 3 | Workflow classes and intermediary enforcement | §9 | Policy matrix from §9 is enforced on all core workflows; test case 4 from §15.3 passes in CI; every audit entry contains the new fields from §13.3; warn-only mode runs for 7 days with zero unexpected denies before flipping to enforce | Phases 4, 5 |
| 4 | `bootstrap` wired into `adapt-repo` | §8 | Phases 1 through 4 of `adapt-repo` produce `adapt-plan.json` on all 8 fixtures; golden-file tests from §15.3 test case 2 pass; structured plan matches the schema declared in §8 | Phase 5 |
| 5 | Generalized PR review/merge/conflict plus new triggers | §11 | `pr_opened` and `pr_head_updated` triggers fire exactly once per `(pr, head_sha)` under load; debouncing works; parameterized `fix-pr-checks`, `merge-pr`, and `resolve-conflicts` pass on `go-cli` and `node-nextjs` fixtures | Phase 6 |
| 6 | Daemon reload plus failed-vessel recovery Phase 2 | §10, recovery Phase 2 | Merge-triggered reload works per §10; in-flight vessels use frozen workflow snapshot; invalid config reload is rejected and logged; recovery Phase 1 (`failure-review.json` artifact) is already landed per commit `ad92e03` / PR #219, so this phase lands Phase 2 deterministic policy; scanner respects the remediation fingerprint | Phase 7 |
| 7 | First external rollout on 2 to 3 hand-picked repos | Full stack | Each repo's `adapt-repo` produces a merged PR within 24h of first daemon boot; zero policy violations in the audit log; cost stays below the per-repo budget configured in `.xylem.yml`; zero secrets leaked (verified via `xylem audit read_secrets` showing only allowed reads) | Phase 8 |
| 8 | Self-hosting overlay migration | §14 | xylem repo switches to `profiles: [core, self-hosting-xylem]` following §14 step sequence; `xylem review` parity within tolerance; original `.xylem.yml` reproduced by composition | Phase 9 |
| 9 | Failed-vessel recovery Phases 3 and 4 | Recovery Phases 3-4 | Diagnosis workflow lands for ambiguous failures; remediation-aware scanner gating replaces source-only gating; all acceptance criteria from the failed-vessel recovery spec are met | — |

### 16.2 Exit criteria

A phase MUST NOT be declared complete until every success criterion in its row is verified. Phases 1 and 2 are the only critical-path items — if either slips, everything downstream slips. Phase 7 is a hard gate before Phase 8: the xylem repo's own migration MUST NOT begin until at least two external repos are running cleanly on the core profile for one full weekly cycle.

---

## 17. Risks and open questions

### 17.1 Risks

| Risk | Severity | Mitigation |
|---|---|---|
| `adapt-repo` makes incorrect detections on unusual stacks (e.g., monorepo with nested build systems) | High | Golden-file tests per fixture (§15.2); `xylem adapt-repo --dry-run` (§12) for new repos; the plan is always reviewable as a PR before apply |
| Workflow class enforcement breaks xylem self-hosting mid-flight | High | Land class enforcement in warn-only mode first; flip to enforce only after 7 days of clean audit logs on the xylem repo (§16 Phase 3) |
| `pr_opened` trigger causes cost blowout in active repos | Medium | Debouncing per `(pr, head_sha)` (§11); per-class budget (§12); fail-safe skip on budget exhaustion |
| Daemon reload mid-vessel corrupts state | Medium | Workflow snapshot at vessel launch; reload only swaps scanner config, never in-flight state (§10) |
| External repos hit xylem-specific assumptions we missed | Medium | 8-fixture test matrix (§15.2); Phase 7 is a hard gate before Phase 8 |
| Migration flip drains the xylem daemon | Low | Planned drain-to-zero before the flip (§14.1 step 5); `xylem daemon reload --rollback` available |
| `bootstrap` package is stale relative to current xylem internals | Medium | Audit and rewrite the package during Phase 4 before wiring it into `adapt-repo` (§16) |
| SoTA §4.9 invariant is violated in a way reviewers miss | Medium | Every `harness-maintenance` PR description MUST cite §13.4 and explain how the invariant is preserved; the policy matrix enforces the invariant mechanically so reviewer vigilance is defence in depth, not the last line |
| Policy matrix denies a legitimate self-modification path during warn-only flip | Low | Warn-only mode precedes enforce; any unexpected warn is investigated before flipping (§16 Phase 3) |

### 17.2 Open questions

These are **proposals**, not decisions. The spec does not block on them; each open question has a recommended default.

1. **Older `.xylem/` layout on repos with existing xylem state.** Proposal: detect a `profile.lock` version mismatch and run in "upgrade-only" mode — diff the current state against the new profile and file an issue with recommendations rather than auto-migrating.
2. **Non-GitHub forges.** Proposal: v1 is GitHub-only. `xylem init` fails fast with a clear error on non-GitHub remotes. A GitLab or Gitea backend is a separate effort.
3. **Multi-tenant daemon process.** Proposal: out of scope for v1. Assume one daemon per repo (or per worktree) until a concrete request arrives.
4. **Secret injection.** Proposal: `.xylem.yml` MAY reference `${ENV_VAR}`; `xylem daemon` refuses to start if any referenced variable is missing. `adapt-repo` MAY propose a `.xylem/secrets.example` file but MUST NOT write real values.
5. **`workflow-health-report` auto-file behaviour.** Proposal: yes, auto-file, rate-limited to one open issue per repo per week. Closed issues are never re-filed within the same week. The workflow exits with `XYLEM_NOOP` if an open report already exists.
6. **`adapt-repo` human approval gate on subsequent runs.** Proposal: no for the first run — the PR is the gate. Yes for subsequent runs when a prior adaptation is still open, to avoid clobbering unmerged work.
7. **`ops` class handling of `xylem auto_merge --allow-admin`.** Proposal: introduce an `ops.admin_merge` sub-policy with explicit opt-in per repo. The existing xylem self-hosting overlay would be the first consumer.
8. **Profile composition with multiple overlays.** Proposal: yes, `profiles: [core, overlay-a, overlay-b]` composes left to right, later overlays winning. Name conflicts in workflows are a validation error surfaced by `xylem config validate`.

### 17.3 Dependencies on other in-flight specs

This spec is not self-contained. It interacts with two sibling specs currently in flight:

- **Failed Vessel Recovery Spec** (`docs/design/failed-vessel-recovery-spec.md`). Phase 1 (artifact-only `failure-review.json`) is already landed per commit `ad92e03` and PR #219. Phases 2 through 4 (deterministic policy, diagnosis workflow, remediation-aware gating) are open and integrate directly with this spec's §16 Phase 6 and Phase 9. The `failure-review.json` artifact is the authoritative source the policy matrix and audit log should consult for recovery decisions.
- **Xylem Harness Impl Spec** (`docs/design/xylem-harness-impl-spec.md`). WS1 (protected surfaces) is the closest sibling. This spec's §9 workflow-class taxonomy supersedes WS1's glob-list-of-protected-surfaces approach: the class matrix is a strictly more expressive model. On migration, WS1's `DefaultProtectedSurfaces` list (currently empty at `cli/internal/config/config.go:55` per the audit) is replaced by class-based rules. `HarnessConfig.ProtectedSurfaces` remains for backwards compatibility and for repos that want to keep the glob model. Both can coexist; the class matrix MUST take precedence when both are configured.

---

## 18. Appendix: concrete snippets

### A.1 `xylem init --profile=core` scaffold output

The exact stdout produced by a clean `xylem init --profile=core` run on a repo with no existing `.xylem/` directory:

```
Created .xylem.yml
Ensured .xylem/ directory exists
Created .xylem/.gitignore
Created .xylem/HARNESS.md
Created .xylem/profile.lock
Created .xylem/workflows/fix-bug.yaml
Created .xylem/workflows/implement-feature.yaml
Created .xylem/workflows/triage.yaml
Created .xylem/workflows/refine-issue.yaml
Created .xylem/workflows/review-pr.yaml
Created .xylem/workflows/respond-to-pr-review.yaml
Created .xylem/workflows/fix-pr-checks.yaml
Created .xylem/workflows/merge-pr.yaml
Created .xylem/workflows/resolve-conflicts.yaml
Created .xylem/workflows/lessons.yaml
Created .xylem/workflows/context-weight-audit.yaml
Created .xylem/workflows/adapt-repo.yaml
Created .xylem/prompts/... (12 workflows × phases)

Next steps:
  1. Edit .xylem.yml to confirm your repo and labels
  2. Start the daemon:     xylem daemon
  3. The first daemon tick will create a [xylem] adapt harness issue
  4. Merge the resulting PR to activate repo-specific adaptations
```

Note: the current `xylem init` at `cli/cmd/xylem/init.go` scaffolds only `fix-bug` + `implement-feature` from inline string constants and writes `.xylem/.gitignore` as `"*\n!.gitignore\n"` — a "control plane hidden from git" default that is wrong for external repos. Phase 2 (§16) rewrites this to use `embed.FS` against the `core` profile and flips the default `.gitignore` to track control plane assets while excluding runtime state (`.xylem/state/`, `.xylem/phases/`, `.xylem/locks/`, `.xylem/traces/`, `.xylem/reviews/`, `.xylem/schedules/`, `.xylem/dtu/`, `.xylem/queue.jsonl`, `.xylem/daemon.pid`).

### A.2 `adapt-repo.yaml` full YAML

```yaml
name: adapt-repo
class: harness-maintenance
description: Analyze the target repo and propose a reviewable harness update PR.
allow_additive_protected_writes: true
allow_canonical_protected_writes: false
phases:
  - name: analyze
    type: command
    run: xylem bootstrap analyze-repo --output .xylem/state/bootstrap/repo-analysis.json

  - name: legibility
    type: command
    run: xylem bootstrap audit-legibility --output .xylem/state/bootstrap/legibility-report.json

  - name: plan
    prompt_file: .xylem/prompts/adapt-repo/plan.md
    max_turns: 40
    depends_on:
      - analyze
      - legibility

  - name: validate
    type: command
    run: |
      set -euo pipefail
      xylem config validate --proposed .xylem/state/bootstrap/adapt-plan.json
      xylem workflow validate --proposed .xylem/state/bootstrap/adapt-plan.json
    depends_on:
      - plan

  - name: apply
    prompt_file: .xylem/prompts/adapt-repo/apply.md
    max_turns: 30
    depends_on:
      - validate

  - name: verify
    type: command
    run: xylem validation run --from-config
    depends_on:
      - apply

  - name: pr
    prompt_file: .xylem/prompts/adapt-repo/pr.md
    max_turns: 20
    depends_on:
      - verify
```

`analyze` and `legibility` are deterministic command phases that MUST run before any LLM phase, per §8's deterministic-first principle. The `validate` and `verify` command phases are the backstops that keep the LLM's plan and apply phases honest.

### A.3 Workflow-class policy package surface

Proposed location: `cli/internal/policy/class.go`. This package is referenced across §9, §13, §15, and §16 but does not yet exist in the codebase.

```go
// cli/internal/policy/class.go
package policy

// Class represents a workflow's permission class.
// Introduced by the productize-xylem spec (§9) to replace ad-hoc protected-surface
// glob lists with a mechanically-enforced policy matrix.
type Class string

const (
    Delivery           Class = "delivery"
    HarnessMaintenance Class = "harness-maintenance"
    Ops                Class = "ops"
)

// Operation represents a policy-checked action the runner intends to take on
// behalf of a vessel. The intermediary evaluates (Class, Operation) pairs
// before allowing the runner to proceed.
type Operation string

const (
    OpWriteControlPlane   Operation = "write_control_plane"
    OpCommitDefaultBranch Operation = "commit_default_branch"
    OpPushBranch          Operation = "push_branch"
    OpCreatePR            Operation = "create_pr"
    OpMergePR             Operation = "merge_pr"
    OpReloadDaemon        Operation = "reload_daemon"
    OpReadSecrets         Operation = "read_secrets"
)

// Decision is the outcome of evaluating a (Class, Operation) pair.
// Rule is the stable identifier of the policy rule that produced the
// decision, used for audit-log correlation.
type Decision struct {
    Allowed bool
    Rule    string
    Audit   bool
}

// Evaluate returns the policy decision for a (class, operation) pair.
// Evaluate MUST be pure, deterministic, and stable under rule reordering
// (TestProp_PolicyStableUnderReorder from §15.4 enforces this).
//
// Fail-closed: if class is unknown, Evaluate returns a Decision with
// Allowed=false and Rule="policy.unknown_class". The runner's boot-time
// self-check (§13.5) relies on this behaviour.
func Evaluate(class Class, op Operation) Decision {
    // Implementation populates a static matrix; see §9 for the full table.
    // ...
}
```

The `workflow.Workflow` struct at `cli/internal/workflow/workflow.go:21` gains a new optional `Class` field:

```go
type Workflow struct {
    Name                          string  `yaml:"name"`
    Description                   string  `yaml:"description,omitempty"`
    Class                         string  `yaml:"class,omitempty"` // NEW: policy.Class; defaults to "delivery"
    LLM                           *string `yaml:"llm,omitempty"`
    Model                         *string `yaml:"model,omitempty"`
    AllowAdditiveProtectedWrites  bool    `yaml:"allow_additive_protected_writes,omitempty"`
    AllowCanonicalProtectedWrites bool    `yaml:"allow_canonical_protected_writes,omitempty"`
    Phases                        []Phase `yaml:"phases"`
}
```

`Class` defaults to `delivery` when omitted — safe-closed: unclassified workflows MUST NOT write to the control plane.

### A.4 `workflow-health-report` scheduled source

```yaml
sources:
  workflow-health:
    type: scheduled
    schedule: "@weekly"
    tasks:
      report:
        workflow: workflow-health-report
```

The `workflow-health-report` workflow wraps `runner/health` output (available today via `xylem doctor health --json`) into a GitHub issue titled `[xylem] weekly workflow health` with sections for anomaly counts, failed vessel classes, retry outcomes, and cost trends. Per §17.2 open question 5, closed issues MUST NOT be re-filed within the same week; the workflow exits with `XYLEM_NOOP` if an open report already exists.

### A.5 Minimal `profile.lock`

```yaml
profile_version: 1
profiles:
  - name: core
    version: 1
  # Uncomment to enable the xylem self-hosting overlay
  # - name: self-hosting-xylem
  #   version: 1
locked_at: 2026-04-09T00:00:00Z
```

The `profile.lock` file is **tracked in git** alongside `.xylem.yml`. Drift between `.xylem.yml` `profiles:` and `profile.lock` is a `xylem config validate` error — operators MUST explicitly re-lock via `xylem profile lock` after changing the profile list.

### A.6 `validation:` block YAML (referenced from §6.4 and §11.4)

The `validation` block declares the language-specific checks `adapt-repo` installs and that workflows like `fix-pr-checks` consume. It replaces hard-coded per-workflow `run:` commands with a single source of truth per repo.

```yaml
validation:
  commands:
    format:
      run: goimports -l .
      fail_if_stdout_nonempty: true
    vet:
      run: go vet ./...
    build:
      run: go build ./...
    test:
      run: go test ./...
    test_race:
      run: go test -race ./...
  live_gates:
    - name: pnpm-lint
      run: pnpm lint
      when:
        stack: node
    - name: pytest
      run: pytest
      when:
        stack: python
  required_for:
    fix-pr-checks:
      - format
      - vet
      - build
      - test
    resolve-conflicts:
      - format
      - build
      - test
    implement-feature:
      - format
      - vet
      - test
```

`adapt-repo` writes this block based on its stack detection (§8); `fix-pr-checks` and `resolve-conflicts` read the `required_for.<workflow>` list and run those commands without knowing the underlying stack. This is the mechanism that makes those workflows "cross-repo reusable" per the audit finding.

### A.7 Example `harness.policy.rules` tightening

A repo that wants stricter defaults than the `core` profile provides MAY append additional deny rules without replacing the whole matrix:

```yaml
harness:
  policy:
    rules:
      # Core profile already denies delivery→write_control_plane; tighten by
      # also denying delivery→create_pr when the PR targets the default branch
      # on a protected directory (e.g., to force all delivery work through
      # feature-specific subdirectories).
      - action: create_pr
        resource: "refs/heads/main:docs/policy/**"
        effect: deny
      # Prevent any class from reloading the daemon unless a human approves,
      # useful in repos where daemon uptime is operationally load-bearing.
      - action: reload_daemon
        resource: "*"
        effect: require_approval
      # Tighten secret-read protection beyond the default denylist by
      # adding a repo-specific pattern.
      - action: read_secrets
        resource: "infra/terraform/*.tfvars"
        effect: deny
```

Tightening rules are always **additive**: a repo-level rule can only make the effective policy stricter than the profile default, never looser. `xylem config validate` MUST reject any rule that would widen a profile-level deny.

### A.8 Example audit log entry with new fields

A single line from `.xylem/state/audit.jsonl` after the §13.3 extensions land, showing a denied `write_control_plane` attempt by a `delivery`-class vessel:

```json
{"intent":{"action":"write","resource":".xylem.yml"},"decision":"deny","timestamp":"2026-04-09T14:22:07Z","approved_by":"","error":"policy.delivery.no_control_plane_writes","workflow_class":"delivery","rule_matched":"delivery.no_control_plane_writes","file_path":"/worktrees/vessel-issue-42/.xylem.yml","operation":"write_control_plane","vessel_id":"issue-42"}
```

Contrast with an allowed `write_control_plane` from a `harness-maintenance` vessel operating inside its worktree:

```json
{"intent":{"action":"write","resource":".xylem/workflows/fix-bug.yaml"},"decision":"allow","timestamp":"2026-04-09T14:23:11Z","approved_by":"","error":"","workflow_class":"harness-maintenance","rule_matched":"harness_maintenance.worktree_writes_allowed","file_path":"/worktrees/vessel-adapt-repo-1/.xylem/workflows/fix-bug.yaml","operation":"write_control_plane","vessel_id":"adapt-repo-1"}
```

And a daemon reload entry emitted after a merged PR triggered a config reload:

```json
{"intent":{"action":"reload","resource":"daemon"},"decision":"allow","timestamp":"2026-04-09T14:25:00Z","approved_by":"","error":"","workflow_class":"ops","rule_matched":"ops.reload_on_merge","file_path":".xylem.yml","operation":"reload_daemon","vessel_id":"","trigger":"merge","before_digest":"sha256:a1b2c3...","after_digest":"sha256:d4e5f6...","diff_summary":"workflows: fix-bug updated; config: cost.daily_budget_usd 40→50"}
```

These three entries are greppable via `xylem audit denied`, `xylem audit counts --by class`, and `xylem audit rule ops.reload_on_merge` respectively, matching the CLI surface defined in §13.3.
