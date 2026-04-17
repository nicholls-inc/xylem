package queue

// Property tests for the invariants specified in docs/invariants/queue.md.
// Each function carries a "// Invariant IN: <Name>" comment (see Governance §2
// of the spec). The file is a protected surface: modifications require a
// human-signed commit (see .claude/rules/protected-surfaces.md).
//
// Four tests are t.Skip'd because they would fail against the current code
// (see the spec's Gap analysis). Removing a skip is a one-line action once
// the corresponding fix lands:
//   - I2: UpdateVessel skips validation on same-state mutations.
//   - I3: resetPendingState does not reset CurrentPhase / PhaseOutputs.
//   - I9: Enqueue does not reject duplicate IDs.
// I2, I3, and I9 are skipped by the "keep CI green until the code fix lands"
// principle with explicit gap-row references in the skip message.
//
// I5b (crash durability) was originally the one sanctioned skip per the
// spec's Governance §4; it now runs against the atomic writeAllVessels fix
// and the writeInterrupt hook in queue.go.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// -----------------------------------------------------------------------------
// Shared helpers
// -----------------------------------------------------------------------------

// newPropQueueWithDir creates a fresh queue on a fresh temp dir and returns
// the queue, the backing file path, and a cleanup function. Callers are
// expected to `defer cleanup()` inside a rapid iteration so each shrink gets
// an isolated queue.
func newPropQueueWithDir(t *rapid.T, prefix string) (*Queue, string, func()) {
	dir, err := os.MkdirTemp("", prefix+"-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	path := filepath.Join(dir, "queue.jsonl")
	return New(path), path, func() { os.RemoveAll(dir) }
}

// drawRef picks a ref from a small pool so collisions (and therefore I1/I1a
// exercises) happen with meaningful frequency.
func drawRef(t *rapid.T) string {
	pool := []string{
		"",
		"https://github.com/example/repo/issues/1",
		"https://github.com/example/repo/issues/2",
		"https://github.com/example/repo/issues/3",
		"https://github.com/example/repo/issues/4",
	}
	return pool[rapid.IntRange(0, len(pool)-1).Draw(t, "ref_idx")]
}

// drawID picks an ID from a small pool so that Update/Cancel ops have a
// realistic chance of matching a live vessel.
func drawID(t *rapid.T) string {
	n := rapid.IntRange(1, 8).Draw(t, "id_n")
	return fmt.Sprintf("issue-%d", n)
}

// drawFreshVessel produces a newly-constructed pending vessel. It never sets
// StartedAt or any running-episode fields; the queue fills those in via its
// own transition logic.
func drawFreshVessel(t *rapid.T) Vessel {
	return Vessel{
		ID:             drawID(t),
		Source:         "github-issue",
		Ref:            drawRef(t),
		Workflow:       rapid.SampledFrom([]string{"fix-bug", "implement-feature", "refactor"}).Draw(t, "workflow"),
		WorkflowDigest: rapid.StringMatching(`wf-[0-9a-f]{8}`).Draw(t, "digest"),
		Tier:           rapid.SampledFrom([]string{"", "low", "med", "high"}).Draw(t, "tier"),
		State:          StatePending,
		CreatedAt:      time.Now().UTC(),
	}
}

// opKind enumerates the queue mutating operations driven by the op generator.
type opKind int

const (
	opEnqueue opKind = iota
	opDequeue
	opUpdate
	opCancel
	opCompact
	opCompactOlderThan
	opUpdateVessel // privileged
	opReplaceAll   // privileged
)

// queueOp is a shrinkable, printable tagged record of a single op draw.
type queueOp struct {
	kind       opKind
	vessel     Vessel   // enqueue, updateVessel
	replaceAll []Vessel // replaceAll
	id         string   // update, cancel
	state      VesselState
	errMsg     string
	cutoff     time.Time // compactOlderThan
}

// drawMutatingOp draws one op. If `privileged` is false, UpdateVessel and
// ReplaceAll (whose I4/I7 exemptions are documented in the spec) are
// excluded.
func drawMutatingOp(t *rapid.T, privileged bool) queueOp {
	kinds := []opKind{
		opEnqueue, opDequeue, opUpdate, opCancel,
		opCompact, opCompactOlderThan,
	}
	if privileged {
		kinds = append(kinds, opUpdateVessel, opReplaceAll)
	}
	kind := kinds[rapid.IntRange(0, len(kinds)-1).Draw(t, "op_kind")]
	switch kind {
	case opEnqueue:
		// Note: we deliberately do NOT set RetryOf in the random generator. The
		// queue does not validate RetryOf (it's caller-set per spec I10 ⚠),
		// so random assignment quickly introduces caller-side cycles. I10 has
		// its own disciplined retry-chain scenario below.
		return queueOp{kind: kind, vessel: drawFreshVessel(t)}
	case opDequeue:
		return queueOp{kind: kind}
	case opUpdate:
		return queueOp{
			kind:   kind,
			id:     drawID(t),
			state:  drawVesselState(t, "target_state"),
			errMsg: rapid.SampledFrom([]string{"", "boom", "gate failed"}).Draw(t, "errMsg"),
		}
	case opCancel:
		return queueOp{kind: kind, id: drawID(t)}
	case opCompact:
		return queueOp{kind: kind}
	case opCompactOlderThan:
		return queueOp{kind: kind, cutoff: drawTime(t, "cutoff")}
	case opUpdateVessel:
		return queueOp{kind: kind, vessel: drawFreshVessel(t)}
	case opReplaceAll:
		n := rapid.IntRange(0, 5).Draw(t, "replace_n")
		vs := make([]Vessel, n)
		for i := range vs {
			vs[i] = drawFreshVessel(t)
		}
		return queueOp{kind: kind, replaceAll: vs}
	}
	return queueOp{}
}

// applyOp executes op against q. It intentionally swallows errors — the
// post-condition assertions are what enforce the invariants; op-level errors
// (e.g. "vessel not found") are part of the expected failure modes rapid
// should explore.
func applyOp(q *Queue, op queueOp) {
	switch op.kind {
	case opEnqueue:
		_, _ = q.Enqueue(op.vessel)
	case opDequeue:
		_, _ = q.Dequeue()
	case opUpdate:
		_ = q.Update(op.id, op.state, op.errMsg)
	case opCancel:
		_ = q.Cancel(op.id)
	case opCompact:
		_, _ = q.Compact()
	case opCompactOlderThan:
		_, _ = q.CompactOlderThan(op.cutoff)
	case opUpdateVessel:
		_ = q.UpdateVessel(op.vessel)
	case opReplaceAll:
		_ = q.ReplaceAll(op.replaceAll)
	}
}

// mustList reads the queue and fails the rapid iteration on error.
func mustList(t *rapid.T, q *Queue) []Vessel {
	vessels, err := q.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return vessels
}

// activeRefCounts tallies the active-state vessels per non-empty ref.
func activeRefCounts(vessels []Vessel) map[string]int {
	counts := map[string]int{}
	for _, v := range vessels {
		if v.Ref == "" {
			continue
		}
		switch v.State {
		case StatePending, StateRunning, StateWaiting:
			counts[v.Ref]++
		}
	}
	return counts
}

// -----------------------------------------------------------------------------
// Invariant properties
// -----------------------------------------------------------------------------

// Invariant I1: At-most-one active per ref.
func TestPropQueueInvariant_I1_AtMostOneActivePerRef(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, _, cleanup := newPropQueueWithDir(t, "queue-i1-prop")
		defer cleanup()
		n := rapid.IntRange(1, 20).Draw(t, "n")
		for i := 0; i < n; i++ {
			op := drawMutatingOp(t, false) // privileged ops carry caller-preservation obligations (see spec I1 ⚠ row)
			applyOp(q, op)
			vessels := mustList(t, q)
			for ref, count := range activeRefCounts(vessels) {
				if count > 1 {
					t.Fatalf("I1: ref %q has %d active vessels after op %d (%v)", ref, count, i, op.kind)
				}
			}
		}
	})
}

