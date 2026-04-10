package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/daemonhealth"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

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

func TestDoctorJSONOutput(t *testing.T) {
	report := &doctorReport{}
	report.add("test_check", "ok", "All good")
	report.add("test_warn", "warn", "Minor issue")

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}

	var decoded doctorReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(decoded.Checks))
	}
}
