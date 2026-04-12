package discussion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	resolveQuery = `query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
    id
    discussionCategories(first: 25) { nodes { id, name } }
  }
}`
	searchQuery = `query($repoId: ID!, $catId: ID!) {
  node(id: $repoId) {
    ... on Repository {
      discussions(first: 20, categoryId: $catId, orderBy: {field: CREATED_AT, direction: DESC}) {
        nodes { id, title, url }
      }
    }
  }
}`
	createMutation = `mutation($repoId: ID!, $catId: ID!, $title: String!, $body: String!) {
  createDiscussion(input: {repositoryId: $repoId, title: $title, body: $body, categoryId: $catId}) {
    discussion { id, title, url }
  }
}`
	commentMutation = `mutation($discussionId: ID!, $body: String!) {
  addDiscussionComment(input: {discussionId: $discussionId, body: $body}) {
    comment { url }
  }
}`
)

// CommandRunner abstracts subprocess execution for the discussion publisher.
type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Ref identifies an existing GitHub discussion.
type Ref struct {
	ID    string
	Title string
	URL   string
}

// Target holds the resolved repository and category IDs needed for
// discussion operations.
type Target struct {
	RepoID     string
	CategoryID string
}

// Publisher manages GitHub discussion creation and commenting via the
// gh CLI's GraphQL endpoint.
type Publisher struct {
	Runner CommandRunner
}

// ResolveTarget resolves a repository owner/name and category name into
// their GraphQL node IDs.
func (p *Publisher) ResolveTarget(ctx context.Context, owner, repo, category string) (Target, error) {
	out, err := p.Runner.RunOutput(ctx, "gh", "api", "graphql",
		"-f", "query="+resolveQuery,
		"-f", "owner="+owner,
		"-f", "repo="+repo)
	if err != nil {
		return Target{}, fmt.Errorf("resolve repo %s/%s: %w", owner, repo, err)
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
		return Target{}, fmt.Errorf("parse discussion target response: %w", err)
	}
	if resp.Data.Repository.ID == "" {
		return Target{}, fmt.Errorf("could not resolve repository %s/%s", owner, repo)
	}

	for _, node := range resp.Data.Repository.DiscussionCategories.Nodes {
		if node.Name == category {
			return Target{RepoID: resp.Data.Repository.ID, CategoryID: node.ID}, nil
		}
	}
	return Target{}, fmt.Errorf("discussion category %q not found in %s/%s", category, owner, repo)
}

// FindExisting searches recent discussions in the given repository and
// category for one whose title starts with titlePrefix.
func (p *Publisher) FindExisting(ctx context.Context, repoID, categoryID, titlePrefix string) (Ref, error) {
	out, err := p.Runner.RunOutput(ctx, "gh", "api", "graphql",
		"-f", "query="+searchQuery,
		"-f", "repoId="+repoID,
		"-f", "catId="+categoryID)
	if err != nil {
		return Ref{}, fmt.Errorf("search discussions: %w", err)
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
		return Ref{}, fmt.Errorf("parse discussion search response: %w", err)
	}

	for _, node := range resp.Data.Node.Discussions.Nodes {
		if strings.HasPrefix(node.Title, titlePrefix) {
			return Ref{ID: node.ID, Title: node.Title, URL: node.URL}, nil
		}
	}
	return Ref{}, nil
}

// Comment adds a comment to an existing discussion.
func (p *Publisher) Comment(ctx context.Context, discussionID, body string) error {
	if _, err := p.Runner.RunOutput(ctx, "gh", "api", "graphql",
		"-f", "query="+commentMutation,
		"-f", "discussionId="+discussionID,
		"-f", "body="+body); err != nil {
		return fmt.Errorf("add discussion comment: %w", err)
	}
	return nil
}

// Create creates a new discussion and returns a reference to it.
func (p *Publisher) Create(ctx context.Context, repoID, categoryID, title, body string) (Ref, error) {
	out, err := p.Runner.RunOutput(ctx, "gh", "api", "graphql",
		"-f", "query="+createMutation,
		"-f", "repoId="+repoID,
		"-f", "catId="+categoryID,
		"-f", "title="+title,
		"-f", "body="+body)
	if err != nil {
		return Ref{}, err
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
		return Ref{}, fmt.Errorf("parse create discussion response: %w", err)
	}
	if resp.Data.CreateDiscussion.Discussion.ID == "" {
		return Ref{}, fmt.Errorf("create discussion response missing discussion id")
	}
	return Ref{
		ID:    resp.Data.CreateDiscussion.Discussion.ID,
		Title: resp.Data.CreateDiscussion.Discussion.Title,
		URL:   resp.Data.CreateDiscussion.Discussion.URL,
	}, nil
}

// FindOrComment is a high-level flow that resolves a discussion target,
// searches for an existing discussion by title prefix, and either comments
// on it or creates a new one. Returns the discussion URL.
func (p *Publisher) FindOrComment(ctx context.Context, owner, repo, category, titlePrefix, title, body string) (string, error) {
	target, err := p.ResolveTarget(ctx, owner, repo, category)
	if err != nil {
		return "", err
	}

	existing, err := p.FindExisting(ctx, target.RepoID, target.CategoryID, titlePrefix)
	if err != nil {
		return "", err
	}
	if existing.ID != "" {
		if err := p.Comment(ctx, existing.ID, body); err != nil {
			return "", fmt.Errorf("comment existing discussion %q: %w", existing.Title, err)
		}
		return existing.URL, nil
	}

	created, err := p.Create(ctx, target.RepoID, target.CategoryID, title, body)
	if err != nil {
		return "", fmt.Errorf("create discussion %q: %w", title, err)
	}
	return created.URL, nil
}

// SplitRepoSlug parses an "owner/repo" string into its components.
func SplitRepoSlug(slug string) (owner, repo string, err error) {
	slug = strings.TrimSpace(slug)
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("repo slug %q must be in owner/repo form", slug)
	}
	return parts[0], parts[1], nil
}