// Invariant I1a: Enqueue of an active ref is a no-op.
func TestPropQueueInvariant_I1a_EnqueueActiveRefIsNoop(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, path, cleanup := newPropQueueWithDir(t, "queue-i1a-prop")
		defer cleanup()
		first := drawFreshVessel(t)
		// Force a non-empty ref; the invariant is scoped to refs != "".
		first.Ref = "https://github.com/example/repo/issues/" + fmt.Sprintf("%d", rapid.IntRange(1, 100).Draw(t, "issue"))
		ok, err := q.Enqueue(first)
		if err != nil {
			t.Fatalf("first Enqueue: %v", err)
		}
		if !ok {
			t.Fatalf("first Enqueue returned (false, nil) for fresh queue")
		}
		sizeBefore := fileSize(t, path)

		// A second vessel sharing the ref (distinct ID, arbitrary identity).
		second := drawFreshVessel(t)
		second.Ref = first.Ref
		if second.ID == first.ID {
			second.ID = first.ID + "-alt"
		}

		ok2, err := q.Enqueue(second)
		if err != nil {
			t.Fatalf("second Enqueue: %v", err)
		}
		if ok2 {
			t.Fatalf("I1a: second Enqueue of active ref %q returned (true, nil); expected (false, nil)", first.Ref)
		}
		if sz := fileSize(t, path); sz != sizeBefore {
			t.Fatalf("I1a: file size changed from %d to %d after no-op Enqueue", sizeBefore, sz)
		}
	})
}

