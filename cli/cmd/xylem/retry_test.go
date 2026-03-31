package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func newRetryTestQueue(t *testing.T) *queue.Queue {
	t.Helper()
	return queue.New(filepath.Join(t.TempDir(), "queue.jsonl"))
}

func newRetryTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{StateDir: t.TempDir()}
}

func TestRetryCreatesNewVessel(t *testing.T) {
	q := newRetryTestQueue(t)
	cfg := newRetryTestConfig(t)
	now := time.Now().UTC()
	v := queue.Vessel{
		ID: "issue-42", Source: "github-issue", Workflow: "fix-bug",
		Ref:   "https://github.com/owner/repo/issues/42",
		Meta:  map[string]string{"issue_num": "42"},
		State: queue.StatePending, CreatedAt: now,
		FailedPhase: "gate",
		GateOutput:  "exit code 1: tests failed",
	}
	q.Enqueue(v)                                          //nolint:errcheck
	q.Update("issue-42", queue.StateRunning, "")          //nolint:errcheck
	q.Update("issue-42", queue.StateFailed, "test error") //nolint:errcheck

	err := cmdRetry(q, cfg, "issue-42", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vessels, _ := q.List()
	var found bool
	for _, v := range vessels {
		if v.ID == "issue-42-retry-1" {
			found = true
			if v.State != queue.StatePending {
				t.Errorf("retry should be pending, got %s", v.State)
			}
			if v.Workflow != "fix-bug" {
				t.Errorf("retry should have same workflow, got %s", v.Workflow)
			}
			if v.Source != "github-issue" {
				t.Errorf("retry should have same source, got %s", v.Source)
			}
			if v.Ref != "https://github.com/owner/repo/issues/42" {
				t.Errorf("retry should have same ref, got %s", v.Ref)
			}
			if v.RetryOf != "issue-42" {
				t.Errorf("retry_of should be issue-42, got %s", v.RetryOf)
			}
			if v.Meta["retry_of"] != "issue-42" {
				t.Errorf("meta retry_of should be issue-42, got %s", v.Meta["retry_of"])
			}
			if v.Meta["retry_error"] != "test error" {
				t.Errorf("meta retry_error should be 'test error', got %s", v.Meta["retry_error"])
			}
			if v.Meta["issue_num"] != "42" {
				t.Errorf("meta issue_num should be preserved, got %s", v.Meta["issue_num"])
			}
			if v.Meta["failed_phase"] != "gate" {
				t.Errorf("meta failed_phase should be 'gate', got %s", v.Meta["failed_phase"])
			}
			if v.Meta["gate_output"] != "exit code 1: tests failed" {
				t.Errorf("meta gate_output should be 'exit code 1: tests failed', got %s", v.Meta["gate_output"])
			}
			if v.FailedPhase != "gate" {
				t.Errorf("FailedPhase should be 'gate', got %s", v.FailedPhase)
			}
			if v.GateOutput != "exit code 1: tests failed" {
				t.Errorf("GateOutput should be 'exit code 1: tests failed', got %s", v.GateOutput)
			}
		}
	}
	if !found {
		t.Error("retry vessel issue-42-retry-1 not found")
	}
}

func TestRetryNonFailedVessel(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, q *queue.Queue)
	}{
		{
			name: "pending",
			setup: func(t *testing.T, q *queue.Queue) {
				t.Helper()
				q.Enqueue(queue.Vessel{ID: "issue-1", Source: "manual", State: queue.StatePending, CreatedAt: time.Now().UTC()}) //nolint:errcheck
			},
		},
		{
			name: "completed",
			setup: func(t *testing.T, q *queue.Queue) {
				t.Helper()
				q.Enqueue(queue.Vessel{ID: "issue-1", Source: "manual", State: queue.StatePending, CreatedAt: time.Now().UTC()}) //nolint:errcheck
				q.Update("issue-1", queue.StateRunning, "")                                                                      //nolint:errcheck
				q.Update("issue-1", queue.StateCompleted, "")                                                                    //nolint:errcheck
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newRetryTestQueue(t)
			cfg := newRetryTestConfig(t)
			tt.setup(t, q)

			err := cmdRetry(q, cfg, "issue-1", false)
			if err == nil {
				t.Fatal("expected error retrying non-failed vessel")
			}
			if !strings.Contains(err.Error(), "not in failed state") {
				t.Errorf("expected 'not in failed state' error, got: %v", err)
			}
		})
	}
}

