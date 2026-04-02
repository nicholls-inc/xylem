package dtu

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	eventLogFileName = "events.jsonl"
	maxEventLineSize = 8 * 1024 * 1024
)

// EventKind describes a DTU event category.
type EventKind string

const (
	EventKindStateSaved               EventKind = "state_saved"
	EventKindStateUpdated             EventKind = "state_updated"
	EventKindShimInvocation           EventKind = "shim_invocation"
	EventKindShimResult               EventKind = "shim_result"
	EventKindSchedulerObserved        EventKind = "scheduler_observed"
	EventKindSchedulerMutationApplied EventKind = "scheduler_mutation_applied"
	EventKindVesselUpdated            EventKind = "vessel_updated"
)

// Valid reports whether k is a recognized event kind.
func (k EventKind) Valid() bool {
	switch k {
	case EventKindStateSaved,
		EventKindStateUpdated,
		EventKindShimInvocation,
		EventKindShimResult,
		EventKindSchedulerObserved,
		EventKindSchedulerMutationApplied,
		EventKindVesselUpdated:
		return true
	default:
		return false
	}
}

// StateOperation describes the state persistence path that emitted an event.
type StateOperation string

const (
	StateOperationSave   StateOperation = "save"
	StateOperationUpdate StateOperation = "update"
	StateOperationReset  StateOperation = "reset"
)

// Valid reports whether o is a recognized state operation.
func (o StateOperation) Valid() bool {
	switch o {
	case StateOperationSave, StateOperationUpdate, StateOperationReset:
		return true
	default:
		return false
	}
}

// Event is a structured DTU event stored in the append-only event log.
type Event struct {
	RecordedAt string          `json:"recorded_at"`
	Kind       EventKind       `json:"kind"`
	UniverseID string          `json:"universe_id"`
	State      *StateEvent     `json:"state,omitempty"`
	Shim       *ShimEvent      `json:"shim,omitempty"`
	Scheduler  *SchedulerEvent `json:"scheduler,omitempty"`
	Vessel     *VesselEvent    `json:"vessel,omitempty"`
}

// StateEvent captures a persisted DTU state snapshot and quick summary metadata.
type StateEvent struct {
	Operation    StateOperation `json:"operation"`
	Changed      bool           `json:"changed"`
	PreviousHash string         `json:"previous_hash,omitempty"`
	Hash         string         `json:"hash"`
	Summary      StateSummary   `json:"summary"`
	Snapshot     *State         `json:"snapshot,omitempty"`
}

// StateSummary provides a compact overview of a DTU state snapshot.
type StateSummary struct {
	Version          string   `json:"version"`
	MetadataName     string   `json:"metadata_name"`
	ManifestPath     string   `json:"manifest_path,omitempty"`
	Clock            string   `json:"clock,omitempty"`
	Repositories     []string `json:"repositories,omitempty"`
	RepositoryCount  int      `json:"repository_count"`
	IssueCount       int      `json:"issue_count"`
	PullRequestCount int      `json:"pull_request_count"`
	CommentCount     int      `json:"comment_count"`
	ReviewCount      int      `json:"review_count"`
	CheckCount       int      `json:"check_count"`
	Counters         Counters `json:"counters"`
}

// ReplaySnapshot is a replayable DTU state snapshot captured in the event log.
type ReplaySnapshot struct {
	EventIndex int            `json:"event_index"`
	RecordedAt string         `json:"recorded_at"`
	Kind       EventKind      `json:"kind"`
	Operation  StateOperation `json:"operation"`
	Hash       string         `json:"hash"`
	Summary    StateSummary   `json:"summary"`
	State      *State         `json:"state"`
}