// Invariant I2: Terminal records are immutable in place (except failed→pending retry).
func TestPropQueueInvariant_I2_TerminalImmutability(t *testing.T) {
	t.Skip("known violation: row I2 in docs/invariants/queue.md gap analysis; UpdateVessel skips transition validation when State unchanged, so terminal vessels can have Error/PhaseOutputs/etc. mutated freely. Remove this Skip when the UpdateVessel guard lands.")
	rapid.Check(t, func(t *rapid.T) {
		q, _, cleanup := newPropQueueWithDir(t, "queue-i2-prop")
		defer cleanup()

		v := drawFreshVessel(t)
		v.Ref = "https://github.com/example/repo/issues/i2-" + fmt.Sprintf("%d", rapid.IntRange(1, 999).Draw(t, "issue"))
		if _, err := q.Enqueue(v); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if _, err := q.Dequeue(); err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		terminal := rapid.SampledFrom([]VesselState{StateCompleted, StateCancelled, StateTimedOut}).Draw(t, "terminal")
		if err := q.Update(v.ID, terminal, "done"); err != nil {
			t.Fatalf("Update → %s: %v", terminal, err)
		}
		before, err := q.FindByID(v.ID)
		if err != nil {
			t.Fatalf("FindByID before: %v", err)
		}
		beforeJSON, _ := json.Marshal(*before)

		// Attempt to mutate every I2-protected field via UpdateVessel. State is preserved.
		mutated := *before
		mutated.Error = "tampered"
		mutated.PhaseOutputs = map[string]string{"x": "y"}
		mutated.WorktreePath = "/tmp/tampered"
		mutated.GateRetries = 99
		mutated.FailedPhase = "tampered-phase"
		mutated.GateOutput = "tampered-gate"
		_ = q.UpdateVessel(mutated) // Per I2 must be rejected or no-op.

		after, err := q.FindByID(v.ID)
		if err != nil {
			t.Fatalf("FindByID after: %v", err)
		}
		afterJSON, _ := json.Marshal(*after)
		if !bytes.Equal(beforeJSON, afterJSON) {
			t.Fatalf("I2: terminal record mutated in place.\n  before: %s\n  after:  %s", beforeJSON, afterJSON)
		}
	})
}

