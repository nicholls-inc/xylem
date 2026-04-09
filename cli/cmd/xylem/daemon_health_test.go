package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type backlogCmdRunnerStub struct {
	responses map[string][]byte
}

func (b backlogCmdRunnerStub) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), "\x00")
	if out, ok := b.responses[key]; ok {
		return out, nil
	}
	return []byte("[]"), nil
}

func TestSmoke_S2_IdleWithBacklogWarnsWhenQueueIdle(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		StateDir: filepath.Join(dir, ".xylem"),
		Daemon: config.DaemonConfig{
			StallMonitor: config.StallMonitorConfig{
				ScannerIdleThreshold: "5m",
			},
		},
		Sources: map[string]config.SourceConfig{
			"issues": {
				Type: "github",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"},
				},
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.StateDir, 0o755))
	q := queue.New(filepath.Join(cfg.StateDir, "queue.jsonl"))
	runner := backlogCmdRunnerStub{
		responses: map[string][]byte{
			strings.Join([]string{"gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number,labels", "--limit", "100", "--label", "bug"}, "\x00"): []byte(`[
				{"number":1,"labels":[{"name":"bug"}]},
				{"number":2,"labels":[{"name":"bug"}]},
				{"number":3,"labels":[{"name":"bug"}]}
			]`),
		},
	}
	monitor := newDaemonBacklogMonitor(cfg, runner)
	logBuf := withBufferedDefaultLogger(t)

	now := time.Now().UTC()
	require.Empty(t, monitor.ObserveScan(context.Background(), now, scanner.ScanResult{}, nil, q))

	alerts := monitor.ObserveScan(context.Background(), now.Add(6*time.Minute), scanner.ScanResult{}, nil, q)
	require.Len(t, alerts, 1)
	assert.Equal(t, "idle_with_backlog", alerts[0].Code)
	assert.Contains(t, alerts[0].Message, "3 backlog items")
	assert.Contains(t, logBuf.String(), "daemon idle with backlog")
}

func TestSmoke_S5_StatusShowsDaemonHealthAlerts(t *testing.T) {
	dir := t.TempDir()
	cfg := testStatusConfig(dir)
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	snapshot := daemonStatusSnapshot{
		PID:         os.Getpid(),
		StartedAt:   time.Now().UTC().Add(-2 * time.Hour),
		HeartbeatAt: time.Now().UTC(),
		Build:       "abcdef123456",
		AutoUpgrade: true,
		Alerts: []daemonStatusAlert{
			{Code: "idle_with_backlog", Severity: "warning", Message: "Daemon idle with 3 backlog items on GitHub"},
			{Code: "phase_stalled", Severity: "critical", Message: "Vessel issue-158 phase-stalled (10m12s no output on analyze)"},
		},
	}
	lastUpgrade := time.Now().UTC().Add(-30 * time.Minute)
	snapshot.LastUpgradeAt = &lastUpgrade
	require.NoError(t, saveDaemonStatusSnapshot(daemonHealthPath(cfg), snapshot))

	var err error
	out := captureStdout(func() { err = cmdStatus(cfg, q, false, "") })
	require.NoError(t, err)
	require.Contains(t, out, "No vessels in queue.")
	for _, want := range []string{
		"Daemon health:",
		"OK Daemon alive",
		"OK Auto-upgrade current",
		"WARN Daemon idle with 3 backlog items on GitHub",
		"FAIL Vessel issue-158 phase-stalled",
	} {
		assert.Contains(t, out, want)
	}
}

func TestNewDaemonHealthRecorderOnlyMarksActiveAutoUpgradeWhenWired(t *testing.T) {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			AutoUpgrade: true,
		},
	}

	recorder := newDaemonHealthRecorder(cfg, time.Now().UTC(), false)
	if recorder.snapshot.AutoUpgrade {
		t.Fatal("snapshot.AutoUpgrade = true, want false when upgrade path is unavailable")
	}

	recorder = newDaemonHealthRecorder(cfg, time.Now().UTC(), true)
	if !recorder.snapshot.AutoUpgrade {
		t.Fatal("snapshot.AutoUpgrade = false, want true when upgrade path is active")
	}
}
