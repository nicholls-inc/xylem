package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/daemonhealth"
	"github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

// drainShutdownTimeout is how long the daemon waits for an in-flight drain to
// finish after receiving a shutdown signal before returning anyway.
const drainShutdownTimeout = 30 * time.Second

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run continuous scan-drain loop",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdDaemon(deps.cfg, deps.q, deps.wt)
		},
	}
	return cmd
}

func cmdDaemon(cfg *config.Config, q *queue.Queue, wt *worktree.Manager) error {
	slog.Info("daemon starting", "commit", buildInfo())

	// Isolation check: refuse to run in the main git worktree because vessel
	// subprocesses may switch branches or modify the working tree, which
	// would corrupt the user's primary checkout.
	if !logDaemonWorktreeCheck() {
		return fmt.Errorf("daemon refused to start in main worktree; see log for instructions")
	}

	scanInterval, drainInterval := parseDaemonIntervals(cfg.Daemon)

	// P0-3: Acquire singleton lock to prevent multiple daemons.
	pidPath := filepath.Join(cfg.StateDir, "daemon.pid")
	unlock, err := acquireDaemonLock(pidPath)
	if err != nil {
		return err
	}
	defer unlock()

	// Auto-upgrade: pull latest main, rebuild, exec() if binary changed.
	// Runs after PID lock (prevents concurrent upgrades) and before any vessel
	// processing. If exec() fires, the new process re-acquires the lock.
	//
	// Captures the executable path and repo dir once so the same closure can
	// also be used for periodic upgrades inside the daemon loop.
	var upgrade upgradeFunc
	upgradeInterval := time.Duration(0)
	if cfg.Daemon.AutoUpgrade {
		target, err := resolveDaemonUpgradeTarget(os.Getwd, os.Executable)
		if err != nil {
			slog.Warn("daemon auto-upgrade skipped", "error", err)
		} else {
			selfUpgrade(target.repoDir, target.executablePath)

			upgrade = func() { selfUpgrade(target.repoDir, target.executablePath) }
			upgradeInterval = parseUpgradeInterval(cfg.Daemon)
		}
	}

	// P0-2: Reconcile any vessels left in running state from a previous daemon.
	// The singleton lock guarantees no other daemon is running, so all running
	// vessels are definitionally orphaned.
	if err := daemonStartup(context.Background(), cfg, q, wt, newCmdRunner(cfg)); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startedAt := daemonNow()
	lastActivityAt := startedAt
	lastUpgradeAt := startedAt
	idleThreshold := 5 * time.Minute
	if cfg.Daemon.StallMonitor.ScannerIdleThreshold != "" {
		if parsed, err := time.ParseDuration(cfg.Daemon.StallMonitor.ScannerIdleThreshold); err == nil {
			idleThreshold = parsed
		}
	}
	var backlogCount int
	var stallChecks []daemonhealth.Check
	scanRunner := newCmdRunner(cfg)
	scan := func(ctx context.Context) (scanner.ScanResult, error) {
		s := scanner.New(cfg, q, scanRunner)
		result, err := s.Scan(ctx)
		if err != nil {
			return result, err
		}
		if result.Added > 0 {
			lastActivityAt = daemonNow()
			backlogCount = 0
			return result, nil
		}
		if !daemonQueueIdle(q) {
			backlogCount = 0
			return result, nil
		}
		count, err := s.BacklogCount(ctx)
		if err != nil {
			slog.Warn("daemon backlog check failed", "error", err)
			backlogCount = 0
			return result, nil
		}
		backlogCount = count
		if count > 0 {
			idleFor := daemonNow().Sub(lastActivityAt)
			if idleFor > idleThreshold {
				slog.Warn("daemon idle with backlog", "count", count, "idle_for", idleFor)
			}
		}
		return result, nil
	}
	cmdRunner := newCmdRunner(cfg)
	drainRunner, cleanupDrainRunner := buildDrainRunner(cfg, q, wt, cmdRunner)
	defer cleanupDrainRunner()
	drainRunner.Reporter = buildReporter(cfg, cmdRunner)
	drainRunner.DrainBudget = drainInterval
	commandSpan := startCommandSpan(drainRunner.Tracer, ctx, "daemon", false, cfg.StateDir)
	var commandErr error
	defer func() {
		finishCommandSpan(drainRunner.Tracer, commandSpan, commandErr)
	}()
	if spanCtx := commandSpan.Context(); spanCtx != nil {
		ctx = spanCtx
	}
	drain := func(ctx context.Context) (runner.DrainResult, error) {
		return drainRunner.Drain(ctx)
	}
	check := func(ctx context.Context) {
		findings := drainRunner.CheckStalledVessels(ctx)
		stallChecks = daemonChecksFromFindings(findings, daemonNow())
		drainRunner.CheckWaitingVessels(ctx)
		drainRunner.CheckHungVessels(ctx)
		if removed := drainRunner.PruneStaleWorktrees(ctx); removed > 0 {
			slog.Info("daemon pruned stale worktrees", "removed", removed)
		}
		// Cancel pending vessels whose PRs are already merged/closed,
		// freeing concurrency slots for real work.
		drainRunner.CancelStalePRVessels(ctx)
		// Auto-merge: best-effort request copilot review on merge-ready
		// vessel PRs, then admin-merge once deterministic safety checks
		// are green.
		if cfg.Daemon.AutoMerge {
			autoMergeXylemPRs(ctx, cfg.Daemon)
		}
	}

	tickHook := func(now time.Time, state daemonTickState) {
		if !state.LastUpgrade.IsZero() {
			lastUpgradeAt = state.LastUpgrade
		}
		counts := daemonQueueCounts(q)
		if counts.pending > 0 || counts.running > 0 || counts.waiting > 0 || state.InFlight > 0 {
			lastActivityAt = now
		}
		checks := append([]daemonhealth.Check(nil), stallChecks...)
		if check := daemonBacklogHealthCheck(now, lastActivityAt, idleThreshold, backlogCount, counts); check != nil {
			checks = append(checks, *check)
		}
		snapshot := daemonhealth.Snapshot{
			PID:           os.Getpid(),
			StartedAt:     startedAt.UTC(),
			UpdatedAt:     now.UTC(),
			Binary:        buildInfo(),
			LastUpgradeAt: lastUpgradeAt.UTC(),
			Checks:        checks,
		}
		if err := daemonhealth.Save(cfg.StateDir, snapshot); err != nil {
			slog.Warn("daemon save health failed", "error", err)
		}
	}

	commandErr = daemonLoop(ctx, q, drainRunner, scan, drain, check, upgrade, tickHook, scanInterval, drainInterval, upgradeInterval)
	return commandErr
}