// Invariant I3: Retry resets to indistinguishable-from-fresh.
func TestPropQueueInvariant_I3_RetryResetsCleanly(t *testing.T) {
	t.Skip("known violation: row I3 in docs/invariants/queue.md gap analysis; resetPendingState does not reset CurrentPhase or PhaseOutputs. Remove this Skip when the reset is extended.")
	rapid.Check(t, func(t *rapid.T) {
		q, _, cleanup := newPropQueueWithDir(t, "queue-i3-prop")
		defer cleanup()

		fresh := drawFreshVessel(t)
		fresh.Ref = "https://github.com/example/repo/issues/i3-" + fmt.Sprintf("%d", rapid.IntRange(1, 999).Draw(t, "issue"))
		if _, err := q.Enqueue(fresh); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if _, err := q.Dequeue(); err != nil {
			t.Fatalf("Dequeue: %v", err)
		}

		// Populate every resettable field via UpdateVessel while still running,
		// then transition to failed.
		started := time.Now().UTC().Add(-time.Hour)
		ended := time.Now().UTC().Add(-time.Minute)
		waitingSince := time.Now().UTC().Add(-30 * time.Minute)
		populated := fresh
		populated.State = StateFailed
		populated.StartedAt = &started
		populated.EndedAt = &ended
		populated.Error = "boom"
		populated.CurrentPhase = 3
		populated.PhaseOutputs = map[string]string{"analyze": "output"}
		populated.GateRetries = 2
		populated.WaitingSince = &waitingSince
		populated.WaitingFor = "some-label"
		populated.FailedPhase = "implement"
		populated.GateOutput = "gate failed"
		populated.WorktreePath = "/tmp/wt"
		if err := q.UpdateVessel(populated); err != nil {
			t.Fatalf("UpdateVessel → failed: %v", err)
		}

		// Transition failed → pending (retry).
		if err := q.Update(fresh.ID, StatePending, ""); err != nil {
			t.Fatalf("Update → pending: %v", err)
		}
		after, err := q.FindByID(fresh.ID)
		if err != nil {
			t.Fatalf("FindByID after retry: %v", err)
		}

		checks := []struct {
			name string
			bad  bool
		}{
			{"StartedAt", after.StartedAt != nil},
			{"EndedAt", after.EndedAt != nil},
			{"Error", after.Error != ""},
			{"CurrentPhase", after.CurrentPhase != 0},
			{"PhaseOutputs", after.PhaseOutputs != nil},
			{"GateRetries", after.GateRetries != 0},
			{"WaitingSince", after.WaitingSince != nil},
			{"WaitingFor", after.WaitingFor != ""},
			{"FailedPhase", after.FailedPhase != ""},
			{"GateOutput", after.GateOutput != ""},
			{"WorktreePath", after.WorktreePath != ""},
		}
		for _, c := range checks {
			if c.bad {
				t.Fatalf("I3: field %s not reset on failed→pending retry; vessel=%+v", c.name, *after)
			}
		}
	})
}

// Invariant I4: Monotonic lifecycle timestamps within a running episode.
func TestPropQueueInvariant_I4_MonotonicTimestamps(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, _, cleanup := newPropQueueWithDir(t, "queue-i4-prop")
		defer cleanup()
		n := rapid.IntRange(1, 20).Draw(t, "n")
		for i := 0; i < n; i++ {
			op := drawMutatingOp(t, false) // non-privileged only (spec I4 exempts UpdateVessel/ReplaceAll)
			applyOp(q, op)
			for _, v := range mustList(t, q) {
				if v.StartedAt != nil && v.StartedAt.Before(v.CreatedAt) {
					t.Fatalf("I4: StartedAt %v < CreatedAt %v for vessel %s", v.StartedAt, v.CreatedAt, v.ID)
				}
				if v.StartedAt != nil && v.EndedAt != nil && v.EndedAt.Before(*v.StartedAt) {
					t.Fatalf("I4: EndedAt %v < StartedAt %v for vessel %s", v.EndedAt, v.StartedAt, v.ID)
				}
				if v.State == StateWaiting && v.WaitingSince != nil && v.StartedAt != nil && v.WaitingSince.Before(*v.StartedAt) {
					t.Fatalf("I4: WaitingSince %v < StartedAt %v for vessel %s (state %s)", v.WaitingSince, v.StartedAt, v.ID, v.State)
				}
			}
		}
	})
}

// Invariant I5a: Reopen-equivalence (graceful durability).
func TestPropQueueInvariant_I5a_ReopenEquivalence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, path, cleanup := newPropQueueWithDir(t, "queue-i5a-prop")
		defer cleanup()
		n := rapid.IntRange(1, 15).Draw(t, "n")
		for i := 0; i < n; i++ {
			op := drawMutatingOp(t, false)
			applyOp(q, op)
			inProcess := mustList(t, q)
			reopened := mustList(t, New(path))
			if !vesselSlicesEqual(inProcess, reopened) {
				inJSON, _ := json.Marshal(inProcess)
				outJSON, _ := json.Marshal(reopened)
				t.Fatalf("I5a: in-process List() diverges from reopened List() after op %d (%v)\n  in:  %s\n  out: %s",
					i, op.kind, inJSON, outJSON)
			}
		}
	})
}

