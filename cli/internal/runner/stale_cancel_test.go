package runner

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
)

func TestCancelStalePRVessels_CancelsMerged(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	v := queue.Vessel{
		ID:        "merge-pr-42",
		Source:    "github-pr",
		Ref:       "https://github.com/owner/repo/pull/42",
		Workflow:  "merge-pr",
		State:     queue.StatePending,
		CreatedAt: time.Now(),
		Meta:      map[string]string{"pr_num": "42", "config_source": "prs"},
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}

	resp, _ := json.Marshal(map[string]string{"state": "MERGED"})
	mock := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return resp, nil, true
			}
			return nil, nil, false
		},
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"prs": {Type: "github-pr", Repo: "owner/repo"},
		},
	}

	r := &Runner{
		Config: cfg,
		Queue:  q,
		Runner: mock,
		Sources: map[string]source.Source{
			"github-pr": &source.GitHubPR{Repo: "owner/repo"},
		},
	}

	cancelled := r.CancelStalePRVessels(context.Background())
	if cancelled != 1 {
		t.Errorf("expected 1 cancelled, got %d", cancelled)
	}

	vessel, err := q.FindByID("merge-pr-42")
	if err != nil {
		t.Fatal(err)
	}
	if vessel.State != queue.StateCancelled {
		t.Errorf("expected cancelled, got %s", vessel.State)
	}
}

func TestCancelStalePRVessels_KeepsOpen(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	v := queue.Vessel{
		ID:        "merge-pr-43",
		Source:    "github-pr",
		Ref:       "https://github.com/owner/repo/pull/43",
		Workflow:  "merge-pr",
		State:     queue.StatePending,
		CreatedAt: time.Now(),
		Meta:      map[string]string{"pr_num": "43", "config_source": "prs"},
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}

	resp, _ := json.Marshal(map[string]string{"state": "OPEN"})
	mock := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "gh" {
				return resp, nil, true
			}
			return nil, nil, false
		},
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"prs": {Type: "github-pr", Repo: "owner/repo"},
		},
	}

	r := &Runner{
		Config: cfg,
		Queue:  q,
		Runner: mock,
		Sources: map[string]source.Source{
			"github-pr": &source.GitHubPR{Repo: "owner/repo"},
		},
	}

	cancelled := r.CancelStalePRVessels(context.Background())
	if cancelled != 0 {
		t.Errorf("expected 0 cancelled, got %d", cancelled)
	}

	vessel, err := q.FindByID("merge-pr-43")
	if err != nil {
		t.Fatal(err)
	}
	if vessel.State != queue.StatePending {
		t.Errorf("expected pending, got %s", vessel.State)
	}
}

func TestCancelStalePRVessels_SkipsNonPRSources(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	v := queue.Vessel{
		ID:        "issue-99",
		Source:    "github-issue",
		Ref:       "https://github.com/owner/repo/issues/99",
		Workflow:  "implement-feature",
		State:     queue.StatePending,
		CreatedAt: time.Now(),
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}

	ghCalled := false
	mock := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "gh" {
				ghCalled = true
			}
			return nil, nil, false
		},
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
	}

	r := &Runner{
		Config: cfg,
		Queue:  q,
		Runner: mock,
	}

	cancelled := r.CancelStalePRVessels(context.Background())
	if cancelled != 0 {
		t.Errorf("expected 0 cancelled, got %d", cancelled)
	}
	if ghCalled {
		t.Error("gh should not be called for non-PR sources")
	}
}

func TestCancelStalePRVessels_CancelsClosed(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	v := queue.Vessel{
		ID:        "resolve-pr-50",
		Source:    "github-pr-events",
		Ref:       "https://github.com/owner/repo/pull/50#checks-failed-abc",
		Workflow:  "fix-pr-checks",
		State:     queue.StatePending,
		CreatedAt: time.Now(),
		Meta:      map[string]string{"pr_num": "50", "config_source": "pr-events"},
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}

	resp, _ := json.Marshal(map[string]string{"state": "CLOSED"})
	mock := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "gh" {
				return resp, nil, true
			}
			return nil, nil, false
		},
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"pr-events": {Type: "github-pr-events", Repo: "owner/repo"},
		},
	}

	r := &Runner{
		Config: cfg,
		Queue:  q,
		Runner: mock,
		Sources: map[string]source.Source{
			"github-pr-events": &source.GitHubPREvents{Repo: "owner/repo"},
		},
	}

	cancelled := r.CancelStalePRVessels(context.Background())
	if cancelled != 1 {
		t.Errorf("expected 1 cancelled, got %d", cancelled)
	}
}

func TestCancelStalePRVessels_SkipsGithubMergeSource(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	v := queue.Vessel{
		ID:        "unblock-wave-pr-77",
		Source:    "github-merge",
		Ref:       "https://github.com/owner/repo/pull/77",
		Workflow:  "unblock-wave",
		State:     queue.StatePending,
		CreatedAt: time.Now(),
		Meta:      map[string]string{"pr_num": "77", "config_source": "merges"},
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}

	// PR is MERGED — this is the vessel's trigger, not a stale signal.
	resp, _ := json.Marshal(map[string]string{"state": "MERGED"})
	ghCalled := false
	mock := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "gh" {
				ghCalled = true
				return resp, nil, true
			}
			return nil, nil, false
		},
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
	}

	r := &Runner{
		Config: cfg,
		Queue:  q,
		Runner: mock,
	}

	cancelled := r.CancelStalePRVessels(context.Background())
	if cancelled != 0 {
		t.Errorf("expected 0 cancelled, got %d", cancelled)
	}
	if ghCalled {
		t.Error("gh should not be called for github-merge source vessels")
	}

	vessel, err := q.FindByID("unblock-wave-pr-77")
	if err != nil {
		t.Fatal(err)
	}
	if vessel.State != queue.StatePending {
		t.Errorf("expected pending, got %s", vessel.State)
	}
}

