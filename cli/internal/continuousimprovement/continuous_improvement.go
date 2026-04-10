package continuousimprovement

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	StateVersion    = 1
	MaxHistory      = 40
	recentRunWindow = 5
	rotationSlots   = 10
)

type Group string

const (
	GroupRepoSpecific Group = "repo-specific"
	GroupStandard     Group = "standard"
	GroupRevisit      Group = "revisit"
)

type Focus struct {
	Key            string   `json:"key"`
	Title          string   `json:"title"`
	Group          Group    `json:"group"`
	Summary        string   `json:"summary"`
	CandidatePaths []string `json:"candidate_paths,omitempty"`
}

type HistoryEntry struct {
	SelectedAt     string   `json:"selected_at"`
	FocusKey       string   `json:"focus_key"`
	FocusTitle     string   `json:"focus_title"`
	Group          Group    `json:"group"`
	RotationSlot   int      `json:"rotation_slot"`
	CandidatePaths []string `json:"candidate_paths,omitempty"`
}

type State struct {
	Version int            `json:"version"`
	Runs    int            `json:"runs"`
	Counts  map[string]int `json:"counts,omitempty"`
	History []HistoryEntry `json:"history,omitempty"`
}

type Selection struct {
	Version       int            `json:"version"`
	Repo          string         `json:"repo,omitempty"`
	GeneratedAt   string         `json:"generated_at"`
	RotationSlot  int            `json:"rotation_slot"`
	RotationGroup Group          `json:"rotation_group"`
	Focus         Focus          `json:"focus"`
	Reason        string         `json:"reason"`
	RecentHistory []HistoryEntry `json:"recent_history,omitempty"`
	StatePath     string         `json:"state_path,omitempty"`
	SelectionPath string         `json:"selection_path,omitempty"`
	Fingerprint   string         `json:"fingerprint,omitempty"`
}

type Options struct {
	Repo          string
	Now           time.Time
	StatePath     string
	SelectionPath string
}

var repoSpecificFocuses = []Focus{
	{
		Key:     "workflow-prompts-and-gates",
		Title:   "Workflow prompts and gates",
		Group:   GroupRepoSpecific,
		Summary: "Tighten self-hosted workflow prompts, command phases, and gate behavior so recurring automation stays reliable.",
		CandidatePaths: []string{
			".xylem/workflows",
			".xylem/prompts",
			"cli/internal/workflow",
			"cli/internal/phase",
			"cli/internal/runner",
		},
	},
	{
		Key:     "scheduled-automation-and-daemon-ops",
		Title:   "Scheduled automation and daemon operations",
		Group:   GroupRepoSpecific,
		Summary: "Improve the recurring automation loop around scheduled sources, daemon scans, built-in maintenance workflows, and queue orchestration.",
		CandidatePaths: []string{
			".xylem.yml",
			"cli/cmd/xylem",
			"cli/internal/source",
			"cli/internal/scanner",
			"cli/internal/runner",
		},
	},
	{
		Key:     "queue-runner-worktree-safety",
		Title:   "Queue, runner, and worktree safety",
		Group:   GroupRepoSpecific,
		Summary: "Strengthen the vessel state machine, worktree lifecycle, and concurrency boundaries used by the self-hosted control plane.",
		CandidatePaths: []string{
			"cli/internal/queue",
			"cli/internal/runner",
			"cli/internal/worktree",
			"cli/internal/gate",
		},
	},
	{
		Key:     "self-hosting-config-and-docs",
		Title:   "Self-hosting config and docs alignment",
		Group:   GroupRepoSpecific,
		Summary: "Reduce drift between the checked-in .xylem assets, profile overlays, init scaffolding, and self-hosting documentation.",
		CandidatePaths: []string{
			".xylem.yml",
			".xylem",
			"cli/internal/profiles",
			"docs/configuration.md",
			"docs/workflows.md",
		},
	},
}

