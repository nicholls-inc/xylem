# `cli/internal/queue/reference` — naive reference queue

This package exists for one reason: **differential testing**. It is a naive,
in-memory, O(n) twin of `cli/internal/queue` that transcribes every documented
queue invariant line-for-line. The real queue is tested against it under
`pgregory.net/rapid`-generated op sequences.

The pattern is due to de Moura (2026): *an inefficient program that is
obviously correct can serve as its own specification.* See
`docs/research/literature-review.md` §de Moura and
`docs/assurance/immediate/03-naive-reference-differential-test.md` for context.

## What the reference models

Everything observable from the *spec* of the real queue
(`docs/invariants/queue.md`):

- **I1a** — Ref-dedup on `Enqueue` (active-state collision ⇒ `(false, nil)`).
- **I9** — Unique `ID` (`Enqueue` rejects duplicates with `ErrDuplicateID`).
- **I7** — State machine: every transition validated against the same
  `validTransitions` table the real queue uses.
- **I2** — Terminal-field immutability (via `Cancel`/`Update` paths).
- **I3** — `failed → pending` retry reset semantics.
- `FindByID` / `FindLatestByRef` tail-scan semantics (latest record wins).

## What the reference does **not** model

- **Durability** (I5a/I5b) — no JSONL file, no `fsync`, no crash recovery.
  The reference lives in RAM and vanishes with the process.
- **Concurrency** (I6) — single-threaded only. The differential test
  serializes ops. Race-condition coverage is owned by the real queue's
  `go test -race` runs and the invariant property tests.
- **Compaction** (I11) — `Compact`, `CompactDryRun`, `CompactOlderThan` are
  out of scope here; they are covered by `queue_prop_test.go`.
- **`ReplaceAll`** — a privileged op deliberately excluded from the generator.
- **Timestamps** — the reference does not set `StartedAt`, `EndedAt`, or
  `WaitingSince`. The differential test's `normalize` function strips those
  fields from both sides before comparing. Timestamp monotonicity (I4) is
  separately pinned by `queue_invariants_prop_test.go`.
- **DTU event emission** — no observability side-effects.

## How to update the reference

If an invariant changes (in `docs/invariants/queue.md`, with human sign-off),
the reference must be updated **before** the real queue, so the differential
test catches implementation drift. Any change here:

1. Cross-check against the invariant doc line-by-line in review.
2. Keep the file under ~200 lines. If it grows past that, the queue's
   interface has widened and should be split before the reference is
   expanded. (See roadmap item 03 kill criteria.)
3. Do **not** mirror an implementation optimization in the real queue
   verbatim — the whole point is that the reference is a transcription of
   the *spec*, not the *code*. If the real queue introduces a fast path,
   the reference should stay slow-but-obvious.

## Running the tests

```bash
cd cli
go test ./internal/queue/reference/...
# or with more iterations
go test -rapid.checks=10000 ./internal/queue/reference/...
```

Rapid defaults to 100 iterations per property. The acceptance criterion in
roadmap item 03 is 10,000+ iterations passing without divergence.

## How to inject a deliberate bug (sanity check)

The differential test is only useful if it actually fails on real-queue bugs.
To sanity-check it, temporarily swap two lines in
`cli/internal/queue/queue.go` — for instance, make `Dequeue` pick the *last*
pending vessel instead of the first, or omit the `Error = ""` assignment on
the `StatePending` case of `resetPendingState`. `go test
./internal/queue/reference/...` should fail within a few iterations, and
rapid's shrinker should produce a minimal offending op sequence. **Revert
the bug before committing.**