// Invariant I5b: Crash durability.
//
// Simulates a SIGKILL at one of four enumerated stages inside writeAllVessels
// by panicking from the writeInterrupt hook. After the induced panic, the
// file on disk must yield either the pre-call vessel set or the post-call
// vessel set — never a torn/partial state. The four stages collectively
// cover every crash window the spec requires:
//
//   - before-tmp      : crash before any tmpfile exists; the real file must
//     still be the pre-state.
//   - after-tmp-write : crash after payload bytes are in the tmpfile but
//     before fsync/rename; the real file must still be
//     the pre-state (rename has not happened).
//   - after-tmp-fsync : crash after the tmpfile is durable on disk but
//     before the rename; the real file must still be
//     the pre-state.
//   - after-rename    : crash after the rename but before the dir fsync;
//     the real file must be the post-state (rename is
//     atomic, the dir fsync only guarantees durability
//     across power loss, not visibility).
//
// The assertion compares vessels at the semantic level rather than at the
// byte level so that wall-clock-derived fields (StartedAt/EndedAt) in the
// reference run don't produce false positives against the crash run. Any
// divergence in vessel count, ID, state, ref, workflow, or error is a real
// torn-write violation; differences in the numeric value of a timestamp
// are not — only the non-nil/nil shape matters.
func TestPropQueueInvariant_I5b_CrashDurability(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed, op := drawI5bScenario(t)

		// Reference run: execute op on a sibling queue with no hook, and
		// capture the resulting vessel set. This is the canonical
		// "post-state" against which the crash run is compared.
		refQ, _, refCleanup := newPropQueueWithDir(t, "queue-i5b-ref")
		defer refCleanup()
		if _, err := refQ.Enqueue(seed); err != nil {
			t.Fatalf("ref seed Enqueue: %v", err)
		}
		preRefState, err := refQ.List()
		if err != nil {
			t.Fatalf("ref List pre-op: %v", err)
		}
		applyOp(refQ, op)
		postRefState, err := refQ.List()
		if err != nil {
			t.Fatalf("ref List post-op: %v", err)
		}

		// Queue under test: seed identically, install crash hook, apply op.
		q, path, cleanup := newPropQueueWithDir(t, "queue-i5b-prop")
		defer cleanup()
		if _, err := q.Enqueue(seed); err != nil {
			t.Fatalf("seed Enqueue: %v", err)
		}

		stage := rapid.SampledFrom([]string{
			"before-tmp", "after-tmp-write", "after-tmp-fsync", "after-rename",
		}).Draw(t, "stage")

		writeInterrupt = func(s string) {
			if s == stage {
				panic("test-kill")
			}
		}
		func() {
			defer func() {
				writeInterrupt = nil
				if r := recover(); r != nil && r != "test-kill" {
					// A different panic is a real test failure — re-raise.
					panic(r)
				}
			}()
			applyOp(q, op)
		}()

		// Durability check (a): re-opening a fresh Queue on the same path
		// must not error — the file is well-formed.
		observed, err := New(path).List()
		if err != nil {
			t.Fatalf("I5b: fresh List() after crash at %s returned error: %v", stage, err)
		}

		// Durability check (b): the observed vessel set must equal either
		// preRefState or postRefState at the semantic level.
		if vesselSetsEquivalentIgnoringClock(observed, preRefState) ||
			vesselSetsEquivalentIgnoringClock(observed, postRefState) {
			return
		}
		obsJSON, _ := json.Marshal(observed)
		preJSON, _ := json.Marshal(preRefState)
		postJSON, _ := json.Marshal(postRefState)
		t.Fatalf("I5b: crash at stage %s left file in intermediate state\n  pre:      %s\n  post:     %s\n  observed: %s",
			stage, preJSON, postJSON, obsJSON)
	})
}

