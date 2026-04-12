package discussion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// mockResponse pairs output bytes with an optional error for a single call.
type mockResponse struct {
	output []byte
	err    error
}

// mockRunner records every RunOutput invocation and replays canned responses
// keyed by the joined (name + args) string.
type mockRunner struct {
	calls     [][]string
	responses map[string]mockResponse
}

func (m *mockRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	m.calls = append(m.calls, call)

	key := strings.Join(call, "\x00")
	if resp, ok := m.responses[key]; ok {
		return resp.output, resp.err
	}
	return nil, errors.New("unexpected call: " + strings.Join(call, " "))
}

// responseKey builds the lookup key that mockRunner uses.
func responseKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

// --- helpers to build realistic GraphQL JSON responses ---

func resolveJSON(repoID string, categories map[string]string) []byte {
	type catNode struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	nodes := make([]catNode, 0, len(categories))
	for name, id := range categories {
		nodes = append(nodes, catNode{ID: id, Name: name})
	}
	resp := struct {
		Data struct {
			Repository struct {
				ID                   string `json:"id"`
				DiscussionCategories struct {
					Nodes []catNode `json:"nodes"`
				} `json:"discussionCategories"`
			} `json:"repository"`
		} `json:"data"`
	}{}
	resp.Data.Repository.ID = repoID
	resp.Data.Repository.DiscussionCategories.Nodes = nodes
	b, _ := json.Marshal(resp)
	return b
}

func searchJSON(discussions []Ref) []byte {
	type disc struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	nodes := make([]disc, 0, len(discussions))
	for _, d := range discussions {
		nodes = append(nodes, disc(d))
	}
	resp := struct {
		Data struct {
			Node struct {
				Discussions struct {
					Nodes []disc `json:"nodes"`
				} `json:"discussions"`
			} `json:"node"`
		} `json:"data"`
	}{}
	resp.Data.Node.Discussions.Nodes = nodes
	b, _ := json.Marshal(resp)
	return b
}

func createJSON(id, title, url string) []byte {
	resp := struct {
		Data struct {
			CreateDiscussion struct {
				Discussion struct {
					ID    string `json:"id"`
					Title string `json:"title"`
					URL   string `json:"url"`
				} `json:"discussion"`
			} `json:"createDiscussion"`
		} `json:"data"`
	}{}
	resp.Data.CreateDiscussion.Discussion.ID = id
	resp.Data.CreateDiscussion.Discussion.Title = title
	resp.Data.CreateDiscussion.Discussion.URL = url
	b, _ := json.Marshal(resp)
	return b
}

func commentJSON() []byte {
	resp := struct {
		Data struct {
			AddDiscussionComment struct {
				Comment struct {
					URL string `json:"url"`
				} `json:"comment"`
			} `json:"addDiscussionComment"`
		} `json:"data"`
	}{}
	resp.Data.AddDiscussionComment.Comment.URL = "https://github.com/owner/repo/discussions/1#comment-42"
	b, _ := json.Marshal(resp)
	return b
}

// --- Tests ---

func TestResolveTarget(t *testing.T) {
	m := &mockRunner{responses: map[string]mockResponse{
		responseKey("gh", "api", "graphql",
			"-f", "query="+resolveQuery,
			"-f", "owner=acme",
			"-f", "repo=widgets"): {
			output: resolveJSON("R_abc123", map[string]string{
				"General":       "DC_general",
				"Announcements": "DC_announce",
			}),
		},
	}}

	p := Publisher{Runner: m}
	target, err := p.ResolveTarget(context.Background(), "acme", "widgets", "General")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.RepoID != "R_abc123" {
		t.Errorf("RepoID = %q, want %q", target.RepoID, "R_abc123")
	}
	if target.CategoryID != "DC_general" {
		t.Errorf("CategoryID = %q, want %q", target.CategoryID, "DC_general")
	}
}

