package dtu

import (
	"fmt"
	"strings"
	"time"
)

// PreviewObservation applies an observation to a cloned state so callers can
// render against the next deterministic state without persisting it yet.
func PreviewObservation(state *State, inv ShimInvocation) (*State, *MutationResult, error) {
	if state == nil {
		return nil, nil, fmt.Errorf("preview observation: state must not be nil")
	}
	cloned, err := cloneState(state)
	if err != nil {
		return nil, nil, fmt.Errorf("preview observation: clone state: %w", err)
	}
	clock, err := ResolveClock(cloned.Clock, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("preview observation: resolve clock: %w", err)
	}
	result, err := observeState(cloned, inv, clock)
	if err != nil {
		return nil, nil, fmt.Errorf("preview observation: %w", err)
	}
	return cloned, result, nil
}

// RecordObservation increments runtime counters for a shim invocation and applies
// any scheduled mutations whose thresholds have been reached.
func (s *Store) RecordObservation(inv ShimInvocation) (*MutationResult, error) {
	if !inv.Command.Valid() {
		return nil, fmt.Errorf("record observation: invalid command %q", inv.Command)
	}

	var result *MutationResult
	err := s.Update(func(state *State) error {
		var err error
		clock, err := ResolveClock(state.Clock, s.clockOrDefault())
		if err != nil {
			return fmt.Errorf("record observation: resolve clock: %w", err)
		}
		result, err = observeState(state, inv, clock)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func observeState(state *State, inv ShimInvocation, clock Clock) (*MutationResult, error) {
	if state == nil {
		return nil, fmt.Errorf("record observation: state must not be nil")
	}
	if !inv.Command.Valid() {
		return nil, fmt.Errorf("record observation: invalid command %q", inv.Command)
	}
	if clock == nil {
		var err error
		clock, err = ResolveClock(state.Clock, nil)
		if err != nil {
			return nil, fmt.Errorf("record observation: resolve clock: %w", err)
		}
	}
	observationKey := ObservationKey(inv)
	count := incrementObservationCount(&state.Runtime, observationKey)
	applied, err := applyScheduledMutations(state, inv, observationKey, count, clock)
	if err != nil {
		return nil, err
	}
	return &MutationResult{
		ObservationKey:   observationKey,
		ObservationCount: count,
		Applied:          applied,
	}, nil
}

// ObservationKey produces the stable runtime key used for deterministic mutation scheduling.
func ObservationKey(inv ShimInvocation) string {
	parts := []string{
		string(inv.Command),
		strings.TrimSpace(inv.Phase),
		strings.TrimSpace(inv.Script),
		fmt.Sprintf("%d", inv.Attempt),
		strings.Join(normalizeMatchArgs(inv.Args), "\x1f"),
	}
	return strings.Join(parts, "|")
}

func incrementObservationCount(runtime *RuntimeState, key string) int {
	if runtime == nil {
		return 0
	}
	for i := range runtime.Observations {
		if runtime.Observations[i].Key == key {
			runtime.Observations[i].Count++
			return runtime.Observations[i].Count
		}
	}
	runtime.Observations = append(runtime.Observations, ObservationCounter{
		Key:   key,
		Count: 1,
	})
	return 1
}

func applyScheduledMutations(state *State, inv ShimInvocation, observationKey string, count int, clock Clock) ([]ScheduledMutation, error) {
	if state == nil {
		return nil, fmt.Errorf("apply scheduled mutations: state must not be nil")
	}
	if clock == nil {
		clock = SystemClock{}
	}

	var applied []ScheduledMutation
	for i := range state.ScheduledMutations {
		mutation := state.ScheduledMutations[i]
		if !MatchScheduledMutation(&mutation, inv) {
			continue
		}
		if mutationAlreadyApplied(state.Runtime, mutation.Name, observationKey) {
			continue
		}
		if count <= mutation.Trigger.After {
			continue
		}
		if err := applyMutationOperations(state, mutation); err != nil {
			return nil, fmt.Errorf("apply scheduled mutation %q: %w", mutation.Name, err)
		}
		state.Runtime.AppliedMutations = append(state.Runtime.AppliedMutations, AppliedMutation{
			Name:      mutation.Name,
			Key:       observationKey,
			AppliedAt: clock.Now().UTC().Format(time.RFC3339Nano),
		})
		applied = append(applied, mutation)
	}
	return applied, nil
}

func mutationAlreadyApplied(runtime RuntimeState, name string, key string) bool {
	for _, applied := range runtime.AppliedMutations {
		if applied.Name == name && applied.Key == key {
			return true
		}
	}
	return false
}

func applyMutationOperations(state *State, mutation ScheduledMutation) error {
	for _, operation := range mutation.Operations {
		repo := state.RepositoryBySlug(operation.Repo)
		if repo == nil {
			return fmt.Errorf("repository %q not found", operation.Repo)
		}

		switch operation.Type {
		case MutationOperationIssueAddLabel:
			issue := repo.IssueByNumber(operation.Number)
			if issue == nil {
				return fmt.Errorf("issue %d not found in %s", operation.Number, operation.Repo)
			}
			issue.Labels = mutateLabels(issue.Labels, []string{operation.Label}, nil)
		case MutationOperationIssueRemoveLabel:
			issue := repo.IssueByNumber(operation.Number)
			if issue == nil {
				return fmt.Errorf("issue %d not found in %s", operation.Number, operation.Repo)
			}
			issue.Labels = mutateLabels(issue.Labels, nil, []string{operation.Label})
		case MutationOperationIssueAddComment:
			issue := repo.IssueByNumber(operation.Number)
			if issue == nil {
				return fmt.Errorf("issue %d not found in %s", operation.Number, operation.Repo)
			}
			issue.Comments = append(issue.Comments, Comment{
				ID:   state.AdvanceCommentID(),
				Body: operation.Body,
			})
		case MutationOperationPRAddLabel:
			pr := repo.PullRequestByNumber(operation.Number)
			if pr == nil {
				return fmt.Errorf("pull request %d not found in %s", operation.Number, operation.Repo)
			}
			pr.Labels = mutateLabels(pr.Labels, []string{operation.Label}, nil)
		case MutationOperationPRRemoveLabel:
			pr := repo.PullRequestByNumber(operation.Number)
			if pr == nil {
				return fmt.Errorf("pull request %d not found in %s", operation.Number, operation.Repo)
			}
			pr.Labels = mutateLabels(pr.Labels, nil, []string{operation.Label})
		case MutationOperationPRSetCheckState:
			pr := repo.PullRequestByNumber(operation.Number)
			if pr == nil {
				return fmt.Errorf("pull request %d not found in %s", operation.Number, operation.Repo)
			}
			check := findOrAppendCheck(state, pr, operation.Check)
			check.State = CheckState(operation.State)
		case MutationOperationPRAddComment:
			pr := repo.PullRequestByNumber(operation.Number)
			if pr == nil {
				return fmt.Errorf("pull request %d not found in %s", operation.Number, operation.Repo)
			}
			pr.Comments = append(pr.Comments, Comment{
				ID:   state.AdvanceCommentID(),
				Body: operation.Body,
			})
		default:
			return fmt.Errorf("unsupported mutation operation %q", operation.Type)
		}
	}
	return nil
}

func findOrAppendCheck(state *State, pr *PullRequest, name string) *Check {
	if pr == nil {
		return nil
	}
	for i := range pr.Checks {
		if pr.Checks[i].Name == name {
			return &pr.Checks[i]
		}
	}
	pr.Checks = append(pr.Checks, Check{
		ID:    state.AdvanceCheckID(),
		Name:  name,
		State: CheckStatePending,
	})
	return &pr.Checks[len(pr.Checks)-1]
}

func mutateLabels(existing, add, remove []string) []string {
	labelSet := make(map[string]struct{}, len(existing)+len(add))
	for _, label := range existing {
		if trimmed := strings.TrimSpace(label); trimmed != "" {
			labelSet[trimmed] = struct{}{}
		}
	}
	for _, label := range add {
		if trimmed := strings.TrimSpace(label); trimmed != "" {
			labelSet[trimmed] = struct{}{}
		}
	}
	for _, label := range remove {
		delete(labelSet, strings.TrimSpace(label))
	}
	out := make([]string, 0, len(labelSet))
	for label := range labelSet {
		out = append(out, label)
	}
	return normalizeStrings(out)
}
