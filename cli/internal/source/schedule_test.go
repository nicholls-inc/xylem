package source

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

func TestScheduleFirstRunPersistsAndSuppressesUntilNextDue(t *testing.T) {
	stateDir := t.TempDir()
	q := queue.New(filepath.Join(stateDir, "queue.jsonl"))
	src := &Schedule{
		ConfigName: "lessons",
		Cadence:    "@daily",
		Workflow:   "lessons",
		StateDir:   stateDir,
		Queue:      q,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("len(Scan()) = %d, want 1", len(vessels))
	}
	if vessels[0].Source != "schedule" {
		t.Fatalf("vessels[0].Source = %q, want schedule", vessels[0].Source)
	}

	if err := src.OnEnqueue(context.Background(), vessels[0]); err != nil {
		t.Fatalf("OnEnqueue() error = %v", err)
	}
	vessels, err = src.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() after enqueue error = %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("len(Scan() after enqueue) = %d, want 0", len(vessels))
	}
}

func TestScheduleStatePersistsAcrossRestart(t *testing.T) {
	stateDir := t.TempDir()
	q := queue.New(filepath.Join(stateDir, "queue.jsonl"))
	src := &Schedule{
		ConfigName: "weekly-lessons",
		Cadence:    "@weekly",
		Workflow:   "lessons",
		StateDir:   stateDir,
		Queue:      q,
	}

	vessels, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if err := src.OnEnqueue(context.Background(), vessels[0]); err != nil {
		t.Fatalf("OnEnqueue() error = %v", err)
	}

	restarted := &Schedule{
		ConfigName: "weekly-lessons",
		Cadence:    "@weekly",
		Workflow:   "lessons",
		StateDir:   stateDir,
		Queue:      q,
	}
	vessels, err = restarted.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() after restart error = %v", err)
	}
	if len(vessels) != 0 {
		t.Fatalf("len(Scan() after restart) = %d, want 0", len(vessels))
	}
}

func TestValidateCadenceRejectsMalformedCron(t *testing.T) {
	if err := ValidateCadence("not-a-cadence"); err == nil {
		t.Fatal("ValidateCadence() error = nil, want error")
	}
}
