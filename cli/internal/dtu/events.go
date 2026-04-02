package dtu

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const (
	eventLogFileName = "events.jsonl"
	maxEventLineSize = 8 * 1024 * 1024
)

// EventKind describes a DTU event category.
type EventKind string

const (
	EventKindStateSaved     EventKind = "state_saved"
	EventKindStateUpdated   EventKind = "state_updated"
	EventKindShimInvocation EventKind = "shim_invocation"
	EventKindShimResult     EventKind = "shim_result"
)

// Valid reports whether k is a recognized event kind.
func (k EventKind) Valid() bool {
	switch k {
	case EventKindStateSaved, EventKindStateUpdated, EventKindShimInvocation, EventKindShimResult:
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
)

// Valid reports whether o is a recognized state operation.
func (o StateOperation) Valid() bool {
	switch o {
	case StateOperationSave, StateOperationUpdate:
		return true
	default:
		return false
	}
}

// Event is a structured DTU event stored in the append-only event log.
type Event struct {
	RecordedAt string      `json:"recorded_at"`
	Kind       EventKind   `json:"kind"`
	UniverseID string      `json:"universe_id"`
	State      *StateEvent `json:"state,omitempty"`
	Shim       *ShimEvent  `json:"shim,omitempty"`
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

// ShimEvent is a reusable payload for future shim execution logging hooks.
type ShimEvent struct {
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	Provider   Provider `json:"provider,omitempty"`
	Phase      string   `json:"phase,omitempty"`
	Attempt    int      `json:"attempt,omitempty"`
	Prompt     string   `json:"prompt,omitempty"`
	PromptHash string   `json:"prompt_hash,omitempty"`
	Script     string   `json:"script,omitempty"`
	ExitCode   *int     `json:"exit_code,omitempty"`
	Duration   string   `json:"duration,omitempty"`
	Error      string   `json:"error,omitempty"`
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
	return &normalized, nil
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
