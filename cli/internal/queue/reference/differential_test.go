package reference_test

// Differential test between the real queue (cli/internal/queue) and the naive
// reference (cli/internal/queue/reference). Every rapid-generated op sequence
// is applied to both queues, and after each op the observable state is diffed.
// Any divergence is a real bug in one of the two — and the reference is the
// obviously-correct twin.
//
// Deliberately not using the queue package's internal drawMutatingOp. That
// generator short-circuits some cases via the real queue's own dedup (see
// roadmap item 03). This file defines its own generator.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/queue/reference"
)

// -----------------------------------------------------------------------------
// Generators. Narrow pools so IDs and Refs collide with meaningful frequency.
// -----------------------------------------------------------------------------

func drawID(t *rapid.T) string {
	n := rapid.IntRange(1, 6).Draw(t, "id_n")
	return fmt.Sprintf("issue-%d", n)
}

func drawRef(t *rapid.T) string {
	pool := []string{
		"",
		"https://github.com/example/repo/issues/1",
		"https://github.com/example/repo/issues/2",
		"https://github.com/example/repo/issues/3",
	}
	return pool[rapid.IntRange(0, len(pool)-1).Draw(t, "ref_idx")]
}

func drawState(t *rapid.T) queue.VesselState {
	states := []queue.VesselState{
		queue.StatePending, queue.StateRunning, queue.StateCompleted,
		queue.StateFailed, queue.StateCancelled, queue.StateTimedOut,
		queue.StateWaiting,
	}
	return states[rapid.IntRange(0, len(states)-1).Draw(t, "state")]
}

func drawVessel(t *rapid.T, createdAt time.Time) queue.Vessel {
	return queue.Vessel{
		ID:             drawID(t),
		Source:         "github-issue",
		Ref:            drawRef(t),
		Workflow:       rapid.SampledFrom([]string{"fix-bug", "implement-feature", "refactor"}).Draw(t, "workflow"),
		WorkflowDigest: rapid.StringMatching(`wf-[0-9a-f]{8}`).Draw(t, "digest"),
		Tier:           rapid.SampledFrom([]string{"", "low", "med", "high"}).Draw(t, "tier"),
		State:          queue.StatePending,
		CreatedAt:      createdAt,
	}
}

// opKind enumerates the mutating operations the differential driver issues.
type opKind int

const (
	opEnqueue opKind = iota
	opDequeue
	opUpdate
	opCancel
)

type op struct {
	kind   opKind
	vessel queue.Vessel
	id     string
	state  queue.VesselState
	errMsg string
}

func drawOp(t *rapid.T, createdAt time.Time) op {
	kind := opKind(rapid.IntRange(0, 3).Draw(t, "op_kind"))
	switch kind {
	case opEnqueue:
		return op{kind: kind, vessel: drawVessel(t, createdAt)}
	case opDequeue:
		return op{kind: kind}
	case opUpdate:
		return op{
			kind:   kind,
			id:     drawID(t),
			state:  drawState(t),
			errMsg: rapid.SampledFrom([]string{"", "boom"}).Draw(t, "errMsg"),
		}
	case opCancel:
		return op{kind: kind, id: drawID(t)}
	}
	panic("unreachable")
}

// -----------------------------------------------------------------------------
// Driver.
// -----------------------------------------------------------------------------

// applyReal runs the op against the real queue and returns a coarse outcome
// tag plus any sentinel-classifying error.
func applyReal(q *queue.Queue, o op) outcome {
	switch o.kind {
	case opEnqueue:
		ok, err := q.Enqueue(o.vessel)
		return outcome{enq: ok, err: err}
	case opDequeue:
		v, err := q.Dequeue()
		return outcome{dequeued: v != nil, err: err}
	case opUpdate:
		err := q.Update(o.id, o.state, o.errMsg)
		return outcome{err: err}
	case opCancel:
		err := q.Cancel(o.id)
		return outcome{err: err}
	}
	return outcome{}
}

func applyRef(q *reference.Queue, o op) outcome {
	switch o.kind {
	case opEnqueue:
		ok, err := q.Enqueue(o.vessel)
		return outcome{enq: ok, err: err}
	case opDequeue:
		v, err := q.Dequeue()
		return outcome{dequeued: v != nil, err: err}
	case opUpdate:
		err := q.Update(o.id, o.state, o.errMsg)
		return outcome{err: err}
	case opCancel:
		err := q.Cancel(o.id)
		return outcome{err: err}
	}
	return outcome{}
}

type outcome struct {
	enq      bool
	dequeued bool
	err      error
}

// outcomesAgree checks that both queues came to the same conclusion about
// whether to succeed or fail. For errors we compare sentinel identity when
// available; for free-form errors ("vessel not found", "cannot cancel …")
// we match on "both nil" vs "both non-nil".
func outcomesAgree(a, b outcome) (bool, string) {
	if a.enq != b.enq {
		return false, fmt.Sprintf("Enqueue ok mismatch: real=%v ref=%v", a.enq, b.enq)
	}
	if a.dequeued != b.dequeued {
		return false, fmt.Sprintf("Dequeue hit mismatch: real=%v ref=%v", a.dequeued, b.dequeued)
	}
	// Sentinel check: if either error wraps a known sentinel, both must.
	for _, sentinel := range []error{
		queue.ErrDuplicateID,
		queue.ErrInvalidTransition,
		queue.ErrTerminalImmutable,
	} {
		aMatch := a.err != nil && errors.Is(a.err, sentinel)
		bMatch := b.err != nil && errors.Is(b.err, sentinel)
		if aMatch != bMatch {
			return false, fmt.Sprintf("sentinel %v: real=%v ref=%v (real err=%v ref err=%v)",
				sentinel, aMatch, bMatch, a.err, b.err)
		}
	}
	// Fall-through: agree on nil-vs-non-nil.
	if (a.err == nil) != (b.err == nil) {
		return false, fmt.Sprintf("error presence mismatch: real=%v ref=%v", a.err, b.err)
	}
	return true, ""
}

