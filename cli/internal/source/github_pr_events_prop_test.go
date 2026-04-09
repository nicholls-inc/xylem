package source

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func preEventTriggerGen() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		preventLabel,
		preventReviewSubmitted,
		preventChecksFailed,
		preventCommented,
		preventPROpened,
		preventPRHeadUpdated,
	})
}

func TestPropPREventExplicitDebounceOverridesDefaults(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		trigger := preEventTriggerGen().Draw(t, "trigger")
		debounce := time.Duration(rapid.Int64Range(0, int64(24*time.Hour)).Draw(t, "debounce"))
		task := PREventsTask{Debounce: debounce}

		if got := effectivePREventDebounce(task, trigger); got != debounce {
			t.Fatalf("effectivePREventDebounce(%q) = %v, want %v", trigger, got, debounce)
		}
	})
}

func TestPropPREventDefaultDebounceMatchesTrigger(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		trigger := preEventTriggerGen().Draw(t, "trigger")
		task := PREventsTask{Debounce: UnsetPREventsDebounce}

		want := time.Duration(0)
		switch trigger {
		case preventPRHeadUpdated:
			want = 10 * time.Minute
		}

		if got := effectivePREventDebounce(task, trigger); got != want {
			t.Fatalf("effectivePREventDebounce(%q) = %v, want %v", trigger, got, want)
		}
	})
}
