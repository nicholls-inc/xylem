package dtu

import (
	"fmt"
	"strings"
	"time"
)

// Clock provides injectable time access for DTU state and shim replay.
type Clock interface {
	Now() time.Time
	Since(time.Time) time.Duration
}

// SystemClock delegates to the wall clock for live DTU setup paths.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

// Since reports the elapsed wall-clock duration since start.
func (SystemClock) Since(start time.Time) time.Duration {
	return time.Since(start)
}

// FixedClock pins DTU time to a deterministic instant.
type FixedClock struct {
	now time.Time
}

// NewFixedClock returns a deterministic clock anchored to now.
func NewFixedClock(now time.Time) FixedClock {
	return FixedClock{now: now.UTC()}
}

// Now returns the deterministic UTC time.
func (c FixedClock) Now() time.Time {
	return c.now
}

// Since reports the elapsed duration relative to the deterministic time.
func (c FixedClock) Since(start time.Time) time.Duration {
	return c.now.Sub(start)
}

// Advance returns a new deterministic clock shifted by delta.
func (c FixedClock) Advance(delta time.Duration) FixedClock {
	return NewFixedClock(c.now.Add(delta))
}

// ResolveClock returns a deterministic DTU clock when clock.now is set, or the
// provided fallback when the clock state is blank.
func ResolveClock(state ClockState, fallback Clock) (Clock, error) {
	parsed, ok, err := parseClockState(state)
	if err != nil {
		return nil, err
	}
	if ok {
		return NewFixedClock(parsed), nil
	}
	if fallback != nil {
		return fallback, nil
	}
	return SystemClock{}, nil
}

func parseClockState(state ClockState) (time.Time, bool, error) {
	value := strings.TrimSpace(state.Now)
	if value == "" {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("clock.now must be RFC3339: %w", err)
	}
	return parsed.UTC(), true, nil
}

func normalizeClockState(state ClockState, fallback Clock) ClockState {
	parsed, ok, err := parseClockState(state)
	if err == nil && ok {
		state.Now = parsed.Format(time.RFC3339Nano)
		return state
	}
	state.Now = strings.TrimSpace(state.Now)
	if state.Now == "" && fallback != nil {
		state.Now = fallback.Now().UTC().Format(time.RFC3339Nano)
	}
	return state
}
