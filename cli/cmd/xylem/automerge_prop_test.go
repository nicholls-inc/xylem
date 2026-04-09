package main

import (
	"testing"

	"pgregory.net/rapid"
)

func TestPropDecideAutoMergeActionMatchesMergeReadiness(t *testing.T) {
	settings := xylemAutoMergeSettings(t)
	rapid.Check(t, func(t *rapid.T) {
		hasReadyLabel := rapid.Bool().Draw(t, "hasReadyLabel")
		hasHarnessLabel := rapid.Bool().Draw(t, "hasHarnessLabel")
		isXylemBranch := rapid.Bool().Draw(t, "isXylemBranch")

		branch := "docs/not-xylem"
		if isXylemBranch {
			branch = "feat/issue-42-42"
		}

		var labels []struct {
			Name string `json:"name"`
		}
		if hasReadyLabel {
			labels = append(labels, struct {
				Name string `json:"name"`
			}{Name: "ready-to-merge"})
		}
		if hasHarnessLabel {
			labels = append(labels, struct {
				Name string `json:"name"`
			}{Name: "harness-impl"})
		}

		pr := prSummary{
			HeadRefName: branch,
			State:       "OPEN",
			Mergeable:   "MERGEABLE",
			Labels:      labels,
			StatusCheckRollup: []struct {
				Conclusion string `json:"conclusion"`
				Status     string `json:"status"`
			}{{Conclusion: "SUCCESS", Status: "COMPLETED"}},
			ReviewDecision: "APPROVED",
		}

		want := actionSkip
		if isXylemBranch && hasReadyLabel && hasHarnessLabel {
			want = actionRequestReview
		}

		if got := decideAutoMergeAction(pr, settings); got != want {
			t.Fatalf("decideAutoMergeAction(%+v) = %v, want %v", pr, got, want)
		}
	})
}

func TestPropDecideAutoMergeActionWaitsWhenAutoMergeAlreadyEnabled(t *testing.T) {
	settings := xylemAutoMergeSettings(t)
	rapid.Check(t, func(t *rapid.T) {
		reviewDecision := rapid.SampledFrom([]string{"", "REVIEW_REQUIRED", "APPROVED"}).Draw(t, "reviewDecision")
		withReviewRequest := rapid.Bool().Draw(t, "withReviewRequest")
		withLatestReview := rapid.Bool().Draw(t, "withLatestReview")

		pr := prSummary{
			HeadRefName:      "feat/issue-42-42",
			State:            "OPEN",
			Mergeable:        "MERGEABLE",
			ReviewDecision:   reviewDecision,
			AutoMergeRequest: &struct{}{},
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "ready-to-merge"}, {Name: "harness-impl"}},
			StatusCheckRollup: []struct {
				Conclusion string `json:"conclusion"`
				Status     string `json:"status"`
			}{{Conclusion: "SUCCESS", Status: "COMPLETED"}},
		}
		if withReviewRequest {
			pr.ReviewRequests = append(pr.ReviewRequests, struct {
				Login string `json:"login"`
			}{Login: settings.reviewer})
		}
		if withLatestReview {
			pr.LatestReviews = append(pr.LatestReviews, struct {
				Author struct {
					Login string `json:"login"`
				} `json:"author"`
				State string `json:"state"`
			}{
				Author: struct {
					Login string `json:"login"`
				}{Login: settings.reviewer},
				State: "COMMENTED",
			})
		}

		if got := decideAutoMergeAction(pr, settings); got != actionWaitForAutoMerge {
			t.Fatalf("expected wait-for-auto-merge, got %v for %+v", got, pr)
		}
	})
}