func daemonStartup(ctx context.Context, cfg *config.Config, q *queue.Queue, wt *worktree.Manager, seedRunner adaptRepoSeedRunner) error {
	reconcileStaleVessels(q, wt)

	if _, err := ensureAdaptRepoSeeded(ctx, cfg, seedRunner, adaptRepoSeededByDaemon); err != nil {
		slog.Warn("seed adapt-repo issue failed, continuing", "error", err)
	}
	return nil
}

// parseUpgradeInterval returns the effective periodic upgrade interval. If
// the config provides an explicit value, it is parsed; otherwise the default
// is returned.
func parseUpgradeInterval(dc config.DaemonConfig) time.Duration {
	if dc.UpgradeInterval == "" {
		return defaultUpgradeInterval
	}
	d, err := time.ParseDuration(dc.UpgradeInterval)
	if err != nil || d <= 0 {
		slog.Warn("daemon invalid upgrade interval; using default",
			"value", dc.UpgradeInterval,
			"default", defaultUpgradeInterval)
		return defaultUpgradeInterval
	}
	return d
}

func parseDaemonIntervals(dc config.DaemonConfig) (scan, drain time.Duration) {
	scan = 60 * time.Second
	drain = 30 * time.Second

	if dc.ScanInterval != "" {
		if d, err := time.ParseDuration(dc.ScanInterval); err == nil {
			scan = d
		}
	}
	if dc.DrainInterval != "" {
		if d, err := time.ParseDuration(dc.DrainInterval); err == nil {
			drain = d
		}
	}
	return scan, drain
}