// normalize strips clock-driven and caller-private fields so the real queue's
// internally-set timestamps do not cause spurious diffs. Preserves identity,
// state, and spec-mandated derived fields.
func normalize(vs []queue.Vessel) []queue.Vessel {
	out := make([]queue.Vessel, len(vs))
	for i, v := range vs {
		// Zero out every field the real queue sets via queueNow(); the
		// reference intentionally does not touch timestamps.
		v.StartedAt = nil
		v.EndedAt = nil
		v.WaitingSince = nil
		// CreatedAt is caller-provided in our ops; zero it to avoid jitter
		// between the two sides that may format time differently.
		v.CreatedAt = time.Time{}
		// PhaseOutputs nil vs empty-map: normalize both to nil.
		if len(v.PhaseOutputs) == 0 {
			v.PhaseOutputs = nil
		}
		// Meta: likewise.
		if len(v.Meta) == 0 {
			v.Meta = nil
		}
		out[i] = v
	}
	return out
}

// -----------------------------------------------------------------------------
// The test.
// -----------------------------------------------------------------------------

// TestProp_DifferentialAgainstReal is the headline property: after any
// sequence of Enqueue/Dequeue/Update/Cancel ops applied to both queues, the
// normalized List() outputs must match and both queues must agree on each
// op's success or failure.
func TestProp_DifferentialAgainstReal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "queue-diff-*")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)

		realQ := queue.New(filepath.Join(dir, "queue.jsonl"))
		refQ := reference.New()

		createdAt := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
		n := rapid.IntRange(1, 40).Draw(t, "n_ops")

		for step := 0; step < n; step++ {
			o := drawOp(t, createdAt)

			rOut := applyReal(realQ, o)
			fOut := applyRef(refQ, o)

			if agree, why := outcomesAgree(rOut, fOut); !agree {
				t.Fatalf("step %d op=%+v: outcome divergence: %s", step, o, why)
			}

			realList, err := realQ.List()
			if err != nil {
				t.Fatalf("step %d: realQ.List: %v", step, err)
			}
			refList := refQ.List()

			r := normalize(realList)
			f := normalize(refList)
			if !reflect.DeepEqual(r, f) {
				t.Fatalf("step %d op=%+v: List() divergence:\n  real: %+v\n   ref: %+v", step, o, r, f)
			}
		}
	})
}

// TestProp_DifferentialReadAccessors also checks the read-side accessors
// (FindByID, FindLatestByRef, HasRef, HasRefAny, ListByState) — they are
// observable surface and must also agree.
func TestProp_DifferentialReadAccessors(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "queue-diff-read-*")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)

		realQ := queue.New(filepath.Join(dir, "queue.jsonl"))
		refQ := reference.New()

		createdAt := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
		n := rapid.IntRange(1, 25).Draw(t, "n_ops")
		for step := 0; step < n; step++ {
			o := drawOp(t, createdAt)
			applyReal(realQ, o)
			applyRef(refQ, o)
		}

		// Probe read accessors with drawn IDs/refs/states.
		probeID := drawID(t)
		probeRef := drawRef(t)
		probeState := drawState(t)

		rByID, rErrID := realQ.FindByID(probeID)
		fByID, fErrID := refQ.FindByID(probeID)
		if (rErrID == nil) != (fErrID == nil) {
			t.Fatalf("FindByID %q error presence mismatch: real=%v ref=%v", probeID, rErrID, fErrID)
		}
		if rByID != nil && fByID != nil {
			rs := normalize([]queue.Vessel{*rByID})[0]
			fs := normalize([]queue.Vessel{*fByID})[0]
			if !reflect.DeepEqual(rs, fs) {
				t.Fatalf("FindByID %q divergence:\n  real: %+v\n   ref: %+v", probeID, rs, fs)
			}
		}

		rByRef, rErrRef := realQ.FindLatestByRef(probeRef)
		fByRef, fErrRef := refQ.FindLatestByRef(probeRef)
		if (rErrRef == nil) != (fErrRef == nil) {
			t.Fatalf("FindLatestByRef %q error presence mismatch: real=%v ref=%v", probeRef, rErrRef, fErrRef)
		}
		if rByRef != nil && fByRef != nil {
			rs := normalize([]queue.Vessel{*rByRef})[0]
			fs := normalize([]queue.Vessel{*fByRef})[0]
			if !reflect.DeepEqual(rs, fs) {
				t.Fatalf("FindLatestByRef %q divergence:\n  real: %+v\n   ref: %+v", probeRef, rs, fs)
			}
		}

		if realQ.HasRef(probeRef) != refQ.HasRef(probeRef) {
			t.Fatalf("HasRef %q mismatch: real=%v ref=%v", probeRef, realQ.HasRef(probeRef), refQ.HasRef(probeRef))
		}
		if realQ.HasRefAny(probeRef) != refQ.HasRefAny(probeRef) {
			t.Fatalf("HasRefAny %q mismatch", probeRef)
		}

		rByState, err := realQ.ListByState(probeState)
		if err != nil {
			t.Fatalf("ListByState %q real err: %v", probeState, err)
		}
		fByState := refQ.ListByState(probeState)
		if !reflect.DeepEqual(normalize(rByState), normalize(fByState)) {
			t.Fatalf("ListByState %q divergence:\n  real: %+v\n   ref: %+v", probeState, normalize(rByState), normalize(fByState))
		}
	})
}
