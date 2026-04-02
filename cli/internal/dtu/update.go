package dtu

import (
	"encoding/json"
	"fmt"
	"os"
)

// Update loads, mutates, validates, and saves DTU state while holding the write lock.
func (s *Store) Update(fn func(*State) error) error {
	if fn == nil {
		return fmt.Errorf("update state: mutator must not be nil")
	}

	return s.withLock(func() error {
		loaded, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		previous, err := cloneState(loaded)
		if err != nil {
			return fmt.Errorf("update state: clone previous state: %w", err)
		}
		if err := fn(loaded); err != nil {
			return err
		}
		return s.persistUnlocked(loaded, previous, EventKindStateUpdated, StateOperationUpdate)
	})
}

func (s *Store) loadUnlocked() (*State, error) {
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	clock, err := ResolveClock(loaded.Clock, s.clockOrDefault())
	if err != nil {
		return nil, fmt.Errorf("resolve state clock: %w", err)
	}
	loaded.normalizeWithClock(clock)
	if loaded.UniverseID != s.universeID {
		return nil, fmt.Errorf("load state: universe ID mismatch: path %q state %q", s.universeID, loaded.UniverseID)
	}
	if err := loaded.Validate(); err != nil {
		return nil, fmt.Errorf("validate state: %w", err)
	}
	return &loaded, nil
}

func (s *Store) existingStateForEventUnlocked() *State {
	state, err := s.loadUnlocked()
	if err != nil {
		return nil
	}
	return state
}

func (s *Store) persistUnlocked(state *State, previous *State, kind EventKind, operation StateOperation) error {
	return s.persistUnlockedWithEvents(state, previous, kind, operation, nil)
}

func (s *Store) persistUnlockedWithEvents(state *State, previous *State, kind EventKind, operation StateOperation, extraEvents []*Event) error {
	if state == nil {
		return fmt.Errorf("persist state: state must not be nil")
	}
	if state.UniverseID == "" {
		state.UniverseID = s.universeID
	} else if state.UniverseID != s.universeID {
		return fmt.Errorf("persist state: universe ID mismatch: store %q state %q", s.universeID, state.UniverseID)
	}
	clock, err := ResolveClock(state.Clock, s.clockOrDefault())
	if err != nil {
		return fmt.Errorf("persist state: resolve clock: %w", err)
	}
	state.normalizeWithClock(clock)
	if err := state.Validate(); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	if err := s.writeStateUnlocked(state); err != nil {
		return err
	}
	for _, extraEvent := range extraEvents {
		if extraEvent == nil {
			continue
		}
		if err := s.appendEventUnlocked(extraEvent); err != nil {
			return fmt.Errorf("append DTU event: %w", err)
		}
	}
	event, err := newStateEvent(kind, s.universeID, operation, previous, state)
	if err != nil {
		return fmt.Errorf("build DTU event: %w", err)
	}
	if err := s.appendEventUnlocked(event); err != nil {
		return fmt.Errorf("append DTU event: %w", err)
	}
	return nil
}

func (s *Store) writeStateUnlocked(state *State) error {
	if err := os.MkdirAll(s.rootDir, 0o755); err != nil {
		return fmt.Errorf("save state: create dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("save state: marshal: %w", err)
	}
	data = append(data, '\n')
	tmpPath := s.statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("save state: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, s.statePath); err != nil {
		return fmt.Errorf("save state: rename tmp: %w", err)
	}
	return nil
}
