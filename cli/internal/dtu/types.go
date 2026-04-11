package dtu

import (
	"fmt"
	"strings"
	"time"
)

const (
	formatVersion = "v1"
	stateFileName = "state.json"
)

// Provider identifies a simulated LLM provider boundary.
type Provider string

const (
	ProviderClaude  Provider = "claude"
	ProviderCopilot Provider = "copilot"
)

// Valid reports whether p is a recognized provider value.
func (p Provider) Valid() bool {
	switch p {
	case ProviderClaude, ProviderCopilot:
		return true
	default:
		return false
	}
}

// ShimCommand identifies a shimmed binary boundary.
type ShimCommand string

const (
	ShimCommandGH      ShimCommand = "gh"
	ShimCommandGit     ShimCommand = "git"
	ShimCommandClaude  ShimCommand = "claude"
	ShimCommandCopilot ShimCommand = "copilot"
)

// Valid reports whether c is a recognized shim command value.
func (c ShimCommand) Valid() bool {
	switch c {
	case ShimCommandGH, ShimCommandGit, ShimCommandClaude, ShimCommandCopilot:
		return true
	default:
		return false
	}
}

// IssueState describes the lifecycle state of an issue.
type IssueState string

const (
	IssueStateOpen   IssueState = "open"
	IssueStateClosed IssueState = "closed"
)

// Valid reports whether s is a recognized issue state.
func (s IssueState) Valid() bool {
	switch s {
	case "", IssueStateOpen, IssueStateClosed:
		return true
	default:
		return false
	}
}

// PullRequestState describes the lifecycle state of a pull request.
type PullRequestState string

const (
	PullRequestStateOpen   PullRequestState = "open"
	PullRequestStateClosed PullRequestState = "closed"
	PullRequestStateMerged PullRequestState = "merged"
)

// Valid reports whether s is a recognized pull request state.
func (s PullRequestState) Valid() bool {
	switch s {
	case "", PullRequestStateOpen, PullRequestStateClosed, PullRequestStateMerged:
		return true
	default:
		return false
	}
}

// ReviewState describes the outcome of a pull request review.
type ReviewState string

const (
	ReviewStateApproved         ReviewState = "APPROVED"
	ReviewStateChangesRequested ReviewState = "CHANGES_REQUESTED"
	ReviewStateCommented        ReviewState = "COMMENTED"
	ReviewStateDismissed        ReviewState = "DISMISSED"
)

// Valid reports whether s is a recognized review state.
func (s ReviewState) Valid() bool {
	switch s {
	case "", ReviewStateApproved, ReviewStateChangesRequested, ReviewStateCommented, ReviewStateDismissed:
		return true
	default:
		return false
	}
}

// CheckState describes the status of a pull request check.
type CheckState string

const (
	CheckStatePending   CheckState = "pending"
	CheckStateSuccess   CheckState = "success"
	CheckStateFailure   CheckState = "failure"
	CheckStateCancelled CheckState = "cancelled"
	CheckStateSkipped   CheckState = "skipped"
)

// Valid reports whether s is a recognized check state.
func (s CheckState) Valid() bool {
	switch s {
	case "", CheckStatePending, CheckStateSuccess, CheckStateFailure, CheckStateCancelled, CheckStateSkipped:
		return true
	default:
		return false
	}
}

// ManifestMetadata describes the source scenario behind a DTU state file.
type ManifestMetadata struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Scenario    string   `yaml:"scenario,omitempty" json:"scenario,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// Manifest defines the immutable seed data for a DTU universe.
type Manifest struct {
	Version            string              `yaml:"version,omitempty" json:"version,omitempty"`
	Metadata           ManifestMetadata    `yaml:"metadata" json:"metadata"`
	Clock              ClockState          `yaml:"clock,omitempty" json:"clock,omitempty"`
	Repositories       []Repository        `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	Providers          Providers           `yaml:"providers,omitempty" json:"providers,omitempty"`
	ShimFaults         []ShimFault         `yaml:"shim_faults,omitempty" json:"shim_faults,omitempty"`
	ScheduledMutations []ScheduledMutation `yaml:"scheduled_mutations,omitempty" json:"scheduled_mutations,omitempty"`
}

