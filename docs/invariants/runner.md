# Invariants: `cli/internal/runner`

Status: **draft v2 (2026-04-16)**. Ratified by: pending human sign-off.

This document is the load-bearing specification for the `runner` package. It
is protected: changes require human review (see **Governance**). Agent-authored
PRs that relax an invariant without an accompanying
`.claude/rules/protected-surfaces.md` amendment must be rejected.

It composes with [`docs/invariants/queue.md`](queue.md): the queue guarantees
at-most-one-active-per-ref, terminal immutability, and state-transition
soundness; this doc assumes those hold and states what callers gain *on top of
them* when the runner drains the queue.

---

## Contract

The `runner` package is xylem's **drain executor**. A `Runner` observes a
`Queue` and advances `pending` vessels through workflow phases by: reserving
global + per-class concurrency slots, creating an isolated git worktree,
piping rendered prompts to an LLM subprocess, persisting phase outputs to
`.xylem/phases/<id>/`, evaluating gates (and retrying bounded times), and
transitioning the vessel to a terminal state (or `waiting` for label gates).

Callers rely on the runner to **not double-dispatch**, **not exceed configured
concurrency caps**, **not leave orphaned worktrees or LLM subprocesses after
vessel completion**, **not race its own prune with live drain**, **not mutate
terminal vessels**, **not lose phase progress across graceful daemon
restarts**, **not run a phase past a bounded wall-clock ceiling**, **not
re-invoke a source lifecycle hook twice for the same event**, and **not
publish a duplicate discussion comment for the same phase event**. Everything
outside those guarantees (liveness, clock-source trust, external service
correctness) is out of scope.

---

## Invariants

