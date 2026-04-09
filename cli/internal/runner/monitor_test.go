package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProcessTracker struct {
	info           map[string]ProcessInfo
	terminated     []string
	gracePeriods   []time.Duration
	terminateError error
}

func (f *fakeProcessTracker) ProcessInfo(vesselID string) (ProcessInfo, bool) {
	info, ok := f.info[vesselID]
	return info, ok
}

func (f *fakeProcessTracker) TerminateProcess(vesselID string, gracePeriod time.Duration) error {
	f.terminated = append(f.terminated, vesselID)
	f.gracePeriods = append(f.gracePeriods, gracePeriod)
	if f.info != nil {
		delete(f.info, vesselID)
	}
	return f.terminateError
}

func TestSmoke_S1_PhaseLevelStallTimesOutVessel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-11 * time.Minute)
	vessel := queue.Vessel{
		ID:           "issue-1",
		Source:       "manual",
		Workflow:     "fix-bug",
		State:        queue.StateRunning,
		CreatedAt:    now.Add(-12 * time.Minute),
		StartedAt:    &started,
		WorktreePath: "/tmp/worktree-1",
	}
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	phaseDir := filepath.Join(dir, "phases", vessel.ID)
	require.NoError(t, os.MkdirAll(phaseDir, 0o755))
	outputPath := filepath.Join(phaseDir, "analyze.output")
	require.NoError(t, os.WriteFile(outputPath, []byte("stale"), 0o644))
	staleAt := now.Add(-11 * time.Minute)
	require.NoError(t, os.Chtimes(outputPath, staleAt, staleAt))

	wt := &mockWorktree{}
	tracker := &fakeProcessTracker{
		info: map[string]ProcessInfo{
			vessel.ID: {PID: 4242, Phase: "analyze", StartedAt: started, Live: true},
		},
	}
	r := New(cfg, q, wt, &mockCmdRunner{})
	r.ProcessTracker = tracker

	alerts := r.CheckStalledVessels(context.Background())
	require.Len(t, alerts, 1)
	assert.Equal(t, "phase_stalled", alerts[0].Code)
	assert.Equal(t, "analyze", alerts[0].Phase)
	require.Equal(t, []string{vessel.ID}, tracker.terminated)
	require.Equal(t, []time.Duration{phaseStallTerminationGracePeriod}, tracker.gracePeriods)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	require.Equal(t, queue.StateTimedOut, updated.State)
	assert.Contains(t, updated.Error, "phase stalled: no output for")
	assert.True(t, wt.removeCalled)
	assert.Equal(t, vessel.WorktreePath, wt.removePath)
}

func TestSmoke_S3_OrphanedSubprocessTimesOutVessel(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"
	cfg.Daemon.StallMonitor.OrphanCheckEnabled = true

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-11 * time.Minute)
	vessel := queue.Vessel{
		ID:           "issue-2",
		Source:       "manual",
		Workflow:     "fix-bug",
		State:        queue.StateRunning,
		CreatedAt:    now.Add(-3 * time.Minute),
		StartedAt:    &started,
		WorktreePath: "/tmp/worktree-2",
	}
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	wt := &mockWorktree{}
	r := New(cfg, q, wt, &mockCmdRunner{})
	r.ProcessTracker = &fakeProcessTracker{info: map[string]ProcessInfo{}}

	alerts := r.CheckStalledVessels(context.Background())
	require.Len(t, alerts, 1)
	assert.Equal(t, "orphaned_subprocess", alerts[0].Code)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	require.Equal(t, queue.StateTimedOut, updated.State)
	assert.Equal(t, "vessel orphaned (no live subprocess)", updated.Error)
	assert.True(t, wt.removeCalled)
	assert.Equal(t, vessel.WorktreePath, wt.removePath)
}

func TestCheckStalledVesselsSkipsRecentOrphanActivity(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"
	cfg.Daemon.StallMonitor.OrphanCheckEnabled = true

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-2 * time.Minute)
	vessel := queue.Vessel{
		ID:        "issue-2b",
		Source:    "manual",
		Workflow:  "fix-bug",
		State:     queue.StateRunning,
		CreatedAt: now.Add(-3 * time.Minute),
		StartedAt: &started,
	}
	_, err := q.Enqueue(vessel)
	require.NoError(t, err)

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	r.ProcessTracker = &fakeProcessTracker{info: map[string]ProcessInfo{}}

	alerts := r.CheckStalledVessels(context.Background())
	require.Empty(t, alerts)

	updated, err := q.FindByID(vessel.ID)
	require.NoError(t, err)
	require.Equal(t, queue.StateRunning, updated.State)
}

func TestCheckStalledVesselsSkipsRecentActivity(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()
	started := now.Add(-2 * time.Minute)
	vessel := queue.Vessel{
		ID:        "issue-3",
		Source:    "manual",
		Workflow:  "fix-bug",
		State:     queue.StateRunning,
		CreatedAt: now.Add(-3 * time.Minute),
		StartedAt: &started,
	}
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	phaseDir := filepath.Join(dir, "phases", vessel.ID)
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", phaseDir, err)
	}
	outputPath := filepath.Join(phaseDir, "analyze.output")
	if err := os.WriteFile(outputPath, []byte("fresh"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", outputPath, err)
	}

	r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
	r.ProcessTracker = &fakeProcessTracker{
		info: map[string]ProcessInfo{
			vessel.ID: {PID: 7, Phase: "analyze", StartedAt: started, Live: true},
		},
	}

	if alerts := r.CheckStalledVessels(context.Background()); len(alerts) != 0 {
		t.Fatalf("len(alerts) = %d, want 0", len(alerts))
	}
	updated, err := q.FindByID(vessel.ID)
	if err != nil {
		t.Fatalf("FindByID(%q): %v", vessel.ID, err)
	}
	if updated.State != queue.StateRunning {
		t.Fatalf("updated.State = %q, want %q", updated.State, queue.StateRunning)
	}
}
