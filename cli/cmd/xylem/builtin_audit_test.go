package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	reviewpkg "github.com/nicholls-inc/xylem/cli/internal/review"
)

type auditTestRunner struct {
	calls [][]string
}

func (r *auditTestRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return []byte("[]"), nil
}

func TestRunBuiltInScheduledVesselsCompletesContextWeightAudit(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		StateDir: dir,
		Claude: config.ClaudeConfig{
			Command:      "claude",
			DefaultModel: "claude-sonnet-4-6",
		},
		Sources: map[string]config.SourceConfig{
			"scheduled-audit": {
				Type:     "scheduled",
				Repo:     "owner/repo",
				Schedule: "24h",
				Tasks: map[string]config.Task{
					"context": {Workflow: reviewpkg.ContextWeightAuditWorkflow},
				},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, err := q.Enqueue(queue.Vessel{
		ID:        "scheduled-audit-1",
		Source:    "scheduled",
		Ref:       "scheduled://scheduled-audit/context@1",
		Workflow:  reviewpkg.ContextWeightAuditWorkflow,
		State:     queue.StatePending,
		CreatedAt: time.Now().UTC(),
		Meta: map[string]string{
			"config_source":         "scheduled-audit",
			"scheduled_task_name":   "context",
			"scheduled_bucket":      "1",
			"scheduled_config_name": "scheduled-audit",
		},
	})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	cmdRunner := &auditTestRunner{}
	result, err := runBuiltInScheduledVessels(context.Background(), cfg, q, cmdRunner)
	if err != nil {
		t.Fatalf("runBuiltInScheduledVessels() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}
	if result.Failed != 0 {
		t.Fatalf("Failed = %d, want 0", result.Failed)
	}

	vessel, err := q.FindByID("scheduled-audit-1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if vessel.State != queue.StateCompleted {
		t.Fatalf("vessel.State = %s, want %s", vessel.State, queue.StateCompleted)
	}

	if _, err := os.Stat(filepath.Join(dir, "reviews", "context-weight-audit.json")); err != nil {
		t.Fatalf("context-weight-audit.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "phases", "scheduled-audit-1", "summary.json")); err != nil {
		t.Fatalf("summary.json missing: %v", err)
	}
}
