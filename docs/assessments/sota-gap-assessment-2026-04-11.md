# SoTA Harness Gap Assessment — xylem, 2026-04-11

**Scope.** Systematic gap assessment of xylem against three specs:
(A) `docs/best-practices/harness-engineering.md` (harness-engineering topic
guide; `[Established]` vs `[Emerging]`), (B) `docs/design/sota-agent-harness-spec.md`
(SoTA harness spec — Part I "Validated SoTA" with MUST/SHOULD language, Part II
"Predictions" treated as directional), and (C) `docs/design/productize-xylem-for-external-repos-spec.md`
(productize spec — §4 baseline frozen 2026-04-09; §5–§14 forward plan).

**Tree snapshot.** `origin/main` @ `375ed8da801f` ("validation.lint/build/test
run `go` from repo root…", PR #377). All code citations resolve against that
commit unless noted. **The current working branch is `hn/tmp`, which has
locally deleted `cli/cmd/xylem/daemon_reload.go`, `daemon_reload_prop_test.go`,
`daemon_reload_test.go`, `harden.go`, `recovery_cmd.go`, `release_cadence.go`
and several related test files, plus the `.xylem/workflows/backlog-refinement.yaml`
and `release-cadence.yaml` workflow files. These deletions are NOT shipped.
Every assessment below uses origin/main as the source of truth.**

**Daemon snapshot.** PID 1343, binary `375ed8da801f`, last upgrade
2026-04-11T18:18:28Z, updated 2026-04-11T18:27:08Z
(`.daemon-root/.xylem/state/daemon-health.json:1-6`). Queue live at
`.daemon-root/.xylem/queue.jsonl` (178 vessels). Audit log at
`.daemon-root/.xylem/audit.jsonl` (547 entries, 547 allows, 0 denies).

## 1. Executive summary

**Top-5 ship-blockers for external productization.**

1. **Policy class matrix is not enforced on phase writes.** `policy.Class` and
   `policy.Evaluate` exist and are consulted only by `daemon_reload.go` for
   `OpReloadDaemon`. The runner's phase-level enforcement at
   `cli/internal/runner/runner.go:3013-3036` (`enforcePhasePolicy`) calls
   `r.Intermediary.Evaluate(intent)` against glob-based rules, not the class
   matrix; `policy.Evaluate(class, op)` is never called for
   `OpWriteControlPlane`, `OpCommitDefaultBranch`, `OpPushBranch`,
   `OpCreatePR`, `OpMergePR`, or `OpReadSecrets`. PRODUCTIZE §9 mechanical
   enforcement is missing. In-flight fix: PR #366 (CONFLICTING, 4-deep retry
   chain).
2. **AuditEntry lacks workflow_class / policy_rule / rule_matched / file_path
   / operation / vessel_id fields.** `cli/internal/intermediary/intermediary.go:63-68`
   still defines `AuditEntry{Intent, Decision, Timestamp, ApprovedBy, Error}`.
   Production audit log confirms zero entries carry any of the new fields
   (`grep -c workflow_class .daemon-root/.xylem/audit.jsonl = 0`). PRODUCTIZE
   §9.5/§13.3 audit extension is missing.
3. **Daemon reload is wired but is BLOCKED in production by a self-inflicted
   workflow validation error.** Every merge-triggered reload since
   2026-04-11T15:06Z has been rejected with
   `validate workflow ".../diagnose-failures.yaml": phase name "create-issues"
   is invalid; must start with a lowercase letter and contain only lowercase
   letters, digits, and underscores`
   (`.daemon-root/.xylem/state/daemon-reload-audit.jsonl:1-3`,
   `daemon-reload-log.jsonl:1-3`). The phase name hyphen is in both
   `.daemon-root/.xylem/workflows/diagnose-failures.yaml:7` and the profile
   source at `cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml:7`.
   The validator regex is `^[a-z][a-z0-9_]*$`
   (`cli/internal/workflow/workflow.go:20`). Fix is a one-character rename;
   until it lands, every post-merge config update rolls back and the daemon
   serves a stale control plane.
4. **BudgetGate is a permissive no-op.** `cli/internal/cost/budgetgate.go:25-33`:
   `func (g *BudgetGate) Check(_ string) Decision { return Decision{Allowed: true} }`.
   Wired into scanner at `cli/internal/scanner/scanner.go:49,79,110-114` but
   the integration seam is a stub. No per-class daily budget exists in
   `CostConfig` (`cli/internal/config/config.go:268-275` — only `MaxCostUSD`
   and `MaxTokens`). PRODUCTIZE §12.1 (`daily_budget_usd`, `per_class_limit`,
   `on_exceeded`) is absent. Cost alerts exist as files on disk
   (`cost-report.json`, `budget-alerts.json`) but total_cost_usd reports 0
   (`…/pr-366-resolve-conflicts/cost-report.json:7`).
5. **xylem is not yet self-hosted on its own profile composition.** The
   on-tree `.xylem.yml` is still the flat inline config
   (`.xylem.yml:1-155` — 10 source blocks, hard-coded `nicholls-inc/xylem`,
   xylem-specific labels like `harness-impl` / `xylem-failed` /
   `pr-vessel-active` / `sota-gap`). The `profiles:` field is absent.
   PRODUCTIZE §14 migration Phase 1-7 is unstarted.

**Top-5 operational risks.**

1. **Retry-chain cascade on PRs #366 and #372.** Queue state histogram
   (`.daemon-root/.xylem/queue.jsonl` @ 178 lines): 55 cancelled, 45 failed,
   30 timed_out, 46 completed, 2 running — a 26% completion rate. 57 vessels
   carry `retry-1` / `retry-2` / ... suffixes (32% of the queue).
   `pr-366-resolve-conflicts-retry-1-retry-1-retry-1-retry-1` and
   `pr-366-merge-pr-retry-1-retry-1-retry-1-retry-1` are the critical path —
   each attempt re-runs the same real merge conflict against
   `cli/internal/config/config_prop_test.go` and
   `cli/internal/runner/runner_prop_test.go`
   (`pr-366-resolve-conflicts-retry-1-retry-1-retry-1-retry-1/merge_main.output:8-16`).