// ShimEvent is a reusable payload for future shim execution logging hooks.
type ShimEvent struct {
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Provider    Provider `json:"provider,omitempty"`
	Phase       string   `json:"phase,omitempty"`
	Attempt     int      `json:"attempt,omitempty"`
	BinaryPath  string   `json:"binary_path,omitempty"`
	BinaryName  string   `json:"binary_name,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	StdinDigest string   `json:"stdin_digest,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
	PromptHash  string   `json:"prompt_hash,omitempty"`
	Script      string   `json:"script,omitempty"`
	ExitCode    *int     `json:"exit_code,omitempty"`
	Duration    string   `json:"duration,omitempty"`
	StdoutBytes int      `json:"stdout_bytes,omitempty"`
	StderrBytes int      `json:"stderr_bytes,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// SchedulerEvent captures deterministic scheduler observations and mutation applications.
type SchedulerEvent struct {
	Command          ShimCommand         `json:"command,omitempty"`
	Args             []string            `json:"args,omitempty"`
	Phase            string              `json:"phase,omitempty"`
	Script           string              `json:"script,omitempty"`
	Attempt          int                 `json:"attempt,omitempty"`
	ObservationKey   string              `json:"observation_key,omitempty"`
	ObservationCount int                 `json:"observation_count,omitempty"`
	MatchedMutations []string            `json:"matched_mutations,omitempty"`
	AppliedMutations []string            `json:"applied_mutations,omitempty"`
	MutationName     string              `json:"mutation_name,omitempty"`
	TriggerAfter     int                 `json:"trigger_after,omitempty"`
	AppliedAt        string              `json:"applied_at,omitempty"`
	Operations       []MutationOperation `json:"operations,omitempty"`
}

// VesselOperation describes the queue mutation path that emitted an event.
type VesselOperation string

const (
	VesselOperationEnqueue      VesselOperation = "enqueue"
	VesselOperationDequeue      VesselOperation = "dequeue"
	VesselOperationUpdate       VesselOperation = "update"
	VesselOperationUpdateVessel VesselOperation = "update_vessel"
	VesselOperationCancel       VesselOperation = "cancel"
)

// Valid reports whether o is a recognized vessel operation.
func (o VesselOperation) Valid() bool {
	switch o {
	case VesselOperationEnqueue,
		VesselOperationDequeue,
		VesselOperationUpdate,
		VesselOperationUpdateVessel,
		VesselOperationCancel:
		return true
	default:
		return false
	}
}

// VesselEvent captures xylem vessel state and run-state mutations observed under DTU.
type VesselEvent struct {
	Operation VesselOperation `json:"operation"`
	VesselID  string          `json:"vessel_id"`
	OldState  string          `json:"old_state,omitempty"`
	NewState  string          `json:"new_state,omitempty"`
	Previous  *VesselSnapshot `json:"previous,omitempty"`
	Current   *VesselSnapshot `json:"current,omitempty"`
}

// VesselSnapshot is a condensed view of queue.Vessel fields relevant to DTU replay.
type VesselSnapshot struct {
	State        string `json:"state,omitempty"`
	Source       string `json:"source,omitempty"`
	Ref          string `json:"ref,omitempty"`
	Workflow     string `json:"workflow,omitempty"`
	Error        string `json:"error,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	EndedAt      string `json:"ended_at,omitempty"`
	CurrentPhase int    `json:"current_phase,omitempty"`
	GateRetries  int    `json:"gate_retries,omitempty"`
	WaitingSince string `json:"waiting_since,omitempty"`
	WaitingFor   string `json:"waiting_for,omitempty"`
	WorktreePath string `json:"worktree_path,omitempty"`
	FailedPhase  string `json:"failed_phase,omitempty"`
	GateOutput   string `json:"gate_output,omitempty"`
	RetryOf      string `json:"retry_of,omitempty"`
}

// RecordEvent appends a structured DTU event while holding the store lock.
func (s *Store) RecordEvent(event *Event) error {
	if event == nil {
		return fmt.Errorf("record DTU event: event must not be nil")
	}
	return s.withLock(func() error {
		if err := s.appendEventUnlocked(event); err != nil {
			return fmt.Errorf("record DTU event: %w", err)
		}
		return nil
	})
}