func TestRetryNonexistentVessel(t *testing.T) {
	q := newRetryTestQueue(t)
	cfg := newRetryTestConfig(t)

	err := cmdRetry(q, cfg, "issue-999", false)
	if err == nil {
		t.Fatal("expected error retrying non-existent vessel")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestRetryMultipleRetries(t *testing.T) {
	q := newRetryTestQueue(t)
	cfg := newRetryTestConfig(t)
	now := time.Now().UTC()

	q.Enqueue(queue.Vessel{ID: "issue-42", Source: "manual", Workflow: "fix-bug", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
	q.Update("issue-42", queue.StateRunning, "")                                                                              //nolint:errcheck
	q.Update("issue-42", queue.StateFailed, "first error")                                                                    //nolint:errcheck

	// First retry
	if err := cmdRetry(q, cfg, "issue-42", false); err != nil {
		t.Fatalf("first retry: %v", err)
	}

	// Second retry of the original
	if err := cmdRetry(q, cfg, "issue-42", false); err != nil {
		t.Fatalf("second retry: %v", err)
	}

	vessels, _ := q.List()
	retryIDs := make(map[string]bool)
	for _, v := range vessels {
		if strings.HasPrefix(v.ID, "issue-42-retry-") {
			retryIDs[v.ID] = true
		}
	}

	if !retryIDs["issue-42-retry-1"] {
		t.Error("expected issue-42-retry-1")
	}
	if !retryIDs["issue-42-retry-2"] {
		t.Error("expected issue-42-retry-2")
	}
}

func TestRetryIDGeneration(t *testing.T) {
	tests := []struct {
		name       string
		originalID string
		existing   []string
		expected   string
	}{
		{
			name:       "first retry",
			originalID: "issue-42",
			existing:   nil,
			expected:   "issue-42-retry-1",
		},
		{
			name:       "second retry",
			originalID: "issue-42",
			existing:   []string{"issue-42-retry-1"},
			expected:   "issue-42-retry-2",
		},
		{
			name:       "gap in numbering",
			originalID: "issue-42",
			existing:   []string{"issue-42-retry-1", "issue-42-retry-3"},
			expected:   "issue-42-retry-4",
		},
		{
			name:       "unrelated vessels ignored",
			originalID: "issue-42",
			existing:   []string{"issue-99-retry-1"},
			expected:   "issue-42-retry-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newRetryTestQueue(t)
			now := time.Now().UTC()
			for _, id := range tt.existing {
				q.Enqueue(queue.Vessel{ID: id, Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
			}

			got := retryID(tt.originalID, q)
			if got != tt.expected {
				t.Errorf("retryID(%q) = %q, want %q", tt.originalID, got, tt.expected)
			}
		})
	}
}

func TestRetryPreservesPrompt(t *testing.T) {
	q := newRetryTestQueue(t)
	cfg := newRetryTestConfig(t)
	now := time.Now().UTC()

	q.Enqueue(queue.Vessel{
		ID: "task-1", Source: "manual", Prompt: "fix the thing",
		State: queue.StatePending, CreatedAt: now,
	}) //nolint:errcheck
	q.Update("task-1", queue.StateRunning, "")         //nolint:errcheck
	q.Update("task-1", queue.StateFailed, "timed out") //nolint:errcheck

	if err := cmdRetry(q, cfg, "task-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vessels, _ := q.List()
	for _, v := range vessels {
		if v.ID == "task-1-retry-1" {
			if v.Prompt != "fix the thing" {
				t.Errorf("expected prompt preserved, got %q", v.Prompt)
			}
			return
		}
	}
	t.Error("retry vessel not found")
}

func TestRetryResumesFromFailedPhase(t *testing.T) {
	q := newRetryTestQueue(t)
	cfg := newRetryTestConfig(t)
	now := time.Now().UTC()

	// Create phase output directory with files for the original vessel
	srcDir := filepath.Join(cfg.StateDir, "phases", "issue-10")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "plan.output"), []byte("plan result"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "implement.output"), []byte("impl result"), 0o644); err != nil {
		t.Fatal(err)
	}

	phasePaths := map[string]string{
		"plan":      filepath.Join(srcDir, "plan.output"),
		"implement": filepath.Join(srcDir, "implement.output"),
	}

	v := queue.Vessel{
		ID:           "issue-10",
		Source:       "manual",
		Workflow:     "fix-bug",
		State:        queue.StatePending,
		CreatedAt:    now,
		CurrentPhase: 2,
		WorktreePath: "/tmp/worktrees/issue-10",
		PhaseOutputs: phasePaths,
		FailedPhase:  "test",
	}
	q.Enqueue(v)                                       //nolint:errcheck
	q.Update("issue-10", queue.StateRunning, "")       //nolint:errcheck
	q.Update("issue-10", queue.StateFailed, "phase 3") //nolint:errcheck

	err := cmdRetry(q, cfg, "issue-10", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	retry, err := q.FindByID("issue-10-retry-1")
	if err != nil {
		t.Fatalf("retry vessel not found: %v", err)
	}

	if retry.CurrentPhase != 2 {
		t.Errorf("CurrentPhase = %d, want 2", retry.CurrentPhase)
	}
	if retry.WorktreePath != "/tmp/worktrees/issue-10" {
		t.Errorf("WorktreePath = %q, want /tmp/worktrees/issue-10", retry.WorktreePath)
	}

	// PhaseOutputs should reference the new vessel ID
	dstDir := filepath.Join(cfg.StateDir, "phases", "issue-10-retry-1")
	for phase, path := range retry.PhaseOutputs {
		if strings.Contains(path, "issue-10"+string(filepath.Separator)) && !strings.Contains(path, "issue-10-retry-1") {
			t.Errorf("PhaseOutputs[%q] still references old ID: %s", phase, path)
		}
		expectedPath := filepath.Join(dstDir, phase+".output")
		if path != expectedPath {
			t.Errorf("PhaseOutputs[%q] = %q, want %q", phase, path, expectedPath)
		}
	}

	// Verify files were copied
	wantContent := map[string]string{
		"plan.output":      "plan result",
		"implement.output": "impl result",
	}
	for name, want := range wantContent {
		data, err := os.ReadFile(filepath.Join(dstDir, name))
		if err != nil {
			t.Errorf("expected copied file %s: %v", name, err)
			continue
		}
		if string(data) != want {
			t.Errorf("file %s content = %q, want %q", name, string(data), want)
		}
	}
}

func TestRetryFromScratch(t *testing.T) {
	q := newRetryTestQueue(t)
	cfg := newRetryTestConfig(t)
	now := time.Now().UTC()

	v := queue.Vessel{
		ID:           "issue-10",
		Source:       "manual",
		Workflow:     "fix-bug",
		State:        queue.StatePending,
		CreatedAt:    now,
		CurrentPhase: 2,
		WorktreePath: "/tmp/worktrees/issue-10",
		PhaseOutputs: map[string]string{"plan": "/some/path/plan.output"},
		FailedPhase:  "test",
	}
	q.Enqueue(v)                                       //nolint:errcheck
	q.Update("issue-10", queue.StateRunning, "")       //nolint:errcheck
	q.Update("issue-10", queue.StateFailed, "phase 3") //nolint:errcheck

	err := cmdRetry(q, cfg, "issue-10", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	retry, err := q.FindByID("issue-10-retry-1")
	if err != nil {
		t.Fatalf("retry vessel not found: %v", err)
	}

	if retry.CurrentPhase != 0 {
		t.Errorf("from-scratch: CurrentPhase = %d, want 0", retry.CurrentPhase)
	}
	if retry.WorktreePath != "" {
		t.Errorf("from-scratch: WorktreePath = %q, want empty", retry.WorktreePath)
	}
	if len(retry.PhaseOutputs) != 0 {
		t.Errorf("from-scratch: PhaseOutputs = %v, want empty", retry.PhaseOutputs)
	}
}

func TestRetryEmptyWorktreePathFallsBackToFromScratch(t *testing.T) {
	q := newRetryTestQueue(t)
	cfg := newRetryTestConfig(t)
	now := time.Now().UTC()

	v := queue.Vessel{
		ID:           "issue-10",
		Source:       "manual",
		Workflow:     "fix-bug",
		State:        queue.StatePending,
		CreatedAt:    now,
		CurrentPhase: 1,
		WorktreePath: "", // failed before worktree creation
		FailedPhase:  "plan",
	}
	q.Enqueue(v)                                           //nolint:errcheck
	q.Update("issue-10", queue.StateRunning, "")           //nolint:errcheck
	q.Update("issue-10", queue.StateFailed, "no worktree") //nolint:errcheck

	err := cmdRetry(q, cfg, "issue-10", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	retry, err := q.FindByID("issue-10-retry-1")
	if err != nil {
		t.Fatalf("retry vessel not found: %v", err)
	}

	if retry.CurrentPhase != 0 {
		t.Errorf("empty worktree fallback: CurrentPhase = %d, want 0", retry.CurrentPhase)
	}
	if retry.WorktreePath != "" {
		t.Errorf("empty worktree fallback: WorktreePath = %q, want empty", retry.WorktreePath)
	}
	if len(retry.PhaseOutputs) != 0 {
		t.Errorf("empty worktree fallback: PhaseOutputs = %v, want empty", retry.PhaseOutputs)
	}
}

func TestRewritePhaseOutputs(t *testing.T) {
	outputs := map[string]string{
		"plan":      "/home/user/.xylem/phases/issue-10/plan.output",
		"implement": "/home/user/.xylem/phases/issue-10/implement.output",
	}

	rewritten := rewritePhaseOutputs(outputs, "issue-10", "issue-10-retry-1")

	for phase, path := range rewritten {
		if strings.Contains(path, "/issue-10/") {
			t.Errorf("PhaseOutputs[%q] still has old ID: %s", phase, path)
		}
		if !strings.Contains(path, "/issue-10-retry-1/") {
			t.Errorf("PhaseOutputs[%q] missing new ID: %s", phase, path)
		}
	}
}

func TestRewritePhaseOutputsNil(t *testing.T) {
	result := rewritePhaseOutputs(nil, "old", "new")
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}

func TestCopyPhaseOutputFilesMissingSrcDir(t *testing.T) {
	stateDir := t.TempDir()
	// Source directory does not exist — should be a no-op
	if err := copyPhaseOutputFiles(stateDir, "nonexistent", "new"); err != nil {
		t.Fatalf("expected no error for missing src dir, got: %v", err)
	}
}
