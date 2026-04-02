package dtu

import (
	"fmt"
	"reflect"
	"strings"
)

// ProviderInvocation describes a provider process invocation for script matching.
type ProviderInvocation struct {
	Provider     Provider
	ScriptName   string
	Scenario     string
	Phase        string
	Attempt      int
	Prompt       string
	Model        string
	AllowedTools []string
}

// ShimInvocation describes a shimmed command invocation for fault matching.
type ShimInvocation struct {
	Command   ShimCommand
	FaultName string
	Args      []string
	Phase     string
	Script    string
	Attempt   int
}

// Matches reports whether the provider script match accepts the invocation.
func (m ProviderScriptMatch) Matches(inv ProviderInvocation) bool {
	if m.Scenario != "" && m.Scenario != inv.Scenario {
		return false
	}
	if m.Phase != "" && m.Phase != inv.Phase {
		return false
	}
	if m.Attempt > 0 && m.Attempt != inv.Attempt {
		return false
	}
	if m.PromptExact != "" && m.PromptExact != inv.Prompt {
		return false
	}
	if m.PromptContains != "" && !strings.Contains(inv.Prompt, m.PromptContains) {
		return false
	}
	return true
}

// Matches reports whether the shim fault match accepts the invocation.
func (m ShimFaultMatch) Matches(inv ShimInvocation) bool {
	if m.Phase != "" && m.Phase != inv.Phase {
		return false
	}
	if m.Script != "" && m.Script != inv.Script {
		return false
	}
	if m.Attempt > 0 && m.Attempt != inv.Attempt {
		return false
	}
	if len(m.ArgsExact) > 0 && !reflect.DeepEqual(m.ArgsExact, inv.Args) {
		return false
	}
	if len(m.ArgsPrefix) > 0 && !hasStringPrefix(inv.Args, m.ArgsPrefix) {
		return false
	}
	return true
}

// Matches reports whether the scheduled mutation trigger accepts the invocation.
func (m MutationTrigger) Matches(inv ShimInvocation) bool {
	if m.Command != inv.Command {
		return false
	}
	if m.Phase != "" && m.Phase != inv.Phase {
		return false
	}
	if m.Script != "" && m.Script != inv.Script {
		return false
	}
	if m.Attempt > 0 && m.Attempt != inv.Attempt {
		return false
	}
	if len(m.ArgsExact) > 0 && !reflect.DeepEqual(m.ArgsExact, inv.Args) {
		return false
	}
	if len(m.ArgsPrefix) > 0 && !hasStringPrefix(inv.Args, m.ArgsPrefix) {
		return false
	}
	return true
}

// MatchProviderScript reports whether script matches the provider invocation.
func MatchProviderScript(script *ProviderScript, inv ProviderInvocation) bool {
	if script == nil {
		return false
	}
	if script.Provider != inv.Provider {
		return false
	}
	if inv.ScriptName != "" && script.Name != inv.ScriptName {
		return false
	}
	if !script.Match.Matches(inv) {
		return false
	}
	if script.Model != "" && script.Model != inv.Model {
		return false
	}
	if len(script.AllowedTools) > 0 && !reflect.DeepEqual(script.AllowedTools, normalizeStrings(inv.AllowedTools)) {
		return false
	}
	return true
}

// MatchShimFault reports whether fault matches the shim invocation.
func MatchShimFault(fault *ShimFault, inv ShimInvocation) bool {
	if fault == nil {
		return false
	}
	if fault.Command != inv.Command {
		return false
	}
	if inv.FaultName != "" && fault.Name != inv.FaultName {
		return false
	}
	return fault.Match.Matches(inv)
}

// MatchScheduledMutation reports whether a scheduled mutation matches the shim invocation.
func MatchScheduledMutation(mutation *ScheduledMutation, inv ShimInvocation) bool {
	if mutation == nil {
		return false
	}
	return mutation.Trigger.Matches(inv)
}

// SelectProviderScript finds the best matching provider script.
func (s *State) SelectProviderScript(inv ProviderInvocation) (*ProviderScript, error) {
	if s == nil {
		return nil, fmt.Errorf("state must not be nil")
	}
	if !inv.Provider.Valid() {
		return nil, fmt.Errorf("provider %q is invalid", inv.Provider)
	}
	if inv.Scenario == "" {
		inv.Scenario = strings.TrimSpace(s.Metadata.Scenario)
	}
	if inv.ScriptName != "" && s.hasProviderScriptNamed(inv.Provider, inv.Scenario, inv.ScriptName) {
		script := s.bestMatchingProviderScript(inv)
		if script != nil {
			return script, nil
		}
		return nil, fmt.Errorf("no matching provider script named %q for provider %q", inv.ScriptName, inv.Provider)
	}

	fallback := inv
	fallback.ScriptName = ""
	if script := s.bestMatchingProviderScript(fallback); script != nil {
		return script, nil
	}
	return nil, fmt.Errorf("no matching provider script for provider %q", inv.Provider)
}

