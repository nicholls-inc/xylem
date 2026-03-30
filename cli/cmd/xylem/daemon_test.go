package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
)

func noopScan(_ context.Context) (scanner.ScanResult, error) {
	return scanner.ScanResult{}, nil
}

func noopDrain(_ context.Context) (runner.DrainResult, error) {
	return runner.DrainResult{}, nil
}

func TestDaemonShutdown(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := daemonLoop(ctx, q, noopScan, noopDrain, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("expected nil error on shutdown, got: %v", err)
	}
}

func TestParseDaemonIntervals(t *testing.T) {
	tests := []struct {
		name          string
		scanInterval  string
		drainInterval string
		expectedScan  time.Duration
		expectedDrain time.Duration
	}{
		{"defaults", "", "", 60 * time.Second, 30 * time.Second},
		{"custom scan", "120s", "", 120 * time.Second, 30 * time.Second},
		{"custom drain", "", "15s", 60 * time.Second, 15 * time.Second},
		{"both custom", "90s", "45s", 90 * time.Second, 45 * time.Second},
		{"invalid scan falls back to default", "not-a-duration", "", 60 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scan, drain := parseDaemonIntervals(config.DaemonConfig{
				ScanInterval:  tt.scanInterval,
				DrainInterval: tt.drainInterval,
			})
			if scan != tt.expectedScan {
				t.Errorf("scan interval: got %s, want %s", scan, tt.expectedScan)
			}
			if drain != tt.expectedDrain {
				t.Errorf("drain interval: got %s, want %s", drain, tt.expectedDrain)
			}
		})
	}
}

func TestDaemonNonBlockingDrain(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))

	now := time.Now().UTC()
	q.Enqueue(queue.Vessel{ID: "v1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck

	// slowDrain simulates a drain that takes 5 seconds. The context should
	// cancel well before that, and the daemon should wait for the in-flight
	// drain to finish (or timeout) rather than abandoning it.
	var drainStarted int32
	slowDrain := func(ctx context.Context) (runner.DrainResult, error) {
		atomic.StoreInt32(&drainStarted, 1)
		select {
		case <-ctx.Done():
			return runner.DrainResult{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return runner.DrainResult{Completed: 1}, nil
		}
	}

	// drainInterval=1ms ensures the drain fires immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := daemonLoop(ctx, q, noopScan, slowDrain, time.Hour, time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error on shutdown, got: %v", err)
	}

	// The drain goroutine should have been started.
	if atomic.LoadInt32(&drainStarted) == 0 {
		t.Error("drain goroutine was never started")
	}

	// The loop should return after the context cancels and the drain
	// goroutine observes the cancellation — well under 2 seconds.
	if elapsed > 2*time.Second {
		t.Errorf("daemonLoop took %s — drain shutdown wait may be broken", elapsed)
	}
}

func TestLogTickSummary(t *testing.T) {
	dir := t.TempDir()
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	now := time.Now().UTC()

	q.Enqueue(queue.Vessel{ID: "v1", Source: "manual", State: queue.StatePending, CreatedAt: now})   //nolint:errcheck
	q.Enqueue(queue.Vessel{ID: "v2", Source: "manual", State: queue.StateCompleted, CreatedAt: now}) //nolint:errcheck

	// logTickSummary should not panic on any queue state
	logTickSummary(q)
}
