package queue

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/nicholls-inc/xylem/cli/internal/dtu"
)

// writeInterrupt is a test-only hook. When non-nil it is invoked at enumerated
// stages during writeAllVessels so property tests can simulate SIGKILL by
// panicking. It is always nil in production builds; the production call sites
// collapse to a single pointer comparison. See invariant I5b in
// docs/invariants/queue.md and the crash-durability harness in
// queue_invariants_prop_test.go.
var writeInterrupt func(stage string)

type VesselState string

const (
	StatePending   VesselState = "pending"
	StateRunning   VesselState = "running"
	StateCompleted VesselState = "completed"
	StateFailed    VesselState = "failed"
	StateCancelled VesselState = "cancelled"
	StateWaiting   VesselState = "waiting"
	StateTimedOut  VesselState = "timed_out"
)

// validTransitions defines the allowed state transitions. Each key is a current
// state and the value is the set of states it may transition to.
var validTransitions = map[VesselState]map[VesselState]bool{
	StatePending: {
		StateRunning:   true,
		StateCancelled: true,
	},
	StateRunning: {
		StatePending:   true, // daemon restart recovery requeues orphaned work
		StateCompleted: true,
		StateFailed:    true,
		StateCancelled: true,
		StateWaiting:   true, // label gate pauses vessel
		StateTimedOut:  true, // hung vessel timeout
	},
	StateWaiting: { // label gate pause state
		StatePending:   true, // label gate passed, resume via normal dequeue flow
		StateTimedOut:  true, // label gate timed out
		StateCancelled: true, // manually cancelled while waiting
	},
	StateFailed: {
		StatePending: true, // allow retry
	},
	// Terminal states: no transitions out of completed, cancelled, or timed_out.
	StateCompleted: {},
	StateCancelled: {},
	StateTimedOut:  {},
}

// ErrInvalidTransition is returned when a state transition is not allowed.
var ErrInvalidTransition = errors.New("invalid state transition")

// ErrDuplicateID is returned by Enqueue when the vessel's ID collides with any
// existing vessel in the queue. ID is the primary key (invariant I9); callers
// must supply unique IDs. Distinct from Ref collision, which is a silent no-op.
var ErrDuplicateID = errors.New("duplicate vessel ID")

// IsTerminal reports whether s is a terminal vessel state.
func (s VesselState) IsTerminal() bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled || s == StateTimedOut
}

