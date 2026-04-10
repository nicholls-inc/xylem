package review

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

func TestPropParseGitRevListAheadBehindRoundTripsCounts(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		behind := rapid.IntRange(0, 500).Draw(t, "behind")
		ahead := rapid.IntRange(0, 500).Draw(t, "ahead")
		separator := rapid.SampledFrom([]string{" ", "\t", "  ", "\t\t"}).Draw(t, "separator")
		raw := fmt.Sprintf("%d%s%d\n", behind, separator, ahead)

		gotBehind, gotAhead, err := parseGitRevListAheadBehind(raw)
		if err != nil {
			t.Fatalf("parseGitRevListAheadBehind(%q) error = %v", raw, err)
		}
		if gotBehind != behind || gotAhead != ahead {
			t.Fatalf("parseGitRevListAheadBehind(%q) = (%d,%d), want (%d,%d)", raw, gotBehind, gotAhead, behind, ahead)
		}
	})
}
