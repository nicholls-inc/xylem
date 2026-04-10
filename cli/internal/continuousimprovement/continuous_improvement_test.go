package continuousimprovement

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmoke_S1_SelectAndPersistRotatesRepoSpecificFocusAndPersistsState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	selectionPath := filepath.Join(dir, "selection.json")
	previous := HistoryEntry{
		SelectedAt: "2026-04-09T00:00:00Z",
		FocusKey:   repoSpecificFocuses[0].Key,
		FocusTitle: repoSpecificFocuses[0].Title,
		Group:      GroupRepoSpecific,
	}
	require.NoError(t, SaveState(statePath, &State{
		Version: StateVersion,
		Counts: map[string]int{
			repoSpecificFocuses[0].Key: 1,
		},
		History: []HistoryEntry{previous},
	}))

	selection, err := SelectAndPersist(Options{
		Repo:          "owner/repo",
		Now:           time.Date(2026, time.April, 10, 3, 0, 0, 0, time.UTC),
		StatePath:     statePath,
		SelectionPath: selectionPath,
	})
	require.NoError(t, err)

	require.NotNil(t, selection)
	assert.Equal(t, GroupRepoSpecific, selection.RotationGroup)
	assert.Equal(t, "owner/repo", selection.Repo)
	assert.NotEmpty(t, selection.Focus.Key)
	assert.NotEqual(t, previous.FocusKey, selection.Focus.Key)
	assert.Contains(t, selection.Reason, "repo-specific")
	require.Len(t, selection.RecentHistory, 1)
	assert.Equal(t, previous.FocusKey, selection.RecentHistory[0].FocusKey)
	assert.Equal(t, statePath, selection.StatePath)
	assert.Equal(t, selectionPath, selection.SelectionPath)

	loadedState, err := LoadState(statePath)
	require.NoError(t, err)
	assert.Equal(t, 1, loadedState.Runs)
	assert.Equal(t, 1, loadedState.Counts[previous.FocusKey])
	assert.Equal(t, 1, loadedState.Counts[selection.Focus.Key])
	require.Len(t, loadedState.History, 2)
	assert.Equal(t, previous.FocusKey, loadedState.History[0].FocusKey)
	assert.Equal(t, selection.Focus.Key, loadedState.History[1].FocusKey)
	assert.Equal(t, selection.GeneratedAt, loadedState.History[1].SelectedAt)

	selectionData, err := os.ReadFile(selectionPath)
	require.NoError(t, err)
	var persisted Selection
	require.NoError(t, json.Unmarshal(selectionData, &persisted))
	assert.Equal(t, selection.Focus.Key, persisted.Focus.Key)
	assert.Equal(t, selection.Fingerprint, persisted.Fingerprint)
	assert.Equal(t, selectionPath, persisted.SelectionPath)
}

