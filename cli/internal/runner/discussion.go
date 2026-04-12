package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/discussion"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

// GraphQL constants kept as package-level aliases so that tests in this
// package can match mock hook calls against the exact query text sent by
// discussion.Publisher.
const (
	discussionResolveQuery = `query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
    id
    discussionCategories(first: 25) { nodes { id, name } }
  }
}`
	discussionSearchQuery = `query($repoId: ID!, $catId: ID!) {
  node(id: $repoId) {
    ... on Repository {
      discussions(first: 20, categoryId: $catId, orderBy: {field: CREATED_AT, direction: DESC}) {
        nodes { id, title, url }
      }
    }
  }
}`
	discussionCreateMutation = `mutation($repoId: ID!, $catId: ID!, $title: String!, $body: String!) {
  createDiscussion(input: {repositoryId: $repoId, title: $title, body: $body, categoryId: $catId}) {
    discussion { id, title, url }
  }
}`
	discussionCommentMutation = `mutation($discussionId: ID!, $body: String!) {
  addDiscussionComment(input: {discussionId: $discussionId, body: $body}) {
    comment { url }
  }
}`
)

type discussionPublisher struct {
	pub *discussion.Publisher
}

func (r *Runner) publishPhaseOutput(ctx context.Context, vessel queue.Vessel, p workflow.Phase, td phase.TemplateData, body string) error {
	if p.Output == "" || phaseMatchedNoOp(&p, body) {
		return nil
	}

	switch p.Output {
	case "discussion":
		repoSlug := r.resolveRepo(vessel)
		dp := discussionPublisher{pub: &discussion.Publisher{Runner: r.Runner}}
		if _, err := dp.Publish(ctx, repoSlug, p.Name, p.Discussion, td, body); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("phase %s: unsupported output target %q", p.Name, p.Output)
	}
}

func (dp discussionPublisher) Publish(ctx context.Context, repoSlug string, phaseName string, cfg *workflow.DiscussionOutput, td phase.TemplateData, body string) (string, error) {
	if dp.pub == nil || dp.pub.Runner == nil {
		return "", fmt.Errorf("publish discussion for phase %s: runner is required", phaseName)
	}
	if cfg == nil {
		return "", fmt.Errorf("publish discussion for phase %s: discussion config is required", phaseName)
	}

	owner, repo, err := discussion.SplitRepoSlug(repoSlug)
	if err != nil {
		return "", fmt.Errorf("publish discussion for phase %s: %w", phaseName, err)
	}

	title, err := renderDiscussionTemplate(phaseName, "discussion.title_template", cfg.TitleTemplate, td)
	if err != nil {
		return "", err
	}
	titleSearch := title
	if strings.TrimSpace(cfg.TitleSearchTemplate) != "" {
		titleSearch, err = renderDiscussionTemplate(phaseName, "discussion.title_search_template", cfg.TitleSearchTemplate, td)
		if err != nil {
			return "", err
		}
	}

	target, err := dp.pub.ResolveTarget(ctx, owner, repo, cfg.Category)
	if err != nil {
		return "", fmt.Errorf("publish discussion for phase %s: %w", phaseName, err)
	}

	existing, err := dp.pub.FindExisting(ctx, target.RepoID, target.CategoryID, titleSearch)
	if err != nil {
		return "", fmt.Errorf("publish discussion for phase %s: %w", phaseName, err)
	}
	if existing.ID != "" {
		if err := dp.pub.Comment(ctx, existing.ID, body); err != nil {
			return "", fmt.Errorf("publish discussion for phase %s: comment existing discussion %q: %w", phaseName, existing.Title, err)
		}
		return existing.URL, nil
	}

	created, err := dp.pub.Create(ctx, target.RepoID, target.CategoryID, title, body)
	if err != nil {
		return "", fmt.Errorf("publish discussion for phase %s: create discussion %q: %w", phaseName, title, err)
	}
	return created.URL, nil
}

// splitRepoSlug delegates to discussion.SplitRepoSlug. Kept as a package-
// level function so that property tests in this package continue to compile.
func splitRepoSlug(repoSlug string) (string, string, error) {
	return discussion.SplitRepoSlug(repoSlug)
}

func renderDiscussionTemplate(phaseName, field, tmpl string, td phase.TemplateData) (string, error) {
	rendered, err := phase.RenderPrompt(tmpl, td)
	if err != nil {
		return "", fmt.Errorf("render %s for phase %s: %w", field, phaseName, err)
	}
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return "", fmt.Errorf("phase %s: %s rendered empty", phaseName, field)
	}
	if err := validateCommandRender(fmt.Sprintf("%s for phase %s", field, phaseName), rendered); err != nil {
		return "", err
	}
	return rendered, nil
}