type Vessel struct {
	ID             string            `json:"id"`
	Source         string            `json:"source"`
	Ref            string            `json:"ref,omitempty"`
	Workflow       string            `json:"workflow,omitempty"`
	WorkflowDigest string            `json:"workflow_digest,omitempty"`
	WorkflowClass  string            `json:"workflow_class,omitempty"`
	Tier           string            `json:"tier,omitempty"`
	Prompt         string            `json:"prompt,omitempty"`
	Meta           map[string]string `json:"meta,omitempty"`
	State          VesselState       `json:"state"`
	CreatedAt      time.Time         `json:"created_at"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	EndedAt        *time.Time        `json:"ended_at,omitempty"`
	Error          string            `json:"error,omitempty"`

	// v2 phase-based execution fields
	CurrentPhase int               `json:"current_phase,omitempty"`
	PhaseOutputs map[string]string `json:"phase_outputs,omitempty"`
	GateRetries  int               `json:"gate_retries,omitempty"`
	WaitingSince *time.Time        `json:"waiting_since,omitempty"`
	WaitingFor   string            `json:"waiting_for,omitempty"`
	WorktreePath string            `json:"worktree_path,omitempty"`
	FailedPhase  string            `json:"failed_phase,omitempty"`
	GateOutput   string            `json:"gate_output,omitempty"`
	RetryOf      string            `json:"retry_of,omitempty"`
}

func (v *Vessel) NormalizeWorkflowClass() {
	if v == nil {
		return
	}
	if trimmed := strings.TrimSpace(v.WorkflowClass); trimmed != "" {
		v.WorkflowClass = trimmed
		return
	}
	v.WorkflowClass = strings.TrimSpace(v.Workflow)
}

func (v Vessel) ConcurrencyClass() string {
	if trimmed := strings.TrimSpace(v.WorkflowClass); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(v.Workflow)
}

type Queue struct {
	path     string
	lockPath string
}

func New(path string) *Queue {
	return &Queue{path: path, lockPath: path + ".lock"}
}

// Enqueue adds a vessel to the queue. If the vessel has a non-empty Ref that
// already exists in an active state (pending, running, waiting), the call is a
// no-op and returns (false, nil). Otherwise the vessel is appended and the call
// returns (true, nil). The ref check and append happen under a single lock
// acquisition, eliminating the TOCTOU race between HasRef and Enqueue.
func (q *Queue) Enqueue(vessel Vessel) (bool, error) {
	var enqueued bool
	vessel.NormalizeWorkflowClass()
	err := q.withLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}

		if vessel.Ref != "" {
			for _, v := range vessels {
				if v.Ref == vessel.Ref {
					switch v.State {
					case StatePending, StateRunning, StateWaiting:
						return nil // already active, skip silently
					}
				}
			}
		}

		for _, v := range vessels {
			if v.ID == vessel.ID {
				return ErrDuplicateID
			}
		}

		enqueued = true
		vessels = append(vessels, vessel)
		if err := q.writeAllVessels(vessels); err != nil {
			return err
		}
		if err := recordRuntimeVesselEvent(dtu.VesselOperationEnqueue, nil, &vessel); err != nil {
			return err
		}
		return nil
	})
	return enqueued, err
}

func (q *Queue) Dequeue() (*Vessel, error) {
	return q.DequeueMatching(nil)
}

func (q *Queue) DequeueMatching(match func(Vessel) bool) (*Vessel, error) {
	var out *Vessel
	err := q.withLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}

		for i := range vessels {
			if vessels[i].State != StatePending {
				continue
			}
			if match != nil && !match(vessels[i]) {
				continue
			}
			previous := vessels[i]
			vessels[i].NormalizeWorkflowClass()
			now := queueNow()
			vessels[i].State = StateRunning
			vessels[i].StartedAt = &now
			vessels[i].Error = ""

			vessel := vessels[i]
			out = &vessel
			if err := q.writeAllVessels(vessels); err != nil {
				return err
			}
			current := vessels[i]
			if err := recordRuntimeVesselEvent(dtu.VesselOperationDequeue, &previous, &current); err != nil {
				return err
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (q *Queue) Update(id string, state VesselState, errMsg string) error {
	return q.withLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}

		for i := len(vessels) - 1; i >= 0; i-- {
			if vessels[i].ID != id {
				continue
			}
			previous := vessels[i]

			// Validate state transition.
			allowed, knownState := validTransitions[vessels[i].State]
			if !knownState {
				return fmt.Errorf("%w: unknown current state %s for vessel %s", ErrInvalidTransition, vessels[i].State, id)
			}
			if !allowed[state] {
				return fmt.Errorf("%w: cannot move vessel %s from %s to %s", ErrInvalidTransition, id, vessels[i].State, state)
			}

			now := queueNow()
			vessels[i].State = state
			switch state {
			case StatePending:
				resetPendingState(&vessels[i], previous.State)
			case StateRunning:
				if vessels[i].StartedAt == nil {
					vessels[i].StartedAt = &now
				}
				vessels[i].EndedAt = nil
				vessels[i].Error = ""
			case StateFailed:
				vessels[i].EndedAt = &now
				vessels[i].Error = errMsg
			case StateCompleted, StateCancelled:
				vessels[i].EndedAt = &now
				vessels[i].Error = ""
			case StateWaiting:
				// Don't set EndedAt — vessel is still in progress
				vessels[i].WaitingSince = &now
				vessels[i].Error = ""
			case StateTimedOut:
				vessels[i].EndedAt = &now
				vessels[i].Error = errMsg
			default:
				vessels[i].Error = ""
			}
			if err := q.writeAllVessels(vessels); err != nil {
				return err
			}
			current := vessels[i]
			if err := recordRuntimeVesselEvent(dtu.VesselOperationUpdate, &previous, &current); err != nil {
				return err
			}
			return nil
		}

		return fmt.Errorf("vessel %s not found", id)
	})
}

func resetPendingState(vessel *Vessel, previousState VesselState) {
	vessel.StartedAt = nil
	vessel.EndedAt = nil
	vessel.Error = ""
	vessel.GateRetries = 0
	vessel.WaitingSince = nil
	vessel.WaitingFor = ""
	vessel.FailedPhase = ""
	vessel.GateOutput = ""
	switch previousState {
	case StateFailed, StateRunning:
		// Retry (failed→pending) and orphan reconcile (running→pending) must
		// restart the workflow from phase 0 with no inherited worktree.
		// Partial resumes caused the loop-202 chdir cascade.
		vessel.CurrentPhase = 0
		vessel.PhaseOutputs = nil
		vessel.WorktreePath = ""
	case StateWaiting:
		// Label-gate resume keeps CurrentPhase, PhaseOutputs, WorktreePath —
		// same workflow instance continues after the label appears.
	}
}

func (q *Queue) List() ([]Vessel, error) {
	var vessels []Vessel
	err := q.withRLock(func() error {
		var readErr error
		vessels, readErr = q.readAllVessels()
		return readErr
	})
	return vessels, err
}

// ReplaceAll rewrites the queue contents with vessels in their current order.
func (q *Queue) ReplaceAll(vessels []Vessel) error {
	return q.withLock(func() error {
		copied := make([]Vessel, len(vessels))
		copy(copied, vessels)
		for i := range copied {
			copied[i].NormalizeWorkflowClass()
		}
		return q.writeAllVessels(copied)
	})
}

func (q *Queue) FindByID(id string) (*Vessel, error) {
	var found *Vessel
	err := q.withRLock(func() error {
		vessels, readErr := q.readAllVessels()
		if readErr != nil {
			return readErr
		}
		for i := len(vessels) - 1; i >= 0; i-- {
			if vessels[i].ID == id {
				v := vessels[i]
				found = &v
				return nil
			}
		}
		return fmt.Errorf("vessel %s not found", id)
	})
	return found, err
}

// FindLatestByRef returns the most recent vessel with the given ref.
func (q *Queue) FindLatestByRef(ref string) (*Vessel, error) {
	var found *Vessel
	err := q.withRLock(func() error {
		vessels, readErr := q.readAllVessels()
		if readErr != nil {
			return readErr
		}
		for i := len(vessels) - 1; i >= 0; i-- {
			if vessels[i].Ref != ref {
				continue
			}
			v := vessels[i]
			found = &v
			return nil
		}
		return fmt.Errorf("vessel with ref %s not found", ref)
	})
	return found, err
}

func (q *Queue) ListByState(state VesselState) ([]Vessel, error) {
	vessels, err := q.List()
	if err != nil {
		return nil, err
	}

	filtered := make([]Vessel, 0, len(vessels))
	for _, vessel := range vessels {
		if vessel.State == state {
			filtered = append(filtered, vessel)
		}
	}
	return filtered, nil
}

// UpdateVessel replaces a vessel in the queue with the given vessel (matched by ID).
// This persists all v2 fields (CurrentPhase, WorktreePath, etc.).
func (q *Queue) UpdateVessel(vessel Vessel) error {
	return q.withLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}

		for i := len(vessels) - 1; i >= 0; i-- {
			if vessels[i].ID != vessel.ID {
				continue
			}
			previous := vessels[i]
			if previous.State != vessel.State {
				allowed, knownState := validTransitions[previous.State]
				if !knownState {
					return fmt.Errorf("%w: unknown current state %s for vessel %s", ErrInvalidTransition, previous.State, vessel.ID)
				}
				if !allowed[vessel.State] {
					return fmt.Errorf("%w: cannot move vessel %s from %s to %s", ErrInvalidTransition, vessel.ID, previous.State, vessel.State)
				}
			}
			vessels[i] = vessel
			if err := q.writeAllVessels(vessels); err != nil {
				return err
			}
			current := vessels[i]
			if err := recordRuntimeVesselEvent(dtu.VesselOperationUpdateVessel, &previous, &current); err != nil {
				return err
			}
			return nil
		}

		return fmt.Errorf("vessel %s not found", vessel.ID)
	})
}

func (q *Queue) Cancel(id string) error {
	return q.withLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}

		for i := len(vessels) - 1; i >= 0; i-- {
			if vessels[i].ID != id {
				continue
			}
			previous := vessels[i]
			allowed, knownState := validTransitions[vessels[i].State]
			if !knownState || !allowed[StateCancelled] {
				return fmt.Errorf("cannot cancel vessel %s in state %s", id, vessels[i].State)
			}
			now := queueNow()
			vessels[i].State = StateCancelled
			vessels[i].EndedAt = &now
			vessels[i].Error = ""
			if err := q.writeAllVessels(vessels); err != nil {
				return err
			}
			current := vessels[i]
			if err := recordRuntimeVesselEvent(dtu.VesselOperationCancel, &previous, &current); err != nil {
				return err
			}
			return nil
		}

		return fmt.Errorf("vessel %s not found", id)
	})
}

// Compact rewrites the queue file keeping only the latest record per vessel ID.
// Non-terminal records (pending, running, waiting) are always preserved.
// For terminal records (completed, failed, cancelled, timed_out), only the
// latest one per vessel ID is retained. Returns the number of records removed.
func (q *Queue) Compact() (int, error) {
	var removed int
	err := q.withLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}
		compacted, n := compactVessels(vessels)
		removed = n
		return q.writeAllVessels(compacted)
	})
	return removed, err
}

// CompactDryRun reports how many records would be removed by Compact without
// modifying the queue file.
func (q *Queue) CompactDryRun() (int, error) {
	var removable int
	err := q.withRLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}
		_, removable = compactVessels(vessels)
		return nil
	})
	return removable, err
}

// compactVessels returns the compacted vessel slice and the number of records
// removed. For each vessel ID, only the latest record is kept; non-terminal
// records are always preserved.
func compactVessels(vessels []Vessel) ([]Vessel, int) {
	seen := make(map[string]int, len(vessels)) // ID → index of latest record
	for i, v := range vessels {
		seen[v.ID] = i
	}
	var (
		compacted []Vessel
		removed   int
	)
	for i, v := range vessels {
		if v.State.IsTerminal() && seen[v.ID] != i {
			removed++
		} else {
			compacted = append(compacted, v)
		}
	}
	return compacted, removed
}

// compactVesselsOlderThan removes terminal vessels whose EndedAt is before
// cutoff, in addition to deduplication. Vessels with a nil EndedAt are always
// kept regardless of state.
func compactVesselsOlderThan(vessels []Vessel, cutoff time.Time) ([]Vessel, int) {
	deduped, dedupRemoved := compactVessels(vessels)
	var (
		kept       []Vessel
		ageRemoved int
	)
	for _, v := range deduped {
		if v.State.IsTerminal() && v.EndedAt != nil && v.EndedAt.Before(cutoff) {
			ageRemoved++
		} else {
			kept = append(kept, v)
		}
	}
	return kept, dedupRemoved + ageRemoved
}

// CompactOlderThan removes terminal vessel records whose EndedAt is before
// cutoff, in addition to deduplication. Vessels with a nil EndedAt are always
// kept. Returns the count of records removed.
func (q *Queue) CompactOlderThan(cutoff time.Time) (int, error) {
	var removed int
	err := q.withLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}
		compacted, n := compactVesselsOlderThan(vessels, cutoff)
		removed = n
		return q.writeAllVessels(compacted)
	})
	return removed, err
}

// CompactOlderThanDryRun reports how many records CompactOlderThan would remove
// without modifying the queue file.
func (q *Queue) CompactOlderThanDryRun(cutoff time.Time) (int, error) {
	var removable int
	err := q.withRLock(func() error {
		vessels, err := q.readAllVessels()
		if err != nil {
			return err
		}
		_, removable = compactVesselsOlderThan(vessels, cutoff)
		return nil
	})
	return removable, err
}

func (q *Queue) HasRef(ref string) bool {
	vessels, err := q.List()
	if err != nil {
		// Treat unreadable queue as "ref present" to avoid duplicate enqueues
		// from transient errors. The scanner will retry on the next cycle.
		log.Printf("warn: HasRef(%q) queue read failed, assuming present: %v", ref, err)
		return true
	}

	for _, vessel := range vessels {
		if vessel.Ref == ref {
			switch vessel.State {
			case StatePending, StateRunning, StateWaiting:
				return true
			}
		}
	}
	return false
}

// HasRefAny reports whether any vessel (in any state) has the given ref.
func (q *Queue) HasRefAny(ref string) bool {
	vessels, err := q.List()
	if err != nil {
		// Treat unreadable queue as "ref present" to avoid duplicate enqueues
		// from transient errors. The scanner will retry on the next cycle.
		log.Printf("warn: HasRefAny(%q) queue read failed, assuming present: %v", ref, err)
		return true
	}
	for _, vessel := range vessels {
		if vessel.Ref == ref {
			return true
		}
	}
	return false
}

// WithWriteLock executes fn while holding the queue's write lock.
func (q *Queue) WithWriteLock(fn func() error) error {
	return q.withLock(fn)
}

func (q *Queue) withLock(fn func() error) error {
	lock := flock.New(q.lockPath)
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			log.Printf("warn: failed to unlock queue: %v", unlockErr)
		}
	}()
	return fn()
}

func (q *Queue) withRLock(fn func() error) error {
	lock := flock.New(q.lockPath)
	if err := lock.RLock(); err != nil {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			log.Printf("warn: failed to unlock queue: %v", unlockErr)
		}
	}()
	return fn()
}

func (q *Queue) readAllVessels() ([]Vessel, error) {
	f, err := os.Open(q.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Vessel{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var (
		vessels = make([]Vessel, 0)
		lineNum int
		skipped int
	)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var vessel Vessel
		if err := json.Unmarshal([]byte(line), &vessel); err != nil {
			skipped++
			log.Printf("warn: skipping malformed queue entry at line %d: %v (content: %s)", lineNum, err, line)
			continue
		}
		migrateLegacyVessel(&vessel, []byte(line))
		vessels = append(vessels, vessel)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if skipped > 0 {
		return vessels, fmt.Errorf("%d malformed queue entries skipped", skipped)
	}

	return vessels, nil
}

// migrateLegacyVessel populates the new generic fields from legacy
// issue_url/issue_num JSON fields when reading old queue entries.
func migrateLegacyVessel(v *Vessel, raw []byte) {
	if v.Source != "" {
		return // already migrated
	}
	var legacy struct {
		IssueURL string `json:"issue_url"`
		IssueNum int    `json:"issue_num"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return
	}
	if legacy.IssueURL != "" {
		v.Source = "github-issue"
		v.Ref = legacy.IssueURL
		if v.Meta == nil {
			v.Meta = make(map[string]string)
		}
		if legacy.IssueNum != 0 {
			v.Meta["issue_num"] = strconv.Itoa(legacy.IssueNum)
		}
	}
}

