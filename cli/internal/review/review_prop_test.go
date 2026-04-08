package review

import (
	"testing"

	"pgregory.net/rapid"
)

func TestPropRecommendGroupCleanRunsAreMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		minSamples := rapid.IntRange(1, 6).Draw(t, "minSamples")
		samples := rapid.IntRange(0, minSamples*3).Draw(t, "samples")

		got, _ := recommendGroup(aggregateStats{Samples: samples}, minSamples)
		switch {
		case samples < minSamples && got != RecommendationInsufficientData:
			t.Fatalf("recommendGroup(%d,%d) = %q, want insufficient-data", samples, minSamples, got)
		case samples >= minSamples*2 && got != RecommendationPruneCandidate:
			t.Fatalf("recommendGroup(%d,%d) = %q, want prune-candidate", samples, minSamples, got)
		case samples >= minSamples && samples < minSamples*2 && got != RecommendationKeep:
			t.Fatalf("recommendGroup(%d,%d) = %q, want keep", samples, minSamples, got)
		}
	})
}
