# Invariants: `cli/internal/queue`

Status: **draft v1** (2026-04-16). Ratified by: pending human sign-off.

This document is the load-bearing specification for the `queue` package. It is
protected: changes require human review (see **Governance**). Agent-authored
PRs that relax an invariant without an accompanying `.claude/rules/protected-surfaces.md`
amendment must be rejected.

---

## Contract

The `queue` package provides a **durable, linearizable, single-writer vessel
queue** backed by a JSONL file with `flock`-based locking. Callers enqueue
work items (`Vessel`s) identified by opaque `ID` and optional external `Ref`;
the queue transitions them through a fixed state machine (`pending → running
→ {completed, failed, cancelled, waiting, timed_out}`); supports retry via
`failed → pending`; and survives graceful process restarts.

Callers rely on the queue to **not double-dispatch, not lose work under
graceful shutdown, not corrupt terminal records, and not permit illegal state
transitions**. Everything outside those guarantees (liveness, external
consistency, cost) is the runner's problem.

---

## Invariants

**I1. At-most-one active per ref.**
For every non-empty `Ref`, at most one vessel with that `Ref` is in
`{pending, running, waiting}` at any observable instant.
Formal: `∀ ref ≠ "". |{v ∈ List() : v.Ref = ref ∧ v.State ∈ {pending, running, waiting}}| ≤ 1`.
- *Why:* prevents double-dispatch (issue #541 AND-match duplicates; loop 239).
- *Test:* rapid sequence of arbitrary Enqueue/Update/Cancel ops; after each step, assert the count invariant for every `Ref` seen.

**I1a. Enqueue of an active ref is a no-op.**
If I1's active set already contains a vessel with `Ref = R`, `Enqueue` of any
vessel with the same `Ref` must not append a record, must not mutate the
existing vessel, and must return `(false, nil)`.
- *Why:* operational corollary of I1 at the API boundary; prevents scanners
  from silently growing the queue file.
- *Test:* enqueue twice with same ref; assert second returns `(false, nil)`
  and file byte-length is unchanged.

**I2. Terminal records are immutable in place, except for `failed → pending` retry.**
Once `v.State ∈ {completed, cancelled, timed_out}`, no operation may mutate
any of: `State, StartedAt, EndedAt, Error, CurrentPhase, PhaseOutputs,
GateRetries, WaitingSince, WaitingFor, WorktreePath, FailedPhase, GateOutput,
Ref, Source, Workflow, WorkflowDigest, WorkflowClass, Tier, RetryOf`.
Physical deletion via `Compact`/`CompactOlderThan` is permitted and does not
count as mutation. `failed` is the only terminal state exempt from this
invariant, and only via transition to `pending` (governed by I3).
- *Why:* terminal records are the audit trail. Silent mutation makes
  postmortems unreliable and corrupts the compaction dedup key.
- *Test:* generate an arbitrary terminal vessel; apply any sequence of
  mutating ops that do not transition state; assert serialized record is
  byte-identical pre/post.
- *Verified kernel:* the predicate "state is terminal" (`IsTerminal`) is
  formally verified in `cli/internal/queue/verified/state_machine.dfy`
  (Dafny 4.11.0, 1 verified, 0 errors). The Go extraction is
  `cli/internal/queue/verified/state_machine.go`. Roadmap #06.

**I3. Retry resets to indistinguishable-from-fresh.**
A vessel transitioned `failed → pending` must have *exactly* the following
fields cleared: `StartedAt=nil, EndedAt=nil, Error="", CurrentPhase=0,
PhaseOutputs=nil, GateRetries=0, WaitingSince=nil, WaitingFor="",
FailedPhase="", GateOutput="", WorktreePath=""`. The only fields carried
across retry are identity (`ID`, `Source`, `Ref`, `Workflow`, `WorkflowDigest`,
`WorkflowClass`, `Tier`, `Prompt`, `Meta`, `CreatedAt`) and `RetryOf`.
- *Why:* partial state leaks cause mid-workflow resumption bugs (see loop 202
  chdir cascade). If runner resumes at non-zero `CurrentPhase`, retries are
  not fresh — they are corrupted resumes.
- *Test:* construct a failed vessel with every resettable field set; call
  `Update(id, pending, "")`; diff against a freshly-constructed pending
  vessel (same identity); assert all lifecycle fields match.

**I4. Monotonic lifecycle timestamps within a running episode.**
For any vessel mutated only via `Enqueue`, `Dequeue(Matching)`, `Update`, or
`Cancel`: `CreatedAt ≤ StartedAt ≤ EndedAt` whenever each is set, and
`WaitingSince ≥ StartedAt` within a single running episode (where a running
episode is the interval between the last `_ → running` transition and the
next transition out of `running`).
`UpdateVessel` and `ReplaceAll` are privileged and exempt; callers of those
paths carry the timestamp-monotonicity obligation.
- *Why:* stall detection, age compaction, and phase timeouts all rely on
  time moving forward. A clock regression (NTP jump, bad caller) silently
  poisons all three.
- *Test:* rapid sequence of legal transitions on the non-privileged paths;
  after every mutation, assert the timestamp partial order on every vessel.

**I5a. Reopen-equivalence (graceful durability).**
After any successful mutating call returns, constructing a fresh `Queue`
against the same path and calling `List()` produces a vessel slice
byte-identical to the in-process `List()`.
- *Why:* daemon restarts are the recovery primitive. If memory diverges from
  disk even once, reconcile lies.
- *Test:* after every mutating op in a rapid sequence, snapshot `q.List()`;
  construct a new `Queue` against the same path; assert equality.

**I5b. Crash durability (aspirational — currently violated).**
Unplanned process termination at any point during a mutating call must leave
the queue file in a state that, when reopened, yields either (a) the
pre-call vessel set or (b) the post-call vessel set. Intermediate states are
forbidden.
- *Why:* the daemon is SIGKILL'd routinely (loops 200, 206, 214, 219). A
  torn write between states is silent data loss.
- *Test:* harness that launches a subprocess, interrupts it via `SIGKILL` at
  randomized offsets through a mutating call, reopens, and asserts the file
  parses cleanly and contains one of the two valid sets.
- *Status:* **known violation.** `writeAllVessels` uses `os.WriteFile` (no
  fsync, no tmpfile+rename). Marking aspirational until atomic writes land;
  property test should exist and be allowed to fail until then.

**I6. Linearizability.**
Every queue operation appears to take effect atomically at some instant
between its invocation and its response, and the order of those instants is
consistent with the real-time ordering of non-overlapping operations. I1–I5
hold at every observable instant.
- *Why:* concurrent scanners + drain + reconcile must not interleave into
  invalid states. `flock` gives exclusion; this invariant gives the user
  the guarantee that depends on it.
- *Test:* rapid-stateful driver issues a random op sequence across N
  goroutines; record each op's invocation/response timestamps; after join,
  search for a linear schedule consistent with observed responses and
  assert I1–I5 hold at every point in that schedule.

**I7. State transition soundness.**
Every operation that mutates `State` obeys `validTransitions`:
`∀ mutation producing (v_before, v_after) with v_before.State ≠ v_after.State.
validTransitions[v_before.State][v_after.State] = true`. `Compact`,
`CompactOlderThan`, and `ReplaceAll` do not mutate `State` per-vessel and are
exempt. `ReplaceAll` is a privileged operation that may install any vessel
set, but callers are responsible for preserving I1, I8, I9, I10 when using it.
- *Why:* the transition table is load-bearing. Every illegal transition is
  a latent cascade.
- *Test:* rapid ops on arbitrary starting states; after every mutation,
  assert `validTransitions[old][new] == true`.
- *Verified kernel:* the `IsTerminal` predicate used by terminal-state guards
  is formally verified in `cli/internal/queue/verified/state_machine.dfy`.
  Roadmap #06.

**I8. Queue file well-formedness.**
Every non-blank line in the queue file is a valid JSON `Vessel`. Malformed
lines are a corruption event and must either (a) be rejected at read with an
error that stops further queue use, or (b) never be produced.
- *Why:* `readAllVessels` currently skips malformed lines silently (queue.go:629).
  Any torn write therefore causes invisible data loss rather than loud failure.
  Fail-closed is a prerequisite for I5b.
- *Test:* rapid-generated write sequences; after each, assert `readAllVessels`
  returns without error and every line round-trips.
- *Status:* **known violation.** `readAllVessels` tolerates corruption.

**I9. Unique vessel IDs.**
`∀ v1, v2 ∈ List(). v1.ID = v2.ID ⟹ v1 = v2`. `ID` is a primary key across
the queue.
- *Why:* `FindByID` returns the last match (queue.go:311); without ID
  uniqueness, every downstream state-machine claim would silently diverge
  when two vessels shared an `ID`.
- *Test:* explicit two-vessel construction — enqueue a vessel, then enqueue
  a second vessel sharing the first's `ID` but with a distinct `Ref` (to
  bypass the Ref-dedup short-circuit); assert `ErrDuplicateID` and
  `len(List()) == 1`.
