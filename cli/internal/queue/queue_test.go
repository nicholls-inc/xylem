package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/dtu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperTransitionToWaiting transitions a vessel from pending -> running -> waiting via the queue.
func helperTransitionToWaiting(t *testing.T, q *Queue, id string) {
	t.Helper()
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update(id, StateWaiting, ""); err != nil {
		t.Fatalf("update to waiting: %v", err)
	}
}

func newTestQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	return New(path), path
}

func testVessel(issue int) Vessel {
	return Vessel{
		ID:        fmt.Sprintf("issue-%d", issue),
		Source:    "github-issue",
		Ref:       fmt.Sprintf("https://github.com/example/repo/issues/%d", issue),
		Workflow:  "fix-bug",
		Meta:      map[string]string{"issue_num": fmt.Sprintf("%d", issue)},
		State:     StatePending,
		CreatedAt: time.Now().UTC(),
	}
}

func readNonEmptyLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read queue file: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestEnqueue(t *testing.T) {
	q, path := newTestQueue(t)
	vessel := testVessel(42)

	enqueued, err := q.Enqueue(vessel)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if !enqueued {
		t.Fatal("expected enqueued=true for new vessel")
	}

	lines := readNonEmptyLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var got Vessel
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if got.ID != "issue-42" {
		t.Fatalf("expected id issue-42, got %q", got.ID)
	}
	if got.Source != "github-issue" {
		t.Fatalf("expected source github-issue, got %q", got.Source)
	}
	if got.Ref != "https://github.com/example/repo/issues/42" {
		t.Fatalf("expected ref issue URL, got %q", got.Ref)
	}
	if got.State != StatePending {
		t.Fatalf("expected state pending, got %q", got.State)
	}
}

func TestSmoke_S2_LegacyQueueJsonlWithoutTierLoadsEmptyTier(t *testing.T) {
	q, path := newTestQueue(t)
	legacy := `{"id":"issue-42","source":"github-issue","ref":"https://github.com/example/repo/issues/42","workflow":"fix-bug","state":"pending","created_at":"2026-04-10T00:00:00Z"}`
	require.NoError(t, os.WriteFile(path, []byte(legacy+"\n"), 0o644))

	vessels, err := q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Empty(t, vessels[0].Tier)
}

func TestSmoke_S3_QueueJsonlRoundTripsTier(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(77)
	vessel.Tier = "high"

	enqueued, err := q.Enqueue(vessel)
	require.NoError(t, err)
	require.True(t, enqueued)

	vessels, err := q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "high", vessels[0].Tier)
}

func TestSmoke_S4_LegacyQueueJsonlWithoutWorkflowDigestLoadsEmptyWorkflowDigest(t *testing.T) {
	q, path := newTestQueue(t)
	legacy := `{"id":"issue-42","source":"github-issue","ref":"https://github.com/example/repo/issues/42","workflow":"fix-bug","state":"pending","created_at":"2026-04-10T00:00:00Z"}`
	require.NoError(t, os.WriteFile(path, []byte(legacy+"\n"), 0o644))

	vessels, err := q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Empty(t, vessels[0].WorkflowDigest)
}

func TestSmoke_S5_QueueJsonlRoundTripsWorkflowDigest(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(88)
	vessel.WorkflowDigest = "wf-1234abcd"

	enqueued, err := q.Enqueue(vessel)
	require.NoError(t, err)
	require.True(t, enqueued)

	vessels, err := q.List()
	require.NoError(t, err)
	require.Len(t, vessels, 1)
	assert.Equal(t, "wf-1234abcd", vessels[0].WorkflowDigest)
}

// TestWS6S28VesselJSONLNoNewFields verifies that queue JSONL records remain
// free of harness-specific fields.
//
// Covers: WS6 S28.
func TestWS6S28VesselJSONLNoNewFields(t *testing.T) {
	q, path := newTestQueue(t)
	now := time.Now().UTC()
	vessel := Vessel{
		ID:        "compat-v3-test",
		Source:    "github-issue",
		Ref:       "https://github.com/example/repo/issues/1",
		Workflow:  "fix-bug",
		State:     StatePending,
		CreatedAt: now,
	}

	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	line := strings.TrimSpace(string(raw))

	for _, field := range []string{`"id"`, `"source"`, `"state"`, `"created_at"`} {
		if !strings.Contains(line, field) {
			t.Errorf("JSONL line missing expected field %s", field)
		}
	}
	for _, forbidden := range []string{`"intermediary"`, `"audit_log"`, `"tracer"`, `"budget"`, `"trace_id"`} {
		if strings.Contains(line, forbidden) {
			t.Errorf("JSONL line contains forbidden field %s: %s", forbidden, line)
		}
	}
}

func TestDequeue(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(1)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if got == nil {
		t.Fatal("expected vessel, got nil")
	}
	if got.State != StateRunning {
		t.Fatalf("expected running, got %q", got.State)
	}
	if got.StartedAt == nil {
		t.Fatal("expected StartedAt to be set")
	}
	if got.WorkflowClass != "fix-bug" {
		t.Fatalf("expected workflow class fix-bug, got %q", got.WorkflowClass)
	}
}

func TestDequeueEmpty(t *testing.T) {
	q, _ := newTestQueue(t)
	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("dequeue empty: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil vessel, got %+v", *got)
	}
}

