package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/daemonhealth"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type doctorStubRunner struct {
	outputs map[string][]byte
}

func (r *doctorStubRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	if out, ok := r.outputs[key]; ok {
		return out, nil
	}
	return []byte{}, nil
}

func captureDoctorStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	defer r.Close()
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(data)
}

func TestDoctorDetectsZombieVessels(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	started := time.Now().Add(-2 * time.Hour)
	v := queue.Vessel{
		ID:        "zombie-1",
		Source:    "github-issue",
		Workflow:  "implement-feature",
		State:     queue.StatePending,
		CreatedAt: started,
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}
	if err := q.Update("zombie-1", queue.StateRunning, ""); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
	}

	report := &doctorReport{}
	checkZombieVessels(cfg, q, nil, report, false, false)

	found := false
	for _, c := range report.Checks {
		if c.Name == "zombie_vessels" && c.Status == "fail" {
			found = true
		}
	}
	if !found {
		t.Error("expected zombie_vessels fail check")
	}
}

func TestDoctorFixReapsZombies(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	started := time.Now().Add(-2 * time.Hour)
	v := queue.Vessel{
		ID:        "zombie-fix",
		Source:    "github-issue",
		Workflow:  "implement-feature",
		State:     queue.StatePending,
		CreatedAt: started,
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}
	if err := q.Update("zombie-fix", queue.StateRunning, ""); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Timeout:  "45m",
		StateDir: dir,
	}

	report := &doctorReport{}
	checkZombieVessels(cfg, q, nil, report, true, false)

	vessel, err := q.FindByID("zombie-fix")
	if err != nil {
		t.Fatal(err)
	}
	if vessel.State != queue.StateTimedOut {
		t.Errorf("expected timed_out, got %s", vessel.State)
	}

	found := false
	for _, c := range report.Checks {
		if c.Name == "zombie_vessels" && c.Fixed {
			found = true
		}
	}
	if !found {
		t.Error("expected zombie_vessels fixed check")
	}
}

func TestDoctorDetectsDeadDaemon(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	os.MkdirAll(stateDir, 0o755)

	snapshot := daemonhealth.Snapshot{
		PID:       99999,
		StartedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}
	if err := daemonhealth.Save(dir, snapshot); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		StateDir: dir,
	}

	report := &doctorReport{}
	checkDaemonLiveness(cfg, report)

	found := false
	for _, c := range report.Checks {
		if c.Name == "daemon" && c.Status == "fail" {
			found = true
		}
	}
	if !found {
		t.Error("expected daemon fail check for dead process")
	}
}

func TestDoctorQueueHealth(t *testing.T) {
	dir := t.TempDir()
	qPath := filepath.Join(dir, "queue.jsonl")
	q := queue.New(qPath)

	v := queue.Vessel{
		ID:        "done-1",
		Source:    "github-issue",
		Workflow:  "implement-feature",
		State:     queue.StatePending,
		CreatedAt: time.Now(),
	}
	if _, err := q.Enqueue(v); err != nil {
		t.Fatal(err)
	}
	if err := q.Update("done-1", queue.StateRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := q.Update("done-1", queue.StateCompleted, ""); err != nil {
		t.Fatal(err)
	}

	report := &doctorReport{}
	checkQueueHealth(q, report)

	if report.Summary.Fail > 0 {
		t.Error("expected no failures for healthy queue")
	}
}

func TestDoctorReportTracksSummaryAndFixedChecks(t *testing.T) {
	report := &doctorReport{}
	report.add("test_check", "ok", "All good")
	report.add("test_warn", "warn", "Minor issue")
	report.addFixed("test_fix", "Fixed issue")

	if got, want := len(report.Checks), 3; got != want {
		t.Fatalf("len(report.Checks) = %d, want %d", got, want)
	}
	if got, want := report.Summary.OK, 2; got != want {
		t.Fatalf("report.Summary.OK = %d, want %d", got, want)
	}
	if got, want := report.Summary.Warn, 1; got != want {
		t.Fatalf("report.Summary.Warn = %d, want %d", got, want)
	}
	if got, want := report.Summary.Fail, 0; got != want {
		t.Fatalf("report.Summary.Fail = %d, want %d", got, want)
	}
	if got := report.Checks[2]; !got.Fixed || got.Status != "ok" || got.Name != "test_fix" {
		t.Fatalf("fixed check = %#v, want fixed ok check named test_fix", got)
	}
}

func TestDoctorDepsForRootRebasesStateDir(t *testing.T) {
	root := t.TempDir()
	base := &appDeps{
		cfg: &config.Config{
			StateDir:      ".xylem",
			DefaultBranch: "main",
		},
	}

	scoped, err := doctorDepsForRoot(base, root)
	if err != nil {
		t.Fatalf("doctorDepsForRoot() error = %v", err)
	}

	if scoped == base {
		t.Fatal("doctorDepsForRoot() returned the original dependencies")
	}
	if got, want := scoped.cfg.StateDir, filepath.Join(root, ".xylem"); got != want {
		t.Fatalf("scoped cfg.StateDir = %q, want %q", got, want)
	}
	if got, want := scoped.wt.RepoRoot, root; got != want {
		t.Fatalf("scoped wt.RepoRoot = %q, want %q", got, want)
	}
	if got, want := scoped.wt.DefaultBranch, "main"; got != want {
		t.Fatalf("scoped wt.DefaultBranch = %q, want %q", got, want)
	}
	if got := base.cfg.StateDir; got != ".xylem" {
		t.Fatalf("base cfg.StateDir mutated to %q", got)
	}
}

func TestDoctorJSONOutputUsesRootStateDir(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".xylem")
	snapshot := daemonhealth.Snapshot{
		PID:       99999,
		StartedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}
	if err := daemonhealth.Save(stateDir, snapshot); err != nil {
		t.Fatal(err)
	}

	scoped, err := doctorDepsForRoot(&appDeps{
		cfg: &config.Config{
			StateDir:    ".xylem",
			Timeout:     "45m",
			Concurrency: 1,
			Daemon: config.DaemonConfig{
				StallMonitor: config.StallMonitorConfig{
					PhaseStallThreshold: "10m",
				},
				AutoUpgrade: true,
			},
		},
	}, root)
	if err != nil {
		t.Fatalf("doctorDepsForRoot() error = %v", err)
	}

	wt := worktree.New(root, &doctorStubRunner{
		outputs: map[string][]byte{
			"git worktree list --porcelain": []byte{},
		},
	})
	output := captureDoctorStdout(t, func() {
		if err := cmdDoctor(scoped.cfg, scoped.q, wt, false, true); err != nil {
			t.Fatalf("cmdDoctor() error = %v", err)
		}
	})

	var decoded doctorReport
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	found := false
	for _, check := range decoded.Checks {
		if check.Name == "daemon" && check.Status == "fail" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected rooted JSON output to include daemon failure, got %#v", decoded.Checks)
	}
}

