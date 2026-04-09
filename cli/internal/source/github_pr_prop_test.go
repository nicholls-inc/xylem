package source

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"pgregory.net/rapid"
)

func genVesselState() *rapid.Generator[queue.VesselState] {
	return rapid.SampledFrom([]queue.VesselState{
		queue.StatePending,
		queue.StateRunning,
		queue.StateWaiting,
		queue.StateFailed,
		queue.StateCompleted,
		queue.StateCancelled,
		queue.StateTimedOut,
	})
}

func TestPropPriorVesselBlocksReenqueueMatchesStateMachine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		state := genVesselState().Draw(t, "state")
		match := rapid.Bool().Draw(t, "match")
		fingerprint := rapid.StringMatching(`[a-z0-9-]{1,16}`).Draw(t, "fingerprint")

		metaFingerprint := fingerprint
		if !match {
			metaFingerprint = fingerprint + "-other"
		}
		vessel := &queue.Vessel{
			State: state,
			Meta:  map[string]string{"source_input_fingerprint": metaFingerprint},
		}

		got := priorVesselBlocksReenqueue(vessel, fingerprint)

		want := false
		switch state {
		case queue.StatePending, queue.StateRunning, queue.StateWaiting:
			want = true
		case queue.StateFailed, queue.StateTimedOut:
			want = match
		}

		if got != want {
			t.Fatalf("priorVesselBlocksReenqueue(state=%s, match=%v) = %v, want %v", state, match, got, want)
		}
	})
}
