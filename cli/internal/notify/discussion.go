package notify

import (
	"context"
	"fmt"
	"strings"
)

// DiscussionPoster abstracts the find-or-create + comment flow for GitHub
// Discussions. Satisfied by *discussion.Publisher.
type DiscussionPoster interface {
	FindOrComment(ctx context.Context, owner, repo, category, titlePrefix, title, body string) (string, error)
}

// Discussion posts status reports as comments on a GitHub Discussion.
type Discussion struct {
	poster   DiscussionPoster
	owner    string
	repo     string
	category string
	title    string
}

// NewDiscussion creates a Discussion notifier. repoSlug must be "owner/repo".
func NewDiscussion(poster DiscussionPoster, repoSlug, category, title string) (*Discussion, error) {
	parts := strings.SplitN(repoSlug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid repo slug %q: expected owner/repo", repoSlug)
	}
	return &Discussion{
		poster:   poster,
		owner:    parts[0],
		repo:     parts[1],
		category: category,
		title:    title,
	}, nil
}

func (d *Discussion) Name() string { return "github_discussion" }

// PostStatus posts the report as a comment on the status Discussion, creating
// the Discussion if it does not yet exist. Each call resolves the discussion
// target and searches for an existing discussion by title — two GraphQL calls
// per invocation. At hourly frequency this is well within GitHub's rate limits.
func (d *Discussion) PostStatus(ctx context.Context, report StatusReport) error {
	_, err := d.poster.FindOrComment(ctx, d.owner, d.repo, d.category, d.title, d.title, report.Markdown)
	if err != nil {
		return fmt.Errorf("post status to discussion: %w", err)
	}
	return nil
}

// SendAlert is a no-op -- Discussion is for status only.
func (d *Discussion) SendAlert(_ context.Context, _ Alert) error {
	return nil
}