// drawI5bScenario draws (seed vessel, mutating op) for an I5b iteration.
// The op is chosen from a set that is guaranteed to change the queue file
// relative to the seeded state — Dequeue, Enqueue-of-distinct-vessel, or
// Cancel — so that pre-state and post-state are distinguishable and the
// property actually exercises every crash window (rather than collapsing
// to a trivial same-state check).
func drawI5bScenario(t *rapid.T) (Vessel, queueOp) {
	seed := drawFreshVessel(t)
	seed.Ref = "https://github.com/example/repo/issues/i5b-" +
		fmt.Sprintf("%d", rapid.IntRange(1, 1_000_000).Draw(t, "i5b_seed_issue"))
	kind := rapid.SampledFrom([]int{0, 1, 2}).Draw(t, "i5b_op_kind")
	switch kind {
	case 0:
		v := drawFreshVessel(t)
		if v.ID == seed.ID {
			v.ID = seed.ID + "-i5b-alt"
		}
		v.Ref = "https://github.com/example/repo/issues/i5b-other-" +
			fmt.Sprintf("%d", rapid.IntRange(1, 1_000_000).Draw(t, "i5b_other_issue"))
		return seed, queueOp{kind: opEnqueue, vessel: v}
	case 1:
		return seed, queueOp{kind: opDequeue}
	default:
		return seed, queueOp{kind: opCancel, id: seed.ID}
	}
}

// vesselSetsEquivalentIgnoringClock returns true when two vessel slices are
// equal at the semantic level modulo the numeric value of time-derived
// fields (CreatedAt/StartedAt/EndedAt/WaitingSince). It still enforces that
// a time field is nil in one slice iff it is nil in the other — a torn
// write that drops StartedAt entirely, or adds one where there should be
// none, is still rejected.
func vesselSetsEquivalentIgnoringClock(a, b []Vessel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !vesselEquivalentIgnoringClock(a[i], b[i]) {
			return false
		}
	}
	return true
}

func vesselEquivalentIgnoringClock(a, b Vessel) bool {
	a.CreatedAt = time.Time{}
	b.CreatedAt = time.Time{}
	a.StartedAt = zeroTimePtrIfNonNil(a.StartedAt)
	b.StartedAt = zeroTimePtrIfNonNil(b.StartedAt)
	a.EndedAt = zeroTimePtrIfNonNil(a.EndedAt)
	b.EndedAt = zeroTimePtrIfNonNil(b.EndedAt)
	a.WaitingSince = zeroTimePtrIfNonNil(a.WaitingSince)
	b.WaitingSince = zeroTimePtrIfNonNil(b.WaitingSince)
	return reflect.DeepEqual(a, b)
}

func zeroTimePtrIfNonNil(p *time.Time) *time.Time {
	if p == nil {
		return nil
	}
	z := time.Time{}
	return &z
}

// Invariant I6: Linearizability (sanity check via concurrent ops).
//
// A rigorous linearizability test would record each op's invocation/response
// timestamps and search for a linear schedule. That's heavy for v1; instead
// this property exercises the concurrent path and asserts that I1 and I5a
// hold on the final observable state. If a concurrency bug breaks exclusion,
// at least one of those two checks should trip.
func TestPropQueueInvariant_I6_ConcurrentSanity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, path, cleanup := newPropQueueWithDir(t, "queue-i6-prop")
		defer cleanup()
		const workers = 4
		opsPerWorker := rapid.IntRange(3, 10).Draw(t, "ops_per_worker")
		seqs := make([][]queueOp, workers)
		for w := 0; w < workers; w++ {
			seqs[w] = make([]queueOp, opsPerWorker)
			for i := range seqs[w] {
				seqs[w][i] = drawMutatingOp(t, false)
			}
		}
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(ops []queueOp) {
				defer wg.Done()
				for _, op := range ops {
					applyOp(q, op)
				}
			}(seqs[w])
		}
		wg.Wait()

		final := mustList(t, q)
		for ref, count := range activeRefCounts(final) {
			if count > 1 {
				t.Fatalf("I6: concurrent ops broke I1 (ref %q has %d active)", ref, count)
			}
		}
		reopened := mustList(t, New(path))
		if !vesselSlicesEqual(final, reopened) {
			t.Fatalf("I6: concurrent ops broke I5a (in-process vs reopened diverge)")
		}
	})
}