// ReadEvents reads DTU events from the append-only event log in file order.
func (s *Store) ReadEvents() ([]Event, error) {
	var events []Event
	err := s.withRLock(func() error {
		loaded, err := s.readEventsUnlocked()
		if err != nil {
			return err
		}
		events = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

// Replay returns state snapshots captured in the DTU event log in file order.
func (s *Store) Replay() ([]ReplaySnapshot, error) {
	var snapshots []ReplaySnapshot
	err := s.withRLock(func() error {
		events, err := s.readEventsUnlocked()
		if err != nil {
			return err
		}
		replayed, err := replaySnapshotsFromEvents(events)
		if err != nil {
			return err
		}
		snapshots = replayed
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("replay DTU state: %w", err)
	}
	return snapshots, nil
}

func (s *Store) readEventsUnlocked() ([]Event, error) {
	file, err := os.Open(s.eventLogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open DTU event log: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventLineSize)

	var events []Event
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode DTU event line %d: %w", lineNo, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan DTU event log: %w", err)
	}
	return events, nil
}

func (s *Store) appendEventUnlocked(event *Event) error {
	normalized, err := s.normalizeEvent(event)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.rootDir, 0o755); err != nil {
		return fmt.Errorf("create DTU event dir: %w", err)
	}
	file, err := os.OpenFile(s.eventLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open DTU event log: %w", err)
	}
	defer file.Close()

	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal DTU event: %w", err)
	}
	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("append DTU event: %w", err)
	}
	return nil
}

func (s *Store) normalizeEvent(event *Event) (*Event, error) {
	normalized := *event
	if !normalized.Kind.Valid() {
		return nil, fmt.Errorf("DTU event kind %q is invalid", normalized.Kind)
	}
	if normalized.UniverseID == "" {
		normalized.UniverseID = s.universeID
	} else if normalized.UniverseID != s.universeID {
		return nil, fmt.Errorf("DTU event universe ID mismatch: store %q event %q", s.universeID, normalized.UniverseID)
	}
	if err := validatePathComponent(normalized.UniverseID); err != nil {
		return nil, fmt.Errorf("DTU event universe ID: %w", err)
	}
	if normalized.RecordedAt == "" {
		clock, err := s.currentClockUnlocked()
		if err != nil {
			return nil, err
		}
		normalized.RecordedAt = clock.Now().UTC().Format(time.RFC3339Nano)
	} else {
		recordedAt, err := time.Parse(time.RFC3339Nano, normalized.RecordedAt)
		if err != nil {
			return nil, fmt.Errorf("DTU event recorded_at must be RFC3339: %w", err)
		}
		normalized.RecordedAt = recordedAt.UTC().Format(time.RFC3339Nano)
	}
	switch normalized.Kind {
	case EventKindStateSaved, EventKindStateUpdated:
		if normalized.State == nil {
			return nil, fmt.Errorf("DTU state event payload is required for kind %q", normalized.Kind)
		}
	case EventKindShimInvocation, EventKindShimResult:
		if normalized.Shim == nil {
			return nil, fmt.Errorf("DTU shim event payload is required for kind %q", normalized.Kind)
		}
	case EventKindSchedulerObserved, EventKindSchedulerMutationApplied:
		if normalized.Scheduler == nil {
			return nil, fmt.Errorf("DTU scheduler event payload is required for kind %q", normalized.Kind)
		}
	case EventKindVesselUpdated:
		if normalized.Vessel == nil {
			return nil, fmt.Errorf("DTU vessel event payload is required for kind %q", normalized.Kind)
		}
	}
	if normalized.State != nil {
		stateEvent, err := normalizeStateEvent(normalized.State, s.universeID)
		if err != nil {
			return nil, err
		}
		normalized.State = stateEvent
	}
	if normalized.Shim != nil {
		shimEvent, err := normalizeShimEvent(normalized.Shim)
		if err != nil {
			return nil, err
		}
		normalized.Shim = shimEvent
	}
	if normalized.Scheduler != nil {
		schedulerEvent, err := normalizeSchedulerEvent(normalized.Scheduler)
		if err != nil {
			return nil, err
		}
		normalized.Scheduler = schedulerEvent
	}
	if normalized.Vessel != nil {
		vesselEvent, err := normalizeVesselEvent(normalized.Vessel)
		if err != nil {
			return nil, err
		}
		normalized.Vessel = vesselEvent
	}
	return &normalized, nil
}

