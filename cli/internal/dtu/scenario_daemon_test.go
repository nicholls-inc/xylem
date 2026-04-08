package dtu_test

import (
	"context"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	dtu "github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
)

// reconcileStaleVesselsForTest replicates the daemon's stale-vessel
// reconciliation logic (from cli/cmd/xylem/daemon.go) so the DTU scenario
// test can exercise recovery without importing the main package.
// The singleton daemon lock guarantees all running vessels are orphaned,
// so no timeout heuristic is needed.
func reconcileStaleVesselsForTest(q *queue.Queue) int {
	vessels, err := q.List()
	if err != nil {
		return 0
	}

	reconciled := 0
	for _, v := range vessels {
		if v.State != queue.StateRunning {
			continue
		}
		q.Update(v.ID, queue.StateTimedOut, "orphaned by daemon restart") //nolint:errcheck
		reconciled++
	}
	return reconciled
}

func TestScenarioDaemonRecovery(t *testing.T) {
	env := newScenarioEnv(t, "issue-daemon-recovery.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "fix-bug", []scenarioPhase{
		{name: "implement", prompt: "Implement issue {{.Issue.Number}}"},
	})

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Timeout = "45m"
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:  "queued",
						Running: "in-progress",
						Failed:  "failed",
					},
				},
			},
		},
	}

	// --- Phase 1: Scan the issue into the queue ---
	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	scanResult, err := scan.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if scanResult.Added != 1 {
		t.Fatalf("ScanResult.Added = %d, want 1", scanResult.Added)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 4), []string{"bug", "queued"})

	// --- Phase 2: Dequeue and simulate the daemon running the vessel ---
	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	vessel, err := env.queue.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if vessel == nil {
		t.Fatal("Dequeue() = nil, want vessel")
		return
	}
	if vessel.State != queue.StateRunning {
		t.Fatalf("vessel.State = %q, want %q", vessel.State, queue.StateRunning)
	}

	if err := src.OnStart(context.Background(), *vessel); err != nil {
		t.Fatalf("OnStart() error = %v", err)
	}
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 4), []string{"bug", "in-progress"})

	if vessel.StartedAt == nil {
		t.Fatal("vessel.StartedAt = nil after Dequeue()")
	}

	// --- Phase 3: Daemon restarts and reconciles orphaned vessels ---
	// No need to advance time — the singleton lock means all running vessels
	// are orphaned by definition.
	reconciled := reconcileStaleVesselsForTest(env.queue)
	if reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1", reconciled)
	}

	// Verify the orphaned vessel transitioned to timed_out.
	updated, err := env.queue.FindByID("issue-4")
	if err != nil {
		t.Fatalf("FindByID(issue-4) error = %v", err)
	}
	if updated.State != queue.StateTimedOut {
		t.Fatalf("updated.State = %q, want %q", updated.State, queue.StateTimedOut)
	}
	if updated.Error != "orphaned by daemon restart" {
		t.Fatalf("updated.Error = %q, want %q", updated.Error, "orphaned by daemon restart")
	}
	if updated.EndedAt == nil {
		t.Fatal("updated.EndedAt = nil, want end timestamp")
	}

	// --- Phase 5: Daemon continues processing new vessels after recovery ---
	// Enqueue a fresh manual vessel to simulate continued daemon operation.
	now, err := dtu.RuntimeNow()
	if err != nil {
		t.Fatalf("RuntimeNow() error = %v", err)
	}
	enqueued, err := env.queue.Enqueue(queue.Vessel{
		ID:        "manual-recovery-1",
		Source:    "manual",
		Ref:       "",
		Workflow:  "fix-bug",
		Prompt:    "recovery test",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Enqueue(manual-recovery-1) error = %v", err)
	}
	if !enqueued {
		t.Fatal("Enqueue(manual-recovery-1) = false, want true")
	}

	// Dequeue and verify the new vessel enters running state.
	newVessel, err := env.queue.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if newVessel == nil {
		t.Fatal("second Dequeue() = nil, want vessel")
		return
	}
	if newVessel.ID != "manual-recovery-1" {
		t.Fatalf("newVessel.ID = %q, want %q", newVessel.ID, "manual-recovery-1")
	}
	if newVessel.State != queue.StateRunning {
		t.Fatalf("newVessel.State = %q, want %q", newVessel.State, queue.StateRunning)
	}

	// Complete the new vessel to prove the pipeline is fully operational.
	if err := env.queue.Update("manual-recovery-1", queue.StateCompleted, ""); err != nil {
		t.Fatalf("Update(manual-recovery-1, completed) error = %v", err)
	}
	completed, err := env.queue.FindByID("manual-recovery-1")
	if err != nil {
		t.Fatalf("FindByID(manual-recovery-1) error = %v", err)
	}
	if completed.State != queue.StateCompleted {
		t.Fatalf("completed.State = %q, want %q", completed.State, queue.StateCompleted)
	}

	// Verify the full queue state: orphaned vessel timed_out, new vessel completed.
	vessels, err := env.queue.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	states := make(map[string]queue.VesselState)
	for _, v := range vessels {
		states[v.ID] = v.State
	}
	if states["issue-4"] != queue.StateTimedOut {
		t.Fatalf("queue[issue-4] = %q, want %q", states["issue-4"], queue.StateTimedOut)
	}
	if states["manual-recovery-1"] != queue.StateCompleted {
		t.Fatalf("queue[manual-recovery-1] = %q, want %q", states["manual-recovery-1"], queue.StateCompleted)
	}

	// Verify DTU events were recorded during the scenario.
	events := readEvents(t, env.store)
	if len(events) == 0 {
		t.Fatal("no DTU events recorded")
	}
}