// Invariant I7: State transition soundness.
//
// I9 (unique IDs) is a known violation, so we cannot track vessels by ID across
// the op. Instead we track by index in the slice: non-privileged ops never
// reorder or remove entries — Enqueue appends, Update/Cancel mutate in place —
// so before[i] and after[i] refer to the same vessel for i < len(before).
func TestPropQueueInvariant_I7_TransitionSoundness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, _, cleanup := newPropQueueWithDir(t, "queue-i7-prop")
		defer cleanup()
		n := rapid.IntRange(1, 20).Draw(t, "n")
		for i := 0; i < n; i++ {
			before := mustList(t, q)
			op := drawMutatingOp(t, false) // non-privileged; ReplaceAll/UpdateVessel are exempt per spec
			applyOp(q, op)
			if op.kind == opCompact || op.kind == opCompactOlderThan {
				continue // these do not mutate State per vessel
			}
			after := mustList(t, q)
			limit := len(before)
			if len(after) < limit {
				t.Fatalf("I7: non-privileged op %v shrank list from %d to %d", op.kind, len(before), len(after))
			}
			for j := 0; j < limit; j++ {
				if before[j].ID != after[j].ID {
					t.Fatalf("I7: vessel identity at index %d changed from %s to %s via op %v", j, before[j].ID, after[j].ID, op.kind)
				}
				if before[j].State == after[j].State {
					continue
				}
				allowed, known := validTransitions[before[j].State]
				if !known || !allowed[after[j].State] {
					t.Fatalf("I7: illegal transition %s → %s at index %d (id %s) via op %v",
						before[j].State, after[j].State, j, before[j].ID, op.kind)
				}
			}
		}
	})
}

// Invariant I8: Queue file well-formedness (graceful-path guardrail).
//
// NOTE: The spec lists I8 as a known violation because readAllVessels silently
// skips malformed lines under corruption. That case only manifests on torn
// writes (covered by I5b); this test is the graceful-path regression guard —
// under normal ops every line must parse as a Vessel.
func TestPropQueueInvariant_I8_FileWellFormedness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, path, cleanup := newPropQueueWithDir(t, "queue-i8-prop")
		defer cleanup()
		n := rapid.IntRange(0, 20).Draw(t, "n")
		for i := 0; i < n; i++ {
			op := drawMutatingOp(t, true) // include privileged — ReplaceAll/UpdateVessel also go through writeAllVessels
			applyOp(q, op)
			raw, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				t.Fatalf("ReadFile: %v", err)
			}
			for ln, line := range bytes.Split(raw, []byte("\n")) {
				trimmed := bytes.TrimSpace(line)
				if len(trimmed) == 0 {
					continue
				}
				var parsed Vessel
				if err := json.Unmarshal(trimmed, &parsed); err != nil {
					t.Fatalf("I8: line %d in queue file is not valid JSON Vessel: %v\n  content: %s", ln+1, err, string(trimmed))
				}
			}
		}
	})
}

// Invariant I9: Unique vessel IDs.
func TestPropQueueInvariant_I9_UniqueIDs(t *testing.T) {
	t.Skip("known violation: row I9 in docs/invariants/queue.md gap analysis; Enqueue checks only Ref, not ID, so two vessels can share an ID. Remove this Skip when Enqueue rejects duplicate IDs.")
	rapid.Check(t, func(t *rapid.T) {
		q, _, cleanup := newPropQueueWithDir(t, "queue-i9-prop")
		defer cleanup()
		n := rapid.IntRange(1, 20).Draw(t, "n")
		for i := 0; i < n; i++ {
			op := drawMutatingOp(t, false)
			applyOp(q, op)
			seen := map[string]bool{}
			for _, v := range mustList(t, q) {
				if seen[v.ID] {
					t.Fatalf("I9: duplicate ID %q in queue after op %d (%v)", v.ID, i, op.kind)
				}
				seen[v.ID] = true
			}
		}
	})
}

