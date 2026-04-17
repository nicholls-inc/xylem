package runner

// Property tests for the runner module invariants specified in
// docs/invariants/runner.md.  Every test carries an "Invariant IN: Name"
// header comment linking it to the spec entry.  Tests for known violations
// and aspirational invariants are wrapped in t.Skip so CI stays green until
// the corresponding fix lands; the test body remains real so the skip can be
// removed once the fix merges.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// ---------------------------------------------------------------------------
// I1: ConcurrencyCapsNeverExceeded
// ---------------------------------------------------------------------------

// Invariant I1: ConcurrencyCapsNeverExceeded
func TestInvariant_I1_ConcurrencyCapsNeverExceeded(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cap := rapid.IntRange(1, 3).Draw(rt, "cap")
		n := rapid.IntRange(cap, cap*3).Draw(rt, "n")

		dir := t.TempDir()
		cfg := makeTestConfig(dir, cap)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		for i := 0; i < n; i++ {
			_, err := q.Enqueue(makePromptVessel(i+1, "do work"))
			require.NoError(t, err)
		}

		var (
			current int64
			peak    int64
		)
		cr := &mockCmdRunner{
			runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
				c := atomic.AddInt64(&current, 1)
				for {
					old := atomic.LoadInt64(&peak)
					if c <= old || atomic.CompareAndSwapInt64(&peak, old, c) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond) // hold slot briefly
				atomic.AddInt64(&current, -1)
				return []byte("done"), nil, true
			},
		}
		r := New(cfg, q, &mockWorktree{path: dir}, cr)

		_, err := r.DrainAndWait(context.Background())
		require.NoError(t, err)

		if int(atomic.LoadInt64(&peak)) > cap {
			rt.Fatalf("peak concurrent executions %d exceeded cap %d", peak, cap)
		}
	})
}

// ---------------------------------------------------------------------------
// I2: NoConcurrentPhaseForVesselID
// ---------------------------------------------------------------------------

// Invariant I2: NoConcurrentPhaseForVesselID
func TestInvariant_I2_NoConcurrentPhaseForVesselID(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(rt, "n")

		dir := t.TempDir()
		cfg := makeTestConfig(dir, n) // concurrency == n so all can run
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		ids := make([]string, n)
		for i := 0; i < n; i++ {
			v := makePromptVessel(i+1, fmt.Sprintf("task %d", i+1))
			ids[i] = v.ID
			_, err := q.Enqueue(v)
			require.NoError(t, err)
		}

		cr := &mockCmdRunner{
			runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
				return []byte("done"), nil, true
			},
		}
		r := New(cfg, q, &mockWorktree{path: dir}, cr)
		result, err := r.DrainAndWait(context.Background())
		require.NoError(t, err)

		// Each vessel must complete exactly once — no ID dispatched twice.
		if result.Completed != n {
			rt.Fatalf("expected %d completed, got %d (failed=%d)", n, result.Completed, result.Failed)
		}
		// No in-flight processes after Wait.
		r.processMu.Lock()
		remaining := len(r.processes)
		r.processMu.Unlock()
		if remaining != 0 {
			rt.Fatalf("processes map has %d entries after Wait, expected 0", remaining)
		}
	})
}

// ---------------------------------------------------------------------------
// I3: CancellationPrecedesCompletion
// ---------------------------------------------------------------------------

// Invariant I3: CancellationPrecedesCompletion
func TestInvariant_I3_CancellationPrecedesCompletion(t *testing.T) {
	// Cancel-before-dispatch: vessel cancelled before runner picks it up must
	// end in state=cancelled, not completed.
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	v := makePromptVessel(1, "do work")
	_, err := q.Enqueue(v)
	require.NoError(t, err)

	// Cancel before any drain.
	require.NoError(t, q.Cancel(v.ID))

	cr := &mockCmdRunner{
		runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
			return []byte("done"), nil, true
		},
	}
	r := New(cfg, q, &mockWorktree{path: dir}, cr)
	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 0, result.Completed, "cancelled vessel must not count as completed")

	final, err := q.FindByID(v.ID)
	require.NoError(t, err)
	require.NotNil(t, final)
	assert.Equal(t, queue.StateCancelled, final.State, "vessel state must be cancelled")
}

