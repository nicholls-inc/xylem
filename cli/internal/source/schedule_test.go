package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduleName(t *testing.T) {
	if got := (&Schedule{}).Name(); got != "schedule" {
		t.Fatalf("Name() = %q, want schedule", got)
	}
}

func TestScheduleScanFirstRun(t *testing.T) {
	now := time.Date(2026, time.April, 9, 6, 45, 12, 0, time.UTC)
	s := &Schedule{
		ConfigName: "doc-gardener",
		Cadence:    "1h",
		Workflow:   "doc-garden",
		StateDir:   t.TempDir(),
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue.jsonl")),
		Now: func() time.Time {
			return now
		},
	}

	vessels, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}

	v := vessels[0]
	if v.Source != "schedule" {
		t.Errorf("Source = %q, want schedule", v.Source)
	}
	if v.Workflow != "doc-garden" {
		t.Errorf("Workflow = %q, want doc-garden", v.Workflow)
	}
	if got, want := v.ID, "schedule-doc-gardener-20260409t064512z"; got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}
	if got, want := v.Ref, "schedule://doc-gardener/2026-04-09T06:45:12Z"; got != want {
		t.Errorf("Ref = %q, want %q", got, want)
	}
	if got := v.Meta["schedule.cadence"]; got != "1h" {
		t.Errorf("schedule.cadence = %q, want 1h", got)
	}
	if got := v.Meta["schedule.source_name"]; got != "doc-gardener" {
		t.Errorf("schedule.source_name = %q, want doc-gardener", got)
	}
	if got := v.Meta["schedule.fired_at"]; got != now.Format(time.RFC3339) {
		t.Errorf("schedule.fired_at = %q, want %q", got, now.Format(time.RFC3339))
	}
}

func TestScheduleScanSuppressesUntilCadenceElapses(t *testing.T) {
	stateDir := t.TempDir()
	q := queue.New(filepath.Join(t.TempDir(), "queue.jsonl"))
	now := time.Date(2026, time.April, 9, 6, 0, 0, 0, time.UTC)
	s := &Schedule{
		ConfigName: "doctor",
		Cadence:    "1h",
		Workflow:   "doctor",
		StateDir:   stateDir,
		Queue:      q,
		Now: func() time.Time {
			return now
		},
	}

	first, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("first Scan() error = %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected first scan to emit 1 vessel, got %d", len(first))
	}
	if err := s.OnEnqueue(context.Background(), first[0]); err != nil {
		t.Fatalf("OnEnqueue() error = %v", err)
	}

	now = now.Add(30 * time.Minute)
	suppressed, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("suppressed Scan() error = %v", err)
	}
	if len(suppressed) != 0 {
		t.Fatalf("expected no vessel before cadence elapsed, got %d", len(suppressed))
	}

	now = now.Add(30 * time.Minute)
	second, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("expected one vessel after cadence elapsed, got %d", len(second))
	}
	if got, want := second[0].Meta["schedule.fired_at"], "2026-04-09T07:00:00Z"; got != want {
		t.Errorf("schedule.fired_at = %q, want %q", got, want)
	}
}

func TestScheduleScanPersistsAcrossRestart(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 9, 6, 0, 0, 0, time.UTC)
	first := &Schedule{
		ConfigName: "self-gap-analysis",
		Cadence:    "@daily",
		Workflow:   "gap-analysis",
		StateDir:   stateDir,
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue-one.jsonl")),
		Now: func() time.Time {
			return now
		},
	}

	vessels, err := first.Scan(context.Background())
	if err != nil {
		t.Fatalf("first Scan() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected first scan to emit 1 vessel, got %d", len(vessels))
	}
	if err := first.OnEnqueue(context.Background(), vessels[0]); err != nil {
		t.Fatalf("OnEnqueue() error = %v", err)
	}

	now = now.Add(12 * time.Hour)
	second := &Schedule{
		ConfigName: "self-gap-analysis",
		Cadence:    "@daily",
		Workflow:   "gap-analysis",
		StateDir:   stateDir,
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue-two.jsonl")),
		Now: func() time.Time {
			return now
		},
	}

	suppressed, err := second.Scan(context.Background())
	if err != nil {
		t.Fatalf("restart Scan() error = %v", err)
	}
	if len(suppressed) != 0 {
		t.Fatalf("expected restart scan before next tick to emit 0 vessels, got %d", len(suppressed))
	}

	now = time.Date(2026, time.April, 10, 6, 0, 0, 0, time.UTC)
	due, err := second.Scan(context.Background())
	if err != nil {
		t.Fatalf("next-day Scan() error = %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected restart scan after next tick to emit 1 vessel, got %d", len(due))
	}
	if got, want := due[0].Meta["schedule.fired_at"], "2026-04-10T00:00:00Z"; got != want {
		t.Errorf("schedule.fired_at = %q, want %q", got, want)
	}
}

