package dtu

import (
	"testing"
	"time"
)

func sampleEventState() *State {
	return &State{
		UniverseID: "universe-1",
		Metadata:   ManifestMetadata{Name: "sample-universe"},
		Clock:      ClockState{Now: "2026-01-02T03:04:05Z"},
		Repositories: []Repository{{
			Owner:         "nicholls-inc",
			Name:          "xylem",
			DefaultBranch: "main",
			Labels:        []Label{{Name: "bug"}, {Name: "ready"}},
			Issues: []Issue{{
				Number:   1,
				Title:    "Issue",
				Labels:   []string{"bug"},
				Comments: []Comment{{ID: 1, Body: "seeded"}},
			}},
			PullRequests: []PullRequest{{
				Number:     2,
				Title:      "PR",
				BaseBranch: "main",
				HeadBranch: "feature",
				HeadSHA:    "abc123",
				Comments:   []Comment{{ID: 2, Body: "review me"}},
				Reviews:    []Review{{ID: 1, State: ReviewStateApproved}},
				Checks:     []Check{{ID: 1, Name: "ci", State: CheckStateSuccess}},
			}},
		}},
		Counters: Counters{NextCommentID: 3, NextReviewID: 2, NextCheckID: 2},
	}
}

func TestRecordEventAppendsAndReadsJSONL(t *testing.T) {
	t.Parallel()

	recordedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	store, err := NewStoreWithClock(t.TempDir(), "universe-1", NewFixedClock(recordedAt))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	invocation := &Event{
		Kind: EventKindShimInvocation,
		Shim: &ShimEvent{
			Command:  "gh",
			Args:     []string{"issue", "view", "1"},
			Provider: ProviderClaude,
			Phase:    "analyze",
			Attempt:  2,
			Prompt:   "final prompt text",
		},
	}
	exitCode := 0
	result := &Event{
		Kind: EventKindShimResult,
		Shim: &ShimEvent{
			Command:  "gh",
			Args:     []string{"issue", "view", "1"},
			Provider: ProviderClaude,
			ExitCode: &exitCode,
			Duration: "250ms",
		},
	}
	if err := store.RecordEvent(invocation); err != nil {
		t.Fatalf("RecordEvent(invocation) error = %v", err)
	}
	if err := store.RecordEvent(result); err != nil {
		t.Fatalf("RecordEvent(result) error = %v", err)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Kind != EventKindShimInvocation {
		t.Fatalf("events[0].Kind = %q, want %q", events[0].Kind, EventKindShimInvocation)
	}
	if events[1].Kind != EventKindShimResult {
		t.Fatalf("events[1].Kind = %q, want %q", events[1].Kind, EventKindShimResult)
	}
	for i, event := range events {
		if event.UniverseID != "universe-1" {
			t.Fatalf("events[%d].UniverseID = %q, want universe-1", i, event.UniverseID)
		}
		if got, want := event.RecordedAt, recordedAt.Format(time.RFC3339Nano); got != want {
			t.Fatalf("events[%d].RecordedAt = %q, want %q", i, got, want)
		}
	}
	if events[1].Shim == nil || events[1].Shim.ExitCode == nil || *events[1].Shim.ExitCode != 0 {
		t.Fatalf("events[1].Shim.ExitCode = %#v, want 0", events[1].Shim)
	}
	if events[0].Shim == nil || events[0].Shim.Attempt != 2 || events[0].Shim.Prompt != "final prompt text" {
		t.Fatalf("events[0].Shim = %#v, want attempt and prompt preserved", events[0].Shim)
	}
}

func TestStoreUpdateRecordsStateMutationEvent(t *testing.T) {
	t.Parallel()

	state := sampleEventState()
	store, err := NewStore(t.TempDir(), state.UniverseID)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := store.Update(func(state *State) error {
		repo := state.Repository("nicholls-inc", "xylem")
		if repo == nil {
			t.Fatal("Repository() = nil, want repo")
		}
		issue := repo.IssueByNumber(1)
		if issue == nil {
			t.Fatal("IssueByNumber() = nil, want issue")
		}
		issue.Labels = append(issue.Labels, "ready")
		issue.Comments = append(issue.Comments, Comment{Body: "follow-up"})
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Kind != EventKindStateSaved {
		t.Fatalf("events[0].Kind = %q, want %q", events[0].Kind, EventKindStateSaved)
	}
	updated := events[1]
	if updated.Kind != EventKindStateUpdated {
		t.Fatalf("events[1].Kind = %q, want %q", updated.Kind, EventKindStateUpdated)
	}
	if updated.State == nil {
		t.Fatal("events[1].State = nil, want state payload")
	}
	if updated.State.Operation != StateOperationUpdate {
		t.Fatalf("events[1].State.Operation = %q, want %q", updated.State.Operation, StateOperationUpdate)
	}
	if !updated.State.Changed {
		t.Fatal("events[1].State.Changed = false, want true")
	}
	if updated.State.PreviousHash == "" {
		t.Fatal("events[1].State.PreviousHash is empty")
	}
	if updated.State.PreviousHash == updated.State.Hash {
		t.Fatal("events[1].State hash did not change")
	}
	if updated.State.Summary.CommentCount != 3 {
		t.Fatalf("events[1].State.Summary.CommentCount = %d, want 3", updated.State.Summary.CommentCount)
	}
	if len(updated.State.Summary.Repositories) != 1 || updated.State.Summary.Repositories[0] != "nicholls-inc/xylem" {
		t.Fatalf("events[1].State.Summary.Repositories = %#v, want [nicholls-inc/xylem]", updated.State.Summary.Repositories)
	}
	if updated.State.Snapshot == nil {
		t.Fatal("events[1].State.Snapshot = nil, want snapshot")
	}
	repo := updated.State.Snapshot.Repository("nicholls-inc", "xylem")
	if repo == nil {
		t.Fatal("snapshot Repository() = nil, want repo")
	}
	issue := repo.IssueByNumber(1)
	if issue == nil {
		t.Fatal("snapshot IssueByNumber() = nil, want issue")
	}
	if len(issue.Comments) != 2 {
		t.Fatalf("len(snapshot issue comments) = %d, want 2", len(issue.Comments))
	}
	if got := issue.Comments[1].ID; got != 3 {
		t.Fatalf("snapshot issue comment ID = %d, want 3", got)
	}
	if got := issue.Labels[1]; got != "ready" {
		t.Fatalf("snapshot issue labels = %#v, want ready appended", issue.Labels)
	}
}

func TestDefaultEventLogPathMatchesStorePath(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	store, err := NewStore(stateDir, "universe-1")
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	path, err := DefaultEventLogPath(stateDir, "universe-1")
	if err != nil {
		t.Fatalf("DefaultEventLogPath() error = %v", err)
	}
	if path != store.EventLogPath() {
		t.Fatalf("DefaultEventLogPath() = %q, want %q", path, store.EventLogPath())
	}
}
