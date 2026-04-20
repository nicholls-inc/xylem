package queue

// Differential test: verified.IsTerminal must agree with VesselState.IsTerminal
// for every canonical state. This is the abstraction-gap check — same result
// from the Dafny-extracted Go as from the original inline implementation.
//
// Lives in package queue (internal) so it can call VesselState.IsTerminal()
// directly. Importing verified from queue_test is safe because queue does not
// yet import verified; the wiring PR will flip that dependency.

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue/verified"
)

func TestIsTerminal_DifferentialWithVerified(t *testing.T) {
	canonical := []string{
		"pending",
		"running",
		"completed",
		"failed",
		"cancelled",
		"waiting",
		"timed_out",
	}
	for _, s := range canonical {
		want := VesselState(s).IsTerminal()
		got := verified.IsTerminal(s)
		if got != want {
			t.Errorf("state %q: VesselState.IsTerminal()=%v, verified.IsTerminal()=%v", s, want, got)
		}
	}
}
