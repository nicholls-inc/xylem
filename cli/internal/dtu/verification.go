package dtu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Boundary identifies a twinned external boundary.
type Boundary string

const (
	BoundaryGH      Boundary = "gh"
	BoundaryGit     Boundary = "git"
	BoundaryClaude  Boundary = "claude"
	BoundaryCopilot Boundary = "copilot"
)

// Valid reports whether b is a supported boundary.
func (b Boundary) Valid() bool {
	switch b {
	case BoundaryGH, BoundaryGit, BoundaryClaude, BoundaryCopilot:
		return true
	default:
		return false
	}
}

// DivergenceType classifies the kind of live-vs-twin mismatch being tracked.
type DivergenceType string

const (
	DivergenceTypeShape         DivergenceType = "shape"
	DivergenceTypeFieldOmission DivergenceType = "field_omission"
	DivergenceTypeExitCode      DivergenceType = "exit_code"
	DivergenceTypeLatency       DivergenceType = "latency"
	DivergenceTypeSideEffect    DivergenceType = "side_effect"
)

// Valid reports whether t is a supported divergence type.
func (t DivergenceType) Valid() bool {
	switch t {
	case DivergenceTypeShape, DivergenceTypeFieldOmission, DivergenceTypeExitCode, DivergenceTypeLatency, DivergenceTypeSideEffect:
		return true
	default:
		return false
	}
}

// DivergenceStatus tracks the lifecycle of a known divergence entry.
type DivergenceStatus string

const (
	DivergenceStatusIntentional   DivergenceStatus = "intentional"
	DivergenceStatusTemporary     DivergenceStatus = "temporary"
	DivergenceStatusInvestigating DivergenceStatus = "investigating"
)

// Valid reports whether s is a supported divergence status.
func (s DivergenceStatus) Valid() bool {
	switch s {
	case DivergenceStatusIntentional, DivergenceStatusTemporary, DivergenceStatusInvestigating:
		return true
	default:
		return false
	}
}

// DivergenceRegistry is the machine-readable source of truth for intentional or
// known DTU/live mismatches.
type DivergenceRegistry struct {
	Version     string       `yaml:"version,omitempty" json:"version,omitempty"`
	Divergences []Divergence `yaml:"divergences" json:"divergences"`
}

// Divergence records one known twin/live mismatch.
type Divergence struct {
	Boundary       Boundary         `yaml:"boundary" json:"boundary"`
	CommandOrCase  string           `yaml:"command_or_case" json:"command_or_case"`
	DivergenceType DivergenceType   `yaml:"divergence_type" json:"divergence_type"`
	Status         DivergenceStatus `yaml:"status" json:"status"`
	Rationale      string           `yaml:"rationale" json:"rationale"`
	LiveReference  string           `yaml:"live_reference" json:"live_reference"`
}

// AttributionClassification identifies who should own a failure first.
type AttributionClassification string

const (
	AttributionClassificationXylemBug        AttributionClassification = "xylem_bug"
	AttributionClassificationTwinBug         AttributionClassification = "twin_bug"
	AttributionClassificationFidelityBug     AttributionClassification = "fidelity_bug"
	AttributionClassificationMissingFidelity AttributionClassification = "missing_fidelity"
)

// Valid reports whether c is a supported attribution classification.
func (c AttributionClassification) Valid() bool {
	switch c {
	case AttributionClassificationXylemBug,
		AttributionClassificationTwinBug,
		AttributionClassificationFidelityBug,
		AttributionClassificationMissingFidelity:
		return true
	default:
		return false
	}
}

// AttributionPolicy captures the bug-attribution decision rules for DTU triage.
type AttributionPolicy struct {
	Version string            `yaml:"version,omitempty" json:"version,omitempty"`
	Rules   []AttributionRule `yaml:"rules" json:"rules"`
}

// AttributionRule describes one classification rule and the next step it implies.
type AttributionRule struct {
	Name           string                    `yaml:"name" json:"name"`
	When           string                    `yaml:"when" json:"when"`
	Classification AttributionClassification `yaml:"classification" json:"classification"`
	NextStep       string                    `yaml:"next_step" json:"next_step"`
}

// LiveVerificationSuite defines env-gated live differential/canary scaffolding.
type LiveVerificationSuite struct {
	Version      string                 `yaml:"version,omitempty" json:"version,omitempty"`
	Differential []LiveVerificationCase `yaml:"differential,omitempty" json:"differential,omitempty"`
	Canaries     []LiveVerificationCase `yaml:"canaries,omitempty" json:"canaries,omitempty"`
	baseDir      string                 `yaml:"-" json:"-"`
}

