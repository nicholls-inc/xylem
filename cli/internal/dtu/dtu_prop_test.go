package dtu

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

func TestPropRepositoryAutoMergeRespectsCheckStates(t *testing.T) {
	t.Parallel()

	checkStates := []CheckState{
		CheckStatePending,
		CheckStateSuccess,
		CheckStateFailure,
		CheckStateCancelled,
		CheckStateSkipped,
	}

	rapid.Check(t, func(t *rapid.T) {
		deleteHeadBranch := rapid.Bool().Draw(t, "delete_head_branch")
		adminMerge := rapid.Bool().Draw(t, "admin_merge")
		checkCount := rapid.IntRange(0, 5).Draw(t, "check_count")

		checks := make([]Check, 0, checkCount)
		blocked := false
		for i := 0; i < checkCount; i++ {
			state := rapid.SampledFrom(checkStates).Draw(t, fmt.Sprintf("check_state_%d", i))
			if state != CheckStateSuccess && state != CheckStateSkipped {
				blocked = true
			}
			checks = append(checks, Check{
				ID:    int64(i + 1),
				Name:  fmt.Sprintf("check-%d", i),
				State: state,
			})
		}

		repo := &Repository{
			Owner:         "owner",
			Name:          "repo",
			DefaultBranch: "main",
			Branches: []Branch{
				{Name: "main", SHA: "1111111111111111111111111111111111111111"},
				{Name: "feature/merge-me", SHA: "deadbeefcafebabe"},
			},
			PullRequests: []PullRequest{{
				Number:     7,
				Title:      "Merge me",
				State:      PullRequestStateOpen,
				BaseBranch: "main",
				HeadBranch: "feature/merge-me",
				HeadSHA:    "feedfacecafebeef",
				Checks:     checks,
			}},
		}

		if err := repo.MergePullRequest(7, MergePullRequestOptions{
			DeleteHeadBranch: deleteHeadBranch,
			AutoMerge:        true,
			Admin:            adminMerge,
		}); err != nil {
			t.Fatalf("MergePullRequest() error = %v", err)
		}

		pr := repo.PullRequestByNumber(7)
		if pr == nil {
			t.Fatal("PullRequestByNumber() = nil")
		}

		if blocked {
			if pr.State != PullRequestStateOpen || pr.Merged {
				t.Fatalf("queued pull request = %#v, want open queued auto-merge", pr)
			}
			if !pr.AutoMergeEnabled || pr.AutoMergeDeleteBranch != deleteHeadBranch || pr.AutoMergeAdmin != adminMerge {
				t.Fatalf("queued pull request flags = %#v, want auto-merge enabled with delete=%t admin=%t", pr, deleteHeadBranch, adminMerge)
			}
			if branch := repo.BranchByName("main"); branch == nil || branch.SHA != "1111111111111111111111111111111111111111" {
				t.Fatalf("main branch = %#v, want original SHA preserved", branch)
			}
			if branch := repo.BranchByName(pr.HeadBranch); branch == nil {
				t.Fatalf("head branch = %#v, want retained while merge is queued", branch)
			}

			for i := range pr.Checks {
				pr.Checks[i].State = CheckStateSuccess
			}
			if err := repo.ApplyQueuedAutoMerge(7); err != nil {
				t.Fatalf("ApplyQueuedAutoMerge() error = %v", err)
			}
		}

		if pr.State != PullRequestStateMerged || !pr.Merged {
			t.Fatalf("merged pull request = %#v, want merged state", pr)
		}
		if pr.AutoMergeEnabled || pr.AutoMergeDeleteBranch || pr.AutoMergeAdmin {
			t.Fatalf("merged pull request flags = %#v, want cleared auto-merge flags", pr)
		}
		if pr.MergedByAdmin != adminMerge {
			t.Fatalf("MergedByAdmin = %t, want %t", pr.MergedByAdmin, adminMerge)
		}
		if branch := repo.BranchByName("main"); branch == nil || branch.SHA != pr.HeadSHA {
			t.Fatalf("main branch = %#v, want SHA %q", branch, pr.HeadSHA)
		}
		if deleteHeadBranch {
			if branch := repo.BranchByName(pr.HeadBranch); branch != nil {
				t.Fatalf("head branch = %#v, want deleted", branch)
			}
		} else {
			if branch := repo.BranchByName(pr.HeadBranch); branch == nil || branch.SHA != pr.HeadSHA {
				t.Fatalf("head branch = %#v, want retained at SHA %q", branch, pr.HeadSHA)
			}
		}
	})
}
