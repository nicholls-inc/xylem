package notify

import (
	"context"
	"errors"
	"testing"
)

// mockDiscussionPoster implements DiscussionPoster for testing.
type mockDiscussionPoster struct {
	findOrCommentCalls []findOrCommentCall
	findOrCommentURL   string
	findOrCommentErr   error
}

type findOrCommentCall struct {
	owner, repo, category, titlePrefix, title, body string
}

func (m *mockDiscussionPoster) FindOrComment(_ context.Context, owner, repo, category, titlePrefix, title, body string) (string, error) {
	m.findOrCommentCalls = append(m.findOrCommentCalls, findOrCommentCall{
		owner: owner, repo: repo, category: category,
		titlePrefix: titlePrefix, title: title, body: body,
	})
	return m.findOrCommentURL, m.findOrCommentErr
}

func TestDiscussion_PostStatus_CallsFindOrComment(t *testing.T) {
	mock := &mockDiscussionPoster{
		findOrCommentURL: "https://github.com/owner/repo/discussions/42",
	}
	d, err := NewDiscussion(mock, "owner/repo", "General", "Status")
	if err != nil {
		t.Fatalf("NewDiscussion: %v", err)
	}

	report := StatusReport{Markdown: "report body"}
	if err := d.PostStatus(context.Background(), report); err != nil {
		t.Fatalf("PostStatus: %v", err)
	}

	if len(mock.findOrCommentCalls) != 1 {
		t.Fatalf("expected 1 FindOrComment call, got %d", len(mock.findOrCommentCalls))
	}
	call := mock.findOrCommentCalls[0]
	if call.owner != "owner" || call.repo != "repo" {
		t.Errorf("expected owner/repo, got %s/%s", call.owner, call.repo)
	}
	if call.category != "General" {
		t.Errorf("expected category General, got %q", call.category)
	}
	if call.title != "Status" {
		t.Errorf("expected title Status, got %q", call.title)
	}
	if call.body != "report body" {
		t.Errorf("expected body 'report body', got %q", call.body)
	}
}

func TestDiscussion_PostStatus_PropagatesError(t *testing.T) {
	mock := &mockDiscussionPoster{
		findOrCommentErr: errors.New("graphql: forbidden"),
	}
	d, err := NewDiscussion(mock, "owner/repo", "General", "Status")
	if err != nil {
		t.Fatalf("NewDiscussion: %v", err)
	}

	err = d.PostStatus(context.Background(), StatusReport{Markdown: "body"})
	if err == nil {
		t.Fatal("expected error from PostStatus, got nil")
	}
	if !errors.Is(err, mock.findOrCommentErr) {
		// The error is wrapped, so check the message contains the original.
		if got := err.Error(); got == "" {
			t.Errorf("expected wrapped error, got empty")
		}
	}
}

func TestDiscussion_SendAlert_IsNoop(t *testing.T) {
	mock := &mockDiscussionPoster{}
	d, err := NewDiscussion(mock, "owner/repo", "General", "Status")
	if err != nil {
		t.Fatalf("NewDiscussion: %v", err)
	}

	err = d.SendAlert(context.Background(), Alert{Code: "test"})
	if err != nil {
		t.Fatalf("SendAlert should return nil, got: %v", err)
	}
	if len(mock.findOrCommentCalls) != 0 {
		t.Error("SendAlert should not call any poster methods")
	}
}

func TestNewDiscussion_InvalidRepoSlug(t *testing.T) {
	tests := []struct {
		name string
		slug string
	}{
		{"empty", ""},
		{"no slash", "ownerrepo"},
		{"empty owner", "/repo"},
		{"empty repo", "owner/"},
	}

	mock := &mockDiscussionPoster{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDiscussion(mock, tt.slug, "General", "Status")
			if err == nil {
				t.Errorf("expected error for slug %q, got nil", tt.slug)
			}
		})
	}
}

func TestDiscussion_Name(t *testing.T) {
	mock := &mockDiscussionPoster{}
	d, err := NewDiscussion(mock, "owner/repo", "General", "Status")
	if err != nil {
		t.Fatalf("NewDiscussion: %v", err)
	}
	if d.Name() != "github_discussion" {
		t.Errorf("expected Name()=github_discussion, got %q", d.Name())
	}
}
