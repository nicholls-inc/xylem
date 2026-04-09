package main

import (
	"testing"

	"pgregory.net/rapid"
)

func TestPropDecideAutoMergeActionMatchesMergeReadiness(t *testing.T) {
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
			}{Name: readyToMergeLabel})
		}
		if hasHarnessLabel {
			labels = append(labels, struct {
				Name string `json:"name"`
			}{Name: harnessImplLabel})
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

		if got := decideAutoMergeAction(pr); got != want {
			t.Fatalf("decideAutoMergeAction(%+v) = %v, want %v", pr, got, want)
		}
	})
}

func TestPropDecideAutoMergeActionWaitsWhenAutoMergeAlreadyEnabled(t *testing.T) {
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
			}{{Name: readyToMergeLabel}, {Name: harnessImplLabel}},
			StatusCheckRollup: []struct {
				Conclusion string `json:"conclusion"`
				Status     string `json:"status"`
			}{{Conclusion: "SUCCESS", Status: "COMPLETED"}},
		}
		if withReviewRequest {
			pr.ReviewRequests = append(pr.ReviewRequests, struct {
				Login string `json:"login"`
			}{Login: copilotReviewerLogin})
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
				}{Login: copilotReviewerLogin},
				State: "COMMENTED",
			})
		}

		if got := decideAutoMergeAction(pr); got != actionWaitForAutoMerge {
			t.Fatalf("expected wait-for-auto-merge, got %v for %+v", got, pr)
		}
	})
}