func queueNow() time.Time {
	now, err := dtu.RuntimeNow()
	if err != nil {
		log.Printf("warn: queue: resolve runtime clock: %v", err)
		return time.Now().UTC()
	}
	return now.UTC()
}

func (q *Queue) writeAllVessels(vessels []Vessel) error {
	lines := make([]string, 0, len(vessels))
	for _, vessel := range vessels {
		b, err := json.Marshal(vessel)
		if err != nil {
			return err
		}
		lines = append(lines, string(b))
	}

	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	payload := []byte(content)

	if writeInterrupt != nil {
		writeInterrupt("before-tmp")
	}

	dir := filepath.Dir(q.path)
	tmp, err := os.CreateTemp(dir, ".queue-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Clean up the tmpfile on any error path OR panic. Once the rename
	// succeeds we clear tmpPath so this defer becomes a no-op and the real
	// queue file is preserved.
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}

	if writeInterrupt != nil {
		writeInterrupt("after-tmp-write")
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}

	if writeInterrupt != nil {
		writeInterrupt("after-tmp-fsync")
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, q.path); err != nil {
		return err
	}
	// Rename succeeded: the tmpfile is now the real queue file. Clear
	// tmpPath so the deferred cleanup does not delete it.
	tmpPath = ""

	if writeInterrupt != nil {
		writeInterrupt("after-rename")
	}

	// fsync the containing directory so the rename is durable across power
	// loss on ext4/xfs. Failure here is not fatal to the logical write
	// (the rename has already taken effect in the kernel's page cache),
	// but we surface it so the caller can choose whether to retry.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func recordRuntimeVesselEvent(operation dtu.VesselOperation, previous, current *Vessel) error {
	event := buildRuntimeVesselEvent(operation, previous, current)
	if event == nil {
		return nil
	}
	if err := dtu.RecordRuntimeEvent(&dtu.Event{
		Kind:   dtu.EventKindVesselUpdated,
		Vessel: event,
	}); err != nil {
		return fmt.Errorf("record DTU vessel event: %w", err)
	}
	return nil
}