func normalizeStateEvent(event *StateEvent, universeID string) (*StateEvent, error) {
	normalized := *event
	if !normalized.Operation.Valid() {
		return nil, fmt.Errorf("DTU state event operation %q is invalid", normalized.Operation)
	}
	if normalized.Snapshot != nil {
		snapshot, hash, summary, err := snapshotState(normalized.Snapshot, universeID)
		if err != nil {
			return nil, err
		}
		normalized.Snapshot = snapshot
		normalized.Summary = summary
		if normalized.Hash == "" {
			normalized.Hash = hash
		} else if normalized.Hash != hash {
			return nil, fmt.Errorf("DTU state event hash mismatch: have %q want %q", normalized.Hash, hash)
		}
	}
	if normalized.Hash == "" {
		return nil, fmt.Errorf("DTU state event hash is required")
	}
	return &normalized, nil
}

func normalizeShimEvent(event *ShimEvent) (*ShimEvent, error) {
	normalized := *event
	normalized.Args = append([]string(nil), event.Args...)
	normalized.BinaryPath = strings.TrimSpace(normalized.BinaryPath)
	normalized.BinaryName = strings.TrimSpace(normalized.BinaryName)
	normalized.WorkingDir = strings.TrimSpace(normalized.WorkingDir)
	normalized.StdinDigest = strings.TrimSpace(normalized.StdinDigest)
	if normalized.BinaryName == "" && normalized.BinaryPath != "" {
		normalized.BinaryName = filepath.Base(normalized.BinaryPath)
	}
	if normalized.Provider != "" && !normalized.Provider.Valid() {
		return nil, fmt.Errorf("DTU shim event provider %q is invalid", normalized.Provider)
	}
	if normalized.Attempt < 0 {
		return nil, fmt.Errorf("DTU shim event attempt must be non-negative")
	}
	if event.ExitCode != nil {
		exitCode := *event.ExitCode
		normalized.ExitCode = &exitCode
	}
	if normalized.Duration != "" {
		if _, err := time.ParseDuration(normalized.Duration); err != nil {
			return nil, fmt.Errorf("DTU shim event duration must be a valid duration: %w", err)
		}
	}
	if normalized.StdoutBytes < 0 {
		return nil, fmt.Errorf("DTU shim event stdout bytes must be non-negative")
	}
	if normalized.StderrBytes < 0 {
		return nil, fmt.Errorf("DTU shim event stderr bytes must be non-negative")
	}
	return &normalized, nil
}

func normalizeSchedulerEvent(event *SchedulerEvent) (*SchedulerEvent, error) {
	normalized := *event
	if !normalized.Command.Valid() {
		return nil, fmt.Errorf("DTU scheduler event command %q is invalid", normalized.Command)
	}
	if normalized.Attempt < 0 {
		return nil, fmt.Errorf("DTU scheduler event attempt must be non-negative")
	}
	if normalized.ObservationCount < 0 {
		return nil, fmt.Errorf("DTU scheduler event observation count must be non-negative")
	}
	if normalized.TriggerAfter < 0 {
		return nil, fmt.Errorf("DTU scheduler event trigger_after must be non-negative")
	}
	normalized.Args = append([]string(nil), event.Args...)
	normalized.Phase = strings.TrimSpace(normalized.Phase)
	normalized.Script = strings.TrimSpace(normalized.Script)
	normalized.ObservationKey = strings.TrimSpace(normalized.ObservationKey)
	normalized.MutationName = strings.TrimSpace(normalized.MutationName)
	normalized.MatchedMutations = normalizeStrings(normalized.MatchedMutations)
	normalized.AppliedMutations = normalizeStrings(normalized.AppliedMutations)
	if normalized.ObservationKey == "" {
		return nil, fmt.Errorf("DTU scheduler event observation key is required")
	}
	if normalized.AppliedAt != "" {
		appliedAt, err := time.Parse(time.RFC3339Nano, normalized.AppliedAt)
		if err != nil {
			return nil, fmt.Errorf("DTU scheduler event applied_at must be RFC3339: %w", err)
		}
		normalized.AppliedAt = appliedAt.UTC().Format(time.RFC3339Nano)
	}
	operations, err := normalizeMutationOperations(normalized.Operations)
	if err != nil {
		return nil, fmt.Errorf("DTU scheduler event operations: %w", err)
	}
	normalized.Operations = operations
	return &normalized, nil
}

