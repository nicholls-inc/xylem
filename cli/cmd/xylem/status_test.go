package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func captureStdout(fn func()) string {
	old := os.Stdout
	pr, pw, _ := os.Pipe()
	os.Stdout = pw
	fn()
	pw.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, pr) //nolint:errcheck
	return buf.String()
}

func testStatusVessel(id string, state queue.VesselState) queue.Vessel {
	return queue.Vessel{
		ID: id, Source: "github-issue", Workflow: "fix-bug",
		State: state, CreatedAt: time.Now().UTC(),
	}
}

func testStatusConfig(dir string) *config.Config {
	return &config.Config{StateDir: filepath.Join(dir, ".xylem")}
}

func TestStatusEmpty(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No vessels") {
		t.Errorf("expected empty message, got: %s", out)
	}
}

func TestStatusTable(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	q.Enqueue(testStatusVessel("issue-42", queue.StatePending))   //nolint:errcheck
	q.Enqueue(testStatusVessel("issue-55", queue.StateCompleted)) //nolint:errcheck

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "issue-42") {
		t.Errorf("expected issue-42 in output, got: %s", out)
	}
	if !strings.Contains(out, "issue-55") {
		t.Errorf("expected issue-55 in output, got: %s", out)
	}
	if !strings.Contains(out, "pending") {
		t.Errorf("expected 'pending' state in output, got: %s", out)
	}
	if !strings.Contains(out, "completed") {
		t.Errorf("expected 'completed' state in output, got: %s", out)
	}
	if !strings.Contains(out, "Summary:") {
		t.Errorf("expected summary line, got: %s", out)
	}
	if !strings.Contains(out, "Info") {
		t.Errorf("expected Info column header, got: %s", out)
	}
}

func TestStatusJSON(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	q.Enqueue(testStatusVessel("issue-1", queue.StatePending)) //nolint:errcheck

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, true, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var vessels []queue.Vessel
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &vessels); err != nil {
		t.Fatalf("expected valid JSON, got: %s\nerr: %v", out, err)
	}
	if len(vessels) != 1 {
		t.Errorf("expected 1 vessel in JSON, got %d", len(vessels))
	}
}

func TestStatusStateFilter(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	q.Enqueue(testStatusVessel("issue-1", queue.StatePending))   //nolint:errcheck
	q.Enqueue(testStatusVessel("issue-2", queue.StateCompleted)) //nolint:errcheck

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "pending") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "issue-1") {
		t.Errorf("expected issue-1 in filtered output, got: %s", out)
	}
	if strings.Contains(out, "issue-2") {
		t.Errorf("expected issue-2 filtered out, got: %s", out)
	}
}

func TestStatusRunningVesselShowsDuration(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-2 * time.Minute)
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-10", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateRunning, CreatedAt: now, StartedAt: &started,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "issue-10") {
		t.Errorf("expected issue-10 in output, got: %s", out)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("expected 'running' state in output, got: %s", out)
	}
	if !strings.Contains(out, "2m") {
		t.Errorf("expected duration ~2m in output, got: %s", out)
	}
}

func TestStatusCompletedVesselShowsFixedDuration(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-5 * time.Minute)
	ended := now.Add(-2 * time.Minute)
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-20", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateCompleted, CreatedAt: now,
		StartedAt: &started, EndedAt: &ended,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "3m0s") {
		t.Errorf("expected duration '3m0s' in output, got: %s", out)
	}
}

func TestStatusShowsWaitingVessels(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-30 * time.Minute)
	waitingSince := now.Add(-10 * time.Minute)
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-99", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateWaiting, CreatedAt: now,
		StartedAt: &started, WaitingFor: "plan-approved",
		WaitingSince: &waitingSince,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "issue-99") {
		t.Errorf("expected issue-99 in output, got: %s", out)
	}
	if !strings.Contains(out, "waiting") {
		t.Errorf("expected 'waiting' state in output, got: %s", out)
	}
	if !strings.Contains(out, "plan-approved") {
		t.Errorf("expected 'plan-approved' label in info column, got: %s", out)
	}
	if !strings.Contains(out, "waiting for") {
		t.Errorf("expected 'waiting for' info text, got: %s", out)
	}
}

