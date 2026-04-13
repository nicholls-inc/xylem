# SoTA Gap Implementation Plan — two-prong split, 2026-04-11

**Source.** `docs/assessments/sota-gap-assessment-2026-04-11.md` (877 lines,
`origin/main` @ `d5ce884` at planning time — one commit ahead of the
assessment's `375ed8da801f` baseline). All citations resolve against
`origin/main`, not the working tree (`hn/tmp` has local deletions that are
not shipped).

**Audit corrections folded in.** Two facts verified directly against
`origin/main` that materially change Prong 1:

1. **There is a second hyphenated phase name.** The assessment says ship-blocker
   #3 (reload rejection) is caused by `create-issues` in
   `cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml:7`.
   That is only the first failing file found by
   `filepath.WalkDir(workflowsDir)` at
   `cli/cmd/xylem/daemon_reload.go:458`. Walking the full on-disk workflow
   set over `origin/main` reveals a **second** hyphenated phase name:
   `test-critic` in `cli/internal/profiles/core/workflows/review-pr.yaml:10`.
   Once `create-issues` is renamed the next reload will fail on `test-critic`.
   Both must be fixed in the same phase-1 commit.

2. **Profile assets are never re-synced after a daemon upgrade.**
   `syncProfileAssets` is called only from `xylem init`
   (`cli/cmd/xylem/init.go:144`) — `daemonStartup`
   (`cli/cmd/xylem/daemon.go:207-214`) does not re-sync profile assets, and
   `selfUpgrade` at `cli/cmd/xylem/upgrade.go` runs `git pull && go build &&
   exec()` with no profile re-sync step either. The assessment's memory of
   the "drift bug" (`.daemon-root/.xylem/workflows/*.yaml` stale since
   PR #157) is therefore still structurally live: **landing a fix to the
   profile source alone is not sufficient to unblock reload.** The plan
   treats runtime-copy edits as a deliberate Prong-1 action, authorised by
   the user's direct request to unblock the daemon, and files a separate
   Prong-2 issue to eliminate the drift mechanism at its root.

**PR #366 / PR #372 reality check** (as of 2026-04-11T19:35Z).

| PR  | Head              | Base   | Merge state | Last HEAD commit (LLM)                                     | Labels                                                             | Notes |
| --- | ----------------- | ------ | ----------- | ---------------------------------------------------------- | ------------------------------------------------------------------ | ----- |
| 366 | `feat/issue-235-235` | `main` | `DIRTY`  | `ed06907` 2026-04-11T12:49Z "fix: address review feedback" | `harness-impl`, `ready-to-merge`, `needs-conflict-resolution`      | 1102+/133− over 11 files (drain, config, intermediary, runner, dtu, tests). Real merge conflict with main on `config_prop_test.go` + `runner_prop_test.go`. |
| 372 | `feat/issue-151-151` | `main` | `DIRTY`  | `ae06a89` 2026-04-11T15:27Z "fix: address review feedback" | `ready-to-merge`, `needs-conflict-resolution`                      | 1260+/621− over 18 files — introduces `cli/internal/runner/evaluator_loop.go` (461 LOC) and deletes 601 LOC from `runner.go` as a refactor-and-extract. **Textually conflicts with PR #366 on `runner.go` and `runner_test.go`** — both PRs reshape the same function bodies. |

Both carry `ready-to-merge`; both are being repeatedly retried by
`pr-366-resolve-conflicts-retry-1-retry-1-retry-1-retry-1` and
`pr-366-merge-pr-retry-*` / `pr-372-resolve-conflicts-retry-1-retry-1`.
Neither has made forward progress in ~7 hours. The retry loop is
deterministically broken for these two PRs because the conflicts cross file
regions the vessel re-resolves identically each attempt.

**Issue-state cross-reference** (confirmed against `gh issue view`).

| Issue | State | Labels                                                              | Assessment row |
| ----- | ----- | ------------------------------------------------------------------- | -------------- |
| #235  | open  | `enhancement ready-for-work wave-2b`                                | P0 #2/#3 — tracks PR #366. |
| #151  | open  | `ready-for-work`                                                    | P1 #11 — tracks PR #372 (primary). |
| #313  | open  | `enhancement ready-for-work harness-impl xylem-failed`              | P1 #11 — duplicate tracker. Will be closed when #151 / PR #372 lands. |
| #60   | open  | `enhancement harness-impl xylem-failed`                             | P1 #12/#13 — ctxmgr + memory. Needs `xylem-failed` stripped. |
| #240  | open  | `enhancement ready-for-work wave-3`                                 | P0 #6 — xylem-self-migration. |
| #241  | open  | `documentation` — productize tracking parent                        | P0 #5 umbrella. |
| #57   | open  | `enhancement xylem-failed`                                          | P1 #20 — eval corpus. Needs label strip. |
| #58   | open  | `enhancement xylem-failed`                                          | P1 #21 — containment. Needs label strip. |
| #59   | open  | `enhancement xylem-failed`                                          | P1 #19 — agent-readable runtime artifacts. Needs label strip. |
| #378  | **closed** | n/a (fix landed in a later PR) — pre-existing smoke-test failure   | P2 #50 — no longer a blocker. |
| #322  | open  | — (harness-self-filed)                                              | §8 signal observation, not actionable for this plan. |
| #323  | open  | — (harness-self-filed)                                              | §8 signal observation, not actionable for this plan. |

Only 11 issues are currently open — far smaller than the 178-line queue
implies. Most queue entries are retry-chain artefacts, not fresh work.

---

## 1. Split rationale

The split is driven by two operational realities that the assessment's §7
priority table does not explicitly sequence:

**First, the reload cascade is currently a brick wall.** Every PR that
lands after 2026-04-11T15:06Z is failing to propagate to the live daemon
because `daemon-reload-audit.jsonl` is in a 3-consecutive-deny state
(`create-issues` phase-name rejection), and once that is fixed the *next*
failing reload is a previously-latent `test-critic` phase-name rejection.
This means the xylem self-improvement loop cannot land *any* further fixes
through its own drain pipeline until both phase renames are shipped and
the runtime copies are re-synced. Asking a vessel to fix a bug that
prevents the vessel's own output from taking effect is a bootstrap paradox.
Every P0 item that even *transitively* depends on a reload (so: almost all
of them) must be sequenced after Phase 1 of Prong 1.

**Second, PRs #366 and #372 conflict with each other as well as with
main.** The assessment treats them as two independent "in-flight" PRs and
assumes landing them is additive. In reality #372's `runner.go` -601 /
`evaluator_loop.go` +461 refactor reshapes the same function bodies that
#366 extends for `enforcePhasePolicy` / `enforceDefaultBranchPushPolicy`,
and #366's new `runner_prop_test.go` stanzas overlap with #372's
`runner_test.go` additions. Landing them in the wrong order, or letting
the vessel retry-loop try both at once, produces deterministic conflict
chains. The external agent has to rebase them serially against a freshly
unblocked main.

### P0 assignments (all 10 items)

| §7 P0 | Item                                                                | Prong   | Reasoning |
| ----- | ------------------------------------------------------------------- | ------- | --------- |
| 1     | Merge-triggered reload rejected by own validator                    | **P1**  | Blast radius zero, unblocks every other P0 in cascade, and is a protected-surface edit that vessels cannot safely make. Must be phase 1 of Prong 1. Extended to cover the latent `test-critic` second-hit I found during verification. |
| 2     | Policy class matrix not enforced on phase writes                    | **P1**  | PR #366 exists, is `ready-to-merge`, and is blocked on a rebase that the vessel loop cannot resolve. Landing it is a direct rebase, not a greenfield implementation, so it is correctly external-agent work. |
| 3     | AuditEntry missing `workflow_class` / `policy_rule` / `rule_matched` / `operation` / `file_path` / `vessel_id` | **P1**  | Bundled into PR #366. Same rationale. |
| 4     | `xylem config validate` / `xylem workflow validate` CLI missing     | **P2**  | No existing PR, pure new code (two small command files wrapping existing `Config.Validate()` / `workflow.LoadWithDigest`), vessel-friendly scope. Defer to Prong 2 once reload is unblocked. |
| 5     | `xylem init` does not emit `AGENTS.md`                              | **P2**  | Self-contained template + render step. Small, vessel-friendly. Prong 2. |
| 6     | xylem's own `.xylem.yml` still flat (no `profiles:` field)          | **Deferred to maintainer judgment** — see §4. Executing productize §14 steps 1–7 on the live repo is exactly the kind of control-plane rewrite the class-matrix enforcement exists to protect. Doing this before PR #366 is enforced in `warn` mode (§8 open question 1) is load-bearing on a safety net that does not yet exist. Strong recommendation: **do not ship this until PR #366 is merged and has run `warn` for at least 24 hours.** Even then, the external agent should do the mechanical edit with the daemon paused, not route it through a vessel. Tracked under existing issue #240, not duplicated. |
| 7     | `queue.jsonl` + `audit.jsonl` still at flat `.xylem/` root          | **P2**  | Productize §5.2 step. Schema-compatibility shim is needed because the live daemon has a 178-line queue.jsonl and an in-flight audit.jsonl that cannot be silently relocated. A vessel can do this carefully in one PR if the issue body names the compat rule. Prong 2. |
| 8     | Cost schema missing `daily_budget_usd` / `per_class_limit` / `on_exceeded` | **P2**  | Pure schema extension. Vessel-friendly. Prong 2. |
| 9     | `BudgetGate.Check` is a permissive no-op stub                       | **P2**  | Depends on #8 landing first. Vessel-friendly once schema is in place. Prong 2, blocked on #8. |
| 10    | Evidence claims `untyped` in production (classify at source)        | **P1 and/or P2**  | The assessment is right that the fix is literally one line at `cli/internal/runner/runner.go:2988`. So short and so isolated that it might as well ride along with the PR #366 rebase. But if PR #366 rebase blows out in scope, spin this off as a trivial standalone Prong 2 issue. **Plan: opportunistic — fold into Prong 1 Phase 3 if it doesn't expand the diff, else Prong 2 issue E-2.** |

### P1 assignments (§7 items 11–25)

| §7 P1  | Item                                                     | Prong   | Reasoning |
| ------ | -------------------------------------------------------- | ------- | --------- |
| 11     | Generator/evaluator loop not wired                       | **P1**  | PR #372 is `ready-to-merge` and stuck on conflicts that the vessel retry loop keeps re-re-resolving. External-agent rebase is the unlock. |
| 12     | Context compilation pipeline (ctxmgr) not wired          | **P2**  | Tracked under #60. Strip `xylem-failed`, re-scope in a Prong 2 issue. |
| 13     | Memory package not wired                                 | **P2**  | Same — tracked under #60, re-scope. |
| 14     | Tool catalog with permission scopes not wired            | **P2**  | No existing issue. Prong 2, new issue. |
| 15     | `xylem adapt-repo` CLI wrapper + `--dry-run`             | **Deferred** — MAY, `§3.5 "no interactive prompts"` adjacent, low payoff before adapt-plan schema lands. Track under §7 P1 #17 rollup. |
| 16     | `xylem audit` CLI                                        | **P2**  | New CLI. Prong 2. |
| 17     | `adapt-plan.json` schema + validator                     | **P2**  | Prong 2. Conditional on external-repo rollout; not blocking. |
| 18     | `validation.working_dir` + per-workflow `required_for`   | **P2**  | Prong 2 — productize §A.6. PR #374 / #377 were partial, reactive fixes; the structural change is left. |
| 19     | Agent-readable runtime artifacts                         | **P2**  | Tracked under #59. Strip label, re-scope. |
| 20     | Eval corpus + baseline comparison                        | **P2**  | Tracked under #57. Strip label, re-scope. |
| 21     | Runtime containment / network policy / scoped secrets    | **P2**  | Tracked under #58. Strip label, re-scope. |
| 22     | Default-branch boundary linter (`cli/internal` ≠ `cli/cmd`) | **P2**  | Small, vessel-friendly. Prong 2 new issue. |
| 23     | Sprint contracts / mission decomposition                 | **Deferred** — §8 open question: `[Emerging]`, gated on model-capability regression that is not present today. |
| 24     | USD cost pricing not wired into `cost.Tracker`           | **P2**  | Small, vessel-friendly. Depends on P0 #8 landing first. |
| 25     | Dormant `signal` package                                 | **Deferred** — MAY, wait for phase-stall signal to exceed heuristic-justified threshold. Not filing now. |

### P2 assignments (§7 items 26–50)

- P2 items #26, #32, #39 (artifact coverage, auto_merge validation, trust-boundary typing) are consolidated into a single **Prong 2 umbrella issue H-1** on phase-artifact and claim-completeness.
- P2 items #27 (class matrix merged with glob eval order) and #41 (runner-level class enforcement test case) are consolidated into **Prong 2 issue E-1** following PR #366.
- P2 item #47 (`doc-garden` reload-block verification) is already solved by Prong 1 Phase 1 — the reload regex fix means any reload that picks up `doc-garden.yaml` now sees the fixed validator. Noted in Phase 1 acceptance criteria.
- P2 items #34 (`AGENTS.md` at xylem repo root), #35 (`DefaultProtectedSurfaces` rationale refresh), #44 (`pr_opened` restart-survival test), #45 (adapt-repo closed-issue dedup test), #46 (`adaptation-log.jsonl` rate-limit) — rolled into **Prong 2 umbrella issue H-2** on housekeeping.
- P2 items #28 (`workflow-health-report` dedup), #33 (`max_debounce` ceiling) — consolidated into **Prong 2 issue C-1** on scanner/config validation.
- P2 items #29 (`cost.reset_timezone`), #30 (per-workflow cost alerts), #31 (`xylem profile lock` command), #43 (`profile.lock` force-path audit), #49 (AGENTS.md hierarchy test) are **deferred entirely** — see §4.
- P2 items #36 / #37 (retry-chain cascades) are dissolved by Prong 1 landing PRs #366 and #372. Not backlog items; side-effects.
- P2 items #38 / #48 / #50 are signal observations, not actionable tasks.
- P2 item #40 — confirm intent on `cli/internal/cadence` and `cli/internal/skills` — **do not file as issue; capture as a single §8 maintainer question at the end of this plan.**
- P2 item #42 — confirm `TestProp_PolicyStableUnderReorder` coverage — **fold into PR #366 rebase acceptance criteria in Phase 2.**

### What I disagree with in §6 "shortest path" re-sequencing

The assessment's §6 ordering is:
1. rename `create-issues`,
2. move queue/audit under `state/`,
3. emit `AGENTS.md` stub,
4. ship `xylem config validate` + `xylem workflow validate`,
5. land PR #366,
6. land PR #372,
7. wire evidence levels,
8. flip xylem's own `.xylem.yml` to `profiles:`.

**I disagree with steps 2 and 8 being on the "shortest path":**

- **Step 2 (queue/audit relocation)** is not on any external-repo
  productization-readiness critical path. For a new external repo the queue
  is empty at day 0 — the flat-vs-`state/` path doesn't matter. It only
  matters for xylem-on-xylem, where the live daemon has 178 vessels in
  flight and the path change needs a compat shim. Reclassifying to P1.
- **Step 8 (flip xylem to `profiles:`)** is a self-modification that needs
  class-matrix enforcement to already be live in `warn` mode (§8 open
  question 1). Doing it before step 5 is landed is the kind of
  unprotected control-plane edit that the class matrix exists to catch.
  The correct order is: **5 → wait 24h in warn → 8**, and with the daemon
  **paused** during the edit. Reclassifying step 8 to "deferred pending
  maintainer decision after step 5 lands".

### What PR #366 and PR #372 should be

**PR #366: rebase-and-land as Prong 1.** Cost of re-filing is strictly
greater than cost of rebase:
- The PR carries 1102 lines of reviewed production code, a full test
  matrix extension across `intermediary_test.go` +145, `intermediary_prop_test.go` +95,
  `runner_test.go` +296, `runner_prop_test.go` +20, `config_test.go` +58;
- the conflict is on two `*_prop_test.go` files that have drifted due to
  unrelated PR #374 / #377 / #375 test-path fixups, not on the actual
  implementation surface;
- the vessel retry loop has tried the same resolution four times and
  failed — which proves the conflict is not one a copilot vessel can
  structurally resolve by re-running `git merge`.

**PR #372: rebase-and-land as Prong 1, after #366.** Same reasoning, plus:
- #372 refactors the runner (`runner.go` -601 / `evaluator_loop.go` +461)
  which means it must land *on top of* #366, not underneath, or #366's
  `enforcePhasePolicy` extensions will need to be re-ported into the
  extracted module;
- the conflict between #372 and main is also on the same prop-test files;
- no greenfield reimplementation is on the table because the evaluator
  loop code is the PR #372 diff and is non-trivial to rebuild.

### Deferred items (justification collected in §4 below)

- §7 P0 #6 (flip xylem's own config to `profiles:`)
- §7 P1 #15 (`xylem adapt-repo` CLI wrapper)
- §7 P1 #23 (sprint contracts)
- §7 P1 #25 (signal package)
- §7 P2 #29, #30, #31, #43, #49

All others are placed.

---

## 2. Prong 1 — external-agent work

Phases are strictly dependency-ordered. Each phase must be mergeable on
its own.

### Phase 1 — unblock reload (≤30 minutes of work, blast radius zero)

**Citation.** §1 ship-blocker #3; §7 P0 #1; §6 shortest-path step 1.
Extended with the second-hit `test-critic` finding from this plan's audit
correction.

**Why this is Prong 1.** Vessels cannot edit the protected control
surfaces needed. The fix is mechanical, well-scoped, and strictly
blocking the rest of the work.

**Files to touch** (all edits preserve exact indentation and surrounding
comment structure):

1. `cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml:7`
   — change `- name: create-issues` → `- name: create_issues`.
2. `cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml:8`
   — change `prompt_file: .xylem/prompts/diagnose-failures/create-issues.md`
   → `prompt_file: .xylem/prompts/diagnose-failures/create_issues.md`.
3. Rename `cli/internal/profiles/self-hosting-xylem/prompts/diagnose-failures/create-issues.md`
   → `cli/internal/profiles/self-hosting-xylem/prompts/diagnose-failures/create_issues.md`
   (via `git mv`).
4. `cli/internal/profiles/self-hosting-xylem/prompts/diagnose-failures/refine.md:4`
   — change `{{.PreviousOutputs.create-issues}}` → `{{.PreviousOutputs.create_issues}}`.
5. `cli/internal/profiles/core/workflows/review-pr.yaml:10`
   — change `- name: test-critic` → `- name: test_critic`.
6. `cli/internal/profiles/core/workflows/review-pr.yaml:11`
   — change `prompt_file: .xylem/prompts/review-pr/test-critic.md` →
   `prompt_file: .xylem/prompts/review-pr/test_critic.md`.
7. Rename `cli/internal/profiles/core/prompts/review-pr/test-critic.md`
   → `cli/internal/profiles/core/prompts/review-pr/test_critic.md`.
8. `cli/internal/profiles/core/prompts/review-pr/review.md:14`
   — change `{{.PreviousOutputs.test-critic}}` → `{{.PreviousOutputs.test_critic}}`.
   (Leave the prose references to "test-critic findings" on lines 21, 54,
   69 as-is — they are human-readable text, not template identifiers.)
9. Tests: grep under `cli/internal/profiles/**/_test.go` and
   `cli/cmd/xylem/init_test.go` for string literals `"create-issues"` or
   `"test-critic"` and update to the new names. Re-run
   `go test ./cli/cmd/xylem/...` and `go test ./cli/internal/profiles/...`.
10. **Runtime copies** (authorised by the user's direct Prong 1 request —
    these are protected surfaces but the user's task text explicitly
    instructs the external agent to unblock the daemon): repeat changes
    1–8 in the two runtime locations:
    - `/Users/harry.nicholls/repos/xylem/.xylem/workflows/diagnose-failures.yaml`
      and `.../.xylem/prompts/diagnose-failures/...` (repo root runtime).
    - `/Users/harry.nicholls/repos/xylem/.daemon-root/.xylem/workflows/diagnose-failures.yaml`
      and `.../.daemon-root/.xylem/prompts/diagnose-failures/...` (live
      daemon runtime).
    - Same for `review-pr.yaml` and its prompts in both locations.
    - **These runtime edits must be coordinated with the live daemon.**
      Before editing `.daemon-root/...`, run `./cli/xylem pause` (or the
      equivalent daemon-pause mechanism) to prevent the watchdog from
      picking up mid-edit state. Do not kill the daemon — it is
      processing the PR #366 / #372 rebases and must survive.
11. After the edit: request a reload via either SIGHUP to daemon PID 1343
    or `xylem daemon reload` CLI. Confirm in
    `.daemon-root/.xylem/state/daemon-reload-audit.jsonl` that the next
    entry is an `allow`.

**Concrete change summary.**

- Six phase-name + prompt-file + template-reference renames across six
  source files, each occurring in exactly three locations (profile source
  + two runtime copies), for a total of about eighteen text edits and
  four file renames.
- Zero logic changes.
- One git commit for the profile-source edits (will be a normal PR),
  plus a separate manual edit to the runtime copies that never goes into
  git (they are regenerated from the profile source on the next `xylem
  init --force`).

**Acceptance criteria.**

- [ ] `go test ./cli/internal/workflow/...` — phase-name regex still
      passes for the new names.
- [ ] `go test ./cli/internal/profiles/...` — composed profile validates
      cleanly. Will also catch any stale smoke-test assertion referencing
      the old phase names.
- [ ] Manually run
      `xylem daemon reload --dry-run --config .daemon-root/.xylem.yml` (or
      the equivalent CLI invocation for the running daemon) — expect
      `reload ok`, not a `phase name … is invalid` rejection.
- [ ] Observe next entry in `.daemon-root/.xylem/state/daemon-reload-audit.jsonl`
      is `allow` with no `phase name … is invalid` error.
- [ ] `.daemon-root/.xylem/state/daemon-reload-log.jsonl` no longer shows
      consecutive denies.

**Dependencies.** None. Phase 1 is the root.

**Blast radius.** Low.

- Profile-source changes are source-only — vessels that previously tried
  to invoke the hyphenated phase names were failing anyway (the template
  reference `{{.PreviousOutputs.create-issues}}` is not valid Go template
  syntax and would have errored at render time).
- Runtime-copy edits touch the live daemon but are byte-local and
  reversible. The daemon is paused during the edit so no vessel launches
  can race with it.
- No backward-compat concern — no external consumer references these
  phase names.

**Effect on in-flight PRs.** Unblocks **every** merge-triggered reload
going forward, including PRs #366, #372, #3 (release-please), and all
vessel-produced PRs. Necessary precondition for Phase 2 and Phase 3 of
Prong 1 to have any propagation effect.

### Phase 2 — rebase and land PR #366 (workflow class matrix enforcement)

**Citation.** §1 ship-blockers #1 and #2; §7 P0 #2 and #3; §6
shortest-path step 5; tracking issue #235.

**Why this is Prong 1.** (a) PR already exists, already reviewed, already
carries `ready-to-merge`; (b) vessel retry loop is demonstrably unable to
resolve the same conflict on successive attempts (4-deep chain); (c) the
merge conflict is on two `*_prop_test.go` files that were reshaped by
unrelated recent PRs #374, #375, #377 — a purely mechanical rebase, not
a logic rewrite.

**Files to touch** (rebase, not re-implement).

1. `cli/internal/config/config_prop_test.go` — reconcile PR #366's
   `intermediary.PolicyMode` harness config tests against the current
   `validateWorkflowRequirements` rewrite at `cli/internal/config/config.go:1036`.
2. `cli/internal/runner/runner_prop_test.go` — reconcile PR #366's
   `TestProp_*` additions with the current prop-test file (sample the
   `pr-366-resolve-conflicts-retry-*/merge_main.output` vessel logs for
   the exact conflict hunks).
3. Everything else in the PR #366 diff (config.go +17/-18, intermediary
   changes +218/-39, runner.go +211/-17, test files) stays as-is unless
   a merge driver reports a hunk collision — sample the retry outputs
   for any additional conflict hunks.
4. After the rebase completes, verify:
   - `cli/internal/policy/class_prop_test.go` does in fact contain
     `TestProp_PolicyStableUnderReorder` (§7 P2 #42). If not, add it to
     this PR. If yes, mark the §7 P2 #42 item as confirmed.
   - `TestSmoke_S4_WorkflowClassEnforcement` (named in the PR #366 body)
     is present and green.

**Concrete change summary.** Interactive rebase of `feat/issue-235-235`
onto `origin/main`, resolving the two prop-test conflicts by hand (using
the retry vessel's `merge_main.output` as the record of what the copilot
kept proposing), then `git push --force-with-lease`. One additional
commit if `TestProp_PolicyStableUnderReorder` needs to be added.

**Acceptance criteria.**

- [ ] `cd cli && go vet ./...` — clean.
- [ ] `cd cli && go build ./cmd/xylem` — builds.
- [ ] `cd cli && goimports -l ./cli/...` — no output (the PR #374/#377
      gate script is the authoritative source).
- [ ] `cd cli && go test ./...` including the new `TestSmoke_S4_WorkflowClassEnforcement`.
- [ ] `cd cli && go test ./internal/policy -run TestProp` — covers both
      `TestEvaluate` and `TestProp_PolicyStableUnderReorder`.
- [ ] PR #366 flips to `MERGEABLE`; `gh pr merge 366 --auto --squash`
      succeeds, or admin-merge under the `ready-to-merge` label policy
      from PR #293.
- [ ] First post-merge reload at daemon completes `allow`.
- [ ] First post-merge `intermediary.AuditEntry` persisted contains
      `workflow_class` and `policy_rule` fields in the JSONL line (verify
      by `tail -1 .daemon-root/.xylem/audit.jsonl | jq .`).

**Dependencies.** Phase 1 (otherwise the merge-triggered reload will
still reject).

**Blast radius.** Medium.

- The PR extends the intermediary audit schema — existing entries in
  `.daemon-root/.xylem/audit.jsonl` (547 lines) lack the new fields but
  that is a strict forward-compatible additive change for JSONL readers.
- The PR flips `enforcePhasePolicy` from pure glob evaluation to
  class-matrix + glob evaluation. If the class matrix in
  `cli/internal/policy/class.go:40-68` is overly strict for a currently-running
  vessel class, existing in-flight phases could start failing at policy
  time. **Mitigation: the PR body names `intermediary.PolicyMode` with
  `warn|enforce` states. Phase 2 acceptance criteria must include
  "daemon runs PR #366 in `warn` mode by default". Read the PR's
  `config.go` change for the `HarnessPolicyMode` field and confirm that
  its zero-value is `warn`; if not, patch to make it so before merging.**

**Effect on in-flight PRs.** (1) Dissolves the
`pr-366-resolve-conflicts-retry-*` cascade by making the PR
mergeable. (2) Makes #372's eventual rebase easier because #372's
runner.go refactor now rebases on the class-matrix-wired runner,
and the second rebase has fewer surprises than two independent
rebases on the same surface.

### Phase 3 — rebase and land PR #372 (generator/evaluator loops), optionally fold in the one-line evidence-level fix

**Citation.** §1 fastest win #3; §7 P1 #11; §6 shortest-path step 6;
tracking issues #151 and #313.

**Why this is Prong 1.** Same reasoning as Phase 2 — the PR exists, is
reviewed, is `ready-to-merge`, and the vessel retry loop is stuck. The
refactor is too large and intertwined with runner.go to safely re-implement
under time pressure.

**Files to touch.**

1. Rebase `feat/issue-151-151` onto `origin/main` after Phase 2 lands.
2. Primary conflict surface is `cli/internal/runner/runner.go` (601 lines
   deleted, 207 added by #372, also touched by #366 +211/-17). Resolve
   by:
   - keeping #366's `enforcePhasePolicy` / `enforceDefaultBranchPushPolicy`
     additions,
   - and porting them into the extracted `cli/internal/runner/evaluator_loop.go`
     only if they sit inside a function that was moved by #372 — in
     practice the phase-enforcement helpers live in the phase-execution
     path that #372 leaves in `runner.go`, so the port is unlikely to
     be needed.
3. `cli/internal/runner/runner_test.go` (+208/-7 by #372; +296/-17 by
   #366) — mechanical merge, keeping both sets of test additions.
4. The new `cli/internal/profiles/core/prompts/*/implement_evaluator.md`
   files (+28 each) and the `cli/internal/profiles/core/workflows/fix-bug.yaml`
   / `implement-feature.yaml` `evaluator:` stanza additions (+14 each)
   land unchanged.
5. `cli/internal/workflow/workflow.go` gets +57 lines for the `Evaluator`
   YAML struct additions — no collision with current regex changes.

**Opportunistic one-line evidence-level fix (P0 #10).** If the rebase is
clean, add a single commit that edits
`cli/internal/runner/runner.go` around the `buildGateClaim` switch at
origin/main line 2966–2985: add a `default:` branch that classifies
command-gate claims as `evidence.BehaviorallyChecked`. Pseudocode:

```go
switch p.Gate.Type {
case "live":
    claim.Level = evidence.ObservedInSitu
    // ... existing live branch ...
default:
    claim.Level = evidence.BehaviorallyChecked  // NEW
    claim.Checker = p.Gate.Run
    claim.TrustBoundary = "Command gate output"  // NEW
}
```

This fix is in the same file as PR #372's refactor and must be applied
after the rebase, not before, to avoid triggering a third rebase loop.
**If the PR #372 rebase blows out in scope (more than an hour of
conflict resolution), abort the opportunistic fold-in and spin P0 #10
off into Prong 2 issue E-2 (drafted in §3 below).**

**Acceptance criteria.**

- [ ] Same five `go` commands from Phase 2.
- [ ] PR #372 flips to `MERGEABLE`, merges.
- [ ] Run one `fix-bug` vessel against a trivial issue and confirm the
      evaluator phase fires (check `.xylem/state/phases/<id>/` for an
      `evaluator.output` file or the renamed equivalent).
- [ ] If P0 #10 folded in: inspect a newly-written
      `.xylem/state/phases/<id>/evidence-manifest.json` and confirm the
      gate claim level is `"behaviorally_checked"` (not `"untyped"`).
- [ ] Close tracking issues #151 and #313.

**Dependencies.** Phase 2 (PR #366 must land first to minimise the
rebase surface on `runner.go`).

**Blast radius.** Medium-high — `runner.go` is the hottest file in the
harness and the evaluator loop is a new wave-level execution primitive.
But the PR has been through review and has existing unit tests.
The fallback is rollback via `gh pr revert`.

**Effect on in-flight PRs.** Dissolves the `pr-372-resolve-conflicts-*`
retry chain. Unlocks the evaluator-based fastest-win #3 entirely. If
P0 #10 folds in, also closes the "evidence untyped in production"
ship-blocker pre-emptively.

### Phase 4 — post-merge verification (no code changes)

**Why included.** The plan needs an explicit "Prong 1 is done" gate
that doesn't depend on me (the external agent) passing control back to
the user with unverified assumptions.

**Steps.**

1. After PR #366 merges, watch `.daemon-root/.xylem/state/daemon-reload-audit.jsonl`
   for the next reload event. Expect `allow`.
2. After PR #372 merges, watch for a second `allow` event.
3. Run `grep workflow_class .daemon-root/.xylem/audit.jsonl | wc -l` —
   expect a non-zero number once the daemon has handled one phase
   under the new intermediary code.
4. Run `find .daemon-root/.xylem/state/phases -name 'evidence-manifest.json'
   -newer .daemon-root/.xylem/state/daemon-health.json -exec jq '.claims[0].level'
   {} \;` — expect `"behaviorally_checked"` (not `"untyped"`) on any
   post-#372 evidence manifest, **if** the P0 #10 one-liner folded in.
5. Run `gh pr view 3 --json mergeable` — the release-please PR has been
   held by the reload block too. It may or may not be mergeable
   depending on what changed in main since it was opened.
6. Decide: are we ready to kick off Prong 2? Look at the 11-item open-issue
   set in the table above and check which have been picked up by the
   scanner and are already in progress — skip filing any issue that
   already has a running vessel.

**Acceptance criteria.**

- [ ] Three consecutive daemon reloads post-merge all return `allow`.
- [ ] `audit.jsonl` contains at least one entry with the new fields.
- [ ] Prong 2 issues (§3 below) are filed with `gh issue create` and the
      scanner picks them up within one scan cycle (typically ≤60s).

**Dependencies.** Phases 1, 2, 3.

**Blast radius.** Zero — observation only.

---

## 3. Prong 2 — xylem backlog (GitHub issue drafts)

All drafts below are self-contained: no reference to "the assessment" or
"the plan" unless explicitly quoted, and no reliance on the vessel being
able to read anything other than the issue body. File:line citations and
command output are inlined.

Theme groupings:

- **A — CLI surface** (A-1, A-2, A-3)
- **B — State/config split** (B-1)
- **C — Scanner / config validation** (C-1)
- **D — Cost/budget enforcement** (D-1, D-2, D-3)
- **E — Evidence typing & class-matrix follow-ups** (E-1, E-2)
- **F — Dormant-package wiring** (F-1, F-2, F-3)
- **G — Adapt-repo / productize finishing** (G-1, G-2)
- **H — Housekeeping umbrellas** (H-1, H-2)
- **I — Reload drift root cause** (I-1)
- **J — Re-scope existing xylem-failed issues** (J-57, J-58, J-59, J-60)

### A — CLI surface

#### A-1: feat: add `xylem config validate` CLI subcommand

```
Title: feat: add `xylem config validate` CLI subcommand for scaffold + ops verification
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  The productize spec §2.7 and §8.8 list `xylem config validate` as a
  required part of the validation surface for external repos. Right now
  the only way to validate a `.xylem.yml` is to run `xylem init` or
  `xylem daemon reload` and inspect the error. That is slow, destructive
  (init scaffolds files), and unavailable during adapt-repo phase 4
  ("validate") which the `adapt-repo` workflow wants to run inline.

  The underlying library function already exists: `Config.Validate()` is
  called from `config.Load()` at `cli/internal/config/config.go` (see the
  `validateWorkflowRequirements` path at lines 636 and 1036, plus the
  top-level validators that run during Load). All this issue needs is a
  thin CLI wrapper.

  ## Current behavior

  `cli/cmd/xylem/root.go:90-115` lists the registered subcommands:
  `bootstrap`, `doctor`, `daemon`, `drain`, `exec`, `field-report`,
  `init`, `pause`, `retry`, `scan`, `status` — no `config` subcommand.
  Running `xylem config validate` today errors with `unknown command
  "config" for "xylem"`.

  ## Proposed change

  1. Create `cli/cmd/xylem/config.go` with a top-level `config` cobra
     command and a `validate` subcommand. Follow the same pattern used
     by `cli/cmd/xylem/daemon.go` + `daemon_reload.go` for subcommand
     registration.
  2. The `validate` subcommand takes one optional positional arg: the
     path to the config file. If omitted, it looks for
     `./.xylem.yml` relative to the working directory (same as the
     daemon).
  3. It also takes a `--proposed` flag; when set, it accepts a path to
     a candidate file instead of the current on-disk config (spec
     §8.8 naming).
  4. Call `config.Load(path)`; on success print `ok: <path>` and exit
     0. On failure print the error with `cobra`'s standard error
     formatter and exit 1. Do not re-wrap the error — `config.Load`
     already annotates errors with sufficient context.
  5. Register in `cli/cmd/xylem/root.go` alongside the other
     subcommands (maintain alphabetical order).
  6. Add unit tests at `cli/cmd/xylem/config_test.go` that cover:
     a valid flat config, a valid `profiles:` config, a malformed YAML,
     a config with an unknown `harness.policy.mode` value. Use the
     existing `cobra_test.go` harness pattern.

  ## Acceptance criteria
  - [ ] `go vet ./...` clean; `go test ./cli/cmd/xylem/...` green.
  - [ ] `xylem config validate` on a valid config exits 0 and prints
        `ok: <path>`.
  - [ ] `xylem config validate` on a malformed config exits 1 with the
        same error text that `config.Load()` returns.
  - [ ] `xylem config validate --proposed /tmp/candidate.yml` works
        even when `./.xylem.yml` does not exist.
  - [ ] `xylem config validate --help` shows the command in the
        subcommand list output by `xylem --help`.

  ## Dependencies
  - Blocked by: none (can ship immediately after Prong 1 reload unblock).
  - Related to: #241 (productize tracking parent).

  ## Notes / gotchas
  - Do NOT call `config.Load` directly on the result of `os.ReadFile` —
    the public `config.Load(path string)` takes the path itself because
    it uses `filepath.Dir(path)` to resolve relative state paths.
  - Exit codes must be explicit: return `nil` for success, return the
    error from the RunE closure for failure — do not `os.Exit(1)`
    inside the handler because that breaks test assertions via
    `cobra_test.go`.
  - The command should respect `--json` output if that flag is already
    part of the root `persistentFlags()` — check `cli/cmd/xylem/root.go`
    for the pattern.
```

#### A-2: feat: add `xylem workflow validate` CLI subcommand

```
Title: feat: add `xylem workflow validate` CLI subcommand
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Spec §2.7 and §8.8 also call for `xylem workflow validate`. This is
  the counterpart to `xylem config validate` — it validates a single
  workflow YAML against the phase-name regex, prompt-file existence,
  gate-schema shape, and class-enum values.

  The library function already exists: `workflow.LoadWithDigest(path)`
  at `cli/internal/workflow/workflow.go:200` runs the full validation
  pipeline (same one used by the daemon reload at
  `cli/cmd/xylem/daemon_reload.go:467`). This issue is a thin wrapper.

  ## Current behavior

  `cli/cmd/xylem/root.go:90-115` has no `workflow` subcommand. Users
  can only get workflow validation errors as a side-effect of
  `xylem init` or a merge-triggered reload — which has caused three
  live reload rejections in the last 24h (see
  `.daemon-root/.xylem/state/daemon-reload-audit.jsonl` before the
  Prong 1 reload unblock).

  ## Proposed change

  1. Create `cli/cmd/xylem/workflow.go` with a top-level `workflow`
     cobra command and a `validate` subcommand.
  2. The subcommand takes one or more positional args — workflow YAML
     paths. If no args, default to walking
     `<state_dir>/workflows/*.yaml`.
  3. Same `--proposed` flag as `xylem config validate` (see #A-1).
  4. For each workflow file call `workflow.LoadWithDigest(path)`.
     Collect failures, print one line per file on stderr, exit 1 if
     any failed, else print `ok: N workflow(s)` and exit 0.
  5. Register in `cli/cmd/xylem/root.go`. Alphabetical order.
  6. Add unit tests at `cli/cmd/xylem/workflow_test.go`. Use a tempdir
     + `os.WriteFile` pattern similar to `daemon_reload_test.go`.

  ## Acceptance criteria
  - [ ] `go vet ./...` clean; `go test ./cli/cmd/xylem/...` green.
  - [ ] `xylem workflow validate path/to/wf.yaml` returns the same
        error text as the live daemon reload validator for the same
        file.
  - [ ] `xylem workflow validate` with no args walks the default
        workflow directory and reports coverage as `ok: N workflow(s)`.
  - [ ] `xylem workflow validate --proposed /tmp/new.yml` works on a
        standalone file outside the configured state dir.

  ## Dependencies
  - Blocked by: none.
  - Related to: #A-1 (should ship together for consistent UX).

  ## Notes / gotchas
  - The validator depends on `resolvePromptFilePath` stat'ing prompt
    files. When validating a `--proposed` file that lives outside the
    configured state dir, the prompt stat will fail unless the
    prompts are co-located. Either: (a) accept the failure and surface
    it as the error, or (b) add a `--skip-prompt-stat` flag. Prefer
    (a) — it matches the daemon reload behavior.
```

#### A-3: feat: add `xylem audit` CLI subcommand for JSONL tail and filtering

```
Title: feat: add `xylem audit` CLI subcommand (tail / denied / counts / rule)
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Spec §13.3 lists `xylem audit` as the operator-facing way to
  introspect the intermediary audit log (`.xylem/audit.jsonl`). Today
  the only way to read it is `jq`, which requires knowing the JSONL
  schema ahead of time. As the class-matrix lands and
  `intermediary.AuditEntry` gains `workflow_class`, `policy_rule`,
  `rule_matched`, `operation`, `file_path`, `vessel_id` fields (see
  PR #366), a typed CLI surface becomes the sane way to query it.

  ## Current behavior

  No `audit` subcommand exists
  (`cli/cmd/xylem/root.go:90-115`). 547 entries sit in
  `.daemon-root/.xylem/audit.jsonl`, all `allow`, no way to filter
  them without ad-hoc `jq`.

  ## Proposed change

  1. Create `cli/cmd/xylem/audit.go` with top-level `audit` cobra
     command and four subcommands: `tail`, `denied`, `counts`, `rule`.
  2. `xylem audit tail [-n N]`: print the last N entries (default 20)
     as formatted lines, one per entry. Newest last.
  3. `xylem audit denied`: print every entry whose `decision` is not
     `allow`. Useful once PR #366 lands and denies start appearing.
  4. `xylem audit counts`: print a histogram of `action` ×
     `decision`, formatted as a table. Expected output after PR #366:
     `phase_execute allow=N1 deny=N2 approve=N3`.
  5. `xylem audit rule <rule-name>`: print every entry whose
     `policy_rule` field matches. Error if the field is not present
     in the audit schema (pre-#366 logs).
  6. Use `encoding/json` on `bufio.Scanner` lines — do not load the
     whole file.
  7. Add tests at `cli/cmd/xylem/audit_test.go` with a small fixture
     JSONL file.

  ## Acceptance criteria
  - [ ] `go test ./cli/cmd/xylem/...` green.
  - [ ] `xylem audit tail -n 5` prints the last 5 entries from
        `.xylem/audit.jsonl` relative to the working-dir `.xylem.yml`.
  - [ ] `xylem audit denied` is empty on a pre-#366 log and non-empty
        on a post-#366 log after the first `deny`.
  - [ ] `xylem audit counts` output includes every distinct `action`
        present in the log.

  ## Dependencies
  - Blocked by: Prong 1 Phase 2 (PR #366) — the new AuditEntry fields
    only exist in the post-#366 schema.
  - Related to: #A-1, #A-2.

  ## Notes / gotchas
  - Be lenient about missing fields: older entries lack
    `workflow_class` / `policy_rule` / `rule_matched` etc. Print a `-`
    for missing fields rather than erroring.
  - The file path must respect `config.RuntimePath(state_dir, "audit.jsonl")`
    rather than hard-coding — when Prong 2 issue B-1 lands the path
    moves under `state/`.
```

### B — State/config split

#### B-1: feat: relocate `queue.jsonl` / `audit.jsonl` / `daemon.pid` under `state/`

```
Title: feat: move queue.jsonl + audit.jsonl + daemon.pid under state/ with compat shim
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Productize spec §5.2 separates the control plane (tracked files
  like `.xylem.yml`, workflows, prompts, HARNESS.md) from the runtime
  state (queue, audit, locks, worktrees). The split is partially done
  — phases, daemon-health, daemon-reload artifacts live under
  `.xylem/state/phases/`, `.xylem/state/daemon-health.json`,
  `.xylem/state/daemon-reload-*.jsonl`. **But `queue.jsonl`,
  `audit.jsonl`, `daemon.pid` are still at the flat `.xylem/` root.**

  On a live daemon the current state:
  - `.daemon-root/.xylem/queue.jsonl` (178 lines)
  - `.daemon-root/.xylem/audit.jsonl` (547 lines)
  - `.daemon-root/.xylem/daemon.pid`
  - `.daemon-root/.xylem/state/phases/…` (33 subdirs)

  A naive path change will orphan the live queue and audit log.

  ## Current behavior

  `cli/cmd/xylem/root.go:73` resolves the queue path as
  `filepath.Join(cfg.StateDir, "queue.jsonl")`. Audit log is written
  at `cli/internal/intermediary/intermediary.go` via `AuditLog.path`.
  Daemon PID is written by `daemon.go` startup.

  The existing helper `config.RuntimePath(stateDir, elems...)` at
  `cli/internal/config/config.go:377-388` already resolves
  `<stateDir>/state/<elems>` for the phases path. Unused for queue,
  audit, pid.

  ## Proposed change

  1. In `cli/cmd/xylem/root.go:73`, change the queue path from
     `filepath.Join(cfg.StateDir, "queue.jsonl")` to
     `config.RuntimePath(cfg.StateDir, "queue.jsonl")`.
  2. Same for `audit.jsonl` in the intermediary wiring (find the
     caller of `intermediary.NewAuditLog` and update the path
     argument) and `daemon.pid` in `cli/cmd/xylem/daemon.go`.
  3. Add a compat shim: on daemon startup, if the new path is
     missing AND the legacy flat path exists, rename the file
     (`os.Rename`) and log a single-line INFO. This must be atomic
     per file — rename is.
  4. Add a config-level migration helper at
     `cli/internal/config/migrate.go` (new file) with a
     `MigrateFlatStateToRuntime(stateDir string) error` function the
     daemon startup calls. Test it thoroughly.
  5. Update any test fixtures that write to the flat path.
  6. Document the migration in `docs/decisions/` (short ADR, ~40
     lines).

  ## Acceptance criteria
  - [ ] `go test ./cli/...` green on a fresh temp dir.
  - [ ] Smoke scenario: start daemon with the legacy flat layout,
        let migration run, confirm queue entries still dequeue via
        `xylem drain`.
  - [ ] Smoke scenario: start daemon with the new layout from
        scratch, no rename fires, queue works.
  - [ ] Smoke scenario: start daemon with BOTH legacy and new paths
        present — the migration MUST error loudly, not silently pick
        one. This catches half-migrated states.
  - [ ] `.daemon-root/.xylem/state/queue.jsonl` exists after one
        clean daemon cycle post-merge.

  ## Dependencies
  - Blocked by: none (can ship anytime after Prong 1 reload unblock).
  - Related to: #A-3 (the `xylem audit` command needs to resolve the
    new path via `config.RuntimePath`).

  ## Notes / gotchas
  - Do NOT delete the legacy file after rename — leave a breadcrumb
    `.xylem/queue.jsonl.migrated` (zero-byte marker) so ops can see
    the migration happened without diff-ing paths.
  - The audit log is append-only with file locking via
    `cli/internal/intermediary/intermediary.go` (see `lockPath`
    handling). Make sure the lock path moves with the data path.
  - The daemon PID file is special — you cannot atomically "rename"
    a PID file without risking a torn read. Handle by: acquire the
    daemon's own PID via `os.Getpid()`, write the new path, delete
    the legacy path. Do not rename.
  - Watch for `cli/cmd/xylem/doctor.go` references to flat paths —
    `doctor --fix` should not blow up on the half-migrated state.
```

### C — Scanner / config validation

#### C-1: feat: config validator rejects `daemon.auto_merge: true` without an `ops`-class workflow in the profile

```
Title: feat: validate that daemon.auto_merge=true requires an ops-class workflow in the active profile
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Productize §11.6 says auto-merge should be allowed only when the
  profile composition exposes an `ops`-class workflow (e.g.
  `merge-pr`). Today `DaemonConfig.AutoMerge` defaults to `false` at
  `cli/internal/config/config.go:163`, but when a profile flips it
  to `true` there is no validator rule that requires a
  `Workflow.Class == policy.Ops` workflow to be present. An external
  repo could enable auto-merge with only delivery-class workflows,
  which would silently violate the §11.6 contract.

  ## Current behavior

  `Config.Validate()` → `validateWorkflowRequirements()` at
  `cli/internal/config/config.go:1036` runs several structural
  checks (validation block shape, `working_dir`, profile presence)
  but does not cross-check `daemon.auto_merge` against workflow
  classes.

  ## Proposed change

  1. Add a new validator function `validateAutoMergeWorkflowClass`
     in `cli/internal/config/config.go` that returns an error if
     `cfg.Daemon.AutoMerge` is true AND no workflow in the composed
     profile has `Class == policy.Ops`.
  2. Call it from `validateWorkflowRequirements()`.
  3. Test cases at `cli/internal/config/config_test.go`:
     - `auto_merge=false`, no ops workflow → allow.
     - `auto_merge=true`, no ops workflow → reject with clear error.
     - `auto_merge=true`, ops workflow present → allow.
     - Edge: `auto_merge=true`, ops workflow has `Class` unset (zero
       value) → reject, because zero value is `Delivery`.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/config/...` green with new cases.
  - [ ] Running `xylem config validate` (A-1) on a contrived bad
        config reports the new error.
  - [ ] The xylem self-hosting overlay still validates cleanly
        because `merge-pr.yaml` exists and is ops-class.

  ## Dependencies
  - Blocked by: none.
  - Related to: #A-1 (the new validator is surfaced by the
    `xylem config validate` CLI).

  ## Notes / gotchas
  - The composed profile is what matters, not the per-profile file.
    Use `profiles.Compose(cfg.Profiles...)` and walk the resulting
    `ComposedProfile.Workflows` map.
  - Backwards compat: xylem's own config currently sets
    `daemon.auto_merge: true` and does have `merge-pr.yaml` in the
    overlay, so the self-hosting config should still pass.
```

### D — Cost/budget enforcement

#### D-1: feat: extend CostConfig with `daily_budget_usd`, `per_class_limit`, `on_exceeded`

```
Title: feat: extend CostConfig schema with daily_budget_usd, per_class_limit, on_exceeded
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Productize §12.1 proposes:
  ```
  cost:
    budget:
      daily_budget_usd: 50.0
      per_class_limit:
        delivery: 0.8   # 80% of daily
        ops: 0.1
      on_exceeded: pause   # | stop | warn
  ```
  Today `CostConfig.Budget` only carries `MaxCostUSD` and
  `MaxTokens` at `cli/internal/config/config.go:268-275` — no
  per-class, no daily window, no policy on exceed.

  `cost-report.json` files exist under
  `.daemon-root/.xylem/state/phases/<id>/cost-report.json` but
  `total_cost_usd` is `0` across all 29 samples because the cost
  pricing wiring is not done (see D-3).

  ## Current behavior

  ```go
  // cli/internal/config/config.go:268-275
  type BudgetConfig struct {
      MaxCostUSD float64 `yaml:"max_cost_usd,omitempty"`
      MaxTokens  int64   `yaml:"max_tokens,omitempty"`
  }
  ```

  No per-class slicing, no reset period, no consequence on exceed.

  ## Proposed change

  1. Extend `BudgetConfig` in `cli/internal/config/config.go` with:
     ```go
     DailyBudgetUSD float64           `yaml:"daily_budget_usd,omitempty"`
     PerClassLimit  map[string]float64 `yaml:"per_class_limit,omitempty"`
     OnExceeded     string            `yaml:"on_exceeded,omitempty"` // pause | stop | warn
     ResetTimezone  string            `yaml:"reset_timezone,omitempty"` // optional, spec §12.1
     ```
  2. Validate in `Config.Validate()`:
     - `OnExceeded` must be one of `pause`, `stop`, `warn` (or empty
       → default `warn`).
     - Each value in `PerClassLimit` must be in `[0.0, 1.0]`.
     - The sum of `PerClassLimit` values MAY exceed 1.0 (allows
       overlap / double-allocation) — do NOT reject on sum >1.
     - Keys of `PerClassLimit` must match known `policy.Class`
       values; unknown class name → validation error.
  3. Add unit tests at `config_test.go` for the happy path, each
     validation error branch, and YAML round-trip.
  4. Leave the existing `MaxCostUSD` / `MaxTokens` fields alone —
     additive extension only.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/config/...` green.
  - [ ] YAML round-trip test for a `cost:` block with all new
        fields passes.
  - [ ] Setting `on_exceeded: pause-now` errors with
        `unknown on_exceeded value "pause-now"`.
  - [ ] Setting `per_class_limit: { unknown_class: 0.5 }` errors.

  ## Dependencies
  - Blocked by: none.
  - Related to: #D-2 (scanner budget gate needs these fields),
    #D-3 (USD pricing feeds the counter the gate reads).

  ## Notes / gotchas
  - `policy.Class` values include at minimum Delivery, Ops, Housekeeping,
    Diagnostic — confirm against `cli/internal/policy/class.go:8-26` at
    implementation time (the enum may have grown).
  - `reset_timezone` is a MAY per spec §12.1; include the field in
    the schema but don't wire behavior — that's deferred.
```

#### D-2: feat: replace `BudgetGate.Check` stub with tracker-backed enforcement

```
Title: feat: replace BudgetGate.Check permissive stub with real tracker-backed per-class enforcement
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  `cli/internal/cost/budgetgate.go:23-34` is an intentionally
  permissive no-op:
  ```go
  func (g *BudgetGate) Check(_ string) Decision {
      decision := Decision{Allowed: true}
      if g == nil || g.budget == nil {
          return decision
      }
      if g.budget.CostLimitUSD > 0 {
          decision.RemainingUSD = g.budget.CostLimitUSD
      }
      return decision
  }
  ```

  Scanner calls it at `cli/internal/scanner/scanner.go:49,79,110-114`.
  Production consequence: there is no operational throttle on
  scanner enqueue at all. Pathological scanner behavior (e.g. label
  storm, runaway retry loop) has no stopping point.

  Productize §11.3 and §12.2 specify BudgetGate as an operational
  throttle — NOT a security control. Per spec §8 open question 2,
  the maintainer decision is to ship it as a real tracker-backed
  operational throttle.

  ## Current behavior

  - No per-class tracking of spend-to-date.
  - No daily reset.
  - No `on_exceeded` enforcement.
  - `cost-report.json` files accumulate under
    `.daemon-root/.xylem/state/phases/<id>/` but no aggregator reads
    them back.

  ## Proposed change

  1. Extend `cli/internal/cost/cost.go`'s `Tracker` (or create a
     new `cli/internal/cost/aggregator.go`) with a method:
     ```go
     func (t *Tracker) SpentToday(class string) float64
     ```
     that aggregates `cost-report.json` files from
     `<state_dir>/state/phases/*/cost-report.json` filtering by
     `recorded_at` within the last UTC day and by `by_class`
     match.
  2. Rewrite `BudgetGate.Check(class string) Decision` to:
     a. If `budget == nil` → `{Allowed: true, Reason: "no budget"}`.
     b. Fetch today's class spend via the tracker.
     c. Compute per-class cap = `DailyBudgetUSD * PerClassLimit[class]`
        (falling back to `DailyBudgetUSD` if no per-class limit set).
     d. If spend >= cap, honour `OnExceeded`:
        - `warn` → `{Allowed: true, Reason: "over cap"}`.
        - `pause` → `{Allowed: false, Reason: ...}` and emit a
          `budget-alerts.jsonl` line.
        - `stop` → `{Allowed: false, Reason: ...}` AND set a
          persistent marker file that `daemon.go` reads on startup
          to refuse new scan cycles until operator resets.
     e. Else `{Allowed: true, RemainingUSD: cap - spend}`.
  3. Unit tests at `cli/internal/cost/budgetgate_test.go`:
     - Empty state dir → allow with no budget.
     - Spend < cap → allow with remaining.
     - Spend >= cap + `warn` → allow.
     - Spend >= cap + `pause` → deny + alert file written.
     - Spend >= cap + `stop` → deny + marker file written.
     - Unknown class with no per-class limit uses total daily budget.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/cost/...` green.
  - [ ] Smoke: create a fixture tree with `cost-report.json` summing
        to $0.90 at class `delivery`, set `daily_budget_usd: 1.00`,
        `per_class_limit: { delivery: 1.0 }`, `on_exceeded: pause` —
        BudgetGate.Check("delivery") returns `Allowed: false`.
  - [ ] Scanner enqueue path (`cli/internal/scanner/scanner.go:79`)
        respects the `Allowed: false` decision — the same issue is
        NOT enqueued twice in a row.

  ## Dependencies
  - Blocked by: #D-1 (schema) and #D-3 (pricing). Without #D-3 the
    cost-report.json values are $0.00 and the gate will never fire.
  - Related to: scanner remediation-aware gating (#257, merged).

  ## Notes / gotchas
  - Do not block the scanner on cold-start: if the tracker has no
    data yet, treat spend as 0.
  - The "reset at UTC midnight" rule is the cheapest correct answer;
    leave `reset_timezone` from #D-1 unwired for now — per §4
    deferred list.
```

#### D-3: feat: wire real USD pricing into `cost.Tracker` from the `providers` model table

```
Title: feat: wire USD per-model pricing into cost.Tracker so cost-report.json total_cost_usd is non-zero
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  `cost-report.json` files currently report `total_cost_usd: 0`
  across all 29 sampled phase dirs in
  `.daemon-root/.xylem/state/phases/*/cost-report.json` — e.g.
  `.daemon-root/.xylem/state/phases/pr-366-resolve-conflicts/cost-report.json:7`.
  This happens because `cost.Tracker` records token counts broken
  down `by_model` but the model → USD conversion table is not
  populated.

  ## Current behavior

  Look at `cli/internal/cost/cost.go` for the tracker. The
  `UsageRecord` struct has fields like `ModelID`, `InputTokens`,
  `OutputTokens`. The `CostReport` struct has `ByModel map[string]float64`.
  Pricing → the `ByModel` map is never populated with dollar values.

  ## Proposed change

  1. Add `cli/internal/cost/pricing.go` with a hardcoded table:
     ```go
     var defaultPricing = map[string]ModelPrice{
         "claude-opus-4-6":     {InputPerMTok: 15.00, OutputPerMTok: 75.00},
         "claude-sonnet-4-6":   {InputPerMTok:  3.00, OutputPerMTok: 15.00},
         "claude-haiku-4-5":    {InputPerMTok:  0.25, OutputPerMTok:  1.25},
         "gpt-5.4-mini":        {InputPerMTok:  0.40, OutputPerMTok:  1.20},
         // ... add the models actually used by xylem vessels ...
     }
     ```
     (Cross-check with
     `.daemon-root/.xylem/state/phases/*/cost-report.json` `by_model`
     keys at implementation time. Use the exact strings that appear
     there.)
  2. Allow override via `providers.<name>.pricing` YAML block — the
     `Config.Providers` struct at `cli/internal/config/config.go:100-107`
     already carries provider metadata, extend with a `Pricing` map.
  3. In `Tracker.Record` (or the aggregator that builds
     `CostReport`), look up the model in merge(config override,
     default table), compute `cost = input_tokens/1e6 * input_per_mtok +
     output_tokens/1e6 * output_per_mtok`, and write it into
     `ByModel` and `TotalUSD`.
  4. Unit tests: known model hits the default table, unknown model
     falls back to zero (do NOT error — zero-cost telemetry is fine).
  5. Write ADR at `docs/decisions/` explaining why pricing is
     hardcoded vs. fetched from the API.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/cost/...` green.
  - [ ] A new vessel running through at least one phase writes a
        `cost-report.json` where `total_cost_usd` > 0.
  - [ ] `jq '.total_cost_usd'` on the latest post-merge phase dir
        reports a plausible number (pennies for a trivial phase).

  ## Dependencies
  - Blocked by: none.
  - Related to: #D-1, #D-2 (both depend on the output of this
    counter to make decisions).

  ## Notes / gotchas
  - Do NOT hit the pricing API at runtime — the hardcoded table is
    the right answer for now, per harness-engineering best
    practices "no external network calls during cost accounting".
  - Whenever Anthropic or OpenAI publishes new pricing, bump the
    constants. Track in the ADR.
```

### E — Evidence typing & class-matrix follow-ups

#### E-1: test: add runner-level integration test for policy class enforcement denial (spec §15.3 test case 4)

```
Title: test: add runner-level integration test for policy class-matrix denial (spec §15.3 test case 4)
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Productize §15.3 lists test case 4 ("class enforcement denial")
  as a required test that asserts the runner refuses to execute a
  phase when `policy.Evaluate(class, op)` returns `Deny`. PR #366
  lands the enforcement path. Spec says the property test
  `TestProp_PolicyStableUnderReorder` (spec §15.4) must also be
  present in `cli/internal/policy/class_prop_test.go`.

  At plan time, `cli/internal/policy/class_prop_test.go` exists
  but its coverage is unverified. This issue is the post-#366
  cleanup to verify both.

  ## Current behavior

  - `cli/internal/policy/class_test.go` covers `policy.Evaluate`
    as a pure function with a rule table.
  - There is no test that instantiates a `Runner`, runs a delivery
    phase against a contrived rule set that denies
    `OpCommitDefaultBranch`, and asserts the runner raises a
    denial error.
  - Property test coverage for reorder-stability is unverified.

  ## Proposed change

  1. Add `TestIntegration_RunnerRejectsClassMatrixDenial` at
     `cli/internal/runner/runner_test.go` that:
     a. Builds a minimal `Runner` with a delivery-class workflow.
     b. Configures the intermediary with a deny rule for
        `OpCommitDefaultBranch`.
     c. Runs one phase that would commit to the default branch.
     d. Asserts the phase fails with a `policy deny` error AND
        that an `AuditEntry` with `decision == deny` is written.
  2. Verify or add
     `TestProp_PolicyStableUnderReorder` at
     `cli/internal/policy/class_prop_test.go` — property: for any
     rule set, shuffling rule order yields the same decision for
     any fixed (class, op) pair given that rules are
     precedence-based and no two rules match at equal precedence.
  3. If reorder stability is NOT actually a property of the
     current rule engine (i.e. order matters), DO NOT force the
     property — instead flip the task to: document that order
     matters, and add a runner-side invariant test that makes the
     order explicit.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/runner -run TestIntegration_RunnerRejectsClassMatrixDenial`
        green.
  - [ ] `go test ./cli/internal/policy -run TestProp` covers either
        `TestProp_PolicyStableUnderReorder` OR a documented
        order-sensitivity invariant test.
  - [ ] Running on pre-#366 HEAD the new test fails cleanly (no
        panic) — confirms the test catches the regression.

  ## Dependencies
  - Blocked by: Prong 1 Phase 2 (PR #366 must land).
  - Related to: #235 (tracking).

  ## Notes / gotchas
  - The runner test harness already has helpers for a fake
    intermediary — reuse them (see `runner_test.go`
    `TestSmoke_S*` scenarios).
```

#### E-2: fix: classify command-gate evidence claims at source (runner.go:2988 default branch)

```
Title: fix: evidence claim level defaults to "untyped" — classify command-gate claims as BehaviorallyChecked at source
Labels: ready-for-work, bug, harness-impl
Body:
  ## Context

  Every `evidence-manifest.json` written by the live daemon has
  `"level": "untyped"` for every recorded claim — sampled across
  9/9 files. Example:
  `.daemon-root/.xylem/state/phases/issue-236/evidence-manifest.json:7`
  shows `"level": "untyped"`.

  This violates SoTA spec §4.6 which requires every completion
  claim to carry a typed level: `proved`, `mechanically_checked`,
  `behaviorally_checked`, or `observed_in_situ`.

  ## Current behavior

  At `cli/internal/runner/runner.go` the `buildGateClaim` helper
  (around line 2961) has:
  ```go
  claim := evidence.Claim{
      Level:         evidence.Untyped,
      // ...
      TrustBoundary: "No trust boundary declared",
  }
  if p.Gate != nil {
      switch p.Gate.Type {
      case "live":
          claim.Level = evidence.ObservedInSitu
          // ...
          claim.TrustBoundary = "Running system observation"
      default:
          claim.Checker = p.Gate.Run
          // NO level assignment — stays Untyped
      }
  }
  ```

  Only the `live` branch sets a typed level. Command gates (run
  via `p.Gate.Run`) fall through the `default:` branch with
  `level: Untyped` and `TrustBoundary: "No trust boundary
  declared"`.

  ## Proposed change

  Exactly two lines added in the `default:` branch at
  `cli/internal/runner/runner.go` in `buildGateClaim`:

  ```go
  default:
      claim.Level = evidence.BehaviorallyChecked  // NEW
      claim.Checker = p.Gate.Run
      claim.TrustBoundary = "Command gate output"  // NEW
  ```

  Plus the corresponding unit test:

  - In `cli/internal/runner/runner_test.go`, add
    `TestBuildGateClaim_CommandGate_IsBehaviorallyChecked` that
    constructs a phase with a command gate and asserts
    `claim.Level == evidence.BehaviorallyChecked` and
    `claim.TrustBoundary == "Command gate output"`.
  - Extend the existing live-gate test (if present) to assert the
    full switch table.

  Do NOT touch the `live` branch. Do NOT change the evidence
  package. This is surgical.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/runner -run TestBuildGateClaim` green.
  - [ ] After one post-merge vessel runs, the latest
        `.xylem/state/phases/<id>/evidence-manifest.json` has a
        claim with `"level": "behaviorally_checked"` and
        `"trust_boundary": "Command gate output"`.
  - [ ] Old `live` gate claims are unchanged.

  ## Dependencies
  - Blocked by: Prong 1 Phase 3 (if folded in) OR none (if shipped
    as its own PR post-Prong 1).
  - Related to: §7 P0 #10; sota-gap-snapshot.json verification.

  ## Notes / gotchas
  - If the runner is refactored by PR #372 and `buildGateClaim`
    moves into `cli/internal/runner/evaluator_loop.go` or elsewhere,
    find the new location via `grep -n BuildGateClaim cli/internal/runner/`.
  - The `evidence.Level` constants are defined in
    `cli/internal/evidence/evidence.go:20-26` — confirm
    `BehaviorallyChecked` is spelled exactly that way.
```

### F — Dormant package wiring

#### F-1: feat: thread `ctxmgr.Compact` into `phase.RenderPrompt` at a configured token budget

```
Title: feat: wire cli/internal/ctxmgr into phase.RenderPrompt for context compaction
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  `cli/internal/ctxmgr/ctxmgr.go:17-50` defines `Strategy`, `Segment`,
  `Window` types and a `Compact` function. Zero import sites from
  production code — the package is dormant. SoTA spec §4.2 lists
  context compilation as MUST, and harness-engineering §2 names
  compaction as an `[Established]` best practice.

  Prompt assembly today is flat Go template concatenation at
  `cli/internal/phase/phase.go:110` (`RenderPrompt`) — no retrieval
  pass, no compaction pass, no window tracking. On long-running
  vessels this means HARNESS.md and every phase output get
  re-concatenated every phase without any reduction.

  ## Current behavior

  ```go
  // cli/internal/phase/phase.go:110 (approximate)
  func RenderPrompt(template string, data TemplateData) (string, error) {
      // text/template.Execute on `data`, return the result
  }
  ```

  No token counting, no compaction.

  ## Proposed change

  1. Add a `cfg.Phase.ContextBudget` field at
     `cli/internal/config/config.go` (default: 100_000 tokens).
  2. Extend `phase.RenderPrompt` to: after template execution,
     count approximate tokens (via a simple character-based
     heuristic — 4 chars ≈ 1 token — no tokenizer dependency).
  3. If the count exceeds `ContextBudget`, call
     `ctxmgr.Compact(rendered, ctxmgr.Strategy{Target: budget})`
     and use the result.
  4. The compaction strategy: drop the oldest-written previous-phase
     outputs first, then prose-summarise mid-size sections, keep
     the HARNESS.md preamble untouched.
  5. Unit tests at `cli/internal/phase/phase_test.go`:
     - Small prompt → unchanged.
     - Oversize prompt → compacted to ≤ budget.
     - HARNESS.md preamble preserved verbatim.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/phase/...` green.
  - [ ] `go test ./cli/internal/ctxmgr/...` still green — no
        breaking ctxmgr API changes.
  - [ ] A smoke vessel where the prompt would exceed 100k tokens
        runs without error and writes a compacted prompt to
        `.xylem/state/phases/<id>/<phase>.prompt`.

  ## Dependencies
  - Blocked by: Prong 1 Phases 1-3 (no baseline reason, just
    scheduling).
  - Related to: #60 (rescoped), #F-2 (memory integration).

  ## Notes / gotchas
  - 4-chars-per-token is the standard heuristic for English;
    accept it. Do not add tokenizers.
  - Be careful that the compaction output is still valid for the
    downstream template consumers — if a template expects
    `{{.PreviousOutputs.analyze}}` to be present verbatim, a
    summarising compaction may break it. Either: (a) only compact
    AFTER template rendering (simpler, shippable), or (b) compact
    individual segments before rendering (more correct but bigger
    change). Prefer (a).
  - Tracking issue #60 has the `xylem-failed` label — strip it
    before enqueueing this replacement issue.
```

#### F-2: feat: wire memory package for episodic memory (cheapest of the three types)

```
Title: feat: wire cli/internal/memory for episodic memory persistence from phase outputs
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  `cli/internal/memory/memory.go:22-32` defines the `MemoryType`
  enum (Procedural / Semantic / Episodic) and memory primitives.
  Zero import sites from production. SoTA spec §4.4 requires all
  three types, harness-engineering §4 describes episodic as the
  cheapest-win because it maps directly onto per-vessel phase
  outputs.

  Today the closest thing to episodic memory is
  `evidence-manifest.json` and `failure-review.json` under
  `.xylem/state/phases/<id>/`. These are per-phase artifacts —
  they exist but are not indexed, searchable, or consumable
  across vessels.

  ## Current behavior

  No memory.* import sites in cli/cmd/xylem/ or cli/internal/runner/
  or cli/internal/scanner/.

  ## Proposed change

  1. Extend `cli/internal/memory/memory.go` with a simple
     `EpisodicStore` that writes an entry per phase completion
     to `<state_dir>/state/memory/episodic.jsonl` with fields:
     `vessel_id, phase_name, recorded_at, outcome (success|fail),
     summary, citations[]`.
  2. At phase completion in the runner (`cli/internal/runner/runner.go`
     — find the `phase completed` log point), call
     `memory.EpisodicStore.Append(entry)` with the summary.json
     contents and citation list.
  3. Build `memory.EpisodicStore.RecentForVessel(vesselID, n)` that
     returns the last `n` episodic entries for a vessel — the
     prompt builder can use this to inject prior phase outcomes
     into the next phase's system prompt.
  4. Unit tests at `cli/internal/memory/memory_test.go`:
     append, read, read-with-vessel-filter, read-with-limit,
     concurrent-append safety.
  5. Integration test: run a mock two-phase workflow and assert
     the second phase's prompt contains a reference to the first
     phase's episodic entry.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/memory/...` green.
  - [ ] `.xylem/state/memory/episodic.jsonl` exists after one clean
        vessel run and has at least one entry per phase.
  - [ ] The second phase of a two-phase workflow includes prior
        episodic data in its prompt (assert via a test hook or
        direct inspection of the rendered prompt file).

  ## Dependencies
  - Blocked by: none.
  - Related to: #F-1 (context compaction may need to drop the
    oldest episodic entries first), #60 (rescoped).

  ## Notes / gotchas
  - Procedural (HARNESS.md + workflow YAML) and semantic (lessons)
    memory are already wired today. Episodic is the missing one.
    Skip semantic/procedural in this issue — do not conflate.
  - Watch the write path: episodic.jsonl must survive daemon
    restarts. Use append-only with file locking, same pattern as
    `cli/internal/intermediary/intermediary.go`'s AuditLog.
```

#### F-3: feat: wire `catalog` package for phase-level tool permission scopes

```
Title: feat: wire cli/internal/catalog for phase-level tool permission scopes
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  `cli/internal/catalog/catalog.go:11-49` defines `PermissionScope`,
  `Tool`, `ReadOnly`, `WriteWithApproval`, `FullAutonomy` types.
  Zero import sites in production. SoTA §4.3 requires a clear tool
  contract with scoped permissions.

  Today the only tool gate is `Workflow.AllowedTools` (an optional
  string list on the workflow struct). No scoping, no
  read-vs-write classification, no approval plumbing.

  ## Current behavior

  - `cli/internal/workflow/workflow.go` has `Workflow.AllowedTools []string`.
  - The runner passes this through to `claude -p` as the allowed
    tools list.
  - No integration with the catalog package.

  ## Proposed change

  1. At phase launch time (`cli/internal/runner/runner.go`, the
     `launchPhase` or equivalent), construct a `catalog.PermissionScope`
     from the workflow class:
     - `policy.Delivery` → `ReadOnly + WriteWithApproval` (write
       requires intermediary.RequireApproval effect).
     - `policy.Ops` → `FullAutonomy` (ops workflows merge PRs).
     - `policy.Housekeeping` → `ReadOnly + WriteWithApproval`.
     - `policy.Diagnostic` → `ReadOnly`.
  2. Pass the scope into the `claude -p` invocation via its env
     vars or as a prompt-header annotation. The exact mechanism
     depends on whether Claude Code CLI supports per-call
     permission scoping via env — check the current exec.go.
  3. Add a config override at `harness.policy.tool_overrides` so
     an operator can widen or narrow per-workflow.
  4. Unit tests: each class maps to the expected scope; config
     override takes precedence.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/catalog/...` green (new or existing).
  - [ ] `go test ./cli/internal/runner/...` green with new scope
        assertions.
  - [ ] A delivery-class phase launched under the new code carries
        the `ReadOnly + WriteWithApproval` scope in its launched
        `claude -p` invocation.

  ## Dependencies
  - Blocked by: Prong 1 Phase 2 (PR #366 must land so the class
    matrix is the authoritative source of class decisions).
  - Related to: F-1, F-2, E-1.

  ## Notes / gotchas
  - Claude Code's CLI `--allowed-tools` flag is the closest
    primitive today. If it doesn't expose write-with-approval,
    this issue may need to fall back to policy enforcement at
    the intermediary level rather than the claude CLI level.
  - Read the current `exec.go` carefully for the exact invocation
    pattern before changing it.
```

### G — Adapt-repo / productize finishing

#### G-1: feat: emit `AGENTS.md` stub from `xylem init` with pointer to HARNESS.md + docs/

```
Title: feat: xylem init emits AGENTS.md stub with progressive-disclosure pointer
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Productize spec §4.4 lists `AGENTS.md` stub as a §4.4 gap:
  `xylem init` scaffolds `HARNESS.md`, workflows, and prompts but
  does not emit an `AGENTS.md` root file. SoTA spec §4.1 MUST NOT
  have a monolithic `AGENTS.md`; it MUST have a short root
  instruction file that points at progressive-disclosure structured
  docs.

  The xylem repo itself also lacks `AGENTS.md` at root — see
  `ls` on the repo root. `.xylem/HARNESS.md` covers roughly that
  role today but is nested and not the convention external
  harnesses expect.

  ## Current behavior

  - `cli/cmd/xylem/init.go:82-141` (`cmdInitWithProfileAndOptions`)
    composes profiles and calls `syncProfileAssets` at line 144.
  - `syncProfileAssets` at `cli/cmd/xylem/init.go:214` walks the
    composed profile's `ComposedProfile.Prompts` and
    `ComposedProfile.Workflows` and writes them to disk.
  - No profile ships an `AGENTS.md.tmpl` —
    `find cli/internal/profiles -name 'AGENTS*'` returns empty.

  ## Proposed change

  1. Add `cli/internal/profiles/core/AGENTS.md.tmpl` containing:
     ```
     # Agents guide

     This repository uses the xylem agent harness. For agents
     (Claude Code, Copilot, etc.) working in this repo, the
     authoritative orientation is:

     1. **Short root instruction file**: this file.
     2. **Harness conventions**: `.xylem/HARNESS.md` — how the
        harness drives phases, where outputs go, what gates run.
     3. **Design docs**: `docs/design/` — read the one most
        relevant to your current task.
     4. **Decision log**: `docs/decisions/` — past architectural
        choices.

     ## Build and test

     See `.xylem/HARNESS.md` for the authoritative commands.
     Short version:

     ```bash
     {{.Validation.Build}}
     {{.Validation.Test}}
     ```

     ## Contributing

     Open issues are labelled `ready-for-work`. Vessels pick
     them up via the xylem daemon. Humans pick them up via
     `gh issue list --label ready-for-work`.
     ```
  2. Update `cli/internal/profiles/profiles.go` `Compose()` to
     include the new template in `ComposedProfile.RootFiles` (or
     add a similar field — check the current struct).
  3. Update `syncProfileAssets` at
     `cli/cmd/xylem/init.go:214` to render `AGENTS.md.tmpl` to
     `<repo_root>/AGENTS.md` — NOT to `<state_dir>/AGENTS.md`.
     The file belongs at the repo root for the convention to work.
  4. Use `TemplateData` from
     `cli/internal/phase/phase.go:18-71` to interpolate
     `{{.Validation.Build}}` / `.Test`.
  5. Update `cli/cmd/xylem/init_test.go` to assert `AGENTS.md`
     exists at the expected path after `xylem init --profile=core`.

  ## Acceptance criteria
  - [ ] `go test ./cli/cmd/xylem/...` green including a new
        `TestInitEmitsAgentsMd` test.
  - [ ] `xylem init --profile=core --state-dir $TMPDIR/repo`
        creates `$TMPDIR/repo/AGENTS.md` with the rendered build
        commands.
  - [ ] The file is about 30-50 lines, not hundreds — enforce
        via a line-count assertion in the test.

  ## Dependencies
  - Blocked by: none.
  - Related to: #241 (productize tracking parent).

  ## Notes / gotchas
  - Do NOT render `AGENTS.md` under `.xylem/` — it must be at
    repo root for tool conventions to find it.
  - The `--force` flag on init overwrites existing files. For
    `AGENTS.md` specifically, be extra conservative: if the file
    exists and its first line is not `# Agents guide`, DO NOT
    overwrite. Print a warning and skip.
  - Add the same file to the xylem repo root as a separate
    follow-up issue (H-2 in the housekeeping umbrella) — do not
    commit it to xylem in this issue since the init behavior
    needs to land first.
```

#### G-2: feat: define `adapt-plan.json` schema + Go struct + validator

```
Title: feat: define adapt-plan.json schema (schema_version=1) with validator and Go struct
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Productize spec §8.4 requires an `adapt-plan.json` schema with
  `schema_version=1` and seven specific fields: `repo_slug`,
  `language_stack`, `validation_commands`, `workflows_to_enable`,
  `overlay_profiles`, `risk_assessment`, `rollout_steps`.

  The `adapt-repo` workflow (`cli/internal/profiles/core/workflows/adapt-repo.yaml`)
  has a `plan` phase (phase 3 of 7) whose output is supposed to be
  this adapt plan. Today the output is unstructured prose — there
  is no Go struct, no JSON schema, no validator.

  Consequences:
  - Phase 4 (`validate`) has nothing to statically validate.
  - Phase 5 (`apply`) parses freeform prose which is brittle.
  - External repos cannot inspect the plan before it runs.

  ## Current behavior

  - `cli/internal/profiles/core/workflows/adapt-repo.yaml:1-33`
    has the 7 phases but no schema reference.
  - `cli/internal/bootstrap/` has `analyze-repo` and
    `audit-legibility` subcommands but no adapt-plan write path.
  - `grep -r adapt_plan cli/internal/` returns no matches.

  ## Proposed change

  1. Create `cli/internal/bootstrap/adapt_plan.go` with:
     ```go
     type AdaptPlan struct {
         SchemaVersion      int               `json:"schema_version"`
         RepoSlug           string            `json:"repo_slug"`
         LanguageStack      []string          `json:"language_stack"`
         ValidationCommands map[string]string `json:"validation_commands"`
         WorkflowsToEnable  []string          `json:"workflows_to_enable"`
         OverlayProfiles    []string          `json:"overlay_profiles"`
         RiskAssessment     RiskAssessment    `json:"risk_assessment"`
         RolloutSteps       []RolloutStep     `json:"rollout_steps"`
     }
     ```
     with sub-types for `RiskAssessment` and `RolloutStep`.
  2. Add `func (p *AdaptPlan) Validate() error` checking:
     `SchemaVersion == 1`, `RepoSlug` matches `^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`,
     `LanguageStack` non-empty, each `ValidationCommands` key in
     `{format, lint, build, test}`, `WorkflowsToEnable` non-empty
     and each name matches a known workflow in the composed
     profile.
  3. Add `func ReadAdaptPlan(path string) (*AdaptPlan, error)` and
     `func WriteAdaptPlan(path string, plan *AdaptPlan) error`.
  4. Add the schema file at
     `cli/internal/profiles/core/schemas/adapt-plan.schema.json`
     (JSON Schema draft-07) and embed it with `//go:embed`.
  5. Update the `adapt-repo.yaml` phase 3 prompt to explicitly
     require output in this JSON shape (instruct the model to
     emit a single fenced json block).
  6. Phase 4 (validate) now calls `bootstrap.ValidateAdaptPlan(path)`
     via a command gate.
  7. Unit tests at
     `cli/internal/bootstrap/adapt_plan_test.go` for validate +
     read + write + round-trip.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/bootstrap/...` green.
  - [ ] `bootstrap.ReadAdaptPlan` on a fixture file returns the
        expected struct.
  - [ ] Fixture with `schema_version: 2` errors with clear text.
  - [ ] Fixture with missing `repo_slug` errors with clear text.
  - [ ] `adapt-repo` phase 4 gate now references the validator.

  ## Dependencies
  - Blocked by: none.
  - Related to: #241, #G-1.

  ## Notes / gotchas
  - Keep `schema_version=1` frozen until the first external repo
    lands. Do NOT pre-emptively add v2 migration code.
  - The JSON Schema file is informational — the Go struct +
    validator is authoritative. Don't wire a JSON schema library.
```

### H — Housekeeping umbrellas

#### H-1: feat: make phase-artifact writes (summary/cost/evidence) unconditional + type trust boundaries

```
Title: feat: unconditional phase-artifact writes for summary + cost + evidence; type trust boundaries per gate type
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Phase-artifact coverage in the live daemon:
  - `summary.json`: 29 / 62 dirs (47%)
  - `failure-review.json`: 18 / 62 dirs (29%)
  - `evidence-manifest.json`: 9 / 62 dirs (15%)
  - `cost-report.json`: 29 / 62 dirs (47%)

  Sampling shows some phases write the artifacts and some do not.
  The inconsistency creates a blindspot for §4.8 "observability"
  claims — any downstream tool that aggregates over
  `.xylem/state/phases/` gets non-uniform data.

  Additionally: every evidence-manifest claim has
  `"trust_boundary": "No trust boundary declared"`. Issue E-2
  fixes the `level` field; this issue fixes the trust-boundary
  field at the same code location.

  ## Current behavior

  - `cli/internal/runner/runner.go:1667-1686` is where phase
    artifacts are written (approximate). The current code has
    branches that skip writes for certain phase types (e.g.
    non-LLM phases may skip `cost-report.json`).
  - Trust boundary is literal string `"No trust boundary declared"`
    at `runner.go:2961-2969` for all default-case claims.

  ## Proposed change

  1. In `runner.go` (or the evaluator_loop.go after PR #372),
     remove the phase-type conditional gating on summary / cost /
     evidence writes. EVERY phase writes summary.json (even if
     most fields are zero), cost-report.json (even with zero
     usage), and evidence-manifest.json (even if empty claims).
  2. In `buildGateClaim` default branch: set
     `TrustBoundary = "Command gate output"` (paired with issue E-2).
  3. Unit test: a run of `fix-bug` through all 4 phases produces
     4 summary.json, 4 cost-report.json, and 4 evidence-manifest.json
     files.

  ## Acceptance criteria
  - [ ] `go test ./cli/internal/runner/...` green.
  - [ ] A post-merge vessel run produces coverage ≥ 95% on all
        three artifact types (allowing for the occasional
        legitimate skip on scheduled non-LLM phases).
  - [ ] Grep over `.daemon-root/.xylem/state/phases/*/evidence-manifest.json`
        finds no claims with `trust_boundary` = `"No trust
        boundary declared"` for command gates.

  ## Dependencies
  - Blocked by: Prong 1 Phase 3 (PR #372) to avoid a rebase
    conflict on the runner refactor.
  - Related to: #E-2.

  ## Notes / gotchas
  - Keep legitimate skips: if a phase has no gate at all, skip
    evidence. But emit an empty file rather than no file — easier
    for downstream tools.
  - `summary.json` is the vessel's external-world report; it must
    be present even for failing phases.
```

#### H-2: chore: xylem-repo housekeeping (AGENTS.md at root, DefaultProtectedSurfaces rationale refresh, pr_opened restart-survival test, adapt-repo closed-issue dedup test)

```
Title: chore: xylem repo housekeeping — AGENTS.md at root + rationale + restart-survival test + dedup test
Labels: ready-for-work, enhancement, harness-impl
Body:
  ## Context

  Five small P2 items consolidated because each is too small to
  justify its own issue:

  A. **AGENTS.md at xylem repo root.** `ls` on the xylem repo
     root shows no `AGENTS.md`. The convention is for tools like
     Claude Code and Cursor to read `AGENTS.md` as the root
     orientation file. Today `.xylem/HARNESS.md` is the closest
     thing but it's nested. Add a short 30-line `AGENTS.md` at
     root pointing at `.xylem/HARNESS.md` + `docs/design/` +
     `docs/decisions/` + `CLAUDE.md`.

  B. **`DefaultProtectedSurfaces` rationale refresh.** The
     comment at `cli/internal/config/config.go:20-54` explains
     why `DefaultProtectedSurfaces = []string{}` (rationale:
     "human PR review is the security gate, see issue #194"). That
     issue is closed but the comment is aging — refresh it to
     point at the productize §9 class matrix as the authoritative
     enforcement layer now that PR #366 has landed.

  C. **`pr_opened` dedup restart-survival test.** `cli/internal/source/github_pr_events.go:261`
     dedups by `(pr, head_sha)`. Add a test that restarts the
     daemon mid-dedup and asserts the second (post-restart)
     `pr_opened` event is STILL deduped. Persistent state
     required via `.xylem/state/pr-events/debounce.json`.

  D. **`ensureAdaptRepoSeeded` closed-issue dedup test.**
     `cli/cmd/xylem/adapt_repo_seed.go` dedupes by title but it's
     unclear whether closed issues with the same title are
     treated as "already seeded". Spec §8.2 expects dedup on both
     open and closed. Add a test covering the closed case.

  E. **Reference G-1 AGENTS.md template in HARNESS.md.** Once
     G-1 lands, `.xylem/HARNESS.md` should reference the root
     `AGENTS.md` as the entry point. One-line edit to HARNESS.md.

  ## Current behavior

  A–D: see citations above. E: HARNESS.md is the entry today.

  ## Proposed change

  Five independent commits on the same branch, one per item.
  Each commit stands alone and could be reviewed individually.
  Bundled for scheduling efficiency.

  ## Acceptance criteria
  - [ ] `ls AGENTS.md` at repo root returns the new file.
  - [ ] `go test ./cli/internal/config -run TestDefaultProtectedSurfacesComment` passes a lint-style assertion that the comment contains "#366" or "class matrix".
  - [ ] `go test ./cli/internal/source -run TestPROpenedDedupSurvivesRestart` green.
  - [ ] `go test ./cli/cmd/xylem -run TestEnsureAdaptRepoSeededDedupesClosedIssues` green.
  - [ ] `.xylem/HARNESS.md` references `AGENTS.md`.

  ## Dependencies
  - Blocked by: #G-1 for sub-item A's AGENTS.md template
    alignment.
  - Related to: #241.

  ## Notes / gotchas
  - Do NOT commit the AGENTS.md content as duplicated prose —
    source it from the G-1 template or from HARNESS.md's existing
    preamble.
  - The restart-survival test needs a temp state dir and two
    daemon processes — use the existing test helper for that
    (see `daemon_test.go`).
```

### I — Reload drift root cause

#### I-1: feat: `daemon.selfUpgrade` (or `daemonStartup`) re-syncs profile assets when the embedded FS digest differs from the on-disk runtime workflows

```
Title: feat: daemon re-syncs profile assets from embedded FS when digest drifts (root cause of stale runtime workflows)
Labels: ready-for-work, enhancement, bug, harness-impl
Body:
  ## Context

  Prong 1 Phase 1 unblocked a reload rejection caused by a
  hyphenated phase name that lived in BOTH the profile source
  (`cli/internal/profiles/self-hosting-xylem/workflows/diagnose-failures.yaml:7`)
  AND the runtime copy (`.daemon-root/.xylem/workflows/diagnose-failures.yaml`).
  The profile-source fix alone did NOT unblock the reload — the
  runtime copy had to be edited in parallel because
  `syncProfileAssets` is called only from `xylem init`, never
  from `daemonStartup` or `selfUpgrade`.

  Current flow:
  1. `xylem init` writes profile assets to `<state_dir>/.xylem/workflows/*.yaml`.
  2. Daemon runs with those runtime copies.
  3. `selfUpgrade` (`cli/cmd/xylem/upgrade.go:selfUpgrade`) runs
     `git pull && go build && exec()` — the binary is replaced
     but the runtime workflow copies are NOT re-synced.
  4. Result: profile source updates (via PR merges) never
     propagate to the runtime workflows until `xylem init` is
     manually re-run.

  This is the root cause of stale runtime workflows and of the
  workflow-class-matrix-test-critic-second-hit problem found
  during Prong 1 verification. It has been observed before
  (see memory loop 17: "drift bug").

  ## Current behavior

  `cli/cmd/xylem/daemon.go:207-214` (`daemonStartup`) runs:
  ```go
  func daemonStartup(ctx context.Context, cfg *config.Config, q *queue.Queue, wt *worktree.Manager, seedRunner adaptRepoSeedRunner) error {
      reconcileStaleVessels(q, wt)
      if _, err := ensureAdaptRepoSeeded(ctx, cfg, seedRunner, adaptRepoSeededByDaemon); err != nil {
          slog.Warn("seed adapt-repo issue failed, continuing", "error", err)
      }
      return nil
  }
  ```
  No profile re-sync step.

  `cli/cmd/xylem/upgrade.go:selfUpgrade` also has no re-sync.

  `cli/cmd/xylem/init.go:144` calls
  `syncProfileAssets(defaultStateDir, composed, force)` — this is
  the ONLY caller of `syncProfileAssets` in production code.

  ## Proposed change

  1. Extract the digest computation from `syncProfileAssets` into
     a separate helper `profiles.ComputeRuntimeDigest(stateDir) string`
     that walks the runtime workflows + prompts and returns a SHA
     over the file contents (same digest scheme used by
     `Workflow.Digest`).
  2. Extract the embedded-FS digest computation into
     `profiles.ComputeEmbeddedDigest(composed) string` that does
     the same over the composed profile.
  3. In `daemonStartup`, call
     `profiles.ComputeRuntimeDigest(cfg.StateDir)` and
     `profiles.ComputeEmbeddedDigest(composed)`; if they differ,
     call `syncProfileAssets(cfg.StateDir, composed, true)` (force
     mode) and log an INFO line with both digests.
  4. Add a `--no-profile-sync` flag on daemon startup that
     disables this behavior for operators who want to manually
     manage profile drift. Default: enabled.
  5. Unit test: a tempdir tree with runtime workflows that differ
     from the embedded FS causes a re-sync; a matching tree
     causes no re-sync; the `--no-profile-sync` flag causes no
     re-sync even when they differ.
  6. Property test: for any composed profile, a cold-start daemon
     converges on the embedded digest within one startup cycle.

  ## Acceptance criteria
  - [ ] `go test ./cli/cmd/xylem/... ./cli/internal/profiles/...` green.
  - [ ] Start daemon from a stale runtime dir; observe INFO log
        with the two digests and a subsequent successful reload.
  - [ ] Start daemon from a matching runtime dir; no re-sync log.
  - [ ] `--no-profile-sync` flag suppresses re-sync.
  - [ ] Reload-audit JSONL shows `allow` on the first post-startup
        reload even if the runtime was stale.

  ## Dependencies
  - Blocked by: Prong 1 Phase 1 (reload must be unblocked first so
    this fix can propagate).
  - Related to: memory loop 17-19 observations about the drift
    mechanism; PR #203 (partial earlier fix attempt).

  ## Notes / gotchas
  - Be careful about force=true overwriting operator-hand-edited
    runtime files. The current force semantics in
    `syncProfileAssets` are "overwrite unconditionally". This is
    fine for standard runtime files (workflows, prompts) but NOT
    for `.xylem.yml` — do not force-overwrite the config file.
    Scope the force to the workflows + prompts directory only.
  - Do NOT solve this by re-running `xylem init` from
    `daemonStartup` — init has side-effects (seeds adapt-repo,
    writes profile.lock, etc.) that the daemon already handles.
    Extract only the profile-asset sync step.
  - This fix should land quickly post-Prong-1 so the next
    occurrence of a profile-source edit propagates automatically.
```

### J — Re-scope existing xylem-failed issues

Four existing issues carry `xylem-failed` because a past vessel
attempt stalled. Before re-filing, strip the `xylem-failed` label
so the scanner re-picks them up, and ADD a sub-issue for each with
a refined scope drawing from the gap assessment. **These four items
don't need new issue bodies — they need label strip + a comment
with the refined scope.** Included in the plan for completeness.

| Issue | Action                                                                                                              | Sub-scope comment                                                                         |
| ----- | ------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| #57   | Strip `xylem-failed`. Add a comment pointing at the SoTA §4.7 "comparison across harness variants" requirement and the `cli/internal/continuousimprovement/` + `cli/internal/dtu/testdata/manual-smoke/` code paths. | Focus the eval corpus on: (a) per-phase token count, (b) gate-pass rate, (c) verify-phase green-rate, compared across model tiers. Output a JSONL under `.xylem/eval/` for each run. |
| #58   | Strip `xylem-failed`. Add a comment pointing at the policy class matrix already in code (`cli/internal/policy/class.go`) and ask for the network + secret handling layer on top.                                        | Scope: `intermediary.RequireApproval` effect wired for `ops.admin_merge`, `ops.delete_branch`, `ops.write_secret`. Network egress: deny by default, allow via `policy.network_allowlist`. Secrets: `config.Secrets` map with SOPS or age encryption. |
| #59   | Strip `xylem-failed`. Add a comment pointing at the Jaeger OTLP endpoint at `localhost:4317` and ask for a CLI surface at `xylem obs traces [--vessel X --phase Y]`.                                                     | Scope: a CLI that queries the local Jaeger API (`GET /api/traces?service=xylem`) and returns per-vessel spans. MCP tool registration is out of scope — CLI only. |
| #60   | Strip `xylem-failed`. Link to the new Prong 2 issues #F-1 + #F-2 that replace its scope.                                                                                                                                | Close #60 after #F-1 and #F-2 both land. #60 was a single umbrella; the replacements are two focused issues. |

---

## 4. Deferred / dropped items

| §7 item                                                             | Action      | Reason |
| ------------------------------------------------------------------- | ----------- | ------ |
| P0 #6 — flip xylem's own `.xylem.yml` to `profiles: [core, self-hosting-xylem]` | **Deferred** | Blocked on PR #366 being merged AND having run in `warn` mode for 24h. Doing this self-modification without the class-matrix backstop is exactly the risk the class-matrix exists to prevent. Revisit after Prong 1 + 24h observation window. Tracked under #240. |
| P1 #15 — `xylem adapt-repo` CLI wrapper with `--dry-run`            | **Deferred** | Nice-to-have. Adapt-repo works today via daemon enqueue; the CLI wrapper is a DX improvement, not a productization blocker. Revisit after G-2 (adapt-plan schema) lands. |
| P1 #23 — sprint contracts / mission decomposition                   | **Deferred** | `[Emerging]` per harness-eng §12. Opus 4.6 explicitly removed sprint contracts from its recommendations. The `mission` package is dormant and will likely stay dormant for model-capability reasons. Not filing. |
| P1 #25 — `signal` package heuristics                                | **Deferred** | `signal` watches for repetition/tool-failure/context-thrash. Useful for future model regressions but premature today with Opus 4.6. Not filing. |
| P2 #29 — `cost.reset_timezone`                                      | **Deferred** | MAY per spec §12.1. Default UTC is correct for now. Wire when a non-UTC operator asks. |
| P2 #30 — per-workflow cost alerts (2× weekly MA for 3 days)         | **Deferred** | MAY per spec §12.5. Needs a historical aggregator that does not yet exist; requires #D-2 + #D-3 + a moving-average store. Not a productize blocker. |
| P2 #31 — `xylem profile lock` command                               | **Deferred** | MAY per spec §A.5. `profile.lock` is already written by init; an explicit lock command is nice-to-have. |
| P2 #43 — `profile.lock` force-path audit                            | **Deferred** | Small audit, not urgent. Fold into H-2 if capacity. |
| P2 #49 — AGENTS.md instruction hierarchy (org→repo→dir) test        | **Deferred** | MAY. Single-repo xylem has no org/dir hierarchy to test. Revisit when a second repo adopts xylem. |

---

## 5. Sequencing summary — a one-paragraph read

**Prong 1, in order**: Phase 1 (rename `create-issues` + `test-critic`
across profile source + two runtime copies, plus template refs; pause
daemon briefly); Phase 2 (rebase + land PR #366, making sure
`HarnessPolicyMode` defaults to `warn`); Phase 3 (rebase + land PR #372,
optionally folding in the one-line evidence-level fix); Phase 4
(observation-only verification of the three audit log / evidence
manifest acceptance signals).

**Prong 2, parallelisable once Prong 1 completes**:
`{A-1, A-2, A-3, B-1, C-1, D-1, E-1, G-1, G-2, H-1, H-2, I-1}` can all
be filed at once; `{D-2, D-3, E-2, F-1, F-2, F-3, J-*}` have blockers
named in each issue body. The scanner will pick up whatever lands in the
`ready-for-work` label within one scan cycle.

**Deferred, not filed**: §7 P0 #6 (pending maintainer sign-off after PR
#366 has run in `warn` for 24h); §7 P1 #15, #23, #25; §7 P2 #29, #30,
#31, #43, #49.

**Open maintainer question carried forward from §8 of the assessment**:
are `cli/internal/cadence` and `cli/internal/skills` fresh
work-in-progress or accidentally-created dead packages? (§7 P2 #40.)
Resolving this is cheap — `git log --oneline cli/internal/cadence/` and
`cli/internal/skills/` will say — but the answer determines whether
either should be wired into Prong 2 at all. Flagged here rather than
filed.

---

**End of plan.** No files were modified; no issues were filed.
