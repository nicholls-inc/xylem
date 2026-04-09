package recovery

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"pgregory.net/rapid"
)

func TestPropApplyToMetaPreservesRecoveryFields(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		vesselID := rapid.StringMatching(`[a-z0-9-]{1,24}`).Draw(t, "vesselID")
		retryOutcome := rapid.SampledFrom([]string{"suppressed", "not_attempted", "enqueued"}).Draw(t, "retryOutcome")
		unlockDimension := rapid.SampledFrom([]string{"", "source", "workflow", "decision"}).Draw(t, "unlockDimension")

		artifact := &Artifact{
			VesselID:        vesselID,
			RecoveryClass:   ClassSpecGap,
			RecoveryAction:  ActionRefine,
			Rationale:       "needs clarification",
			FollowUpRoute:   "needs-refinement",
			RetrySuppressed: true,
			RetryOutcome:    retryOutcome,
			UnlockDimension: unlockDimension,
			State:           string(queue.StateFailed),
			CreatedAt:       time.Unix(0, 0).UTC(),
		}

		meta := ApplyToMeta(map[string]string{"keep": "me"}, artifact)
		if meta["keep"] != "me" {
			t.Fatalf("expected unrelated metadata to be preserved")
		}
		if meta[MetaClass] != string(artifact.RecoveryClass) {
			t.Fatalf("MetaClass = %q, want %q", meta[MetaClass], artifact.RecoveryClass)
		}
		if meta[MetaAction] != string(artifact.RecoveryAction) {
			t.Fatalf("MetaAction = %q, want %q", meta[MetaAction], artifact.RecoveryAction)
		}
		if meta[MetaRetrySuppressed] != strconv.FormatBool(artifact.RetrySuppressed) {
			t.Fatalf("MetaRetrySuppressed = %q, want %q", meta[MetaRetrySuppressed], strconv.FormatBool(artifact.RetrySuppressed))
		}
		if meta[MetaRetryOutcome] != retryOutcome {
			t.Fatalf("MetaRetryOutcome = %q, want %q", meta[MetaRetryOutcome], retryOutcome)
		}
		if unlockDimension == "" {
			if _, ok := meta[MetaUnlockDimension]; ok {
				t.Fatalf("MetaUnlockDimension should be absent when empty, got %q", meta[MetaUnlockDimension])
			}
			return
		}
		if meta[MetaUnlockDimension] != unlockDimension {
			t.Fatalf("MetaUnlockDimension = %q, want %q", meta[MetaUnlockDimension], unlockDimension)
		}
	})
}

func TestPropFailureFingerprintNormalizesWhitespaceAndCase(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		message := rapid.StringMatching(`[A-Za-z0-9 ]{1,48}`).Draw(t, "message")
		base := Input{
			Workflow:    "fix-bug",
			State:       queue.StateFailed,
			FailedPhase: "implement",
			Error:       message,
			GateOutput:  "gate output",
		}
		variant := base
		variant.Error = "  " + strings.ToUpper(message) + "   "

		got := failureFingerprint(base)
		want := failureFingerprint(variant)
		if got != want {
			t.Fatalf("failureFingerprint() = %q, want %q", got, want)
		}
	})
}
