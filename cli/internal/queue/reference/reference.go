// Package reference is a naive, in-memory, O(n) reference implementation of
// the xylem vessel queue. It exists so the real queue in cli/internal/queue
// can be differential-tested against a "stupidly correct" twin.
//
// The principle (de Moura 2026): an inefficient program that is obviously
// correct can serve as its own specification. Every method here is a line-for-
// line transcription of the behaviour the real queue is documented to provide
// (see docs/invariants/queue.md, cli/internal/queue/queue.go).
//
// What this reference MODELS (identically to the real queue):
//   - Ref-dedup on Enqueue (I1a): active-ref collision → (false, nil).
//   - ID-uniqueness on Enqueue (I9): ID collision → ErrDuplicateID.
//   - State machine (I7): transitions validated against validTransitions.
//   - Retry reset (I3): failed→pending clears running-episode fields.
//   - Terminal immutability (I2): UpdateVessel rejects protected-field mutation
//     on sealed terminal vessels.
//   - FindByID / FindLatestByRef: scan from the tail so the latest record wins.
//
// What this reference DOES NOT MODEL (intentionally):
//   - Durability (I5a, I5b): no JSONL file, no fsync, no crash recovery.
//   - Concurrency (I6): single-threaded only; differential tests serialize ops.
//   - Compaction (I11): Compact and CompactOlderThan are not implemented.
//   - Timestamps: callers must strip StartedAt/EndedAt/WaitingSince before
//     comparing against the real queue. The reference leaves timestamp fields
//     untouched.
//   - DTU event emission: no observability side-effects.
//
// Keep this file under 200 lines. If the interface grows past that, the real
// queue's interface is too wide and should be split before the reference is
// expanded.
package reference