func TestGenerateSelectionUsesWeightedGroups(t *testing.T) {
	baseState := defaultState()
	baseState.History = append(baseState.History, HistoryEntry{
		SelectedAt: "2026-04-01T00:00:00Z",
		FocusKey:   repoSpecificFocuses[0].Key,
		FocusTitle: repoSpecificFocuses[0].Title,
		Group:      GroupRepoSpecific,
	})
	baseState.Counts[repoSpecificFocuses[0].Key] = 1

	repoSelection, _, err := GenerateSelection(baseState, Options{
		Repo: "owner/repo",
		Now:  time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("GenerateSelection(repo) error = %v", err)
	}
	if repoSelection.RotationGroup != GroupRepoSpecific {
		t.Fatalf("RotationGroup = %q, want %q", repoSelection.RotationGroup, GroupRepoSpecific)
	}

	standardState := cloneState(baseState)
	standardState.Runs = 6
	standardSelection, _, err := GenerateSelection(standardState, Options{
		Repo: "owner/repo",
		Now:  time.Date(2026, time.April, 10, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("GenerateSelection(standard) error = %v", err)
	}
	if standardSelection.RotationGroup != GroupStandard {
		t.Fatalf("RotationGroup = %q, want %q", standardSelection.RotationGroup, GroupStandard)
	}

	revisitState := cloneState(baseState)
	revisitState.Runs = 9
	revisitSelection, _, err := GenerateSelection(revisitState, Options{
		Repo: "owner/repo",
		Now:  time.Date(2026, time.April, 10, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("GenerateSelection(revisit) error = %v", err)
	}
	if revisitSelection.RotationGroup != GroupRevisit {
		t.Fatalf("RotationGroup = %q, want %q", revisitSelection.RotationGroup, GroupRevisit)
	}
}

func TestGenerateSelectionAvoidsImmediateRepeatWhenAlternativesExist(t *testing.T) {
	state := defaultState()
	state.History = append(state.History, HistoryEntry{
		SelectedAt: "2026-04-01T00:00:00Z",
		FocusKey:   repoSpecificFocuses[0].Key,
		FocusTitle: repoSpecificFocuses[0].Title,
		Group:      GroupRepoSpecific,
	})
	state.Counts[repoSpecificFocuses[0].Key] = 1

	selection, _, err := GenerateSelection(state, Options{
		Repo: "owner/repo",
		Now:  time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("GenerateSelection() error = %v", err)
	}
	if selection.Focus.Key == repoSpecificFocuses[0].Key {
		t.Fatalf("Focus.Key = %q, want a different repo-specific focus when alternatives exist", selection.Focus.Key)
	}
}

func TestSelectAndPersistWritesStateAndSelection(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	selectionPath := filepath.Join(dir, "selection.json")

	selection, err := SelectAndPersist(Options{
		Repo:          "owner/repo",
		Now:           time.Date(2026, time.April, 10, 3, 0, 0, 0, time.UTC),
		StatePath:     statePath,
		SelectionPath: selectionPath,
	})
	if err != nil {
		t.Fatalf("SelectAndPersist() error = %v", err)
	}
	if selection.Focus.Key == "" {
		t.Fatal("selection focus key = empty, want non-empty")
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	if _, err := os.Stat(selectionPath); err != nil {
		t.Fatalf("selection file missing: %v", err)
	}
	loaded, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if loaded.Runs != 1 {
		t.Fatalf("Runs = %d, want 1", loaded.Runs)
	}
	if loaded.Counts[selection.Focus.Key] != 1 {
		t.Fatalf("Counts[%q] = %d, want 1", selection.Focus.Key, loaded.Counts[selection.Focus.Key])
	}

	selectionData, err := os.ReadFile(selectionPath)
	if err != nil {
		t.Fatalf("ReadFile(selectionPath) error = %v", err)
	}
	var persisted Selection
	if err := json.Unmarshal(selectionData, &persisted); err != nil {
		t.Fatalf("Unmarshal(selectionPath) error = %v", err)
	}
	if persisted.Focus.Key != selection.Focus.Key {
		t.Fatalf("persisted focus key = %q, want %q", persisted.Focus.Key, selection.Focus.Key)
	}
	if persisted.StatePath != statePath {
		t.Fatalf("persisted state path = %q, want %q", persisted.StatePath, statePath)
	}
	if persisted.SelectionPath != selectionPath {
		t.Fatalf("persisted selection path = %q, want %q", persisted.SelectionPath, selectionPath)
	}
}

func TestLoadStateMissingFileReturnsDefault(t *testing.T) {
	state, err := LoadState(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if state.Version != StateVersion {
		t.Fatalf("Version = %d, want %d", state.Version, StateVersion)
	}
	if state.Runs != 0 {
		t.Fatalf("Runs = %d, want 0", state.Runs)
	}
}

func TestRenderMarkdownIncludesSelectionSummary(t *testing.T) {
	selection := &Selection{
		Version:       StateVersion,
		Repo:          "owner/repo",
		GeneratedAt:   "2026-04-10T00:00:00Z",
		RotationSlot:  0,
		RotationGroup: GroupRepoSpecific,
		Focus:         repoSpecificFocuses[0],
		Reason:        "Because it is the next repo-specific slot.",
		Fingerprint:   "deadbeefcafebabe",
	}

	rendered := RenderMarkdown(selection)
	if !strings.Contains(rendered, "# Continuous Improvement Focus") {
		t.Fatalf("rendered markdown missing heading: %q", rendered)
	}
	if !strings.Contains(rendered, repoSpecificFocuses[0].Title) {
		t.Fatalf("rendered markdown missing focus title: %q", rendered)
	}
	if !strings.Contains(rendered, "deadbeefcafebabe") {
		t.Fatalf("rendered markdown missing fingerprint: %q", rendered)
	}
}