func TestResolveTarget_CategoryNotFound(t *testing.T) {
	m := &mockRunner{responses: map[string]mockResponse{
		responseKey("gh", "api", "graphql",
			"-f", "query="+resolveQuery,
			"-f", "owner=acme",
			"-f", "repo=widgets"): {
			output: resolveJSON("R_abc123", map[string]string{
				"General": "DC_general",
			}),
		},
	}}

	p := Publisher{Runner: m}
	_, err := p.ResolveTarget(context.Background(), "acme", "widgets", "Missing")
	if err == nil {
		t.Fatal("expected error for missing category")
	}
	if !strings.Contains(err.Error(), `"Missing"`) {
		t.Errorf("error = %q, want mention of category name", err.Error())
	}
}

func TestFindExisting(t *testing.T) {
	refs := []Ref{
		{ID: "D_99", Title: "Weekly Report 2026-04-12", URL: "https://github.com/acme/widgets/discussions/99"},
		{ID: "D_98", Title: "Weekly Report 2026-04-05", URL: "https://github.com/acme/widgets/discussions/98"},
	}
	m := &mockRunner{responses: map[string]mockResponse{
		responseKey("gh", "api", "graphql",
			"-f", "query="+searchQuery,
			"-f", "repoId=R_abc123",
			"-f", "catId=DC_general"): {
			output: searchJSON(refs),
		},
	}}

	p := Publisher{Runner: m}
	got, err := p.FindExisting(context.Background(), "R_abc123", "DC_general", "Weekly Report")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "D_99" {
		t.Errorf("ID = %q, want %q", got.ID, "D_99")
	}
	if got.Title != "Weekly Report 2026-04-12" {
		t.Errorf("Title = %q, want %q", got.Title, "Weekly Report 2026-04-12")
	}
	if got.URL != "https://github.com/acme/widgets/discussions/99" {
		t.Errorf("URL = %q, want full discussion URL", got.URL)
	}
}

func TestFindExisting_NoMatch(t *testing.T) {
	refs := []Ref{
		{ID: "D_50", Title: "Something else entirely", URL: "https://github.com/acme/widgets/discussions/50"},
	}
	m := &mockRunner{responses: map[string]mockResponse{
		responseKey("gh", "api", "graphql",
			"-f", "query="+searchQuery,
			"-f", "repoId=R_abc123",
			"-f", "catId=DC_general"): {
			output: searchJSON(refs),
		},
	}}

	p := Publisher{Runner: m}
	got, err := p.FindExisting(context.Background(), "R_abc123", "DC_general", "Weekly Report")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "" {
		t.Errorf("expected empty Ref, got ID=%q", got.ID)
	}
}

func TestComment(t *testing.T) {
	key := responseKey("gh", "api", "graphql",
		"-f", "query="+commentMutation,
		"-f", "discussionId=D_99",
		"-f", "body=Hello world")

	m := &mockRunner{responses: map[string]mockResponse{
		key: {output: commentJSON()},
	}}

	p := Publisher{Runner: m}
	err := p.Comment(context.Background(), "D_99", "Hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(m.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(m.calls))
	}
	call := m.calls[0]
	// Verify key args passed to gh.
	if call[0] != "gh" {
		t.Errorf("binary = %q, want gh", call[0])
	}
	// The call should include the discussion ID and body verbatim.
	joined := strings.Join(call, " ")
	if !strings.Contains(joined, "discussionId=D_99") {
		t.Error("call missing discussionId field")
	}
	if !strings.Contains(joined, "body=Hello world") {
		t.Error("call missing body field")
	}
}

func TestCreate(t *testing.T) {
	key := responseKey("gh", "api", "graphql",
		"-f", "query="+createMutation,
		"-f", "repoId=R_abc123",
		"-f", "catId=DC_general",
		"-f", "title=New Discussion",
		"-f", "body=Some content")

	m := &mockRunner{responses: map[string]mockResponse{
		key: {output: createJSON("D_100", "New Discussion", "https://github.com/acme/widgets/discussions/100")},
	}}

	p := Publisher{Runner: m}
	ref, err := p.Create(context.Background(), "R_abc123", "DC_general", "New Discussion", "Some content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.ID != "D_100" {
		t.Errorf("ID = %q, want %q", ref.ID, "D_100")
	}
	if ref.Title != "New Discussion" {
		t.Errorf("Title = %q, want %q", ref.Title, "New Discussion")
	}
	if ref.URL != "https://github.com/acme/widgets/discussions/100" {
		t.Errorf("URL = %q, want full discussion URL", ref.URL)
	}
}