func normalizeVesselEvent(event *VesselEvent) (*VesselEvent, error) {
	normalized := *event
	if !normalized.Operation.Valid() {
		return nil, fmt.Errorf("DTU vessel event operation %q is invalid", normalized.Operation)
	}
	normalized.VesselID = strings.TrimSpace(normalized.VesselID)
	if normalized.VesselID == "" {
		return nil, fmt.Errorf("DTU vessel event vessel_id is required")
	}
	normalized.OldState = strings.TrimSpace(normalized.OldState)
	normalized.NewState = strings.TrimSpace(normalized.NewState)
	previous, err := normalizeVesselSnapshot(normalized.Previous)
	if err != nil {
		return nil, fmt.Errorf("DTU vessel event previous snapshot: %w", err)
	}
	current, err := normalizeVesselSnapshot(normalized.Current)
	if err != nil {
		return nil, fmt.Errorf("DTU vessel event current snapshot: %w", err)
	}
	if current == nil {
		return nil, fmt.Errorf("DTU vessel event current snapshot is required")
	}
	normalized.Previous = previous
	normalized.Current = current
	if normalized.OldState == "" && previous != nil {
		normalized.OldState = previous.State
	}
	if normalized.NewState == "" {
		normalized.NewState = current.State
	}
	return &normalized, nil
}

func normalizeVesselSnapshot(snapshot *VesselSnapshot) (*VesselSnapshot, error) {
	if snapshot == nil {
		return nil, nil
	}
	normalized := *snapshot
	normalized.State = strings.TrimSpace(normalized.State)
	normalized.Source = strings.TrimSpace(normalized.Source)
	normalized.Ref = strings.TrimSpace(normalized.Ref)
	normalized.Workflow = strings.TrimSpace(normalized.Workflow)
	normalized.Error = strings.TrimSpace(normalized.Error)
	normalized.WaitingFor = strings.TrimSpace(normalized.WaitingFor)
	normalized.WorktreePath = strings.TrimSpace(normalized.WorktreePath)
	normalized.FailedPhase = strings.TrimSpace(normalized.FailedPhase)
	normalized.GateOutput = strings.TrimSpace(normalized.GateOutput)
	normalized.RetryOf = strings.TrimSpace(normalized.RetryOf)
	if normalized.CurrentPhase < 0 {
		return nil, fmt.Errorf("current_phase must be non-negative")
	}
	if normalized.GateRetries < 0 {
		return nil, fmt.Errorf("gate_retries must be non-negative")
	}
	var err error
	if normalized.CreatedAt, err = normalizeOptionalRFC3339(normalized.CreatedAt); err != nil {
		return nil, fmt.Errorf("created_at must be RFC3339: %w", err)
	}
	if normalized.StartedAt, err = normalizeOptionalRFC3339(normalized.StartedAt); err != nil {
		return nil, fmt.Errorf("started_at must be RFC3339: %w", err)
	}
	if normalized.EndedAt, err = normalizeOptionalRFC3339(normalized.EndedAt); err != nil {
		return nil, fmt.Errorf("ended_at must be RFC3339: %w", err)
	}
	if normalized.WaitingSince, err = normalizeOptionalRFC3339(normalized.WaitingSince); err != nil {
		return nil, fmt.Errorf("waiting_since must be RFC3339: %w", err)
	}
	return &normalized, nil
}

func normalizeMutationOperations(operations []MutationOperation) ([]MutationOperation, error) {
	if len(operations) == 0 {
		return nil, nil
	}
	normalized := append([]MutationOperation(nil), operations...)
	for i := range normalized {
		if !normalized[i].Type.Valid() {
			return nil, fmt.Errorf("invalid operation type %q", normalized[i].Type)
		}
		normalized[i].Repo = strings.TrimSpace(normalized[i].Repo)
		normalized[i].Label = strings.TrimSpace(normalized[i].Label)
		normalized[i].Check = strings.TrimSpace(normalized[i].Check)
		normalized[i].State = strings.TrimSpace(normalized[i].State)
		normalized[i].Body = strings.TrimSpace(normalized[i].Body)
	}
	return normalized, nil
}