// ClockState captures the current simulated time.
type ClockState struct {
	Now string `yaml:"now,omitempty" json:"now,omitempty"`
}

// State is the persisted DTU universe consumed by upcoming commands and shims.
type State struct {
	UniverseID         string              `json:"universe_id"`
	Version            string              `json:"version"`
	Metadata           ManifestMetadata    `json:"metadata"`
	ManifestPath       string              `json:"manifest_path,omitempty"`
	Clock              ClockState          `json:"clock,omitempty"`
	Repositories       []Repository        `json:"repositories,omitempty"`
	Providers          Providers           `json:"providers,omitempty"`
	ShimFaults         []ShimFault         `json:"shim_faults,omitempty"`
	ScheduledMutations []ScheduledMutation `json:"scheduled_mutations,omitempty"`
	Runtime            RuntimeState        `json:"runtime,omitempty"`
	Counters           Counters            `json:"counters"`
}

// Counters tracks the next synthetic IDs to allocate for mutable GitHub entities.
type Counters struct {
	NextCommentID int64 `json:"next_comment_id,omitempty"`
	NextReviewID  int64 `json:"next_review_id,omitempty"`
	NextCheckID   int64 `json:"next_check_id,omitempty"`
}

// Providers groups provider-specific scripted behavior.
type Providers struct {
	Scripts []ProviderScript `yaml:"scripts,omitempty" json:"scripts,omitempty"`
}