- *Status:* **enforced.** `Enqueue` rejects duplicate `ID` with
  `ErrDuplicateID` (PR #594, queue.go:223–227).

**I10. `RetryOf` forms a DAG rooted at fresh vessels.**
If `v.RetryOf ≠ ""`, then (a) there exists `w ∈ List() ∪ compacted_history`
with `w.ID = v.RetryOf`, (b) `w.State` is terminal, and (c) the retry graph
is acyclic.
- *Why:* prevents dangling retry references and retry cycles. Without this,
  retry chains can become orphaned and `FindLatestByRef` semantics drift.
- *Test:* rapid retry sequences; assert graph is a DAG whose leaves are
  terminal and whose roots have `RetryOf = ""`; also exercise the verified
  checker's error path: call `verified.IsAcyclic` directly with a cyclic
  graph and assert it returns false; assert `Enqueue` rejects a self-loop
  (`RetryOf == ID`); assert `UpdateVessel` rejects a RetryOf change that
  would close a cycle.
- *Verified kernel:* the acyclicity predicate `IsAcyclic` is formally
  verified in `cli/internal/queue/verified/retry_dag.dfy` (Dafny 4.11.0,
  2 verified, 0 errors). The Go extraction is
  `cli/internal/queue/verified/retry_dag.go`. Roadmap #09.
- *Enforcement scope:* `Enqueue` and `UpdateVessel` enforce the acyclicity
  check via the verified kernel. `ReplaceAll` is a privileged bulk-write
  operation; callers carry the I10 obligation for that path (same as I1).

**I11. Compaction preserves the active set.**
`∀ v ∈ vessels with v.State ∈ {pending, running, waiting}: v ∈ Compact(vessels)`.
`CompactOlderThan` obeys the same guarantee for active vessels regardless of
`EndedAt` (which is `nil` for them anyway).
- *Why:* active work must never vanish to compaction. Currently true by
  construction (`compactVessels` only removes when `IsTerminal()`), but
  unstated — a future refactor could silently break it.
- *Test:* rapid vessel sets with mixed states; assert every non-terminal
  input vessel appears byte-identical in the output.

**Retry-idempotence note (not a separate invariant):**
`failed → pending` MUST reuse the same `ID`; it must NOT create a new vessel.
Otherwise a runner bug could re-enqueue under a new ID and bypass I1 (new
ID, same ref ≤ 1 active still passes). Enforced implicitly by the transition
table + `Update` semantics; if the retry path is ever refactored to use
`Enqueue`, this note becomes a load-bearing invariant.

---

## Not covered

Explicitly out of scope for this spec. If you suspect a bug in one of these,
it does not belong in `queue`:

- **Liveness.** "Every `pending` vessel eventually reaches a terminal state."
  Belongs to `runner` / scheduler.
- **Dispatch correctness.** "The right vessel runs on the right worktree with
  the right workflow." Belongs to `runner`.
- **Worktree consistency.** The queue tracks `WorktreePath` as a string; it
  does not guarantee the worktree exists on disk or matches the vessel's
  expectations. Belongs to `worktree` + `runner`.
- **External consistency.** GitHub labels, subprocess liveness, fingerprint
  dedup. Belongs to `source` / `runner`.
- **Cost and quota.** No per-vessel or per-queue spend bound. Belongs to
  `cost`.
- **Clock-source trust.** I4 assumes monotonic time; the queue uses wall
  clock via `queueNow()` → `dtu.RuntimeNow()` → `time.Now()`. Clock regressions
  are outside the queue's control.
- **Cross-daemon dispatch.** `flock` is advisory per-FD and local-host only;
  does not prevent two daemons on a shared filesystem (e.g. NFS) from double-
  dispatching. Out of scope per CWD-scoping (see CLAUDE.md multi-repo section).

---

## Gap analysis

Reviewed 2026-04-16 against `cli/internal/queue/queue.go` as of commit
`60eeaba`. This is the fix backlog. When the property tests land, each
non-✓ row below is an expected test failure until the corresponding code
fix is merged.

| Invariant | Status | Site | Note |
|---|---|---|---|
| I1 | ✓ | `Enqueue` (queue.go:127) | Ref check + append under single lock. |
| I1 | ⚠ | `ReplaceAll` (queue.go:293) | No ref-uniqueness check; property test must assert I1 after `ReplaceAll`. |
| I1a | ✓ | `Enqueue` (queue.go:137–144) | Returns `(false, nil)` on active-ref collision. |
| I2 | ✓ | `UpdateVessel` (queue.go:472) | `protectedFieldsEqual` guard: rejects any `UpdateVessel` where `isSealedTerminal(previous.State)` and any of the 19 I2-protected fields differ. Explicit field-by-field comparison (not reflection); `TestPropQueueInvariant_I2_TerminalImmutability` pins the guarantee. |
| I3 | ✗ | `resetPendingState` (queue.go:268) | Does not reset `CurrentPhase` or `PhaseOutputs`. Runner-visible consequence: retries resume mid-workflow. `WorktreePath` reset is conditional on previous state being `running`, which leaks stale paths on `failed → failed` chains that later retry. |
| I4 | ⚠ | `UpdateVessel`, `ReplaceAll` | Accept arbitrary caller timestamps. Privileged-path exemption is documented in I4 itself; no code fix required, but property test must scope to non-privileged paths. |
| I4 | ⚠ | `queueNow` (queue.go:674) | Falls back to `time.Now().UTC()` on `dtu.RuntimeNow` error; wall-clock can regress. Documented as "Not covered: clock-source trust." |
| I5a | ✓ | `writeAllVessels` + `readAllVessels` | Holds under graceful return. |
| I5b | ✗ | `writeAllVessels` (queue.go:698) | `os.WriteFile` is not atomic: no `fsync`, no tmpfile+rename. Crash mid-write silently truncates or partial-writes. Fix: write to `path + ".tmp"`, `fsync(tmp)`, `rename(tmp, path)`, `fsync(dir)`. |
| I6 | ✓ | `withLock`/`withRLock` (queue.go:579, 592) | Single-writer flock gives linearizability for each op. Property test must still exercise concurrency to rule out future regressions. |
| I7 | ✓ | `Update` (queue.go:219), `Cancel` (queue.go:409) | Transition validated before mutation. |
| I7 | ✓ | `UpdateVessel` (queue.go:464) | Validates only on state-change, which is correct for I7. |
| I8 | ✗ | `readAllVessels` (queue.go:629) | Silently skips malformed lines with a `log.Printf` warning. Fix: fail-closed (return error and stop using queue) OR make writes atomic so malformed lines cannot appear. Prerequisite for honest I5b. |
| I9 | ✓ | `Enqueue` (queue.go:223–227) | Rejects duplicate-`ID` enqueue with `ErrDuplicateID` (PR #594). Ref-dedup at queue.go:212–221 short-circuits before the ID check when both `Ref` and `ID` collide; property test uses a distinct `Ref` to exercise the ID path. |
| I10 | ✓ | `Enqueue`, `UpdateVessel` | Acyclicity enforced via `verified.IsAcyclic` (Dafny-verified kernel, retry_dag.dfy). Self-loops and multi-node cycles rejected with `ErrRetryDAGCycle`. `ReplaceAll` remains caller-responsibility (privileged path). Roadmap #09. |
| I11 | ✓ | `compactVessels` (queue.go:467) | Removes only when `IsTerminal()`. Property test pins the current guarantee against future regressions. |

**Summary:** 3 outright violations (I3, I5b, I8), 2 warnings (I1/I4 partial coverage). I2 landed on branch `feat/queue-verified-valid-transition` (PR #687) and is pinned by `TestPropQueueInvariant_I2_TerminalImmutability`. I9 landed in PR #594 and is pinned by `TestPropQueueInvariant_I9_UniqueIDs`. I10 acyclicity enforcement landed in roadmap #09 (verified kernel `retry_dag.dfy`), pinned by `TestPropQueueInvariant_I10_RetryOfDAG`. Fix order recommended for the remaining items:

1. **I3** (`CurrentPhase`/`PhaseOutputs` reset) — needs policy sign-off on runner-side consequences before the reset is added.
2. **I8 + I5b** (atomic writes + fail-closed reads) — paired change; correct order is I8 first (loud), then I5b (quiet, builds on I8).

---

## Governance

1. **Spec location.** This file: `docs/invariants/queue.md`. Changes require
   a human-signed commit. Agent-authored PRs that edit this file without
   explicit human direction must be rejected.
2. **Test location.** `cli/internal/queue/queue_invariants_prop_test.go`
   (distinct from the existing `queue_prop_test.go` which covers only
   compaction). Every property test must carry a `// Invariant IN: <Name>`
   comment linking it to the spec entry above.
3. **Protected surfaces.** `.claude/rules/protected-surfaces.md` extended to
   include both paths. CI must fail if either is modified without a signed
   commit.
4. **CI enforcement.** The property tests run under the existing `go test
   ./...` path in CI. The I5b test is expected to fail until atomic writes
   land; until then it is marked `t.Skip` with a comment referencing this
   document's "known violation" note for I5b.

**Amendment procedure:** an invariant may only be relaxed via a PR that (a)
edits this document, (b) is authored or signed by a human, and (c) includes
rationale tied to a real constraint (not "the test was failing"). Agents may
propose amendments but may not merge them.