// scanFunc and drainFunc abstract scan/drain for testability.
type scanFunc func(ctx context.Context) (scanner.ScanResult, error)
type drainFunc func(ctx context.Context) (runner.DrainResult, error)
type inFlightTracker interface {
	Wait() runner.DrainResult
	InFlightCount() int
}

// checkFunc runs periodic vessel health checks (waiting vessel label checks,
// hung vessel timeouts). May be nil if no checks are needed.
type checkFunc func(ctx context.Context)
type tickFunc func(now time.Time, state daemonTickState)

// upgradeFunc runs a self-upgrade attempt. If it succeeds with a binary
// change, it calls exec() and never returns. If it returns, either the
// binary was unchanged or the upgrade failed; the daemon continues normally.
// May be nil to disable periodic upgrades.
type upgradeFunc func()

type daemonTickState struct {
	LastUpgrade time.Time
	InFlight    int
	Draining    bool
}

// defaultUpgradeInterval is how often the daemon checks for a new binary.
// Five minutes balances fast activation of newly-merged fixes against
// excessive git fetches and rebuilds.
const defaultUpgradeInterval = 5 * time.Minute

// upgradeOverdueMultiplier controls the forced-upgrade escape hatch: after
// this many upgradeIntervals have elapsed without a successful upgrade
// (because the daemon was never idle enough to fire the normal path), the
// next drain tick is PAUSED — new vessels are not dequeued — so that the
// currently in-flight vessels can finish naturally and the upgrade condition
// (`in_flight == 0`) is eventually satisfied.
//
// At the default 5m upgradeInterval + 3x multiplier = 15 minutes of
// "upgrade pending" before drain starts gracefully parking. This preserves
// the safety invariant (no exec() while subprocesses are alive) while
// guaranteeing that a continuously-saturated scheduled source can never
// permanently lock the daemon on a stale binary.
//
// 3 is chosen so that a single healthy upgrade cycle (5m interval, fires
// reliably) is not perturbed, but a degraded "always saturated" condition
// is resolved in bounded time.
const upgradeOverdueMultiplier = 3