**I1. Concurrency caps are never exceeded.**
At every observable instant during `Drain` / `DrainAndWait`:
`|{v ∈ Queue : v.State = running ∧ runner holds sem slot}| ≤ cfg.Concurrency`,
and for every class `c` with `cfg.ConcurrencyLimit(c) = n > 0`,
`|{v running : v.ConcurrencyClass() = c}| ≤ n`. Reservation is atomic with
`Dequeue`: a vessel is dispatched only if BOTH the global `sem` slot and the
class slot are acquired before the goroutine launches.
- *Why:* without this, long-running vessels crowd out all other work (#171
  "drain concurrency collapses to 1"), and per-class caps on heavy workflows
  (#266) are advisory theatre. Bug class: cap skew on retry paths or between
  `classSlotAvailable` check and `reserveClassSlot` reservation.
- *Test:* rapid sequence of Enqueue ops across classes; drive `Drain` with a
  gate that blocks until released; assert `InFlightCount ≤ Concurrency` and
  per-class counts ≤ limits throughout; release; join.

**I2. No concurrent phase execution for a single vessel ID.**
For every `vessel.ID`, at most one `runVessel` goroutine is active at any
instant, and at most one `trackedProcess` exists in `r.processes[vessel.ID]`.
In particular: if `Drain` is called concurrently (daemon tick + operator
`drain`), the same `vessel.ID` is dispatched by at most one caller.
- *Why:* queue I1 (one active vessel per `Ref`) does NOT cover ID-level
  concurrency — two ticks dequeuing via separate `DequeueMatching` calls
  without the queue's own ID-duplicate guard (see queue I9 gap) could launch
  two goroutines for the same ID. Bug class: flaky
  `TestProp_InFlightAccountingMatchesLaunchedWork` (#182) was the first tremor
  of this; double-execution of phase subprocesses would corrupt
  `.xylem/phases/<id>/<phase>.output` without a file-lock.
- *Test:* concurrent `Drain` calls on a shared queue with N pending vessels;
  after `Wait`, assert `ResultCount(Launched) ≤ NumPending` and
  `|distinct IDs in tracked-process history| = |launched vessels|`.

**I3. Cancellation takes precedence over completion.**
If `vessel.State` observable via `Queue.FindByID` becomes `cancelled` at any
point during `runVessel`, the goroutine's final outcome is `"cancelled"` and
the queue's final state for that vessel is `cancelled` (never `completed` or
`failed`). A completed phase does not override a concurrent cancellation.
- *Why:* cancel is the operator's emergency brake (and the recovery system's
  way to stop a misbehaving workflow, #205). If a race lets `completeVessel`
  win, the user's intent is silently dropped and the worktree may be left in
  a dangerous post-completion state that was supposed to be cleaned up via
  `cancelVessel`.
- *Test:* rapid drive a minimal workflow with a mock LLM that holds before
  returning; issue `Queue.Cancel(id)` at randomized offsets during execution;
  assert final `queue.FindByID(id).State = cancelled` and outcome string is
  `"cancelled"`.

**I4. Worktrees are removed on every terminal outcome.**
For every vessel that enters `running`, there exists a finite time after which
`vessel.WorktreePath` either (a) does not exist on disk, or (b) is reused by
a subsequent retry of the same vessel. Specifically: on
`{completed, failed, cancelled, timed_out}`, `removeWorktree` is called with
the persisted `WorktreePath`; on `waiting`, the worktree is retained
(retrieval by `vessel.WorktreePath` on resume is the point); on `failed via
ensureWorktree` before persistence, any created worktree is torn down before
the vessel is marked failed.
- *Why:* orphaned worktrees (#22, #518, #531) accumulate to GB-scale and
  cause prune-drain races (#360, #546). The cancel path was particularly
  leaky (#518, fixed by PR#531); every terminal path now needs the
  guarantee.
- *Test:* rapid sequence of Enqueue + mock-source behavior (OnStart/OnFail
  stubs) + cancellation + timeout injection; after `Wait`, assert
  `FindStaleWorktrees()` returns empty for all non-`waiting` vessels.

**I5. Prune never removes the worktree of a non-terminal vessel.**
`PruneStaleWorktrees` and `FindStaleWorktrees` must treat any worktree whose
path matches `vessel.WorktreePath` for a vessel with
`State ∈ {pending, running, waiting}` as active, regardless of whether that
vessel's `claude` subprocess is currently live. Prune must also defer (return
early with `errWorktreeSetupInProgress`) if any running vessel has
`WorktreePath == ""`, because its worktree exists on disk but is not yet
persisted in the queue.
- *Why:* this is the #360/#546 regression class — prune sees a worktree on
  disk, the queue has not yet persisted the path, prune removes it, and the
  phase goroutine crashes on `chdir`. Worse, with the chdir compound bug
  (loop 218), the phase script continued to execute in daemon root instead
  of failing cleanly.
- *Test:* harness that interleaves `Enqueue → Dequeue → Worktree.Create →
  Queue.UpdateVessel(WorktreePath)` with concurrent `PruneStaleWorktrees`
  calls; at every point where a vessel is non-terminal, assert its worktree
  path is absent from the `stale` set returned by `FindStaleWorktrees`.

**I6. Gate retries are finite, and label-kind gates suspend instead of
consuming retry budget.**
For every phase in every workflow, the number of gate evaluations performed
by the runner is bounded by `max(phase.Gate.Retries, 0) + 1`. Once that
bound is reached, the vessel must transition to a terminal state
(`failed`/`timed_out`/`cancelled`) or to `waiting` (label gate). The runner
must not emit a `"completed"` report to the reporter / discussion publisher
between gate attempts (only a `"failed"` or similar outcome for the attempt).
**Sub-clause (label-kind gates):** when a phase's gate is of kind `label`
and the label is not yet present, the vessel transitions to
`queue.StateWaiting` and its `src.OnWait` hook fires; no retry attempt is
consumed by the suspension, and the phase does not re-invoke the LLM
subprocess on resume (the gate is the resumption point, not a re-run of the
phase it gates).
- *Why:* infinite retry chains (#362, #380) burn budget and block auto-
  merge. The "no false-completed comment" property is already tested
  (`TestProp_CommandGateFailuresNeverEmitCompletedComments`); this invariant
  is its structural counterpart — the retry loop itself is bounded. The
  label-kind sub-clause prevents the recurring confusion where operators
  believed a label-wait was burning retries and timing vessels out
  prematurely.
- *Test:* rapid-generated workflow with arbitrary `retries` drawn from
  `[0, 8]`, single phase, gate that always fails; assert gate evaluator
  was invoked exactly `retries + 1` times and the vessel's final state is
  `failed` (or `waiting` for label gates). For label-kind gates:
  pre-seed gate-label absent; assert final state is `waiting`, retry
  counter was NOT decremented, and `src.OnWait` was called exactly once.

**I7. Phase output persistence precedes next-phase dispatch.**
Phase N+1 must not begin until phase N's output has been written to
`.xylem/phases/<vessel.ID>/<phase-N>.output`, and the vessel's
`CurrentPhase` / `PhaseOutputs[phase-N]` have been persisted via
`Queue.UpdateVessel`. A graceful daemon restart that occurs between phase N
completion and phase N+1 dispatch must resume at phase N+1 on rehydration
(via `rebuildPreviousOutputs`), not re-run phase N.
- *Why:* mid-workflow resumption bugs (#6, interacts with queue I3 gap) —
  when output persistence and state-machine advance are not ordered as a
  single committed step, restarts silently either re-run a phase (lost
  idempotency) or skip it (lost output). Both are silent. This is the
  runner's half of the restart-durability story; queue owns the write
  atomicity half.
- *Test:* rapid sequence of phase completions on an injected
  `CommandRunner` that records the order of `WriteFile(phase.output)`
  vs. `Queue.UpdateVessel(CurrentPhase=N+1)`; assert the writeback order
  is `phase-N output → UpdateVessel → phase-N+1 prompt` for every
  transition. Separately: after each state checkpoint, re-hydrate the
  runner from disk and assert `rebuildPreviousOutputs` returns a map with
  one entry per already-completed phase.
- *Status:* **aspirational.** `runSinglePhase`'s output persistence and
  queue `UpdateVessel` (runner.go:742) are not executed as a single
  atomic unit — a crash between them leaves CurrentPhase unchanged on
  next restart, which re-runs the already-completed phase. Mark skip
  until this ordering is hardened.

**I8. In-flight accounting is exact.**
At every instant, `InFlightCount()` equals the number of `runVessel`
goroutines that have incremented `inFlight` and not yet decremented. On
`Wait`-return, `InFlightCount() = 0`. The accounting does not double-count a
cancellation, double-release a class slot, or fail to release on any
`panic` / early return from the goroutine's deferred unwind.
- *Why:* the drain-loop's decision to break `drainLoop` ("concurrency
  saturated, ending tick") depends on `InFlightCount` being accurate; a leak
  would permanently hold a phantom slot, starving the queue (#171). The
  existing `TestProp_InFlightAccountingMatchesLaunchedWork` covers the
  equality against `Launched` but not the "Wait-return = 0" invariant under
  panic or cancellation paths.
- *Test:* rapid-generated mixed vessel outcomes including injected
  `CommandRunner` panics and early cancellations; after `Wait`, assert
  `InFlightCount() == 0` and `len(r.processes) == 0`.

**I9. The LLM subprocess is killed on every terminal outcome before its
entry is dropped from the tracked-process map.**
For every `vessel.ID` registered in `r.processes` via `markProcessStarted`
(runner.go:4002) on the path to a terminal outcome
(`{completed, failed, cancelled, timed_out}`), the runner MUST invoke
`stopProcess` (runner.go:4049) — i.e. send the configured signal and wait
(or abandon after the bounded wait window) — before `clearTrackedProcess`
removes the map entry at the deferred unwind (runner.go:588). In particular,
`cancelVessel`, `failVessel`, and `completeVessel` must each go through a
kill path on the tracked PID for the vessel they are terminating. "At most
once" is acceptable: if `stopProcess` was already invoked for this vessel
earlier in the goroutine (e.g. from `CheckStalledVessels`), subsequent
terminal-path calls are no-ops.
- *Why:* this is the in-process obligation at the heart of #575 (LLM
  subprocess orphans after vessel kill). The cross-process reaping —
  PPID-1 orphans that outlive the daemon's own tree — is out of scope here
  (see "Not covered") and must be handled by the supervisor's SIGKILL
  reaper. But inside the runner, the contract that "every terminated
  vessel's subprocess was actually signalled, not just had its map entry
  deleted" is local, enforceable, and currently only honored by
  `CheckStalledVessels` (runner.go:4437).
- *Test:* rapid-generated mixed-outcome vessels against an injected
  `CommandRunner` that records signal delivery per PID. After `Wait`, for
  every vessel whose final state is terminal, assert the PID recorded in
  `markProcessStarted` received at least one signal before
  `clearTrackedProcess` removed the map entry. Interleave with cancel and
  timeout injection to cover all three terminal paths.

**I10. Vessels left in `running` without a live goroutine are reconciled
by the runner's periodic sweeps within a bounded number of ticks.**
For every vessel `v` with `v.State = running` observed by the runner
but with no corresponding entry in the live `processes` map, within `k`
ticks of `{CheckHungVessels, CheckStalledVessels}` the runner MUST
either (a) re-dispatch `v` (advancing it through `running → terminal`
via a fresh goroutine), or (b) transition `v` to a terminal state with
a recovery reason (`recovery_reason ∈ {worktree_missing,
daemon_restart, abandoned_running}`). The bound `k` is a small constant
(≤ 3 ticks under the current daemon tick cadence); the invariant is
that no vessel remains indefinitely in `running` with no live goroutine
as observed by the runner.
- *Why:* this closes the freeze-restart cascade class (loops 201, 202,
  215, 216). `CheckHungVessels` (runner.go:4302) and `CheckStalledVessels`
  (runner.go:4357) are the in-runner sweep surfaces. Startup-side
  reconciliation (`reconcileStaleVessels` at `cli/cmd/xylem/daemon.go:664`)
  is owned by the daemon command layer (out of scope for this spec;
  cross-ref: pending daemon-invariants doc). This invariant covers only
  what the runner itself guarantees via its tick loop — that the sweeps,
  once running, do not allow an indefinitely-stranded vessel.
- *Test:* construct a queue pre-seeded with N vessels in `running`, each
  with a persisted `WorktreePath` that may or may not exist on disk.
  Construct a fresh `Runner` against it with no corresponding processes
  in the map. Advance the clock to drive `k` tick cycles (with
  `CheckHungVessels` and `CheckStalledVessels` firing under a controlled
  clock). Assert every vessel has either been re-dispatched (new
  `trackedProcess` or subsequent terminal with a fresh goroutine) or
  been transitioned to terminal with a non-empty `recovery_reason` field.
  No vessel remains in `running` with zero goroutines after `k` ticks.

**I11. Every phase invocation is bounded by a finite wall-clock ceiling.**
For every phase the runner dispatches, there exists a finite wall-clock
duration `B` after which either (a) the phase has completed (success,
gate-failure, or gate-pass-with-retry), or (b) the phase's LLM subprocess
has been forcibly terminated via `stopProcess` and the vessel has been
transitioned to a terminal state (`timed_out` or `failed`). `B` is the
composite of the per-vessel timeout imposed by
`context.WithTimeout(watchedCtx, vesselTimeout)` (runner.go:284) and the
phase-stall threshold enforced by `CheckStalledVessels` (runner.go:4357,
driven by `StallMonitor.PhaseStallThreshold` and the PR#543 worker-stall
safety net). The runner does not claim every phase *succeeds*, only that no
phase runs past `B`.
- *Why:* a wall-clock bound IS a safety invariant — it is falsifiable in
  finite time (observe a phase running longer than `B` with a live PID and
  no terminal transition) and prevents the runaway-phase class (loop 219
  freeze, #542/#543 worker-stall, #544/#545 gh-timeout, #547). Distinct
  from liveness: we are NOT claiming the phase makes progress, only that
  it cannot run unboundedly.
- *Test:* rapid-generated workflow with a single phase driven by an
  injected `CommandRunner` that never returns (simulates a hung
  subprocess). Drive the clock forward past `B`. Assert: the phase's
  `stopProcess` was called, the vessel's final state is one of
  `{timed_out, failed}`, and the vessel's `WorktreePath` was removed
  (composition with I4). Parametrize over the two distinct paths:
  per-vessel `context.WithTimeout` expiry and `CheckStalledVessels`
  detection.

**I12. Stale-cancel satisfies cancellation post-conditions.**
`CancelStalePRVessels` (`stale_cancel.go:41`) must satisfy the same
post-conditions as I3 (the vessel's final state is `cancelled`, not
`completed` or `failed`) and I4 (if the vessel had a persisted
`WorktreePath`, that worktree is removed as part of cancellation).
**Scope note:** the current implementation operates only on vessels in
`StatePending` (line 42 filter) and delegates to `Queue.Cancel(vessel.ID)`
(line 76). Pending vessels have neither a live goroutine nor a persisted
worktree, so I3 / I4 post-conditions are trivially satisfied. I12 is
stated as a **defensive future-proofing invariant**: if stale-cancel's
filter is ever widened to include `StateRunning` or `StateWaiting`, the
cancellation path must still end at `cancelled` + worktree-removed, not
leak artifacts.
- *Why:* the catch-22 class (loops 198, 210 substring-trap) and the
  `stale_cancel` widening surface (#540 `gh` timeout broadening) make
  filter-expansion plausible. Locking the contract now, before code
  changes, prevents the regression from shipping silently.
- *Test:* rapid-generated pool of pending PR-ref vessels, some with
  injected "merged" / "closed" `gh pr view` responses and some with
  "open". After `CancelStalePRVessels`, assert: every vessel whose PR
  responded merged/closed is in state `cancelled`; every "open" vessel
  is still `pending`; no worktree was created (invariant-trivial for
  pending) and no subprocess was tracked. Include a widened-scope test
  variant gated behind `t.Skip` that pre-seeds vessels in
  `StateRunning`; the test asserts I3 + I4 post-conditions hold when /
  if the filter is expanded.

**I13. No duplicate discussion publications per `(vessel_id, phase_id,
event_kind)` triple.**
For every unique triple `(vessel.ID, phase.ID, event_kind)` observed
during a `runVessel` execution, at most one discussion comment or new
discussion is published by `publishPhaseOutput` (`discussion.go:49`).
`event_kind` is a stable categorical tag (`pre_check`, `implement`,
`review`, `self_review_revision`, `analysis`, `summary`, etc.) assigned
by the phase's `output` configuration. Retries of the same gate, repeated
calls to `publishPhaseOutput` via the runner's outer loop, and restarts
from rebuilt state must all be idempotent against this triple.
- *Why:* PR#493's 306-duplicate-comment regression shipped because dedupe
  was keyed on title match alone (`FindExisting(repoID, categoryID,
  titlePrefix)` at `cli/internal/discussion/discussion.go:104`, invoked
  from `discussion.go:97`), and title rendering was phase-template
  dependent — two retries produced subtly different titles, the title
  match missed, `Publish` called `Create` instead of `Comment`. A
  triple-keyed dedupe is the contract we want, whether implemented as
  queue-side idempotency records, publication-attempt journaling, or
  discussion-API idempotency tokens.
- *Test:* rapid-drive a workflow whose phase publishes on completion; run
  the same vessel through multiple gate retries and one simulated daemon
  restart mid-publication. After `Wait`, collect all `FindExisting`/`Create`/
  `Comment` calls recorded by the injected discussion hook. Assert: for
  every distinct `(vessel.ID, phase_id, event_kind)` triple, the
  publication count is ≤ 1.
- *Status:* **aspirational.** The current dedupe key is title-based (see
  `discussion.go:97`); the contract above specifies a triple-based key.
  This property test is expected to fail against current code until
  dedupe is re-keyed — mark `t.Skip` with a ref to the gap-analysis row.

**I14. Source lifecycle hooks fire exactly once per lifecycle event.**
For every vessel `v` and every source lifecycle event `E ∈ {OnStart,
OnFail, OnCancel, OnWait, OnComplete}`, the corresponding hook
`src.E(ctx, v, ...)` is invoked AT MOST ONCE during the vessel's
lifetime, and EXACTLY ONCE if `v` reaches the state implied by `E`
(`OnStart` on `running`, `OnFail` on `failed`, `OnCancel` on `cancelled`,
`OnWait` on `waiting`, `OnComplete` on `completed`). Specifically:
gate-retries of a single phase do NOT re-invoke `OnStart`; restart-rehydration
does NOT double-fire `OnStart` for vessels already past the OnStart
checkpoint; `OnFail` and `OnCancel` are mutually exclusive.
- *Why:* duplicate `OnStart` calls double-post GitHub reaction comments;
  duplicate `OnFail` calls cause the recovery system to loop (#551,
  substring-trap). The `src.OnStart` site at runner.go:594 is the primary
  surface; the many `src.OnFail` call sites scattered through `runVessel`
  terminal paths need to be audited as a set. `src.OnWait` at runner.go:2338
  is the label-gate resumption surface and composes with I6's sub-clause.
- *Test:* rapid-generated mixed-outcome vessels (success, fail via gate,
  cancel mid-phase, timeout, label-wait-then-resume, multi-phase with
  retries on non-first phase). Against an injected source stub that
  records every hook invocation with a timestamp, after `Wait` assert:
  for every vessel and every lifecycle event, the hook invocation count
  is ≤ 1, and if the vessel reached the state implied by the event the
  count is exactly 1. Specifically: `OnStart` count == 1 for every vessel
  that entered `running`; `OnFail` count == 1 for every vessel that
  ended `failed`; `OnCancel` count == 1 for every vessel that ended
  `cancelled`; `OnFail` + `OnCancel` sum ≤ 1 per vessel.

---

## Not covered

Explicitly out of scope for this spec. If you suspect a bug in one of these,
it does not belong in `runner`:

- **Queue-level properties.** At-most-one active per ref, terminal
  immutability, state-transition soundness, unique IDs, retry freshness —
  owned by `queue`. See [`queue.md`](queue.md).
- **Liveness and phase progress.** I11 gives a wall-clock ceiling, not a
  progress guarantee. We do not claim every phase eventually produces useful
  output, advances its gate, or moves the vessel closer to a terminal state
  — only that no phase runs past the bound `B` without being forcibly
  terminated. "Every `pending` vessel eventually reaches a terminal state"
  is a liveness property owned by the daemon scheduler, not the runner.
- **Daemon freeze / heartbeat freshness.** "Every gh-CLI / LLM subprocess
  call completes or times out." (#536, #540). This is primarily enforced by
  static constraints on call sites in `exec` / `automerge` packages; a
  property-based test is an awkward fit. Belongs to **static analysis of gh
  CLI / exec call sites** plus the daemon's supervisor loop. Referenced
  here only because the symptom shows up as stalled vessels.
- **Cross-process LLM orphan cleanup.** (#575) — I9 covers the in-process
  kill obligation (`stopProcess` must be invoked before
  `clearTrackedProcess` for every terminal-path vessel). What remains out
  of scope is the *cross-process* reaping of children that outlive the
  runner's own tree: PPID-1 orphans, process-group leaks when the child
  re-parents itself, zombie accumulation under SIGKILL of the daemon.
  That concern belongs to **process-group handling in `exec`** plus the
  supervisor's SIGKILL reaper. The in-process `trackedProcess` map cannot
  observe a leaked child after the runner's own tree exits.
- **Cost isolation across vessels.** Covered by the existing
  `TestProp_BudgetEnforcementNeverLeaksAcrossVessels`; this spec does not
  duplicate it.
- **Clock-source trust.** `runtimeNow()` ultimately falls back to
  `time.Now().UTC()`. Wall-clock regressions are outside the runner's
  control. Scheduling invariants that imply monotonic time (e.g., "stall
  detector only fires on genuinely stale phases") carry the clock
  obligation through to the caller.
- **Cross-daemon coordination.** Per CLAUDE.md multi-repo section, each
  daemon is CWD-scoped; the runner does not guarantee behavior if two
  runners share a queue file (queue's `flock` is local-host only).
- **External correctness.** GitHub label state, PR mergeability, workflow
  YAML validity after drift — belongs to `source` / `workflow`.

---

## Gap analysis

Reviewed 2026-04-16 against `cli/internal/runner/*.go` as of commit
`f230a45` (queue.md-baseline + PR#545). This is the fix backlog. When the
property tests land, each non-✓ row is an expected test skip or failure
until the corresponding code fix is merged.

| Invariant | Status | Site | Note |
|---|---|---|---|
| I1 | ✓ | `classSlotAvailable` + `reserveClassSlot` (runner.go:368–399) under `scheduleMu` (runner.go:229), global `sem` at runner.go:221 | `Dequeue` + class reservation held under `scheduleMu`; `sem` acquired before dequeue. Race window: `reserveClassSlot` releases `scheduleMu` before class reservation is decremented on failure path, which could be re-checked. |
| I1 | ⚠ | `DrainAndWait` loop (runner.go:422) | Tight-loop dequeues until saturation; property test should drive concurrent `DrainAndWait` and confirm caps hold. |
| I2 | ⚠ | `Drain` / `runVessel` (runner.go:253, 587) | Relies on queue I1 (per-`Ref`) + per-ID `processes` map. If `Drain` is called concurrently by two callers (daemon + operator), the queue's `DequeueMatching` locks protect against dequeuing the same vessel, but the runner's `processes` map has no pre-insert check — a duplicate ID dispatch would silently overwrite the prior tracked PID. Acceptable if the queue guard holds; property test must assert no `processes[id]` collision occurred. |
| I3 | ✓ | `completeVessel`'s Update error-recognition (runner.go:1369–1371) + `cancelledTransition` (runner.go:1231) | Race between `Queue.Cancel` and `completeVessel`'s `Update(..., StateCompleted, "")` is closed: the `cancelled → completed` transition returns `ErrInvalidTransition`, `cancelledTransition` matches it against the current queue state, and execution redirects to `cancelVessel`. Property test pins behavior against regression — if the error-recognition branch is ever widened or dropped, the race reopens silently. |
| I4 | ✗ | `failVessel` (runner.go:1278) — confirmed via grep of all `removeWorktree` / `Worktree.Remove` call sites in the runner package | Known violation: `failVessel` does not call `removeWorktree`. Every `runVessel` returning `"failed"` that successfully created a worktree leaks it to disk until `PruneStaleWorktrees` reaps asynchronously — and prune conservatively defers on any setup-in-progress vessel (I5), so the leak window is unbounded under sustained drain. `completeVessel` (runner.go:1382), `cancelVessel` (runner.go:1274), and `ensureWorktree` rollback paths (runner.go:1035, 1056) all call `removeWorktree`; only the `failed` terminal is gapped. Fix: add deferred `removeWorktree` in `failVessel`, or extract a terminal-path worktree-cleanup helper called from the `persistRunArtifacts` defer (runner.go:615–626). File as companion issue to this spec. |
| I5 | ✓ | `activeWorktreePaths` (worktree_prune.go:81) + `errWorktreeSetupInProgress` (worktree_prune.go:17) | PR#548 closed the #360/#546 race: prune defers if any running vessel has empty `WorktreePath`. Property test should regression-pin this behavior. |
| I6 | ⚠ | gate retry loops in `runSinglePhase` (runner.go:1835) + `executeVerificationGate` (runner.go:2467) | Function too long to audit at a glance; counting sites needs a dedicated read. Property test drives retry-count exhaustively. |
| I6 (label sub-clause) | ✓ | label-kind gate → `vessel.State = queue.StateWaiting` + `src.OnWait` (runner.go:2310–2352, specifically runner.go:2338) | Label-wait does not consume retry budget; resume re-enters at the gate, not at the phase's LLM invocation. Pin against regression. |
| I7 | ✗ | `runVessel` phase-advance sequence (runner.go:737–749) | Phase output is written by `runSinglePhase` internally, then `CurrentPhase = i + 1` and `UpdateVessel(vessel)` are called. These are two separate ops on the queue's JSONL file; a SIGKILL between them leaves `CurrentPhase` unchanged, causing next-restart to re-run the just-completed phase. Fix: combine write-output + UpdateVessel into an atomic queue mutation, or make `rebuildPreviousOutputs` the single source of truth and drop `CurrentPhase` from the state-machine. Requires queue I5b (atomic writes) to land first. |
| I8 | ⚠ | `runVessel` defers (runner.go:256–260) | Deferred `releaseClassSlot` + `<-r.sem` + `inFlight.Add(-1)` are correct under normal and panic unwinds, but `clearTrackedProcess` happens in a separate defer at runner.go:588. If a panic fires between those defers, `processes` could leak while `inFlight` reconciles. Property test should inject panics. |
| I9 | ✗ | `stopProcess` (runner.go:4049) has ONE call site — `CheckStalledVessels` (runner.go:4437). `cancelVessel` (runner.go:1265), `failVessel` (runner.go:1278), `completeVessel` (runner.go:1368) do NOT invoke it. `clearTrackedProcess` (runner.go:588) removes the map entry with no prior kill. | Known violation. `processes[id]` registration at runner.go:4002 is the entry obligation; the terminal-path cleanup is a map-clear with no signal delivery. Today, subprocesses on terminal-path vessels (not stall-detected) are orphaned until the child's own exit. Fix: have `{cancel,fail,complete}Vessel` invoke `stopProcess` on the tracked PID before transitioning, OR extract a terminal-path helper that composes `stopProcess` + state transition + worktree removal (overlap with I4 fix). Composes with #575 cross-process reaper (out of scope). |
| I10 | ✓ (sweeps only) | `CheckHungVessels` (runner.go:4302) + `CheckStalledVessels` (runner.go:4357), both invoked from daemon tick (`cli/cmd/xylem/daemon_reload.go:205–207`). | Scope restricted to runner-owned sweeps. Startup-side `reconcileStaleVessels` (daemon.go:664) is the daemon command layer's obligation and is out of scope here. Property test: pre-seed `running` vessels with no live processes, drive `k` tick cycles, assert reconciliation. |
| I11 | ✓ | `context.WithTimeout(watchedCtx, vesselTimeout)` at runner.go:284 (per-vessel ceiling) + `CheckStalledVessels` at runner.go:4357 + PR#543 worker-stall safety net. | Composite bound. Two distinct enforcement paths; property test should parametrize over both (context-timeout-path and stall-detection-path) to confirm either alone is sufficient to guarantee B. |
| I12 | ⚠ (trivially holds as written; scope mismatch) | `CancelStalePRVessels` at `stale_cancel.go:42` filters to `StatePending` only; line 76 calls `r.Queue.Cancel(vessel.ID)`. Pending vessels have no worktree, no tracked subprocess — I3 and I4 post-conditions are vacuously satisfied. | See **Notes for reviewer**. The invariant is a defensive contract for the filter-widening surface (#540 gh-timeout broadening, future "stale running PR" detection). Decide at sign-off: keep as future-proofing, or fold into I3 / drop and re-add when scope widens. |
| I13 | ✗ | `publishPhaseOutput` (discussion.go:49) → `Publish` (discussion.go:67) → `FindExisting(ctx, target.RepoID, target.CategoryID, titleSearch)` (discussion.go:97) → `Publisher.FindExisting(ctx, repoID, categoryID, titlePrefix string)` (`cli/internal/discussion/discussion.go:104`). | Known spec/code mismatch. Contract: dedupe key is `(vessel_id, phase_id, event_kind)`. Code: dedupe key is title prefix. PR#493's 306-duplicate-comment regression was precisely this mismatch — two retries rendered subtly different titles, title-match missed, `Create` fired instead of `Comment`. Fix: add a triple-keyed idempotency record (queue-side journal entry or discussion-publisher cache keyed on the triple), keep title-match as a second line of defense, not the primary dedupe. Property test `t.Skip` until this lands. |
| I14 | ⚠ | `src.OnStart` at runner.go:594 (single site); `src.OnWait` at runner.go:2338 (single site); `src.OnFail` has many call sites across runner.go terminal paths (grep count ~30+). | Single-site hooks (`OnStart`, `OnWait`, `OnComplete`) look clean by structure; `OnFail` fan-out warrants a directed audit to confirm mutual exclusivity with `OnCancel` and single-fire on rehydration. Property test drives mixed outcomes and asserts per-event invocation count ≤ 1. |

**Summary:** 3 known violations (I4 failed-path worktree leak, I9
terminal-path subprocess-kill gap, I13 dedupe-key mismatch), 1
aspirational (I7 — blocked on queue I5b atomic writes), 4 warnings
requiring directed tests (I2 concurrent-drain, I6 retry-count audit, I8
panic-unwind leak, I14 OnFail fan-out audit), 4 clean + pin-against-
regression (I3, I5, I6 label sub-clause, I11), 1 clean-sweeps-only (I10 —
runner periodic sweeps; startup-reconcile is daemon-layer, out of scope),
1 defensive-future-proofing (I12), 1 mixed
(I1: primary path clean, `DrainAndWait` loop warrants stress test).

Fix order recommended:

1. **I4** (failed-path worktree leak) — confirmed violation. One-line fix:
   add deferred `removeWorktree` in `failVessel` (runner.go:1278), or thread
   cleanup through the `persistRunArtifacts` defer. Cheap regression
   closure against the #22/#518/#531 family.
2. **I9** (terminal-path subprocess-kill gap) — confirmed violation.
   Co-located fix with I4: a shared terminal-path helper invoking
   `stopProcess` + `removeWorktree` + state transition. Closes in-process
   half of #575.
3. **I13** (dedupe-key mismatch) — aspirational until triple-keyed
   idempotency lands. Scope: add a journal entry or publisher-side cache
   keyed on `(vessel_id, phase_id, event_kind)`; keep title-match as
   defense in depth.
4. **I2 ID-duplicate guard** — add a pre-insert check to
   `markProcessStarted` that rejects dispatch if the ID is already tracked.
5. **I3 / I5 / I6 label sub-clause regression-pin** — write the directed
   property tests now; all are already closed in code but property tests
   prevent silent regression.
6. **I14 OnFail fan-out audit** — cheap to write the property test;
   confirms mutual exclusivity and single-fire without code changes.
7. **I7** — requires queue I5b atomic writes; tracked as composite fix.

---

## Notes for reviewer

Decisions recorded (2026-04-17):

- **I12** — keep as defensive future-proofing. Contract locks before filter-
  widening (#540 surface); trivially holds today; test is cheap.
- **I13** — triple-keyed `(vessel_id, phase_id, event_kind)` is the target
  contract. Current title-prefix dedupe is a known failure mode, not a
  specification. Marked aspirational with `t.Skip` pending the triple-keyed
  dedupe implementation.
- **I10** — restricted to runner-owned invariants. Startup-side
  `reconcileStaleVessels` (daemon.go:664) is the daemon command layer's
  obligation and is cross-ref'd but not claimed. I10 statement and gap row
  updated accordingly.

Grounding notes retained for implementors:

- **I9's `stopProcess` call graph.** One call site today: runner.go:4437
  (`CheckStalledVessels`). Terminal paths (`cancelVessel`, `failVessel`,
  `completeVessel`) all clear the map via `clearTrackedProcess` (deferred
  at runner.go:588) without invoking `stopProcess`. Clearest known violation;
  fix is co-located with I4.

- **I11 is a composite bound.** No single `phaseMaxRuntime` config exists.
  Ceiling = `context.WithTimeout(watchedCtx, vesselTimeout)` at runner.go:284
  (per-vessel) + `StallMonitor.PhaseStallThreshold` at runner.go:4357
  (PR#543 worker-stall safety net). Property test parametrizes over both paths.

---

## Governance

1. **Spec location.** This file: `docs/invariants/runner.md`. Changes require
   a human-signed commit. Agent-authored PRs that edit this file without
   explicit human direction must be rejected.
2. **Test location.** `cli/internal/runner/runner_invariants_prop_test.go`
   (distinct from the existing `runner_prop_test.go`,
   `runner_tracing_prop_test.go`, `monitor_prop_test.go`, and
   `summary_prop_test.go` — all of which cover local function-level
   properties, not module-level contracts). Every property test in the new
   file must carry a `// Invariant IN: <Name>` comment linking it to the
   spec entry above.
3. **Protected surfaces.** `.claude/rules/protected-surfaces.md` already
   includes `docs/invariants/*.md` and `cli/internal/*/invariants_prop_test.go`
   (added when queue.md landed). No amendment required for this doc or the
   test file to be protected. CI must fail if either is modified without a
   signed commit.
4. **CI enforcement.** Property tests run under the existing `go test ./...`
   path in CI. Tests for aspirational invariants (I7, I13) are `t.Skip`'d
   with comments referencing the gap-analysis row; tests for warning-flagged
   invariants (I1 race, I2 concurrent-drain, I3 cancel-vs-complete, I4
   failed-path leak, I6 retry bound + label sub-clause, I8 panic leak, I9
   terminal-kill gap, I10 sweep-reconcile, I11 wall-clock bound, I12
   stale-cancel, I14 single-fire hooks) run unskipped and are expected to
   pass against the current code — a failure is a genuine regression, not
   an unmet aspirational bound. I9 and I4 are expected failures until their
   respective fixes land; CI treats the test file's `t.Skip` / non-skip
   state as authoritative.

**Amendment procedure:** an invariant may only be relaxed via a PR that (a)
edits this document, (b) is authored or signed by a human, and (c) includes
rationale tied to a real constraint (not "the test was failing"). Agents may
propose amendments but may not merge them.

---

## Proposed property tests

Signatures and one-line purposes. Bodies to be generated after English sign-
off per skill methodology (Step 7).

```go
// In cli/internal/runner/runner_invariants_prop_test.go

// Invariant I1: Concurrency caps are never exceeded.
func TestInvariant_I1_ConcurrencyCapsNeverExceeded(t *testing.T)
// Drives rapid-generated Enqueue ops across mixed classes; launches Drain
// with a CommandRunner that blocks on a release channel; at every observable
// instant (sampled via a goroutine poller), asserts InFlightCount ≤
// cfg.Concurrency AND, for each class c, len({v: ConcurrencyClass == c ∧
// running}) ≤ ConcurrencyLimit(c). Releases; joins; asserts cap invariant
// never broke.

// Invariant I2: No concurrent phase execution for a single vessel ID.
func TestInvariant_I2_NoConcurrentPhaseForVesselID(t *testing.T)
// Enqueues N vessels with mixed IDs; spawns K concurrent Drain calls on the
// shared runner; after Wait, inspects the tracked-process history log and
// asserts (a) no ID appeared in `markProcessStarted` twice without an
// intervening `clearTrackedProcess`, and (b) sum of Launched across ticks
// ≤ N.

// Invariant I3: Cancellation takes precedence over completion.
func TestInvariant_I3_CancellationPrecedesCompletion(t *testing.T)
// Rapid-drives a single-phase workflow with a mock LLM that holds on a
// channel. Concurrently issues Queue.Cancel(id) at rapid-drawn offsets
// (before, during, just-before-UpdateCompleted, after). After Wait, asserts:
// if Cancel was observed pre-UpdateCompleted, final state is cancelled and
// outcome is "cancelled"; otherwise state is completed.

// Invariant I4: Worktrees are removed on every terminal outcome.
func TestInvariant_I4_WorktreeRemovedOnTerminalOutcome(t *testing.T)
// Drives mixed-outcome vessels (completed, failed via LLM error, cancelled
// mid-phase, timed_out via injected timeout) against a mockWorktree that
// records Create/Remove calls. After Wait, for every vessel whose final
// state is terminal-not-waiting, asserts the path returned by Create was
// later passed to Remove (or the Create was rolled back before persistence).

// Invariant I5: Prune never removes the worktree of a non-terminal vessel.
func TestInvariant_I5_PruneExcludesNonTerminalWorktrees(t *testing.T)
// Interleaves Enqueue → (mock) Create → Queue.UpdateVessel(WorktreePath) with
// concurrent PruneStaleWorktrees calls. Asserts: (a) for any vessel that had
// entered running but not yet persisted WorktreePath, prune returned
// errWorktreeSetupInProgress OR defered; (b) for any vessel whose
// WorktreePath was persisted and state ∈ {pending, running, waiting}, the
// path never appeared in FindStaleWorktrees().

// Invariant I6: Gate retries are finite, and label-kind gates suspend
// instead of consuming retry budget.
func TestInvariant_I6_GateRetriesFiniteAndLabelSuspends(t *testing.T)
// Rapid-generated workflow with retries ∈ [0, 8], single phase, gate that
// always fails with injected mockCmdRunner. Asserts total gate invocations
// = retries + 1 and final vessel state is failed (or waiting, for label
// gates). Explicitly NO "completed" reporter calls interleaved (overlap
// with existing TestProp_CommandGateFailuresNeverEmitCompletedComments is
// intentional — this test's scope is the COUNT, not the comment semantics).
// Sub-clause: for label-kind gates, pre-seed gate-label absent; assert
// final state is `waiting`, retry counter was NOT decremented (i.e.,
// gate-attempt count is 1, not `retries + 1`), and src.OnWait was called
// exactly once.

// Invariant I7: Phase output persistence precedes next-phase dispatch.
// ASPIRATIONAL — t.Skip with ref to gap-analysis row until queue I5b lands.
func TestInvariant_I7_PhaseOutputPersistenceOrdering(t *testing.T)
// Rapid-drives a multi-phase workflow with an injected CommandRunner that
// records per-call timestamps AND a queue wrapper that records
// UpdateVessel(CurrentPhase=N+1) timestamps. Asserts, for every transition
// N → N+1: phase-N output file exists before UpdateVessel returned, AND
// UpdateVessel(CurrentPhase=N+1) returned before phase-N+1's CommandRunner
// call began. Separately simulates mid-transition SIGKILL by rebuilding the
// runner from disk at every checkpoint and asserting
// rebuildPreviousOutputs' result matches the persisted state.

// Invariant I8: In-flight accounting is exact.
func TestInvariant_I8_InFlightAccountingExact(t *testing.T)
// Rapid-generated mixed outcomes including mockCmdRunner panics, context
// cancellations, and timeout injections. After Wait, asserts
// InFlightCount() == 0 AND len(r.processes) == 0. Separately samples
// InFlightCount during drive and asserts it never exceeds the number of
// goroutines that have observably started (bound by observability span
// count) and never drops below zero.

// Invariant I9: The LLM subprocess is killed on every terminal outcome
// before its entry is dropped from the tracked-process map.
// KNOWN VIOLATION as of draft v2 — expected failure until fix lands.
func TestInvariant_I9_SubprocessKilledOnTerminalOutcome(t *testing.T)
// Rapid-generated mixed-outcome vessels against an injected CommandRunner
// that records signal delivery per PID. After Wait, for every vessel whose
// final state is terminal (completed, failed, cancelled, timed_out), assert
// the PID recorded in markProcessStarted received at least one signal
// before clearTrackedProcess removed the map entry. Interleave with
// cancel-mid-phase and timeout injection to cover all three terminal paths
// (cancelVessel, failVessel, completeVessel); stall-path is already covered
// by CheckStalledVessels and serves as the "at most once" acceptance case.

// Invariant I10: Vessels in running with no live goroutine are reconciled
// by the runner's periodic sweeps within a bounded number of ticks.
func TestInvariant_I10_SweepReconciliationOfRunningVessels(t *testing.T)
// Constructs a queue pre-seeded with N vessels in `running` (with and
// without persisted WorktreePath, with and without on-disk worktrees)
// but with NO corresponding entries in the runner's processes map.
// Constructs a fresh Runner against it. Advances the clock to drive `k`
// tick cycles (CheckHungVessels + CheckStalledVessels under a controlled
// clock). Asserts every vessel has either been re-dispatched (new
// trackedProcess observed) OR transitioned to a terminal state with a
// non-empty recovery_reason. NO vessel remains in `running` with zero
// goroutines after `k` ticks. Scope: runner-owned sweeps only; daemon-
// layer startup-reconcile (daemon.go:664) is not exercised here.

// Invariant I11: Every phase invocation is bounded by a finite wall-clock
// ceiling.
func TestInvariant_I11_PhaseInvocationWallClockBound(t *testing.T)
// Rapid-generated single-phase workflow driven by an injected CommandRunner
// that blocks indefinitely (simulates hung subprocess). Parametrizes over
// two enforcement paths: (a) per-vessel context.WithTimeout expiry at
// runner.go:284, and (b) CheckStalledVessels path at runner.go:4357 with
// driven-clock advance past PhaseStallThreshold. For each path, asserts:
// the phase's stopProcess was called; the vessel's final state is one of
// {timed_out, failed}; the vessel's WorktreePath was removed (composes
// with I4); the wall-clock elapsed between dispatch and terminal
// transition is ≤ B + tolerance.

// Invariant I12: Stale-cancel satisfies cancellation post-conditions.
func TestInvariant_I12_StaleCancelSatisfiesPostConditions(t *testing.T)
// Rapid-generated pool of pending PR-ref vessels, some with injected
// "merged" / "closed" `gh pr view` responses and some with "open". After
// CancelStalePRVessels, asserts: every vessel whose PR responded
// merged/closed is in state `cancelled`; every "open" vessel is still
// `pending`; no worktree was created (trivially true for pending); no
// subprocess was tracked. Includes a scope-widened variant gated behind
// t.Skip (pre-seeds vessels in StateRunning with tracked worktree/PID)
// that asserts I3 + I4 post-conditions hold under hypothetical filter
// widening — exists to lock the contract, not to exercise current code.

// Invariant I13: No duplicate discussion publications per (vessel_id,
// phase_id, event_kind) triple.
// ASPIRATIONAL — t.Skip until triple-keyed dedupe lands; current code
// dedupes by title prefix only (discussion.go:97).
func TestInvariant_I13_NoDuplicateDiscussionPublicationsPerEvent(t *testing.T)
// Rapid-drives a workflow whose phase publishes on completion. Runs the
// same vessel through multiple gate retries and one simulated daemon
// restart mid-publication. After Wait, collects all FindExisting/Create/
// Comment calls recorded by the injected discussion hook. Asserts: for
// every distinct (vessel.ID, phase_id, event_kind) triple, publication
// count is ≤ 1. Injects title-render variability across retries to
// reproduce the PR#493 regression class and confirm triple-keyed dedupe
// holds where title-keyed dedupe fails.

// Invariant I14: Source lifecycle hooks fire exactly once per lifecycle
// event.
func TestInvariant_I14_SourceLifecycleHooksFireExactlyOnce(t *testing.T)
// Rapid-generated mixed-outcome vessels (success, fail via gate, cancel
// mid-phase, timeout, label-wait-then-resume, multi-phase with retries on
// non-first phase). Injected source stub records every hook invocation
// with a timestamp. After Wait, asserts: OnStart count == 1 for every
// vessel that entered running; OnFail count == 1 for every vessel ending
// `failed`; OnCancel count == 1 for every vessel ending `cancelled`;
// OnWait count == 1 for every vessel entering `waiting`; OnComplete count
// == 1 for every vessel ending `completed`; OnFail + OnCancel ≤ 1 per
// vessel (mutual exclusivity); restart-rehydration does NOT re-fire
// OnStart for vessels already past the OnStart checkpoint.
```

Each test carries a `// Invariant IN: <Name>` comment linking it back to the
spec entry. The skip messages on aspirational tests (I7, I13) reference the
gap-analysis row so reviewers can find the blocking work item.
