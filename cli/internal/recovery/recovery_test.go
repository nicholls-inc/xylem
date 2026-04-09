package recovery

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func TestEvaluateRetryUnlocksByDimension(t *testing.T) {
	now := time.Date(2026, time.April, 9, 18, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)

	tests := []struct {
		name       string
		review     *FailureReview
		source     string
		harness    string
		workflow   string
		wantAllow  bool
		wantUnlock string
	}{
		{
			name: "source",
			review: &FailureReview{
				RecommendedAction: "retry",
				RetryCap:          2,
				Unlock: UnlockFingerprint{
					SourceInputFingerprint: "source-a",
					HarnessDigest:          "harness-a",
					WorkflowDigest:         "workflow-a",
				},
			},
			source:     "source-b",
			harness:    "harness-a",
			workflow:   "workflow-a",
			wantAllow:  true,
			wantUnlock: "source",
		},
		{
			name: "harness",
			review: &FailureReview{
				RecommendedAction: "retry",
				RetryCap:          2,
				Unlock: UnlockFingerprint{
					SourceInputFingerprint: "source-a",
					HarnessDigest:          "harness-a",
					WorkflowDigest:         "workflow-a",
				},
			},
			source:     "source-a",
			harness:    "harness-b",
			workflow:   "workflow-a",
			wantAllow:  true,
			wantUnlock: "harness",
		},
		{
			name: "workflow",
			review: &FailureReview{
				RecommendedAction: "retry",
				RetryCap:          2,
				Unlock: UnlockFingerprint{
					SourceInputFingerprint: "source-a",
					HarnessDigest:          "harness-a",
					WorkflowDigest:         "workflow-a",
				},
			},
			source:     "source-a",
			harness:    "harness-a",
			workflow:   "workflow-b",
			wantAllow:  true,
			wantUnlock: "workflow",
		},
		{
			name: "decision",
			review: &FailureReview{
				RecommendedAction: "retry",
				RetryCap:          2,
				Hypothesis:        "new hypothesis",
				Unlock: UnlockFingerprint{
					SourceInputFingerprint: "source-a",
					HarnessDigest:          "harness-a",
					WorkflowDigest:         "workflow-a",
					DecisionDigest:         "old-decision",
				},
			},
			source:     "source-a",
			harness:    "harness-a",
			workflow:   "workflow-a",
			wantAllow:  true,
			wantUnlock: "decision",
		},
		{
			name: "cooldown",
			review: &FailureReview{
				RecommendedAction: "retry",
				RetryCap:          2,
				RetryAfter:        &past,
				Unlock: UnlockFingerprint{
					SourceInputFingerprint: "source-a",
					HarnessDigest:          "harness-a",
					WorkflowDigest:         "workflow-a",
				},
			},
			source:     "source-a",
			harness:    "harness-a",
			workflow:   "workflow-a",
			wantAllow:  true,
			wantUnlock: "cooldown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previousDecisionDigest := tt.review.Unlock.DecisionDigest
			if previousDecisionDigest == "" {
				previousDecisionDigest = DecisionDigest(tt.review)
				tt.review.Unlock.DecisionDigest = previousDecisionDigest
			}
			tt.review.RemediationFingerprint = RemediationFingerprint(
				tt.review.Unlock.SourceInputFingerprint,
				tt.review.Unlock.HarnessDigest,
				tt.review.Unlock.WorkflowDigest,
				previousDecisionDigest,
				tt.review.RemediationEpoch,
			)

			got := EvaluateRetry(tt.review, tt.source, tt.harness, tt.workflow, now)
			if got.Allowed != tt.wantAllow {
				t.Fatalf("Allowed = %v, want %v", got.Allowed, tt.wantAllow)
			}
			if got.UnlockedBy != tt.wantUnlock {
				t.Fatalf("UnlockedBy = %q, want %q", got.UnlockedBy, tt.wantUnlock)
			}
		})
	}
}

