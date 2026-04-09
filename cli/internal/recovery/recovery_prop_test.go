package recovery

import (
	"strconv"
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
		retryCount := rapid.IntRange(0, 3).Draw(t, "retryCount")
		retryCap := rapid.IntRange(retryCount, retryCount+3).Draw(t, "retryCap")
		retryAfter := time.Unix(int64(rapid.Int64().Draw(t, "retryAfterUnix")), 0).UTC()

		artifact := &Artifact{
			VesselID:        vesselID,
			RecoveryClass:   ClassSpecGap,
			RecoveryAction:  ActionRefine,
			Rationale:       "needs clarification",
			FollowUpRoute:   "needs-refinement",
			RetrySuppressed: true,
			RetryCount:      retryCount,
			RetryCap:        retryCap,
			RetryAfter:      &retryAfter,
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
		if meta[MetaRetryCount] != strconv.Itoa(retryCount) {
			t.Fatalf("MetaRetryCount = %q, want %q", meta[MetaRetryCount], strconv.Itoa(retryCount))
		}
		if meta[MetaRetryCap] != strconv.Itoa(retryCap) {
			t.Fatalf("MetaRetryCap = %q, want %q", meta[MetaRetryCap], strconv.Itoa(retryCap))
		}
		if meta[MetaRetryAfter] != retryAfter.Format(time.RFC3339) {
			t.Fatalf("MetaRetryAfter = %q, want %q", meta[MetaRetryAfter], retryAfter.Format(time.RFC3339))
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
		if meta[MetaUnlockedBy] != unlockDimension {
			t.Fatalf("MetaUnlockedBy = %q, want %q", meta[MetaUnlockedBy], unlockDimension)
		}
	})
}

func TestPropRetryReadyNeverBypassesCapOrCooldown(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		retryCount := rapid.IntRange(0, 4).Draw(t, "retryCount")
		retryCap := rapid.IntRange(0, 4).Draw(t, "retryCap")
		offsetMinutes := rapid.IntRange(-30, 30).Draw(t, "offsetMinutes")
		now := time.Unix(0, 0).UTC()
		retryAfter := now.Add(time.Duration(offsetMinutes) * time.Minute)

		decision := RetryReady(&Artifact{
			RecoveryAction: ActionRetry,
			RetryCount:     retryCount,
			RetryCap:       retryCap,
			RetryAfter:     &retryAfter,
		}, now)

		want := retryCap == 0 || retryCount < retryCap
		if now.Before(retryAfter) {
			want = false
		}
		if decision.Eligible != want {
			t.Fatalf("RetryReady(retryCount=%d, retryCap=%d, retryAfter=%s) = %v, want %v", retryCount, retryCap, retryAfter.Format(time.RFC3339), decision.Eligible, want)
		}
	})
}