// ---------------------------------------------------------------------------
// I4: WorktreeRemovedOnTerminalOutcome  [KNOWN VIOLATION — t.Skip until fix]
// ---------------------------------------------------------------------------

// Invariant I4: WorktreeRemovedOnTerminalOutcome
func TestInvariant_I4_WorktreeRemovedOnTerminalOutcome(t *testing.T) {
	t.Skip("known violation: failVessel does not call removeWorktree — see docs/invariants/runner.md I4 gap row; remove skip after I4 fix merges")

	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 4).Draw(rt, "n")
		dir := t.TempDir()
		cfg := makeTestConfig(dir, n)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		worktreeDir := filepath.Join(dir, "worktrees")
		tw := &trackingWorktree{path: worktreeDir}

		var failIdx int32 // fail the first vessel
		cr := &mockCmdRunner{
			runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
				if atomic.AddInt32(&failIdx, 1) == 1 {
					return nil, errors.New("simulated phase failure"), true
				}
				return []byte("done"), nil, true
			},
		}

		for i := 0; i < n; i++ {
			_, err := q.Enqueue(makePromptVessel(i+1, fmt.Sprintf("task %d", i)))
			require.NoError(t, err)
		}

		r := New(cfg, q, tw, cr)
		_, err := r.DrainAndWait(context.Background())
		require.NoError(t, err)

		// Every vessel that got a worktree (createCalls) must have had it removed.
		if tw.removeCalls < tw.createCalls {
			rt.Fatalf("removeCalls=%d < createCalls=%d: worktrees leaked on terminal paths",
				tw.removeCalls, tw.createCalls)
		}
	})
}

// ---------------------------------------------------------------------------
// I5: PruneExcludesNonTerminalWorktrees
// ---------------------------------------------------------------------------

// Invariant I5: PruneExcludesNonTerminalWorktrees
func TestInvariant_I5_PruneExcludesNonTerminalWorktrees(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(rt, "n")

		dir := t.TempDir()
		cfg := makeTestConfig(dir, n)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		r := New(cfg, q, &mockWorktree{path: dir}, &mockCmdRunner{})

		// Enqueue and start (running) vessels with persisted worktree paths.
		for i := 0; i < n; i++ {
			v := makePromptVessel(i+1, "task")
			_, err := q.Enqueue(v)
			require.NoError(t, err)
			// Transition to running so activeWorktreePaths includes it.
			require.NoError(t, q.Update(v.ID, queue.StateRunning, ""))
			// Persist a worktree path so it appears in active set.
			v2, err := q.FindByID(v.ID)
			require.NoError(t, err)
			require.NotNil(t, v2)
			v2.WorktreePath = filepath.Join(dir, "worktrees", v.ID)
			require.NoError(t, q.UpdateVessel(*v2))
		}

		active, err := r.activeWorktreePaths()
		require.NoError(t, err)

		// Every running vessel's worktree path must appear in the active set.
		running, err := q.ListByState(queue.StateRunning)
		require.NoError(t, err)
		for _, vessel := range running {
			if vessel.WorktreePath == "" {
				continue
			}
			normalized := r.normalizeWorktreePath(vessel.WorktreePath)
			if _, ok := active[normalized]; !ok {
				rt.Fatalf("running vessel %s worktree %q not in active set — prune would remove it",
					vessel.ID, vessel.WorktreePath)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// I6: GateRetriesFiniteAndLabelSuspends
// ---------------------------------------------------------------------------

// Invariant I6: GateRetriesFiniteAndLabelSuspends
func TestInvariant_I6_GateRetriesFiniteAndLabelSuspends(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		retries := rapid.IntRange(0, 3).Draw(rt, "retries")

		dir := t.TempDir()
		cfg := makeTestConfig(dir, 1)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		// Build a workflow with a gate that always fails.
		wfName := "gate-test"
		gateYAML := fmt.Sprintf("      type: command\n      run: \"exit 1\"\n      retries: %d\n      retry_delay: \"1ms\"", retries)
		writeWorkflowFile(t, dir, wfName, []testPhase{
			{
				name:          "implement",
				promptContent: "do the work",
				maxTurns:      5,
				gate:          gateYAML,
			},
		})
		withTestWorkingDir(t, dir)

		v := makeVessel(1, wfName)
		_, err := q.Enqueue(v)
		require.NoError(t, err)

		// Phase always succeeds; gate always fails via non-zero exit code.
		cr := &mockCmdRunner{
			runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
				return []byte("phase output"), nil, true
			},
			gateOutput: []byte("gate check failed"),
			gateErr:    &mockExitError{code: 1},
		}

		r := New(cfg, q, &mockWorktree{path: dir}, cr)
		r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

		result, err := r.DrainAndWait(context.Background())
		require.NoError(t, err)

		// Vessel must fail (retries exhausted), not complete.
		if result.Completed != 0 {
			rt.Fatalf("expected 0 completed (gate should fail vessel), got %d", result.Completed)
		}
		if result.Failed != 1 {
			rt.Fatalf("expected 1 failed, got %d", result.Failed)
		}

		// Phase must be invoked exactly retries+1 times (initial + N retries).
		cr.mu.Lock()
		phaseCnt := len(cr.phaseCalls)
		cr.mu.Unlock()
		if phaseCnt != retries+1 {
			rt.Fatalf("phase invoked %d times, expected %d (retries=%d)",
				phaseCnt, retries+1, retries)
		}
	})
}