func buildRuntimeVesselEvent(operation dtu.VesselOperation, previous, current *Vessel) *dtu.VesselEvent {
	var vesselID string
	switch {
	case current != nil:
		vesselID = current.ID
	case previous != nil:
		vesselID = previous.ID
	default:
		return nil
	}
	event := &dtu.VesselEvent{
		Operation: operation,
		VesselID:  vesselID,
		Previous:  buildVesselSnapshot(previous),
		Current:   buildVesselSnapshot(current),
	}
	if previous != nil {
		event.OldState = string(previous.State)
	}
	if current != nil {
		event.NewState = string(current.State)
	}
	return event
}

func buildVesselSnapshot(vessel *Vessel) *dtu.VesselSnapshot {
	if vessel == nil {
		return nil
	}
	return &dtu.VesselSnapshot{
		State:          string(vessel.State),
		Source:         vessel.Source,
		Ref:            vessel.Ref,
		Workflow:       vessel.Workflow,
		WorkflowDigest: vessel.WorkflowDigest,
		Error:          vessel.Error,
		CreatedAt:      formatVesselTime(&vessel.CreatedAt),
		StartedAt:      formatVesselTime(vessel.StartedAt),
		EndedAt:        formatVesselTime(vessel.EndedAt),
		CurrentPhase:   vessel.CurrentPhase,
		GateRetries:    vessel.GateRetries,
		WaitingSince:   formatVesselTime(vessel.WaitingSince),
		WaitingFor:     vessel.WaitingFor,
		WorktreePath:   vessel.WorktreePath,
		FailedPhase:    vessel.FailedPhase,
		GateOutput:     vessel.GateOutput,
		RetryOf:        vessel.RetryOf,
	}
}

func formatVesselTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
