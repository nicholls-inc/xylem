package dtu

import (
	"strings"
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
			Command:     "gh",
			Args:        []string{"issue", "view", "1"},
			Provider:    ProviderClaude,
			Phase:       "analyze",
			Attempt:     2,
			BinaryPath:  "/usr/local/bin/gh",
			BinaryName:  "gh",
			WorkingDir:  "/repo/worktree",
			StdinDigest: "stdin-digest",
			Prompt:      "final prompt text",
		},
	}
	exitCode := 0
	result := &Event{
		Kind: EventKindShimResult,
		Shim: &ShimEvent{
			Command:     "gh",
			Args:        []string{"issue", "view", "1"},
			Provider:    ProviderClaude,
			BinaryPath:  "/usr/local/bin/gh",
			BinaryName:  "gh",
			WorkingDir:  "/repo/worktree",
			ExitCode:    &exitCode,
			Duration:    "250ms",
			StdoutBytes: 42,
			StderrBytes: 7,
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
	if got, want := events[0].Shim.BinaryName, "gh"; got != want {
		t.Fatalf("events[0].Shim.BinaryName = %q, want %q", got, want)
	}
	if got, want := events[0].Shim.WorkingDir, "/repo/worktree"; got != want {
		t.Fatalf("events[0].Shim.WorkingDir = %q, want %q", got, want)
	}
	if got, want := events[0].Shim.StdinDigest, "stdin-digest"; got != want {
		t.Fatalf("events[0].Shim.StdinDigest = %q, want %q", got, want)
	}
	if got, want := events[1].Shim.StdoutBytes, 42; got != want {
		t.Fatalf("events[1].Shim.StdoutBytes = %d, want %d", got, want)
	}
	if got, want := events[1].Shim.StderrBytes, 7; got != want {
		t.Fatalf("events[1].Shim.StderrBytes = %d, want %d", got, want)
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

func TestStoreReplayReturnsStateSnapshots(t *testing.T) {
	t.Parallel()

	state := sampleEventState()
	store, err := NewStore(t.TempDir(), state.UniverseID)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.RecordEvent(&Event{
		Kind: EventKindShimInvocation,
		Shim: &ShimEvent{
			Command: "gh",
			Args:    []string{"issue", "view", "1"},
		},
	}); err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
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

	snapshots, err := store.Replay()
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("len(snapshots) = %d, want 2", len(snapshots))
	}
	if got, want := snapshots[0].EventIndex, 0; got != want {
		t.Fatalf("snapshots[0].EventIndex = %d, want %d", got, want)
	}
	if got, want := snapshots[0].Operation, StateOperationSave; got != want {
		t.Fatalf("snapshots[0].Operation = %q, want %q", got, want)
	}
	if snapshots[0].Hash == "" {
		t.Fatal("snapshots[0].Hash is empty")
	}
	repo := snapshots[0].State.Repository("nicholls-inc", "xylem")
	if repo == nil {
		t.Fatal("snapshots[0].State.Repository() = nil, want repo")
	}
	issue := repo.IssueByNumber(1)
	if issue == nil {
		t.Fatal("snapshots[0].State.IssueByNumber() = nil, want issue")
	}
	if got, want := len(issue.Labels), 1; got != want {
		t.Fatalf("len(snapshots[0] issue labels) = %d, want %d", got, want)
	}

	if got, want := snapshots[1].EventIndex, 2; got != want {
		t.Fatalf("snapshots[1].EventIndex = %d, want %d", got, want)
	}
	if got, want := snapshots[1].Operation, StateOperationUpdate; got != want {
		t.Fatalf("snapshots[1].Operation = %q, want %q", got, want)
	}
	repo = snapshots[1].State.Repository("nicholls-inc", "xylem")
	if repo == nil {
		t.Fatal("snapshots[1].State.Repository() = nil, want repo")
	}
	issue = repo.IssueByNumber(1)
	if issue == nil {
		t.Fatal("snapshots[1].State.IssueByNumber() = nil, want issue")
	}
	if got, want := len(issue.Comments), 2; got != want {
		t.Fatalf("len(snapshots[1] issue comments) = %d, want %d", got, want)
	}
	if got, want := issue.Labels[1], "ready"; got != want {
		t.Fatalf("snapshots[1] issue labels[1] = %q, want %q", got, want)
	}
}

func TestStoreResetRestoresReplaySnapshot(t *testing.T) {
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

	snapshots, err := store.Replay()
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("len(snapshots) = %d, want 2", len(snapshots))
	}
	if err := store.Reset(snapshots[0]); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	repo := loaded.Repository("nicholls-inc", "xylem")
	if repo == nil {
		t.Fatal("loaded.Repository() = nil, want repo")
	}
	issue := repo.IssueByNumber(1)
	if issue == nil {
		t.Fatal("loaded.IssueByNumber() = nil, want issue")
	}
	if got, want := len(issue.Labels), 1; got != want {
		t.Fatalf("len(loaded issue labels) = %d, want %d", got, want)
	}
	if got, want := len(issue.Comments), 1; got != want {
		t.Fatalf("len(loaded issue comments) = %d, want %d", got, want)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
	reset := events[2]
	if got, want := reset.Kind, EventKindStateUpdated; got != want {
		t.Fatalf("events[2].Kind = %q, want %q", got, want)
	}
	if reset.State == nil {
		t.Fatal("events[2].State = nil, want payload")
	}
	if got, want := reset.State.Operation, StateOperationReset; got != want {
		t.Fatalf("events[2].State.Operation = %q, want %q", got, want)
	}
	if got, want := reset.State.PreviousHash, snapshots[1].Hash; got != want {
		t.Fatalf("events[2].State.PreviousHash = %q, want %q", got, want)
	}
	if got, want := reset.State.Hash, snapshots[0].Hash; got != want {
		t.Fatalf("events[2].State.Hash = %q, want %q", got, want)
	}
}

func TestStoreResetRejectsModifiedReplaySnapshot(t *testing.T) {
	t.Parallel()

	state := sampleEventState()
	store, err := NewStore(t.TempDir(), state.UniverseID)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	snapshots, err := store.Replay()
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots) = %d, want 1", len(snapshots))
	}

	snapshot := snapshots[0]
	snapshot.State.Metadata.Name = "tampered"

	err = store.Reset(snapshot)
	if err == nil {
		t.Fatal("Reset() error = nil, want hash mismatch")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("Reset() error = %v, want hash mismatch", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := loaded.Metadata.Name, "sample-universe"; got != want {
		t.Fatalf("loaded.Metadata.Name = %q, want %q", got, want)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
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

func TestStoreRecordObservationAppendsSchedulerEvents(t *testing.T) {
	t.Parallel()

	state := sampleEventState()
	state.ScheduledMutations = []ScheduledMutation{{
		Name: "label-ready",
		Trigger: MutationTrigger{
			Command:    ShimCommandGH,
			ArgsPrefix: []string{"issue", "view", "1"},
		},
		Operations: []MutationOperation{{
			Type:   MutationOperationIssueAddLabel,
			Repo:   "nicholls-inc/xylem",
			Number: 1,
			Label:  "ready",
		}},
	}}
	store, err := NewStoreWithClock(t.TempDir(), state.UniverseID, NewFixedClock(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)))
	if err != nil {
		t.Fatalf("NewStoreWithClock() error = %v", err)
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	result, err := store.RecordObservation(ShimInvocation{
		Command: ShimCommandGH,
		Args:    []string{"issue", "view", "1", "--repo", "nicholls-inc/xylem", "--json", "labels"},
	})
	if err != nil {
		t.Fatalf("RecordObservation() error = %v", err)
	}
	if result == nil {
		t.Fatal("RecordObservation() = nil, want result")
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("len(events) = %d, want 4", len(events))
	}
	if got, want := events[1].Kind, EventKindSchedulerObserved; got != want {
		t.Fatalf("events[1].Kind = %q, want %q", got, want)
	}
	if got, want := events[2].Kind, EventKindSchedulerMutationApplied; got != want {
		t.Fatalf("events[2].Kind = %q, want %q", got, want)
	}
	if got, want := events[3].Kind, EventKindStateUpdated; got != want {
		t.Fatalf("events[3].Kind = %q, want %q", got, want)
	}

	observed := events[1].Scheduler
	if observed == nil {
		t.Fatal("events[1].Scheduler = nil, want payload")
	}
	if got, want := observed.ObservationKey, result.ObservationKey; got != want {
		t.Fatalf("ObservationKey = %q, want %q", got, want)
	}
	if got, want := observed.ObservationCount, 1; got != want {
		t.Fatalf("ObservationCount = %d, want %d", got, want)
	}
	if len(observed.MatchedMutations) != 1 || observed.MatchedMutations[0] != "label-ready" {
		t.Fatalf("MatchedMutations = %#v, want [label-ready]", observed.MatchedMutations)
	}
	if len(observed.AppliedMutations) != 1 || observed.AppliedMutations[0] != "label-ready" {
		t.Fatalf("AppliedMutations = %#v, want [label-ready]", observed.AppliedMutations)
	}

	applied := events[2].Scheduler
	if applied == nil {
		t.Fatal("events[2].Scheduler = nil, want payload")
	}
	if got, want := applied.MutationName, "label-ready"; got != want {
		t.Fatalf("MutationName = %q, want %q", got, want)
	}
	if got, want := applied.ObservationKey, result.ObservationKey; got != want {
		t.Fatalf("mutation ObservationKey = %q, want %q", got, want)
	}
	if got, want := applied.AppliedAt, "2026-01-02T03:04:05Z"; got != want {
		t.Fatalf("AppliedAt = %q, want %q", got, want)
	}
	if len(applied.Operations) != 1 || applied.Operations[0].Type != MutationOperationIssueAddLabel {
		t.Fatalf("Operations = %#v, want one issue_add_label", applied.Operations)
	}
}