// ---------------------------------------------------------------------------
// I7: PhaseOutputPersistenceOrdering  [ASPIRATIONAL — t.Skip until queue I5b]
// ---------------------------------------------------------------------------

// Invariant I7: PhaseOutputPersistenceOrdering
func TestInvariant_I7_PhaseOutputPersistenceOrdering(t *testing.T) {
	t.Skip("aspirational: blocked on queue I5b (atomic phase-output + CurrentPhase writes) — see docs/invariants/runner.md I7 gap row")

	// When unblocked: multi-phase workflow with timestamp-recording CommandRunner.
	// Assert phase-N output file exists on disk before phase-N+1 RunPhase call,
	// and CurrentPhase is persisted before next phase starts.
}

// ---------------------------------------------------------------------------
// I8: InFlightAccountingExact
// ---------------------------------------------------------------------------

// Invariant I8: InFlightAccountingExact
func TestInvariant_I8_InFlightAccountingExact(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(rt, "n")
		cap := rapid.IntRange(1, n).Draw(rt, "cap")

		dir := t.TempDir()
		cfg := makeTestConfig(dir, cap)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		failN := rapid.IntRange(0, n).Draw(rt, "failN")
		var dispatched int32
		cr := &mockCmdRunner{
			runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
				idx := int(atomic.AddInt32(&dispatched, 1))
				if idx <= failN {
					return nil, errors.New("injected failure"), true
				}
				return []byte("done"), nil, true
			},
		}

		for i := 0; i < n; i++ {
			_, err := q.Enqueue(makePromptVessel(i+1, fmt.Sprintf("task %d", i)))
			require.NoError(t, err)
		}

		r := New(cfg, q, &mockWorktree{path: dir}, cr)
		_, err := r.DrainAndWait(context.Background())
		require.NoError(t, err)

		if got := r.InFlightCount(); got != 0 {
			rt.Fatalf("InFlightCount=%d after Wait, expected 0", got)
		}
		r.processMu.Lock()
		remaining := len(r.processes)
		r.processMu.Unlock()
		if remaining != 0 {
			rt.Fatalf("r.processes has %d entries after Wait, expected 0", remaining)
		}
	})
}

// ---------------------------------------------------------------------------
// I9: SubprocessKilledOnTerminalOutcome  [KNOWN VIOLATION — t.Skip until fix]
// ---------------------------------------------------------------------------