func TestFindOrComment_ExistingDiscussion(t *testing.T) {
	resolveKey := responseKey("gh", "api", "graphql",
		"-f", "query="+resolveQuery,
		"-f", "owner=acme",
		"-f", "repo=widgets")
	searchKey := responseKey("gh", "api", "graphql",
		"-f", "query="+searchQuery,
		"-f", "repoId=R_abc123",
		"-f", "catId=DC_general")
	commentKey := responseKey("gh", "api", "graphql",
		"-f", "query="+commentMutation,
		"-f", "discussionId=D_99",
		"-f", "body=update body")

	m := &mockRunner{responses: map[string]mockResponse{
		resolveKey: {output: resolveJSON("R_abc123", map[string]string{"General": "DC_general"})},
		searchKey: {output: searchJSON([]Ref{
			{ID: "D_99", Title: "Weekly Report 2026-04-12", URL: "https://github.com/acme/widgets/discussions/99"},
		})},
		commentKey: {output: commentJSON()},
	}}

	p := Publisher{Runner: m}
	url, err := p.FindOrComment(context.Background(), "acme", "widgets", "General", "Weekly Report", "Weekly Report 2026-04-12", "update body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://github.com/acme/widgets/discussions/99" {
		t.Errorf("URL = %q, want existing discussion URL", url)
	}

	// Verify Comment was called, not Create.
	for _, call := range m.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, createMutation) {
			t.Error("Create mutation was called; expected only Comment")
		}
	}
	// Verify Comment WAS called.
	found := false
	for _, call := range m.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, commentMutation) {
			found = true
			break
		}
	}
	if !found {
		t.Error("Comment mutation was not called")
	}
}

func TestFindOrComment_NewDiscussion(t *testing.T) {
	resolveKey := responseKey("gh", "api", "graphql",
		"-f", "query="+resolveQuery,
		"-f", "owner=acme",
		"-f", "repo=widgets")
	searchKey := responseKey("gh", "api", "graphql",
		"-f", "query="+searchQuery,
		"-f", "repoId=R_abc123",
		"-f", "catId=DC_general")
	createKey := responseKey("gh", "api", "graphql",
		"-f", "query="+createMutation,
		"-f", "repoId=R_abc123",
		"-f", "catId=DC_general",
		"-f", "title=Weekly Report 2026-04-12",
		"-f", "body=fresh body")

	m := &mockRunner{responses: map[string]mockResponse{
		resolveKey: {output: resolveJSON("R_abc123", map[string]string{"General": "DC_general"})},
		searchKey:  {output: searchJSON(nil)}, // no existing discussions
		createKey:  {output: createJSON("D_200", "Weekly Report 2026-04-12", "https://github.com/acme/widgets/discussions/200")},
	}}

	p := Publisher{Runner: m}
	url, err := p.FindOrComment(context.Background(), "acme", "widgets", "General", "Weekly Report", "Weekly Report 2026-04-12", "fresh body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://github.com/acme/widgets/discussions/200" {
		t.Errorf("URL = %q, want newly created discussion URL", url)
	}

	// Verify Create was called, not Comment.
	for _, call := range m.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, commentMutation) {
			t.Error("Comment mutation was called; expected only Create")
		}
	}
	found := false
	for _, call := range m.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, createMutation) {
			found = true
			break
		}
	}
	if !found {
		t.Error("Create mutation was not called")
	}
}

func TestSplitRepoSlug(t *testing.T) {
	tests := []struct {
		slug      string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{slug: "owner/repo", wantOwner: "owner", wantRepo: "repo"},
		{slug: "bad", wantErr: true},
		{slug: "", wantErr: true},
		{slug: "a/", wantErr: true},
		{slug: "/b", wantErr: true},
		{slug: "a/b/c", wantErr: true},
		{slug: "  owner/repo  ", wantOwner: "owner", wantRepo: "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			owner, repo, err := SplitRepoSlug(tt.slug)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SplitRepoSlug(%q) expected error, got owner=%q repo=%q", tt.slug, owner, repo)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitRepoSlug(%q) unexpected error: %v", tt.slug, err)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}
