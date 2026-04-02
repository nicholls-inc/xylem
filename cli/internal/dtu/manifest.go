package dtu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadManifest reads and validates a DTU manifest YAML file.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest file %q: %w", path, err)
	}

	var raw struct {
		Version         string           `yaml:"version,omitempty"`
		Metadata        ManifestMetadata `yaml:"metadata"`
		Clock           ClockState       `yaml:"clock,omitempty"`
		Repositories    []Repository     `yaml:"repositories,omitempty"`
		Providers       Providers        `yaml:"providers,omitempty"`
		ProviderScripts []ProviderScript `yaml:"provider_scripts,omitempty"`
		ShimFaults      []struct {
			Name     string         `yaml:"name"`
			Command  ShimCommand    `yaml:"command"`
			Match    ShimFaultMatch `yaml:"match,omitempty"`
			Stdout   string         `yaml:"stdout,omitempty"`
			Stderr   string         `yaml:"stderr,omitempty"`
			ExitCode *int           `yaml:"exit_code,omitempty"`
			Delay    string         `yaml:"delay,omitempty"`
			Hang     bool           `yaml:"hang,omitempty"`
		} `yaml:"shim_faults,omitempty"`
		ScheduledMutations []ScheduledMutation `yaml:"scheduled_mutations,omitempty"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse manifest yaml %q: %w", path, err)
	}
	if len(raw.ProviderScripts) > 0 && len(raw.Providers.Scripts) > 0 {
		return nil, fmt.Errorf("parse manifest yaml %q: provider scripts must be declared in either providers.scripts or provider_scripts, not both", path)
	}
	if len(raw.ProviderScripts) > 0 {
		raw.Providers.Scripts = raw.ProviderScripts
	}

	shimFaults := make([]ShimFault, 0, len(raw.ShimFaults))
	for _, fault := range raw.ShimFaults {
		normalized := ShimFault{
			Name:    fault.Name,
			Command: fault.Command,
			Match:   fault.Match,
			Stdout:  fault.Stdout,
			Stderr:  fault.Stderr,
			Delay:   fault.Delay,
			Hang:    fault.Hang,
		}
		if fault.ExitCode != nil {
			normalized.ExitCode = *fault.ExitCode
			normalized.ExitSet = true
		}
		shimFaults = append(shimFaults, normalized)
	}

	manifest := &Manifest{
		Version:            raw.Version,
		Metadata:           raw.Metadata,
		Clock:              raw.Clock,
		Repositories:       raw.Repositories,
		Providers:          raw.Providers,
		ShimFaults:         shimFaults,
		ScheduledMutations: raw.ScheduledMutations,
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("validate manifest %q: %w", path, err)
	}
	return manifest, nil
}

// Validate checks that a DTU manifest is well formed.
func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("manifest must not be nil")
	}
	if m.Version == "" {
		m.Version = formatVersion
	}
	if m.Version != formatVersion {
		return fmt.Errorf("version must be %q, got %q", formatVersion, m.Version)
	}
	if strings.TrimSpace(m.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if err := validateClock(m.Clock); err != nil {
		return err
	}
	if err := validateRepositories(m.Repositories); err != nil {
		return err
	}
	if err := validateProviderScripts(m.Providers.Scripts); err != nil {
		return err
	}
	if err := validateShimFaults(m.ShimFaults); err != nil {
		return err
	}
	if err := validateScheduledMutations(m.ScheduledMutations); err != nil {
		return err
	}
	return nil
}

// NewState builds a mutable DTU state snapshot from a validated manifest.
func NewState(universeID string, manifest *Manifest, manifestPath string, now time.Time) (*State, error) {
	if err := validatePathComponent(universeID); err != nil {
		return nil, fmt.Errorf("new state: invalid universe ID: %w", err)
	}
	if manifest == nil {
		return nil, fmt.Errorf("new state: manifest must not be nil")
	}
	var clock Clock
	switch {
	case strings.TrimSpace(manifest.Clock.Now) != "":
		resolved, err := ResolveClock(manifest.Clock, nil)
		if err != nil {
			return nil, fmt.Errorf("new state: resolve manifest clock: %w", err)
		}
		clock = resolved
	case !now.IsZero():
		now = now.UTC()
		clock = NewFixedClock(now)
	default:
		clock = SystemClock{}
	}

	state := &State{
		UniverseID:         universeID,
		Version:            manifest.Version,
		Metadata:           cloneMetadata(manifest.Metadata),
		ManifestPath:       filepath.Clean(manifestPath),
		Clock:              manifest.Clock,
		Repositories:       cloneRepositories(manifest.Repositories),
		Providers:          cloneProviders(manifest.Providers),
		ShimFaults:         cloneShimFaults(manifest.ShimFaults),
		ScheduledMutations: cloneScheduledMutations(manifest.ScheduledMutations),
	}
	state.Clock = normalizeClockState(state.Clock, clock)
	state.normalizeWithClock(clock)
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("new state: %w", err)
	}
	return state, nil
}

func cloneMetadata(in ManifestMetadata) ManifestMetadata {
	out := in
	out.Tags = append([]string(nil), in.Tags...)
	return out
}

func cloneProviders(in Providers) Providers {
	out := Providers{Scripts: make([]ProviderScript, len(in.Scripts))}
	for i := range in.Scripts {
		out.Scripts[i] = in.Scripts[i]
		out.Scripts[i].AllowedTools = append([]string(nil), in.Scripts[i].AllowedTools...)
	}
	return out
}

func cloneShimFaults(in []ShimFault) []ShimFault {
	out := make([]ShimFault, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Match.ArgsExact = append([]string(nil), in[i].Match.ArgsExact...)
		out[i].Match.ArgsPrefix = append([]string(nil), in[i].Match.ArgsPrefix...)
	}
	return out
}

func cloneScheduledMutations(in []ScheduledMutation) []ScheduledMutation {
	out := make([]ScheduledMutation, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Trigger.ArgsExact = append([]string(nil), in[i].Trigger.ArgsExact...)
		out[i].Trigger.ArgsPrefix = append([]string(nil), in[i].Trigger.ArgsPrefix...)
		out[i].Operations = append([]MutationOperation(nil), in[i].Operations...)
	}
	return out
}

func cloneRepositories(in []Repository) []Repository {
	out := make([]Repository, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Labels = append([]Label(nil), in[i].Labels...)
		out[i].Branches = append([]Branch(nil), in[i].Branches...)
		out[i].Worktrees = append([]Worktree(nil), in[i].Worktrees...)
		out[i].Issues = cloneIssues(in[i].Issues)
		out[i].PullRequests = clonePullRequests(in[i].PullRequests)
	}
	return out
}

func cloneIssues(in []Issue) []Issue {
	out := make([]Issue, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Labels = append([]string(nil), in[i].Labels...)
		out[i].Comments = append([]Comment(nil), in[i].Comments...)
	}
	return out
}

func clonePullRequests(in []PullRequest) []PullRequest {
	out := make([]PullRequest, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Labels = append([]string(nil), in[i].Labels...)
		out[i].Comments = append([]Comment(nil), in[i].Comments...)
		out[i].Reviews = append([]Review(nil), in[i].Reviews...)
		out[i].Checks = append([]Check(nil), in[i].Checks...)
	}
	return out
}