func TestEvaluateRetryBlocksPendingCooldownAndExhaustedCap(t *testing.T) {
	now := time.Date(2026, time.April, 9, 18, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)

	tests := []struct {
		name   string
		review *FailureReview
	}{
		{
			name: "cooldown pending",
			review: &FailureReview{
				RecommendedAction: "retry",
				RetryCap:          2,
				RetryAfter:        &future,
				Unlock: UnlockFingerprint{
					SourceInputFingerprint: "source-a",
					HarnessDigest:          "harness-a",
					WorkflowDigest:         "workflow-a",
				},
			},
		},
		{
			name: "retry cap exhausted",
			review: &FailureReview{
				RecommendedAction: "retry",
				RetryCap:          1,
				RetryCount:        1,
				Unlock: UnlockFingerprint{
					SourceInputFingerprint: "source-a",
					HarnessDigest:          "harness-a",
					WorkflowDigest:         "workflow-a",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.review.Unlock.DecisionDigest = DecisionDigest(tt.review)
			tt.review.RemediationFingerprint = RemediationFingerprint(
				tt.review.Unlock.SourceInputFingerprint,
				tt.review.Unlock.HarnessDigest,
				tt.review.Unlock.WorkflowDigest,
				tt.review.Unlock.DecisionDigest,
				tt.review.RemediationEpoch,
			)

			got := EvaluateRetry(tt.review, "source-b", "harness-b", "workflow-b", now)
			if got.Allowed {
				t.Fatal("Allowed = true, want blocked")
			}
			if got.UnlockedBy != "" {
				t.Fatalf("UnlockedBy = %q, want empty when blocked", got.UnlockedBy)
			}
			if got.CurrentFingerprint == "" {
				t.Fatal("CurrentFingerprint = empty, want computed fingerprint even when blocked")
			}
		})
	}
}

func TestEvaluateRetryReturnsZeroValueForNilReview(t *testing.T) {
	got := EvaluateRetry(nil, "source-a", "harness-a", "workflow-a", time.Now().UTC())
	if got != (RetryGateResult{}) {
		t.Fatalf("EvaluateRetry(nil) = %#v, want zero value", got)
	}
}

func TestSaveFailureReviewPopulatesDerivedFields(t *testing.T) {
	stateDir := t.TempDir()
	review := &FailureReview{
		VesselID:           "issue-42",
		RecommendedAction:  "retry",
		RetryCap:           2,
		EvidencePaths:      []string{filepath.Join("phases", "issue-42", "summary.json"), " phases/issue-42/log.txt "},
		FailureFingerprint: "fail-123",
		Unlock: UnlockFingerprint{
			SourceInputFingerprint: "source-a",
			HarnessDigest:          "harness-a",
			WorkflowDigest:         "workflow-a",
		},
	}

	if err := SaveFailureReview(stateDir, review); err != nil {
		t.Fatalf("SaveFailureReview() error = %v", err)
	}

	loaded, err := LoadFailureReview(stateDir, "issue-42")
	if err != nil {
		t.Fatalf("LoadFailureReview() error = %v", err)
	}
	if loaded.Unlock.DecisionDigest == "" {
		t.Fatal("DecisionDigest = empty, want populated")
	}
	if loaded.RemediationFingerprint == "" {
		t.Fatal("RemediationFingerprint = empty, want populated")
	}
	if got, want := loaded.EvidencePaths, []string{"phases/issue-42/log.txt", "phases/issue-42/summary.json"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("EvidencePaths = %#v, want %#v", got, want)
	}
	if got, want := FailureReviewPath(stateDir, "issue-42"), filepath.Join(stateDir, "phases", "issue-42", FailureReviewFileName); got != want {
		t.Fatalf("FailureReviewPath() = %q, want %q", got, want)
	}
}

func TestRetryHelpersUseOriginalRootSequence(t *testing.T) {
	stateDir := t.TempDir()
	q := queue.New(filepath.Join(stateDir, "queue.jsonl"))
	if _, err := q.Enqueue(queue.Vessel{ID: "issue-42", Ref: "ref", State: queue.StateFailed}); err != nil {
		t.Fatalf("Enqueue(root) error = %v", err)
	}
	if _, err := q.Enqueue(queue.Vessel{ID: "issue-42-retry-1", Ref: "ref", State: queue.StateFailed}); err != nil {
		t.Fatalf("Enqueue(retry) error = %v", err)
	}
	if got := RetryID("issue-42", q); got != "issue-42-retry-2" {
		t.Fatalf("RetryID() = %q, want issue-42-retry-2", got)
	}
	if got := RetryRootID(queue.Vessel{
		ID:      "issue-42-retry-1",
		RetryOf: "issue-42",
		Meta:    map[string]string{"retry_of": "issue-42"},
	}); got != "issue-42" {
		t.Fatalf("RetryRootID() = %q, want issue-42", got)
	}
	if got := RetryCountFromVessel(queue.Vessel{ID: "issue-42-retry-3"}); got != 3 {
		t.Fatalf("RetryCountFromVessel() = %d, want 3", got)
	}
}

func TestRetryIDContinuesRootSequenceForNestedRetries(t *testing.T) {
	stateDir := t.TempDir()
	q := queue.New(filepath.Join(stateDir, "queue.jsonl"))

	_, _ = q.Enqueue(queue.Vessel{ID: "issue-42", Ref: "ref", State: queue.StateFailed})
	_, _ = q.Enqueue(queue.Vessel{
		ID:      "issue-42-retry-1",
		Ref:     "ref",
		State:   queue.StateFailed,
		RetryOf: "issue-42",
		Meta:    map[string]string{"retry_of": "issue-42", "retry_count": "1"},
	})

	prior := queue.Vessel{
		ID:      "issue-42-retry-1",
		RetryOf: "issue-42",
		Meta:    map[string]string{"retry_of": "issue-42", "retry_count": "1"},
	}
	if got := RetryID(RetryRootID(prior), q); got != "issue-42-retry-2" {
		t.Fatalf("RetryID(RetryRootID(prior)) = %q, want issue-42-retry-2", got)
	}
}
