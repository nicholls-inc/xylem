package review

import (
	"math/rand"
	"testing"

	"pgregory.net/rapid"
)

func TestPropWorkflowHealthPatternFingerprintIgnoresAnomalyOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		unique := rapid.SampledFrom([]string{
			"budget_exceeded",
			"budget_warning",
			"gate_failed",
			"phase_failed",
			"run_failed",
			"timed_out",
			"waiting_on_gate",
		})
		count := rapid.IntRange(1, 5).Draw(t, "count")
		seen := make(map[string]struct{}, count)
		codes := make([]string, 0, count)
		for len(codes) < count {
			code := unique.Draw(t, "code")
			if _, ok := seen[code]; ok {
				continue
			}
			seen[code] = struct{}{}
			codes = append(codes, code)
		}

		permuted := append([]string(nil), codes...)
		rng := rand.New(rand.NewSource(int64(rapid.Int().Draw(t, "seed")))) //nolint:gosec
		rng.Shuffle(len(permuted), func(i, j int) {
			permuted[i], permuted[j] = permuted[j], permuted[i]
		})

		got := workflowHealthPatternFingerprint("build", codes, "implement", "suppressed")
		permutedGot := workflowHealthPatternFingerprint("build", permuted, "implement", "suppressed")
		if got != permutedGot {
			t.Fatalf("fingerprint changed across permutations: %q != %q", got, permutedGot)
		}
	})
}