func normalizeOptionalRFC3339(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", err
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func newStateEvent(kind EventKind, universeID string, operation StateOperation, previous, current *State) (*Event, error) {
	snapshot, hash, summary, err := snapshotState(current, universeID)
	if err != nil {
		return nil, fmt.Errorf("snapshot current state: %w", err)
	}

	var previousHash string
	if previous != nil {
		_, previousHash, _, err = snapshotState(previous, universeID)
		if err != nil {
			return nil, fmt.Errorf("snapshot previous state: %w", err)
		}
	}

	return &Event{
		Kind:       kind,
		UniverseID: universeID,
		State: &StateEvent{
			Operation:    operation,
			Changed:      previous == nil || previousHash != hash,
			PreviousHash: previousHash,
			Hash:         hash,
			Summary:      summary,
			Snapshot:     snapshot,
		},
	}, nil
}

func snapshotState(state *State, universeID string) (*State, string, StateSummary, error) {
	cloned, err := cloneState(state)
	if err != nil {
		return nil, "", StateSummary{}, fmt.Errorf("clone state: %w", err)
	}
	if cloned.UniverseID == "" {
		cloned.UniverseID = universeID
	} else if cloned.UniverseID != universeID {
		return nil, "", StateSummary{}, fmt.Errorf("state universe ID mismatch: have %q want %q", cloned.UniverseID, universeID)
	}
	clock, err := ResolveClock(cloned.Clock, nil)
	if err != nil {
		return nil, "", StateSummary{}, fmt.Errorf("resolve state snapshot clock: %w", err)
	}
	cloned.normalizeWithClock(clock)
	if err := cloned.Validate(); err != nil {
		return nil, "", StateSummary{}, fmt.Errorf("validate state snapshot: %w", err)
	}
	data, err := json.Marshal(cloned)
	if err != nil {
		return nil, "", StateSummary{}, fmt.Errorf("marshal state snapshot: %w", err)
	}
	sum := sha256.Sum256(data)
	return cloned, hex.EncodeToString(sum[:]), summarizeState(cloned), nil
}

func summarizeState(state *State) StateSummary {
	summary := StateSummary{
		Version:      state.Version,
		MetadataName: state.Metadata.Name,
		ManifestPath: state.ManifestPath,
		Clock:        state.Clock.Now,
		Counters:     state.Counters,
	}
	for _, repo := range state.Repositories {
		summary.Repositories = append(summary.Repositories, repo.Slug())
		summary.RepositoryCount++
		summary.IssueCount += len(repo.Issues)
		summary.PullRequestCount += len(repo.PullRequests)
		for _, issue := range repo.Issues {
			summary.CommentCount += len(issue.Comments)
		}
		for _, pr := range repo.PullRequests {
			summary.CommentCount += len(pr.Comments)
			summary.ReviewCount += len(pr.Reviews)
			summary.CheckCount += len(pr.Checks)
		}
	}
	return summary
}

func cloneState(state *State) (*State, error) {
	if state == nil {
		return nil, fmt.Errorf("state must not be nil")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshal state clone: %w", err)
	}
	var cloned State
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, fmt.Errorf("unmarshal state clone: %w", err)
	}
	return &cloned, nil
}

func replaySnapshotsFromEvents(events []Event) ([]ReplaySnapshot, error) {
	snapshots := make([]ReplaySnapshot, 0, len(events))
	for i, event := range events {
		snapshot, ok, err := newReplaySnapshot(i, event)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func newReplaySnapshot(eventIndex int, event Event) (ReplaySnapshot, bool, error) {
	if event.State == nil || event.State.Snapshot == nil {
		return ReplaySnapshot{}, false, nil
	}
	state, err := cloneState(event.State.Snapshot)
	if err != nil {
		return ReplaySnapshot{}, false, fmt.Errorf("build replay snapshot for event %d: clone state: %w", eventIndex, err)
	}
	return ReplaySnapshot{
		EventIndex: eventIndex,
		RecordedAt: event.RecordedAt,
		Kind:       event.Kind,
		Operation:  event.State.Operation,
		Hash:       event.State.Hash,
		Summary:    cloneStateSummary(event.State.Summary),
		State:      state,
	}, true, nil
}

func cloneStateSummary(summary StateSummary) StateSummary {
	cloned := summary
	cloned.Repositories = append([]string(nil), summary.Repositories...)
	return cloned
}