var standardFocuses = []Focus{
	{
		Key:     "dependency-hygiene",
		Title:   "Dependency hygiene",
		Group:   GroupStandard,
		Summary: "Look for stale or unnecessary dependencies, narrow version constraints, and dead imports that can be removed safely.",
		CandidatePaths: []string{
			"cli/go.mod",
			"cli/go.sum",
			"cli/internal",
			"cli/cmd",
		},
	},
	{
		Key:     "type-safety",
		Title:   "Type safety",
		Group:   GroupStandard,
		Summary: "Replace loosely typed plumbing with clearer concrete types, stronger validation, and safer helper boundaries.",
		CandidatePaths: []string{
			"cli/internal",
			"cli/cmd",
		},
	},
	{
		Key:     "code-quality",
		Title:   "Code quality",
		Group:   GroupStandard,
		Summary: "Pay down small correctness or maintainability issues that make future changes harder than they need to be.",
		CandidatePaths: []string{
			"cli/internal",
			"cli/cmd",
			"docs",
		},
	},
	{
		Key:     "test-coverage",
		Title:   "Test coverage",
		Group:   GroupStandard,
		Summary: "Tighten unit and property coverage around existing behavior gaps, especially for error handling and scheduling edges.",
		CandidatePaths: []string{
			"cli/internal",
			"cli/cmd",
		},
	},
	{
		Key:     "documentation",
		Title:   "Documentation",
		Group:   GroupStandard,
		Summary: "Close code-to-doc drift so self-hosting operators and contributors can trust the documented behavior.",
		CandidatePaths: []string{
			"README.md",
			"docs",
			".xylem.yml",
		},
	},
	{
		Key:     "performance",
		Title:   "Performance",
		Group:   GroupStandard,
		Summary: "Trim unnecessary work in scans, queue traversals, workflow execution, or repeated file-system operations.",
		CandidatePaths: []string{
			"cli/internal/runner",
			"cli/internal/source",
			"cli/internal/queue",
		},
	},
	{
		Key:     "security",
		Title:   "Security",
		Group:   GroupStandard,
		Summary: "Harden command execution, path handling, or config-driven behavior that crosses trust boundaries in the harness.",
		CandidatePaths: []string{
			"cli/internal/runner",
			"cli/internal/config",
			"cli/internal/intermediary",
			"cli/internal/worktree",
		},
	},
}

func SelectAndPersist(opts Options) (*Selection, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}

	state, err := LoadState(opts.StatePath)
	if err != nil {
		return nil, err
	}
	selection, nextState, err := GenerateSelection(state, opts)
	if err != nil {
		return nil, err
	}
	if err := SaveState(opts.StatePath, nextState); err != nil {
		return nil, err
	}
	selection.StatePath = opts.StatePath
	selection.SelectionPath = opts.SelectionPath
	if strings.TrimSpace(opts.SelectionPath) != "" {
		if err := SaveSelection(opts.SelectionPath, selection); err != nil {
			return nil, err
		}
	}
	return selection, nil
}

