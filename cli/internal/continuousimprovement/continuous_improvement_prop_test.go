package continuousimprovement

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestPropGenerateSelectionMaintainsBoundedHistoryAndKnownFocuses(t *testing.T) {
	t.Parallel()

	known := make(map[string]struct{}, len(KnownFocuses()))
	for _, focus := range KnownFocuses() {
		known[focus.Key] = struct{}{}
	}

	rapid.Check(t, func(t *rapid.T) {
		steps := rapid.IntRange(1, 120).Draw(t, "steps")
		state := defaultState()

		for step := 0; step < steps; step++ {
			selection, nextState, err := GenerateSelection(state, Options{
				Repo: "owner/repo",
				Now:  time.Date(2026, time.April, 10, 0, 0, step, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("GenerateSelection() error = %v", err)
			}
			if _, ok := known[selection.Focus.Key]; !ok {
				t.Fatalf("selected unknown focus key %q", selection.Focus.Key)
			}
			if nextState.Runs != state.Runs+1 {
				t.Fatalf("Runs = %d, want %d", nextState.Runs, state.Runs+1)
			}
			if nextState.Counts[selection.Focus.Key] <= state.Counts[selection.Focus.Key] {
				t.Fatalf("Counts[%q] did not increase", selection.Focus.Key)
			}
			if len(nextState.History) == 0 {
				t.Fatal("history is empty after selection")
			}
			if len(nextState.History) > MaxHistory {
				t.Fatalf("history length = %d, want <= %d", len(nextState.History), MaxHistory)
			}
			last := nextState.History[len(nextState.History)-1]
			if last.FocusKey != selection.Focus.Key {
				t.Fatalf("last history key = %q, want %q", last.FocusKey, selection.Focus.Key)
			}
			state = nextState
		}
	})
}
