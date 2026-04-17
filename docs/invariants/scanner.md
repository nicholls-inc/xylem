# Invariants: `cli/internal/scanner`

Status: **draft v1** (2026-04-16). Ratified by: pending human sign-off.

This document is the load-bearing specification for the `scanner` package. It
is protected: changes require human review (see **Governance**). Agent-authored
PRs that relax an invariant without an accompanying `.claude/rules/protected-surfaces.md`
amendment must be rejected.

---

## Contract

The `scanner` package discovers actionable external work via configured
`Source` implementations and deposits it into the queue as well-formed
`pending` `Vessel`s. It is the **only ingress path** from the outside world
(GitHub issues, PR events, merge requests, scheduled cron, failed GitHub
Actions) into the vessel queue.

Callers — the daemon tick loop and the `xylem scan` CLI — rely on the scanner
to **not double-enqueue, not silently drop work, not let one source's failure
block the others, and not mutate the queue from read paths** (e.g.
`BacklogCount`). Everything outside those guarantees (per-source filter
correctness, vessel construction, scheduled-source cadence, liveness) belongs
to `source` or the daemon.

---

## Invariants

**S1. Enqueue is the scanner's only queue-mutation operation.**
During any `Scan` or `BacklogCount` call, scanner invokes exactly and only
`Queue.Enqueue` as a write operation. It does not call `Update`, `Cancel`,
`UpdateVessel`, `ReplaceAll`, `Compact`, or any other mutating API. Read-only
queries are permitted.
- *Why:* the runner owns state transitions; the scanner owns ingestion. A
  scanner bug must at worst enqueue garbage, never corrupt an in-flight
  vessel. Separation lets incident response blame the right module (loop 222
  "daemon merged PR" was a misattribution precisely because multiple modules
  touch GitHub).
- *Test:* wrap `Queue` in a spy that counts calls per method; run rapid scans
  with arbitrary configs; assert only `Enqueue` and read-only methods have
  non-zero counts.

**S2. No duplicate enqueue per `(Source, Ref)` within a tick.**
For any `Scan(ctx)` returning `(result, nil)`, the set of vessels where
`Queue.Enqueue` returned `(true, nil)` has no two elements sharing the same
`(Source, Ref)` with non-empty `Ref`. Scanner MAY rely on `Queue.Enqueue`'s
I1/I1a dedup, but the property must hold regardless of implementation
strategy.
- *Why:* cross-task dedup (`TestScanCrossTaskDedupDeterministic`). Duplicates
  burn cost and create conflicting PRs (PR#544 AND-match leaked duplicates;
  loop 239 cancelled two duplicates).
- *Test:* rapid source outputs with overlapping `(Source, Ref)` pairs; spy
  queue to record every successful `Enqueue`; assert uniqueness of
  `(Source, Ref)` keys in the enqueued set.

**S3. Pause marker aborts the tick before any side effect.**
If `<StateDir>/paused` exists when `Scan` is invoked, `Scan` returns
`ScanResult{Added: 0, Skipped: 0, Paused: true}, nil` without invoking any
`Source.Scan`, `BudgetGate.Check`, `Queue.Enqueue`, or lifecycle hook.
- *Why:* `state/paused` is the user's emergency brake during incidents (loop
  234 used it to halt burn during an auth failure). A scanner that "gets
  halfway" would dispatch partially during an emergency.
- *Test:* rapid scanner configurations; for each, toggle the pause marker;
  spy Source/Queue/BudgetGate; when `Paused=true`, assert all spy counters
  are zero and the result is exactly `{0, 0, true}`.

**S4. Source `Scan` errors do not cross-contaminate other sources.**
For a configuration with N sources, if `S_k.Scan(ctx)` returns `err ≠ nil`,
`Scanner.Scan` must still invoke `Scan` on sources `S_j, j ≠ k` and enqueue
their returned vessels. The overall `Scan` call MAY surface `err` from
`S_k`, but the `ScanResult` must reflect the sum of every successful source's
contribution.
- *Why:* one GitHub repo outage silently blocking every other repo's intake
  would be catastrophic. `TestScanGHFailure` covers the single-source case
  only. Scanner-silence incidents (loop 223, 50 minutes) are the worst
  observability pathology in this system.