func TestStatusShowsTimedOutVessels(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-1 * time.Hour)
	ended := now.Add(-30 * time.Minute)
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-77", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateTimedOut, CreatedAt: now,
		StartedAt: &started, EndedAt: &ended,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "issue-77") {
		t.Errorf("expected issue-77 in output, got: %s", out)
	}
	if !strings.Contains(out, "timed_out") {
		t.Errorf("expected 'timed_out' state in output, got: %s", out)
	}
}

func TestStatusFilterByWaiting(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	waitingSince := now.Add(-5 * time.Minute)
	started := now.Add(-10 * time.Minute)

	q.Enqueue(testStatusVessel("issue-1", queue.StatePending)) //nolint:errcheck
	q.Enqueue(queue.Vessel{                                    //nolint:errcheck
		ID: "issue-2", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateWaiting, CreatedAt: now,
		StartedAt: &started, WaitingFor: "review",
		WaitingSince: &waitingSince,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "waiting") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "issue-2") {
		t.Errorf("expected issue-2 in filtered output, got: %s", out)
	}
	if strings.Contains(out, "issue-1") {
		t.Errorf("expected issue-1 filtered out, got: %s", out)
	}
}

func TestStatusSummaryCounts(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-10 * time.Minute)
	ended := now.Add(-5 * time.Minute)
	waitingSince := now.Add(-3 * time.Minute)

	q.Enqueue(testStatusVessel("v-1", queue.StatePending)) //nolint:errcheck
	q.Enqueue(queue.Vessel{                                //nolint:errcheck
		ID: "v-2", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateRunning, CreatedAt: now, StartedAt: &started,
	})
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "v-3", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateCompleted, CreatedAt: now,
		StartedAt: &started, EndedAt: &ended,
	})
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "v-4", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateFailed, CreatedAt: now,
		StartedAt: &started, EndedAt: &ended,
	})
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "v-5", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateCancelled, CreatedAt: now, EndedAt: &ended,
	})
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "v-6", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateWaiting, CreatedAt: now,
		StartedAt: &started, WaitingFor: "approval",
		WaitingSince: &waitingSince,
	})
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "v-7", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateTimedOut, CreatedAt: now,
		StartedAt: &started, EndedAt: &ended,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all counts appear in summary
	if !strings.Contains(out, "1 pending") {
		t.Errorf("expected '1 pending' in summary, got: %s", out)
	}
	if !strings.Contains(out, "1 running") {
		t.Errorf("expected '1 running' in summary, got: %s", out)
	}
	if !strings.Contains(out, "1 completed") {
		t.Errorf("expected '1 completed' in summary, got: %s", out)
	}
	if !strings.Contains(out, "1 failed") {
		t.Errorf("expected '1 failed' in summary, got: %s", out)
	}
	if !strings.Contains(out, "1 cancelled") {
		t.Errorf("expected '1 cancelled' in summary, got: %s", out)
	}
	if !strings.Contains(out, "1 waiting") {
		t.Errorf("expected '1 waiting' in summary, got: %s", out)
	}
	if !strings.Contains(out, "1 timed_out") {
		t.Errorf("expected '1 timed_out' in summary, got: %s", out)
	}
}