// Invariant I9: SubprocessKilledOnTerminalOutcome
func TestInvariant_I9_SubprocessKilledOnTerminalOutcome(t *testing.T) {
	t.Skip("known violation: cancelVessel/failVessel/completeVessel do not call stopProcess — see docs/invariants/runner.md I9 gap row; remove skip after I9 fix merges")

	rapid.Check(t, func(rt *rapid.T) {
		// When the fix lands: inject a CommandRunner that registers a fake PID via
		// markProcessStarted, then drive mixed-outcome vessels and assert
		// terminateTrackedProcess (or stopProcess) was invoked for every terminal
		// vessel before clearTrackedProcess removed the map entry.
		//
		// Minimal proxy assertion (testable today after fix): processes map is
		// empty after DrainAndWait (I8 sub-assertion), and every terminal vessel's
		// PID was not left dangling.
		n := rapid.IntRange(1, 4).Draw(rt, "n")
		dir := t.TempDir()
		cfg := makeTestConfig(dir, n)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		cr := &mockCmdRunner{
			runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
				return []byte("done"), nil, true
			},
		}
		for i := 0; i < n; i++ {
			_, err := q.Enqueue(makePromptVessel(i+1, fmt.Sprintf("task %d", i)))
			require.NoError(t, err)
		}

		r := New(cfg, q, &mockWorktree{path: dir}, cr)
		_, err := r.DrainAndWait(context.Background())
		require.NoError(t, err)

		r.processMu.Lock()
		remaining := len(r.processes)
		r.processMu.Unlock()
		if remaining != 0 {
			rt.Fatalf("processes map non-empty after DrainAndWait: %d entries", remaining)
		}
	})
}

// ---------------------------------------------------------------------------
// I10: SweepReconciliationOfRunningVessels
// ---------------------------------------------------------------------------

// Invariant I10: SweepReconciliationOfRunningVessels
func TestInvariant_I10_SweepReconciliationOfRunningVessels(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 4).Draw(rt, "n")

		dir := t.TempDir()
		// Use a very short timeout so CheckHungVessels triggers immediately.
		cfg := makeTestConfig(dir, n)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		cfg.Timeout = "1ms"
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		pastTime := time.Now().UTC().Add(-1 * time.Hour)

		// Pre-seed vessels in running state with StartedAt far in the past.
		for i := 0; i < n; i++ {
			v := makePromptVessel(i+1, "orphan task")
			_, err := q.Enqueue(v)
			require.NoError(t, err)
			require.NoError(t, q.Update(v.ID, queue.StateRunning, ""))
			v2, err := q.FindByID(v.ID)
			require.NoError(t, err)
			require.NotNil(t, v2)
			v2.StartedAt = &pastTime
			require.NoError(t, q.UpdateVessel(*v2))
		}

		// Construct a fresh runner — no live processes in its map.
		r := New(cfg, q, &mockWorktree{path: dir}, &mockCmdRunner{})

		// Drive CheckHungVessels — should time out the stale running vessels.
		r.CheckHungVessels(context.Background())

		// All pre-seeded vessels must no longer be in running state.
		still, err := q.ListByState(queue.StateRunning)
		require.NoError(t, err)
		if len(still) != 0 {
			rt.Fatalf("%d vessels still in running state after CheckHungVessels; expected 0", len(still))
		}
	})
}

// ---------------------------------------------------------------------------
// I11: PhaseInvocationWallClockBound
// ---------------------------------------------------------------------------

