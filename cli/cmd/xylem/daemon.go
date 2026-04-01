package main

import (
	"context"
	"log"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
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
	scanInterval, drainInterval := parseDaemonIntervals(cfg.Daemon)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	scan := func(ctx context.Context) (scanner.ScanResult, error) {
		return runScan(ctx, cfg, q)
	}
	drain := func(ctx context.Context) (runner.DrainResult, error) {
		return runDrain(ctx, cfg, q, wt)
	}

	return daemonLoop(ctx, q, scan, drain, scanInterval, drainInterval)
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

// daemonLoop is the core loop extracted for testability. It accepts an
// externally-controlled context so tests can cancel it without signals,
// and injectable scan/drain functions so tests can use stubs.
func daemonLoop(ctx context.Context, q *queue.Queue, scan scanFunc, drain drainFunc, scanInterval, drainInterval time.Duration) error {
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

		now := time.Now()

		if now.Sub(lastScan) >= scanInterval {
			scanResult, err := scan(ctx)
			if err != nil {
				log.Printf("daemon: scan error: %v", err)
			} else {
				lastScan = now
				log.Printf("daemon: scan complete — added=%d skipped=%d", scanResult.Added, scanResult.Skipped)
			}
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
		case <-time.After(tickInterval):
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