func TestCheckPRState_RespectsTimeout(t *testing.T) {
	orig := ghCallTimeout
	ghCallTimeout = 5 * time.Millisecond
	t.Cleanup(func() { ghCallTimeout = orig })

	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	// The mock blocks until the context it receives is cancelled. This simulates a
	// hung gh process. If checkPRState does not apply context.WithTimeout, the
	// goroutine below will never unblock and the time.After fires.
	mock := &mockCmdRunner{
		runOutputCtxHook: func(ctx context.Context, name string, args ...string) ([]byte, error, bool) {
			if name == "gh" {
				<-ctx.Done()
				return nil, ctx.Err(), true
			}
			return nil, nil, false
		},
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"prs": {Type: "github-pr", Repo: "owner/repo"},
		},
	}

	r := &Runner{
		Config: cfg,
		Queue:  q,
		Runner: mock,
		Sources: map[string]source.Source{
			"github-pr": &source.GitHubPR{Repo: "owner/repo"},
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.checkPRState(context.Background(), "owner/repo", 1)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from timed-out gh call, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("checkPRState did not return within 2s — gh timeout not applied")
	}
}

func TestCancelStalePRVessels_CancelsWaitingMerged(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	v := queue.Vessel{
		ID:        "merge-pr-60",
		Source:    "github-pr",
		Ref:       "https://github.com/owner/repo/pull/60",
		Workflow:  "merge-pr",
		State:     queue.StatePending,
		CreatedAt: time.Now(),
		Meta:      map[string]string{"pr_num": "60", "config_source": "prs"},
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}
	if err := q.Update(v.ID, queue.StateRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := q.Update(v.ID, queue.StateWaiting, ""); err != nil {
		t.Fatal(err)
	}

	resp, _ := json.Marshal(map[string]string{"state": "MERGED"})
	mock := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return resp, nil, true
			}
			return nil, nil, false
		},
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"prs": {Type: "github-pr", Repo: "owner/repo"},
		},
	}

	r := &Runner{
		Config: cfg,
		Queue:  q,
		Runner: mock,
		Sources: map[string]source.Source{
			"github-pr": &source.GitHubPR{Repo: "owner/repo"},
		},
	}

	cancelled := r.CancelStalePRVessels(context.Background())
	if cancelled != 1 {
		t.Errorf("expected 1 cancelled, got %d", cancelled)
	}

	vessel, err := q.FindByID("merge-pr-60")
	if err != nil {
		t.Fatal(err)
	}
	if vessel.State != queue.StateCancelled {
		t.Errorf("expected cancelled, got %s", vessel.State)
	}
}

func TestCancelStalePRVessels_KeepsWaitingOpen(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	v := queue.Vessel{
		ID:        "merge-pr-61",
		Source:    "github-pr",
		Ref:       "https://github.com/owner/repo/pull/61",
		Workflow:  "merge-pr",
		State:     queue.StatePending,
		CreatedAt: time.Now(),
		Meta:      map[string]string{"pr_num": "61", "config_source": "prs"},
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}
	if err := q.Update(v.ID, queue.StateRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := q.Update(v.ID, queue.StateWaiting, ""); err != nil {
		t.Fatal(err)
	}

	resp, _ := json.Marshal(map[string]string{"state": "OPEN"})
	mock := &mockCmdRunner{
		runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
			if name == "gh" {
				return resp, nil, true
			}
			return nil, nil, false
		},
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
		Sources: map[string]config.SourceConfig{
			"prs": {Type: "github-pr", Repo: "owner/repo"},
		},
	}

	r := &Runner{
		Config: cfg,
		Queue:  q,
		Runner: mock,
		Sources: map[string]source.Source{
			"github-pr": &source.GitHubPR{Repo: "owner/repo"},
		},
	}

	cancelled := r.CancelStalePRVessels(context.Background())
	if cancelled != 0 {
		t.Errorf("expected 0 cancelled, got %d", cancelled)
	}

	vessel, err := q.FindByID("merge-pr-61")
	if err != nil {
		t.Fatal(err)
	}
	if vessel.State != queue.StateWaiting {
		t.Errorf("expected waiting, got %s", vessel.State)
	}
}

func TestExtractPRNumber(t *testing.T) {
	tests := []struct {
		name     string
		vessel   queue.Vessel
		expected int
	}{
		{
			name:     "from meta",
			vessel:   queue.Vessel{Meta: map[string]string{"pr_num": "42"}},
			expected: 42,
		},
		{
			name:     "from ref",
			vessel:   queue.Vessel{Ref: "https://github.com/owner/repo/pull/99"},
			expected: 99,
		},
		{
			name:     "from ref with fragment",
			vessel:   queue.Vessel{Ref: "https://github.com/owner/repo/pull/55#merge-abc123"},
			expected: 55,
		},
		{
			name:     "no pr number",
			vessel:   queue.Vessel{Ref: "https://github.com/owner/repo/issues/10"},
			expected: 0,
		},
		{
			name:     "meta takes precedence",
			vessel:   queue.Vessel{Meta: map[string]string{"pr_num": "7"}, Ref: "https://github.com/owner/repo/pull/99"},
			expected: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPRNumber(tt.vessel)
			if got != tt.expected {
				t.Errorf("extractPRNumber() = %d, want %d", got, tt.expected)
			}
		})
	}
}