// daemonLoop is the core loop extracted for testability. It accepts an
// externally-controlled context so tests can cancel it without signals,
// and injectable scan/drain/check functions so tests can use stubs.
//
// If upgrade is non-nil, it is called under one of two conditions:
//
//  1. Normal path: daemon is fully idle (no active drain, zero in-flight
//     vessels) and upgradeInterval has elapsed since the last upgrade.
//
//  2. Overdue path: upgrade has been pending for
//     upgradeInterval*upgradeOverdueMultiplier without firing because the
//     daemon was never idle enough. In this case, new drain ticks are
//     paused (no new dequeue) so in-flight vessels can drain naturally.
//     Once in_flight reaches zero, the normal path fires.
//
// Pass nil/zero upgrade/upgradeInterval to disable.
func daemonLoop(ctx context.Context, q *queue.Queue, tracker inFlightTracker, scan scanFunc, drain drainFunc, check checkFunc, upgrade upgradeFunc, tick tickFunc, scanInterval, drainInterval, upgradeInterval time.Duration) error {
	tickInterval := scanInterval
	if drainInterval < tickInterval {
		tickInterval = drainInterval
	}

	var lastScan, lastDrain, lastUpgrade time.Time
	var draining int32 // 0=idle, 1=running
	var drainWg sync.WaitGroup

	// Initialise lastUpgrade to now so the first upgrade check happens after
	// upgradeInterval — not immediately, since cmdDaemon already ran the
	// startup upgrade.
	lastUpgrade = daemonNow()

	slog.Info("daemon started",
		"scan_interval", scanInterval,
		"drain_interval", drainInterval,
		"upgrade_interval", upgradeInterval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("daemon received shutdown signal", "waiting_for", "in-flight drain")
			waitDone := make(chan struct{})
			go func() {
				drainWg.Wait()
				if tracker != nil {
					tracker.Wait()
				}
				close(waitDone)
			}()
			select {
			case <-waitDone:
				slog.Info("daemon drain finished during shutdown")
			case <-time.After(drainShutdownTimeout):
				slog.Warn("daemon drain shutdown timeout exceeded", "timeout", drainShutdownTimeout)
			}
			return nil
		default:
		}

		now := daemonNow()

		if now.Sub(lastScan) >= scanInterval {
			scanResult, err := scan(ctx)
			if err != nil {
				slog.Error("daemon scan failed", "error", err)
			} else {
				lastScan = now
				slog.Info("daemon scan complete", "added", scanResult.Added, "skipped", scanResult.Skipped)
			}
		}

		// Run vessel health checks (waiting label gates, hung vessel timeouts)
		if check != nil {
			check(ctx)
		}

		// Compute upgrade state once per tick so both the upgrade check and
		// the drain-pause decision share a single snapshot of conditions.
		upgradeReady := upgrade != nil && upgradeInterval > 0
		upgradeElapsed := now.Sub(lastUpgrade)
		upgradePending := upgradeReady && upgradeElapsed >= upgradeInterval
		upgradeOverdue := upgradeReady && upgradeElapsed >= upgradeInterval*time.Duration(upgradeOverdueMultiplier)
		inFlight := trackerInFlightCount(tracker)
		drainIdle := atomic.LoadInt32(&draining) == 0
		drainingNow := !drainIdle

		if upgradePending && drainIdle && inFlight == 0 {
			// Normal path: daemon is fully idle and upgrade is due.
			lastUpgrade = now
			upgrade()
		} else if upgradeOverdue && drainIdle && inFlight > 0 {
			// Check for phantom in_flight: the runner's atomic counter can
			// get stuck when a goroutine is blocked on a killed subprocess
			// whose grandchildren hold stdout/stderr open. Detect by
			// comparing in_flight against actual queue state, but only
			// after a longer grace period (2x overdue = 6x upgrade
			// interval) to avoid racing with legitimate drain completions.
			// Phantom threshold: 30 minutes of overdue with 0 queue running.
			// This is deliberately long to avoid false positives during
			// legitimate drain wind-down (the S39 test scenario).
			phantomThreshold := 30 * time.Minute
			if upgradeElapsed >= phantomThreshold && daemonQueueCounts(q).running == 0 {
				slog.Warn("daemon auto-upgrade forcing past phantom in_flight",
					"elapsed", upgradeElapsed,
					"in_flight", inFlight,
					"queue_running", 0)
				lastUpgrade = now
				upgrade()
			} else {
				slog.Warn("daemon auto-upgrade overdue; pausing new drain dequeue until idle",
					"elapsed", upgradeElapsed,
					"in_flight", inFlight)
			}
		}

		// Drain dequeue is suppressed while an upgrade is overdue and
		// real in-flight vessels still exist. Once the phantom threshold
		// fires (6x upgrade interval with 0 queue running), the forced
		// upgrade above handles recovery via exec().
		phantomInFlight := upgradeElapsed >= 30*time.Minute && inFlight > 0 && daemonQueueCounts(q).running == 0
		drainPaused := upgradeOverdue && inFlight > 0 && !phantomInFlight
		if !drainPaused && now.Sub(lastDrain) >= drainInterval {
			if atomic.CompareAndSwapInt32(&draining, 0, 1) {
				lastDrain = now
				drainWg.Add(1)
				go func() {
					defer drainWg.Done()
					defer atomic.StoreInt32(&draining, 0)
					drainResult, err := drain(ctx)
					if err != nil {
						slog.Error("daemon drain failed", "error", err)
						return
					}
					slog.Info("daemon drain complete",
						"launched", drainResult.Launched,
						"completed", drainResult.Completed,
						"failed", drainResult.Failed,
						"skipped", drainResult.Skipped,
						"waiting", drainResult.Waiting,
						"in_flight", trackerInFlightCount(tracker))
				}()
			}
		}

		logTickSummary(q)
		if tick != nil {
			tick(now, daemonTickState{
				LastUpgrade: lastUpgrade,
				InFlight:    inFlight,
				Draining:    drainingNow,
			})
		}

		select {
		case <-ctx.Done():
			slog.Info("daemon received shutdown signal", "waiting_for", "in-flight drain")
			waitDone := make(chan struct{})
			go func() {
				drainWg.Wait()
				if tracker != nil {
					tracker.Wait()
				}
				close(waitDone)
			}()
			select {
			case <-waitDone:
				slog.Info("daemon drain finished during shutdown")
			case <-time.After(drainShutdownTimeout):
				slog.Warn("daemon drain shutdown timeout exceeded", "timeout", drainShutdownTimeout)
			}
			return nil
		case <-daemonAfter(ctx, tickInterval):
		}
	}
}