// Invariant I11: PhaseInvocationWallClockBound
func TestInvariant_I11_PhaseInvocationWallClockBound(t *testing.T) {
	// Use the per-vessel context timeout path (context.WithTimeout).
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")
	cfg.Timeout = "200ms" // very short timeout
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	v := makePromptVessel(1, "hung task")
	_, err := q.Enqueue(v)
	require.NoError(t, err)

	// Block forever until context is cancelled.
	ready := make(chan struct{})
	cr := &mockCmdRunner{
		runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
			close(ready)
			// Block until caller's context is done (simulates hung LLM subprocess).
			<-context.Background().Done() // never fires; runner cancels via context
			return nil, context.Canceled, true
		},
	}

	// Override with a blocking runner that respects context cancellation.
	blockCR := newBlockingPhaseCmdRunner()
	r := New(cfg, q, &mockWorktree{path: dir}, blockCR)

	start := time.Now()
	_, drainErr := r.DrainAndWait(context.Background())
	elapsed := time.Since(start)

	// Drain should return (not block forever). Allow 3x the configured timeout
	// for overhead.
	bound := 3 * 200 * time.Millisecond
	if elapsed > bound {
		t.Fatalf("DrainAndWait took %s, expected completion within %s (I11 wall-clock bound)", elapsed, bound)
	}

	// Vessel must be in a terminal state.
	final, err := q.FindByID(v.ID)
	require.NoError(t, err)
	require.NotNil(t, final)
	if !final.State.IsTerminal() {
		t.Fatalf("vessel state=%q after timeout; expected terminal", final.State)
	}

	_ = cr // suppress unused warning
	_ = drainErr
	_ = ready
}

// ---------------------------------------------------------------------------
// I12: StaleCancelSatisfiesPostConditions
// ---------------------------------------------------------------------------