// LiveVerificationCase describes one live differential or canary command.
type LiveVerificationCase struct {
	Name       string   `yaml:"name" json:"name"`
	Boundary   Boundary `yaml:"boundary" json:"boundary"`
	EnabledEnv string   `yaml:"enabled_env" json:"enabled_env"`
	Command    string   `yaml:"command" json:"command"`
	Args       []string `yaml:"args,omitempty" json:"args,omitempty"`
	Normalizer string   `yaml:"normalizer,omitempty" json:"normalizer,omitempty"`
	Fixture    string   `yaml:"fixture,omitempty" json:"fixture,omitempty"`
	Phase      string   `yaml:"phase,omitempty" json:"phase,omitempty"`
	Script     string   `yaml:"script,omitempty" json:"script,omitempty"`
	Attempt    int      `yaml:"attempt,omitempty" json:"attempt,omitempty"`
	Fault      string   `yaml:"fault,omitempty" json:"fault,omitempty"`
}

// LoadDivergenceRegistry reads and validates a divergence registry YAML file.
func LoadDivergenceRegistry(path string) (*DivergenceRegistry, error) {
	var registry DivergenceRegistry
	if err := loadVerificationYAML(path, &registry); err != nil {
		return nil, err
	}
	if err := registry.Validate(); err != nil {
		return nil, fmt.Errorf("validate divergence registry %q: %w", path, err)
	}
	return &registry, nil
}

// Validate checks that a divergence registry is well formed.
func (r *DivergenceRegistry) Validate() error {
	if r == nil {
		return fmt.Errorf("divergence registry must not be nil")
	}
	if r.Version == "" {
		r.Version = formatVersion
	}
	if r.Version != formatVersion {
		return fmt.Errorf("version must be %q, got %q", formatVersion, r.Version)
	}
	if len(r.Divergences) == 0 {
		return fmt.Errorf("divergences must contain at least one entry")
	}
	seen := make(map[string]struct{}, len(r.Divergences))
	for _, divergence := range r.Divergences {
		if !divergence.Boundary.Valid() {
			return fmt.Errorf("divergence %q: invalid boundary %q", divergence.CommandOrCase, divergence.Boundary)
		}
		commandOrCase := strings.TrimSpace(divergence.CommandOrCase)
		if commandOrCase == "" {
			return fmt.Errorf("divergence command_or_case is required")
		}
		if !divergence.DivergenceType.Valid() {
			return fmt.Errorf("divergence %q: invalid divergence_type %q", commandOrCase, divergence.DivergenceType)
		}
		if !divergence.Status.Valid() {
			return fmt.Errorf("divergence %q: invalid status %q", commandOrCase, divergence.Status)
		}
		if strings.TrimSpace(divergence.Rationale) == "" {
			return fmt.Errorf("divergence %q: rationale is required", commandOrCase)
		}
		if strings.TrimSpace(divergence.LiveReference) == "" {
			return fmt.Errorf("divergence %q: live_reference is required", commandOrCase)
		}
		key := string(divergence.Boundary) + ":" + commandOrCase
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate divergence entry for %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// LoadAttributionPolicy reads and validates an attribution policy YAML file.
func LoadAttributionPolicy(path string) (*AttributionPolicy, error) {
	var policy AttributionPolicy
	if err := loadVerificationYAML(path, &policy); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("validate attribution policy %q: %w", path, err)
	}
	return &policy, nil
}

// Validate checks that an attribution policy is well formed.
func (p *AttributionPolicy) Validate() error {
	if p == nil {
		return fmt.Errorf("attribution policy must not be nil")
	}
	if p.Version == "" {
		p.Version = formatVersion
	}
	if p.Version != formatVersion {
		return fmt.Errorf("version must be %q, got %q", formatVersion, p.Version)
	}
	if len(p.Rules) == 0 {
		return fmt.Errorf("rules must contain at least one entry")
	}
	seen := make(map[string]struct{}, len(p.Rules))
	for _, rule := range p.Rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			return fmt.Errorf("rule name is required")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate attribution rule %q", name)
		}
		seen[name] = struct{}{}
		if strings.TrimSpace(rule.When) == "" {
			return fmt.Errorf("rule %q: when is required", name)
		}
		if !rule.Classification.Valid() {
			return fmt.Errorf("rule %q: invalid classification %q", name, rule.Classification)
		}
		if strings.TrimSpace(rule.NextStep) == "" {
			return fmt.Errorf("rule %q: next_step is required", name)
		}
	}
	return nil
}

// LoadLiveVerificationSuite reads and validates a live verification suite YAML file.
func LoadLiveVerificationSuite(path string) (*LiveVerificationSuite, error) {
	var suite LiveVerificationSuite
	if err := loadVerificationYAML(path, &suite); err != nil {
		return nil, err
	}
	suite.baseDir = filepath.Dir(path)
	if err := suite.Validate(); err != nil {
		return nil, fmt.Errorf("validate live verification suite %q: %w", path, err)
	}
	return &suite, nil
}

