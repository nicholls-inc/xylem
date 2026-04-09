package dtu_test

import (
	"context"
	"io"
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

type stallScenarioCmdRunner struct{}

func (stallScenarioCmdRunner) RunOutput(context.Context, string, ...string) ([]byte, error) {
	return []byte{}, nil
}

func (stallScenarioCmdRunner) RunProcess(context.Context, string, string, ...string) error {
	return nil
}

func (stallScenarioCmdRunner) RunPhase(context.Context, string, io.Reader, string, ...string) ([]byte, error) {
	return []byte("ok"), nil
}

type stallScenarioWorktree struct {
	removedPath string
}

func (s *stallScenarioWorktree) Create(context.Context, string) (string, error) {
	return ".claude/worktrees/stall", nil
}

func (s *stallScenarioWorktree) Remove(_ context.Context, worktreePath string) error {
	s.removedPath = worktreePath
	return nil
}

type stallScenarioTracker struct {
	info       map[string]runner.ProcessInfo
	terminated []string
}

func (s *stallScenarioTracker) ProcessInfo(vesselID string) (runner.ProcessInfo, bool) {
	info, ok := s.info[vesselID]
	return info, ok
}

func (s *stallScenarioTracker) TerminateProcess(vesselID string, _ time.Duration) error {
	s.terminated = append(s.terminated, vesselID)
	delete(s.info, vesselID)
	return nil
}

func TestSmoke_S4_DaemonPhaseStallRecovery(t *testing.T) {
	env := newScenarioEnv(t, "issue-daemon-stall.yaml")
	defer withWorkingDir(t, env.repoDir)()

	cfg := baseScenarioConfig(env.stateDir)
	cfg.Sources = map[string]config.SourceConfig{
		"issues": {
			Type: "github",
			Repo: "owner/repo",
			Tasks: map[string]config.Task{
				"bugs": {
					Labels:   []string{"bug"},
					Workflow: "fix-bug",
					StatusLabels: &config.StatusLabels{
						Queued:   "queued",
						Running:  "in-progress",
						TimedOut: "timed-out",
					},
				},
			},
		},
	}
	cfg.Daemon.StallMonitor.PhaseStallThreshold = "10m"

	scan := scanner.New(cfg, env.queue, env.cmdRunner)
	result, err := scan.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Added)

	src := &source.GitHub{Repo: "owner/repo", CmdRunner: env.cmdRunner}
	vessel, err := env.queue.Dequeue()
	require.NoError(t, err)
	require.NotNil(t, vessel)
	vessel.WorktreePath = ".claude/worktrees/issue-4"
	require.NoError(t, env.queue.UpdateVessel(*vessel))
	require.NoError(t, src.OnStart(context.Background(), *vessel))

	phaseDir := filepath.Join(env.stateDir, "phases", vessel.ID)
	require.NoError(t, os.MkdirAll(phaseDir, 0o755))
	outputPath := filepath.Join(phaseDir, "analyze.output")
	require.NoError(t, os.WriteFile(outputPath, []byte("stalled"), 0o644))
	now, err := dtu.RuntimeNow()
	require.NoError(t, err)
	staleAt := now.Add(-11 * time.Minute)
	require.NoError(t, os.Chtimes(outputPath, staleAt, staleAt))

	wt := &stallScenarioWorktree{}
	r := runner.New(cfg, env.queue, wt, stallScenarioCmdRunner{})
	r.Sources = map[string]source.Source{"issues": src, "github-issue": src}
	tracker := &stallScenarioTracker{
		info: map[string]runner.ProcessInfo{
			vessel.ID: {
				PID:       1234,
				Phase:     "analyze",
				StartedAt: staleAt,
				Live:      true,
			},
		},
	}
	r.ProcessTracker = tracker

	alerts := r.CheckStalledVessels(context.Background())
	require.Len(t, alerts, 1)
	assert.Equal(t, "phase_stalled", alerts[0].Code)
	assert.Equal(t, "analyze", alerts[0].Phase)

	updated, err := env.queue.FindByID(vessel.ID)
	require.NoError(t, err)
	require.Equal(t, queue.StateTimedOut, updated.State)
	assert.Equal(t, vessel.WorktreePath, wt.removedPath)
	assert.Equal(t, []string{vessel.ID}, tracker.terminated)
	assertStringSliceEqual(t, readIssueLabels(t, env.store, "owner/repo", 4), []string{"bug", "timed-out"})

	events := readEvents(t, env.store)
	assert.NotEmpty(t, events)
}
