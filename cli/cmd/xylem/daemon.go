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
	if cfg.Daemon.AutoUpgrade {
		execPath, execErr := os.Executable()
		if execErr != nil {
			slog.Warn("daemon auto-upgrade skipped: resolve executable path", "error", execErr)
		} else {
			repoDir := filepath.Dir(filepath.Dir(execPath))
			selfUpgrade(repoDir, execPath)
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
	drain := func(ctx context.Context) (runner.DrainResult, error) {
		return runDrain(ctx, cfg, q, wt)
	}
	check := func(ctx context.Context) {
		cmdRunner := &realCmdRunner{}
		r := runner.New(cfg, q, wt, cmdRunner)
		r.Sources = buildSourceMap(cfg, q, cmdRunner)
		r.CheckWaitingVessels(ctx)
		r.CheckHungVessels(ctx)
		// Auto-merge: request copilot review on unreviewed xylem PRs,
		// and merge PRs that are approved + CI-green + mergeable.
		if cfg.Daemon.AutoMerge {
			autoMergeXylemPRs(ctx, cfg.Daemon.AutoMergeRepo)
		}
	}

	return daemonLoop(ctx, q, scan, drain, check, scanInterval, drainInterval)
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

// checkFunc runs periodic vessel health checks (waiting vessel label checks,
// hung vessel timeouts). May be nil if no checks are needed.
type checkFunc func(ctx context.Context)

// daemonLoop is the core loop extracted for testability. It accepts an
// externally-controlled context so tests can cancel it without signals,
// and injectable scan/drain/check functions so tests can use stubs.
func daemonLoop(ctx context.Context, q *queue.Queue, scan scanFunc, drain drainFunc, check checkFunc, scanInterval, drainInterval time.Duration) error {
	tickInterval := scanInterval
	if drainInterval < tickInterval {
		tickInterval = drainInterval
	}

	var lastScan, lastDrain time.Time
	var draining int32 // 0=idle, 1=running
	var drainWg sync.WaitGroup

	slog.Info("daemon started", "scan_interval", scanInterval, "drain_interval", drainInterval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("daemon received shutdown signal", "waiting_for", "in-flight drain")
			waitDone := make(chan struct{})
			go func() { drainWg.Wait(); close(waitDone) }()
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

		if now.Sub(lastDrain) >= drainInterval {
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
						"completed", drainResult.Completed,
						"failed", drainResult.Failed,
						"skipped", drainResult.Skipped)
				}()
			}
		}

		logTickSummary(q)

		select {
		case <-ctx.Done():
			slog.Info("daemon received shutdown signal", "waiting_for", "in-flight drain")
			waitDone := make(chan struct{})
			go func() { drainWg.Wait(); close(waitDone) }()
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

func runDrain(ctx context.Context, cfg *config.Config, q *queue.Queue, wt *worktree.Manager) (runner.DrainResult, error) {
	cmdRunner := newCmdRunner(cfg)
	r, cleanup := buildDrainRunner(cfg, q, wt, cmdRunner)
	defer cleanup()
	result, err := r.Drain(ctx)
	if err == nil {
		maybeAutoGenerateHarnessReview(cfg, result)
	}
	return result, err
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

// reconcileStaleVessels transitions ALL running vessels to timed_out. The
// singleton daemon lock guarantees that no other daemon is executing vessels
// when this runs, so every running vessel is orphaned by definition. The
// timed_out state supports retry via `xylem retry`.
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
		slog.Warn("daemon reconcile found orphaned running vessel", "vessel", v.ID, "target_state", queue.StateTimedOut)
		if err := q.Update(v.ID, queue.StateTimedOut, "orphaned by daemon restart"); err != nil {
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
		slog.Info("daemon reconcile recovered orphaned vessels", "recovered", recovered)
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
