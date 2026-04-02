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

// defaultStaleTimeout is the fallback timeout for considering a running vessel
// stale when no explicit timeout is configured.
const defaultStaleTimeout = 2 * time.Hour

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
	scanInterval, drainInterval := parseDaemonIntervals(cfg.Daemon)

	// P0-3: Acquire singleton lock to prevent multiple daemons.
	pidPath := filepath.Join(cfg.StateDir, "daemon.pid")
	unlock, err := acquireDaemonLock(pidPath)
	if err != nil {
		return err
	}
	defer unlock()

	// P0-2: Reconcile any vessels left in running state from a previous daemon.
	var timeout time.Duration
	if cfg.Timeout != "" {
		timeout, _ = time.ParseDuration(cfg.Timeout)
	}
	reconcileStaleVessels(q, timeout)

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

	log.Printf("daemon started: scan_interval=%s drain_interval=%s", scanInterval, drainInterval)

	for {
		select {
		case <-ctx.Done():
			log.Println("daemon: received shutdown signal, waiting for in-flight drain")
			waitDone := make(chan struct{})
			go func() { drainWg.Wait(); close(waitDone) }()
			select {
			case <-waitDone:
				log.Println("daemon: in-flight drain finished")
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

		if now.Sub(lastDrain) >= drainInterval {
			if atomic.CompareAndSwapInt32(&draining, 0, 1) {
				lastDrain = now
				drainWg.Add(1)
				go func() {
					defer drainWg.Done()
					defer atomic.StoreInt32(&draining, 0)
					drainResult, err := drain(ctx)
					if err != nil {
						log.Printf("daemon: drain error: %v", err)
						return
					}
					log.Printf("daemon: drain complete — completed=%d failed=%d skipped=%d",
						drainResult.Completed, drainResult.Failed, drainResult.Skipped)
				}()
			}
		}

		logTickSummary(q)

		select {
		case <-ctx.Done():
			log.Println("daemon: received shutdown signal, waiting for in-flight drain")
			waitDone := make(chan struct{})
			go func() { drainWg.Wait(); close(waitDone) }()
			select {
			case <-waitDone:
				log.Println("daemon: in-flight drain finished")
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

func runDrain(ctx context.Context, cfg *config.Config, q *queue.Queue, wt *worktree.Manager) (runner.DrainResult, error) {
	cmdRunner := newCmdRunner(cfg)
	r := runner.New(cfg, q, wt, cmdRunner)
	r.Sources = buildSourceMap(cfg, q, cmdRunner)
	return r.Drain(ctx)
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

// reconcileStaleVessels transitions any running vessels that have exceeded the
// given timeout to failed state. This cleans up orphaned vessels left behind
// when a previous daemon was killed. If timeout is zero, defaultStaleTimeout
// is used.
func reconcileStaleVessels(q *queue.Queue, timeout time.Duration) {
	if timeout == 0 {
		timeout = defaultStaleTimeout
	}

	vessels, err := q.List()
	if err != nil {
		log.Printf("daemon: reconcile: failed to list vessels: %v", err)
		return
	}

	now := daemonNow()
	for _, v := range vessels {
		if v.State != queue.StateRunning {
			continue
		}
		if v.StartedAt == nil {
			// No start time recorded — treat as stale.
			log.Printf("daemon: reconcile: vessel %s in running state with no start time, marking failed", v.ID)
			if err := q.Update(v.ID, queue.StateFailed, "orphaned by daemon restart"); err != nil {
				log.Printf("daemon: reconcile: failed to update vessel %s: %v", v.ID, err)
			}
			continue
		}
		if v.StartedAt.Add(timeout).Before(now) {
			log.Printf("daemon: reconcile: vessel %s started at %s exceeded timeout %s, marking failed", v.ID, v.StartedAt.Format(time.RFC3339), timeout)
			if err := q.Update(v.ID, queue.StateFailed, "orphaned by daemon restart"); err != nil {
				log.Printf("daemon: reconcile: failed to update vessel %s: %v", v.ID, err)
			}
		}
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