func GenerateSelection(state *State, opts Options) (*Selection, *State, error) {
	if state == nil {
		state = defaultState()
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	nextState := cloneState(state)
	if nextState.Version == 0 {
		nextState.Version = StateVersion
	}
	if nextState.Counts == nil {
		nextState.Counts = make(map[string]int)
	}

	slot := nextState.Runs % rotationSlots
	targetGroup := groupForSlot(slot)
	focus, actualGroup, reason := chooseFocus(nextState, targetGroup, slot)

	entry := HistoryEntry{
		SelectedAt:     now.Format(time.RFC3339),
		FocusKey:       focus.Key,
		FocusTitle:     focus.Title,
		Group:          actualGroup,
		RotationSlot:   slot,
		CandidatePaths: append([]string(nil), focus.CandidatePaths...),
	}
	nextState.Runs++
	nextState.Counts[focus.Key]++
	nextState.History = append(nextState.History, entry)
	if len(nextState.History) > MaxHistory {
		nextState.History = append([]HistoryEntry(nil), nextState.History[len(nextState.History)-MaxHistory:]...)
	}

	selection := &Selection{
		Version:       StateVersion,
		Repo:          strings.TrimSpace(opts.Repo),
		GeneratedAt:   now.Format(time.RFC3339),
		RotationSlot:  slot,
		RotationGroup: actualGroup,
		Focus:         focus,
		Reason:        reason,
		RecentHistory: recentHistory(state.History),
		Fingerprint:   selectionFingerprint(opts.Repo, slot, focus.Key, now),
	}
	return selection, nextState, nil
}

func LoadState(path string) (*State, error) {
	if strings.TrimSpace(path) == "" {
		return defaultState(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultState(), nil
		}
		return nil, fmt.Errorf("read continuous improvement state %q: %w", path, err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse continuous improvement state %q: %w", path, err)
	}
	if state.Version == 0 {
		state.Version = StateVersion
	}
	if state.Counts == nil {
		state.Counts = make(map[string]int)
	}
	if state.History == nil {
		state.History = []HistoryEntry{}
	}
	return &state, nil
}

func SaveState(path string, state *State) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if state == nil {
		state = defaultState()
	}
	return writeJSON(path, state, "continuous improvement state")
}

func SaveSelection(path string, selection *Selection) error {
	if strings.TrimSpace(path) == "" || selection == nil {
		return nil
	}
	return writeJSON(path, selection, "continuous improvement selection")
}

func RenderMarkdown(selection *Selection) string {
	if selection == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Continuous Improvement Focus\n\n")
	if strings.TrimSpace(selection.Repo) != "" {
		fmt.Fprintf(&b, "- **Repo:** `%s`\n", selection.Repo)
	}
	fmt.Fprintf(&b, "- **Rotation slot:** %d/10\n", selection.RotationSlot+1)
	fmt.Fprintf(&b, "- **Rotation group:** %s\n", selection.RotationGroup)
	fmt.Fprintf(&b, "- **Focus area:** %s (`%s`)\n", selection.Focus.Title, selection.Focus.Key)
	fmt.Fprintf(&b, "- **Generated at:** %s\n", selection.GeneratedAt)
	fmt.Fprintf(&b, "- **Fingerprint:** `%s`\n", selection.Fingerprint)
	b.WriteString("\n## Why this focus now\n")
	b.WriteString(selection.Reason)
	b.WriteString("\n")
	if len(selection.Focus.CandidatePaths) > 0 {
		b.WriteString("\n## Candidate paths\n")
		for _, p := range selection.Focus.CandidatePaths {
			fmt.Fprintf(&b, "- `%s`\n", p)
		}
	}
	if len(selection.RecentHistory) > 0 {
		b.WriteString("\n## Recent focus history\n")
		for _, entry := range selection.RecentHistory {
			fmt.Fprintf(&b, "- %s — %s (%s)\n", entry.SelectedAt, entry.FocusTitle, entry.Group)
		}
	}
	b.WriteString("\n## Operator constraints\n")
	b.WriteString("- Prefer one small, mergeable improvement rather than a batch of unrelated edits.\n")
	b.WriteString("- Bias toward repo-specific harness concerns before generic cleanup when multiple options look similar.\n")
	b.WriteString("- Avoid immediately repeating the most recent focus area unless the revisit slot explicitly selected it.\n")
	return b.String()
}

func KnownFocuses() []Focus {
	out := make([]Focus, 0, len(repoSpecificFocuses)+len(standardFocuses))
	out = append(out, repoSpecificFocuses...)
	out = append(out, standardFocuses...)
	return out
}

func chooseFocus(state *State, targetGroup Group, slot int) (Focus, Group, string) {
	switch targetGroup {
	case GroupRepoSpecific:
		focus := rankedFocus(repoSpecificFocuses, state)
		return focus, GroupRepoSpecific, fmt.Sprintf("Weighted slot %d targets repo-specific harness work (60%% of runs). %s", slot+1, rankedReason(state, focus))
	case GroupStandard:
		focus := rankedFocus(standardFocuses, state)
		return focus, GroupStandard, fmt.Sprintf("Weighted slot %d targets a standard improvement category (30%% of runs). %s", slot+1, rankedReason(state, focus))
	default:
		revisitPool := revisitCandidates(state)
		if len(revisitPool) > 0 {
			focus := rankedFocus(revisitPool, state)
			return focus, GroupRevisit, fmt.Sprintf("Weighted slot %d is reserved for revisits (10%% of runs). Revisiting an older focus keeps long-lived concerns from going stale. %s", slot+1, rankedReason(state, focus))
		}
		focus := rankedFocus(repoSpecificFocuses, state)
		return focus, GroupRepoSpecific, fmt.Sprintf("Weighted slot %d would revisit a prior focus, but the history is empty, so it falls back to repo-specific work. %s", slot+1, rankedReason(state, focus))
	}
}

func rankedReason(state *State, focus Focus) string {
	count := 0
	if state != nil && state.Counts != nil {
		count = state.Counts[focus.Key]
	}
	lastSeen := "never selected before"
	if idx := lastSelectedIndex(state, focus.Key); idx >= 0 && idx < len(state.History) {
		lastSeen = "last selected at " + state.History[idx].SelectedAt
	}
	return fmt.Sprintf("%q has the lightest recent coverage (%d prior selections; %s).", focus.Title, count, lastSeen)
}

func rankedFocus(pool []Focus, state *State) Focus {
	if len(pool) == 0 {
		return Focus{
			Key:     "general-maintenance",
			Title:   "General maintenance",
			Group:   GroupStandard,
			Summary: "Fallback improvement category when no explicit focus areas are available.",
		}
	}

	filtered := append([]Focus(nil), pool...)
	if len(filtered) > 1 && state != nil && len(state.History) > 0 {
		last := state.History[len(state.History)-1].FocusKey
		next := filtered[:0]
		for _, focus := range filtered {
			if focus.Key == last {
				continue
			}
			next = append(next, focus)
		}
		if len(next) > 0 {
			filtered = next
		}
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		leftCount := selectionCount(state, filtered[i].Key)
		rightCount := selectionCount(state, filtered[j].Key)
		if leftCount != rightCount {
			return leftCount < rightCount
		}
		leftIndex := lastSelectedIndex(state, filtered[i].Key)
		rightIndex := lastSelectedIndex(state, filtered[j].Key)
		if leftIndex != rightIndex {
			return leftIndex < rightIndex
		}
		return filtered[i].Key < filtered[j].Key
	})
	return filtered[0]
}

