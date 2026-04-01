package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

	err := daemonLoop(ctx, q, noopScan, noopDrain, nil, time.Hour, time.Hour)
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
	err := daemonLoop(ctx, q, noopScan, slowDrain, nil, time.Hour, time.Millisecond)
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

func TestReconcileStaleVessels(t *testing.T) {
	t.Run("stale running vessel transitions to failed", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()
		staleStart := now.Add(-3 * time.Hour)

		// Enqueue then dequeue to move to running state (sets StartedAt).
		q.Enqueue(queue.Vessel{ID: "stale-1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		if v == nil {
			t.Fatal("expected vessel from dequeue")
		}
		// Backdate StartedAt to make it stale.
		v.StartedAt = &staleStart
		q.UpdateVessel(*v) //nolint:errcheck

		reconcileStaleVessels(q, 2*time.Hour)

		updated, err := q.FindByID("stale-1")
		if err != nil {
			t.Fatalf("failed to find vessel: %v", err)
		}
		if updated.State != queue.StateFailed {
			t.Errorf("expected state %s, got %s", queue.StateFailed, updated.State)
		}
		if updated.Error != "orphaned by daemon restart" {
			t.Errorf("expected error 'orphaned by daemon restart', got %q", updated.Error)
		}
	})

	t.Run("recent running vessel is not reconciled", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "recent-1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		if v == nil {
			t.Fatal("expected vessel from dequeue")
		}
		// StartedAt is set to now by Dequeue — well within the timeout.

		reconcileStaleVessels(q, 2*time.Hour)

		updated, err := q.FindByID("recent-1")
		if err != nil {
			t.Fatalf("failed to find vessel: %v", err)
		}
		if updated.State != queue.StateRunning {
			t.Errorf("expected state %s, got %s", queue.StateRunning, updated.State)
		}
	})

	t.Run("pending and completed vessels are not affected", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "pending-1", Source: "manual", State: queue.StatePending, CreatedAt: now})    //nolint:errcheck
		q.Enqueue(queue.Vessel{ID: "complete-1", Source: "manual", State: queue.StateCompleted, CreatedAt: now}) //nolint:errcheck

		reconcileStaleVessels(q, 1*time.Millisecond)

		pending, _ := q.FindByID("pending-1")
		if pending.State != queue.StatePending {
			t.Errorf("expected pending-1 to remain pending, got %s", pending.State)
		}
		complete, _ := q.FindByID("complete-1")
		if complete.State != queue.StateCompleted {
			t.Errorf("expected complete-1 to remain completed, got %s", complete.State)
		}
	})

	t.Run("zero timeout uses default", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()
		// Started 1 hour ago — less than the 2-hour default.
		recentStart := now.Add(-1 * time.Hour)

		q.Enqueue(queue.Vessel{ID: "v1", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		v.StartedAt = &recentStart
		q.UpdateVessel(*v) //nolint:errcheck

		reconcileStaleVessels(q, 0) // should use defaultStaleTimeout (2h)

		updated, _ := q.FindByID("v1")
		if updated.State != queue.StateRunning {
			t.Errorf("expected vessel to remain running with default timeout, got %s", updated.State)
		}
	})

	t.Run("running vessel with nil StartedAt is reconciled", func(t *testing.T) {
		dir := t.TempDir()
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()

		q.Enqueue(queue.Vessel{ID: "nil-start", Source: "manual", State: queue.StatePending, CreatedAt: now}) //nolint:errcheck
		v, _ := q.Dequeue()
		// Clear StartedAt to simulate a legacy/corrupt entry.
		v.StartedAt = nil
		q.UpdateVessel(*v) //nolint:errcheck

		reconcileStaleVessels(q, 2*time.Hour)

		updated, _ := q.FindByID("nil-start")
		if updated.State != queue.StateFailed {
			t.Errorf("expected state failed for nil StartedAt, got %s", updated.State)
		}
	})
}

func TestAcquireDaemonLock(t *testing.T) {
	t.Run("acquires lock successfully", func(t *testing.T) {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "daemon.pid")

		unlock, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		defer unlock()

		// PID file should exist and contain our PID.
		data, err := os.ReadFile(pidPath)
		if err != nil {
			t.Fatalf("failed to read PID file: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("PID file is empty")
		}
	})

	t.Run("second lock fails with already running error", func(t *testing.T) {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "daemon.pid")

		unlock1, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("first lock failed: %v", err)
		}
		defer unlock1()

		_, err = acquireDaemonLock(pidPath)
		if err == nil {
			t.Fatal("expected error from second lock, got nil")
		}
		if !strings.Contains(err.Error(), "daemon already running") {
			t.Errorf("expected 'daemon already running' error, got: %v", err)
		}
	})

	t.Run("lock is released on unlock", func(t *testing.T) {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "daemon.pid")

		unlock1, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("first lock failed: %v", err)
		}
		unlock1()

		// Should be able to acquire again after unlock.
		unlock2, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("second lock after unlock failed: %v", err)
		}
		defer unlock2()
	})

	t.Run("creates parent directory if needed", func(t *testing.T) {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "nested", "subdir", "daemon.pid")

		unlock, err := acquireDaemonLock(pidPath)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		defer unlock()
	})
}
