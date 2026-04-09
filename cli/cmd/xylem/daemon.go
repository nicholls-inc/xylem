package main

import (
	"context"
	"fmt"
	"log"
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
	log.Printf("daemon: starting, binary built from commit %s", buildInfo())

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
		execPath, execErr := os.Executable()
		if execErr != nil {
			log.Printf("daemon: auto-upgrade: resolve executable path: %v (skipping upgrade)", execErr)
		} else {
			repoDir := filepath.Dir(filepath.Dir(execPath))
			selfUpgrade(repoDir, execPath)

			upgrade = func() { selfUpgrade(repoDir, execPath) }
			upgradeInterval = parseUpgradeInterval(cfg.Daemon)
		}
	}

	// P0-2: Reconcile any vessels left in running state from a previous daemon.
	// The singleton lock guarantees no other daemon is running, so all running
	// vessels are definitionally orphaned.
	reconcileStaleVessels(q, wt)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	scan := func(ctx context.Context) (scanner.ScanResult, error) {
		return runScan(ctx, cfg, q)
	}
	cmdRunner := newCmdRunner(cfg)
	drainRunner, cleanupDrainRunner := buildDrainRunner(cfg, q, wt, cmdRunner)
	defer cleanupDrainRunner()
	drainRunner.Reporter = buildReporter(cfg, cmdRunner)
	drainRunner.DrainBudget = drainInterval
	drain := func(ctx context.Context) (runner.DrainResult, error) {
		return drainRunner.Drain(ctx)
	}
	check := func(ctx context.Context) {
		drainRunner.CheckWaitingVessels(ctx)
		drainRunner.CheckHungVessels(ctx)
		// Auto-merge: best-effort request copilot review on merge-ready
		// harness PRs, then enable GitHub auto-merge once checks are green
		// and the PR is mergeable.
		if cfg.Daemon.AutoMerge {
			autoMergeXylemPRs(ctx, cfg.Daemon.AutoMergeRepo)
		}
	}

	return daemonLoop(ctx, q, drainRunner, scan, drain, check, upgrade, scanInterval, drainInterval, upgradeInterval)
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
		log.Printf("daemon: invalid upgrade_interval %q, using default %s", dc.UpgradeInterval, defaultUpgradeInterval)
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

