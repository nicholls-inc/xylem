package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// MergeTask defines a task triggered by merged PRs.
type MergeTask struct {
	Workflow string
}

// GitHubMerge scans for merged PRs and produces vessels.
type GitHubMerge struct {
	Repo      string
	Tasks     map[string]MergeTask
	Queue     *queue.Queue
	CmdRunner CommandRunner
}

type ghMergeCommit struct {
	OID string `json:"oid"`
}

type ghMergedPR struct {
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	URL         string        `json:"url"`
	MergeCommit ghMergeCommit `json:"mergeCommit"`
	HeadRefName string        `json:"headRefName"`
}

func (g *GitHubMerge) Name() string { return "github-merge" }

func (g *GitHubMerge) Scan(ctx context.Context) ([]queue.Vessel, error) {
	args := []string{
		"pr", "list",
		"--repo", g.Repo,
		"--state", "merged",
		"--json", "number,title,url,mergeCommit,headRefName",
		"--limit", "20",
	}
	out, err := g.CmdRunner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh pr list (merged): %w", err)
	}

	var prs []ghMergedPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}

	var vessels []queue.Vessel

	for _, pr := range prs {
		oid := strings.TrimSpace(pr.MergeCommit.OID)
		if oid == "" {
			continue
		}

		for _, task := range g.Tasks {
			ref := fmt.Sprintf("%s#merge-%s", pr.URL, oid)
			if g.Queue.HasRefAny(ref) {
				continue
			}
			vessels = append(vessels, queue.Vessel{
				ID:       fmt.Sprintf("merge-pr-%d-%s", pr.Number, oid[:minLen(len(oid), 8)]),
				Source:   "github-merge",
				Ref:      ref,
				Workflow: task.Workflow,
				Meta: map[string]string{
					"pr_num":         strconv.Itoa(pr.Number),
					"event_type":     "merge",
					"pr_head_branch": pr.HeadRefName,
				},
				State:     queue.StatePending,
				CreatedAt: sourceNow(),
			})
		}
	}

	return vessels, nil
}

func (g *GitHubMerge) OnEnqueue(_ context.Context, _ queue.Vessel) error  { return nil }
func (g *GitHubMerge) OnStart(_ context.Context, _ queue.Vessel) error    { return nil }
func (g *GitHubMerge) OnComplete(_ context.Context, _ queue.Vessel) error { return nil }
func (g *GitHubMerge) OnFail(_ context.Context, _ queue.Vessel) error     { return nil }
func (g *GitHubMerge) OnTimedOut(_ context.Context, _ queue.Vessel) error { return nil }

func (g *GitHubMerge) BranchName(vessel queue.Vessel) string {
	prNum := vessel.Meta["pr_num"]
	slug := slugify(vessel.Ref)
	return fmt.Sprintf("merge/pr-%s-%s", prNum, slug)
}