func revisitCandidates(state *State) []Focus {
	if state == nil || len(state.History) == 0 {
		return nil
	}
	known := make(map[string]Focus, len(KnownFocuses()))
	for _, focus := range KnownFocuses() {
		known[focus.Key] = focus
	}

	seen := make(map[string]struct{})
	candidates := make([]Focus, 0, len(known))
	for i := 0; i < len(state.History); i++ {
		entry := state.History[i]
		focus, ok := known[entry.FocusKey]
		if !ok {
			continue
		}
		if _, exists := seen[focus.Key]; exists {
			continue
		}
		seen[focus.Key] = struct{}{}
		focus.Group = GroupRevisit
		candidates = append(candidates, focus)
	}
	return candidates
}

func groupForSlot(slot int) Group {
	switch {
	case slot < 6:
		return GroupRepoSpecific
	case slot < 9:
		return GroupStandard
	default:
		return GroupRevisit
	}
}

func selectionCount(state *State, key string) int {
	if state == nil || state.Counts == nil {
		return 0
	}
	return state.Counts[key]
}

func lastSelectedIndex(state *State, key string) int {
	if state == nil {
		return -1
	}
	for i := len(state.History) - 1; i >= 0; i-- {
		if state.History[i].FocusKey == key {
			return i
		}
	}
	return -1
}

func recentHistory(history []HistoryEntry) []HistoryEntry {
	if len(history) == 0 {
		return nil
	}
	if len(history) <= recentRunWindow {
		return append([]HistoryEntry(nil), history...)
	}
	return append([]HistoryEntry(nil), history[len(history)-recentRunWindow:]...)
}

func cloneState(state *State) *State {
	if state == nil {
		return defaultState()
	}
	cloned := &State{
		Version: state.Version,
		Runs:    state.Runs,
		Counts:  make(map[string]int, len(state.Counts)),
		History: append([]HistoryEntry(nil), state.History...),
	}
	for key, count := range state.Counts {
		cloned.Counts[key] = count
	}
	return cloned
}

func defaultState() *State {
	return &State{
		Version: StateVersion,
		Counts:  make(map[string]int),
		History: []HistoryEntry{},
	}
}

func writeJSON(path string, payload any, label string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s dir for %q: %w", label, path, err)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s %q: %w", label, path, err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s temp file %q: %w", label, tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s %q: %w", label, path, err)
	}
	return nil
}

func selectionFingerprint(repo string, slot int, focusKey string, now time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(repo),
		fmt.Sprintf("%d", slot),
		focusKey,
		now.UTC().Format(time.RFC3339),
	}, "\n")))
	return fmt.Sprintf("%x", sum[:8])
}
