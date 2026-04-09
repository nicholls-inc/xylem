package recovery

import (
	"testing"

	"pgregory.net/rapid"
)

func TestPropRemediationFingerprintStable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		source := rapid.String().Draw(t, "source")
		harness := rapid.String().Draw(t, "harness")
		workflow := rapid.String().Draw(t, "workflow")
		decision := rapid.String().Draw(t, "decision")
		epoch := rapid.String().Draw(t, "epoch")

		got := RemediationFingerprint(source, harness, workflow, decision, epoch)
		if got == "" {
			t.Fatal("RemediationFingerprint() = empty, want digest")
		}
		if again := RemediationFingerprint(source, harness, workflow, decision, epoch); again != got {
			t.Fatalf("RemediationFingerprint() unstable: first=%q second=%q", got, again)
		}
	})
}