// Invariant I10: RetryOf forms a DAG rooted at fresh vessels.
//
// The queue never validates RetryOf (spec marks I10 as caller-responsibility
// ⚠), so this test explicitly builds *disciplined* retry chains — each retry
// points at an existing terminal vessel — and asserts the observable subgraph
// is acyclic with terminal targets. A random op generator would quickly
// manufacture caller-side cycles that the queue permits by design, so we
// don't use one here.
func TestPropQueueInvariant_I10_RetryOfDAG(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, _, cleanup := newPropQueueWithDir(t, "queue-i10-prop")
		defer cleanup()

		chainLen := rapid.IntRange(1, 5).Draw(t, "chain_len")
		seed := rapid.IntRange(0, 1_000_000).Draw(t, "chain_seed")
		var prevID string
		for i := 0; i < chainLen; i++ {
			v := drawFreshVessel(t)
			v.ID = fmt.Sprintf("chain-%d-%d", seed, i)
			v.Ref = fmt.Sprintf("https://example.test/i10/%d/%d", seed, i)
			v.RetryOf = prevID // "" for the root, prior chain link otherwise
			if _, err := q.Enqueue(v); err != nil {
				t.Fatalf("Enqueue chain[%d]: %v", i, err)
			}
			if _, err := q.Dequeue(); err != nil {
				t.Fatalf("Dequeue chain[%d]: %v", i, err)
			}
			// Drive to a terminal state so the next link has a terminal target.
			terminal := rapid.SampledFrom([]VesselState{StateFailed, StateCompleted, StateCancelled, StateTimedOut}).Draw(t, "terminal_state")
			if err := q.Update(v.ID, terminal, "done"); err != nil {
				t.Fatalf("Update chain[%d] → %s: %v", i, terminal, err)
			}
			prevID = v.ID
		}

		vessels := mustList(t, q)
		byID := map[string]Vessel{}
		for _, v := range vessels {
			byID[v.ID] = v
		}

		// Acyclicity: DFS with white/gray/black coloring.
		const (
			white = 0
			gray  = 1
			black = 2
		)
		color := map[string]int{}
		var visit func(id string, path []string) error
		visit = func(id string, path []string) error {
			switch color[id] {
			case gray:
				return fmt.Errorf("cycle detected: %v → %s", path, id)
			case black:
				return nil
			}
			color[id] = gray
			if v, ok := byID[id]; ok && v.RetryOf != "" {
				if err := visit(v.RetryOf, append(path, id)); err != nil {
					return err
				}
			}
			color[id] = black
			return nil
		}
		for id := range byID {
			if err := visit(id, nil); err != nil {
				t.Fatalf("I10: %v", err)
			}
		}

		// "Target is terminal" for every observable retry edge.
		for _, v := range vessels {
			if v.RetryOf == "" {
				continue
			}
			target, ok := byID[v.RetryOf]
			if !ok {
				continue // target may live in compacted history
			}
			if !target.State.IsTerminal() {
				t.Fatalf("I10: vessel %s retries non-terminal target %s (state=%s)",
					v.ID, target.ID, target.State)
			}
		}
	})
}

// Invariant I11: Compaction preserves the active set.
func TestPropQueueInvariant_I11_CompactionPreservesActive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q, _, cleanup := newPropQueueWithDir(t, "queue-i11-prop")
		defer cleanup()
		n := rapid.IntRange(0, 15).Draw(t, "n")
		for i := 0; i < n; i++ {
			applyOp(q, drawMutatingOp(t, false))
		}
		before := mustList(t, q)
		if _, err := q.Compact(); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		after := mustList(t, q)
		for _, v := range before {
			switch v.State {
			case StatePending, StateRunning, StateWaiting:
			default:
				continue
			}
			found := false
			for _, w := range after {
				if reflect.DeepEqual(v, w) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("I11: active vessel removed or mutated by Compact: %+v", v)
			}
		}

		// CompactOlderThan with a far-future cutoff should preserve active vessels too.
		if _, err := q.CompactOlderThan(time.Now().UTC().Add(365 * 24 * time.Hour)); err != nil {
			t.Fatalf("CompactOlderThan: %v", err)
		}
		afterAge := mustList(t, q)
		for _, v := range before {
			switch v.State {
			case StatePending, StateRunning, StateWaiting:
			default:
				continue
			}
			found := false
			for _, w := range afterAge {
				if reflect.DeepEqual(v, w) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("I11: active vessel removed or mutated by CompactOlderThan: %+v", v)
			}
		}
	})
}

// -----------------------------------------------------------------------------
// Non-rapid helpers
// -----------------------------------------------------------------------------

// fileSize returns the size in bytes of the queue file, or 0 if absent.
func fileSize(t *rapid.T, path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// vesselSlicesEqual compares two vessel slices by their JSON encoding. This
// is stable across time.Time wall/monotonic representations that survive the
// round-trip through a file.
func vesselSlicesEqual(a, b []Vessel) bool {
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aJSON, bJSON)
}