func runScan(ctx context.Context, cfg *config.Config, q *queue.Queue) (scanner.ScanResult, error) {
	cmdRunner := newCmdRunner(cfg)
	s := scanner.New(cfg, q, cmdRunner)
	return s.Scan(ctx)
}

func runDrain(ctx context.Context, cfg *config.Config, q *queue.Queue, wt *worktree.Manager, budget time.Duration) (runner.DrainResult, error) {
	cmdRunner := newCmdRunner(cfg)
	r, cleanup := buildDrainRunner(cfg, q, wt, cmdRunner)
	defer cleanup()
	r.DrainBudget = budget
	commandSpan := startCommandSpan(r.Tracer, ctx, "run-drain", false, cfg.StateDir)
	var commandErr error
	defer func() {
		finishCommandSpan(r.Tracer, commandSpan, commandErr)
	}()
	if spanCtx := commandSpan.Context(); spanCtx != nil {
		ctx = spanCtx
	}
	builtInResult, err := runBuiltInScheduledVessels(ctx, cfg, q, cmdRunner)
	if err != nil {
		commandErr = err
		return builtInResult, fmt.Errorf("drain built-in audit vessels: %w", err)
	}
	result, err := r.DrainAndWait(ctx)
	addDrainResults(&result, builtInResult)
	if err == nil {
		maybeAutoGenerateHarnessReview(cfg, result)
	}
	commandErr = err
	return result, err
}

func trackerInFlightCount(tracker inFlightTracker) int {
	if tracker == nil {
		return 0
	}
	return tracker.InFlightCount()
}

func logTickSummary(q *queue.Queue) {
	vessels, err := q.List()
	if err != nil {
		return
	}
	counts := make(map[queue.VesselState]int)
	for _, v := range vessels {
		counts[v.State]++
	}
	slog.Info("daemon tick summary",
		"pending", counts[queue.StatePending],
		"running", counts[queue.StateRunning],
		"completed", counts[queue.StateCompleted],
		"failed", counts[queue.StateFailed])
}

type daemonQueueSnapshot struct {
	pending int
	running int
	waiting int
}

func daemonQueueCounts(q *queue.Queue) daemonQueueSnapshot {
	vessels, err := q.List()
	if err != nil {
		return daemonQueueSnapshot{}
	}
	var snapshot daemonQueueSnapshot
	for _, vessel := range vessels {
		switch vessel.State {
		case queue.StatePending:
			snapshot.pending++
		case queue.StateRunning:
			snapshot.running++
		case queue.StateWaiting:
			snapshot.waiting++
		}
	}
	return snapshot
}

func daemonQueueIdle(q *queue.Queue) bool {
	counts := daemonQueueCounts(q)
	return counts.pending == 0 && counts.running == 0
}

func daemonChecksFromFindings(findings []runner.StallFinding, now time.Time) []daemonhealth.Check {
	checks := make([]daemonhealth.Check, 0, len(findings))
	for _, finding := range findings {
		level := daemonhealth.LevelCritical
		if finding.Level == "warning" {
			level = daemonhealth.LevelWarning
		}
		checks = append(checks, daemonhealth.Check{
			Code:      finding.Code,
			Level:     level,
			Message:   finding.Message,
			UpdatedAt: now.UTC(),
		})
	}
	return checks
}