func TestStatusJSONIncludesWaitingFields(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-10 * time.Minute)
	waitingSince := now.Add(-5 * time.Minute)

	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-50", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateWaiting, CreatedAt: now,
		StartedAt: &started, WaitingFor: "plan-approved",
		WaitingSince: &waitingSince,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(testStatusConfig(dir), q, true, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var vessels []queue.Vessel
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &vessels); err != nil {
		t.Fatalf("expected valid JSON, got: %s\nerr: %v", out, err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	v := vessels[0]
	if v.WaitingFor != "plan-approved" {
		t.Errorf("expected waiting_for='plan-approved', got %q", v.WaitingFor)
	}
	if v.WaitingSince == nil {
		t.Error("expected waiting_since to be set")
	}
}

func TestStatusSurfacesHealthAndPatterns(t *testing.T) {
	dir := t.TempDir()
	cfg := testStatusConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-10 * time.Minute)
	ended := now.Add(-5 * time.Minute)

	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-1", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateCompleted, CreatedAt: now,
		StartedAt: &started, EndedAt: &ended,
	})
	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-2", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateFailed, CreatedAt: now,
		StartedAt: &started, EndedAt: &ended,
	})

	summaryDir := filepath.Join(cfg.StateDir, "phases", "issue-2")
	if err := os.MkdirAll(summaryDir, 0o755); err != nil {
		t.Fatalf("mkdir summary dir: %v", err)
	}
	summary := `{
  "vessel_id": "issue-2",
  "source": "github-issue",
  "workflow": "fix-bug",
  "state": "failed",
  "started_at": "2026-04-08T20:00:00Z",
  "ended_at": "2026-04-08T20:05:00Z",
  "duration_ms": 300000,
  "phases": [
    {
      "name": "implement",
      "type": "prompt",
      "duration_ms": 1000,
      "status": "failed",
      "gate_type": "command",
      "gate_passed": false,
      "error": "exit status 1"
    }
  ],
  "total_input_tokens_est": 0,
  "total_output_tokens_est": 0,
  "total_tokens_est": 0,
  "total_cost_usd_est": 0,
  "budget_exceeded": true,
  "note": "test"
}`
	if err := os.WriteFile(filepath.Join(summaryDir, "summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	var err error
	out := captureStdout(func() { err = cmdStatus(cfg, q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "Health") {
		t.Fatalf("expected Health column/header in output, got: %s", out)
	}
	if !strings.Contains(out, "issue-2") || !strings.Contains(out, "unhealthy") {
		t.Fatalf("expected unhealthy issue-2 row, got: %s", out)
	}
	if !strings.Contains(out, "budget exceeded") {
		t.Fatalf("expected anomaly details in output, got: %s", out)
	}
	if !strings.Contains(out, "Health: 1 healthy, 0 degraded, 1 unhealthy") {
		t.Fatalf("expected health summary counts, got: %s", out)
	}
	if !strings.Contains(out, "Patterns:") || !strings.Contains(out, "budget_exceeded=1") {
		t.Fatalf("expected pattern summary, got: %s", out)
	}
}

func TestStatusJSONIncludesHealthAndAnomalies(t *testing.T) {
	dir := t.TempDir()
	cfg := testStatusConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	waitingSince := now.Add(-5 * time.Minute)

	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "issue-3", Source: "github-issue", Workflow: "fix-bug",
		State: queue.StateWaiting, CreatedAt: now,
		WaitingFor: "plan-approved", WaitingSince: &waitingSince,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(cfg, q, true, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rows []statusRow
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("expected valid JSON, got: %s\nerr: %v", out, err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Health != "degraded" {
		t.Fatalf("expected degraded health, got %q", rows[0].Health)
	}
	if len(rows[0].Anomalies) != 1 || rows[0].Anomalies[0].Code != "waiting_on_gate" {
		t.Fatalf("expected waiting_on_gate anomaly, got %#v", rows[0].Anomalies)
	}
}

func TestStatusSkipsSummaryLookupForUnsafeVesselIDs(t *testing.T) {
	dir := t.TempDir()
	cfg := testStatusConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()

	q.Enqueue(queue.Vessel{ //nolint:errcheck
		ID: "manual/task", Source: "manual", Workflow: "fix-bug",
		State: queue.StatePending, CreatedAt: now,
	})

	var err error
	out := captureStdout(func() { err = cmdStatus(cfg, q, false, "") })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "manual/task") {
		t.Fatalf("expected unsafe vessel ID in output, got: %s", out)
	}
	if !strings.Contains(out, "healthy") {
		t.Fatalf("expected health column to still render, got: %s", out)
	}
}