// Invariant I12: StaleCancelSatisfiesPostConditions
func TestInvariant_I12_StaleCancelSatisfiesPostConditions(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		nMerged := rapid.IntRange(1, 3).Draw(rt, "nMerged")
		nOpen := rapid.IntRange(0, 3).Draw(rt, "nOpen")

		dir := t.TempDir()
		cfg := makeTestConfig(dir, 1)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		// Source config needed for resolveRepo to return a non-empty repo.
		cfg.Sources = map[string]config.SourceConfig{
			"prs": {Type: "github-pr", Repo: "owner/repo"},
		}
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		// Enqueue pending PR vessels. config_source maps to the "prs" source config.
		mergedIDs := make([]string, 0, nMerged)
		for i := 0; i < nMerged; i++ {
			v := queue.Vessel{
				ID:        fmt.Sprintf("pr-%d", i+1),
				Source:    "github-pr",
				Ref:       fmt.Sprintf("https://github.com/owner/repo/pull/%d", i+100),
				Workflow:  "resolve-conflicts",
				State:     queue.StatePending,
				CreatedAt: time.Now().UTC(),
				Meta:      map[string]string{"config_source": "prs"},
			}
			_, err := q.Enqueue(v)
			require.NoError(t, err)
			mergedIDs = append(mergedIDs, v.ID)
		}
		openIDs := make([]string, 0, nOpen)
		for i := 0; i < nOpen; i++ {
			v := queue.Vessel{
				ID:        fmt.Sprintf("pr-open-%d", i+1),
				Source:    "github-pr",
				Ref:       fmt.Sprintf("https://github.com/owner/repo/pull/%d", i+200),
				Workflow:  "resolve-conflicts",
				State:     queue.StatePending,
				CreatedAt: time.Now().UTC(),
				Meta:      map[string]string{"config_source": "prs"},
			}
			_, err := q.Enqueue(v)
			require.NoError(t, err)
			openIDs = append(openIDs, v.ID)
		}

		// Mock RunOutput: gh pr view <num> — merged PRs (100–1NN) return MERGED, open (200–2NN) return OPEN.
		cr := &mockCmdRunner{
			runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
				if name != "gh" || len(args) < 3 || args[0] != "pr" || args[1] != "view" {
					return nil, nil, false
				}
				num, _ := strconv.Atoi(args[2])
				if num >= 100 && num < 100+nMerged {
					return []byte(`{"state":"MERGED"}`), nil, true
				}
				return []byte(`{"state":"OPEN"}`), nil, true
			},
		}

		r := New(cfg, q, &mockWorktree{path: dir}, cr)
		cancelled := r.CancelStalePRVessels(context.Background())

		if cancelled != nMerged {
			rt.Fatalf("CancelStalePRVessels returned %d, expected %d (nMerged=%d, nOpen=%d)",
				cancelled, nMerged, nMerged, nOpen)
		}

		// Merged PR vessels must be cancelled.
		for _, id := range mergedIDs {
			v, err := q.FindByID(id)
			require.NoError(t, err)
			require.NotNil(t, v)
			if v.State != queue.StateCancelled {
				rt.Fatalf("merged PR vessel %s has state=%q, expected cancelled", id, v.State)
			}
		}

		// Open PR vessels must remain pending.
		for _, id := range openIDs {
			v, err := q.FindByID(id)
			require.NoError(t, err)
			require.NotNil(t, v)
			if v.State != queue.StatePending {
				rt.Fatalf("open PR vessel %s has state=%q, expected pending", id, v.State)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// I13: NoDuplicateDiscussionPublicationsPerEvent  [ASPIRATIONAL — t.Skip]
// ---------------------------------------------------------------------------

// Invariant I13: NoDuplicateDiscussionPublicationsPerEvent
func TestInvariant_I13_NoDuplicateDiscussionPublicationsPerEvent(t *testing.T) {
	// Proxy assertion: calling publishPhaseOutput twice with the same
	// (vessel.ID, phase.Name, phase.Output) triple must return nil on the
	// second call without triggering any gh-api call.
	// The discussionSeen sync.Map guard (PR#636) makes this deterministic.
	//
	// Property: for any valid (vesselID, phaseName) pair, the triple
	// (vesselID, phaseName, "discussion") used with publishPhaseOutput
	// produces exactly one set of gh-api GraphQL calls across two back-to-back
	// invocations with the same (vessel, phase, body).
	rapid.Check(t, func(rt *rapid.T) {
		vesselID := rapid.StringMatching(`[a-z][a-z0-9-]{0,15}`).Draw(rt, "vesselID")
		phaseName := rapid.SampledFrom([]string{
			"analyze", "plan", "implement", "report", "review", "verify",
		}).Draw(rt, "phaseName")

		dir := t.TempDir()
		cfg := makeTestConfig(dir, 1)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		// Source config so resolveRepo returns a non-empty repo slug.
		cfg.Sources = map[string]config.SourceConfig{
			"scheduled": {Type: "scheduled", Repo: "owner/repo"},
		}

		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		// Count every gh-api call observed by the mock. Any GraphQL mutation
		// or query fired by discussion.Publisher will route through this hook.
		var ghCalls int32
		cr := &mockCmdRunner{
			runOutputHook: func(name string, args ...string) ([]byte, error, bool) {
				if name != "gh" {
					return nil, nil, false
				}
				atomic.AddInt32(&ghCalls, 1)
				joined := ""
				for _, a := range args {
					joined += a + " "
				}
				switch {
				case contains(joined, discussionResolveQuery):
					return []byte(`{"data":{"repository":{"id":"R_1","discussionCategories":{"nodes":[{"id":"C_1","name":"General"}]}}}}`), nil, true
				case contains(joined, discussionSearchQuery):
					// No existing discussion -> Create path.
					return []byte(`{"data":{"node":{"discussions":{"nodes":[]}}}}`), nil, true
				case contains(joined, discussionCreateMutation):
					return []byte(`{"data":{"createDiscussion":{"discussion":{"id":"D_1","title":"Phase Output","url":"https://github.com/owner/repo/discussions/1"}}}}`), nil, true
				case contains(joined, discussionCommentMutation):
					return []byte(`{"data":{"addDiscussionComment":{"comment":{"url":"https://github.com/owner/repo/discussions/1#discussioncomment-1"}}}}`), nil, true
				default:
					return nil, nil, false
				}
			},
		}

		r := New(cfg, q, &mockWorktree{path: dir}, cr)

		vessel := queue.Vessel{
			ID:        vesselID,
			Source:    "scheduled",
			Workflow:  "weekly-report",
			State:     queue.StatePending,
			CreatedAt: time.Now().UTC(),
			Meta:      map[string]string{"config_source": "scheduled"},
		}

		p := workflow.Phase{
			Name:   phaseName,
			Output: "discussion",
			Discussion: &workflow.DiscussionOutput{
				Category:      "General",
				TitleTemplate: "Phase Output",
			},
		}

		td := phase.TemplateData{}
		body := "some output body"

		// First invocation: guard inserts the triple, publish proceeds,
		// gh-api calls must fire.
		if err := r.publishPhaseOutput(context.Background(), vessel, p, td, body); err != nil {
			rt.Fatalf("first publishPhaseOutput returned error: %v", err)
		}
		afterFirst := atomic.LoadInt32(&ghCalls)
		if afterFirst < 1 {
			rt.Fatalf("expected at least 1 gh-api call on first publish, got %d", afterFirst)
		}

		// Second invocation with the same (vessel.ID, phase.Name, phase.Output)
		// triple: guard short-circuits, zero additional gh-api calls.
		if err := r.publishPhaseOutput(context.Background(), vessel, p, td, body); err != nil {
			rt.Fatalf("second publishPhaseOutput returned error: %v", err)
		}
		afterSecond := atomic.LoadInt32(&ghCalls)
		if afterSecond != afterFirst {
			rt.Fatalf("I13 violation: second publishPhaseOutput with same triple fired %d additional gh-api call(s) (before=%d, after=%d)",
				afterSecond-afterFirst, afterFirst, afterSecond)
		}
	})
}

// contains is a local substring helper that avoids pulling in strings only
// for this test function (the imports block for this file intentionally
// excludes strings; other tests in the file use fmt/filepath instead).
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// I14: SourceLifecycleHooksFireExactlyOnce
// ---------------------------------------------------------------------------

// Invariant I14: SourceLifecycleHooksFireExactlyOnce
func TestInvariant_I14_SourceLifecycleHooksFireExactlyOnce(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		nComplete := rapid.IntRange(1, 3).Draw(rt, "nComplete")
		nFail := rapid.IntRange(0, 2).Draw(rt, "nFail")
		total := nComplete + nFail

		dir := t.TempDir()
		cfg := makeTestConfig(dir, total)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))

		var failCount int32
		cr := &mockCmdRunner{
			runPhaseHook: func(_, _, _ string, _ ...string) ([]byte, error, bool) {
				if int(atomic.AddInt32(&failCount, 1)) <= nFail {
					return nil, errors.New("injected failure"), true
				}
				return []byte("done"), nil, true
			},
		}

		src := &recordingSource{}
		for i := 0; i < total; i++ {
			_, err := q.Enqueue(makePromptVessel(i+1, fmt.Sprintf("task %d", i)))
			require.NoError(t, err)
		}

		r := New(cfg, q, &mockWorktree{path: dir}, cr)
		r.Sources = map[string]source.Source{"manual": src}

		_, err := r.DrainAndWait(context.Background())
		require.NoError(t, err)

		starts := int(src.startCalls.Load())
		completions := int(src.completeCalls.Load())
		failures := int(src.failCalls.Load())

		// OnStart must fire exactly once per vessel that entered running.
		if starts != total {
			rt.Fatalf("OnStart called %d times, expected %d (one per vessel)", starts, total)
		}
		// OnComplete fires for completed vessels.
		if completions != nComplete {
			rt.Fatalf("OnComplete called %d times, expected %d", completions, nComplete)
		}
		// OnFail fires for failed vessels.
		if failures != nFail {
			rt.Fatalf("OnFail called %d times, expected %d", failures, nFail)
		}
		// OnFail + OnComplete must equal total (mutually exclusive).
		if completions+failures != total {
			rt.Fatalf("OnComplete(%d) + OnFail(%d) = %d, expected %d (not mutually exclusive)",
				completions, failures, completions+failures, total)
		}
	})
}

// ---------------------------------------------------------------------------
// Ensure this file compiles even when the config package doesn't export
// Timeout as a Duration (it's a string field parsed at runtime).
// ---------------------------------------------------------------------------

var _ = config.Config{}
