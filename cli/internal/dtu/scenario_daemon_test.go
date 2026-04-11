package dtu_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	dtu "github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconcileStaleVesselsForTest replicates the daemon's startup recovery
// behavior (from cli/cmd/xylem/daemon.go) so the DTU scenario test can
// exercise orphan recovery without importing the main package.
func reconcileStaleVesselsForTest(q *queue.Queue) (int, error) {
	vessels, err := q.List()
	if err != nil {
		return 0, err
	}

	reconciled := 0
	for _, v := range vessels {
		if v.State != queue.StateRunning {
			continue
		}
		if err := q.Update(v.ID, queue.StatePending, ""); err != nil {
			return reconciled, err
		}
		reconciled++
	}
	return reconciled, nil
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
	reconciled, err := reconcileStaleVesselsForTest(env.queue)
	if err != nil {
		t.Fatalf("reconcileStaleVesselsForTest() error = %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1", reconciled)
	}

	// Verify the orphaned vessel was requeued for another drain attempt.
	updated, err := env.queue.FindByID("issue-4")
	if err != nil {
		t.Fatalf("FindByID(issue-4) error = %v", err)
	}
	if updated.State != queue.StatePending {
		t.Fatalf("updated.State = %q, want %q", updated.State, queue.StatePending)
	}
	if updated.Error != "" {
		t.Fatalf("updated.Error = %q, want empty", updated.Error)
	}
	if updated.EndedAt != nil {
		t.Fatal("updated.EndedAt != nil, want cleared end timestamp")
	}

	// --- Phase 5: Daemon drains the recovered vessel on the next pass ---
	newVessel, err := env.queue.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if newVessel == nil {
		t.Fatal("second Dequeue() = nil, want recovered vessel")
		return
	}
	if newVessel.ID != "issue-4" {
		t.Fatalf("newVessel.ID = %q, want %q", newVessel.ID, "issue-4")
	}
	if newVessel.State != queue.StateRunning {
		t.Fatalf("newVessel.State = %q, want %q", newVessel.State, queue.StateRunning)
	}

	// Complete the recovered vessel to prove the pipeline is fully operational.
	if err := env.queue.Update("issue-4", queue.StateCompleted, ""); err != nil {
		t.Fatalf("Update(issue-4, completed) error = %v", err)
	}
	completed, err := env.queue.FindByID("issue-4")
	if err != nil {
		t.Fatalf("FindByID(issue-4) error = %v", err)
	}
	if completed.State != queue.StateCompleted {
		t.Fatalf("completed.State = %q, want %q", completed.State, queue.StateCompleted)
	}

	// Verify the full queue state: the recovered vessel eventually completes.
	vessels, err := env.queue.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	states := make(map[string]queue.VesselState)
	for _, v := range vessels {
		states[v.ID] = v.State
	}
	if states["issue-4"] != queue.StateCompleted {
		t.Fatalf("queue[issue-4] = %q, want %q", states["issue-4"], queue.StateCompleted)
	}

	// Verify DTU events were recorded during the scenario.
	events := readEvents(t, env.store)
	if len(events) == 0 {
		t.Fatal("no DTU events recorded")
	}
}

func TestSmoke_S4_DeterministicPhaseStallRecovery(t *testing.T) {
	env := newScenarioEnv(t, "issue-daemon-recovery.yaml")
	defer withWorkingDir(t, env.repoDir)()

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"
	cfg.Daemon.StallMonitor.OrphanCheckEnabled = false

	now, err := dtu.RuntimeNow()
	require.NoError(t, err)
	enqueued, err := env.queue.Enqueue(queue.Vessel{
		ID:        "stall-1",
		Source:    "manual",
		Workflow:  "fix-bug",
		State:     queue.StatePending,
		CreatedAt: now,
	})
	require.NoError(t, err)
	require.True(t, enqueued)
	vessel, err := env.queue.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)

	outputPath := config.RuntimePath(env.stateDir, "phases", vessel.ID, "analyze.output")
	require.NoError(t, os.MkdirAll(filepath.Dir(outputPath), 0o755))
	require.NoError(t, os.WriteFile(outputPath, []byte(""), 0o644))
	old := now.Add(-11 * time.Minute)
	require.NoError(t, os.Chtimes(outputPath, old, old))
	require.NoError(t, env.queue.UpdateVessel(*vessel))

	r := runner.New(cfg, env.queue, nil, env.cmdRunner)
	findings := r.CheckStalledVessels(context.Background())
	require.Len(t, findings, 1)
	assert.Equal(t, "phase_stalled", findings[0].Code)
	assert.Equal(t, "analyze", findings[0].Phase)

	updated, err := env.queue.FindByID(vessel.ID)
	require.NoError(t, err)
	assert.Equal(t, queue.StateTimedOut, updated.State)
	assert.Contains(t, updated.Error, "phase stalled: no output for")
	require.NotNil(t, updated.EndedAt)

	events := readEvents(t, env.store)
	assert.NotEmpty(t, events)
}