// SelectShimFault finds the best matching shim fault.
func (s *State) SelectShimFault(inv ShimInvocation) (*ShimFault, error) {
	if s == nil {
		return nil, fmt.Errorf("state must not be nil")
	}
	if !inv.Command.Valid() {
		return nil, fmt.Errorf("command %q is invalid", inv.Command)
	}
	var best *ShimFault
	bestScore := -1
	for i := range s.ShimFaults {
		fault := &s.ShimFaults[i]
		if MatchShimFault(fault, inv) {
			score := shimFaultSpecificity(fault, inv)
			if best == nil || score > bestScore {
				best = fault
				bestScore = score
			}
		}
	}
	if best != nil {
		return best, nil
	}
	if inv.FaultName != "" {
		return nil, fmt.Errorf("no matching shim fault named %q for command %q", inv.FaultName, inv.Command)
	}
	return nil, fmt.Errorf("no matching shim fault for command %q", inv.Command)
}

// MatchingScheduledMutations returns scheduled mutations whose triggers match the invocation.
func (s *State) MatchingScheduledMutations(inv ShimInvocation) []ScheduledMutation {
	if s == nil || !inv.Command.Valid() {
		return nil
	}
	matches := make([]ScheduledMutation, 0, len(s.ScheduledMutations))
	for i := range s.ScheduledMutations {
		mutation := s.ScheduledMutations[i]
		if MatchScheduledMutation(&mutation, inv) {
			matches = append(matches, mutation)
		}
	}
	return matches
}

func providerScriptSpecificity(script *ProviderScript, inv ProviderInvocation) int {
	if script == nil {
		return -1
	}
	score := 0
	if script.Match.Scenario != "" && script.Match.Scenario == inv.Scenario {
		score += 500
	}
	if script.Match.Attempt > 0 && script.Match.Attempt == inv.Attempt {
		score += 1000
	}
	if script.Match.Phase != "" {
		score += 100
	}
	if script.Match.PromptExact != "" {
		score += 80
	}
	if script.Match.PromptContains != "" {
		score += 40
	}
	if script.Model != "" {
		score += 20
	}
	if len(script.AllowedTools) > 0 {
		score += 10 + len(script.AllowedTools)
	}
	return score
}

func (s *State) hasProviderScriptNamed(provider Provider, scenario string, name string) bool {
	scenario = strings.TrimSpace(scenario)
	for i := range s.Providers.Scripts {
		script := &s.Providers.Scripts[i]
		if script.Provider != provider || script.Name != name {
			continue
		}
		scriptScenario := strings.TrimSpace(script.Match.Scenario)
		if scriptScenario == "" || scriptScenario == scenario {
			return true
		}
	}
	return false
}

func (s *State) bestMatchingProviderScript(inv ProviderInvocation) *ProviderScript {
	var best *ProviderScript
	bestScore := -1
	for i := range s.Providers.Scripts {
		script := &s.Providers.Scripts[i]
		if !MatchProviderScript(script, inv) {
			continue
		}
		score := providerScriptSpecificity(script, inv)
		if best == nil || score > bestScore {
			best = script
			bestScore = score
		}
	}
	return best
}

func shimFaultSpecificity(fault *ShimFault, inv ShimInvocation) int {
	if fault == nil {
		return -1
	}
	score := 0
	if fault.Match.Attempt > 0 && fault.Match.Attempt == inv.Attempt {
		score += 1000
	}
	if fault.Match.Script != "" {
		score += 200
	}
	if fault.Match.Phase != "" {
		score += 100
	}
	if len(fault.Match.ArgsExact) > 0 {
		score += 50 + len(fault.Match.ArgsExact)
	}
	if len(fault.Match.ArgsPrefix) > 0 {
		score += 25 + len(fault.Match.ArgsPrefix)
	}
	return score
}

func hasStringPrefix(values, prefix []string) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(values) < len(prefix) {
		return false
	}
	for i := range prefix {
		if values[i] != prefix[i] {
			return false
		}
	}
	return true
}