func daemonBacklogHealthCheck(now, lastActivityAt time.Time, idleThreshold time.Duration, backlogCount int, counts daemonQueueSnapshot) *daemonhealth.Check {
	if backlogCount == 0 || counts.pending != 0 || counts.running != 0 {
		return nil
	}
	if now.Sub(lastActivityAt) <= idleThreshold {
		return nil
	}
	return &daemonhealth.Check{
		Code:      "idle_with_backlog",
		Level:     daemonhealth.LevelWarning,
		Message:   fmt.Sprintf("Daemon idle with %d backlog items on GitHub", backlogCount),
		UpdatedAt: now.UTC(),
	}
}

// reconcileStaleVessels requeues ALL running vessels as pending work. The
// singleton daemon lock guarantees that no other daemon is executing vessels
// when this runs, so every running vessel is orphaned by definition. Restart
// recovery preserves completed phase progress but clears crash-specific runtime
// fields so the daemon can safely resume the vessel on the next drain tick.
func reconcileStaleVessels(q *queue.Queue, wt *worktree.Manager) {
	vessels, err := q.List()
	if err != nil {
		slog.Error("daemon reconcile failed to list vessels", "error", err)
		return
	}

	var recovered int
	for _, v := range vessels {
		if v.State != queue.StateRunning {
			continue
		}
		slog.Warn("daemon reconcile found orphaned running vessel", "vessel", v.ID, "target_state", queue.StatePending)
		if err := q.Update(v.ID, queue.StatePending, ""); err != nil {
			slog.Error("daemon reconcile failed to update vessel", "vessel", v.ID, "error", err)
			continue
		}
		recovered++

		// Best-effort worktree cleanup.
		if v.WorktreePath != "" && wt != nil {
			if err := wt.Remove(context.Background(), v.WorktreePath); err != nil {
				slog.Error("daemon reconcile failed to remove worktree", "vessel", v.ID, "worktree", v.WorktreePath, "error", err)
			}
		}
	}
	if recovered > 0 {
		slog.Info("daemon reconcile requeued orphaned vessels", "recovered", recovered)
	}
}

func daemonNow() time.Time {
	now, err := dtu.RuntimeNow()
	if err != nil {
		slog.Warn("daemon resolve runtime clock", "error", err)
		return time.Now()
	}
	return now
}

func daemonAfter(ctx context.Context, delay time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		if err := dtu.RuntimeSleep(ctx, delay); err != nil {
			return
		}
		ch <- daemonNow()
	}()
	return ch
}

// acquireDaemonLock tries to acquire an exclusive file lock on the given PID
// file path. On success it writes the current PID and returns an unlock
// function. On failure (another daemon holds the lock) it returns an error
// that includes the PID of the existing daemon.
func acquireDaemonLock(pidPath string) (unlock func(), err error) {
	// Ensure the parent directory exists.
	if mkErr := os.MkdirAll(filepath.Dir(pidPath), 0o755); mkErr != nil {
		return nil, fmt.Errorf("daemon lock: create state dir: %w", mkErr)
	}

	fl := flock.New(pidPath)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("daemon lock: %w", err)
	}
	if !locked {
		// Try to read existing PID for a helpful error message.
		if data, readErr := os.ReadFile(pidPath); readErr == nil {
			if pid, parseErr := strconv.Atoi(string(data)); parseErr == nil {
				return nil, fmt.Errorf("daemon already running (PID %d)", pid)
			}
		}
		return nil, fmt.Errorf("daemon already running (could not read PID)")
	}

	// Write our PID to the file.
	pid := os.Getpid()
	if writeErr := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o644); writeErr != nil {
		fl.Unlock() //nolint:errcheck
		return nil, fmt.Errorf("daemon lock: write PID: %w", writeErr)
	}

	slog.Info("daemon acquired lock", "path", pidPath, "pid", pid)

	return func() {
		if unlockErr := fl.Unlock(); unlockErr != nil {
			slog.Error("daemon failed to release lock", "path", pidPath, "error", unlockErr)
		}
		os.Remove(pidPath) //nolint:errcheck
	}, nil
}
