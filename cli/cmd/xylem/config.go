package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/profiles"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

type adaptRepoPlan struct {
	SchemaVersion int                     `json:"schema_version"`
	Detected      adaptRepoPlanDetected   `json:"detected"`
	Planned       []adaptRepoPlannedAsset `json:"planned_changes"`
	Skipped       []adaptRepoSkippedAsset `json:"skipped"`
}

type adaptRepoPlanDetected struct {
	Languages   []string `json:"languages"`
	BuildTools  []string `json:"build_tools"`
	TestRunners []string `json:"test_runners"`
	Linters     []string `json:"linters"`
	HasFrontend bool     `json:"has_frontend"`
	HasDatabase bool     `json:"has_database"`
	EntryPoints []string `json:"entry_points"`
}

type adaptRepoPlannedAsset struct {
	Path        string `json:"path"`
	Op          string `json:"op"`
	Rationale   string `json:"rationale"`
	DiffSummary string `json:"diff_summary,omitempty"`
}

type adaptRepoSkippedAsset struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type profileLock struct {
	ProfileVersion int                 `yaml:"profile_version"`
	Profiles       []profileLockRecord `yaml:"profiles"`
	LockedAt       string              `yaml:"locked_at"`
}

type profileLockRecord struct {
	Name    string `yaml:"name"`
	Version int    `yaml:"version"`
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate xylem config",
	}
	cmd.AddCommand(newConfigValidateCmd())
	return cmd
}

func newConfigValidateCmd() *cobra.Command {
	var proposedPath string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate .xylem.yml and related scaffold metadata",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdConfigValidate(viper.GetString("config"), proposedPath, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&proposedPath, "proposed", "", "Apply a proposed adapt-plan.json to config in memory before validation")
	return cmd
}

func cmdConfigValidate(configPath, proposedPath string, stdout io.Writer) error {
	cfg, err := config.LoadUnvalidated(configPath)
	if err != nil {
		return fmt.Errorf("load config for validation: %w", err)
	}

	var plan *adaptRepoPlan
	if strings.TrimSpace(proposedPath) != "" {
		plan, err = loadAdaptRepoPlan(proposedPath)
		if err != nil {
			return err
		}
		if err := applyAdaptRepoPlan(cfg, plan); err != nil {
			return err
		}
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	if err := validateConfiguredProfiles(cfg); err != nil {
		return err
	}
	if err := validateProfileLock(configPath, cfg, plan); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Config valid.")
	if len(cfg.Profiles) > 0 {
		fmt.Fprintf(stdout, "Profiles: %s\n", strings.Join(cfg.Profiles, ", "))
	}
	fmt.Fprintf(stdout, "Policy mode: %s\n", cfg.HarnessPolicyMode())
	fmt.Fprintf(stdout, "Protected surfaces: %d\n", len(cfg.EffectiveProtectedSurfaces()))
	fmt.Fprintf(stdout, "Auto-merge labels: %s\n", strings.Join(cfg.Daemon.EffectiveAutoMergeLabels(), ", "))
	return nil
}

func loadAdaptRepoPlan(path string) (*adaptRepoPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read proposed plan %q: %w", path, err)
	}

	var plan adaptRepoPlan
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return nil, fmt.Errorf("parse proposed plan %q: %w", path, err)
	}
	if err := dec.Decode(new(struct{})); err != io.EOF {
		return nil, fmt.Errorf("parse proposed plan %q: trailing data", path)
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("validate proposed plan %q: %w", path, err)
	}
	return &plan, nil
}

func (p *adaptRepoPlan) Validate() error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must equal 1")
	}
	if p.Skipped == nil {
		return fmt.Errorf("skipped must be present")
	}
	for i, change := range p.Planned {
		if strings.TrimSpace(change.Path) == "" {
			return fmt.Errorf("planned_changes[%d].path must be non-empty", i)
		}
		if !isAllowedAdaptPlanPath(change.Path) {
			return fmt.Errorf("planned_changes[%d].path %q is outside the allowed harness surfaces", i, change.Path)
		}
		switch change.Op {
		case "patch", "replace", "create", "delete":
		default:
			return fmt.Errorf("planned_changes[%d].op %q is invalid", i, change.Op)
		}
		if change.Op == "delete" && !strings.HasPrefix(change.Path, ".xylem/workflows/") {
			return fmt.Errorf("planned_changes[%d].op delete is only allowed under .xylem/workflows/", i)
		}
		if strings.TrimSpace(change.Rationale) == "" {
			return fmt.Errorf("planned_changes[%d].rationale must be non-empty", i)
		}
	}
	for i, skipped := range p.Skipped {
		if strings.TrimSpace(skipped.Path) == "" {
			return fmt.Errorf("skipped[%d].path must be non-empty", i)
		}
		if !isAllowedAdaptPlanPath(skipped.Path) {
			return fmt.Errorf("skipped[%d].path %q is outside the allowed harness surfaces", i, skipped.Path)
		}
		if strings.TrimSpace(skipped.Reason) == "" {
			return fmt.Errorf("skipped[%d].reason must be non-empty", i)
		}
	}
	return nil
}