func TestDequeueMatchingSkipsBlockedPending(t *testing.T) {
	q, _ := newTestQueue(t)
	first := testVessel(1)
	first.Workflow = "implement-feature"
	second := testVessel(2)
	second.Workflow = "merge-pr"
	if _, err := q.Enqueue(first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if _, err := q.Enqueue(second); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	got, err := q.DequeueMatching(func(v Vessel) bool {
		return v.ConcurrencyClass() == "merge-pr"
	})
	if err != nil {
		t.Fatalf("dequeue matching: %v", err)
	}
	if got == nil {
		t.Fatal("expected vessel, got nil")
	}
	if got.ID != second.ID {
		t.Fatalf("Dequeued ID = %q, want %q", got.ID, second.ID)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if vessels[0].State != StatePending {
		t.Fatalf("first vessel state = %q, want pending", vessels[0].State)
	}
}

func TestUpdate(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(2)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if got == nil {
		t.Fatal("expected vessel")
	}

	if err := q.Update(got.ID, StateCompleted, ""); err != nil {
		t.Fatalf("update completed: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].State != StateCompleted {
		t.Fatalf("expected completed, got %q", vessels[0].State)
	}
	if vessels[0].EndedAt == nil {
		t.Fatal("expected EndedAt to be set")
	}
}

func TestUpdateFailed(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(3)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Must transition through running before going to failed.
	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if got == nil {
		t.Fatal("expected vessel")
	}

	if err := q.Update(got.ID, StateFailed, "boom"); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if vessels[0].State != StateFailed {
		t.Fatalf("expected failed, got %q", vessels[0].State)
	}
	if vessels[0].Error != "boom" {
		t.Fatalf("expected error boom, got %q", vessels[0].Error)
	}
	if vessels[0].EndedAt == nil {
		t.Fatal("expected EndedAt to be set")
	}
}

func TestCancel(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(4)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := q.Cancel(vessel.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if vessels[0].State != StateCancelled {
		t.Fatalf("expected cancelled, got %q", vessels[0].State)
	}
}

func TestCancelRunning(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(5)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}

	if err := q.Cancel(vessel.ID); err != nil {
		t.Fatalf("cancel running vessel: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if vessels[0].State != StateCancelled {
		t.Fatalf("expected cancelled, got %q", vessels[0].State)
	}
	if vessels[0].EndedAt == nil {
		t.Fatal("expected EndedAt to be set")
	}
}

func TestCancelCompleted(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(6)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Must go through pending -> running -> completed.
	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if got == nil {
		t.Fatal("expected vessel")
	}
	if err := q.Update(got.ID, StateCompleted, ""); err != nil {
		t.Fatalf("update completed: %v", err)
	}

	if err := q.Cancel(vessel.ID); err == nil {
		t.Fatal("expected error cancelling completed vessel")
	}
}

func TestCancelNotFound(t *testing.T) {
	q, _ := newTestQueue(t)
	if err := q.Cancel("issue-999"); err == nil {
		t.Fatal("expected not found error")
	}
}

func TestHasRef(t *testing.T) {
	q, _ := newTestQueue(t)
	if _, err := q.Enqueue(testVessel(42)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if !q.HasRef("https://github.com/example/repo/issues/42") {
		t.Fatal("expected HasRef to be true for enqueued ref")
	}
	if q.HasRef("https://github.com/example/repo/issues/99") {
		t.Fatal("expected HasRef to be false for unknown ref")
	}
}

func TestHasRefCancelled(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(42)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.Cancel(vessel.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if q.HasRef("https://github.com/example/repo/issues/42") {
		t.Fatal("expected cancelled vessel to not count in HasRef")
	}
}

func TestHasRefTerminalStates(t *testing.T) {
	tests := []struct {
		state       VesselState
		transitions []VesselState
	}{
		{StateCompleted, []VesselState{StateRunning, StateCompleted}},
		{StateFailed, []VesselState{StateRunning, StateFailed}},
		{StateTimedOut, []VesselState{StateRunning, StateWaiting, StateTimedOut}},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			q, _ := newTestQueue(t)
			vessel := testVessel(42)
			if _, err := q.Enqueue(vessel); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			for _, s := range tt.transitions {
				if err := q.Update(vessel.ID, s, ""); err != nil {
					t.Fatalf("update to %s: %v", s, err)
				}
			}
			if q.HasRef("https://github.com/example/repo/issues/42") {
				t.Fatalf("expected %s vessel to not block re-enqueueing", tt.state)
			}
		})
	}
}

func TestHasRefActiveStates(t *testing.T) {
	transitions := []VesselState{StatePending, StateRunning, StateWaiting}
	q, _ := newTestQueue(t)
	vessel := testVessel(42)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	for _, state := range transitions {
		if state != StatePending {
			if err := q.Update(vessel.ID, state, ""); err != nil {
				t.Fatalf("update to %s: %v", state, err)
			}
		}
		if !q.HasRef("https://github.com/example/repo/issues/42") {
			t.Fatalf("expected %s vessel to block re-enqueueing", state)
		}
	}
}

func TestHasRefAny(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(42)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if !q.HasRefAny("https://github.com/example/repo/issues/42") {
		t.Fatal("expected HasRefAny to be true for enqueued ref")
	}
	if q.HasRefAny("https://github.com/example/repo/issues/99") {
		t.Fatal("expected HasRefAny to be false for unknown ref")
	}
}

func TestHasRefAnyFindsTerminalStates(t *testing.T) {
	tests := []struct {
		state       VesselState
		transitions []VesselState
	}{
		{StateCompleted, []VesselState{StateRunning, StateCompleted}},
		{StateFailed, []VesselState{StateRunning, StateFailed}},
		{StateTimedOut, []VesselState{StateRunning, StateWaiting, StateTimedOut}},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			q, _ := newTestQueue(t)
			vessel := testVessel(42)
			if _, err := q.Enqueue(vessel); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			for _, s := range tt.transitions {
				if err := q.Update(vessel.ID, s, ""); err != nil {
					t.Fatalf("update to %s: %v", s, err)
				}
			}
			// HasRefAny should still find vessels in terminal states
			if !q.HasRefAny("https://github.com/example/repo/issues/42") {
				t.Fatalf("expected HasRefAny to find %s vessel", tt.state)
			}
			// HasRef should NOT find vessels in terminal states
			if q.HasRef("https://github.com/example/repo/issues/42") {
				t.Fatalf("expected HasRef to NOT find %s vessel", tt.state)
			}
		})
	}
}

func TestHasRefAnyCancelled(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(42)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.Cancel(vessel.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// HasRefAny should find cancelled vessels too
	if !q.HasRefAny("https://github.com/example/repo/issues/42") {
		t.Fatal("expected HasRefAny to find cancelled vessel")
	}
	// HasRef should NOT find cancelled vessels
	if q.HasRef("https://github.com/example/repo/issues/42") {
		t.Fatal("expected HasRef to NOT find cancelled vessel")
	}
}

func TestEnqueueIdempotentDuplicateRef(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(42)

	enqueued, err := q.Enqueue(vessel)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if !enqueued {
		t.Fatal("expected first enqueue to succeed")
	}

	// Second enqueue with the same ref should be a no-op.
	vessel2 := testVessel(42)
	vessel2.ID = "issue-42-dup"
	enqueued, err = q.Enqueue(vessel2)
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if enqueued {
		t.Fatal("expected second enqueue to be skipped (duplicate active ref)")
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel in queue, got %d", len(vessels))
	}
}

func TestEnqueueAfterTerminalState(t *testing.T) {
	t.Run("after completed", func(t *testing.T) {
		q, _ := newTestQueue(t)
		vessel := testVessel(42)
		if _, err := q.Enqueue(vessel); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := q.Update(vessel.ID, StateRunning, ""); err != nil {
			t.Fatalf("update to running: %v", err)
		}
		if err := q.Update(vessel.ID, StateCompleted, ""); err != nil {
			t.Fatalf("update to completed: %v", err)
		}
		vessel2 := testVessel(42)
		vessel2.ID = "issue-42-retry"
		enqueued, err := q.Enqueue(vessel2)
		if err != nil {
			t.Fatalf("re-enqueue: %v", err)
		}
		if !enqueued {
			t.Fatal("expected re-enqueue to succeed after completed state")
		}
	})

	tests := []struct {
		name        string
		transitions []VesselState
	}{
		{"after cancelled", []VesselState{StateCancelled}},
		{"after failed", []VesselState{StateRunning, StateFailed}},
		{"after timed_out", []VesselState{StateRunning, StateWaiting, StateTimedOut}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, _ := newTestQueue(t)
			vessel := testVessel(42)
			if _, err := q.Enqueue(vessel); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			for _, s := range tt.transitions {
				if err := q.Update(vessel.ID, s, ""); err != nil {
					t.Fatalf("update to %s: %v", s, err)
				}
			}

			// Re-enqueue should succeed for non-completed terminal states.
			vessel2 := testVessel(42)
			vessel2.ID = "issue-42-retry"
			enqueued, err := q.Enqueue(vessel2)
			if err != nil {
				t.Fatalf("re-enqueue: %v", err)
			}
			if !enqueued {
				t.Fatal("expected re-enqueue to succeed after terminal state")
			}

			vessels, err := q.List()
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(vessels) != 2 {
				t.Fatalf("expected 2 vessels in queue, got %d", len(vessels))
			}
		})
	}
}

func TestEnqueueEmptyRefAlwaysSucceeds(t *testing.T) {
	q, _ := newTestQueue(t)

	v1 := Vessel{
		ID:        "task-1",
		Source:    "manual",
		Workflow:  "fix-bug",
		Prompt:    "do something",
		State:     StatePending,
		CreatedAt: time.Now().UTC(),
	}
	v2 := Vessel{
		ID:        "task-2",
		Source:    "manual",
		Workflow:  "fix-bug",
		Prompt:    "do something else",
		State:     StatePending,
		CreatedAt: time.Now().UTC(),
	}

	enqueued, err := q.Enqueue(v1)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if !enqueued {
		t.Fatal("expected first enqueue with empty ref to succeed")
	}

	enqueued, err = q.Enqueue(v2)
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if !enqueued {
		t.Fatal("expected second enqueue with empty ref to succeed")
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("expected 2 vessels, got %d", len(vessels))
	}
}

func TestCorruption(t *testing.T) {
	q, path := newTestQueue(t)
	j1 := testVessel(7)
	j2 := testVessel(8)

	b1, err := json.Marshal(j1)
	if err != nil {
		t.Fatalf("marshal j1: %v", err)
	}
	b2, err := json.Marshal(j2)
	if err != nil {
		t.Fatalf("marshal j2: %v", err)
	}

	content := strings.Join([]string{string(b1), "{not-json", string(b2)}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write corruption file: %v", err)
	}

	vessels, err := q.List()
	// readAllVessels now returns an error when malformed lines are encountered,
	// but still returns the valid vessels that could be parsed.
	if err == nil {
		t.Fatal("expected error for malformed entries")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed error, got: %v", err)
	}
	if len(vessels) != 2 {
		t.Fatalf("expected 2 valid vessels despite malformed line, got %d", len(vessels))
	}
}

func TestConcurrentEnqueue(t *testing.T) {
	q, _ := newTestQueue(t)
	const workers = 10

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			vessel := testVessel(100 + i)
			if _, err := q.Enqueue(vessel); err != nil {
				t.Errorf("enqueue %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != workers {
		t.Fatalf("expected %d vessels, got %d", workers, len(vessels))
	}
}

func TestListByState(t *testing.T) {
	q, _ := newTestQueue(t)
	vessels := []Vessel{testVessel(200), testVessel(201), testVessel(202)}
	vessels[1].State = StateRunning
	vessels[2].State = StateCompleted

	for _, vessel := range vessels {
		if _, err := q.Enqueue(vessel); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	pending, err := q.ListByState(StatePending)
	if err != nil {
		t.Fatalf("list by state: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
}

// --- State transition validation tests ---

func TestUpdateInvalidTransitionCompletedToPending(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(10)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update(vessel.ID, StateCompleted, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}

	err := q.Update(vessel.ID, StatePending, "")
	if err == nil {
		t.Fatal("expected error for completed->pending transition")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got: %v", err)
	}
}

func TestUpdateInvalidTransitionPendingToCompleted(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(11)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	err := q.Update(vessel.ID, StateCompleted, "")
	if err == nil {
		t.Fatal("expected error for pending->completed transition")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got: %v", err)
	}
}

func TestUpdateInvalidTransitionPendingToFailed(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(12)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	err := q.Update(vessel.ID, StateFailed, "boom")
	if err == nil {
		t.Fatal("expected error for pending->failed transition")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got: %v", err)
	}
}

func TestUpdateInvalidTransitionCancelledToRunning(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(13)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.Cancel(vessel.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	err := q.Update(vessel.ID, StateRunning, "")
	if err == nil {
		t.Fatal("expected error for cancelled->running transition")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got: %v", err)
	}
}

func TestUpdateValidTransitionFailedToPending(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(14)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update(vessel.ID, StateFailed, "transient error"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	failed, err := q.FindByID(vessel.ID)
	if err != nil {
		t.Fatalf("FindByID after fail: %v", err)
	}
	failed.CurrentPhase = 2
	failed.PhaseOutputs = map[string]string{"plan": "done"}
	failed.WorktreePath = "/tmp/wt-14"
	if err := q.UpdateVessel(*failed); err != nil {
		t.Fatalf("UpdateVessel(failed): %v", err)
	}

	// Retry: failed -> pending should be allowed.
	if err := q.Update(vessel.ID, StatePending, ""); err != nil {
		t.Fatalf("expected failed->pending to succeed for retry, got: %v", err)
	}

	got, err := q.FindByID(vessel.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.State != StatePending {
		t.Fatalf("expected pending after retry, got %q", got.State)
	}
	if got.WorktreePath != "/tmp/wt-14" {
		t.Fatalf("expected WorktreePath preserved, got %q", got.WorktreePath)
	}
	if got.CurrentPhase != 2 {
		t.Fatalf("expected CurrentPhase preserved, got %d", got.CurrentPhase)
	}
	if got.PhaseOutputs["plan"] != "done" {
		t.Fatalf("expected PhaseOutputs[plan]=done, got %q", got.PhaseOutputs["plan"])
	}
}

func TestUpdateValidTransitionRunningToPending(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(15)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	running, err := q.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if running == nil {
		t.Fatal("Dequeue() = nil, want vessel")
	}
	running.CurrentPhase = 2
	running.PhaseOutputs = map[string]string{"plan": "done"}
	running.WorktreePath = "/tmp/wt-15"
	running.GateRetries = 3
	running.FailedPhase = "implement"
	running.GateOutput = "missing check"
	if err := q.UpdateVessel(*running); err != nil {
		t.Fatalf("UpdateVessel(running): %v", err)
	}

	if err := q.Update(vessel.ID, StatePending, ""); err != nil {
		t.Fatalf("expected running->pending to succeed for restart recovery, got: %v", err)
	}

	got, err := q.FindByID(vessel.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.State != StatePending {
		t.Fatalf("expected pending after restart recovery, got %q", got.State)
	}
	if got.StartedAt != nil {
		t.Fatal("expected StartedAt to be cleared")
	}
	if got.GateRetries != 0 {
		t.Fatalf("expected GateRetries reset, got %d", got.GateRetries)
	}
	if got.FailedPhase != "" {
		t.Fatalf("expected FailedPhase cleared, got %q", got.FailedPhase)
	}
	if got.GateOutput != "" {
		t.Fatalf("expected GateOutput cleared, got %q", got.GateOutput)
	}
	if got.CurrentPhase != 2 {
		t.Fatalf("expected CurrentPhase preserved, got %d", got.CurrentPhase)
	}
	if got.PhaseOutputs["plan"] != "done" {
		t.Fatalf("expected PhaseOutputs[plan]=done, got %q", got.PhaseOutputs["plan"])
	}
	if got.WorktreePath != "" {
		t.Fatalf("expected WorktreePath cleared, got %q", got.WorktreePath)
	}
}

func TestUpdateRunningToCancelled(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(16)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}

	if err := q.Update(vessel.ID, StateCancelled, ""); err != nil {
		t.Fatalf("running->cancelled: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if vessels[0].State != StateCancelled {
		t.Fatalf("expected cancelled, got %q", vessels[0].State)
	}
}

func TestConcurrentUpdateAndList(t *testing.T) {
	q, _ := newTestQueue(t)
	const numVessels = 5

	// Enqueue and dequeue to get vessels into running state.
	for i := 0; i < numVessels; i++ {
		if _, err := q.Enqueue(testVessel(800 + i)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	for i := 0; i < numVessels; i++ {
		if _, err := q.Dequeue(); err != nil {
			t.Fatalf("dequeue: %v", err)
		}
	}

	var wg sync.WaitGroup

	// Concurrently update vessels to completed while reading.
	wg.Add(numVessels)
	for i := 0; i < numVessels; i++ {
		i := i
		go func() {
			defer wg.Done()
			err := q.Update(fmt.Sprintf("issue-%d", 800+i), StateCompleted, "")
			if err != nil {
				t.Errorf("update %d: %v", i, err)
			}
		}()
	}

	wg.Add(numVessels)
	for i := 0; i < numVessels; i++ {
		go func() {
			defer wg.Done()
			_, _ = q.List()
			_, _ = q.ListByState(StateCompleted)
		}()
	}

	wg.Wait()

	// All vessels should be completed.
	vessels, err := q.List()
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	for _, vessel := range vessels {
		if vessel.State != StateCompleted {
			t.Errorf("vessel %s: expected completed, got %s", vessel.ID, vessel.State)
		}
	}
}

// --- Additional coverage tests ---

func TestUpdateNonExistentVessel(t *testing.T) {
	q, _ := newTestQueue(t)
	if _, err := q.Enqueue(testVessel(1)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	err := q.Update("issue-999", StateCompleted, "")
	if err == nil {
		t.Fatal("expected error for non-existent vessel")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got: %v", err)
	}
}

func TestUpdateRunningBranchSetsTimestamps(t *testing.T) {
	// Cover the StateRunning case in Update's switch: sets StartedAt if nil,
	// clears EndedAt and Error.
	q, _ := newTestQueue(t)
	vessel := testVessel(20)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// pending -> running via Update (instead of Dequeue)
	if err := q.Update(vessel.ID, StateRunning, ""); err != nil {
		t.Fatalf("update to running: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if vessels[0].State != StateRunning {
		t.Fatalf("expected running, got %q", vessels[0].State)
	}
	if vessels[0].StartedAt == nil {
		t.Fatal("expected StartedAt to be set")
	}
	if vessels[0].EndedAt != nil {
		t.Fatal("expected EndedAt to be nil")
	}
	if vessels[0].Error != "" {
		t.Fatalf("expected empty error, got %q", vessels[0].Error)
	}
}

func TestDequeueSkipsNonPending(t *testing.T) {
	// Dequeue should pick the first pending vessel, skipping running/completed.
	q, _ := newTestQueue(t)
	j1 := testVessel(30)
	j1.State = StateRunning // already running
	j2 := testVessel(31)
	j2.State = StateCompleted
	j3 := testVessel(32) // pending

	for _, j := range []Vessel{j1, j2, j3} {
		if _, err := q.Enqueue(j); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if got == nil {
		t.Fatal("expected a vessel, got nil")
	}
	if got.ID != "issue-32" {
		t.Fatalf("expected issue-32 (first pending), got %s", got.ID)
	}
}

func TestBlankLinesIgnored(t *testing.T) {
	q, path := newTestQueue(t)
	j := testVessel(60)
	b, _ := json.Marshal(j)
	// File with blank lines interspersed
	content := "\n\n" + string(b) + "\n\n\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel (blank lines ignored), got %d", len(vessels))
	}
	_ = q // satisfy vet
}

func TestLegacyJSONLMigration(t *testing.T) {
	_, path := newTestQueue(t)
	q := New(path)

	// Write a legacy-format entry with issue_url and issue_num
	legacy := `{"id":"issue-42","issue_url":"https://github.com/example/repo/issues/42","issue_num":42,"workflow":"fix-bug","state":"pending","created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(legacy+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	v := vessels[0]
	if v.Source != "github-issue" {
		t.Fatalf("expected source github-issue, got %q", v.Source)
	}
	if v.Ref != "https://github.com/example/repo/issues/42" {
		t.Fatalf("expected ref from issue_url, got %q", v.Ref)
	}
	if v.Meta["issue_num"] != "42" {
		t.Fatalf("expected meta issue_num=42, got %q", v.Meta["issue_num"])
	}
}

// --- v2 state and field tests ---

func TestWaitingState(t *testing.T) {
	tests := []struct {
		name      string
		toState   VesselState
		errMsg    string
		wantErr   bool
		wantState VesselState
	}{
		{
			name:      "running to waiting",
			toState:   StateWaiting,
			wantState: StateWaiting,
		},
		{
			name:      "waiting to pending (resume)",
			toState:   StatePending,
			wantState: StatePending,
		},
		{
			name:      "waiting to timed_out",
			toState:   StateTimedOut,
			errMsg:    "gate timeout",
			wantState: StateTimedOut,
		},
		{
			name:      "waiting to cancelled",
			toState:   StateCancelled,
			wantState: StateCancelled,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, _ := newTestQueue(t)
			vessel := testVessel(500)
			if _, err := q.Enqueue(vessel); err != nil {
				t.Fatalf("enqueue: %v", err)
			}

			// Get to running state.
			if _, err := q.Dequeue(); err != nil {
				t.Fatalf("dequeue: %v", err)
			}

			// For tests starting from waiting, transition to waiting first.
			if tc.toState != StateWaiting {
				if err := q.Update(vessel.ID, StateWaiting, ""); err != nil {
					t.Fatalf("transition to waiting: %v", err)
				}
			}

			err := q.Update(vessel.ID, tc.toState, tc.errMsg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Update() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			vessels, err := q.List()
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if vessels[0].State != tc.wantState {
				t.Fatalf("expected state %s, got %s", tc.wantState, vessels[0].State)
			}
		})
	}

	// Verify waiting does NOT allow -> completed or -> failed.
	t.Run("waiting to completed is invalid", func(t *testing.T) {
		q, _ := newTestQueue(t)
		vessel := testVessel(501)
		if _, err := q.Enqueue(vessel); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		helperTransitionToWaiting(t, q, vessel.ID)

		err := q.Update(vessel.ID, StateCompleted, "")
		if err == nil {
			t.Fatal("expected error for waiting->completed transition")
		}
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("expected ErrInvalidTransition, got: %v", err)
		}
	})

	t.Run("waiting to failed is invalid", func(t *testing.T) {
		q, _ := newTestQueue(t)
		vessel := testVessel(502)
		if _, err := q.Enqueue(vessel); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		helperTransitionToWaiting(t, q, vessel.ID)

		err := q.Update(vessel.ID, StateFailed, "boom")
		if err == nil {
			t.Fatal("expected error for waiting->failed transition")
		}
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("expected ErrInvalidTransition, got: %v", err)
		}
	})
}

func TestWaitingStateDoesNotSetEndedAt(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(503)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	helperTransitionToWaiting(t, q, vessel.ID)

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if vessels[0].EndedAt != nil {
		t.Fatal("expected EndedAt to be nil for waiting state")
	}
}

func TestUpdateWaitingToPendingClearsWaitingMetadata(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(504)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	helperTransitionToWaiting(t, q, vessel.ID)

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list before resume: %v", err)
	}
	vessels[0].GateRetries = 2
	vessels[0].FailedPhase = "plan"
	vessels[0].GateOutput = "missing label"
	if err := q.UpdateVessel(vessels[0]); err != nil {
		t.Fatalf("UpdateVessel before resume: %v", err)
	}

	if err := q.Update(vessel.ID, StatePending, ""); err != nil {
		t.Fatalf("resume to pending: %v", err)
	}

	got, err := q.FindByID(vessel.ID)
	if err != nil {
		t.Fatalf("FindByID after resume: %v", err)
	}
	if got.State != StatePending {
		t.Fatalf("expected pending, got %s", got.State)
	}
	if got.WaitingSince != nil {
		t.Fatal("expected WaitingSince to be cleared")
	}
	if got.WaitingFor != "" {
		t.Fatalf("expected WaitingFor cleared, got %q", got.WaitingFor)
	}
	if got.GateRetries != 0 {
		t.Fatalf("expected GateRetries reset, got %d", got.GateRetries)
	}
	if got.FailedPhase != "" {
		t.Fatalf("expected FailedPhase cleared, got %q", got.FailedPhase)
	}
	if got.GateOutput != "" {
		t.Fatalf("expected GateOutput cleared, got %q", got.GateOutput)
	}
}

func TestTimedOutState(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(510)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	helperTransitionToWaiting(t, q, vessel.ID)

	if err := q.Update(vessel.ID, StateTimedOut, "gate timeout"); err != nil {
		t.Fatalf("update to timed_out: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if vessels[0].State != StateTimedOut {
		t.Fatalf("expected timed_out, got %s", vessels[0].State)
	}
	if vessels[0].EndedAt == nil {
		t.Fatal("expected EndedAt to be set for timed_out")
	}
	if vessels[0].Error != "gate timeout" {
		t.Fatalf("expected error 'gate timeout', got %q", vessels[0].Error)
	}

	// timed_out is terminal — no transitions out.
	for _, target := range []VesselState{StatePending, StateRunning, StateWaiting, StateCompleted, StateFailed, StateCancelled} {
		err := q.Update(vessel.ID, target, "")
		if err == nil {
			t.Fatalf("expected error for timed_out->%s transition", target)
		}
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("expected ErrInvalidTransition for timed_out->%s, got: %v", target, err)
		}
	}
}

func TestV2VesselFields(t *testing.T) {
	q, _ := newTestQueue(t)
	now := time.Now().UTC()
	vessel := Vessel{
		ID:           "v2-test-1",
		Source:       "github-issue",
		Ref:          "https://github.com/example/repo/issues/99",
		Workflow:     "fix-bug",
		State:        StatePending,
		CreatedAt:    now,
		CurrentPhase: 2,
		PhaseOutputs: map[string]string{"plan": "done", "implement": "in-progress"},
		GateRetries:  3,
		WaitingSince: &now,
		WaitingFor:   "review-label",
		WorktreePath: "/tmp/worktree-abc",
		FailedPhase:  "implement",
		GateOutput:   "label not found",
		RetryOf:      "v2-test-0",
	}

	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}

	got := vessels[0]
	if got.CurrentPhase != 2 {
		t.Fatalf("expected CurrentPhase 2, got %d", got.CurrentPhase)
	}
	if got.PhaseOutputs["plan"] != "done" {
		t.Fatalf("expected PhaseOutputs[plan]=done, got %q", got.PhaseOutputs["plan"])
	}
	if got.PhaseOutputs["implement"] != "in-progress" {
		t.Fatalf("expected PhaseOutputs[implement]=in-progress, got %q", got.PhaseOutputs["implement"])
	}
	if got.GateRetries != 3 {
		t.Fatalf("expected GateRetries 3, got %d", got.GateRetries)
	}
	if got.WaitingSince == nil {
		t.Fatal("expected WaitingSince to be set")
	}
	if got.WaitingFor != "review-label" {
		t.Fatalf("expected WaitingFor 'review-label', got %q", got.WaitingFor)
	}
	if got.WorktreePath != "/tmp/worktree-abc" {
		t.Fatalf("expected WorktreePath '/tmp/worktree-abc', got %q", got.WorktreePath)
	}
	if got.FailedPhase != "implement" {
		t.Fatalf("expected FailedPhase 'implement', got %q", got.FailedPhase)
	}
	if got.GateOutput != "label not found" {
		t.Fatalf("expected GateOutput 'label not found', got %q", got.GateOutput)
	}
	if got.RetryOf != "v2-test-0" {
		t.Fatalf("expected RetryOf 'v2-test-0', got %q", got.RetryOf)
	}
}

func TestBackwardCompat(t *testing.T) {
	_, path := newTestQueue(t)
	q := New(path)

	// Write a JSONL line with only v1 fields (no v2 fields in JSON).
	v1JSON := `{"id":"compat-1","source":"github-issue","ref":"https://github.com/example/repo/issues/1","workflow":"fix-bug","state":"pending","created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(v1JSON+"\n"), 0o644); err != nil {
		t.Fatalf("write v1 json: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}

	got := vessels[0]
	// v1 fields present.
	if got.ID != "compat-1" {
		t.Fatalf("expected id compat-1, got %q", got.ID)
	}
	if got.State != StatePending {
		t.Fatalf("expected pending, got %q", got.State)
	}
	// v2 fields should be zero values.
	if got.CurrentPhase != 0 {
		t.Fatalf("expected CurrentPhase 0, got %d", got.CurrentPhase)
	}
	if got.PhaseOutputs != nil {
		t.Fatalf("expected nil PhaseOutputs, got %v", got.PhaseOutputs)
	}
	if got.GateRetries != 0 {
		t.Fatalf("expected GateRetries 0, got %d", got.GateRetries)
	}
	if got.WaitingSince != nil {
		t.Fatal("expected nil WaitingSince")
	}
	if got.WaitingFor != "" {
		t.Fatalf("expected empty WaitingFor, got %q", got.WaitingFor)
	}
	if got.WorktreePath != "" {
		t.Fatalf("expected empty WorktreePath, got %q", got.WorktreePath)
	}
	if got.FailedPhase != "" {
		t.Fatalf("expected empty FailedPhase, got %q", got.FailedPhase)
	}
	if got.GateOutput != "" {
		t.Fatalf("expected empty GateOutput, got %q", got.GateOutput)
	}
	if got.RetryOf != "" {
		t.Fatalf("expected empty RetryOf, got %q", got.RetryOf)
	}
}

func TestUpdateVessel(t *testing.T) {
	t.Run("update phase tracking fields", func(t *testing.T) {
		q, _ := newTestQueue(t)
		vessel := testVessel(600)
		if _, err := q.Enqueue(vessel); err != nil {
			t.Fatalf("enqueue: %v", err)
		}

		// Read it back, modify v2 fields, and persist.
		vessels, err := q.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		updated := vessels[0]
		updated.CurrentPhase = 3
		updated.PhaseOutputs = map[string]string{"plan": "ok", "implement": "ok", "test": "running"}
		updated.WorktreePath = "/tmp/wt-600"

		if err := q.UpdateVessel(updated); err != nil {
			t.Fatalf("UpdateVessel: %v", err)
		}

		got, err := q.FindByID(vessel.ID)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		if got.CurrentPhase != 3 {
			t.Fatalf("expected CurrentPhase 3, got %d", got.CurrentPhase)
		}
		if got.PhaseOutputs["test"] != "running" {
			t.Fatalf("expected PhaseOutputs[test]=running, got %q", got.PhaseOutputs["test"])
		}
		if got.WorktreePath != "/tmp/wt-600" {
			t.Fatalf("expected WorktreePath '/tmp/wt-600', got %q", got.WorktreePath)
		}
	})

	t.Run("update non-existent vessel returns error", func(t *testing.T) {
		q, _ := newTestQueue(t)
		ghost := Vessel{ID: "ghost-999"}
		err := q.UpdateVessel(ghost)
		if err == nil {
			t.Fatal("expected error for non-existent vessel")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected not-found error, got: %v", err)
		}
	})

	t.Run("invalid state transition is rejected", func(t *testing.T) {
		q, _ := newTestQueue(t)
		vessel := testVessel(601)
		if _, err := q.Enqueue(vessel); err != nil {
			t.Fatalf("enqueue: %v", err)
		}

		updated := vessel
		updated.State = StateCompleted
		err := q.UpdateVessel(updated)
		if err == nil {
			t.Fatal("expected invalid transition error")
		}
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("expected ErrInvalidTransition, got %v", err)
		}
	})
}

func TestQueueRecordsDTUVesselEvents(t *testing.T) {
	stateDir := t.TempDir()
	store, err := dtu.NewStore(stateDir, "universe-1")
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(&dtu.State{
		UniverseID: "universe-1",
		Metadata:   dtu.ManifestMetadata{Name: "sample"},
		Clock:      dtu.ClockState{Now: "2026-01-02T03:04:05Z"},
		Counters:   dtu.Counters{NextCommentID: 1, NextReviewID: 1, NextCheckID: 1},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	t.Setenv(dtu.EnvStatePath, store.Path())
	t.Setenv(dtu.EnvStateDir, stateDir)
	t.Setenv(dtu.EnvUniverseID, "universe-1")

	q, _ := newTestQueue(t)
	vessel := testVessel(601)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	running, err := q.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if running == nil {
		t.Fatal("Dequeue() = nil, want vessel")
	}
	waitingSince := running.CreatedAt.Add(time.Minute)
	running.State = StateWaiting
	running.CurrentPhase = 1
	running.GateRetries = 2
	running.WaitingSince = &waitingSince
	running.WaitingFor = "plan-approved"
	running.FailedPhase = "plan"
	if err := q.UpdateVessel(*running); err != nil {
		t.Fatalf("UpdateVessel() error = %v", err)
	}
	if err := q.Update(vessel.ID, StatePending, ""); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var vesselEvents []dtu.Event
	for _, event := range events {
		if event.Kind == dtu.EventKindVesselUpdated {
			vesselEvents = append(vesselEvents, event)
		}
	}
	if len(vesselEvents) != 4 {
		t.Fatalf("len(vessel events) = %d, want 4", len(vesselEvents))
	}
	if got, want := vesselEvents[0].Vessel.Operation, dtu.VesselOperationEnqueue; got != want {
		t.Fatalf("vesselEvents[0].Operation = %q, want %q", got, want)
	}
	if got, want := vesselEvents[1].Vessel.OldState, string(StatePending); got != want {
		t.Fatalf("vesselEvents[1].OldState = %q, want %q", got, want)
	}
	if got, want := vesselEvents[1].Vessel.NewState, string(StateRunning); got != want {
		t.Fatalf("vesselEvents[1].NewState = %q, want %q", got, want)
	}
	waitingEvent := vesselEvents[2].Vessel
	if waitingEvent == nil || waitingEvent.Current == nil {
		t.Fatalf("waiting event = %#v, want current snapshot", waitingEvent)
	}
	if got, want := waitingEvent.Operation, dtu.VesselOperationUpdateVessel; got != want {
		t.Fatalf("waiting event operation = %q, want %q", got, want)
	}
	if got, want := waitingEvent.NewState, string(StateWaiting); got != want {
		t.Fatalf("waiting event new state = %q, want %q", got, want)
	}
	if got, want := waitingEvent.Current.WaitingFor, "plan-approved"; got != want {
		t.Fatalf("waiting event WaitingFor = %q, want %q", got, want)
	}
	if got, want := waitingEvent.Current.GateRetries, 2; got != want {
		t.Fatalf("waiting event GateRetries = %d, want %d", got, want)
	}
	if got, want := waitingEvent.Current.FailedPhase, "plan"; got != want {
		t.Fatalf("waiting event FailedPhase = %q, want %q", got, want)
	}
	resumeEvent := vesselEvents[3].Vessel
	if resumeEvent == nil || resumeEvent.Current == nil {
		t.Fatalf("resume event = %#v, want current snapshot", resumeEvent)
	}
	if got, want := resumeEvent.OldState, string(StateWaiting); got != want {
		t.Fatalf("resume event old state = %q, want %q", got, want)
	}
	if got, want := resumeEvent.NewState, string(StatePending); got != want {
		t.Fatalf("resume event new state = %q, want %q", got, want)
	}
	if resumeEvent.Current.WaitingFor != "" || resumeEvent.Current.GateRetries != 0 || resumeEvent.Current.FailedPhase != "" {
		t.Fatalf("resume event current snapshot = %#v, want waiting metadata cleared", resumeEvent.Current)
	}
}

func TestQueueSurfacesDTUVesselEventErrors(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(602)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	invalidStatePath := filepath.Join(t.TempDir(), "not-a-state-file.json")
	if err := os.WriteFile(invalidStatePath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", invalidStatePath, err)
	}
	t.Setenv(dtu.EnvStatePath, invalidStatePath)

	if _, err := q.Dequeue(); err == nil {
		t.Fatal("Dequeue() error = nil, want DTU event recording failure")
	} else if !strings.Contains(err.Error(), "record DTU vessel event") {
		t.Fatalf("Dequeue() error = %v, want DTU vessel event context", err)
	}
}

func TestFindByID(t *testing.T) {
	t.Run("find existing vessel", func(t *testing.T) {
		q, _ := newTestQueue(t)
		vessel := testVessel(700)
		if _, err := q.Enqueue(vessel); err != nil {
			t.Fatalf("enqueue: %v", err)
		}

		got, err := q.FindByID(vessel.ID)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		if got.ID != vessel.ID {
			t.Fatalf("expected id %s, got %s", vessel.ID, got.ID)
		}
		if got.Source != vessel.Source {
			t.Fatalf("expected source %s, got %s", vessel.Source, got.Source)
		}
	})

	t.Run("find non-existent vessel returns error", func(t *testing.T) {
		q, _ := newTestQueue(t)
		_, err := q.FindByID("does-not-exist")
		if err == nil {
			t.Fatal("expected error for non-existent vessel")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected not-found error, got: %v", err)
		}
	})
}

func TestFindLatestByRef(t *testing.T) {
	t.Run("returns latest vessel for ref", func(t *testing.T) {
		q, _ := newTestQueue(t)
		vessel := testVessel(701)
		if _, err := q.Enqueue(vessel); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if _, err := q.Dequeue(); err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if err := q.Update(vessel.ID, StateFailed, "boom"); err != nil {
			t.Fatalf("update failed: %v", err)
		}
		retry := vessel
		retry.ID = "issue-701-retry-1"
		retry.State = StatePending
		retry.CreatedAt = time.Now().UTC()
		if _, err := q.Enqueue(retry); err != nil {
			t.Fatalf("enqueue retry: %v", err)
		}

		got, err := q.FindLatestByRef(vessel.Ref)
		if err != nil {
			t.Fatalf("FindLatestByRef: %v", err)
		}
		if got.ID != retry.ID {
			t.Fatalf("expected latest id %s, got %s", retry.ID, got.ID)
		}
	})

	t.Run("missing ref returns error", func(t *testing.T) {
		q, _ := newTestQueue(t)
		_, err := q.FindLatestByRef("https://github.com/example/repo/issues/999")
		if err == nil {
			t.Fatal("expected error for missing ref")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected not-found error, got: %v", err)
		}
	})
}

// --- Duplicate-ID tests (re-enqueued vessels) ---

// helperCompleteVessel transitions the first pending vessel through pending -> running -> completed.
func helperCompleteVessel(t *testing.T, q *Queue, id string) {
	t.Helper()
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update(id, StateCompleted, ""); err != nil {
		t.Fatalf("update to completed: %v", err)
	}
}

// helperFailVessel transitions the first pending vessel through pending -> running -> failed.
func helperFailVessel(t *testing.T, q *Queue, id string) {
	t.Helper()
	if _, err := q.Dequeue(); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if err := q.Update(id, StateFailed, "test failure"); err != nil {
		t.Fatalf("update to failed: %v", err)
	}
}

// helperEnqueueFailThenReenqueue creates a queue with two records for the
// same vessel ID: the first failed, the second pending (failed vessels allow
// re-enqueue; completed vessels do not).
func helperEnqueueFailThenReenqueue(t *testing.T) (*Queue, Vessel) {
	t.Helper()
	q, _ := newTestQueue(t)
	vessel := testVessel(42)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	helperFailVessel(t, q, vessel.ID)
	if _, err := q.Enqueue(testVessel(42)); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	return q, vessel
}

func TestDuplicateID(t *testing.T) {
	t.Run("Update", func(t *testing.T) {
		q, vessel := helperEnqueueFailThenReenqueue(t)

		// Dequeue the re-enqueued vessel (now running).
		got, err := q.Dequeue()
		if err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if got == nil {
			t.Fatal("expected vessel from dequeue")
		}

		// Update should target the re-enqueued (running) vessel, not the old failed one.
		if err := q.Update(vessel.ID, StateCompleted, ""); err != nil {
			t.Fatalf("update: %v", err)
		}

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vessels) != 2 {
			t.Fatalf("expected 2 vessels, got %d", len(vessels))
		}
		if vessels[0].State != StateFailed {
			t.Fatalf("expected first record (old) to be failed, got %s", vessels[0].State)
		}
		if vessels[1].State != StateCompleted {
			t.Fatalf("expected second record (re-enqueued) to be completed, got %s", vessels[1].State)
		}
	})

	t.Run("UpdateVessel", func(t *testing.T) {
		q, vessel := helperEnqueueFailThenReenqueue(t)

		found, err := q.FindByID(vessel.ID)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		found.WorktreePath = "/tmp/wt-updated"
		found.CurrentPhase = 5

		if err := q.UpdateVessel(*found); err != nil {
			t.Fatalf("UpdateVessel: %v", err)
		}

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vessels) != 2 {
			t.Fatalf("expected 2 vessels, got %d", len(vessels))
		}
		if vessels[0].State != StateFailed {
			t.Fatalf("expected first record to remain failed, got %s", vessels[0].State)
		}
		if vessels[0].WorktreePath != "" {
			t.Fatalf("expected first record WorktreePath empty, got %q", vessels[0].WorktreePath)
		}
		if vessels[1].WorktreePath != "/tmp/wt-updated" {
			t.Fatalf("expected second record WorktreePath '/tmp/wt-updated', got %q", vessels[1].WorktreePath)
		}
		if vessels[1].CurrentPhase != 5 {
			t.Fatalf("expected second record CurrentPhase 5, got %d", vessels[1].CurrentPhase)
		}
	})

	t.Run("FindByID", func(t *testing.T) {
		q, vessel := helperEnqueueFailThenReenqueue(t)

		got, err := q.FindByID(vessel.ID)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		if got.State != StatePending {
			t.Fatalf("expected FindByID to return the re-enqueued pending vessel, got state %s", got.State)
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		q, vessel := helperEnqueueFailThenReenqueue(t)

		if err := q.Cancel(vessel.ID); err != nil {
			t.Fatalf("cancel: %v", err)
		}

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vessels) != 2 {
			t.Fatalf("expected 2 vessels, got %d", len(vessels))
		}
		if vessels[0].State != StateFailed {
			t.Fatalf("expected first record (old) to remain failed, got %s", vessels[0].State)
		}
		if vessels[1].State != StateCancelled {
			t.Fatalf("expected second record (re-enqueued) to be cancelled, got %s", vessels[1].State)
		}
	})
}

func TestCancelWaitingVessel(t *testing.T) {
	q, _ := newTestQueue(t)
	vessel := testVessel(900)
	if _, err := q.Enqueue(vessel); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	helperTransitionToWaiting(t, q, vessel.ID)

	// Cancel the waiting vessel.
	if err := q.Cancel(vessel.ID); err != nil {
		t.Fatalf("cancel waiting vessel: %v", err)
	}

	vessels, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if vessels[0].State != StateCancelled {
		t.Fatalf("expected cancelled, got %s", vessels[0].State)
	}
	if vessels[0].EndedAt == nil {
		t.Fatal("expected EndedAt to be set after cancel")
	}
}

// --- Compaction tests ---

func TestCompact(t *testing.T) {
	t.Run("removes stale terminal records", func(t *testing.T) {
		q, path := newTestQueue(t)

		// Enqueue 3 vessels, fail 2, then re-enqueue them.
		for _, id := range []int{1, 2, 3} {
			if _, err := q.Enqueue(testVessel(id)); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
		}
		// Fail vessel 1 and 2 (failed vessels allow re-enqueue).
		for _, id := range []int{1, 2} {
			helperFailVessel(t, q, fmt.Sprintf("issue-%d", id))
		}
		// Re-enqueue vessel 1 and 2.
		for _, id := range []int{1, 2} {
			if _, err := q.Enqueue(testVessel(id)); err != nil {
				t.Fatalf("re-enqueue: %v", err)
			}
		}

		// Before compaction: 5 records (failed-1, failed-2, pending-3, pending-1, pending-2).
		linesBefore := readNonEmptyLines(t, path)
		if len(linesBefore) != 5 {
			t.Fatalf("expected 5 records before compaction, got %d", len(linesBefore))
		}

		removed, err := q.Compact()
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if removed != 2 {
			t.Fatalf("expected 2 removed, got %d", removed)
		}

		// After compaction: 3 records (pending-3, pending-1, pending-2).
		// The old completed records for vessel 1 and 2 are gone because
		// the latest record for each is the re-enqueued pending one.
		linesAfter := readNonEmptyLines(t, path)
		if len(linesAfter) != 3 {
			t.Fatalf("expected 3 records after compaction, got %d", len(linesAfter))
		}
	})

	t.Run("preserves non-terminal records", func(t *testing.T) {
		q, _ := newTestQueue(t)

		// Enqueue vessels in various non-terminal states.
		pending := testVessel(10)
		running := testVessel(11)
		running.State = StateRunning
		waiting := testVessel(12)
		waiting.State = StateWaiting

		for _, v := range []Vessel{pending, running, waiting} {
			if _, err := q.Enqueue(v); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
		}

		removed, err := q.Compact()
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if removed != 0 {
			t.Fatalf("expected 0 removed (all non-terminal), got %d", removed)
		}

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vessels) != 3 {
			t.Fatalf("expected 3 vessels preserved, got %d", len(vessels))
		}
	})

	t.Run("retains latest terminal record per ID", func(t *testing.T) {
		q, _ := newTestQueue(t)

		// Enqueue a vessel, fail it, re-enqueue, fail again.
		if _, err := q.Enqueue(testVessel(20)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		helperFailVessel(t, q, "issue-20")
		if _, err := q.Enqueue(testVessel(20)); err != nil {
			t.Fatalf("re-enqueue: %v", err)
		}
		// Dequeue and fail the re-enqueued vessel.
		if _, err := q.Dequeue(); err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if err := q.Update("issue-20", StateFailed, "boom"); err != nil {
			t.Fatalf("update: %v", err)
		}

		// Before: completed-20 + failed-20 = 2 records.
		vessels, err := q.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vessels) != 2 {
			t.Fatalf("expected 2 records before compaction, got %d", len(vessels))
		}

		removed, err := q.Compact()
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if removed != 1 {
			t.Fatalf("expected 1 removed, got %d", removed)
		}

		// After: only the latest (failed) record remains.
		vessels, err = q.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vessels) != 1 {
			t.Fatalf("expected 1 vessel after compaction, got %d", len(vessels))
		}
		if vessels[0].State != StateFailed {
			t.Fatalf("expected latest terminal record (failed), got %s", vessels[0].State)
		}
	})

	t.Run("empty queue is a no-op", func(t *testing.T) {
		q, _ := newTestQueue(t)

		removed, err := q.Compact()
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if removed != 0 {
			t.Fatalf("expected 0 removed on empty queue, got %d", removed)
		}
	})

	t.Run("single terminal record is preserved", func(t *testing.T) {
		q, _ := newTestQueue(t)
		if _, err := q.Enqueue(testVessel(30)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		helperCompleteVessel(t, q, "issue-30")

		removed, err := q.Compact()
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if removed != 0 {
			t.Fatalf("expected 0 removed (only one record per ID), got %d", removed)
		}

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vessels) != 1 {
			t.Fatalf("expected 1 vessel preserved, got %d", len(vessels))
		}
	})
}

func TestCompactDryRun(t *testing.T) {
	q, path := newTestQueue(t)

	// Enqueue, fail, and re-enqueue a vessel (failed vessels allow re-enqueue).
	if _, err := q.Enqueue(testVessel(1)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	helperFailVessel(t, q, "issue-1")
	if _, err := q.Enqueue(testVessel(1)); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}

	linesBefore := readNonEmptyLines(t, path)

	removable, err := q.CompactDryRun()
	if err != nil {
		t.Fatalf("compact dry run: %v", err)
	}
	if removable != 1 {
		t.Fatalf("expected 1 removable, got %d", removable)
	}

	// File should be unchanged.
	linesAfter := readNonEmptyLines(t, path)
	if len(linesAfter) != len(linesBefore) {
		t.Fatalf("dry run modified the file: before=%d lines, after=%d lines", len(linesBefore), len(linesAfter))
	}
}
