package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsBenignGhWarning(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not benign", nil, false},
		{"plain error is not benign", errors.New("exit status 1"), false},
		{"projects classic deprecation is benign",
			errors.New("exit status 1: GraphQL: Projects (classic) is being deprecated in favor of the new Projects experience"), true},
		{"projectCards reference is benign",
			errors.New("exit status 1: error in projectCards query"), true},
		{"unrelated graphql error is not benign",
			errors.New("exit status 1: GraphQL: not found"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBenignGhWarning(tt.err); got != tt.want {
				t.Errorf("isBenignGhWarning(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsReviewerNotCollaborator(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not a collaborator error", nil, false},
		{"plain error is not a collaborator error", errors.New("exit status 1: HTTP 500"), false},
		{"422 not a collaborator is detected",
			errors.New(`exit status 1: {"message":"Reviews may only be requested from collaborators. One or more of the users or teams you specified is not a collaborator of the nicholls-inc/xylem repository.","documentation_url":"..."}`),
			true},
		{"bare phrase is detected",
			errors.New("Reviews may only be requested from collaborators"),
			true},
		{"benign projects warning is not a collaborator error",
			errors.New("exit status 1: Projects (classic) is being deprecated"), false},
		{"unrelated 422 is not a collaborator error",
			errors.New(`exit status 1: {"message":"Validation failed"}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReviewerNotCollaborator(tt.err); got != tt.want {
				t.Errorf("isReviewerNotCollaborator(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestGhErrorPredicatesDisjoint asserts that the benign-warning and
// reviewer-not-collaborator predicates never match the same error, so
// the actionRequestReview switch branches cannot both fire.
func TestGhErrorPredicatesDisjoint(t *testing.T) {
	samples := []error{
		nil,
		errors.New("exit status 1"),
		errors.New("exit status 1: Projects (classic) is being deprecated"),
		errors.New("exit status 1: Reviews may only be requested from collaborators"),
		errors.New("exit status 1: projectCards query failed"),
		errors.New("exit status 1: HTTP 500"),
	}
	for _, e := range samples {
		if isBenignGhWarning(e) && isReviewerNotCollaborator(e) {
			t.Errorf("predicates overlap for error: %v", e)
		}
	}
}

func TestPRSummary_HasLabel(t *testing.T) {
	pr := prSummary{
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "needs-conflict-resolution"}, {Name: harnessImplLabel}},
	}
	if !pr.hasLabel("needs-conflict-resolution") {
		t.Error("hasLabel('needs-conflict-resolution') = false, want true")
	}
	if !pr.hasLabel(harnessImplLabel) {
		t.Errorf("hasLabel(%q) = false, want true", harnessImplLabel)
	}
	if pr.hasLabel("nonexistent") {
		t.Error("hasLabel('nonexistent') = true, want false")
	}
}

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
	mergeReadyLabels := []struct {
		Name string `json:"name"`
	}{{Name: readyToMergeLabel}, {Name: harnessImplLabel}}
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
			name: "xylem PR without merge-ready labels is skipped",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1",
				State:       "OPEN",
				Mergeable:   "MERGEABLE",
				Labels: []struct {
					Name string `json:"name"`
				}{{Name: harnessImplLabel}},
			},
			want: actionSkip,
		},
		{
			name: "xylem PR with conflicts and no conflict labels is routed to resolve-conflicts",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1",
				State:       "OPEN",
				Mergeable:   "CONFLICTING",
				Labels:      mergeReadyLabels,
			},
			want: actionRouteConflict,
		},
		{
			name: "xylem PR with conflicts and resolve-conflicts labels waits",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "CONFLICTING",
				Labels: []struct {
					Name string `json:"name"`
				}{{Name: "needs-conflict-resolution"}, {Name: readyToMergeLabel}, {Name: harnessImplLabel}},
			},
			want: actionWaitForMergeable,
		},
		{
			name: "xylem PR with unknown mergeable waits",
			pr:   prSummary{HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "UNKNOWN", Labels: mergeReadyLabels},
			want: actionWaitForMergeable,
		},
		{
			name: "xylem PR with CI failing waits",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				Labels: mergeReadyLabels,
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
				Labels: mergeReadyLabels, StatusCheckRollup: greenChecks, ReviewDecision: "CHANGES_REQUESTED",
			},
			want: actionAddressReview,
		},
		{
			name: "xylem PR without review requests asks for copilot review first",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				Labels: mergeReadyLabels, StatusCheckRollup: greenChecks, ReviewDecision: "REVIEW_REQUIRED",
			},
			want: actionRequestReview,
		},
		{
			name: "xylem PR with copilot requested enables auto-merge",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				Labels: mergeReadyLabels, StatusCheckRollup: greenChecks, ReviewDecision: "REVIEW_REQUIRED",
				ReviewRequests: []struct {
					Login string `json:"login"`
				}{{Login: copilotReviewerLogin}},
			},
			want: actionEnableAutoMerge,
		},
		{
			name: "xylem PR approved + green + mergeable enables auto-merge",
			pr: prSummary{
				HeadRefName: "feat/issue-1-1", State: "OPEN", Mergeable: "MERGEABLE",
				Labels: mergeReadyLabels, StatusCheckRollup: greenChecks, ReviewDecision: "APPROVED",
				LatestReviews: copilotReviewed,
			},
			want: actionEnableAutoMerge,
		},
		{
			name: "xylem PR with auto-merge already enabled waits",
			pr: prSummary{
				HeadRefName:       "feat/issue-1-1",
				State:             "OPEN",
				Mergeable:         "MERGEABLE",
				ReviewDecision:    "REVIEW_REQUIRED",
				AutoMergeRequest:  &struct{}{},
				Labels:            mergeReadyLabels,
				StatusCheckRollup: greenChecks,
			},
			want: actionWaitForAutoMerge,
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

func TestSmoke_S1_AutoMergeContinuesWhenCopilotReviewerIsNotCollaborator(t *testing.T) {
	origListOpenPRsFn := listOpenPRsFn
	origRequestCopilotReviewFn := requestCopilotReviewFn
	origAddPRLabelsFn := addPRLabelsFn
	origEnableAutoMergePRFn := enableAutoMergePRFn
	t.Cleanup(func() {
		listOpenPRsFn = origListOpenPRsFn
		requestCopilotReviewFn = origRequestCopilotReviewFn
		addPRLabelsFn = origAddPRLabelsFn
		enableAutoMergePRFn = origEnableAutoMergePRFn
	})

	mergeReadyPR := prSummary{
		Number:         42,
		HeadRefName:    "feat/issue-42-42",
		Mergeable:      "MERGEABLE",
		State:          "OPEN",
		ReviewDecision: "REVIEW_REQUIRED",
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: readyToMergeLabel}, {Name: harnessImplLabel}},
		StatusCheckRollup: []struct {
			Conclusion string `json:"conclusion"`
			Status     string `json:"status"`
		}{{Conclusion: "SUCCESS", Status: "COMPLETED"}},
	}
	require.Equal(t, actionRequestReview, decideAutoMergeAction(mergeReadyPR))

	listCalls := 0
	listOpenPRsFn = func(context.Context, string) ([]prSummary, error) {
		listCalls++
		return []prSummary{{
			Number:            mergeReadyPR.Number,
			HeadRefName:       mergeReadyPR.HeadRefName,
			Mergeable:         mergeReadyPR.Mergeable,
			State:             mergeReadyPR.State,
			ReviewDecision:    mergeReadyPR.ReviewDecision,
			Labels:            mergeReadyPR.Labels,
			StatusCheckRollup: mergeReadyPR.StatusCheckRollup,
		}}, nil
	}

	reviewCalls := 0
	requestCopilotReviewFn = func(_ context.Context, repo string, number int) error {
		reviewCalls++
		assert.Equal(t, "nicholls-inc/xylem", repo)
		assert.Equal(t, 42, number)
		return errors.New(`exit status 1: {"message":"Reviews may only be requested from collaborators"}`)
	}

	labelCalls := 0
	addPRLabelsFn = func(context.Context, string, int, []string) error {
		labelCalls++
		return nil
	}

	enableCalls := 0
	enableAutoMergePRFn = func(_ context.Context, repo string, number int) error {
		enableCalls++
		if repo != "nicholls-inc/xylem" {
			t.Fatalf("repo = %q, want nicholls-inc/xylem", repo)
		}
		if number != 42 {
			t.Fatalf("number = %d, want 42", number)
		}
		return nil
	}

	autoMergeXylemPRs(context.Background(), "nicholls-inc/xylem")

	assert.Equal(t, 1, listCalls)
	assert.Equal(t, 1, reviewCalls)
	assert.Equal(t, 0, labelCalls)
	assert.Equal(t, 1, enableCalls)
}
