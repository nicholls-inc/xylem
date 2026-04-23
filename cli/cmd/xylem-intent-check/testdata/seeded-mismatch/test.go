// Property tests for ballot tally.
// This file is a testdata fixture — it is NOT compiled as part of the module.

package tally_test

import "testing"

// BallotTally tracks the number of ballots added.
type BallotTally struct {
	count int
}

// AddBallot adds n ballots to the tally.
func (b *BallotTally) AddBallot(n int) {
	b.count += n
}

// Tally returns the current ballot count.
func (b *BallotTally) Tally() int {
	return b.count
}

// Invariant I1: (partial coverage)
// TestPropTallyNonNegative checks that the tally is always non-negative
// after adding ballots.
func TestPropTallyNonNegative(t *testing.T) {
	bt := &BallotTally{}
	for _, n := range []int{1, 2, 3} {
		bt.AddBallot(n)
		if bt.Tally() < 0 {
			t.Errorf("tally went negative: %d", bt.Tally())
		}
	}
}