2. **Reload block compounds with PR #366 stuck chain.** The three consecutive
   rejected reloads (PRs #371, #374, #377 at 15:06Z, 16:06Z, 17:33Z) were
   the specific fixes that would unblock resolve-conflicts. Because reload is
   rejected, the daemon is running the pre-#371 resolve-conflicts YAML and
   re-running the same failure.
3. **Evidence claim levels are untyped in production.** `evidence-manifest.json`
   files exist for only 9 of 62 phase dirs (15% coverage). Every recorded
   claim has `"level": "untyped"` and `"trust_boundary": "No trust boundary
   declared"` (`…/issue-236/evidence-manifest.json:5-48`). SoTA §4.6 "MUST
   record which category supports each important completion claim" is
   formally satisfied in schema but operationally empty — no claim is
   proved/mechanically/behaviorally/observed.
4. **Audit log is pure "allow" — zero denies in 547 entries.** The
   intermediary's default rule set is effectively permissive for the
   self-hosting overlay. When combined with the missing class-matrix
   enforcement, this means no write / commit / push / pr_create has ever been
   blocked at policy time. Every policy signal is post-hoc / review-gate.
5. **Dormant harness library (9 packages) is still dormant.** `ctxmgr`,
   `catalog`, `memory`, `mission`, `evaluator`, `signal`, `skills`, `cadence`,
   `policy` — these have zero production import sites from `runner`, `scanner`,
   or `cmd/xylem` (except `policy`, which is imported only by
   `daemon_reload.go` and by the `workflow.Class` type alias). This is the
   exact risk the productize spec §4.2 flagged; it has not been addressed.

**Top-5 fastest wins.**

1. **One-character fix: rename `diagnose-failures.yaml` phase `create-issues`
   to `create_issues`.** Update both the profile source
   (`cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml:7`)
   and the live copy (`.daemon-root/.xylem/workflows/diagnose-failures.yaml:7`).
   Also rename the prompt file path. Unblocks every future merge-triggered
   reload.
2. **Land PR #366 (workflow class enforcement).** Already CONFLICTING.
   Wires `policy.Evaluate` into the runner's phase enforcement path and
   extends `AuditEntry` — this resolves ship-blockers #1 and #2 in one PR.
3. **Land PR #372 (generator/evaluator loops).** Already CONFLICTING. Wires
   the dormant `evaluator` package into the runner phase loop — directly
   closes SoTA §4.6 / harness-engineering §9 evaluator separation and
   PRODUCTIZE §8 phase-6 evaluator expectation.
4. **Set non-`untyped` evidence levels at gate claim creation.** The claim
   builder at `cli/internal/runner/runner.go:2961-2993` already switches on
   gate type; extend the `default:` branch to classify command gates as
   `evidence.BehaviorallyChecked` and live gates as `evidence.ObservedInSitu`
   (the `live` branch is already wired at line 2974).
5. **Add `validation.working_dir` and cross-module-prefix scripts to the
   `core` profile template.** PR #377 was the third consecutive fix for this —
   the fact that three production reloads were needed for `goimports -l ./cli/...`
   / `go vet ./cli/...` / `go build ./cli/cmd/xylem` indicates the core
   profile's validation template is still Go-flat; shipping a `working_dir`
   awareness in `ValidationConfig` (`cli/internal/config/config.go:268`) would
   prevent recurrence.

## 2. Methodology and evidence inventory

### Phase 1 — spec absorption

Read end-to-end: `docs/best-practices/harness-engineering.md` (421 lines),
`docs/design/sota-agent-harness-spec.md` (622 lines),
`docs/design/productize-xylem-for-external-repos-spec.md` (1841 lines).
Extracted every `MUST` / `SHOULD` / `[Established]` / `[Emerging]` anchor.

### Phase 2 — baseline re-verification

Primary delta: the productize spec §4 baseline was frozen 2026-04-09 and the
tree has moved substantially. Re-verified every §4.1 / §4.2 / §4.3 / §4.4 /
§4.5 claim against `git show origin/main:<path>` — findings in §3 below.

### Phase 3 — capability survey

Three parallel Explore agents audited:

1. Profiles, policy, bootstrap, init, self-hosting overlay.
2. Daemon reload, PR triggers, validation, cost, state split, adapt-repo.
3. SoTA §4.1–§4.11 capabilities, dormant-vs-wired package inventory.

Each agent returned file:line citations. I spot-verified every contested
claim against origin/main directly — specifically: (a) whether `daemon_reload.go`
exists (one agent said "NOT FOUND" — they searched `hn/tmp` where it's
locally deleted; verified present on origin/main at 730 lines), (b) whether
`AuditEntry` has extended fields (zero new fields on origin/main —
`cli/internal/intermediary/intermediary.go:63-68`), (c) whether the runner
calls `policy.Evaluate` (it does not — `enforcePhasePolicy` at
`cli/internal/runner/runner.go:3013-3036` delegates to the Intermediary glob
policy only).

### Phase 4 — operational evidence

Read or grep-sampled:

- `.daemon-root/.xylem/queue.jsonl` (178 lines, state histogram computed).
- `.daemon-root/.xylem/audit.jsonl` (547 lines, action/decision histograms).
- `.daemon-root/.xylem/state/daemon-health.json` (daemon pid, binary, upgrade).
- `.daemon-root/.xylem/state/daemon-reload-audit.jsonl` (3 entries, all deny).
- `.daemon-root/.xylem/state/daemon-reload-log.jsonl` (3 entries, all rejected).
- `.daemon-root/.xylem/state/daemon-control-plane-current.json` (snapshot of
  live workflow digests).
- `.daemon-root/.xylem/state/sota-gap-snapshot.json` (prior art from
  2026-04-09: 10 capability entries — context, tools, memory, orchestration,
  verification, evaluation, observability, security, entropy, cost).
- `.daemon-root/.xylem/state/phases/*/evidence-manifest.json` (9 files).
- `.daemon-root/.xylem/state/phases/*/cost-report.json` (29 files).
- `.daemon-root/.xylem/state/phases/*/summary.json` (29 files).
- `.daemon-root/.xylem/state/phases/*/failure-review.json` (18 files).
- `gh issue list --state open --limit 100` (snapshot below).
- `gh pr list --state open --limit 30` (3 open PRs: #366 CONFLICTING,
  #372 CONFLICTING, #3 MERGEABLE).
- `git log --oneline origin/main --since="3 weeks ago"` (~85 commits).

### Prior art

`.daemon-root/.xylem/state/sota-gap-snapshot.json` is the existing
machine-readable gap report (schema at
`.daemon-root/.xylem/state/sota-gap-snapshot.schema.json`). It covers 10
capability keys. Where this assessment confirms, extends, or contradicts each
entry, it says so explicitly — this document is a follow-on, not a replacement.

### Constraints honoured

Read-only. No files edited, no daemon commands issued, no `doctor --fix`,
no `gh` writes, no protected-surface mutations. `gh` calls used `list` only.

## 3. Spec-baseline drift table — productize spec §4 claims vs current tree

The productize spec §4 froze the baseline at 2026-04-09. The tree has moved
enormously since then — most §4.3 "missing entirely" items are now shipped.
Every row where spec-and-reality diverge is listed below; rows that still
match the spec are summarised at the end.

| §4 claim (spec says) | Current tree shows | Evidence |
| --- | --- | --- |
| §4.2: `bootstrap` package is dormant; "no `cli/cmd/xylem/bootstrap.go` exists" | **Wired.** `cli/cmd/xylem/bootstrap.go` exists; registers `xylem bootstrap analyze-repo` and `xylem bootstrap audit-legibility`; called by `adapt-repo` workflow | `cli/cmd/xylem/root.go:92` registers `newBootstrapCmd()`; `cli/cmd/xylem/bootstrap.go:43-140`; `cli/internal/profiles/core/workflows/adapt-repo.yaml:6-11` |
| §4.3: "Embedded profile filesystem. `cli/internal/profiles/` does not exist" | **Wired.** `cli/internal/profiles/profiles.go` exists with `//go:embed core/** self-hosting-xylem/**`; `Compose()` merges profiles; init consumes profiles | `cli/internal/profiles/profiles.go:1-192`; `cli/cmd/xylem/init.go:82-141`; directory listing: 15 core workflows + 11 self-hosting-xylem workflows |
| §4.3: "Workflow class taxonomy … there is no `Class` field" | **Wired on the type.** `Workflow.Class policy.Class` field exists; defaults to `Delivery`; legacy boolean fall-back preserved | `cli/internal/workflow/workflow.go:22-36` (struct); `cli/internal/workflow/workflow.go:219-357` (defaulting and YAML parse); `cli/internal/policy/class.go:8-26` (Class/Operation enums) |
| §4.3: "`adapt-repo` workflow. No such workflow exists" | **Wired.** `cli/internal/profiles/core/workflows/adapt-repo.yaml` exists with all 7 phases (analyze, legibility, plan, validate, apply, verify, pr); daemon seeds the issue on startup | `cli/internal/profiles/core/workflows/adapt-repo.yaml:1-33`; `cli/cmd/xylem/adapt_repo_seed.go:45-100`; `cli/cmd/xylem/daemon.go:208` |
| §4.3: "Daemon reload. `daemon.go` has no generic config reload path" | **Wired.** `cli/cmd/xylem/daemon_reload.go` (730 lines) implements merge-triggered + SIGHUP + CLI + rollback; `newDaemonReloadCmd` registered; workflow-snapshot-freeze wired | origin/main: `cli/cmd/xylem/daemon_reload.go:89-123` (reload CLI), `daemon.go:93-95,193-195` (SIGHUP signal), `cli/internal/source/github_merge.go:26,75-81` (OnControlPlaneMerge callback), `cli/internal/source/control_plane.go:1-36` (path classifier). **But see ship-blocker #3 — reload is rejected in production.** |
| §4.3: "Bootstrap CLI surface. There is no `xylem bootstrap analyze-repo`" | **Wired.** Both subcommands exist and are called from `adapt-repo.yaml` | `cli/cmd/xylem/bootstrap.go:57-140` |
| §4.3: "`pr_opened` / `pr_head_updated` triggers … cannot fire on a newly opened PR" | **Wired.** Both triggers exist in config and source; per-trigger debounce; dedup by `(pr, head_sha)` | `cli/internal/config/config.go:138-153` (PREventsConfig); `cli/internal/source/github_pr_events.go:23-24,204-218`; `cli/internal/source/github_pr_events_debounce.go:45-55,101-148,223` (debounce state at `.xylem/state/pr-events/debounce.json`) |
| §4.3: "Parameterized PR workflows. `merge-pr`, `fix-pr-checks`, and `resolve-conflicts` hard-code the `nicholls-inc/xylem` repo slug and Go-specific validation commands" | **Partial.** `ValidationConfig` struct exists with `Format` / `Lint` / `Build` / `Test`; workflow YAMLs consume `{{.Validation.*}}` and `{{.Repo.Slug}}`; `DaemonConfig` gained `AutoMergeLabels` / `AutoMergeBranchPattern` / `AutoMergeReviewer` fields; `automerge.go` hard-coded constants removed. **Outstanding: validation block is a single top-level object rather than the proposed per-workflow `required_for` map (§A.6). PR #374, #375, #377 were live corrections to the gate script itself.** | `cli/internal/config/config.go:183-188` (ValidationConfig); `cli/internal/phase/phase.go:18-71` (TemplateData); `cli/internal/config/config.go:203-215,566-592` (DaemonConfig auto-merge fields) |
| §4.3: "`xylem config validate` / `xylem workflow validate` / `xylem doctor` / `xylem audit` / `xylem bootstrap` / `xylem adapt-repo` / `xylem daemon reload`. None exist." | **Three out of seven shipped.** `xylem doctor` (newDoctorCmd registered at `root.go:108`), `xylem bootstrap` (at `root.go:92`), `xylem daemon reload` (from `daemon_reload.go:89`). **Missing:** `xylem config validate`, `xylem workflow validate`, `xylem audit`, `xylem adapt-repo` (CLI wrapper). | `cli/cmd/xylem/root.go:90-115` |
| §4.4: "`xylem init` scaffolds `fix-bug` + `implement-feature` from inline string constants" | **Fixed.** `init` consumes profiles via `profiles.Compose()`; 13 core workflows + `adapt-repo` (15 core YAMLs total under `cli/internal/profiles/core/workflows/`); `--profile` flag accepts `core` or `core,self-hosting-xylem`; `--seed` flag wires `ensureAdaptRepoSeeded` | `cli/cmd/xylem/init.go:55-141`; `cli/cmd/xylem/init.go:82-141` (`cmdInitWithProfileAndOptions`); directory listing of `cli/internal/profiles/core/workflows/` |
| §4.4: "`.xylem/.gitignore` default = `"*\n!.gitignore\n"`" | **Fixed.** Default is now the control-plane-tracking pattern (`state/`, `*.lock`, `!profile.lock`) | `cli/cmd/xylem/init.go:25-29` (const `scaffoldGitignore`) |
| §4.4: "No `.xylem/profile.lock`" / "No `AGENTS.md` stub" / "No seeded `xylem-adapt-repo` issue" | **Partial.** `profile.lock` written at init and at seeding. `xylem-adapt-repo` issue is seeded by daemon startup. **AGENTS.md is still NOT emitted by init** — neither the `core` profile nor `self-hosting-xylem` ships an `AGENTS.md.tmpl`. | `cli/cmd/xylem/init.go:132-138` (profile.lock render); `cli/cmd/xylem/adapt_repo_seed.go:45-100`; `find cli/internal/profiles -name '*AGENTS*'` returns empty |
| §4.5: "Repo slug `nicholls-inc/xylem` hard-coded in `.xylem.yml` lines 7, 20, 33, ..." | **Still present — xylem has not migrated to profiles.** The on-tree `.xylem.yml` is still the 10-source flat layout with repeated `repo: nicholls-inc/xylem` | origin/main:`.xylem.yml:1-155` (10 source blocks) |
| §4.5: "`DefaultProtectedSurfaces = []string{}`" | **Still empty**, but `Workflow.Class` plus `AllowAdditiveProtectedWrites` / `AllowCanonicalProtectedWrites` remain the runtime guards. **Enforcement wiring has not yet moved from glob-policy to class-matrix (ship-blocker #1).** | `cli/internal/config/config.go:55`; `cli/internal/workflow/workflow.go:26-27` |
| §5.2: Control plane / runtime state split (".xylem/ → .xylem/state/") | **Partial.** `RuntimePath` helper at `cli/internal/config/config.go:377-388` resolves `state_dir/state/<elems>` for runtime paths; phases live under `.xylem/state/phases/`. **BUT `queue.jsonl` and `audit.jsonl` still live at the flat `.xylem/` root**, not under `state/`. | `cli/cmd/xylem/root.go:73` (`filepath.Join(cfg.StateDir, "queue.jsonl")` → `.xylem/queue.jsonl`, not `.xylem/state/queue.jsonl`); production: `.daemon-root/.xylem/queue.jsonl` (present) vs `.daemon-root/.xylem/state/queue.jsonl` (absent) |
| §5.3: `cli/internal/profiles/` does not exist | **Wired** (see §4.3 row above) | — |
| §5.4: `.xylem.yml` schema gains `profiles:` field | **Schema supports it** (`Config.Profiles []string` at `cli/internal/config/config.go:61`), **but xylem's own `.xylem.yml` does not use it** (ship-blocker #5) | `cli/internal/config/config.go:61`; `.xylem.yml:1-3` (no `profiles:` key) |

**Rows where §4 is still accurate (no drift):**

- §4.1 The `orchestrator`, `cost`, `intermediary`, `observability`, `evidence`,
  `surface`, and `recovery` packages are all imported from
  `cli/internal/runner/` and are wired into production (confirmed via grep at
  `cli/internal/runner/runner.go:21-35`).
- §4.2 The `mission`, `memory`, `signal`, `evaluator`, `ctxmgr`, `catalog`
  packages are still dormant: zero production import sites from `runner`,
  `scanner`, or `cmd/xylem` (§5 table). Productize §4.2 list is otherwise
  accurate.
- §4.5 `daemon.auto_upgrade: true` is still wired from `.xylem.yml:153` →
  `cli/cmd/xylem/upgrade.go` and assumes `github.com/nicholls-inc/xylem` →
  overlay-only still holds.

## 4. Capability matrix

Columns: `spec` = cite, `language` = MUST/SHOULD/MAY or `[Established]`/`[Emerging]`,
`presence` = `missing` / `dormant` / `wired-partial` / `wired-broken` / `wired-healthy`,
`code ev.` = file:line, `ops ev.` = audit.jsonl/queue.jsonl/issue/PR citation,
`gap class` = `MUST` / `SHOULD` / `MAY` / `[Emerging]`, `next step` = smallest
actionable change.

### 4A. SoTA §4 coverage

| Capability | Spec | Language | Presence | Code evidence | Ops evidence | Gap class | Smallest next step |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Repository-as-knowledge-base | SoTA §4.1 | MUST | wired-partial | `cli/cmd/xylem/init.go:141` (`syncProfileAssets` writes `HARNESS.md`, workflows, prompts); `cli/internal/profiles/core/HARNESS.md.tmpl` exists | HARNESS.md in tree at `.xylem/HARNESS.md`; docs/ progressive-disclosure structure present | SHOULD (MUST for AGENTS.md specifically) | Add `AGENTS.md.tmpl` to `cli/internal/profiles/core/` and render in init. Issue #241 tracks |
| Short root instruction file (not monolithic AGENTS.md) | SoTA §4.1 | MUST (MUST NOT monolithic) | wired-partial | `.xylem/HARNESS.md` is ~150 lines; no root `AGENTS.md`; `docs/` directory has progressive-disclosure layout (docs/design/, docs/decisions/, docs/plans/) | `ls docs/design/` shows 10+ specs; no AGENTS.md at repo root | SHOULD | Emit `AGENTS.md` stub pointing to HARNESS.md + docs/ from init |
| Context as compiled view (ctxmgr) | SoTA §4.2 | MUST | dormant | `cli/internal/ctxmgr/ctxmgr.go:17-50` exists (Strategy, Segment, Window types); **zero import sites** from runner/scanner/cmd | sota-gap-snapshot.json §1 "context" entry marks dormant w/ priority 7 | MUST | Thread `ctxmgr.Compact` into `phase.RenderPrompt` for long-running vessels (issue #60) |
| Minimize context volume, maximize signal | SoTA §4.2 | MUST | wired-partial | Prompt assembly is Go `text/template` at `cli/internal/phase/phase.go:110` (`RenderPrompt`); no compaction or selection pass | Phase prompts use flat `{{.Vessel.*}}` / `{{.Validation.*}}` interpolation; no window tracking in production | SHOULD | Introduce a compaction pass before `RenderPrompt` when prompts exceed a configured token budget |
| Write / Select / Compress / Isolate | SoTA §4.2 | SHOULD | wired-partial | Write = progress files under `.xylem/state/phases/<id>/`; Isolate = worktree per vessel (`cli/internal/worktree/`); Select and Compress absent | Phase artifacts (`analyze.output`, `analyze.prompt`, `summary.json`, `workflow/*.yaml`) present under `.daemon-root/.xylem/state/phases/` | SHOULD | Wire ctxmgr into Compress; wire memory package into Select |
| Tool plane (clear names, explicit parameters, documented failures) | SoTA §4.3 | MUST | wired-partial | `Workflow.AllowedTools` exists; no catalog, no permission scopes; tool contract lives implicitly in prompts | No `catalog` import sites; sota-gap-snapshot.json §2 "tools" entry marks dormant w/ priority 8 | SHOULD | Add catalog-based tool selection at phase launch; issue not yet filed |
| Just-in-time retrieval via tools | SoTA §4.3 | SHOULD | wired-healthy (for file retrieval) | Claude Code's native Grep/Glob/Read tools handle JIT file retrieval inside each phase | Claims derived from `evidence-manifest.json` show command gates using `go test ./...` etc. | `[Established]` | (no action — already matches) |
| MCP support | SoTA §4.3 | SHOULD | missing | No `mcp` import sites in `cli/`; no MCP server configurations in profiles | zero mentions in audit or config | SHOULD | Defer; external repos can configure MCP per Claude Code conventions outside xylem |
| Memory taxonomy (procedural/semantic/episodic) | SoTA §4.4 | MUST | dormant | `cli/internal/memory/memory.go:22-32` defines MemoryType enum; zero import sites from production code | sota-gap-snapshot.json §3 "memory" marks dormant w/ priority 7 | SHOULD | Issue #60 tracks; land alongside context compilation |
| Persist structured handoff artifacts | SoTA §4.4 | MUST | wired-partial | `.xylem/state/phases/<id>/summary.json` (29 files / 62 dirs), `failure-review.json` (18 files), `evidence-manifest.json` (9 files), `cost-report.json` (29 files) | 47% of phase dirs have summaries; 15% have evidence manifests; 29% have failure-review artifacts | SHOULD | Make summary+evidence+cost writes unconditional on phase completion (currently gated on phase type) |
| Per-user/per-session isolation; sanitize before persistence | SoTA §4.4 | MUST | wired-healthy | Per-vessel worktree isolation (`cli/internal/worktree/`) | Every vessel runs in an isolated worktree under `.daemon-root/.claude/worktrees/` | `[Established]` | (no action) |
| Orchestration modes (single / chain / routing / workers / evaluator / handoff) | SoTA §4.5 | SHOULD | wired-partial | `cli/internal/orchestrator/` wired via `cli/internal/runner/schedule.go:31-92` (wave-based DAG execution); `cli/internal/runner/runner.go:1918-1948` (wave concurrent runner); no evaluator or handoff modes | sota-gap-snapshot.json §4 "orchestration" marks wired w/ priority 4 | SHOULD | Wire `evaluator` package into the post-verify loop; PR #372 in flight (CONFLICTING) |
| Start simple, add orchestration only when justified | SoTA §4.5 | MUST | wired-healthy | Single-phase-per-LLM invocation; wave parallelism only when dependencies are independent | Phase artifact sample shows per-phase outputs, no multi-agent chatter | `[Established]` | (no action) |
| Generator / evaluator separation | SoTA §4.5, §4.6 | SHOULD | missing | `cli/internal/evaluator/` is dormant (zero import sites); spec §4.6 "SHOULD whenever task quality cannot be reliably measured by unit tests" | Issue #151 "harness: wire generator/evaluator loop into runner phases"; issue #313 "feat(runner): run evaluator loop on phases with evaluator config"; PR #372 CONFLICTING | SHOULD | Land PR #372; close issues #151, #313 |
| Verification layer (deterministic gates + live checks + evidence capture) | SoTA §4.6 | MUST | wired-partial | `cli/internal/runner/runner.go:2654` (`liveGate.Run`); `runner.go:2961-2993` (`buildGateClaim`); `runner.go:3008-3036` (protected surface snapshots) | Gate claims are built but `level: "untyped"` across all 9 evidence-manifest.json files sampled | MUST | Classify command-gate claims as `BehaviorallyChecked`, live-gate claims as `ObservedInSitu` — one-line fix at `runner.go:2988` default branch |
| Four evidence levels (proved / mechanically / behaviorally / observed) | SoTA §4.6 | SHOULD | wired-partial | `cli/internal/evidence/evidence.go:20-26` enum exists; `level: "live"` branch at `runner.go:2974` is the only one that classifies | `issue-236/evidence-manifest.json:7` — `"level": "untyped"` | MUST | See row above |
| Record which category supports each completion claim | SoTA §4.6 | MUST | wired-broken | Schema supports it; production claims are all `untyped` | 9/9 sampled evidence manifests show `untyped` | MUST | Wire claim levels at source (fastest win #4) |
| Specification quality: human-reviewable artifact before elevating trust | SoTA §4.6 | MUST (if formal tools used) | missing | No formal tools wired; `crosscheck` skills exist at session level, not in-tree | — | MUST (conditional) | N/A unless Dafny/Lean work lands in-tree |
| Evaluation layer wired into every harness change | SoTA §4.7 | MUST | wired-partial | `cli/internal/gapreport/`, `cli/cmd/xylem/gap_report.go` wired; `.xylem.yml:126-134` schedules `sota-gap-analysis` weekly | `.daemon-root/.xylem/state/sota-gap-snapshot.json` exists and is updated by scheduled vessels | SHOULD | Extend sota-gap-snapshot schema to include harness-variant comparisons |
| Regression datasets / trace-based benchmarks | SoTA §4.7 | MUST | wired-partial | `eval/` directory exists (`.xylem/eval/`, `cli/internal/dtu/testdata/manual-smoke/`); `dtu` framework provides smoke scenarios | No per-change eval-comparison reports in `.daemon-root/.xylem/state/`; issue #57 "Build the harness eval corpus and baseline comparison flow" still open | MUST | Issue #57 is the tracking handle |
| Comparison across harness variants on prompt / model / routing changes | SoTA §4.7 | MUST | missing | No variant-comparison tooling wired; `cli/internal/continuousimprovement/` does continuous-improvement scanning of commits, not variant eval | — | SHOULD | Track under issue #57 |
| OpenTelemetry instrumentation from day one | SoTA §4.8 | MUST | wired-healthy | `cli/internal/observability/` wired into `cli/internal/runner/runner.go:165-302` (drain span, vessel span, phase span, vessel-health, vessel-cost, recovery attributes) | `.xylem.yml:146-149` (observability.enabled, endpoint, sample_rate) | `[Established]` | (no action) |
| Per-task / per-worktree isolated environments | SoTA §4.8 | SHOULD | wired-healthy | Per-vessel worktree (`cli/internal/worktree/`); per-vessel phase output dir (`.xylem/state/phases/<id>/`) | Production phase dirs under `.daemon-root/.xylem/state/phases/` | `[Established]` | (no action) |
| Expose logs/metrics/traces to the agent | SoTA §4.8 | SHOULD | missing | No tool endpoint for agents to query spans; no LogQL/PromQL wiring; traces export to Jaeger only for human viewing | Issue #59 "Expose agent-readable runtime artifacts and budget controls" still open | SHOULD | Issue #59 is the tracking handle |
| Secrets / network / workspace sandbox | SoTA §4.9 | MUST | wired-partial | `cli/internal/intermediary/` wired; glob-based rules in `.xylem.yml harness.intent_policy` (implicit) | Audit log shows only `"allow"` decisions (547/547) — no production denies; issue #58 "Add runtime containment, network policy, and scoped secret handling" still open | MUST | Wire class-matrix (see ship-blocker #1) |
| No writes outside workspace, no writes to agent config | SoTA §4.9 | MUST | wired-broken | `DefaultProtectedSurfaces = []string{}` (`cli/internal/config/config.go:55`); class matrix not enforced at phase time | PR #366 in flight to enforce this | MUST | Land PR #366 |
| Human approval for high-risk / irreversible actions | SoTA §4.9 | MUST | wired-partial | Intermediary supports `RequireApproval` effect (`cli/internal/intermediary/intermediary.go:18-28`); not configured in any production rule set | `.xylem.yml` rules allow everything | MUST | Wire class-matrix `ops.admin_merge` sub-policy (PRODUCTIZE §17.2 open question 7) |
| Architectural invariants enforced mechanically | SoTA §4.10 | MUST | wired-partial | CI runs `goimports`, `go vet`, `go build`, `go test`; no dependency-linting rule enforcing the `cli/internal` ≠ `cli/cmd` boundary | .xylem/HARNESS.md:44 documents the boundary but no machine check | SHOULD | Add a `structcheck` or `depguard` rule to CI; not yet tracked |
| Entropy management / recurring cleanup agents | SoTA §4.10 | SHOULD | wired-healthy | `continuous-simplicity` workflow wired (PR #291); `doc-garden` scheduled vessel added in PR #370; `context-weight-audit` wired (SHOULD per spec) | `.xylem.yml` has scheduled `sota-gap`, `harness-post-merge`, `doc-garden` (verified against `.daemon-root/.xylem/workflows/`) | `[Established]` | (no action) |
| Cost measurement at run + task level | SoTA §4.11 | MUST | wired-partial | `cli/internal/cost/cost.go` (`UsageRecord`, `CostReport`, `Budget`); scanner calls `BudgetGate.Check(class)` at `cli/internal/scanner/scanner.go:79` | 29/62 phase dirs carry `cost-report.json` (47%); `by_role`, `by_purpose`, `by_model` maps present; **`total_cost_usd` is 0 in production** (`pr-366-resolve-conflicts/cost-report.json:7`) | SHOULD | Wire real USD pricing into `cost.Tracker`; add per-class daily budgets (ship-blocker #4) |
| Context compaction thresholds | SoTA §4.11 | SHOULD | missing | `ctxmgr.Compact` exists but unused | — | SHOULD | Thread ctxmgr into prompt build |
| Stable prompt prefixes for caching | SoTA §4.11 | SHOULD | missing | No stable prefix design in `RenderPrompt`; HARNESS.md is rendered per-phase as part of system prompt | — | SHOULD | Defer (low impact until token costs exceed run-time overhead) |
| Model routing / model ladders by task difficulty | SoTA §4.11 | SHOULD | wired-partial | `cli/internal/config/config.go:100-107` (`providers`, `tiers`, `TierRouting`); `cli/cmd/xylem/exec.go` supports provider fallback | PR #367 (de7b579) "tier-aware LLM routing with per-provider env and rate-limit fallback" merged 2026-04-11; queue shows `"tier":"med"` per vessel | `[Established]` | (no action) |
| Continuous harness pruning | SoTA §4.11 | MUST | wired-healthy | `continuous-simplicity` scheduled; `harness-gap-analysis` scheduled; open issue #235 currently flags un-pruned class-matrix glue | — | `[Established]` | (no action) |

### 4B. Productize §5–§14 coverage

| Capability | Spec | Presence | Code evidence | Ops evidence | Gap class | Smallest next step |
| --- | --- | --- | --- | --- | --- | --- |
| Three-layer model (core / adapt-repo / self-hosting-xylem) | §5.1 | wired-healthy | `cli/internal/profiles/core/` (15 workflows) and `cli/internal/profiles/self-hosting-xylem/` (11 workflows) with strict composition | — | MUST | — |
| Control plane / runtime state split | §5.2 | wired-partial | `config.RuntimePath()` resolves runtime paths under `state/`; phases, daemon-health, daemon-reload artifacts move to `state/` | `queue.jsonl` and `audit.jsonl` still at the flat `.xylem/` root in production | MUST | Move `queue.jsonl`, `audit.jsonl`, `daemon.pid`, `reviews/`, `schedules/`, `traces/`, `locks/` under `state/` |
| Embedded profile filesystem | §5.3 | wired-healthy | `cli/internal/profiles/profiles.go:1-192`; `embed.FS` with core + self-hosting-xylem | — | MUST | — |
| Profile composition (`profiles:` field) | §5.4 | wired-partial | `Config.Profiles` field exists; `Compose()` merges; on-disk `.xylem.yml` wins | **xylem's own `.xylem.yml` does not use `profiles:` yet** (1:1 drift with PRODUCTIZE §14 target) | MUST | Execute §14 migration step 1 on xylem itself |
| `Workflow.Class` precursor (AllowAdditive/Canonical) | §5.5 | wired-healthy | `cli/internal/workflow/workflow.go:22-36`; legacy boolean compat retained | — | MUST | — |
| Core profile workflow set (13 workflows) | §6.1 | wired-healthy | 15 workflow YAMLs under `cli/internal/profiles/core/workflows/` (includes `field-report` and `security-compliance` beyond the 13 in spec) | Live daemon workflows directory at `.daemon-root/.xylem/workflows/` | SHOULD | — |
| Generic sources block (labels: `bug, enhancement, ready-for-work, needs-triage, ...`) | §6.2 | wired-healthy | `cli/internal/profiles/core/xylem.yml.tmpl` template | — | SHOULD | — |
| Scheduled hygiene (lessons, context-weight-audit, workflow-health-report) | §6.3 | wired-partial | `lessons.yaml`, `context-weight-audit.yaml`, `workflow-health-report.yaml` exist in `core/workflows/` | The spec also expected one open issue per week rate-limit — not validated in ops signals | SHOULD | (no action) |
| `validation:` block (format/lint/build/test) | §6.4, §11.4 | wired-partial | `ValidationConfig` struct (`cli/internal/config/config.go:183-188`) has `Format Lint Build Test` (strings); `TemplateData.Validation` pipes into workflows | The richer `required_for.<workflow>` per-workflow wiring (§A.6) is not implemented; `working_dir` was added via PR #377 but via a full template rewrite, not a structured field | SHOULD | Add `ValidationConfig.WorkingDir string` and per-workflow `required_for` map |
| Self-hosting-xylem overlay inventory | §7.1 | wired-healthy | `cli/internal/profiles/self-hosting-xylem/workflows/` has 11 files (implement-harness, sota-gap-analysis, unblock-wave, diagnose-failures, continuous-improvement, continuous-simplicity, audit, initiative-tracker, metrics-collector, portfolio-analyst, ingest-field-reports); `xylem.overlay.yml` defines overlay sources | — | SHOULD | — |
| Self-hosting overlay migration (xylem's own `.xylem.yml` flips to `profiles:`) | §7, §14 | missing | xylem's `.xylem.yml:1-155` is still the flat 10-source inline config | — | MUST (for self-hosting clean-up) | Execute §14 steps 1-7 |
| adapt-repo workflow with 7 phases | §8.3 | wired-healthy | `cli/internal/profiles/core/workflows/adapt-repo.yaml:1-33` has all 7 phases | — | MUST | — |
| `adapt-repo` seeding (daemon + init flag) | §8.2 | wired-healthy | `cli/cmd/xylem/adapt_repo_seed.go:45-100`; daemon startup calls `ensureAdaptRepoSeeded` at `daemon.go:208`; `init --seed` at `init.go:58,144-156` | Marker file path `.xylem/state/bootstrap/adapt-repo-seeded.json` | MUST | — |
| `adapt-plan.json` schema (schema_version=1) | §8.4 | missing | No `cli/internal/profiles/core/schemas/adapt-plan.json` or Go struct matching the 7-field schema | — | MUST | Add schema + validator; adapt-plan is currently freeform within the phase-3 prompt |
| `xylem config validate [--proposed]` | §8.8 | missing | No `cli/cmd/xylem/config.go`; no command registered | — | MUST | Wrap `Config.Validate()` in a CLI subcommand |
| `xylem workflow validate [--proposed]` | §8.8 | missing | No `cli/cmd/xylem/workflow.go`; no command registered | — | MUST | Wrap `workflow.Load` + `Workflow.Validate()` |
| `xylem validation run --from-config` | §8.8 | missing | No `cli/cmd/xylem/validation.go` | — | SHOULD | Defer; `adapt-repo` verify phase currently runs the block inline |
| Workflow class policy matrix | §9.3, §9.4 | wired-partial | `cli/internal/policy/class.go:1-109` has `Class`, `Operation`, `Decision`, `Evaluate`, 41-rule default matrix | Only `daemon_reload.go:308` calls `policy.Evaluate(class, OpReloadDaemon)`; the runner's phase-policy path (`cli/internal/runner/runner.go:3013-3036`) delegates to `Intermediary.Evaluate(intent)` only | MUST | Land PR #366 |
| `cli/internal/policy/` new package | §9.4 | wired-healthy | See above | — | MUST | — |
| Intermediary extension (WorkflowClass, PolicyRule, RuleMatched) | §9.5, §13.3 | missing | `AuditEntry` has only `{Intent, Decision, Timestamp, ApprovedBy, Error}` (`cli/internal/intermediary/intermediary.go:63-68`) | `grep -c workflow_class .daemon-root/.xylem/audit.jsonl = 0` | MUST | Land PR #366 (includes this) |
| AllowAdditive/Canonical legacy compat | §9.6 | wired-healthy | `cli/internal/workflow/workflow.go:219-357` defaults `Class` from booleans | — | SHOULD | — |
| User override via `harness.policy.class_overrides` | §9.8 | missing | No `HarnessConfig.Policy` struct | — | SHOULD | Add during §9 enforcement wiring |
| Daemon reload (hybrid merge + SIGHUP + CLI) | §10.2 | wired-broken | Full implementation in `cli/cmd/xylem/daemon_reload.go` (730 lines on origin/main); SIGHUP handler at `daemon.go:93-95`; merge-trigger via `source.GithubMerge.OnControlPlaneMerge` at `github_merge.go:26,75-81`; CLI `xylem daemon reload` at `daemon_reload.go:89` | **3 production reload attempts, all rejected** (`.daemon-root/.xylem/state/daemon-reload-audit.jsonl:1-3`); root cause: invalid phase name in `diagnose-failures.yaml:7` | MUST | Rename phase `create-issues` → `create_issues` (ship-blocker #3) |
| Workflow snapshot per vessel | §10.4 | wired-healthy | `Vessel.WorkflowDigest` field at `cli/internal/queue/queue.go:65-91`; snapshot path `cli/internal/runner/runner.go:3682` (`workflowSnapshotPath`); snapshot file at `.xylem/state/phases/<id>/workflow/<name>.yaml` | Confirmed on disk: `.daemon-root/.xylem/state/phases/issue-378/workflow/implement-harness.yaml`, `.../pr-366-resolve-conflicts-retry-1-retry-1-retry-1-retry-1/workflow/resolve-conflicts.yaml` | MUST | — |
| `.xylem/state/reload-log.jsonl` schema | §10.3 | wired-healthy | Path: `config.RuntimePath(cfg.StateDir, "daemon-reload-log.jsonl")` at `daemon_reload.go:484` | Live at `.daemon-root/.xylem/state/daemon-reload-log.jsonl`; 3 entries | MUST | — |
| `xylem daemon reload --rollback` | §10.3 | wired-healthy | `daemon_reload.go:310` (`loadRollbackCandidate`) | Manual path; no rollback attempted in ops history | MAY | — |
| `pr_opened` / `pr_head_updated` triggers | §11.1 | wired-healthy | `PREventsTask.PROpened / PRHeadUpdated` at `cli/internal/source/github_pr_events.go:23-24`; handlers at `204-218`; dedup at `261, 288` | PR event debounce state under `.xylem/state/pr-events/debounce.json` | MUST | — |
| Per-trigger debouncing | §11.2 | wired-healthy | `effectivePREventDebounce` at `cli/internal/source/github_pr_events_debounce.go:45-55`; defaults `pr_head_updated=10m`, `pr_opened=0` | — | SHOULD | — |
| Scanner budget gate | §11.3, §12.2 | wired-broken | Scanner calls `cost.BudgetGate.Check(class)` at `cli/internal/scanner/scanner.go:79`; **BudgetGate.Check is a no-op stub** returning `Allowed: true` at `cli/internal/cost/budgetgate.go:23-34` | — | SHOULD | Replace stub with real tracker-backed check |
| Parameterized PR workflows (`{{.Validation.*}}` / `{{.Repo.Slug}}`) | §11.4, §11.5 | wired-partial | Template data wired via `cli/internal/phase/phase.go:18-71`; workflows consume `{{.Repo.Slug}}` (e.g. `merge-pr.yaml:18`) and `{{.Validation.*}}` (verify phases); `fix-pr-checks.yaml` partially parameterized but history shows repeated fixes (PR #374 / #377) | Three consecutive reload rejections were for fixes to the Go-specific validation template | SHOULD | Add `working_dir` support and per-workflow `required_for` map |
| Automerge decoupled from hard-coded labels | §11.5 | wired-healthy | `DaemonConfig.AutoMergeLabels`, `AutoMergeBranchPattern`, `AutoMergeReviewer` at `cli/internal/config/config.go:203-215` | Overlay still provides xylem-specific defaults via `.xylem.yml:154-155`; `automerge.go` consults config not constants | MUST | — |
| Default merge policy: label-gated ops-class only | §11.6 | wired-partial | `DaemonConfig.AutoMerge` defaults to false (`cli/internal/config/config.go:163`); enabling it in the self-hosting-xylem overlay flips the default | No config-validation rejection of `auto_merge: true` without an `ops`-class workflow in the profile | SHOULD | Add `Config.Validate` rule |
| `cost.daily_budget_usd`, `per_class_limit`, `on_exceeded` | §12.1 | missing | `CostConfig{Budget *BudgetConfig}` has only `MaxCostUSD`, `MaxTokens` at `cli/internal/config/config.go:268-275` | — | SHOULD | Extend schema |
| `xylem adapt-repo --dry-run` | §12.4 | missing | No CLI wrapper for adapt-repo; workflow runs only via daemon / scanner enqueue | — | MAY | (nice-to-have; defer) |
| Per-workflow cost alerts (2x weekly MA for 3 days) | §12.5 | missing | No anomaly-detection wiring in `cli/internal/cost/`; `cost-report.json` exists but no historical moving-average tracking | — | MAY | — |
| Blast-radius: worktree isolation + no default-branch commits | §13.2 | wired-partial | Worktree isolation wired. Policy-matrix OpCommitDefaultBranch not enforced (see policy row above) | Audit log shows 52 `git_commit` and 30 `git_push` intents, all `allow` | MUST | Land PR #366 |
| AuditEntry extensions | §13.3 | missing | See §9.5 row above | 547 allow / 0 deny; no new fields | MUST | Land PR #366 |
| `xylem audit` CLI (tail / denied / counts / rule) | §13.3 | missing | No `cli/cmd/xylem/audit.go` | — | SHOULD | Wrap JSONL tailing |
| Migration plan for xylem repo (§14 7-step) | §14 | missing | Step 1 (state split) partially done; Steps 2–7 unstarted | Confirmed by flat `.xylem.yml` with no `profiles:` field | MUST | Execute §14 step 1 completely (move queue + audit under `state/`) |

### 4C. Failed-vessel recovery integration (productize §16 Phase 6 hook)

| Capability | Spec | Presence | Code evidence | Ops evidence | Gap class |
| --- | --- | --- | --- | --- | --- |
| `failure-review.json` per terminal vessel (Recovery Phase 1) | productize §4.1, §14, §16 P6 | wired-healthy | `cli/internal/recovery/` imported at `cli/internal/runner/runner.go` | 18/62 phase dirs carry `failure-review.json` | `[Established]` |
| Deterministic failure classifier + retry policy (Phase 2) | §16 P6 | wired-healthy | PR #221 (16a9199) "failure-recovery: deterministic classifier and retry policy matrix" | — | `[Established]` |
| Diagnosis workflow for ambiguous / repeated failures (Phase 3) | §16 P9 | wired-broken | `diagnose-failures.yaml` exists (PR #220) but is the source of the reload block (ship-blocker #3) | — | `[Established]` (broken) |
| Remediation-aware scanner gating (Phase 4) | §16 P9 | wired-healthy | PR #257 (bee78a1) "scanner: add remediation-aware retry gating" | — | `[Established]` |

## 5. Harness-engineering best-practices coverage — topic-by-topic

Every topic below is measured against the `[Established]` claims in
`docs/best-practices/harness-engineering.md`. `[Emerging]` items are noted but
not held against xylem as blockers.

### 1. Harness engineering vs context engineering definitions

xylem treats the harness as first-class: `cli/internal/runner/` +
`cli/internal/orchestrator/` + `cli/internal/intermediary/` +
`cli/internal/observability/` form the execution envelope. Context engineering
(strict subset per `harness-engineering.md:22`) is **dormant** — `ctxmgr` is
defined (`cli/internal/ctxmgr/ctxmgr.go:17-50`) but not wired; prompt assembly
is still Go template concatenation via `phase.RenderPrompt`. Net: the harness
component is strong, the subordinate context-engineering component is weak.

### 2. Context window management

`[Established]` claims: (a) Write / Select / Compress / Isolate strategies;
(b) progressive disclosure via short AGENTS.md + structured docs;
(c) compaction at high thresholds (Claude Code auto-compacts at 95%);
(d) context as compiled view.

xylem coverage:

- **Write** — partial. Structured handoff artifacts exist (`summary.json`,
  `failure-review.json`, `evidence-manifest.json`, `cost-report.json`) but
  coverage is 47% / 15% / 29% / 47% across phase dirs. No `progress.txt`
  or `features.json` equivalent for long-running tasks.
- **Select** — missing. No retrieval pass; prompt assembly pulls the whole
  HARNESS.md every phase.
- **Compress** — missing. Auto-compact is Claude Code's default behavior at
  95% utilization; xylem does not inject its own compaction pass.
- **Isolate** — strong. Per-vessel worktrees (`cli/internal/worktree/`), per-
  phase outputs, per-source scoping. This is the strongest leg of the
  strategy.
- **Compiled-view** — missing. `phase.RenderPrompt` at
  `cli/internal/phase/phase.go:110` is classic string concatenation.

Citation: `harness-engineering.md:34-68`. Gap class: `[Established]`
(Write/Compiled-view are MUST in SoTA §4.2). Issue #60 is the tracking handle.

### 3. Tool and function calling design

`[Established]` claims: clear tool contracts, non-overlapping, token-efficient
outputs, MCP for cross-vendor interoperability, JIT retrieval via tools.

xylem coverage:

- xylem does not ship a tool plane of its own. It invokes `claude -p` which
  uses the Claude Code native tool surface (Grep, Glob, Read, Write, Edit,
  Bash, Task). Tool quality / clarity is delegated.
- `Workflow.AllowedTools` (optional string) gates which tools a phase may
  use, but there is no `catalog` import site — the `cli/internal/catalog/`
  package defines PermissionScope / Tool / ReadOnly / WriteWithApproval /
  FullAutonomy but nothing imports it.
- No MCP server definitions in profiles; MCP is configured per-operator via
  Claude Code session config, not through xylem.

Citation: `harness-engineering.md:73-89`. Gap class: `[Established]` for tool
discipline (partially met by delegating to Claude Code); `SHOULD` for MCP
support; `SHOULD` for centralized catalog.

### 4. Memory systems

`[Established]` claims: procedural / semantic / episodic memory types;
scratchpads within session; cross-session structured artifacts.

xylem coverage:

- Procedural = HARNESS.md + workflow YAML + prompts — wired.
- Semantic = lessons file + `lessons` workflow — wired.
- Episodic = `evidence-manifest.json` per vessel is a weak approximation;
  `memory` package is dormant.
- Scratchpads — missing. No `NOTES.md` / `progress.txt` / `features.json`
  equivalent.
- Cross-session — partial. Git history + failure-review.json are the de
  facto cross-session memory.

Citation: `harness-engineering.md:92-117`. Gap class: `[Established]`.
Issue #60 covers the wiring.

### 5. Prompt construction and system prompt design

`[Established]` claims: persona specificity, commands-early, code examples
over explanations, short root + deeper structured docs.

xylem coverage:

- Workflow prompts under `.xylem/prompts/` and profile prompts under
  `cli/internal/profiles/core/prompts/` follow a per-phase persona pattern.
- HARNESS.md is ~150 lines and carries build/test/lint commands early.
- No `AGENTS.md` root file (see §4A row).
- Missing: few-shot examples embedded in profile prompts for `fix-bug` /
  `implement-feature` (sampling shows they are mostly instructional, not
  example-driven).

Citation: `harness-engineering.md:125-139`. Gap class: `SHOULD`.

### 6. Agent loop design

`[Established]` claims: start simple, progressive pattern ladder, evaluator
separation, sprint contracts, methodical simplification.

xylem coverage:

- Start simple — wired. Single-phase-per-LLM invocation with wave
  parallelism only on explicit dependencies.
- Progressive ladder — partial. Prompt chaining ✓, orchestrator-workers ✗,
  evaluator-optimizer ✗.
- Evaluator separation — missing (see §4A row). PR #372 in flight.
- Sprint contracts — missing. `mission.SprintContract` type exists in
  `cli/internal/mission/contract.go` but `mission` package is dormant.
- Methodical simplification — wired. `continuous-simplicity` scheduled
  vessel (PR #291). `doc-garden` (PR #370). `harness-gap-analysis` weekly.

Citation: `harness-engineering.md:146-167`. Gap class: `[Established]`
(evaluator-optimizer is the load-bearing one — issue #151, #313).

### 7. Multi-agent coordination and delegation

`[Established]` claims: handoff / group chat / orchestrator-workers; sub-
agents as context firewalls; file-based communication.

xylem coverage:

- Orchestrator-workers — partial. `cli/internal/orchestrator/` is wired into
  the runner; per-wave parallel execution is wired; single-agent-per-phase
  model means no intra-phase sub-agent spawning.
- Handoff between phases — wired via output artifacts (`analyze.output`
  consumed by downstream phases). This is the "file-based agent communication"
  pattern Anthropic recommends (`harness-engineering.md:196-197`).
- Context firewalls — partial. Phases are isolated via separate `claude -p`
  invocations but each phase re-hydrates the full prompt; no dispatcher
  pattern that sees only prompt+final-result.

Citation: `harness-engineering.md:174-197`. Gap class: `SHOULD`.

### 8. RAG integration

`[Established]` claims: hybrid retrieval for code; citations; RAG as tool.

xylem coverage: Claude Code's native Grep/Glob/Read tools cover the code-
retrieval story. No vector store, no embedding pipeline, no document library.
This is consistent with SoTA §4.3 "prefer just-in-time retrieval via tools"
and is `[Established]`-compliant by delegation.

Citation: `harness-engineering.md:203-213`. Gap class: N/A (delegated).

### 9. Evaluation and observability

`[Established]` claims: instrument-from-day-one; automated evaluation on every
change; evaluator uses the live application; calibrate with human judgment;
local ephemeral observability stack exposed to agents.

xylem coverage:

- OpenTelemetry wired (§4A row). Spans emit for drain / vessel / phase /
  vessel-health / vessel-cost / recovery attributes at
  `cli/internal/runner/runner.go:165-302`.
- Automated evaluation on every change — missing. No eval-on-change pipeline;
  issue #57 tracks.
- Evaluator uses live application — missing. Live-gate support exists
  (`cli/internal/runner/runner.go:2654` calls `LiveGate.Run`; live/api and
  live/browser modes supported in schema) but no production workflow uses it
  today.
- Calibrate evaluator with human judgment — missing (no evaluator wired).
- Local ephemeral observability stack — partial. Jaeger at `localhost:4317`
  per CLAUDE.md; not exposed to agents as a queryable tool. Issue #59 tracks.

Citation: `harness-engineering.md:219-233`. Gap class: `[Established]`
(instrument-from-day-one is met; evaluator and agent-readable obs are not).

### 10. Error handling, retry logic, graceful degradation

`[Established]` claims: failure as system signal, failure→harness repair
loop, retry limits, recovery patterns (feature list, git commits, end-to-end
tests).

xylem coverage:

- Failure-as-signal — strong. `failure-review.json` (Recovery Phase 1) + the
  deterministic classifier (Recovery Phase 2, PR #221) + remediation-aware
  gating (PR #257) form a tight failure→signal→policy loop.
- Retry limits — wired. `Scanner.retryCandidate` honours `cli/internal/config/config.go`
  retry caps; production shows retry chains capped at 4 depth.
- Feature list pattern — missing. No per-vessel `features.json`.
- End-to-end tests — wired via `verify` / `test_critic` / `smoke` gates
  (observed in `issue-236/evidence-manifest.json:17-35`).

Citation: `harness-engineering.md:238-257`. Gap class: `[Established]` —
mostly met. The gap is feature-list status tracking; low priority.

### 11. Security, sandboxing, and trust boundaries

`[Established]` claims: architectural containment > prompt guardrails;
plan-then-execute; least privilege; human-in-the-loop for high-risk; full
virtualization preferred.

xylem coverage:

- Architectural containment — partial. Worktree isolation ✓. Policy matrix
  exists in code but not enforced on phase writes (ship-blocker #1). `.claude/`
  sandbox config at `.claude/settings.json` is Claude Code's concern, not
  xylem's.
- Plan-then-execute — weak. Phases run with broad tool access; no prior
  plan-validation pass.
- Least privilege — partial. `AllowedTools` per workflow is the only scoping
  primitive. No per-phase network or filesystem scoping.
- Human-in-the-loop — partial. `RequireApproval` effect is defined
  (`cli/internal/intermediary/intermediary.go:18-28`) but no production rule
  uses it; every decision is `allow`.
- Full virtualization — N/A (delegated to operator environment).

Citation: `harness-engineering.md:265-281`. Gap class: `[Established]`. Issue
#58 is the tracking handle.

### 12. Long-running agent task management

`[Established]` claims: initializer + worker pattern, structured handoff
artifacts, sprint contracts (earlier models), context resets (earlier models).

xylem coverage:

- Initializer + worker — strong. `xylem init` → daemon seed → scanner →
  drain → phase loop → failure review → lessons → sota-gap-analysis.
  Self-hosted analogue of the init.sh + worker pattern Anthropic describes.
- Structured handoff artifacts — partial (coverage numbers in §4A).
- Sprint contracts — missing (dormant `mission` package).
- Context resets — missing.

`[Emerging]`: Opus 4.6 removed sprint construct and context resets. xylem
using Opus 4.6 via `continuous-improvement` style vessels is directionally
aligned.

Citation: `harness-engineering.md:286-301`. Gap class: `SHOULD` (sprint
contracts are `[Emerging]`).

### 13. State management and checkpointing

`[Established]` claims: git as primary checkpoint; progress files; feature
lists; execution plans as first-class.

xylem coverage:

- Git as checkpoint — strong. Every vessel commits to its own worktree
  branch; PR creation is the real checkpoint.
- Progress files — weak. No `claude-progress.txt` equivalent per vessel.
- Feature lists — missing.
- Execution plans — partial. `summary.json` per vessel plus `review.json`
  per review vessel; no centralized "active plans" document.

Citation: `harness-engineering.md:309-318`. Gap class: `SHOULD`.

### 14. Cost optimization and token budgeting

`[Established]` claims: compaction thresholds, sub-agents as firewalls,
summarization, model ladder, plan caching.

xylem coverage:

- Compaction thresholds — missing (ctxmgr dormant).
- Sub-agents as firewalls — missing (no sub-agent dispatching).
- Summarization — missing.
- Model ladder — wired. PR #367 landed tier-aware routing with provider
  fallback (`cli/internal/config/config.go:100-107`); vessels carry
  `"tier":"med"` per queue.jsonl sample.
- Plan caching — missing.

Citation: `harness-engineering.md:325-341`. Gap class: `[Established]` for
cost measurement; `SHOULD` for the model ladder (done) and compaction (not
done).

### 15. Architectural enforcement and entropy management

`[Established]` claims: enforce invariants not implementations; layered
architecture; background cleanup agents; doc gardener; "pay debt continuously".

xylem coverage:

- Enforce invariants — partial. CI runs format/vet/build/test. No
  dependency-rule linter enforcing the `cli/internal` / `cli/cmd` boundary
  documented in `.xylem/HARNESS.md:44`.
- Layered architecture — partial. Code is organized that way but not
  mechanically checked.
- Background cleanup — strong. `continuous-simplicity` (PR #291),
  `doc-garden` (PR #370), `harness-gap-analysis` (PR #264), `continuous-refactoring`
  (PR #342), `continuous-style` (PR #343) — five scheduled cleanup vessels.
- Doc gardener — wired (PR #370).
- Pay debt continuously — alignment strong in practice: issues #322 / #323
  from the harness-gap-analysis are self-generated.

Citation: `harness-engineering.md:348-359`. Gap class: `[Established]`
(strongest alignment in the whole assessment).

## 6. Productization-readiness assessment

### Goal alignment (productize §2)

| Goal | Status | Why |
| --- | --- | --- |
| §2.1 Immediate usability of `xylem init` | **partial** | `xylem init --profile=core` produces a working profile composition (15 core workflows) and seeds an adapt-repo issue. Missing: `AGENTS.md` stub, `xylem config validate` / `xylem workflow validate` CLI to verify the scaffolded output |
| §2.2 First-cycle adaptation | **partial** | `adapt-repo` workflow exists with 7 phases; daemon seed works; the missing piece is a machine-readable `adapt-plan.json` schema + validator (§8.4) — currently the plan is a prompt convention, not a typed contract |
| §2.3 PR-gated self-modification | **wired-broken** | Mechanism exists (policy matrix, workflow class, worktree isolation) but the runner does not call `policy.Evaluate` on phase writes. PR #366 in flight. Until it lands, a delivery-class workflow can in principle edit `.xylem.yml` in its own worktree and the runner will not deny it at policy time; the only backstop is the protected-surface snapshot check at `runner.go:3008-3036` |
| §2.4 Preserved self-hosting | **partial** | All xylem-specific workflows moved to `cli/internal/profiles/self-hosting-xylem/`. `xylem.overlay.yml` declares the overlay sources. xylem's own `.xylem.yml` has NOT yet been flipped to `profiles: [core, self-hosting-xylem]` (ship-blocker #5) |
| §2.5 Tracked control plane | **wired-partial** | `.xylem/.gitignore` default in core now tracks control plane and hides `state/`. Control plane / runtime split is partially done (phases yes, queue/audit no) |
| §2.6 No xylem-branded defaults in core | **wired-healthy** | Core profile does not contain `nicholls-inc/xylem`, `harness-impl`, `xylem-failed`, `pr-vessel-active`, `sota-gap`, `harness-post-merge` — all live in the overlay |
| §2.7 Validation surface (`xylem config validate` / `xylem workflow validate`) | **missing** | Neither CLI exists |

### Non-goals alignment (productize §3)

- §3.1 No changes to vessel state machine or queue format: held (no state
  machine edits in recent history).
- §3.2 GitHub-only: held.
- §3.3 Single-tenant daemon: held.
- §3.4 No interactive prompts in `init`: held.
- §3.5 No YAML schema redesign beyond classes: held.
- §3.6 No UI: held.
- §3.7 Failed-vessel recovery Phase 1 already landed: confirmed (§4C).

### Shortest path to first external repo

**Eight-change shortest path** (ordered; each is independently mergeable):

1. **Rename diagnose-failures phase `create-issues` → `create_issues`** in
   both `cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml:7`
   and the prompt path. Unblocks reload.
2. **Move `queue.jsonl` + `audit.jsonl` + `daemon.pid` under `state/`**
   following `RuntimePath` (`cli/internal/config/config.go:377-388`).
   Finishes PRODUCTIZE §5.2.
3. **Emit `AGENTS.md` stub from `xylem init`.** Add `core/AGENTS.md.tmpl`,
   render in `cmdInitWithProfileAndOptions`.
4. **Ship `xylem config validate` + `xylem workflow validate`.** Both wrap
   existing library functions. Adds spec §2.7 validation surface.
5. **Land PR #366** (workflow class matrix enforcement + AuditEntry
   extensions). Resolves ship-blockers #1 + #2.
6. **Land PR #372** (generator/evaluator loop). Resolves SoTA §4.5/§4.6 gap
   and closes issues #151 / #313.
7. **Wire evidence claim levels at source** — one-line fix at
   `cli/internal/runner/runner.go:2988`. Turns `untyped` into
   `BehaviorallyChecked` for command gates.
8. **Flip xylem's own `.xylem.yml` to `profiles: [core, self-hosting-xylem]`.**
   Execute §14 migration step 5; validate against the current daemon on a
   worktree first.

After steps 1–8, the core profile is shippable to an external Go repo. The
optional steps for non-Go repos (Python, Node, mixed-stack) are gated on
`adapt-repo`'s detection fidelity, which is wired but under-tested.

## 7. Prioritized gap backlog

Priority function: (a) spec weight (MUST > SHOULD > MAY > [Emerging]);
(b) blast radius — "blocks productization on external repos" > "degrades
xylem-on-xylem"; (c) smallest-next-step cost; (d) existing in-flight PR
or issue.

### P0 — ship-blockers (MUST; blocks external rollout)

| # | Title | Gap class | Spec | Code ev. | Ops ev. | Next step | Issue / PR |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | Merge-triggered daemon reload rejected by own workflow validator | wired-broken | PRODUCTIZE §10 | `cli/internal/workflow/workflow.go:20` regex; `cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml:7` | `daemon-reload-audit.jsonl:1-3` (3 consecutive denies 2026-04-11T15:06Z/16:06Z/17:33Z); `daemon-reload-log.jsonl:1-3` | Rename phase `create-issues` → `create_issues` in workflow + prompt path | no issue filed |
| 2 | Policy class matrix is not enforced on phase writes | wired-partial | PRODUCTIZE §9; SoTA §4.9 | `cli/internal/runner/runner.go:3013-3036` (enforcePhasePolicy goes through Intermediary.Evaluate, not policy.Evaluate); `cli/internal/policy/class.go:40-68` (default rules exist) | audit.jsonl: 547 allow / 0 deny | Land PR #366 | #235; PR #366 (CONFLICTING) |
| 3 | AuditEntry lacks workflow_class / policy_rule / rule_matched / operation / file_path / vessel_id | missing | PRODUCTIZE §9.5, §13.3 | `cli/internal/intermediary/intermediary.go:63-68` | `grep -c workflow_class audit.jsonl = 0` | Land PR #366 (bundled) | #235; PR #366 |
| 4 | `xylem config validate` and `xylem workflow validate` CLI missing | missing | PRODUCTIZE §8.8, §2.7 | `cli/cmd/xylem/root.go:90-115` (no registration) | adapt-repo phase 4 runs inline | Wrap `Config.Validate()` and `workflow.Validate()` in two small command files | not tracked |
| 5 | `xylem init` does not emit AGENTS.md | missing | PRODUCTIZE §4.4; SoTA §4.1 | `cli/internal/profiles/core/` (no AGENTS.md.tmpl) | — | Add template + render step | #241 (productize tracking) |
| 6 | xylem's own `.xylem.yml` is still flat (no `profiles:` field) | missing | PRODUCTIZE §14 steps 1-7 | origin/main `.xylem.yml:1-155` | Daemon runs against flat config | Execute §14 step 5 under a worktree | #240 |
| 7 | `queue.jsonl` + `audit.jsonl` still at flat `.xylem/` root | wired-partial | PRODUCTIZE §5.2 | `cli/cmd/xylem/root.go:73` (`filepath.Join(cfg.StateDir, "queue.jsonl")`) | Live file at `.daemon-root/.xylem/queue.jsonl` | Rebase queue/audit paths under `state/` and add compatibility shim | not tracked |
| 8 | Cost schema missing `daily_budget_usd` / `per_class_limit` / `on_exceeded` | missing | PRODUCTIZE §12.1 | `cli/internal/config/config.go:268-275` (only `MaxCostUSD`, `MaxTokens`) | `cost-report.json` reports `total_cost_usd: 0` | Extend `CostConfig` + `BudgetConfig` | not tracked |
| 9 | `BudgetGate.Check` is a permissive no-op stub | wired-broken | PRODUCTIZE §11.3, §12.2 | `cli/internal/cost/budgetgate.go:23-34` | Scanner always enqueues | Replace stub with tracker-backed per-class check | not tracked |
| 10 | Evidence claims are `untyped` in production (schema OK, values wrong) | wired-broken | SoTA §4.6 | `cli/internal/runner/runner.go:2988` (default branch sets no level) | `issue-236/evidence-manifest.json:7` — `"level": "untyped"` | One-line classification: command gate → `BehaviorallyChecked` | not tracked |

### P1 — major quality gaps (MUST / SHOULD; degrades xylem but does not block)

| # | Title | Gap class | Spec | Code ev. | Ops ev. | Next step | Issue / PR |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 11 | Generator/evaluator loop not wired | SHOULD | SoTA §4.5, §4.6; harness-eng §6 | `cli/internal/evaluator/` (zero import sites); `Phase.Evaluator` config added in PR #344 | PR #372 (CONFLICTING, retry chain) | Land PR #372 | #151, #313; PR #372 |
| 12 | Context compilation pipeline (ctxmgr) not wired | SHOULD | SoTA §4.2; harness-eng §2 | `cli/internal/ctxmgr/ctxmgr.go:17-50` (zero import sites) | sota-gap-snapshot.json §1 priority 7 | Thread into `phase.RenderPrompt` | #60 |
| 13 | Memory package (procedural/semantic/episodic) not wired | SHOULD | SoTA §4.4; harness-eng §4 | `cli/internal/memory/memory.go:22-32` (zero import sites) | sota-gap-snapshot.json §3 priority 7 | Wire for episodic memory first (cheapest win) | #60 |
| 14 | Tool catalog with permission scopes not wired | SHOULD | SoTA §4.3; harness-eng §3 | `cli/internal/catalog/catalog.go:11-49` (zero import sites) | sota-gap-snapshot.json §2 priority 8 | Add phase-level catalog resolution at launch | not tracked |
| 15 | `xylem adapt-repo` CLI wrapper + `--dry-run` missing | MAY | PRODUCTIZE §12.4 | No `cli/cmd/xylem/adapt_repo.go` | adapt-repo runs only via daemon enqueue | Wrap enqueue + `xylem status` follow | not tracked |
| 16 | `xylem audit` CLI (tail / denied / counts / rule) missing | SHOULD | PRODUCTIZE §13.3 | No `cli/cmd/xylem/audit.go` | — | Wrap JSONL tail + filter | not tracked |
| 17 | `adapt-plan.json` schema + validator missing | MUST (conditional on adapt-repo) | PRODUCTIZE §8.4 | — | — | Define schema + Go struct; add to `bootstrap.*` | not tracked |
| 18 | `validation.working_dir` + per-workflow `required_for` map missing | SHOULD | PRODUCTIZE §A.6 | `cli/internal/config/config.go:268-275` (flat Format/Lint/Build/Test) | Three consecutive reload rejections (PRs #371/#374/#377) were fixes to the flat template | Add `WorkingDir` + `RequiredFor map[string][]string` | PR #374, #375, #377 (landed) |
| 19 | Agent-readable runtime artifacts (logs/metrics/traces via tools) missing | SHOULD | SoTA §4.8 | — | — | Wire an MCP or CLI resource endpoint | #59 |
| 20 | Eval corpus + baseline comparison flow missing | SHOULD | SoTA §4.7 | `cli/internal/dtu/testdata/manual-smoke/` exists but is smoke only | — | Track under #57 | #57 |
| 21 | Runtime containment / network policy / scoped secrets missing | SHOULD | SoTA §4.9 | — | — | Track under #58 | #58 |
| 22 | Default-branch boundary linter (`cli/internal` ≠ `cli/cmd`) missing | SHOULD | SoTA §4.10 | `.xylem/HARNESS.md:44` (doc only) | — | Add `depguard` / `structcheck` to CI | not tracked |
| 23 | Sprint contracts / mission decomposition not wired | MAY (`[Emerging]`) | harness-eng §6, §12 | `cli/internal/mission/` (dormant) | — | Defer until model capability forces it | not tracked |
| 24 | USD cost pricing not wired into `cost.Tracker` | SHOULD | SoTA §4.11 | `cost-report.json:total_cost_usd: 0` across 29 samples | — | Add price lookup keyed on `by_model` | not tracked |
| 25 | Dormant `signal` package (repetition / tool-failure / context-thrash heuristics) not wired | MAY | SoTA Part II; harness-eng §9 | `cli/internal/signal/` (zero import sites) | — | Defer until phase stalls justify | not tracked |

### P2 — housekeeping and observability

| # | Title | Gap class | Spec | Code ev. | Ops ev. | Next step |
| --- | --- | --- | --- | --- | --- | --- |
| 26 | `summary.json` + `cost-report.json` + `evidence-manifest.json` coverage inconsistent (47 / 47 / 15%) | SHOULD | SoTA §4.8 | `cli/internal/runner/runner.go:1667-1686` | 62 phase dirs sampled | Make artifact writes unconditional on phase type |
| 27 | Scanner uses `.xylem.yml.intent_policy` glob rules as its only deny path | SHOULD | PRODUCTIZE §9.7 | `cli/internal/intermediary/intermediary.go:41-69` | 547 allows / 0 denies | Merge class matrix into glob eval order (matrix first, fail-closed) |
| 28 | `workflow-health-report` scheduled but no weekly issue dedup rule | SHOULD | PRODUCTIZE §6.3 | `cli/internal/profiles/core/workflows/workflow-health-report.yaml` | — | Implement `XYLEM_NOOP` early-exit if open report exists |
| 29 | `cost.reset_timezone` missing | MAY | PRODUCTIZE §12.1 | — | — | Defer |
| 30 | No per-workflow cost alert (2x weekly MA for 3 consecutive days) | MAY | PRODUCTIZE §12.5 | — | — | Defer |
| 31 | `xylem profile lock` command missing | MAY | PRODUCTIZE §A.5 | — | — | Defer |
| 32 | No config-level validation for `auto_merge: true` without an `ops`-class workflow in the profile | SHOULD | PRODUCTIZE §11.6 | `cli/internal/config/config.go:203-215` | — | Add `Config.Validate` rule |
| 33 | `pr_head_updated` debounce default is 10m — correct, but no central limit on debounce overrides | MAY | PRODUCTIZE §11.2 | `cli/internal/source/github_pr_events_debounce.go:45-55` | — | Add a ceiling (`max_debounce`) per scanner |
| 34 | No `AGENTS.md` at xylem repo root | SHOULD | harness-eng §2, §5 | — | `ls` confirms absent | Add a short root AGENTS.md pointing to HARNESS.md + docs/ |
| 35 | `DefaultProtectedSurfaces = []string{}` is load-bearing but un-commented after the rationale at `config.go:20-54` aged | MAY | PRODUCTIZE §4.5 | `cli/internal/config/config.go:20-54` | — | Refresh rationale comment to point at §9 class matrix |
| 36 | Retry-chain cascade on PR #366 (4 levels deep) | (operational) | — | — | queue.jsonl has 57 retry-suffixed vessels | Land ship-blocker #1 (reload fix) so resolve-conflicts fix reaches the daemon |
| 37 | Retry-chain cascade on PR #372 | (operational) | — | — | 2 levels deep | Same as #36 |
| 38 | xylem-failed status label still applied per workflow in overlay — spec §6 wants generic labels in core, xylem-specific labels only in overlay — confirmed | (compliance) | PRODUCTIZE §6.2, §7.1 | `.xylem.yml:15, 28, 42, 56` vs `cli/internal/profiles/self-hosting-xylem/xylem.overlay.yml:10-13` | — | Holds after migration step 5 lands |
| 39 | evidence `trust_boundary` always "No trust boundary declared" | SHOULD | SoTA §4.6 | `cli/internal/runner/runner.go:2961-2969` | 9/9 evidence manifests | Classify trust boundary per gate type at claim build |
| 40 | `cli/internal/cadence`, `cli/internal/skills` dormant (very recent mod dates) | MAY | — | zero import sites | — | Confirm intent — may be fresh work-in-progress |
| 41 | No test case 4 (§15.3) — class enforcement denial test | SHOULD | PRODUCTIZE §15.3 | `cli/internal/policy/class_test.go` covers Evaluate table but not the runner integration | — | Add runner-level integration test under PR #366 |
| 42 | `TestProp_PolicyStableUnderReorder` missing | MAY | PRODUCTIZE §15.4 | `cli/internal/policy/class_prop_test.go` (present but coverage not verified here) | — | Confirm property coverage matches spec |
| 43 | `profile.lock` version drift detection is a validation-error, but §5.4 rule #5 expects "actionable warnings" not silent overwrites — needs verification | MAY | PRODUCTIZE §5.4 | `cli/internal/profiles/profiles.go:17-20` (version constants) | — | Audit the force-path code |
| 44 | `pr_opened` per-`(pr)` dedup is in place; but no invariant test that dedup survives daemon restart | MAY | PRODUCTIZE §11.1 | `cli/internal/source/github_pr_events.go:261` | — | Add restart-survival property test |
| 45 | `ensureAdaptRepoSeeded` dedupes by title; §8.2 also expects dedup when issue already exists — verify both open and closed | MAY | PRODUCTIZE §8.2 | `cli/cmd/xylem/adapt_repo_seed.go` | — | Add closed-issue dedup test |
| 46 | `.xylem/state/bootstrap/adaptation-log.jsonl` rate-limit per §8.6 (one run per 7 days) not yet implemented | MAY | PRODUCTIZE §8.6 | — | — | Add scanner-level check |
| 47 | Doc-garden workflow reload block is related to PR #370 landing a new workflow — verify the workflow YAML is valid under the new validator | (operational) | — | `cli/internal/profiles/self-hosting-xylem/workflows/` (or core) — verify | — | grep for non-snake_case phase names |
| 48 | Issues #322, #323 (harness-gap-analysis self-generated) indicate the harness is already diagnosing its own gaps — priority for these work as a feedback signal that the self-improvement loop is live, not a gap | (signal) | — | — | two open issues filed by the harness | Track the quality of these self-files as a metric |
| 49 | No `AGENTS.md` instruction-hierarchy test (SoTA §5 instruction hierarchy at org→repo→dir) | MAY | SoTA §5 / harness-eng §5 | — | — | Defer |
| 50 | TestSmoke_S5_DocGardenWorkflow + TestAdaptRepoWorkflowAsset failing on main per issue #378 | (operational) | — | — | issue #378 | Fix tests before external rollout |

### Priority reasoning (the phase-6 priority function)

1. Ship-blockers #1–#4 are MUST + block external rollout + fast fix + no
   open in-flight PR — highest priority.
2. #5 (AGENTS.md) and #6 (profile-migrate) are MUST + already have tracking
   issues (#241, #240) but need an owner.
3. #11 (evaluator) and #17 (adapt-plan schema) are gated on PR #372 and on
   a new schema respectively; both are SHOULD but both unblock §16 Phase 4
   and §16 Phase 6.
4. P2 items are correctness / observability improvements, not blockers.

## 8. Risks and open questions

### Risks needing maintainer judgment (not engineering)

1. **How strict should class enforcement be on day one?** The productize
   spec §16 Phase 3 proposes "warn-only mode runs for 7 days with zero
   unexpected denies before flipping to enforce". PR #366 is in flight and
   will land this. The open question is: does warn-only mode use a feature
   flag or a `.xylem.yml harness.policy.mode: warn|enforce` field? The spec
   does not say.

2. **Is BudgetGate a security control or an operational throttle?** PRODUCTIZE
   §12.6 explicitly says it is an operational throttle ("the opposite of the
   class policy matrix"). But the current no-op stub means there is no
   operational throttle at all — pathological scanner behavior has no
   stopping point. Decision: ship a real tracker-backed gate before external
   rollout, or accept an unbounded external-repo enqueue until someone
   notices?

3. **Doc-garden scheduled vessel (PR #370) adds `.xylem/workflows/doc-garden.yaml`
   — which profile does it belong to?** It's generic (any repo benefits from
   doc-garden) so it should live in `core`. But it was shipped to the
   self-hosting overlay first. Check the landing PR's intent.

4. **The reload validator is stricter than the init validator was.** When
   `newDaemonReloadCmd` validates workflows at reload time it uses the same
   `validPhaseName` regex that was added later. Workflows that were authored
   before the regex existed (like `diagnose-failures` with `create-issues`)
   pass static `xylem init` checks but fail reload-time validation. Decision:
   (a) keep the reload validator strict and fix the workflows, or
   (b) add a one-release grace period where the reload logs a warning but
   proceeds?

5. **`.daemon-root` contains a stale legacy `.xylem/phases/` directory (93
   subdirectories, last mod Apr 10)** alongside the new
   `.xylem/state/phases/` (33 subdirectories, last mod Apr 11). The legacy
   dir is not cleaned up. Is this intentional transition-era residue or a
   latent cleanup bug? `cli/internal/runner/runner.go` writes only to the
   new path; the old one is dead.

6. **All 547 audit entries are "allow".** No glob rule has ever produced a
   deny in this daemon's lifetime. That is either (a) great — nothing bad
   was attempted — or (b) ominous — the rule set is too permissive to
   distinguish. Can not be resolved from audit alone. The class-matrix
   wiring (PR #366) will materially change this picture.

7. **Two agents disagreed about `daemon_reload.go` existence.** This is a
   methodology lesson, not a code gap: one agent read the current working
   tree (hn/tmp) where the file is deleted and reported "missing"; the other
   read origin/main where it is present. Branch vs snapshot discipline
   matters. Before the next audit, standardise on "audit against
   origin/main, not current HEAD".

### Open questions (don't block engineering)

- **Is `docs/design/failed-vessel-recovery-spec.md` Phase 2/3/4 landed?**
  PR #221 is "deterministic classifier and retry policy matrix" — that's
  Phase 2. PR #220 is the diagnosis workflow (Phase 3) but the current bug
  with `create-issues` phase name is a direct consequence. Phase 4
  (remediation-aware gating) landed via PR #257. This assessment treats all
  four phases as at-least-partially shipped but has not re-verified Phase 3.

- **Are `continuous-improvement`, `continuous-simplicity`, `continuous-refactoring`,
  `continuous-style` meaningfully distinct?** They appear as parallel
  scheduled workflows. If they are drifting toward the same responsibility,
  that is its own entropy-management problem. The harness-engineering guide
  §15 recommends consolidation by invariant, not by workflow.

- **Does `adapt-repo` handle mixed-stack repos (Go + TS + Python)?**
  PRODUCTIZE §8.5 says "the `plan` phase MUST NOT attempt to unify validation
  across stacks" and proposes a `working_dir` mechanism. The current
  `ValidationConfig` is flat. Until the `working_dir` + `required_for` work
  lands, mixed-stack fidelity is unknown. Recommend adding a `mixed-go-ts/`
  fixture smoke test before external rollout.

- **The `sota-gap-snapshot.json` prior art claims `orchestration: wired`
  with priority 4 — this assessment confirms.** But the snapshot also lists
  `context`, `tools`, `memory`, `evaluation`, `cost` as dormant, and this
  assessment CONFIRMS all four. The gap between the snapshot (2026-04-09)
  and this assessment (2026-04-11) is that:
  - `orchestration`: wired, now with adaptive tier routing (PR #367) — marginal improvement.
  - `verification`: wired, but all claims `untyped` — drift from the snapshot's confidence.
  - `security`: wired to `intermediary` but class matrix not enforced — confirms snapshot's partial marking.
  - `entropy`: wired-healthy, strongest area — confirms.
  - `cost`: wired-partial, USD pricing missing — confirms.
  In other words, the 2026-04-09 snapshot is substantially accurate even two
  days later. That itself is a signal that **the rate of substantial gap
  closure is slower than the rate of productize-spec forward pressure** —
  the tree is shipping in-flight PRs faster than SoTA-layer capabilities are
  being wired.

---

**End of assessment.** File written to
`docs/assessments/sota-gap-assessment-2026-04-11.md`. No other files were
modified. Prior art at `.daemon-root/.xylem/state/sota-gap-snapshot.json`
was read-only and remains untouched.