// Validate checks that a live verification suite is well formed.
func (s *LiveVerificationSuite) Validate() error {
	if s == nil {
		return fmt.Errorf("live verification suite must not be nil")
	}
	if s.Version == "" {
		s.Version = formatVersion
	}
	if s.Version != formatVersion {
		return fmt.Errorf("version must be %q, got %q", formatVersion, s.Version)
	}
	if len(s.Differential) == 0 && len(s.Canaries) == 0 {
		return fmt.Errorf("at least one differential or canary case is required")
	}
	if err := validateLiveVerificationCases("differential", s.Differential); err != nil {
		return err
	}
	if err := validateLiveVerificationCases("canary", s.Canaries); err != nil {
		return err
	}
	return nil
}

// EnabledDifferential returns live differential cases enabled by the supplied env lookup.
func (s *LiveVerificationSuite) EnabledDifferential(lookup func(string) (string, bool)) []LiveVerificationCase {
	return enabledLiveVerificationCases(s.Differential, lookup)
}

// EnabledCanaries returns live canary cases enabled by the supplied env lookup.
func (s *LiveVerificationSuite) EnabledCanaries(lookup func(string) (string, bool)) []LiveVerificationCase {
	return enabledLiveVerificationCases(s.Canaries, lookup)
}

// BaseDir returns the directory used to resolve relative verification assets.
func (s *LiveVerificationSuite) BaseDir() string {
	if s == nil {
		return ""
	}
	return s.baseDir
}

// ResolveFixturePath resolves a verification fixture relative to the suite file.
func (s *LiveVerificationSuite) ResolveFixturePath(fixture string) (string, error) {
	fixture = strings.TrimSpace(fixture)
	if fixture == "" {
		return "", fmt.Errorf("fixture path is required")
	}
	if filepath.IsAbs(fixture) || s == nil || strings.TrimSpace(s.baseDir) == "" {
		return filepath.Clean(fixture), nil
	}
	resolved := filepath.Clean(filepath.Join(s.baseDir, fixture))
	rel, err := filepath.Rel(s.baseDir, resolved)
	if err != nil {
		return "", fmt.Errorf("resolve fixture path %q: %w", fixture, err)
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("fixture path %q must stay within %s", fixture, s.baseDir)
	}
	return resolved, nil
}

func validateLiveVerificationCases(kind string, cases []LiveVerificationCase) error {
	seen := make(map[string]struct{}, len(cases))
	for _, verificationCase := range cases {
		name := strings.TrimSpace(verificationCase.Name)
		if name == "" {
			return fmt.Errorf("%s case name is required", kind)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate %s case %q", kind, name)
		}
		seen[name] = struct{}{}
		if !verificationCase.Boundary.Valid() {
			return fmt.Errorf("%s case %q: invalid boundary %q", kind, name, verificationCase.Boundary)
		}
		if strings.TrimSpace(verificationCase.EnabledEnv) == "" {
			return fmt.Errorf("%s case %q: enabled_env is required", kind, name)
		}
		if strings.TrimSpace(verificationCase.Command) == "" {
			return fmt.Errorf("%s case %q: command is required", kind, name)
		}
		if verificationCase.Attempt < 0 {
			return fmt.Errorf("%s case %q: attempt must be non-negative", kind, name)
		}
		if kind == "differential" && strings.TrimSpace(verificationCase.Normalizer) == "" {
			return fmt.Errorf("%s case %q: normalizer is required", kind, name)
		}
		if kind == "differential" && strings.TrimSpace(verificationCase.Fixture) == "" {
			return fmt.Errorf("%s case %q: fixture is required", kind, name)
		}
		if normalizerName := strings.TrimSpace(verificationCase.Normalizer); normalizerName != "" {
			if _, ok := LookupVerificationNormalizer(normalizerName); !ok {
				return fmt.Errorf("%s case %q: unknown normalizer %q", kind, name, normalizerName)
			}
		}
	}
	return nil
}

func enabledLiveVerificationCases(cases []LiveVerificationCase, lookup func(string) (string, bool)) []LiveVerificationCase {
	if len(cases) == 0 {
		return nil
	}
	out := make([]LiveVerificationCase, 0, len(cases))
	for _, verificationCase := range cases {
		if isLiveVerificationCaseEnabled(verificationCase.EnabledEnv, lookup) {
			out = append(out, verificationCase)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isLiveVerificationCaseEnabled(envName string, lookup func(string) (string, bool)) bool {
	envName = strings.TrimSpace(envName)
	if envName == "" || lookup == nil {
		return false
	}
	value, ok := lookup(envName)
	return ok && strings.TrimSpace(value) != ""
}

func loadVerificationYAML(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read verification asset %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse verification yaml %q: %w", path, err)
	}
	return nil
}
