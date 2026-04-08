package main

import "testing"

func TestXylemBranchPattern(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"feat/issue-42-42", true},
		{"feat/issue-60-60-runner-context", true},
		{"fix/issue-99-99", true},
		{"chore/issue-1-1", true},
		{"main", false},
		{"release-please--branches--main--components--xylem", false},
		{"worktree-agent-abc", false},
		{"docs/smoke-scenarios-unit-1", false},
		{"feat/self-healing-daemon", false},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			got := xylemBranchPattern.MatchString(tt.branch)
			if got != tt.want {
				t.Errorf("xylemBranchPattern.MatchString(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

func TestAllChecksGreen(t *testing.T) {
	mkcheck := func(conclusion, status string) struct {
		Conclusion string `json:"conclusion"`
		Status     string `json:"status"`
	} {
		return struct {
			Conclusion string `json:"conclusion"`
			Status     string `json:"status"`
		}{Conclusion: conclusion, Status: status}
	}
	type checkT = struct {
		Conclusion string `json:"conclusion"`
		Status     string `json:"status"`
	}
	tests := []struct {
		name  string
		rolls []checkT
		want  bool
	}{
		{name: "no checks", want: true},
		{name: "all success", rolls: []checkT{mkcheck("SUCCESS", "COMPLETED"), mkcheck("SUCCESS", "COMPLETED")}, want: true},
		{name: "neutral and skipped allowed", rolls: []checkT{mkcheck("NEUTRAL", "COMPLETED"), mkcheck("SKIPPED", "COMPLETED")}, want: true},
		{name: "failure blocks", rolls: []checkT{mkcheck("SUCCESS", "COMPLETED"), mkcheck("FAILURE", "COMPLETED")}, want: false},
		{name: "still running blocks", rolls: []checkT{mkcheck("", "IN_PROGRESS")}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := prSummary{StatusCheckRollup: tt.rolls}
			if got := allChecksGreen(pr); got != tt.want {
				t.Errorf("allChecksGreen() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecideAutoMergeAction(t *testing.T) {
	greenChecks := []struct {
		Conclusion string `json:"conclusion"`
		Status     string `json:"status"`
	}{{Conclusion: "SUCCESS", Status: "COMPLETED"}}
	copilotReviewed := []struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State string `json:"state"`
	}{{
		Author: struct {
			Login string `json:"login"`
		}{Login: copilotReviewerLogin},
		State: "APPROVED",
	}}

	tests := []struct {
		name string
		pr   prSummary
		want autoMergeAction
	}{
		{
			name: "non-xylem branch is skipped",
			pr:   prSummary{HeadRefName: "main", State: "OPEN"},
			want: actionSkip,
		},
		{
			name: "xylem PR with conflicts waits",
			pr:   prSummary{HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "CONFLICTING"},
			want: actionWaitForMergeable,
		},
		{
			name: "xylem PR with unknown mergeable waits",
			pr:   prSummary{HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "UNKNOWN"},
			want: actionWaitForMergeable,
		},
		{
			name: "xylem PR with CI failing waits",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				StatusCheckRollup: []struct {
					Conclusion string `json:"conclusion"`
					Status     string `json:"status"`
				}{{Conclusion: "FAILURE", Status: "COMPLETED"}},
			},
			want: actionWaitForChecks,
		},
		{
			name: "xylem PR with changes requested waits",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				StatusCheckRollup: greenChecks, ReviewDecision: "CHANGES_REQUESTED",
			},
			want: actionAddressReview,
		},
		{
			name: "xylem PR without review requests copilot review",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				StatusCheckRollup: greenChecks, ReviewDecision: "REVIEW_REQUIRED",
			},
			want: actionRequestReview,
		},
		{
			name: "xylem PR with copilot requested but not submitted waits",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				StatusCheckRollup: greenChecks, ReviewDecision: "REVIEW_REQUIRED",
				ReviewRequests: []struct {
					Login string `json:"login"`
				}{{Login: copilotReviewerLogin}},
			},
			want: actionWaitForReview,
		},
		{
			name: "xylem PR approved + green + mergeable is merged",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				StatusCheckRollup: greenChecks, ReviewDecision: "APPROVED",
				LatestReviews: copilotReviewed,
			},
			want: actionMerge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideAutoMergeAction(tt.pr); got != tt.want {
				t.Errorf("decideAutoMergeAction() = %v, want %v", got, tt.want)
			}
		})
	}
}
