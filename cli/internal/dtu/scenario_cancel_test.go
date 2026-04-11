package dtu_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	dtu "github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	runnerpkg "github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingResolveScenarioCmdRunner struct {
	base         *dtuScenarioCmdRunner
	resolveStart chan struct{}
	resolveExit  chan struct{}
	startOnce    sync.Once
	exitOnce     sync.Once
	pushStarted  atomic.Int32
}

func newBlockingResolveScenarioCmdRunner(base *dtuScenarioCmdRunner) *blockingResolveScenarioCmdRunner {
	return &blockingResolveScenarioCmdRunner{
		base:         base,
		resolveStart: make(chan struct{}),
		resolveExit:  make(chan struct{}),
	}
}

func (r *blockingResolveScenarioCmdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.base.Run(ctx, name, args...)
}

func (r *blockingResolveScenarioCmdRunner) RunOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.base.RunOutput(ctx, name, args...)
}

func (r *blockingResolveScenarioCmdRunner) RunProcess(ctx context.Context, dir string, name string, args ...string) error {
	return r.base.RunProcess(ctx, dir, name, args...)
}

func (r *blockingResolveScenarioCmdRunner) RunPhase(ctx context.Context, _ string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	return r.RunPhaseWithEnv(ctx, "", nil, stdin, "")
}

func (r *blockingResolveScenarioCmdRunner) RunPhaseWithEnv(ctx context.Context, _ string, _ []string, stdin io.Reader, _ string, _ ...string) ([]byte, error) {
	promptBytes, err := io.ReadAll(stdin)
	if err != nil {
		return nil, err
	}
	prompt := string(promptBytes)

	switch {
	case strings.Contains(prompt, "Analyze conflicts"):
		return []byte("analysis complete"), nil
	case strings.Contains(prompt, "Resolve conflicts"):
		r.startOnce.Do(func() { close(r.resolveStart) })
		<-ctx.Done()
		r.exitOnce.Do(func() { close(r.resolveExit) })
		return nil, ctx.Err()
	case strings.Contains(prompt, "Push branch"):
		r.pushStarted.Add(1)
		return []byte("push complete"), nil
	default:
		return []byte("mock output"), nil
	}
}

func TestScenarioIssueCancelDuringResolveStopsRunner(t *testing.T) {
	env := newScenarioEnv(t, "issue-cancel-resolve.yaml")
	defer withWorkingDir(t, env.repoDir)()

	writeScenarioWorkflow(t, env.repoDir, "resolve-conflicts", []scenarioPhase{
		{name: "analyze", prompt: "Analyze conflicts for issue {{.Issue.Number}}"},
		{name: "resolve", prompt: "Resolve conflicts for issue {{.Issue.Number}}"},
		{name: "push", prompt: "Push branch for issue {{.Issue.Number}}"},
	})

	cfg := staticIssueConfig(env.stateDir, config.Task{
		Labels:   []string{"needs-conflict-resolution"},
		Workflow: "resolve-conflicts",
		StatusLabels: &config.StatusLabels{
			Running: "in-progress",
		},
	})

	scanResult := scanScenario(t, env, cfg)
	require.Equal(t, 1, scanResult.Added)

	cmdRunner := newBlockingResolveScenarioCmdRunner(env.cmdRunner)
	src := &source.GitHub{Repo: "owner/repo", CmdRunner: cmdRunner}
	drainer := runnerpkg.New(cfg, env.queue, worktree.New(env.repoDir, cmdRunner), cmdRunner)
	drainer.Sources = map[string]source.Source{
		src.Name(): src,
	}

	drainResult, err := drainer.Drain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, drainResult.Launched)

	select {
	case <-cmdRunner.resolveStart:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resolve phase to start")
	}

	require.NoError(t, env.queue.Cancel("issue-181"))

	select {
	case <-cmdRunner.resolveExit:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resolve phase to stop after cancel")
	}

	waitDone := make(chan runnerpkg.DrainResult, 1)
	go func() {
		waitDone <- drainer.Wait()
	}()

	var waited runnerpkg.DrainResult
	select {
	case waited = <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runner.Wait()")
	}

	assert.Equal(t, 1, waited.Skipped)
	assert.Zero(t, drainer.InFlightCount())
	assert.Zero(t, cmdRunner.pushStarted.Load())

	vessel, err := env.queue.FindByID("issue-181")
	require.NoError(t, err)
	require.NotNil(t, vessel)
	assert.Equal(t, queue.StateCancelled, vessel.State)

	events := readEvents(t, env.store)
	cancelSeen := false
	for _, event := range events {
		if event.Vessel == nil {
			continue
		}
		if event.Vessel.Operation == dtu.VesselOperationCancel && event.Vessel.VesselID == "issue-181" {
			cancelSeen = true
			break
		}
	}
	assert.True(t, cancelSeen, "expected DTU cancel event for issue-181")

	summary := loadVesselSummary(t, env.stateDir, "issue-181")
	assert.Equal(t, "cancelled", summary.State)
	assert.FileExists(t, config.RuntimePath(env.stateDir, "phases", "issue-181", "summary.json"))
}
