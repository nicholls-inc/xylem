package dtu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeNow returns the active DTU clock time when XYLEM_DTU_STATE_PATH points
// at a real DTU state file, or the wall clock otherwise.
func RuntimeNow() (time.Time, error) {
	clock, err := loadRuntimeClock()
	if err != nil {
		return time.Time{}, err
	}
	return clock.Now().UTC(), nil
}

// RuntimeSince reports elapsed time against the active DTU clock when present,
// or the wall clock otherwise.
func RuntimeSince(start time.Time) (time.Duration, error) {
	clock, err := loadRuntimeClock()
	if err != nil {
		return 0, err
	}
	return clock.Since(start), nil
}

// RuntimeSleep advances the active DTU clock without blocking real time. When
// no DTU state is active, it falls back to a normal context-aware sleep.
func RuntimeSleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	advanced, err := advanceRuntimeClock(delay)
	if err != nil {
		return err
	}
	if advanced {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	return sleepWithContext(ctx, delay)
}

// AdvanceRuntimeClock explicitly advances the active DTU clock. It returns an
// error when DTU state is not configured or the state file cannot be updated.
func AdvanceRuntimeClock(delay time.Duration) error {
	if delay < 0 {
		return fmt.Errorf("advance DTU runtime clock: delay must be non-negative")
	}
	advanced, err := advanceRuntimeClock(delay)
	if err != nil {
		return err
	}
	if !advanced {
		return fmt.Errorf("advance DTU runtime clock: %s is not pointing at an active DTU state file", EnvStatePath)
	}
	return nil
}

func loadRuntimeClock() (Clock, error) {
	store, ok, err := runtimeStore()
	if err != nil {
		return nil, err
	}
	if !ok {
		return SystemClock{}, nil
	}
	state, err := store.Load()
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "read state:") {
			return SystemClock{}, nil
		}
		return nil, fmt.Errorf("resolve DTU runtime clock: %w", err)
	}
	clock, err := ResolveClock(state.Clock, SystemClock{})
	if err != nil {
		return nil, fmt.Errorf("resolve DTU runtime clock: %w", err)
	}
	return clock, nil
}

func advanceRuntimeClock(delay time.Duration) (bool, error) {
	store, ok, err := runtimeStore()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := store.Update(func(state *State) error {
		clock, err := ResolveClock(state.Clock, SystemClock{})
		if err != nil {
			return fmt.Errorf("advance DTU runtime clock: resolve clock: %w", err)
		}
		state.Clock = ClockState{
			Now: clock.Now().UTC().Add(delay).Format(time.RFC3339Nano),
		}
		return nil
	}); err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "read state:") {
			return false, nil
		}
		return false, fmt.Errorf("advance DTU runtime clock: %w", err)
	}
	return true, nil
}

func runtimeStore() (*Store, bool, error) {
	statePath := strings.TrimSpace(os.Getenv(EnvStatePath))
	if statePath == "" {
		return nil, false, nil
	}
	if _, err := os.Stat(statePath); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("resolve DTU runtime clock: stat state file: %w", err)
	}
	stateDir, universeID, err := deriveRuntimeStoreLocation(statePath)
	if err != nil {
		return nil, false, fmt.Errorf("resolve DTU runtime clock: %w", err)
	}
	store, err := NewStore(stateDir, universeID)
	if err != nil {
		return nil, false, fmt.Errorf("resolve DTU runtime clock: %w", err)
	}
	return store, true, nil
}

func deriveRuntimeStoreLocation(statePath string) (string, string, error) {
	cleaned := filepath.Clean(statePath)
	if filepath.Base(cleaned) != stateFileName {
		return "", "", fmt.Errorf("state path %q must point to %s", cleaned, stateFileName)
	}
	universeID := filepath.Base(filepath.Dir(cleaned))
	if err := validatePathComponent(universeID); err != nil {
		return "", "", fmt.Errorf("invalid universe ID: %w", err)
	}
	dtuDir := filepath.Base(filepath.Dir(filepath.Dir(cleaned)))
	if dtuDir != "dtu" {
		return "", "", fmt.Errorf("state path %q must live under <stateDir>/dtu/<universe>/%s", cleaned, stateFileName)
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(cleaned))), universeID, nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
