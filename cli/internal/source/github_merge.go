package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// MergeTask defines a workflow triggered by a merge.
type MergeTask struct {
	Workflow string
}

// GitHubMerge scans for recently merged pull requests.
type GitHubMerge struct {
	Repo      string
	Tasks     map[string]MergeTask
	Queue     *queue.Queue
	CmdRunner CommandRunner
}

type ghMergedPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	MergeCommit struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
}

func (g *GitHubMerge) Name() string { return "github-merge" }

func (g *GitHubMerge) Scan(ctx context.Context) ([]queue.Vessel, error) {
	out, err := g.CmdRunner.Run(ctx, "gh", "pr", "list",
		"--repo", g.Repo,
		"--state", "merged",
		"--json", "number,title,url,mergeCommit",
		"--limit", "20",
	)
	if err != nil {
		return nil, fmt.Errorf("gh pr list (merged): %w", err)
	}

	var prs []ghMergedPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}

	var vessels []queue.Vessel
	seen := make(map[int]bool)

	for _, pr := range prs {
		if seen[pr.Number] {
			continue
		}
		seen[pr.Number] = true

		// Use merge commit OID in ref to prevent re-processing after compaction
		ref := fmt.Sprintf("%s#merge-%s", pr.URL, pr.MergeCommit.Oid)
		if g.Queue.HasRefAny(ref) {
			continue
		}

		for _, task := range g.Tasks {
			vessels = append(vessels, queue.Vessel{
				ID:       fmt.Sprintf("merge-%d-%s", pr.Number, shortMergeHash(pr.MergeCommit.Oid)),
				Source:   "github-merge",
				Ref:      ref,
				Workflow: task.Workflow,
				Meta: map[string]string{
					"pr_num":       strconv.Itoa(pr.Number),
					"merge_commit": pr.MergeCommit.Oid,
				},
				State:     queue.StatePending,
				CreatedAt: time.Now().UTC(),
			})
		}
	}
	return vessels, nil
}

func (g *GitHubMerge) OnStart(ctx context.Context, vessel queue.Vessel) error {
	return nil
}

func (g *GitHubMerge) BranchName(vessel queue.Vessel) string {
	prNum := vessel.Meta["pr_num"]
	slug := slugify(vessel.Ref)
	return fmt.Sprintf("merge/pr-%s-%s", prNum, slug)
}

// shortMergeHash returns the first 8 characters of a commit SHA.
func shortMergeHash(oid string) string {
	if len(oid) > 8 {
		return oid[:8]
	}
	return oid
}
