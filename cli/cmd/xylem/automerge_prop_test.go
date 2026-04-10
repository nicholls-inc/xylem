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

func TestPropDecideAutoMergeActionAdminMergesWithReviewerEvidence(t *testing.T) {
	settings := xylemAutoMergeSettings(t)
	rapid.Check(t, func(t *rapid.T) {
		reviewDecision := rapid.SampledFrom([]string{"", "REVIEW_REQUIRED", "APPROVED"}).Draw(t, "reviewDecision")
		reviewEvidence := rapid.SampledFrom([]string{"request", "latest-review"}).Draw(t, "reviewEvidence")

		pr := prSummary{
			HeadRefName:    "feat/issue-42-42",
			State:          "OPEN",
			Mergeable:      "MERGEABLE",
			ReviewDecision: reviewDecision,
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "ready-to-merge"}, {Name: "harness-impl"}},
			StatusCheckRollup: []struct {
				Conclusion string `json:"conclusion"`
				Status     string `json:"status"`
			}{{Conclusion: "SUCCESS", Status: "COMPLETED"}},
		}
		switch reviewEvidence {
		case "request":
			pr.ReviewRequests = []struct {
				Login string `json:"login"`
			}{{Login: settings.reviewer}}
		case "latest-review":
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
		default:
			t.Fatalf("unexpected review evidence %q", reviewEvidence)
		}

		if got := decideAutoMergeAction(pr, settings); got != actionAdminMerge {
			t.Fatalf("expected admin-merge, got %v for %+v", got, pr)
		}
	})
}

func TestPropDecideAutoMergeActionOptOutLabelAlwaysBlocks(t *testing.T) {
	settings := xylemAutoMergeSettings(t)
	rapid.Check(t, func(t *rapid.T) {
		pr := prSummary{
			HeadRefName:    "feat/issue-42-42",
			State:          "OPEN",
			Mergeable:      "MERGEABLE",
			ReviewDecision: "APPROVED",
			Labels: []struct {
				Name string `json:"name"`
			}{{Name: "ready-to-merge"}, {Name: "harness-impl"}, {Name: settings.optOutLabel}},
			StatusCheckRollup: []struct {
				Conclusion string `json:"conclusion"`
				Status     string `json:"status"`
			}{{Conclusion: "SUCCESS", Status: "COMPLETED"}},
		}

		if got := decideAutoMergeAction(pr, settings); got != actionBlockedOptOut {
			t.Fatalf("expected opt-out block, got %v for %+v", got, pr)
		}
	})
}