func applyAdaptRepoPlan(cfg *config.Config, plan *adaptRepoPlan) error {
	if cfg == nil || plan == nil {
		return nil
	}
	for _, change := range plan.Planned {
		if change.Path != ".xylem.yml" {
			continue
		}
		if change.Op == "delete" {
			return fmt.Errorf("validate proposed config: .xylem.yml cannot be deleted")
		}
		if err := applyConfigDiffSummary(cfg, change.DiffSummary); err != nil {
			return fmt.Errorf("validate proposed config: %w", err)
		}
	}
	return nil
}

func applyConfigDiffSummary(cfg *config.Config, summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil
	}
	for _, raw := range strings.FieldsFunc(summary, func(r rune) bool {
		return r == ';' || r == '\n'
	}) {
		segment := strings.TrimSpace(raw)
		if segment == "" {
			continue
		}
		key, value, ok := strings.Cut(segment, ":")
		if !ok {
			return fmt.Errorf("unsupported .xylem.yml diff_summary segment %q", segment)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("unsupported .xylem.yml diff_summary key %q with empty value", key)
		}
		switch key {
		case "validation.format":
			cfg.Validation.Format = value
		case "validation.lint":
			cfg.Validation.Lint = value
		case "validation.build":
			cfg.Validation.Build = value
		case "validation.test":
			cfg.Validation.Test = value
		case "profiles":
			profiles, err := parseProposedProfiles(value)
			if err != nil {
				return fmt.Errorf("parse profiles override: %w", err)
			}
			cfg.Profiles = profiles
		default:
			return fmt.Errorf("unsupported .xylem.yml diff_summary key %q", key)
		}
	}
	return nil
}

func parseProposedProfiles(value string) ([]string, error) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "["), "]"))
	if trimmed == "" {
		return nil, fmt.Errorf("profiles override must be non-empty")
	}
	parts := strings.Split(trimmed, ",")
	profiles := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.Trim(strings.TrimSpace(part), `"'`)
		if name == "" {
			continue
		}
		profiles = append(profiles, name)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("profiles override must include at least one profile")
	}
	return profiles, nil
}

func validateConfiguredProfiles(cfg *config.Config) error {
	if cfg == nil || len(cfg.Profiles) == 0 {
		return nil
	}
	if _, err := profiles.Compose(cfg.Profiles...); err != nil {
		return fmt.Errorf("validate configured profiles: %w", err)
	}
	return nil
}

func validateProfileLock(configPath string, cfg *config.Config, plan *adaptRepoPlan) error {
	if cfg == nil || len(cfg.Profiles) == 0 || adaptPlanTouchesProfileLock(plan) {
		return nil
	}
	lockPath, err := resolvedStateAssetPath(configPath, cfg.StateDir, "profile.lock")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return fmt.Errorf("read profile lock %q: %w", lockPath, err)
	}
	var lock profileLock
	if err := yaml.Unmarshal(data, &lock); err != nil {
		return fmt.Errorf("parse profile lock %q: %w", lockPath, err)
	}
	lockedProfiles := make([]string, 0, len(lock.Profiles))
	for _, profile := range lock.Profiles {
		if strings.TrimSpace(profile.Name) == "" {
			continue
		}
		lockedProfiles = append(lockedProfiles, profile.Name)
	}
	if !stringSlicesEqual(cfg.Profiles, lockedProfiles) {
		return fmt.Errorf("profile.lock profiles %v do not match .xylem.yml profiles %v", lockedProfiles, cfg.Profiles)
	}
	return nil
}

func resolvedStateAssetPath(configPath, stateDir string, elems ...string) (string, error) {
	configAbs, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("resolve config path %q: %w", configPath, err)
	}
	if strings.TrimSpace(stateDir) == "" {
		return "", fmt.Errorf("state_dir must be non-empty")
	}
	root := filepath.Dir(configAbs)
	resolvedStateDir := config.ResolveStateDir(root, stateDir)
	parts := append([]string{resolvedStateDir}, elems...)
	return filepath.Join(parts...), nil
}

func adaptPlanTouchesProfileLock(plan *adaptRepoPlan) bool {
	if plan == nil {
		return false
	}
	for _, change := range plan.Planned {
		if change.Path == ".xylem/profile.lock" {
			return true
		}
	}
	return false
}

func isAllowedAdaptPlanPath(path string) bool {
	switch {
	case path == ".xylem.yml", path == "AGENTS.md":
		return true
	case strings.HasPrefix(path, ".xylem/"), strings.HasPrefix(path, "docs/"):
		return true
	default:
		return false
	}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
