package dtu_test

import (
	"context"
	"testing"
	"time"

	dtu "github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/nicholls-inc/xylem/cli/internal/releasecadence"
)

func TestFixtureReleaseCadenceMatureAddsReadyLabel(t *testing.T) {
	env := newScenarioEnv(t, "release-cadence-mature.yaml")
	now := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)

	result, err := releasecadence.ApplyReadyLabel(context.Background(), env.cmdRunner, releasecadence.Options{
		Repo: "owner/repo",
		Now:  now,
	})
	if err != nil {
		t.Fatalf("ApplyReadyLabel() error = %v", err)
	}
	if result == nil || result.Action != releasecadence.ActionLabeled {
		t.Fatalf("result = %#v, want labeled result", result)
	}
	if result.CommitCount != 6 {
		t.Fatalf("CommitCount = %d, want 6", result.CommitCount)
	}
	assertStringSliceEqual(t, readPRLabels(t, env.store, "owner/repo", 3), []string{"ready-to-merge"})

	events := readEvents(t, env.store)
	editCalls := filterShimEvents(events, dtu.EventKindShimInvocation, "gh", []string{"pr", "edit", "3", "--repo", "owner/repo", "--add-label", "ready-to-merge"})
	if len(editCalls) != 1 {
		t.Fatalf("len(pr edit calls) = %d, want 1", len(editCalls))
	}
}

func TestFixtureReleaseCadenceBelowThresholdNoops(t *testing.T) {
	env := newScenarioEnv(t, "release-cadence-below-threshold.yaml")
	now := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)

	result, err := releasecadence.ApplyReadyLabel(context.Background(), env.cmdRunner, releasecadence.Options{
		Repo: "owner/repo",
		Now:  now,
	})
	if err != nil {
		t.Fatalf("ApplyReadyLabel() error = %v", err)
	}
	if result == nil || result.Action != releasecadence.ActionNoop {
		t.Fatalf("result = %#v, want noop result", result)
	}
	if result.CommitCount != 3 {
		t.Fatalf("CommitCount = %d, want 3", result.CommitCount)
	}
	assertStringSliceEqual(t, readPRLabels(t, env.store, "owner/repo", 3), nil)

	events := readEvents(t, env.store)
	editCalls := filterShimEvents(events, dtu.EventKindShimInvocation, "gh", []string{"pr", "edit", "3", "--repo", "owner/repo", "--add-label", "ready-to-merge"})
	if len(editCalls) != 0 {
		t.Fatalf("len(pr edit calls) = %d, want 0", len(editCalls))
	}
}

func TestFixtureReleaseCadenceOptOutNoops(t *testing.T) {
	env := newScenarioEnv(t, "release-cadence-opt-out.yaml")
	now := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)

	result, err := releasecadence.ApplyReadyLabel(context.Background(), env.cmdRunner, releasecadence.Options{
		Repo: "owner/repo",
		Now:  now,
	})
	if err != nil {
		t.Fatalf("ApplyReadyLabel() error = %v", err)
	}
	if result == nil || result.Action != releasecadence.ActionNoop {
		t.Fatalf("result = %#v, want noop result", result)
	}
	assertStringSliceEqual(t, readPRLabels(t, env.store, "owner/repo", 3), []string{"no-auto-admin-merge"})

	events := readEvents(t, env.store)
	editCalls := filterShimEvents(events, dtu.EventKindShimInvocation, "gh", []string{"pr", "edit", "3", "--repo", "owner/repo", "--add-label", "ready-to-merge"})
	if len(editCalls) != 0 {
		t.Fatalf("len(pr edit calls) = %d, want 0", len(editCalls))
	}
}