// upgradeFunc runs a self-upgrade attempt. If it succeeds with a binary
// change, it calls exec() and never returns. If it returns, either the
// binary was unchanged or the upgrade failed; the daemon continues normally.
// May be nil to disable periodic upgrades.
type upgradeFunc func()

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
func daemonLoop(ctx context.Context, q *queue.Queue, tracker inFlightTracker, scan scanFunc, drain drainFunc, check checkFunc, upgrade upgradeFunc, scanInterval, drainInterval, upgradeInterval time.Duration) error {
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

	log.Printf("daemon started: scan_interval=%s drain_interval=%s upgrade_interval=%s",
		scanInterval, drainInterval, upgradeInterval)

	for {
		select {
		case <-ctx.Done():
			log.Println("daemon: received shutdown signal, waiting for in-flight drain")
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
				log.Println("daemon: in-flight drain and vessels finished")
			case <-time.After(drainShutdownTimeout):
				log.Println("daemon: drain shutdown timeout exceeded, exiting")
			}
			return nil
		default:
		}

		now := daemonNow()

		if now.Sub(lastScan) >= scanInterval {
			scanResult, err := scan(ctx)
			if err != nil {
				log.Printf("daemon: scan error: %v", err)
			} else {
				lastScan = now
				log.Printf("daemon: scan complete — added=%d skipped=%d", scanResult.Added, scanResult.Skipped)
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

		if upgradePending && drainIdle && inFlight == 0 {
			// Normal path: daemon is fully idle and upgrade is due.
			lastUpgrade = now
			upgrade()
		} else if upgradeOverdue && drainIdle && inFlight > 0 {
			// Overdue: log that we're pausing new dequeue to let in-flight
			// vessels drain naturally, creating an idle window for the
			// normal upgrade path on a subsequent tick. This does NOT kill
			// running vessels — we wait for them to complete on their own.
			log.Printf("daemon: auto-upgrade overdue by %s (in_flight=%d, pausing new drain dequeue until idle)",
				upgradeElapsed, inFlight)
		}

		// Drain dequeue is suppressed while an upgrade is overdue and
		// in-flight vessels still exist. This creates the idle window the
		// normal upgrade path needs, without exec()ing while subprocesses
		// are alive. When in_flight reaches zero, the next tick fires the
		// normal upgrade path above and dequeue resumes.
		drainPaused := upgradeOverdue && inFlight > 0
		if !drainPaused && now.Sub(lastDrain) >= drainInterval {
			if atomic.CompareAndSwapInt32(&draining, 0, 1) {
				lastDrain = now
				drainWg.Add(1)
				go func() {
					defer drainWg.Done()
					defer atomic.StoreInt32(&draining, 0)
					drainResult, err := drain(ctx)
					if err != nil {
						log.Printf("daemon: drain error: %v", err)
					} else {
						log.Printf("daemon: drain tick complete — launched=%d in_flight=%d",
							drainResult.Launched, trackerInFlightCount(tracker))
					}
				}()
			}
		}

		logTickSummary(q)

		select {
		case <-ctx.Done():
			log.Println("daemon: received shutdown signal, waiting for in-flight drain")
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
				log.Println("daemon: in-flight drain and vessels finished")
			case <-time.After(drainShutdownTimeout):
				log.Println("daemon: drain shutdown timeout exceeded, exiting")
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
	result, err := r.DrainAndWait(ctx)
	if err == nil {
		maybeAutoGenerateHarnessReview(cfg, result)
	}
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
	log.Printf("daemon: tick summary — pending=%d running=%d completed=%d failed=%d",
		counts[queue.StatePending], counts[queue.StateRunning],
		counts[queue.StateCompleted], counts[queue.StateFailed])
}

// reconcileStaleVessels transitions ALL running vessels to timed_out. The
// singleton daemon lock guarantees that no other daemon is executing vessels
// when this runs, so every running vessel is orphaned by definition. The
// timed_out state supports retry via `xylem retry`.
func reconcileStaleVessels(q *queue.Queue, wt *worktree.Manager) {
	vessels, err := q.List()
	if err != nil {
		log.Printf("daemon: reconcile: failed to list vessels: %v", err)
		return
	}

	var recovered int
	for _, v := range vessels {
		if v.State != queue.StateRunning {
			continue
		}
		log.Printf("daemon: reconcile: vessel %s orphaned in running state, marking timed_out", v.ID)
		if err := q.Update(v.ID, queue.StateTimedOut, "orphaned by daemon restart"); err != nil {
			log.Printf("daemon: reconcile: failed to update vessel %s: %v", v.ID, err)
			continue
		}
		recovered++

		// Best-effort worktree cleanup.
		if v.WorktreePath != "" && wt != nil {
			if err := wt.Remove(context.Background(), v.WorktreePath); err != nil {
				log.Printf("daemon: reconcile: failed to remove worktree for %s: %v", v.ID, err)
			}
		}
	}
	if recovered > 0 {
		log.Printf("daemon: reconcile: recovered %d orphaned vessel(s)", recovered)
	}
}

func daemonNow() time.Time {
	now, err := dtu.RuntimeNow()
	if err != nil {
		log.Printf("warn: daemon: resolve runtime clock: %v", err)
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

	log.Printf("daemon: acquired lock %s (PID %d)", pidPath, pid)

	return func() {
		if unlockErr := fl.Unlock(); unlockErr != nil {
			log.Printf("daemon: failed to release lock: %v", unlockErr)
		}
		os.Remove(pidPath) //nolint:errcheck
	}, nil
}