func TestSmoke_S1_DoctorRootReadsScopedStateDir(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".xylem")
	snapshot := daemonhealth.Snapshot{
		PID:       99999,
		StartedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}
	require.NoError(t, daemonhealth.Save(stateDir, snapshot))

	scoped, err := doctorDepsForRoot(&appDeps{
		cfg: &config.Config{
			StateDir:    ".xylem",
			Timeout:     "45m",
			Concurrency: 1,
			Daemon: config.DaemonConfig{
				StallMonitor: config.StallMonitorConfig{
					PhaseStallThreshold: "10m",
				},
				AutoUpgrade: true,
			},
		},
	}, root)
	require.NoError(t, err)

	wt := worktree.New(root, &doctorStubRunner{
		outputs: map[string][]byte{
			"git worktree list --porcelain": {},
		},
	})
	out := captureDoctorStdout(t, func() {
		require.NoError(t, cmdDoctor(scoped.cfg, scoped.q, wt, false, false))
	})

	assert.Contains(t, out, "Daemon not running")
	assert.Contains(t, out, "pid=99999")
	assert.Contains(t, out, "Run with --fix to attempt automatic remediation")
}

func TestSmoke_S2_DoctorDefaultBehaviorWithoutRootUnchanged(t *testing.T) {
	defaultRoot := t.TempDir()
	otherRoot := t.TempDir()
	defaultStateDir := filepath.Join(defaultRoot, ".xylem")

	require.NoError(t, daemonhealth.Save(defaultStateDir, daemonhealth.Snapshot{
		PID:       11111,
		StartedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}))
	require.NoError(t, daemonhealth.Save(filepath.Join(otherRoot, ".xylem"), daemonhealth.Snapshot{
		PID:       22222,
		StartedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}))

	base := &appDeps{
		cfg: &config.Config{
			StateDir:    defaultStateDir,
			Timeout:     "45m",
			Concurrency: 1,
			Daemon: config.DaemonConfig{
				StallMonitor: config.StallMonitorConfig{
					PhaseStallThreshold: "10m",
				},
				AutoUpgrade: true,
			},
		},
		q: queue.New(filepath.Join(defaultStateDir, "queue.jsonl")),
		wt: worktree.New(defaultRoot, &doctorStubRunner{
			outputs: map[string][]byte{
				"git worktree list --porcelain": {},
			},
		}),
	}

	scoped, err := doctorDepsForRoot(base, "")
	require.NoError(t, err)
	require.Same(t, base, scoped)

	out := captureDoctorStdout(t, func() {
		require.NoError(t, cmdDoctor(scoped.cfg, scoped.q, scoped.wt, false, false))
	})

	assert.Contains(t, out, "pid=11111")
	assert.NotContains(t, out, "pid=22222")
}

func TestSmoke_S3_DoctorJSONModeHonorsRootFlag(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".xylem")
	snapshot := daemonhealth.Snapshot{
		PID:       33333,
		StartedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}
	require.NoError(t, daemonhealth.Save(stateDir, snapshot))

	scoped, err := doctorDepsForRoot(&appDeps{
		cfg: &config.Config{
			StateDir:    ".xylem",
			Timeout:     "45m",
			Concurrency: 1,
			Daemon: config.DaemonConfig{
				StallMonitor: config.StallMonitorConfig{
					PhaseStallThreshold: "10m",
				},
				AutoUpgrade: true,
			},
		},
	}, root)
	require.NoError(t, err)

	wt := worktree.New(root, &doctorStubRunner{
		outputs: map[string][]byte{
			"git worktree list --porcelain": {},
		},
	})
	out := captureDoctorStdout(t, func() {
		require.NoError(t, cmdDoctor(scoped.cfg, scoped.q, wt, false, true))
	})

	var decoded doctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &decoded), "output:\n%s", out)

	var daemonCheck *doctorCheck
	for i := range decoded.Checks {
		if decoded.Checks[i].Name == "daemon" {
			daemonCheck = &decoded.Checks[i]
			break
		}
	}
	require.NotNil(t, daemonCheck, "expected daemon check in %v", decoded.Checks)
	assert.Equal(t, "fail", daemonCheck.Status)
	assert.Contains(t, daemonCheck.Message, "pid=33333")
}
