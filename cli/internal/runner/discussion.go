package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

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
	runner CommandRunner
}

type discussionRef struct {
	ID    string
	Title string
	URL   string
}

func (r *Runner) publishPhaseOutput(ctx context.Context, vessel queue.Vessel, p workflow.Phase, td phase.TemplateData, body string) error {
	if p.Output == "" || phaseMatchedNoOp(&p, body) {
		return nil
	}

	switch p.Output {
	case "discussion":
		repoSlug := r.resolveRepo(vessel)
		if _, err := (discussionPublisher{runner: r.Runner}).Publish(ctx, repoSlug, p.Name, p.Discussion, td, body); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("phase %s: unsupported output target %q", p.Name, p.Output)
	}
}

func (p discussionPublisher) Publish(ctx context.Context, repoSlug string, phaseName string, cfg *workflow.DiscussionOutput, td phase.TemplateData, body string) (string, error) {
	if p.runner == nil {
		return "", fmt.Errorf("publish discussion for phase %s: runner is required", phaseName)
	}
	if cfg == nil {
		return "", fmt.Errorf("publish discussion for phase %s: discussion config is required", phaseName)
	}

	owner, repo, err := splitRepoSlug(repoSlug)
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

	target, err := p.resolveTarget(ctx, owner, repo, cfg.Category)
	if err != nil {
		return "", fmt.Errorf("publish discussion for phase %s: %w", phaseName, err)
	}

	existing, err := p.findExisting(ctx, target.repoID, target.categoryID, titleSearch)
	if err != nil {
		return "", fmt.Errorf("publish discussion for phase %s: %w", phaseName, err)
	}
	if existing.ID != "" {
		if err := p.comment(ctx, existing.ID, body); err != nil {
			return "", fmt.Errorf("publish discussion for phase %s: comment existing discussion %q: %w", phaseName, existing.Title, err)
		}
		return existing.URL, nil
	}

	created, err := p.create(ctx, target.repoID, target.categoryID, title, body)
	if err != nil {
		return "", fmt.Errorf("publish discussion for phase %s: create discussion %q: %w", phaseName, title, err)
	}
	return created.URL, nil
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

func splitRepoSlug(repoSlug string) (string, string, error) {
	repoSlug = strings.TrimSpace(repoSlug)
	parts := strings.Split(repoSlug, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("repo slug %q must be in owner/repo form", repoSlug)
	}
	return parts[0], parts[1], nil
}

type discussionTarget struct {
	repoID     string
	categoryID string
}

func (p discussionPublisher) resolveTarget(ctx context.Context, owner, repo, category string) (discussionTarget, error) {
	out, err := p.runner.RunOutput(ctx, "gh", "api", "graphql",
		"-f", "query="+discussionResolveQuery,
		"-f", "owner="+owner,
		"-f", "repo="+repo)
	if err != nil {
		return discussionTarget{}, fmt.Errorf("resolve repo %s/%s: %w", owner, repo, err)
	}

	var resp struct {
		Data struct {
			Repository struct {
				ID                   string `json:"id"`
				DiscussionCategories struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"discussionCategories"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return discussionTarget{}, fmt.Errorf("parse discussion target response: %w", err)
	}
	if resp.Data.Repository.ID == "" {
		return discussionTarget{}, fmt.Errorf("could not resolve repository %s/%s", owner, repo)
	}

	for _, node := range resp.Data.Repository.DiscussionCategories.Nodes {
		if node.Name == category {
			return discussionTarget{repoID: resp.Data.Repository.ID, categoryID: node.ID}, nil
		}
	}
	return discussionTarget{}, fmt.Errorf("discussion category %q not found in %s/%s", category, owner, repo)
}

func (p discussionPublisher) findExisting(ctx context.Context, repoID, categoryID, titlePrefix string) (discussionRef, error) {
	out, err := p.runner.RunOutput(ctx, "gh", "api", "graphql",
		"-f", "query="+discussionSearchQuery,
		"-f", "repoId="+repoID,
		"-f", "catId="+categoryID)
	if err != nil {
		return discussionRef{}, fmt.Errorf("search discussions: %w", err)
	}

	var resp struct {
		Data struct {
			Node struct {
				Discussions struct {
					Nodes []struct {
						ID    string `json:"id"`
						Title string `json:"title"`
						URL   string `json:"url"`
					} `json:"nodes"`
				} `json:"discussions"`
			} `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return discussionRef{}, fmt.Errorf("parse discussion search response: %w", err)
	}

	for _, node := range resp.Data.Node.Discussions.Nodes {
		if strings.HasPrefix(node.Title, titlePrefix) {
			return discussionRef{ID: node.ID, Title: node.Title, URL: node.URL}, nil
		}
	}
	return discussionRef{}, nil
}

func (p discussionPublisher) comment(ctx context.Context, discussionID, body string) error {
	if _, err := p.runner.RunOutput(ctx, "gh", "api", "graphql",
		"-f", "query="+discussionCommentMutation,
		"-f", "discussionId="+discussionID,
		"-f", "body="+body); err != nil {
		return fmt.Errorf("add discussion comment: %w", err)
	}
	return nil
}

func (p discussionPublisher) create(ctx context.Context, repoID, categoryID, title, body string) (discussionRef, error) {
	out, err := p.runner.RunOutput(ctx, "gh", "api", "graphql",
		"-f", "query="+discussionCreateMutation,
		"-f", "repoId="+repoID,
		"-f", "catId="+categoryID,
		"-f", "title="+title,
		"-f", "body="+body)
	if err != nil {
		return discussionRef{}, err
	}

	var resp struct {
		Data struct {
			CreateDiscussion struct {
				Discussion struct {
					ID    string `json:"id"`
					Title string `json:"title"`
					URL   string `json:"url"`
				} `json:"discussion"`
			} `json:"createDiscussion"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return discussionRef{}, fmt.Errorf("parse create discussion response: %w", err)
	}
	if resp.Data.CreateDiscussion.Discussion.ID == "" {
		return discussionRef{}, fmt.Errorf("create discussion response missing discussion id")
	}
	return discussionRef{
		ID:    resp.Data.CreateDiscussion.Discussion.ID,
		Title: resp.Data.CreateDiscussion.Discussion.Title,
		URL:   resp.Data.CreateDiscussion.Discussion.URL,
	}, nil
}
