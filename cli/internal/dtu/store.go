package dtu

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

var safePathComponent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Store persists DTU universe state on disk.
type Store struct {
	rootDir      string
	universeID   string
	statePath    string
	eventLogPath string
	lockPath     string
	clock        Clock
}

// NewStore returns a store rooted under <stateDir>/dtu/<universeID>/state.json.
func NewStore(stateDir, universeID string) (*Store, error) {
	return NewStoreWithClock(stateDir, universeID, nil)
}

// NewStoreWithClock returns a store rooted under <stateDir>/dtu/<universeID>/state.json
// with an optional injected clock.
func NewStoreWithClock(stateDir, universeID string, clock Clock) (*Store, error) {
	rootDir, err := UniverseDir(stateDir, universeID)
	if err != nil {
		return nil, fmt.Errorf("new store: %w", err)
	}
	statePath := filepath.Join(rootDir, stateFileName)
	return &Store{
		rootDir:      rootDir,
		universeID:   universeID,
		statePath:    statePath,
		eventLogPath: filepath.Join(rootDir, eventLogFileName),
		lockPath:     statePath + ".lock",
		clock:        clock,
	}, nil
}

// Path returns the underlying state file path.
func (s *Store) Path() string {
	return s.statePath
}

// EventLogPath returns the append-only DTU event log path.
func (s *Store) EventLogPath() string {
	return s.eventLogPath
}

// Exists reports whether the DTU state file exists.
func (s *Store) Exists() (bool, error) {
	var exists bool
	err := s.withRLock(func() error {
		_, err := os.Stat(s.statePath)
		if err == nil {
			exists = true
			return nil
		}
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat state: %w", err)
	})
	return exists, err
}

