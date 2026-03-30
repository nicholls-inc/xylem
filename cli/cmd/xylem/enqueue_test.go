package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func newEnqueueTestQueue(t *testing.T, dir string) *queue.Queue {
	t.Helper()
	return queue.New(filepath.Join(dir, "queue.jsonl"))
}

func setupWorkflowDir(t *testing.T, dir string, workflows ...string) {
	t.Helper()
	wfDir := filepath.Join(dir, ".xylem", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	for _, name := range workflows {
		path := filepath.Join(wfDir, name+".yaml")
		if err := os.WriteFile(path, []byte("name: "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write workflow file: %v", err)
		}
	}
}

func TestEnqueueNonexistentWorkflowReturnsError(t *testing.T) {
	dir := t.TempDir()
	setupWorkflowDir(t, dir, "fix-bug", "implement-feature", "refine-issue")
	q := newEnqueueTestQueue(t, dir)
	stateDir := filepath.Join(dir, ".xylem")

	err := cmdEnqueue(q, stateDir, "refinement", "", "do something", "", "manual", "test-1")
	if err == nil {
		t.Fatal("expected error for nonexistent workflow")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to contain 'not found', got: %v", err)
	}
	if !strings.Contains(err.Error(), "refinement") {
		t.Errorf("expected error to mention workflow name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "available workflows:") {
		t.Errorf("expected error to list available workflows, got: %v", err)
	}
	if !strings.Contains(err.Error(), "fix-bug") {
		t.Errorf("expected available workflows to include fix-bug, got: %v", err)
	}
	if !strings.Contains(err.Error(), "refine-issue") {
		t.Errorf("expected available workflows to include refine-issue, got: %v", err)
	}
}

func TestEnqueuePromptOnlySkipsWorkflowValidation(t *testing.T) {
	dir := t.TempDir()
	q := newEnqueueTestQueue(t, dir)
	stateDir := filepath.Join(dir, ".xylem")

	err := cmdEnqueue(q, stateDir, "", "", "do something", "", "manual", "test-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vessels, _ := q.List()
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].Prompt != "do something" {
		t.Errorf("expected prompt 'do something', got %q", vessels[0].Prompt)
	}
}

func TestEnqueueValidWorkflowSucceeds(t *testing.T) {
	dir := t.TempDir()
	setupWorkflowDir(t, dir, "fix-bug", "implement-feature")
	q := newEnqueueTestQueue(t, dir)
	stateDir := filepath.Join(dir, ".xylem")

	err := cmdEnqueue(q, stateDir, "fix-bug", "https://github.com/owner/repo/issues/1", "", "", "manual", "test-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vessels, _ := q.List()
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].Workflow != "fix-bug" {
		t.Errorf("expected workflow 'fix-bug', got %q", vessels[0].Workflow)
	}
}

func TestEnqueueNonexistentWorkflowNoWorkflowsAvailable(t *testing.T) {
	dir := t.TempDir()
	q := newEnqueueTestQueue(t, dir)
	stateDir := filepath.Join(dir, ".xylem")

	err := cmdEnqueue(q, stateDir, "nonexistent", "", "do something", "", "manual", "test-1")
	if err == nil {
		t.Fatal("expected error for nonexistent workflow")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to contain 'not found', got: %v", err)
	}
	// When no workflows directory exists, should not mention "available workflows"
	if strings.Contains(err.Error(), "available workflows:") {
		t.Errorf("expected no available workflows listed when dir is missing, got: %v", err)
	}
}
