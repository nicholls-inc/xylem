package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

const (
	// UnsetPREventsDebounce marks "no explicit debounce configured" so trigger-
	// specific defaults can still distinguish unset from an explicit zero value.
	UnsetPREventsDebounce = time.Duration(-1)

	preventLabel           = "label"
	preventReviewSubmitted = "review_submitted"
	preventChecksFailed    = "checks_failed"
	preventCommented       = "commented"
	preventPROpened        = "pr_opened"
	preventPRHeadUpdated   = "pr_head_updated"

	defaultPREventDebounce = 10 * time.Minute
)

type prEventDebounceRecord struct {
	Source    string `json:"source"`
	Trigger   string `json:"trigger"`
	PRNumber  int    `json:"pr_number"`
	EmittedAt string `json:"emitted_at"`
}

type prEventDebounceState map[string]prEventDebounceRecord

func debounceStateKey(sourceName, trigger string, prNumber int) string {
	return fmt.Sprintf("%s|%s|%d", sourceName, trigger, prNumber)
}

func effectivePREventDebounce(task PREventsTask, trigger string) time.Duration {
	if task.Debounce >= 0 {
		return task.Debounce
	}
	switch trigger {
	case preventPRHeadUpdated:
		return defaultPREventDebounce
	default:
		return 0
	}
}

func debounceCollapsesEvents(task PREventsTask, trigger string) bool {
	return effectivePREventDebounce(task, trigger) > 0
}

func (g *GitHubPREvents) shouldDebounceTrigger(trigger string, prNumber int, task PREventsTask) (bool, error) {
	debounce := effectivePREventDebounce(task, trigger)
	if debounce <= 0 || strings.TrimSpace(g.StateDir) == "" {
		return false, nil
	}
	lastEmittedAt, err := g.loadDebounceEmittedAt(trigger, prNumber)
	if err != nil {
		return false, err
	}
	if lastEmittedAt == nil {
		return false, nil
	}
	return g.now().Before(lastEmittedAt.Add(debounce)), nil
}

func (g *GitHubPREvents) markDebounceMeta(meta map[string]string, trigger string, prNumber int, task PREventsTask, emittedAt time.Time) {
	if effectivePREventDebounce(task, trigger) <= 0 {
		return
	}
	meta["pr_event.debounce_trigger"] = trigger
	meta["pr_event.debounce_pr_number"] = strconv.Itoa(prNumber)
	meta["pr_event.debounce_emitted_at"] = emittedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func (g *GitHubPREvents) persistDebounce(vessel queue.Vessel) error {
	trigger := vessel.Meta["pr_event.debounce_trigger"]
	if trigger == "" || strings.TrimSpace(g.StateDir) == "" {
		return nil
	}
	prNumber, err := strconv.Atoi(vessel.Meta["pr_event.debounce_pr_number"])
	if err != nil {
		return fmt.Errorf("parse pr_event debounce pr_number for %s: %w", vessel.ID, err)
	}
	emittedAt, err := time.Parse(time.RFC3339, vessel.Meta["pr_event.debounce_emitted_at"])
	if err != nil {
		return fmt.Errorf("parse pr_event debounce emitted_at for %s: %w", vessel.ID, err)
	}
	return g.storeDebounceEmittedAt(trigger, prNumber, emittedAt)
}

func (g *GitHubPREvents) loadDebounceEmittedAt(trigger string, prNumber int) (*time.Time, error) {
	state, err := g.readDebounceState()
	if err != nil {
		return nil, err
	}
	record, ok := state[g.debounceKey(trigger, prNumber)]
	if !ok || strings.TrimSpace(record.EmittedAt) == "" {
		return nil, nil
	}
	emittedAt, err := time.Parse(time.RFC3339, record.EmittedAt)
	if err != nil {
		return nil, fmt.Errorf("parse debounce emitted_at %q: %w", record.EmittedAt, err)
	}
	emittedAt = emittedAt.UTC()
	return &emittedAt, nil
}

func (g *GitHubPREvents) storeDebounceEmittedAt(trigger string, prNumber int, emittedAt time.Time) error {
	if err := os.MkdirAll(filepath.Dir(g.debounceStatePath()), 0o755); err != nil {
		return fmt.Errorf("create pr events debounce state directory: %w", err)
	}
	lock := flock.New(g.debounceStatePath() + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock pr events debounce state: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()

	state, err := g.readDebounceStateUnlocked()
	if err != nil {
		return err
	}
	state[g.debounceKey(trigger, prNumber)] = prEventDebounceRecord{
		Source:    g.Name(),
		Trigger:   trigger,
		PRNumber:  prNumber,
		EmittedAt: emittedAt.UTC().Truncate(time.Second).Format(time.RFC3339),
	}
	data, err := marshalDebounceState(state)
	if err != nil {
		return err
	}
	if err := os.WriteFile(g.debounceStatePath(), data, 0o644); err != nil {
		return fmt.Errorf("write pr events debounce state: %w", err)
	}
	return nil
}

func (g *GitHubPREvents) readDebounceState() (prEventDebounceState, error) {
	if err := os.MkdirAll(filepath.Dir(g.debounceStatePath()), 0o755); err != nil {
		return nil, fmt.Errorf("create pr events debounce state directory: %w", err)
	}
	lock := flock.New(g.debounceStatePath() + ".lock")
	if err := lock.RLock(); err != nil {
		return nil, fmt.Errorf("lock pr events debounce state for read: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()
	return g.readDebounceStateUnlocked()
}

func (g *GitHubPREvents) readDebounceStateUnlocked() (prEventDebounceState, error) {
	data, err := os.ReadFile(g.debounceStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return prEventDebounceState{}, nil
		}
		return nil, fmt.Errorf("read pr events debounce state: %w", err)
	}
	state, err := unmarshalDebounceState(data)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func marshalDebounceState(state prEventDebounceState) ([]byte, error) {
	canonical := make(map[string]prEventDebounceRecord, len(state))
	keys := make([]string, 0, len(state))
	for key := range state {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		canonical[key] = state[key]
	}
	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal pr events debounce state: %w", err)
	}
	if len(data) > 0 {
		data = append(data, '\n')
	}
	return data, nil
}

func unmarshalDebounceState(data []byte) (prEventDebounceState, error) {
	state := prEventDebounceState{}
	if err := json.Unmarshal(data, &state); err == nil {
		return state, nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record prEventDebounceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("parse pr events debounce state line: %w", err)
		}
		sourceName := record.Source
		if strings.TrimSpace(sourceName) == "" {
			sourceName = "github-pr-events"
		}
		state[debounceStateKey(sourceName, record.Trigger, record.PRNumber)] = record
	}
	return state, nil
}

func (g *GitHubPREvents) debounceStatePath() string {
	return filepath.Join(g.StateDir, "state", "pr-events", "debounce.json")
}

func (g *GitHubPREvents) debounceKey(trigger string, prNumber int) string {
	return debounceStateKey(g.Name(), trigger, prNumber)
}
