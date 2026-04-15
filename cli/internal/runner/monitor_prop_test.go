package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

func TestProp_LatestPhaseActivityReturnsNewestOutput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "monitor-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)
		cfg := makeTestConfig(dir, 1)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		r := New(cfg, queue.New(filepath.Join(dir, "queue.jsonl")), &mockWorktree{}, &mockCmdRunner{})

		vesselID := rapid.StringMatching(`[a-z0-9-]{1,16}`).Draw(t, "vesselID")
		phaseCount := rapid.IntRange(1, 8).Draw(t, "phaseCount")
		phasesDir := config.RuntimePath(cfg.StateDir, "phases", vesselID)
		if err := os.MkdirAll(phasesDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", phasesDir, err)
		}

		base := time.Unix(1_700_000_000, 0).UTC()
		var (
			wantPhase string
			wantTime  time.Time
		)
		for i := range phaseCount {
			phaseName := fmt.Sprintf("phase_%d", i)
			offsetSeconds := rapid.IntRange(0, 10_000).Draw(t, fmt.Sprintf("offset-%d", i))
			modTime := base.Add(time.Duration(offsetSeconds+i*10_001) * time.Second)
			outputPath := filepath.Join(phasesDir, phaseName+".output")
			if err := os.WriteFile(outputPath, []byte(phaseName), 0o644); err != nil {
				t.Fatalf("WriteFile(%q): %v", outputPath, err)
			}
			if err := os.Chtimes(outputPath, modTime, modTime); err != nil {
				t.Fatalf("Chtimes(%q): %v", outputPath, err)
			}
			if wantPhase == "" || modTime.After(wantTime) {
				wantPhase = phaseName
				wantTime = modTime
			}
		}

		ignoredPath := filepath.Join(phasesDir, "ignored.txt")
		ignoredTime := wantTime.Add(24 * time.Hour)
		if err := os.WriteFile(ignoredPath, []byte("ignore me"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", ignoredPath, err)
		}
		if err := os.Chtimes(ignoredPath, ignoredTime, ignoredTime); err != nil {
			t.Fatalf("Chtimes(%q): %v", ignoredPath, err)
		}

		gotPhase, gotTime, err := r.latestPhaseActivity(vesselID)
		if err != nil {
			t.Fatalf("latestPhaseActivity(%q) error = %v", vesselID, err)
		}
		if gotPhase != wantPhase {
			t.Fatalf("latestPhaseActivity(%q) phase = %q, want %q", vesselID, gotPhase, wantPhase)
		}
		if !gotTime.Equal(wantTime) {
			t.Fatalf("latestPhaseActivity(%q) time = %s, want %s", vesselID, gotTime, wantTime)
		}
	})
}

// TestProp_NeverRegisteredVesselTimeoutIffPastThreshold verifies that
// CheckStalledVessels times out a never-registered running vessel if and only if
// OrphanCheckEnabled is true AND the vessel has been running longer than the
// configured stall threshold.
func TestProp_NeverRegisteredVesselTimeoutIffPastThreshold(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		thresholdMin := rapid.IntRange(1, 60).Draw(rt, "threshold_min")
		elapsedMin := rapid.IntRange(0, 120).Draw(rt, "elapsed_min")
		orphanEnabled := rapid.Bool().Draw(rt, "orphan_enabled")

		dir, err := os.MkdirTemp("", "prop-never-reg-*")
		if err != nil {
			rt.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)

		cfg := makeTestConfig(dir, 1)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		cfg.Daemon.StallMonitor.PhaseStallThreshold = fmt.Sprintf("%dm", thresholdMin)
		cfg.Daemon.StallMonitor.OrphanCheckEnabled = orphanEnabled

		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		now := time.Now().UTC()
		_, _ = q.Enqueue(queue.Vessel{
			ID:        "prop-1",
			Source:    "manual",
			State:     queue.StatePending,
			CreatedAt: now,
		})
		vessel, _ := q.Dequeue()
		require.NotNil(rt, vessel, "Dequeue returned nil")

		startedAt := now.Add(-time.Duration(elapsedMin) * time.Minute)
		vessel.StartedAt = &startedAt
		_ = q.UpdateVessel(*vessel)

		r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
		findings := r.CheckStalledVessels(context.Background())

		// When elapsedMin == thresholdMin, runtimeSince returns
		// thresholdMin + epsilon (test setup latency), which is always
		// strictly greater than the threshold. So ">=" is the correct
		// predicate here.
		shouldTimeout := orphanEnabled && elapsedMin >= thresholdMin
		if shouldTimeout {
			if len(findings) != 1 {
				rt.Fatalf("expected 1 finding, got %d (orphanEnabled=%v elapsed=%dm threshold=%dm)",
					len(findings), orphanEnabled, elapsedMin, thresholdMin)
			}
			if findings[0].Code != "orphaned_subprocess" {
				rt.Fatalf("expected code=orphaned_subprocess, got %q", findings[0].Code)
			}
		} else {
			if len(findings) != 0 {
				rt.Fatalf("expected 0 findings, got %d (orphanEnabled=%v elapsed=%dm threshold=%dm)",
					len(findings), orphanEnabled, elapsedMin, thresholdMin)
			}
		}
	})
}