// Load reads and validates persisted DTU state.
func (s *Store) Load() (*State, error) {
	var state *State
	err := s.withRLock(func() error {
		loaded, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		state = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

// Save validates and atomically writes DTU state.
func (s *Store) Save(state *State) error {
	if state == nil {
		return fmt.Errorf("save state: state must not be nil")
	}
	return s.withLock(func() error {
		return s.persistUnlocked(state, s.existingStateForEventUnlocked(), EventKindStateSaved, StateOperationSave)
	})
}

// Reset restores persisted DTU state from a replay snapshot.
func (s *Store) Reset(snapshot ReplaySnapshot) error {
	if snapshot.State == nil {
		return fmt.Errorf("reset state: replay snapshot state must not be nil")
	}
	if snapshot.Hash == "" {
		return fmt.Errorf("reset state: replay snapshot hash is required")
	}
	return s.withLock(func() error {
		state, hash, _, err := snapshotState(snapshot.State, s.universeID)
		if err != nil {
			return fmt.Errorf("reset state: snapshot replay state: %w", err)
		}
		if hash != snapshot.Hash {
			return fmt.Errorf("reset state: replay snapshot event %d hash mismatch: have %q want %q", snapshot.EventIndex, hash, snapshot.Hash)
		}
		if err := s.persistUnlocked(state, s.existingStateForEventUnlocked(), EventKindStateUpdated, StateOperationReset); err != nil {
			return fmt.Errorf("reset state: %w", err)
		}
		return nil
	})
}

// LoadState is a convenience wrapper around Store.Load.
func LoadState(stateDir, universeID string) (*State, error) {
	store, err := NewStore(stateDir, universeID)
	if err != nil {
		return nil, err
	}
	return store.Load()
}

// SaveState is a convenience wrapper around Store.Save.
func SaveState(stateDir, universeID string, state *State) error {
	store, err := NewStore(stateDir, universeID)
	if err != nil {
		return err
	}
	return store.Save(state)
}

// Validate checks that a persisted DTU state is internally consistent.
func (s *State) Validate() error {
	if s == nil {
		return fmt.Errorf("state must not be nil")
	}
	if err := validatePathComponent(s.UniverseID); err != nil {
		return fmt.Errorf("invalid universe ID: %w", err)
	}
	if s.Version == "" {
		s.Version = formatVersion
	}
	if s.Version != formatVersion {
		return fmt.Errorf("version must be %q, got %q", formatVersion, s.Version)
	}
	if strings.TrimSpace(s.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if err := validateClock(s.Clock); err != nil {
		return err
	}
	if err := validateRepositories(s.Repositories); err != nil {
		return err
	}
	if err := validateProviderScripts(s.Providers.Scripts); err != nil {
		return err
	}
	if err := validateShimFaults(s.ShimFaults); err != nil {
		return err
	}
	if err := validateScheduledMutations(s.ScheduledMutations); err != nil {
		return err
	}
	if s.Counters.NextCommentID < 1 || s.Counters.NextReviewID < 1 || s.Counters.NextCheckID < 1 {
		return fmt.Errorf("counters must be initialized")
	}
	if err := validateRuntimeState(s.Runtime); err != nil {
		return err
	}
	return nil
}

// AdvanceCommentID reserves the next issue comment ID.
func (s *State) AdvanceCommentID() int64 {
	if s.Counters.NextCommentID < 1 {
		s.Counters.NextCommentID = 1
	}
	id := s.Counters.NextCommentID
	s.Counters.NextCommentID++
	return id
}

// AdvanceReviewID reserves the next pull request review ID.
func (s *State) AdvanceReviewID() int64 {
	if s.Counters.NextReviewID < 1 {
		s.Counters.NextReviewID = 1
	}
	id := s.Counters.NextReviewID
	s.Counters.NextReviewID++
	return id
}

// AdvanceCheckID reserves the next pull request check ID.
func (s *State) AdvanceCheckID() int64 {
	if s.Counters.NextCheckID < 1 {
		s.Counters.NextCheckID = 1
	}
	id := s.Counters.NextCheckID
	s.Counters.NextCheckID++
	return id
}

func validatePathComponent(component string) error {
	if component == "" {
		return fmt.Errorf("path component must not be empty")
	}
	if strings.Contains(component, "..") {
		return fmt.Errorf("path component must not contain %q", "..")
	}
	if !safePathComponent.MatchString(component) {
		return fmt.Errorf("path component %q contains invalid characters (allowed: a-zA-Z0-9._-)", component)
	}
	return nil
}

func validateClock(clock ClockState) error {
	if _, _, err := parseClockState(clock); err != nil {
		return err
	}
	return nil
}

func validateRepositories(repositories []Repository) error {
	seenRepos := make(map[string]struct{}, len(repositories))
	for _, repo := range repositories {
		slug := repo.Slug()
		if slug == "" {
			return fmt.Errorf("repository owner and name are required")
		}
		if _, _, err := SplitRepoSlug(slug); err != nil {
			return fmt.Errorf("repository %q: %w", slug, err)
		}
		if _, ok := seenRepos[slug]; ok {
			return fmt.Errorf("duplicate repository %q", slug)
		}
		seenRepos[slug] = struct{}{}
		if strings.TrimSpace(repo.DefaultBranch) == "" {
			return fmt.Errorf("repository %q: default_branch is required", slug)
		}
		if err := validateLabels(slug, repo.Labels); err != nil {
			return err
		}
		if err := validateBranches(slug, repo.Branches); err != nil {
			return err
		}
		if err := validateWorktrees(slug, repo.Worktrees); err != nil {
			return err
		}
		if err := validateIssues(slug, repo.Issues, repo.Labels); err != nil {
			return err
		}
		if err := validatePullRequests(slug, repo.PullRequests, repo.Labels); err != nil {
			return err
		}
	}
	return nil
}

func validateLabels(repoSlug string, labels []Label) error {
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		name := strings.TrimSpace(label.Name)
		if name == "" {
			return fmt.Errorf("repository %q: label name is required", repoSlug)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("repository %q: duplicate label %q", repoSlug, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateBranches(repoSlug string, branches []Branch) error {
	seen := make(map[string]struct{}, len(branches))
	for _, branch := range branches {
		name := strings.TrimSpace(branch.Name)
		if name == "" {
			return fmt.Errorf("repository %q: branch name is required", repoSlug)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("repository %q: duplicate branch %q", repoSlug, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateWorktrees(repoSlug string, worktrees []Worktree) error {
	seenPaths := make(map[string]struct{}, len(worktrees))
	for _, wt := range worktrees {
		if strings.TrimSpace(wt.Path) == "" {
			return fmt.Errorf("repository %q: worktree path is required", repoSlug)
		}
		if strings.TrimSpace(wt.Branch) == "" {
			return fmt.Errorf("repository %q: worktree %q: branch is required", repoSlug, wt.Path)
		}
		if _, ok := seenPaths[wt.Path]; ok {
			return fmt.Errorf("repository %q: duplicate worktree path %q", repoSlug, wt.Path)
		}
		seenPaths[wt.Path] = struct{}{}
	}
	return nil
}

func validateIssues(repoSlug string, issues []Issue, repoLabels []Label) error {
	knownLabels := make(map[string]struct{}, len(repoLabels))
	for _, label := range repoLabels {
		knownLabels[label.Name] = struct{}{}
	}
	seenIssues := make(map[int]struct{}, len(issues))
	for _, issue := range issues {
		if issue.Number <= 0 {
			return fmt.Errorf("repository %q: issue number must be greater than 0", repoSlug)
		}
		if _, ok := seenIssues[issue.Number]; ok {
			return fmt.Errorf("repository %q: duplicate issue %d", repoSlug, issue.Number)
		}
		seenIssues[issue.Number] = struct{}{}
		if strings.TrimSpace(issue.Title) == "" {
			return fmt.Errorf("repository %q: issue %d: title is required", repoSlug, issue.Number)
		}
		if !issue.State.Valid() {
			return fmt.Errorf("repository %q: issue %d: invalid state %q", repoSlug, issue.Number, issue.State)
		}
		if err := validateStringLabels(repoSlug, fmt.Sprintf("issue %d", issue.Number), issue.Labels, knownLabels); err != nil {
			return err
		}
		if err := validateComments(repoSlug, fmt.Sprintf("issue %d", issue.Number), issue.Comments); err != nil {
			return err
		}
	}
	return nil
}

func validatePullRequests(repoSlug string, prs []PullRequest, repoLabels []Label) error {
	knownLabels := make(map[string]struct{}, len(repoLabels))
	for _, label := range repoLabels {
		knownLabels[label.Name] = struct{}{}
	}
	seenPRs := make(map[int]struct{}, len(prs))
	for _, pr := range prs {
		if pr.Number <= 0 {
			return fmt.Errorf("repository %q: pull request number must be greater than 0", repoSlug)
		}
		if _, ok := seenPRs[pr.Number]; ok {
			return fmt.Errorf("repository %q: duplicate pull request %d", repoSlug, pr.Number)
		}
		seenPRs[pr.Number] = struct{}{}
		if strings.TrimSpace(pr.Title) == "" {
			return fmt.Errorf("repository %q: pull request %d: title is required", repoSlug, pr.Number)
		}
		if !pr.State.Valid() {
			return fmt.Errorf("repository %q: pull request %d: invalid state %q", repoSlug, pr.Number, pr.State)
		}
		if strings.TrimSpace(pr.BaseBranch) == "" {
			return fmt.Errorf("repository %q: pull request %d: base_branch is required", repoSlug, pr.Number)
		}
		if strings.TrimSpace(pr.HeadBranch) == "" {
			return fmt.Errorf("repository %q: pull request %d: head_branch is required", repoSlug, pr.Number)
		}
		if strings.TrimSpace(pr.HeadSHA) == "" {
			return fmt.Errorf("repository %q: pull request %d: head_sha is required", repoSlug, pr.Number)
		}
		if pr.Merged && pr.State != "" && pr.State != PullRequestStateMerged {
			return fmt.Errorf("repository %q: pull request %d: merged pull request must use state %q", repoSlug, pr.Number, PullRequestStateMerged)
		}
		if err := validateStringLabels(repoSlug, fmt.Sprintf("pull request %d", pr.Number), pr.Labels, knownLabels); err != nil {
			return err
		}
		if err := validateComments(repoSlug, fmt.Sprintf("pull request %d", pr.Number), pr.Comments); err != nil {
			return err
		}
		if err := validateReviews(repoSlug, pr.Number, pr.Reviews); err != nil {
			return err
		}
		if err := validateChecks(repoSlug, pr.Number, pr.Checks); err != nil {
			return err
		}
	}
	return nil
}

func validateStringLabels(repoSlug, owner string, labels []string, known map[string]struct{}) error {
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			return fmt.Errorf("repository %q: %s: label must not be empty", repoSlug, owner)
		}
		if len(known) > 0 {
			if _, ok := known[label]; !ok {
				return fmt.Errorf("repository %q: %s: unknown label %q", repoSlug, owner, label)
			}
		}
	}
	return nil
}

func validateComments(repoSlug, owner string, comments []Comment) error {
	seen := make(map[int64]struct{}, len(comments))
	for _, comment := range comments {
		if strings.TrimSpace(comment.Body) == "" {
			return fmt.Errorf("repository %q: %s: comment body is required", repoSlug, owner)
		}
		if comment.ID == 0 {
			continue
		}
		if comment.ID < 0 {
			return fmt.Errorf("repository %q: %s: comment ID must be positive", repoSlug, owner)
		}
		if _, ok := seen[comment.ID]; ok {
			return fmt.Errorf("repository %q: %s: duplicate comment ID %d", repoSlug, owner, comment.ID)
		}
		seen[comment.ID] = struct{}{}
	}
	return nil
}

func validateReviews(repoSlug string, prNumber int, reviews []Review) error {
	seen := make(map[int64]struct{}, len(reviews))
	for _, review := range reviews {
		if !review.State.Valid() {
			return fmt.Errorf("repository %q: pull request %d: invalid review state %q", repoSlug, prNumber, review.State)
		}
		if review.ID == 0 {
			continue
		}
		if review.ID < 0 {
			return fmt.Errorf("repository %q: pull request %d: review ID must be positive", repoSlug, prNumber)
		}
		if _, ok := seen[review.ID]; ok {
			return fmt.Errorf("repository %q: pull request %d: duplicate review ID %d", repoSlug, prNumber, review.ID)
		}
		seen[review.ID] = struct{}{}
	}
	return nil
}

func validateChecks(repoSlug string, prNumber int, checks []Check) error {
	seenNames := make(map[string]struct{}, len(checks))
	seenIDs := make(map[int64]struct{}, len(checks))
	for _, check := range checks {
		if strings.TrimSpace(check.Name) == "" {
			return fmt.Errorf("repository %q: pull request %d: check name is required", repoSlug, prNumber)
		}
		if !check.State.Valid() {
			return fmt.Errorf("repository %q: pull request %d: invalid check state %q", repoSlug, prNumber, check.State)
		}
		if _, ok := seenNames[check.Name]; ok {
			return fmt.Errorf("repository %q: pull request %d: duplicate check %q", repoSlug, prNumber, check.Name)
		}
		seenNames[check.Name] = struct{}{}
		if check.ID == 0 {
			continue
		}
		if check.ID < 0 {
			return fmt.Errorf("repository %q: pull request %d: check ID must be positive", repoSlug, prNumber)
		}
		if _, ok := seenIDs[check.ID]; ok {
			return fmt.Errorf("repository %q: pull request %d: duplicate check ID %d", repoSlug, prNumber, check.ID)
		}
		seenIDs[check.ID] = struct{}{}
	}
	return nil
}

func validateProviderScripts(scripts []ProviderScript) error {
	seen := make(map[string]struct{}, len(scripts))
	for _, script := range scripts {
		name := strings.TrimSpace(script.Name)
		scenario := strings.TrimSpace(script.Match.Scenario)
		if name == "" {
			return fmt.Errorf("provider script name is required")
		}
		if !script.Provider.Valid() {
			return fmt.Errorf("provider script %q: invalid provider %q", name, script.Provider)
		}
		if script.Match.Attempt < 0 {
			return fmt.Errorf("provider script %q: attempt must be greater than or equal to 0", name)
		}
		if script.Delay != "" {
			if _, err := time.ParseDuration(script.Delay); err != nil {
				return fmt.Errorf("provider script %q: delay must be a valid duration: %w", name, err)
			}
		}
		key := providerScriptKey(script.Provider, scenario, name, script.Match.Attempt)
		if _, ok := seen[key]; ok {
			scope := ""
			if scenario != "" {
				scope = fmt.Sprintf(" for scenario %q", scenario)
			}
			if script.Match.Attempt > 0 {
				return fmt.Errorf("duplicate provider script %q for provider %q%s attempt %d", name, script.Provider, scope, script.Match.Attempt)
			}
			return fmt.Errorf("duplicate provider script %q for provider %q%s", name, script.Provider, scope)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateShimFaults(faults []ShimFault) error {
	seen := make(map[string]struct{}, len(faults))
	for _, fault := range faults {
		name := strings.TrimSpace(fault.Name)
		if name == "" {
			return fmt.Errorf("shim fault name is required")
		}
		if !fault.Command.Valid() {
			return fmt.Errorf("shim fault %q: invalid command %q", name, fault.Command)
		}
		if fault.Match.Attempt < 0 {
			return fmt.Errorf("shim fault %q: attempt must be greater than or equal to 0", name)
		}
		if len(fault.Match.ArgsExact) > 0 && len(fault.Match.ArgsPrefix) > 0 {
			return fmt.Errorf("shim fault %q: args_exact and args_prefix are mutually exclusive", name)
		}
		if fault.ExitCode < 0 {
			return fmt.Errorf("shim fault %q: exit_code must be greater than or equal to 0", name)
		}
		if fault.Delay != "" {
			if _, err := time.ParseDuration(fault.Delay); err != nil {
				return fmt.Errorf("shim fault %q: delay must be a valid duration: %w", name, err)
			}
		}
		key := shimFaultKey(fault.Command, name, fault.Match.Attempt)
		if _, ok := seen[key]; ok {
			if fault.Match.Attempt > 0 {
				return fmt.Errorf("duplicate shim fault %q for command %q attempt %d", name, fault.Command, fault.Match.Attempt)
			}
			return fmt.Errorf("duplicate shim fault %q for command %q", name, fault.Command)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateScheduledMutations(mutations []ScheduledMutation) error {
	seen := make(map[string]struct{}, len(mutations))
	for _, mutation := range mutations {
		name := strings.TrimSpace(mutation.Name)
		if name == "" {
			return fmt.Errorf("scheduled mutation name is required")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate scheduled mutation %q", name)
		}
		seen[name] = struct{}{}
		if !mutation.Trigger.Command.Valid() {
			return fmt.Errorf("scheduled mutation %q: invalid trigger command %q", name, mutation.Trigger.Command)
		}
		if mutation.Trigger.After < 0 {
			return fmt.Errorf("scheduled mutation %q: trigger after must be greater than or equal to 0", name)
		}
		if len(mutation.Trigger.ArgsExact) > 0 && len(mutation.Trigger.ArgsPrefix) > 0 {
			return fmt.Errorf("scheduled mutation %q: trigger args_exact and args_prefix are mutually exclusive", name)
		}
		if mutation.Trigger.Attempt < 0 {
			return fmt.Errorf("scheduled mutation %q: trigger attempt must be greater than or equal to 0", name)
		}
		if len(mutation.Operations) == 0 {
			return fmt.Errorf("scheduled mutation %q: at least one operation is required", name)
		}
		for _, operation := range mutation.Operations {
			if !operation.Type.Valid() {
				return fmt.Errorf("scheduled mutation %q: invalid operation type %q", name, operation.Type)
			}
			if strings.TrimSpace(operation.Repo) == "" {
				return fmt.Errorf("scheduled mutation %q: operation repo is required", name)
			}
			if operation.Number <= 0 {
				return fmt.Errorf("scheduled mutation %q: operation number must be greater than 0", name)
			}
			switch operation.Type {
			case MutationOperationIssueAddLabel, MutationOperationIssueRemoveLabel, MutationOperationPRAddLabel, MutationOperationPRRemoveLabel:
				if strings.TrimSpace(operation.Label) == "" {
					return fmt.Errorf("scheduled mutation %q: operation %q requires label", name, operation.Type)
				}
			case MutationOperationIssueAddComment, MutationOperationPRAddComment:
				if strings.TrimSpace(operation.Body) == "" {
					return fmt.Errorf("scheduled mutation %q: operation %q requires body", name, operation.Type)
				}
			case MutationOperationPRSetCheckState:
				if strings.TrimSpace(operation.Check) == "" {
					return fmt.Errorf("scheduled mutation %q: operation %q requires check", name, operation.Type)
				}
				if !CheckState(strings.TrimSpace(operation.State)).Valid() {
					return fmt.Errorf("scheduled mutation %q: operation %q requires valid check state, got %q", name, operation.Type, operation.State)
				}
			}
		}
	}
	return nil
}

func validateRuntimeState(runtime RuntimeState) error {
	for _, observation := range runtime.Observations {
		if strings.TrimSpace(observation.Key) == "" {
			return fmt.Errorf("runtime observation key is required")
		}
		if observation.Count < 0 {
			return fmt.Errorf("runtime observation %q count must be greater than or equal to 0", observation.Key)
		}
	}
	for _, mutation := range runtime.AppliedMutations {
		if strings.TrimSpace(mutation.Name) == "" {
			return fmt.Errorf("runtime applied mutation name is required")
		}
		if mutation.AppliedAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, mutation.AppliedAt); err != nil {
				return fmt.Errorf("runtime applied mutation %q applied_at must be RFC3339: %w", mutation.Name, err)
			}
		}
	}
	return nil
}

func (s *State) normalize() {
	s.normalizeWithClock(nil)
}

func (s *State) normalizeWithClock(clock Clock) {
	if clock == nil {
		clock = SystemClock{}
	}
	if s.Version == "" {
		s.Version = formatVersion
	}
	s.Metadata.Scenario = strings.TrimSpace(s.Metadata.Scenario)
	s.Metadata.Tags = normalizeStrings(s.Metadata.Tags)
	s.Clock = normalizeClockState(s.Clock, clock)

	commentCounter := s.Counters.NextCommentID
	if commentCounter < 1 {
		commentCounter = 1
	}
	reviewCounter := s.Counters.NextReviewID
	if reviewCounter < 1 {
		reviewCounter = 1
	}
	checkCounter := s.Counters.NextCheckID
	if checkCounter < 1 {
		checkCounter = 1
	}

	for i := range s.Repositories {
		repo := &s.Repositories[i]
		repo.Labels = normalizeLabelDefs(repo.Labels)
		repo.Branches = normalizeBranches(repo.Branches)
		repo.Worktrees = normalizeWorktrees(repo.Worktrees)
		for j := range repo.Issues {
			issue := &repo.Issues[j]
			if issue.State == "" {
				issue.State = IssueStateOpen
			}
			issue.Labels = normalizeStrings(issue.Labels)
			for k := range issue.Comments {
				if issue.Comments[k].ID <= 0 {
					issue.Comments[k].ID = commentCounter
					commentCounter++
				}
				if issue.Comments[k].ID >= commentCounter {
					commentCounter = issue.Comments[k].ID + 1
				}
			}
			sort.SliceStable(issue.Comments, func(a, b int) bool {
				if issue.Comments[a].ID == issue.Comments[b].ID {
					return issue.Comments[a].Body < issue.Comments[b].Body
				}
				return issue.Comments[a].ID < issue.Comments[b].ID
			})
		}
		sort.SliceStable(repo.Issues, func(a, b int) bool { return repo.Issues[a].Number < repo.Issues[b].Number })

		for j := range repo.PullRequests {
			pr := &repo.PullRequests[j]
			if pr.State == "" {
				if pr.Merged {
					pr.State = PullRequestStateMerged
				} else {
					pr.State = PullRequestStateOpen
				}
			}
			pr.Labels = normalizeStrings(pr.Labels)
			for k := range pr.Comments {
				if pr.Comments[k].ID <= 0 {
					pr.Comments[k].ID = commentCounter
					commentCounter++
				}
				if pr.Comments[k].ID >= commentCounter {
					commentCounter = pr.Comments[k].ID + 1
				}
			}
			sort.SliceStable(pr.Comments, func(a, b int) bool {
				if pr.Comments[a].ID == pr.Comments[b].ID {
					return pr.Comments[a].Body < pr.Comments[b].Body
				}
				return pr.Comments[a].ID < pr.Comments[b].ID
			})
			for k := range pr.Reviews {
				if pr.Reviews[k].State == "" {
					pr.Reviews[k].State = ReviewStateCommented
				}
				if pr.Reviews[k].ID <= 0 {
					pr.Reviews[k].ID = reviewCounter
					reviewCounter++
				}
				if pr.Reviews[k].ID >= reviewCounter {
					reviewCounter = pr.Reviews[k].ID + 1
				}
			}
			sort.SliceStable(pr.Reviews, func(a, b int) bool {
				if pr.Reviews[a].ID == pr.Reviews[b].ID {
					return pr.Reviews[a].Body < pr.Reviews[b].Body
				}
				return pr.Reviews[a].ID < pr.Reviews[b].ID
			})
			for k := range pr.Checks {
				if pr.Checks[k].State == "" {
					pr.Checks[k].State = CheckStatePending
				}
				if pr.Checks[k].ID <= 0 {
					pr.Checks[k].ID = checkCounter
					checkCounter++
				}
				if pr.Checks[k].ID >= checkCounter {
					checkCounter = pr.Checks[k].ID + 1
				}
			}
			sort.SliceStable(pr.Checks, func(a, b int) bool {
				if pr.Checks[a].Name == pr.Checks[b].Name {
					return pr.Checks[a].ID < pr.Checks[b].ID
				}
				return pr.Checks[a].Name < pr.Checks[b].Name
			})
		}
		sort.SliceStable(repo.PullRequests, func(a, b int) bool { return repo.PullRequests[a].Number < repo.PullRequests[b].Number })
	}
	sort.SliceStable(s.Repositories, func(a, b int) bool { return s.Repositories[a].Slug() < s.Repositories[b].Slug() })

	for i := range s.Providers.Scripts {
		script := &s.Providers.Scripts[i]
		script.Name = strings.TrimSpace(script.Name)
		script.Model = strings.TrimSpace(script.Model)
		script.Match.Scenario = strings.TrimSpace(script.Match.Scenario)
		script.Match.Phase = strings.TrimSpace(script.Match.Phase)
		script.Match.PromptContains = strings.TrimSpace(script.Match.PromptContains)
		script.Match.PromptExact = strings.TrimSpace(script.Match.PromptExact)
		script.AllowedTools = normalizeStrings(script.AllowedTools)
		script.NoOpMarker = strings.TrimSpace(script.NoOpMarker)
	}
	sort.SliceStable(s.Providers.Scripts, func(a, b int) bool {
		left := providerScriptKey(s.Providers.Scripts[a].Provider, s.Providers.Scripts[a].Match.Scenario, s.Providers.Scripts[a].Name, s.Providers.Scripts[a].Match.Attempt)
		right := providerScriptKey(s.Providers.Scripts[b].Provider, s.Providers.Scripts[b].Match.Scenario, s.Providers.Scripts[b].Name, s.Providers.Scripts[b].Match.Attempt)
		return left < right
	})

	for i := range s.ShimFaults {
		fault := &s.ShimFaults[i]
		fault.Name = strings.TrimSpace(fault.Name)
		fault.Match.Phase = strings.TrimSpace(fault.Match.Phase)
		fault.Match.Script = strings.TrimSpace(fault.Match.Script)
		fault.Match.ArgsExact = normalizeMatchArgs(fault.Match.ArgsExact)
		fault.Match.ArgsPrefix = normalizeMatchArgs(fault.Match.ArgsPrefix)
		if !fault.Hang && !fault.ExitSet && fault.ExitCode == 0 {
			fault.ExitCode = 1
		}
	}
	sort.SliceStable(s.ShimFaults, func(a, b int) bool {
		left := shimFaultKey(s.ShimFaults[a].Command, s.ShimFaults[a].Name, s.ShimFaults[a].Match.Attempt)
		right := shimFaultKey(s.ShimFaults[b].Command, s.ShimFaults[b].Name, s.ShimFaults[b].Match.Attempt)
		return left < right
	})

	for i := range s.ScheduledMutations {
		mutation := &s.ScheduledMutations[i]
		mutation.Name = strings.TrimSpace(mutation.Name)
		mutation.Trigger.Phase = strings.TrimSpace(mutation.Trigger.Phase)
		mutation.Trigger.Script = strings.TrimSpace(mutation.Trigger.Script)
		mutation.Trigger.ArgsExact = normalizeMatchArgs(mutation.Trigger.ArgsExact)
		mutation.Trigger.ArgsPrefix = normalizeMatchArgs(mutation.Trigger.ArgsPrefix)
		for j := range mutation.Operations {
			mutation.Operations[j].Repo = strings.TrimSpace(mutation.Operations[j].Repo)
			mutation.Operations[j].Label = strings.TrimSpace(mutation.Operations[j].Label)
			mutation.Operations[j].Check = strings.TrimSpace(mutation.Operations[j].Check)
			mutation.Operations[j].State = strings.TrimSpace(mutation.Operations[j].State)
			mutation.Operations[j].Body = strings.TrimSpace(mutation.Operations[j].Body)
		}
	}
	sort.SliceStable(s.ScheduledMutations, func(a, b int) bool {
		return s.ScheduledMutations[a].Name < s.ScheduledMutations[b].Name
	})

	for i := range s.Runtime.Observations {
		s.Runtime.Observations[i].Key = strings.TrimSpace(s.Runtime.Observations[i].Key)
	}
	sort.SliceStable(s.Runtime.Observations, func(a, b int) bool {
		return s.Runtime.Observations[a].Key < s.Runtime.Observations[b].Key
	})

	for i := range s.Runtime.AppliedMutations {
		s.Runtime.AppliedMutations[i].Name = strings.TrimSpace(s.Runtime.AppliedMutations[i].Name)
		s.Runtime.AppliedMutations[i].Key = strings.TrimSpace(s.Runtime.AppliedMutations[i].Key)
		if appliedAt := strings.TrimSpace(s.Runtime.AppliedMutations[i].AppliedAt); appliedAt != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, appliedAt); err == nil {
				s.Runtime.AppliedMutations[i].AppliedAt = parsed.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	sort.SliceStable(s.Runtime.AppliedMutations, func(a, b int) bool {
		if s.Runtime.AppliedMutations[a].Name == s.Runtime.AppliedMutations[b].Name {
			return s.Runtime.AppliedMutations[a].Key < s.Runtime.AppliedMutations[b].Key
		}
		return s.Runtime.AppliedMutations[a].Name < s.Runtime.AppliedMutations[b].Name
	})

	s.Counters.NextCommentID = commentCounter
	s.Counters.NextReviewID = reviewCounter
	s.Counters.NextCheckID = checkCounter
}

func (s *Store) clockOrDefault() Clock {
	if s != nil && s.clock != nil {
		return s.clock
	}
	return SystemClock{}
}

func (s *Store) currentClockUnlocked() (Clock, error) {
	state, err := s.loadUnlocked()
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "read state:") {
			return s.clockOrDefault(), nil
		}
		return nil, fmt.Errorf("resolve DTU clock: %w", err)
	}
	return ResolveClock(state.Clock, s.clockOrDefault())
}

func normalizeLabelDefs(labels []Label) []Label {
	out := append([]Label(nil), labels...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func normalizeBranches(branches []Branch) []Branch {
	out := append([]Branch(nil), branches...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func normalizeWorktrees(worktrees []Worktree) []Worktree {
	out := append([]Worktree(nil), worktrees...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeMatchArgs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func providerScriptKey(provider Provider, scenario string, name string, attempt int) string {
	return fmt.Sprintf("%s:%s:%s:%06d", provider, strings.TrimSpace(scenario), name, attempt)
}

func shimFaultKey(command ShimCommand, name string, attempt int) string {
	return fmt.Sprintf("%s:%s:%06d", command, name, attempt)
}

func (s *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0o755); err != nil {
		return fmt.Errorf("create DTU lock dir: %w", err)
	}
	lock := flock.New(s.lockPath)
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			log.Printf("warn: failed to unlock DTU state: %v", unlockErr)
		}
	}()
	return fn()
}

func (s *Store) withRLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0o755); err != nil {
		return fmt.Errorf("create DTU lock dir: %w", err)
	}
	lock := flock.New(s.lockPath)
	if err := lock.RLock(); err != nil {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			log.Printf("warn: failed to unlock DTU state: %v", unlockErr)
		}
	}()
	return fn()
}