// ProviderScript captures a deterministic provider response definition.
type ProviderScript struct {
	Name         string              `yaml:"name" json:"name"`
	Provider     Provider            `yaml:"provider" json:"provider"`
	Match        ProviderScriptMatch `yaml:"match,omitempty" json:"match,omitempty"`
	Model        string              `yaml:"model,omitempty" json:"model,omitempty"`
	AllowedTools []string            `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
	Stdout       string              `yaml:"stdout,omitempty" json:"stdout,omitempty"`
	Stderr       string              `yaml:"stderr,omitempty" json:"stderr,omitempty"`
	ExitCode     int                 `yaml:"exit_code,omitempty" json:"exit_code,omitempty"`
	Delay        string              `yaml:"delay,omitempty" json:"delay,omitempty"`
	Hang         bool                `yaml:"hang,omitempty" json:"hang,omitempty"`
	NoOpMarker   string              `yaml:"noop_marker,omitempty" json:"noop_marker,omitempty"`
}

// ProviderScriptMatch narrows when a provider script should be selected.
type ProviderScriptMatch struct {
	Scenario       string `yaml:"scenario,omitempty" json:"scenario,omitempty"`
	Phase          string `yaml:"phase,omitempty" json:"phase,omitempty"`
	Attempt        int    `yaml:"attempt,omitempty" json:"attempt,omitempty"`
	PromptContains string `yaml:"prompt_contains,omitempty" json:"prompt_contains,omitempty"`
	PromptExact    string `yaml:"prompt_exact,omitempty" json:"prompt_exact,omitempty"`
}

// ShimFault captures a deterministic shim failure definition.
type ShimFault struct {
	Name     string         `yaml:"name" json:"name"`
	Command  ShimCommand    `yaml:"command" json:"command"`
	Match    ShimFaultMatch `yaml:"match,omitempty" json:"match,omitempty"`
	Stdout   string         `yaml:"stdout,omitempty" json:"stdout,omitempty"`
	Stderr   string         `yaml:"stderr,omitempty" json:"stderr,omitempty"`
	ExitCode int            `yaml:"exit_code,omitempty" json:"exit_code,omitempty"`
	Delay    string         `yaml:"delay,omitempty" json:"delay,omitempty"`
	Hang     bool           `yaml:"hang,omitempty" json:"hang,omitempty"`
	ExitSet  bool           `yaml:"-" json:"exit_set,omitempty"`
}

// ShimFaultMatch narrows when a shim fault should be selected.
type ShimFaultMatch struct {
	ArgsExact  []string `yaml:"args_exact,omitempty" json:"args_exact,omitempty"`
	ArgsPrefix []string `yaml:"args_prefix,omitempty" json:"args_prefix,omitempty"`
	Phase      string   `yaml:"phase,omitempty" json:"phase,omitempty"`
	Script     string   `yaml:"script,omitempty" json:"script,omitempty"`
	Attempt    int      `yaml:"attempt,omitempty" json:"attempt,omitempty"`
}

// ScheduledMutation captures a deterministic state change that is applied after
// a matching observation has been seen a configured number of times.
type ScheduledMutation struct {
	Name       string              `yaml:"name" json:"name"`
	Trigger    MutationTrigger     `yaml:"trigger" json:"trigger"`
	Operations []MutationOperation `yaml:"operations" json:"operations"`
}

// MutationTrigger defines which shim observation should increment a mutation.
type MutationTrigger struct {
	Command    ShimCommand `yaml:"command" json:"command"`
	ArgsExact  []string    `yaml:"args_exact,omitempty" json:"args_exact,omitempty"`
	ArgsPrefix []string    `yaml:"args_prefix,omitempty" json:"args_prefix,omitempty"`
	Phase      string      `yaml:"phase,omitempty" json:"phase,omitempty"`
	Script     string      `yaml:"script,omitempty" json:"script,omitempty"`
	Attempt    int         `yaml:"attempt,omitempty" json:"attempt,omitempty"`
	After      int         `yaml:"after,omitempty" json:"after,omitempty"`
}

// MutationOperation describes a single state mutation emitted by a scheduled mutation.
type MutationOperation struct {
	Type      MutationOperationType `yaml:"type" json:"type"`
	Repo      string                `yaml:"repo,omitempty" json:"repo,omitempty"`
	Number    int                   `yaml:"number,omitempty" json:"number,omitempty"`
	Label     string                `yaml:"label,omitempty" json:"label,omitempty"`
	Check     string                `yaml:"check,omitempty" json:"check,omitempty"`
	State     string                `yaml:"state,omitempty" json:"state,omitempty"`
	Body      string                `yaml:"body,omitempty" json:"body,omitempty"`
	CommentID int64                 `yaml:"comment_id,omitempty" json:"comment_id,omitempty"`
}

// MutationOperationType identifies the supported scheduled mutation behaviors.
type MutationOperationType string

const (
	MutationOperationIssueAddLabel    MutationOperationType = "issue_add_label"
	MutationOperationIssueRemoveLabel MutationOperationType = "issue_remove_label"
	MutationOperationIssueAddComment  MutationOperationType = "issue_add_comment"
	MutationOperationPRAddLabel       MutationOperationType = "pr_add_label"
	MutationOperationPRRemoveLabel    MutationOperationType = "pr_remove_label"
	MutationOperationPRSetCheckState  MutationOperationType = "pr_set_check_state"
	MutationOperationPRAddComment     MutationOperationType = "pr_add_comment"
)

// Valid reports whether the mutation operation type is recognized.
func (t MutationOperationType) Valid() bool {
	switch t {
	case MutationOperationIssueAddLabel,
		MutationOperationIssueRemoveLabel,
		MutationOperationIssueAddComment,
		MutationOperationPRAddLabel,
		MutationOperationPRRemoveLabel,
		MutationOperationPRSetCheckState,
		MutationOperationPRAddComment:
		return true
	default:
		return false
	}
}

// RuntimeState stores observation counters and fired mutations for deterministic replay.
type RuntimeState struct {
	Observations     []ObservationCounter `json:"observations,omitempty"`
	AppliedMutations []AppliedMutation    `json:"applied_mutations,omitempty"`
}

// ObservationCounter tracks how many times a trigger key has been observed.
type ObservationCounter struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// AppliedMutation records that a scheduled mutation has already been applied.
type AppliedMutation struct {
	Name      string `json:"name"`
	AppliedAt string `json:"applied_at,omitempty"`
	Key       string `json:"key,omitempty"`
}

// MutationResult reports what happened when recording a scheduled mutation observation.
type MutationResult struct {
	ObservationKey   string              `json:"observation_key"`
	ObservationCount int                 `json:"observation_count"`
	Applied          []ScheduledMutation `json:"applied,omitempty"`
}

// Repository is the DTU source of truth for a GitHub repository and git topology.
type Repository struct {
	Owner         string        `yaml:"owner" json:"owner"`
	Name          string        `yaml:"name" json:"name"`
	DefaultBranch string        `yaml:"default_branch" json:"default_branch"`
	Labels        []Label       `yaml:"labels,omitempty" json:"labels,omitempty"`
	Branches      []Branch      `yaml:"branches,omitempty" json:"branches,omitempty"`
	Worktrees     []Worktree    `yaml:"worktrees,omitempty" json:"worktrees,omitempty"`
	Issues        []Issue       `yaml:"issues,omitempty" json:"issues,omitempty"`
	PullRequests  []PullRequest `yaml:"pull_requests,omitempty" json:"pull_requests,omitempty"`
}

// Slug returns the owner/name identifier used by gh.
func (r Repository) Slug() string {
	return RepoSlug(r.Owner, r.Name)
}

// IssueByNumber returns the matching issue, if present.
func (r *Repository) IssueByNumber(number int) *Issue {
	for i := range r.Issues {
		if r.Issues[i].Number == number {
			return &r.Issues[i]
		}
	}
	return nil
}

// PullRequestByNumber returns the matching pull request, if present.
func (r *Repository) PullRequestByNumber(number int) *PullRequest {
	for i := range r.PullRequests {
		if r.PullRequests[i].Number == number {
			return &r.PullRequests[i]
		}
	}
	return nil
}

// BranchByName returns the matching branch, if present.
func (r *Repository) BranchByName(name string) *Branch {
	for i := range r.Branches {
		if r.Branches[i].Name == name {
			return &r.Branches[i]
		}
	}
	return nil
}

// WorktreeByPath returns the matching worktree, if present.
func (r *Repository) WorktreeByPath(path string) *Worktree {
	for i := range r.Worktrees {
		if r.Worktrees[i].Path == path {
			return &r.Worktrees[i]
		}
	}
	return nil
}

// UpsertBranch adds or updates a git-visible branch for the repository.
func (r *Repository) UpsertBranch(branch Branch) {
	if r == nil {
		return
	}
	branch.Name = strings.TrimSpace(branch.Name)
	if branch.Name == "" {
		return
	}
	branch.SHA = strings.TrimSpace(branch.SHA)
	if existing := r.BranchByName(branch.Name); existing != nil {
		existing.SHA = branch.SHA
		return
	}
	r.Branches = append(r.Branches, branch)
}

// DeleteBranch removes a git-visible branch from the repository.
func (r *Repository) DeleteBranch(name string) bool {
	if r == nil {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for i := range r.Branches {
		if r.Branches[i].Name == name {
			r.Branches = append(r.Branches[:i], r.Branches[i+1:]...)
			return true
		}
	}
	return false
}

// MergePullRequestOptions configures gh pr merge behavior inside the DTU model.
type MergePullRequestOptions struct {
	DeleteHeadBranch bool
	AutoMerge        bool
	AdminMerge       bool
}

// MergePullRequest updates pull-request and git-visible state to reflect a merge.
// When auto-merge is requested and merge-blocking checks are still present, the
// pull request remains open with auto-merge enabled until checks pass. Admin
// merges bypass the queued-auto-merge path and merge immediately.
func (r *Repository) MergePullRequest(number int, opts MergePullRequestOptions) error {
	if r == nil {
		return fmt.Errorf("repository must not be nil")
	}
	pr := r.PullRequestByNumber(number)
	if pr == nil {
		return fmt.Errorf("pull request %d not found", number)
	}

	baseBranch := strings.TrimSpace(pr.BaseBranch)
	if baseBranch == "" {
		return fmt.Errorf("pull request %d: base branch is required", number)
	}
	headBranch := strings.TrimSpace(pr.HeadBranch)
	if headBranch == "" {
		return fmt.Errorf("pull request %d: head branch is required", number)
	}
	headSHA := strings.TrimSpace(pr.HeadSHA)
	if headSHA == "" {
		return fmt.Errorf("pull request %d: head SHA is required", number)
	}

	if opts.AutoMerge && !opts.AdminMerge {
		pr.AutoMergeEnabled = true
		pr.AutoMergeDeleteBranch = opts.DeleteHeadBranch
		if pr.HasBlockingMergeChecks() {
			return nil
		}
	}

	return r.mergePullRequest(pr, opts.DeleteHeadBranch)
}

// ApplyQueuedAutoMerge finalizes a previously queued auto-merge once checks no
// longer block the merge.
func (r *Repository) ApplyQueuedAutoMerge(number int) error {
	if r == nil {
		return fmt.Errorf("repository must not be nil")
	}
	pr := r.PullRequestByNumber(number)
	if pr == nil {
		return fmt.Errorf("pull request %d not found", number)
	}
	if !pr.AutoMergeEnabled || pr.HasBlockingMergeChecks() {
		return nil
	}
	return r.mergePullRequest(pr, pr.AutoMergeDeleteBranch)
}

func (r *Repository) mergePullRequest(pr *PullRequest, deleteHeadBranch bool) error {
	if pr == nil {
		return fmt.Errorf("pull request must not be nil")
	}

	baseBranch := strings.TrimSpace(pr.BaseBranch)
	if baseBranch == "" {
		return fmt.Errorf("pull request %d: base branch is required", pr.Number)
	}
	headBranch := strings.TrimSpace(pr.HeadBranch)
	if headBranch == "" {
		return fmt.Errorf("pull request %d: head branch is required", pr.Number)
	}
	headSHA := strings.TrimSpace(pr.HeadSHA)
	if headSHA == "" {
		return fmt.Errorf("pull request %d: head SHA is required", pr.Number)
	}

	pr.State = PullRequestStateMerged
	pr.Merged = true
	pr.AutoMergeEnabled = false
	pr.AutoMergeDeleteBranch = false
	r.UpsertBranch(Branch{Name: baseBranch, SHA: headSHA})

	if deleteHeadBranch {
		if headBranch != baseBranch {
			r.DeleteBranch(headBranch)
		}
		return nil
	}

	r.UpsertBranch(Branch{Name: headBranch, SHA: headSHA})
	return nil
}

// Label describes a repository label.
type Label struct {
	Name        string `yaml:"name" json:"name"`
	Color       string `yaml:"color,omitempty" json:"color,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Branch describes a git branch known to the universe.
type Branch struct {
	Name string `yaml:"name" json:"name"`
	SHA  string `yaml:"sha,omitempty" json:"sha,omitempty"`
}

// Worktree describes an active git worktree.
type Worktree struct {
	Name   string `yaml:"name,omitempty" json:"name,omitempty"`
	Path   string `yaml:"path" json:"path"`
	Branch string `yaml:"branch" json:"branch"`
}

// Issue describes a GitHub issue.
type Issue struct {
	Number   int        `yaml:"number" json:"number"`
	Title    string     `yaml:"title" json:"title"`
	Body     string     `yaml:"body,omitempty" json:"body,omitempty"`
	URL      string     `yaml:"url,omitempty" json:"url,omitempty"`
	State    IssueState `yaml:"state,omitempty" json:"state,omitempty"`
	Labels   []string   `yaml:"labels,omitempty" json:"labels,omitempty"`
	Comments []Comment  `yaml:"comments,omitempty" json:"comments,omitempty"`
}

// PullRequest describes a GitHub pull request.
type PullRequest struct {
	Number                int                 `yaml:"number" json:"number"`
	Title                 string              `yaml:"title" json:"title"`
	Body                  string              `yaml:"body,omitempty" json:"body,omitempty"`
	URL                   string              `yaml:"url,omitempty" json:"url,omitempty"`
	CreatedAt             time.Time           `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	State                 PullRequestState    `yaml:"state,omitempty" json:"state,omitempty"`
	Merged                bool                `yaml:"merged,omitempty" json:"merged,omitempty"`
	AutoMergeEnabled      bool                `yaml:"auto_merge_enabled,omitempty" json:"auto_merge_enabled,omitempty"`
	AutoMergeDeleteBranch bool                `yaml:"auto_merge_delete_branch,omitempty" json:"auto_merge_delete_branch,omitempty"`
	Mergeable             string              `yaml:"mergeable,omitempty" json:"mergeable,omitempty"`
	ReviewDecision        string              `yaml:"review_decision,omitempty" json:"review_decision,omitempty"`
	Labels                []string            `yaml:"labels,omitempty" json:"labels,omitempty"`
	BaseBranch            string              `yaml:"base_branch" json:"base_branch"`
	HeadBranch            string              `yaml:"head_branch" json:"head_branch"`
	HeadSHA               string              `yaml:"head_sha" json:"head_sha"`
	Comments              []Comment           `yaml:"comments,omitempty" json:"comments,omitempty"`
	Reviews               []Review            `yaml:"reviews,omitempty" json:"reviews,omitempty"`
	ReviewRequests        []string            `yaml:"review_requests,omitempty" json:"review_requests,omitempty"`
	ReviewThreads         []ReviewThread      `yaml:"review_threads,omitempty" json:"review_threads,omitempty"`
	Checks                []Check             `yaml:"checks,omitempty" json:"checks,omitempty"`
	Commits               []PullRequestCommit `yaml:"commits,omitempty" json:"commits,omitempty"`
}

// PullRequestCommit describes a commit visible through `gh pr view --json commits`.
type PullRequestCommit struct {
	OID string `yaml:"oid" json:"oid"`
}

// HasBlockingMergeChecks reports whether any check still prevents a queued
// auto-merge from completing.
func (pr *PullRequest) HasBlockingMergeChecks() bool {
	if pr == nil {
		return false
	}
	for _, check := range pr.Checks {
		switch check.State {
		case CheckStateSuccess, CheckStateSkipped:
			continue
		default:
			return true
		}
	}
	return false
}

// Comment describes an issue or pull request comment.
type Comment struct {
	ID     int64  `yaml:"id,omitempty" json:"id,omitempty"`
	Author string `yaml:"author,omitempty" json:"author,omitempty"`
	Body   string `yaml:"body" json:"body"`
}

// Review describes a pull request review.
type Review struct {
	ID     int64       `yaml:"id,omitempty" json:"id,omitempty"`
	Author string      `yaml:"author,omitempty" json:"author,omitempty"`
	State  ReviewState `yaml:"state,omitempty" json:"state,omitempty"`
	Body   string      `yaml:"body,omitempty" json:"body,omitempty"`
}

// ReviewThread describes a pull request review thread.
type ReviewThread struct {
	IsResolved bool `yaml:"is_resolved,omitempty" json:"is_resolved,omitempty"`
}

// Check describes a pull request check run.
type Check struct {
	ID    int64      `yaml:"id,omitempty" json:"id,omitempty"`
	Name  string     `yaml:"name" json:"name"`
	State CheckState `yaml:"state,omitempty" json:"state,omitempty"`
}

// RepoSlug formats an owner/name repository identifier.
func RepoSlug(owner, name string) string {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

// SplitRepoSlug parses an owner/name repository identifier.
func SplitRepoSlug(slug string) (string, string, error) {
	slug = strings.TrimSpace(slug)
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("repo must be in owner/name format")
	}
	return parts[0], parts[1], nil
}

// RepositoryBySlug returns the matching repository, if present.
func (s *State) RepositoryBySlug(slug string) *Repository {
	for i := range s.Repositories {
		if s.Repositories[i].Slug() == slug {
			return &s.Repositories[i]
		}
	}
	return nil
}

// Repository returns the matching repository, if present.
func (s *State) Repository(owner, name string) *Repository {
	return s.RepositoryBySlug(RepoSlug(owner, name))
}

// ProviderScript returns the matching provider script, if present.
func (s *State) ProviderScript(provider Provider, name string) *ProviderScript {
	scenario := strings.TrimSpace(s.Metadata.Scenario)
	var fallback *ProviderScript
	for i := range s.Providers.Scripts {
		script := &s.Providers.Scripts[i]
		if script.Provider == provider && script.Name == name {
			if scenario != "" && script.Match.Scenario == scenario {
				return script
			}
			if fallback == nil {
				fallback = script
			}
		}
	}
	return fallback
}

// ShimFault returns the matching shim fault, if present.
func (s *State) ShimFault(command ShimCommand, name string) *ShimFault {
	for i := range s.ShimFaults {
		fault := &s.ShimFaults[i]
		if fault.Command == command && fault.Name == name {
			return fault
		}
	}
	return nil
}