import (
	"fmt"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// Queue is the naive reference. It stores vessels in append order in a slice
// and scans linearly for every operation.
type Queue struct {
	vessels []queue.Vessel
}

// New returns an empty reference queue.
func New() *Queue { return &Queue{} }

// Enqueue mirrors queue.Queue.Enqueue. Ref-dedup fires first (silent no-op),
// then ID-dedup (ErrDuplicateID), then append.
func (q *Queue) Enqueue(v queue.Vessel) (bool, error) {
	v.NormalizeWorkflowClass()
	if v.Ref != "" {
		for _, existing := range q.vessels {
			if existing.Ref == v.Ref && isActive(existing.State) {
				return false, nil
			}
		}
	}
	for _, existing := range q.vessels {
		if existing.ID == v.ID {
			return false, queue.ErrDuplicateID
		}
	}
	q.vessels = append(q.vessels, v)
	return true, nil
}

// Dequeue picks the first pending vessel, transitions it to running, clears
// Error. Returns (nil, nil) if no pending vessel exists.
func (q *Queue) Dequeue() (*queue.Vessel, error) {
	for i := range q.vessels {
		if q.vessels[i].State != queue.StatePending {
			continue
		}
		q.vessels[i].NormalizeWorkflowClass()
		q.vessels[i].State = queue.StateRunning
		q.vessels[i].Error = ""
		v := q.vessels[i]
		return &v, nil
	}
	return nil, nil
}

// Update transitions a vessel by ID (matching the latest record with that ID).
func (q *Queue) Update(id string, state queue.VesselState, errMsg string) error {
	for i := len(q.vessels) - 1; i >= 0; i-- {
		if q.vessels[i].ID != id {
			continue
		}
		prev := q.vessels[i].State
		allowed, known := validTransitions[prev]
		if !known {
			return fmt.Errorf("%w: unknown current state %s", queue.ErrInvalidTransition, prev)
		}
		if !allowed[state] {
			return fmt.Errorf("%w: cannot move %s→%s", queue.ErrInvalidTransition, prev, state)
		}
		q.vessels[i].State = state
		switch state {
		case queue.StatePending:
			resetPendingState(&q.vessels[i], prev)
		case queue.StateRunning:
			q.vessels[i].Error = ""
		case queue.StateFailed, queue.StateTimedOut:
			q.vessels[i].Error = errMsg
		case queue.StateCompleted, queue.StateCancelled, queue.StateWaiting:
			q.vessels[i].Error = ""
		}
		return nil
	}
	return fmt.Errorf("vessel %s not found", id)
}

// Cancel transitions a vessel by ID to cancelled, if the current state permits.
func (q *Queue) Cancel(id string) error {
	for i := len(q.vessels) - 1; i >= 0; i-- {
		if q.vessels[i].ID != id {
			continue
		}
		allowed, known := validTransitions[q.vessels[i].State]
		if !known || !allowed[queue.StateCancelled] {
			return fmt.Errorf("cannot cancel vessel %s in state %s", id, q.vessels[i].State)
		}
		q.vessels[i].State = queue.StateCancelled
		q.vessels[i].Error = ""
		return nil
	}
	return fmt.Errorf("vessel %s not found", id)
}

// List returns a copy of the vessel slice in append order.
func (q *Queue) List() []queue.Vessel {
	out := make([]queue.Vessel, len(q.vessels))
	copy(out, q.vessels)
	return out
}

// ListByState returns vessels currently in the given state.
func (q *Queue) ListByState(state queue.VesselState) []queue.Vessel {
	out := make([]queue.Vessel, 0)
	for _, v := range q.vessels {
		if v.State == state {
			out = append(out, v)
		}
	}
	return out
}

// FindByID returns the latest vessel with the given ID.
func (q *Queue) FindByID(id string) (*queue.Vessel, error) {
	for i := len(q.vessels) - 1; i >= 0; i-- {
		if q.vessels[i].ID == id {
			v := q.vessels[i]
			return &v, nil
		}
	}
	return nil, fmt.Errorf("vessel %s not found", id)
}

// FindLatestByRef returns the latest vessel with the given Ref.
func (q *Queue) FindLatestByRef(ref string) (*queue.Vessel, error) {
	for i := len(q.vessels) - 1; i >= 0; i-- {
		if q.vessels[i].Ref == ref {
			v := q.vessels[i]
			return &v, nil
		}
	}
	return nil, fmt.Errorf("vessel with ref %s not found", ref)
}

// HasRef reports whether any vessel with this Ref is in an active state
// ({pending, running, waiting}).
func (q *Queue) HasRef(ref string) bool {
	for _, v := range q.vessels {
		if v.Ref == ref && isActive(v.State) {
			return true
		}
	}
	return false
}

// HasRefAny reports whether any vessel with this Ref exists, regardless of state.
func (q *Queue) HasRefAny(ref string) bool {
	for _, v := range q.vessels {
		if v.Ref == ref {
			return true
		}
	}
	return false
}

// Size returns the total number of records. Convenience for tests.
func (q *Queue) Size() int { return len(q.vessels) }

// -----------------------------------------------------------------------------
// Private helpers — direct transcription of the spec semantics.
// -----------------------------------------------------------------------------

var validTransitions = map[queue.VesselState]map[queue.VesselState]bool{
	queue.StatePending: {
		queue.StateRunning:   true,
		queue.StateCancelled: true,
	},
	queue.StateRunning: {
		queue.StatePending:   true,
		queue.StateCompleted: true,
		queue.StateFailed:    true,
		queue.StateCancelled: true,
		queue.StateWaiting:   true,
		queue.StateTimedOut:  true,
	},
	queue.StateWaiting: {
		queue.StatePending:   true,
		queue.StateTimedOut:  true,
		queue.StateCancelled: true,
	},
	queue.StateFailed: {
		queue.StatePending: true,
	},
	queue.StateCompleted: {},
	queue.StateCancelled: {},
	queue.StateTimedOut:  {},
}

func isActive(s queue.VesselState) bool {
	return s == queue.StatePending || s == queue.StateRunning || s == queue.StateWaiting
}

func resetPendingState(v *queue.Vessel, previousState queue.VesselState) {
	v.StartedAt = nil
	v.EndedAt = nil
	v.Error = ""
	v.GateRetries = 0
	v.WaitingSince = nil
	v.WaitingFor = ""
	v.FailedPhase = ""
	v.GateOutput = ""
	switch previousState {
	case queue.StateFailed, queue.StateRunning:
		// Retry and orphan reconcile both restart the workflow.
		v.CurrentPhase = 0
		v.PhaseOutputs = nil
		v.WorktreePath = ""
	case queue.StateWaiting:
		// Label-gate resume keeps CurrentPhase, PhaseOutputs, WorktreePath.
	}
}

// NormalizeWorkflowClass trims whitespace from WorkflowClass and falls back to
// trimmed Workflow when class is empty. Exported so the differential test can
// normalize vessels it constructs to mirror Enqueue's own normalization.
func NormalizeWorkflowClass(v *queue.Vessel) {
	v.NormalizeWorkflowClass()
	// defensive: trimmed Workflow when class is empty (matches queue.go)
	if strings.TrimSpace(v.WorkflowClass) == "" {
		v.WorkflowClass = strings.TrimSpace(v.Workflow)
	}
}