func TestScheduleScanNamespacesStateByConfigSource(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 9, 6, 0, 0, 0, time.UTC)

	doctor := &Schedule{
		ConfigName: "doctor",
		Cadence:    "1h",
		Workflow:   "doctor",
		StateDir:   stateDir,
		Queue:      queue.New(filepath.Join(t.TempDir(), "doctor-queue.jsonl")),
		Now: func() time.Time {
			return now
		},
	}

	first, err := doctor.Scan(context.Background())
	if err != nil {
		t.Fatalf("doctor Scan() error = %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("doctor first scan emitted %d vessels, want 1", len(first))
	}
	if err := doctor.OnEnqueue(context.Background(), first[0]); err != nil {
		t.Fatalf("doctor OnEnqueue() error = %v", err)
	}

	docGardener := &Schedule{
		ConfigName: "doc-gardener",
		Cadence:    "1h",
		Workflow:   "doc-garden",
		StateDir:   stateDir,
		Queue:      queue.New(filepath.Join(t.TempDir(), "doc-gardener-queue.jsonl")),
		Now: func() time.Time {
			return now
		},
	}

	second, err := docGardener.Scan(context.Background())
	if err != nil {
		t.Fatalf("doc-gardener Scan() error = %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("doc-gardener first scan emitted %d vessels, want 1", len(second))
	}
	if got := second[0].Meta["schedule.source_name"]; got != "doc-gardener" {
		t.Fatalf("schedule.source_name = %q, want doc-gardener", got)
	}
	if got := second[0].Ref; got != "schedule://doc-gardener/2026-04-09T06:00:00Z" {
		t.Fatalf("Ref = %q, want doc-gardener schedule ref", got)
	}

	now = now.Add(30 * time.Minute)
	suppressed, err := doctor.Scan(context.Background())
	if err != nil {
		t.Fatalf("doctor second Scan() error = %v", err)
	}
	if len(suppressed) != 0 {
		t.Fatalf("doctor second scan emitted %d vessels before cadence elapsed, want 0", len(suppressed))
	}
}

func TestScheduleScanSuppressesDuplicateWhenActiveVesselExistsWithoutPersistedState(t *testing.T) {
	stateDir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "queue.jsonl")
	q := queue.New(queuePath)
	now := time.Date(2026, time.April, 9, 6, 0, 0, 0, time.UTC)

	first := &Schedule{
		ConfigName: "doctor",
		Cadence:    "1h",
		Workflow:   "doctor",
		StateDir:   stateDir,
		Queue:      q,
		Now: func() time.Time {
			return now
		},
	}

	vessels, err := first.Scan(context.Background())
	if err != nil {
		t.Fatalf("first Scan() error = %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected first scan to emit 1 vessel, got %d", len(vessels))
	}
	if _, err := q.Enqueue(vessels[0]); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	now = now.Add(time.Hour)
	restarted := &Schedule{
		ConfigName: "doctor",
		Cadence:    "1h",
		Workflow:   "doctor",
		StateDir:   stateDir,
		Queue:      q,
		Now: func() time.Time {
			return now
		},
	}

	suppressed, err := restarted.Scan(context.Background())
	if err != nil {
		t.Fatalf("restart Scan() error = %v", err)
	}
	if len(suppressed) != 0 {
		t.Fatalf("expected active queued vessel to suppress duplicate schedule tick, got %d vessels", len(suppressed))
	}
}

func TestScheduleScanRejectsMalformedCadence(t *testing.T) {
	s := &Schedule{
		ConfigName: "broken",
		Cadence:    "not-a-cadence",
		Workflow:   "doctor",
		StateDir:   t.TempDir(),
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue.jsonl")),
	}

	if _, err := s.Scan(context.Background()); err == nil {
		t.Fatal("expected Scan() to reject malformed cadence")
	}
}

func TestSmoke_S1_ScheduledSourceFiresPersistsAndSuppressesDuplicates(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "queue.jsonl")
	q := queue.New(queuePath)
	now := time.Date(2026, time.April, 9, 6, 0, 0, 0, time.UTC)

	schedule := func() *Schedule {
		return &Schedule{
			ConfigName: "doc-gardener",
			Cadence:    "1h",
			Workflow:   "doc-garden",
			StateDir:   stateDir,
			Queue:      q,
			Now: func() time.Time {
				return now
			},
		}
	}

	first := schedule()
	firstVessels, err := first.Scan(ctx)
	require.NoError(t, err)
	require.Len(t, firstVessels, 1)

	firstVessel := firstVessels[0]
	assert.Equal(t, "schedule", firstVessel.Source)
	assert.Equal(t, "doc-garden", firstVessel.Workflow)
	assert.Equal(t, "schedule-doc-gardener-20260409t060000z", firstVessel.ID)
	assert.Equal(t, "schedule://doc-gardener/2026-04-09T06:00:00Z", firstVessel.Ref)
	assert.Equal(t, "1h", firstVessel.Meta["schedule.cadence"])
	assert.Equal(t, "doc-gardener", firstVessel.Meta["schedule.source_name"])
	assert.Equal(t, "2026-04-09T06:00:00Z", firstVessel.Meta["schedule.fired_at"])

	require.NoError(t, first.OnEnqueue(ctx, firstVessel))

	stateBytes, err := os.ReadFile(first.statePath())
	require.NoError(t, err)
	assert.JSONEq(t, `{"sources":{"doc-gardener":{"last_fired_at":"2026-04-09T06:00:00Z"}}}`, string(stateBytes))

	now = now.Add(30 * time.Minute)
	suppressed, err := schedule().Scan(ctx)
	require.NoError(t, err)
	assert.Empty(t, suppressed)

	now = now.Add(30 * time.Minute)
	due, err := schedule().Scan(ctx)
	require.NoError(t, err)
	require.Len(t, due, 1)

	secondVessel := due[0]
	assert.Equal(t, "schedule-doc-gardener-20260409t070000z", secondVessel.ID)
	assert.Equal(t, "schedule://doc-gardener/2026-04-09T07:00:00Z", secondVessel.Ref)
	assert.Equal(t, "2026-04-09T07:00:00Z", secondVessel.Meta["schedule.fired_at"])

	_, err = q.Enqueue(secondVessel)
	require.NoError(t, err)

	duplicate, err := schedule().Scan(ctx)
	require.NoError(t, err)
	assert.Empty(t, duplicate)
}