- *Test:* rapid source list with one random source injected to error; assert
  every other source's vessels appear in the queue and `result.Added` is at
  least the sum of healthy contributions.
- *Status:* **expected violation.** `scanner.go:75-77` `return result, err`
  aborts the loop on the first source error. Property test will fail against
  current code.

**S5. Non-dedup `Enqueue` errors do not cross-contaminate other sources.**
If `Queue.Enqueue(v_kj)` returns an error other than `queue.ErrDuplicateID`
while processing source `S_k`'s j-th vessel, sources `S_m, m > k` and vessels
`v_km, m > j` from `S_k` must still be attempted. `ErrDuplicateID` is
explicitly tolerated (log + `Skipped++`) and already does not fail the tick.
`recovery.UpdateRetryOutcome` errors are covered by the same guarantee.
- *Why:* same blast-radius argument as S4, but for the `Enqueue` leg.
  Partial-tick behavior is the worst failure mode — the operator sees
  `Added=3` and assumes three things were enqueued, when in fact the fourth
  source's twenty items were silently dropped.
- *Test:* rapid sources × rapid `Enqueue` outcomes (inject non-`ErrDuplicateID`
  failure at random positions); assert vessels after the failure point are
  still attempted.
- *Status:* **expected violation.** `scanner.go:106-108` (non-`ErrDuplicateID`
  `Enqueue` error) and `scanner.go:111-113`
  (`recovery.UpdateRetryOutcome` error).

**S6. Hooks fire exactly when `Enqueue` succeeds and `RunHooks` is true.**
For each vessel `v` produced by a source during a non-paused tick,
`Source.OnEnqueue(ctx, v)` is invoked iff: (a)
`BudgetGate.Check(v.ConcurrencyClass()).Allowed = true`, (b)
`Queue.Enqueue(v)` returned `(true, nil)`, AND (c) `Scanner.RunHooks = true`.
In all other cases (budget deny, dedup skip `(false, nil)`, `Enqueue` error,
`RunHooks = false`), `OnEnqueue` is NOT called. `OnEnqueue` errors are
logged and do NOT fail the tick.
- *Why:* `TestScanBudgetGateSkipDoesNotEnqueueOrRunHooks` locks one slice.
  Hooks perform external side effects (GitHub label ops) that must mirror
  queue truth; leaked hooks produce `queued` labels on issues that aren't
  queued.
- *Test:* rapid tick scenarios; spy `Source.OnEnqueue`; assert call count
  equals the count of `(Allowed ∧ enqueued ∧ RunHooks)` triples.

**S7. `config_source` meta propagation.**
For every vessel `v` that `Scanner.Scan` passes to `Queue.Enqueue` with a
non-empty configured source name, `v.Meta["config_source"]` equals that
configured source name. If the source entry has an empty config name
(direct-construction test path), `Meta["config_source"]` may be absent.
Scanner MUST allocate `v.Meta` if the source returned `v.Meta == nil`.
- *Why:* the runner resolves source-level LLM/model overrides by this key.
  Missing or wrong `config_source` causes silent tier misrouting (adjacent
  incident class: loops 236-238, `gpt-4.1 + effort` blocker — wrong LLM
  selection at runtime).
- *Test:* rapid configs with named sources returning vessels with and
  without pre-populated `Meta`; spy `Enqueue`; assert
  `Meta["config_source"]` equals the configured source name on every
  enqueued vessel.

**S8. `BacklogCount` is side-effect free.**
`BacklogCount(ctx)` invokes `BacklogSource.BacklogCount` on each implementing
source and sums the results. It does not call `Queue.Enqueue` or any write
op, any `Source.On*` hook, `BudgetGate.Check`, or touch the pause marker.
The returned count is `≥ 0`.
- *Why:* operator dashboards poll `BacklogCount` to see imminent workload;
  a read that enqueues as a side effect would create the work it reports.
- *Test:* rapid configs; call `BacklogCount` under a spy queue/source;
  assert zero write calls and zero hook invocations; the returned total
  equals the sum of each `BacklogSource.BacklogCount` return.

---

## Not covered

Explicitly out of scope for this spec. If you suspect a bug in one of these,
it does not belong in `scanner`:

- **Required-label AND matching.** Belongs to `source/github.go` (filter
  logic lives there). Follow-up: `docs/invariants/source.md`.
- **Scheduled-source cadence throttling.** Belongs to
  `source/scheduled.go` + `source/schedule.go`.
- **Existing-branch / existing-PR dedup.** Belongs to `source/github.go`;
  scanner receives whatever vessels the source decided to return.
- **Vessel well-formedness** (`ID`, `Workflow`, `State=pending`, `CreatedAt`
  set). Belongs to `source` — each source type constructs its own vessels;
  scanner is not the factory.
- **Unknown source types in config.** Silently skipped in `buildSources`
  (`scanner.go:162-258`, no `default:`); if this is a user footgun, it
  belongs to `config` validation.
- **Source iteration order determinism.** Go map iteration over
  `Config.Sources` is non-deterministic; scanner makes no ordering promise.
  Order-sensitive invariants must not be added here.
- **Concurrent `Scan` calls.** Scanner is not designed for concurrent
  self-invocation. Linearizability is the queue's problem (I6).
- **Liveness / eventual enqueue.** Belongs to the daemon tick-loop cadence,
  not scanner.

---

## Gap analysis

Reviewed 2026-04-16 against `cli/internal/scanner/scanner.go` as of commit
`50de24f`. This is the fix backlog. When the property tests land, each
non-✓ row below is an expected test failure until the corresponding code
fix is merged.

| Invariant | Status | Site | Note |
|---|---|---|---|
| S1 | ✓ | `Scan` only calls `Queue.Enqueue`; `BacklogCount` makes no queue calls | Pin via spy test. |
| S2 | ✓ (delegated) | `scanner.go:96` → queue I1/I1a | Holds via queue's guard; property test exercises both layers. |
| S3 | ✓ | `scanner.go:65-68` | Pause check fires before `buildSources`. |
| S4 | ✗ | `scanner.go:75-77` | `return result, err` aborts iteration on first source error. Fix: collect into a multi-error, continue loop, return aggregate. |
| S5 | ✗ | `scanner.go:106-108`, `scanner.go:111-113` | Both non-`ErrDuplicateID` `Enqueue` errors and `recovery.UpdateRetryOutcome` errors abort mid-tick. Same fix shape as S4. |
| S6 | ✓ | `scanner.go:89-94`, `scanner.go:96-105`, `scanner.go:109-120` | Guard correctly binds the hook call to `(Allowed ∧ enqueued ∧ RunHooks)`. |
| S7 | ✓ | `scanner.go:80-86` | Set unconditionally when `entry.configName != ""`, before the budget check. |
| S8 | ✓ | `scanner.go:137-151` | No write-path access; only iterates and sums. |

**Summary:** 2 outright violations (S4, S5 — both partial-tick /
cross-contamination bugs at the error-return sites); 6 holding invariants
that lock current behavior against drift.

---

## Governance

1. **Spec location.** This file: `docs/invariants/scanner.md`. Changes
   require a human-signed commit. Agent-authored PRs that edit this file
   without explicit human direction must be rejected.
2. **Test location.** `cli/internal/scanner/scanner_invariants_prop_test.go`.
   Every property test must carry a `// Invariant SN: <Name>` comment
   linking it to the spec entry above.
3. **Protected surfaces.** Already covered by the existing
   `.claude/rules/protected-surfaces.md` glob patterns — no edit needed.
4. **CI enforcement.** The property tests run under the existing `go test
   ./...` path in CI. **S4 and S5 property tests are expected to fail**
   against current code. Per the spec's approved decision, they remain
   un-skipped — the red CI is the enforcement mechanism until `Scan` is
   refactored to collect errors rather than abort.

**Amendment procedure:** an invariant may only be relaxed via a PR that (a)
edits this document, (b) is authored or signed by a human, and (c) includes
rationale tied to a real constraint (not "the test was failing"). Agents may
propose amendments but may not merge them.