func TestSmoke_S2_MalformedCadenceRejected(t *testing.T) {
	s := &Schedule{
		ConfigName: "broken",
		Cadence:    "not-a-cadence",
		Workflow:   "doctor",
		StateDir:   t.TempDir(),
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue.jsonl")),
	}

	_, err := s.Scan(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `schedule source "broken"`)
	assert.Contains(t, err.Error(), `parse cadence "not-a-cadence"`)
}

func TestScheduleOnEnqueueAcceptsLegacyMetadata(t *testing.T) {
	stateDir := t.TempDir()
	s := &Schedule{
		ConfigName: "lessons",
		Cadence:    "@daily",
		Workflow:   "lessons",
		StateDir:   stateDir,
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue.jsonl")),
	}

	err := s.OnEnqueue(context.Background(), queue.Vessel{
		ID: "schedule-lessons-20260409t060000z",
		Meta: map[string]string{
			"schedule_fired_at": "2026-04-09T06:00:00Z",
		},
	})
	require.NoError(t, err)

	stateBytes, err := os.ReadFile(s.statePath())
	require.NoError(t, err)
	assert.JSONEq(t, `{"sources":{"lessons":{"last_fired_at":"2026-04-09T06:00:00Z"}}}`, string(stateBytes))
}

func TestScheduleScanReadsLegacyStateFile(t *testing.T) {
	stateDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, legacyScheduleStateFileName),
		[]byte(`{"lessons":{"cadence":"@daily","last_fired_at":"2026-04-09T06:00:00Z","next_due_at":"2026-04-10T00:00:00Z","last_vessel":"schedule-lessons-20260409t060000z"}}`),
		0o644,
	))

	now := time.Date(2026, time.April, 9, 12, 0, 0, 0, time.UTC)
	s := &Schedule{
		ConfigName: "lessons",
		Cadence:    "@daily",
		Workflow:   "lessons",
		StateDir:   stateDir,
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue.jsonl")),
		Now: func() time.Time {
			return now
		},
	}

	vessels, err := s.Scan(context.Background())
	require.NoError(t, err)
	assert.Empty(t, vessels)
}

func TestSmoke_S3_ScheduleFallsBackToLegacyStateAndWritesRuntimeState(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, ".gitignore"), []byte("state/\n"), 0o644))

	legacyPath := filepath.Join(stateDir, legacyScheduleStateFileName)
	require.NoError(t, os.WriteFile(
		legacyPath,
		[]byte(`{"lessons":{"cadence":"@daily","last_fired_at":"2026-04-09T06:00:00Z","next_due_at":"2026-04-10T00:00:00Z","last_vessel":"schedule-lessons-20260409t060000z"}}`),
		0o644,
	))

	now := time.Date(2026, time.April, 9, 12, 0, 0, 0, time.UTC)
	s := &Schedule{
		ConfigName: "lessons",
		Cadence:    "@daily",
		Workflow:   "lessons",
		StateDir:   stateDir,
		Queue:      queue.New(filepath.Join(t.TempDir(), "queue.jsonl")),
		Now: func() time.Time {
			return now
		},
	}

	vessels, err := s.Scan(ctx)
	require.NoError(t, err)
	assert.Empty(t, vessels)

	err = s.OnEnqueue(ctx, queue.Vessel{
		ID: "schedule-lessons-20260410t000000z",
		Meta: map[string]string{
			"schedule_fired_at": "2026-04-10T00:00:00Z",
		},
	})
	require.NoError(t, err)

	stateBytes, err := os.ReadFile(s.statePath())
	require.NoError(t, err)
	assert.JSONEq(t, `{"sources":{"lessons":{"last_fired_at":"2026-04-10T00:00:00Z"}}}`, string(stateBytes))

	legacyBytes, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	assert.JSONEq(t, `{"lessons":{"cadence":"@daily","last_fired_at":"2026-04-09T06:00:00Z","next_due_at":"2026-04-10T00:00:00Z","last_vessel":"schedule-lessons-20260409t060000z"}}`, string(legacyBytes))
}
