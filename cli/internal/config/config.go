package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"gopkg.in/yaml.v3"
)

const minTimeout = 30 * time.Second

const DefaultAuditLogPath = "audit.jsonl"

var DefaultProtectedSurfaces = []string{
	".xylem/HARNESS.md",
	".xylem.yml",
	".xylem/workflows/*.yaml",
	".xylem/prompts/*/*.md",
}

type Config struct {
	Repo          string                  `yaml:"repo,omitempty"`
	Sources       map[string]SourceConfig `yaml:"sources,omitempty"`
	Tasks         map[string]Task         `yaml:"tasks,omitempty"`
	Concurrency   int                     `yaml:"concurrency"`
	MaxTurns      int                     `yaml:"max_turns"`
	Timeout       string                  `yaml:"timeout"`
	StateDir      string                  `yaml:"state_dir"`
	Exclude       []string                `yaml:"exclude,omitempty"`
	DefaultBranch string                  `yaml:"default_branch,omitempty"`
	CleanupAfter  string                  `yaml:"cleanup_after,omitempty"`
	LLM           string                  `yaml:"llm,omitempty"`
	Model         string                  `yaml:"model,omitempty"`
	Claude        ClaudeConfig            `yaml:"claude"`
	Copilot       CopilotConfig           `yaml:"copilot,omitempty"`
	Daemon        DaemonConfig            `yaml:"daemon,omitempty"`
	Harness       HarnessConfig           `yaml:"harness,omitempty"`
	Observability ObservabilityConfig     `yaml:"observability,omitempty"`
	Cost          CostConfig              `yaml:"cost,omitempty"`
}

type SourceConfig struct {
	Type    string          `yaml:"type"`
	Repo    string          `yaml:"repo,omitempty"`
	LLM     string          `yaml:"llm,omitempty"`
	Model   string          `yaml:"model,omitempty"`
	Timeout string          `yaml:"timeout,omitempty"`
	Exclude []string        `yaml:"exclude,omitempty"`
	Tasks   map[string]Task `yaml:"tasks,omitempty"`
}

type StatusLabels struct {
	Queued    string `yaml:"queued,omitempty"`
	Running   string `yaml:"running,omitempty"`
	Completed string `yaml:"completed,omitempty"`
	Failed    string `yaml:"failed,omitempty"`
	TimedOut  string `yaml:"timed_out,omitempty"`
}

// PREventsConfig defines which PR events trigger a workflow.
//
// For authored events (review_submitted, commented), at least one of
// AuthorAllow or AuthorDeny must be specified to prevent self-trigger
// loops — see validateGitHubPREventsSource.
type PREventsConfig struct {
	Labels          []string `yaml:"labels,omitempty"`
	ReviewSubmitted bool     `yaml:"review_submitted,omitempty"`
	ChecksFailed    bool     `yaml:"checks_failed,omitempty"`
	Commented       bool     `yaml:"commented,omitempty"`
	// AuthorAllow is an allowlist of GitHub logins whose reviews/comments
	// create vessels. If non-empty, events from any other login are skipped.
	// YAML footgun: bot logins like "copilot-pull-request-reviewer[bot]"
	// must be quoted because "[" starts a YAML flow sequence.
	AuthorAllow []string `yaml:"author_allow,omitempty"`
	// AuthorDeny is a denylist of GitHub logins whose reviews/comments
	// never create vessels. AuthorDeny takes precedence over AuthorAllow.
	AuthorDeny []string `yaml:"author_deny,omitempty"`
}

type Task struct {
	Labels       []string        `yaml:"labels,omitempty"`
	Workflow     string          `yaml:"workflow"`
	StatusLabels *StatusLabels   `yaml:"status_labels,omitempty"`
	On           *PREventsConfig `yaml:"on,omitempty"`
}

type ClaudeConfig struct {
	Command      string            `yaml:"command"`
	Flags        string            `yaml:"flags,omitempty"`
	DefaultModel string            `yaml:"default_model,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	// Template is kept for deserialization so we can detect and reject it.
	Template     string   `yaml:"template,omitempty"`
	AllowedTools []string `yaml:"allowed_tools,omitempty"`
}

// CopilotConfig holds configuration for the GitHub Copilot CLI provider.
type CopilotConfig struct {
	Command      string            `yaml:"command"`
	Flags        string            `yaml:"flags,omitempty"`
	DefaultModel string            `yaml:"default_model,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
}

type DaemonConfig struct {
	ScanInterval  string `yaml:"scan_interval,omitempty"`
	DrainInterval string `yaml:"drain_interval,omitempty"`
	AutoUpgrade   bool   `yaml:"auto_upgrade,omitempty"`
}

type HarnessConfig struct {
	ProtectedSurfaces ProtectedSurfacesConfig `yaml:"protected_surfaces,omitempty"`
	Policy            PolicyConfig            `yaml:"policy,omitempty"`
	AuditLog          string                  `yaml:"audit_log,omitempty"`
}

type ProtectedSurfacesConfig struct {
	Paths []string `yaml:"paths,omitempty"`
}

type PolicyConfig struct {
	Rules []PolicyRuleConfig `yaml:"rules,omitempty"`
}

type PolicyRuleConfig struct {
	Action   string `yaml:"action"`
	Resource string `yaml:"resource"`
	Effect   string `yaml:"effect"`
}

type ObservabilityConfig struct {
	Enabled    *bool   `yaml:"enabled,omitempty"`
	Endpoint   string  `yaml:"endpoint,omitempty"`
	Insecure   bool    `yaml:"insecure,omitempty"`
	SampleRate float64 `yaml:"sample_rate,omitempty"`
}

type CostConfig struct {
	Budget *BudgetConfig `yaml:"budget,omitempty"`
}

type BudgetConfig struct {
	MaxCostUSD float64 `yaml:"max_cost_usd,omitempty"`
	MaxTokens  int     `yaml:"max_tokens,omitempty"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}

	cfg := &Config{
		Concurrency:  2,
		MaxTurns:     50,
		Timeout:      "30m",
		StateDir:     ".xylem",
		CleanupAfter: "168h",
		Exclude:      []string{"wontfix", "duplicate", "in-progress", "no-bot"},
		Claude: ClaudeConfig{
			Command: "claude",
		},
		Copilot: CopilotConfig{
			Command: "copilot",
		},
		Daemon: DaemonConfig{
			ScanInterval:  "60s",
			DrainInterval: "30s",
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}

	cfg.normalize()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// normalize migrates legacy top-level Repo/Tasks/Exclude into the Sources map.
func (c *Config) normalize() {
	if c.Repo != "" && len(c.Sources) == 0 && len(c.Tasks) > 0 {
		exclude := c.Exclude
		c.Sources = map[string]SourceConfig{
			"github": {
				Type:    "github",
				Repo:    c.Repo,
				Exclude: exclude,
				Tasks:   c.Tasks,
			},
		}
	}
}

func (c *Config) Validate() error {
	if c.Concurrency <= 0 {
		return fmt.Errorf("concurrency must be greater than 0")
	}

	if c.MaxTurns <= 0 {
		return fmt.Errorf("max_turns must be greater than 0")
	}

	dur, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return fmt.Errorf("timeout must be a valid duration: %w", err)
	}
	if dur < minTimeout {
		return fmt.Errorf("timeout must be at least %s", minTimeout)
	}

	if c.CleanupAfter != "" {
		if _, err := time.ParseDuration(c.CleanupAfter); err != nil {
			return fmt.Errorf("cleanup_after must be a valid duration: %w", err)
		}
	}

	switch c.LLM {
	case "", "claude", "copilot":
		// valid
	default:
		return fmt.Errorf("llm must be \"claude\" or \"copilot\", got %q", c.LLM)
	}

	// Validate copilot config: command must be set when copilot is the active provider.
	if c.LLM == "copilot" && c.Copilot.Command == "" {
		return fmt.Errorf("copilot.command must be non-empty")
	}

	if c.Claude.Template != "" {
		return fmt.Errorf("claude.template is no longer supported; migrate to phase-based workflows in .xylem/workflows/")
	}

	if len(c.Claude.AllowedTools) > 0 {
		return fmt.Errorf("claude.allowed_tools is no longer supported; use allowed_tools in workflow phase definitions")
	}

	if strings.Contains(c.Claude.Flags, "--bare") {
		if c.Claude.Env == nil || c.Claude.Env["ANTHROPIC_API_KEY"] == "" {
			return fmt.Errorf("--bare requires ANTHROPIC_API_KEY in claude.env")
		}
	}

	if c.Daemon.ScanInterval != "" {
		if _, err := time.ParseDuration(c.Daemon.ScanInterval); err != nil {
			return fmt.Errorf("daemon.scan_interval must be a valid duration: %w", err)
		}
	}
	if c.Daemon.DrainInterval != "" {
		if _, err := time.ParseDuration(c.Daemon.DrainInterval); err != nil {
			return fmt.Errorf("daemon.drain_interval must be a valid duration: %w", err)
		}
	}

	// Validate sources
	for name, src := range c.Sources {
		switch src.LLM {
		case "", "claude", "copilot":
			// valid
		default:
			return fmt.Errorf("source %q: llm must be \"claude\" or \"copilot\", got %q", name, src.LLM)
		}

		if src.Timeout != "" {
			dur, err := time.ParseDuration(src.Timeout)
			if err != nil {
				return fmt.Errorf("source %q: timeout must be a valid duration: %w", name, err)
			}
			if dur < minTimeout {
				return fmt.Errorf("source %q: timeout must be at least %s", name, minTimeout)
			}
		}

		switch src.Type {
		case "github", "github-pr":
			if err := validateGitHubSource(name, src); err != nil {
				return err
			}
		case "github-pr-events":
			if err := validateGitHubPREventsSource(name, src); err != nil {
				return err
			}
		case "github-merge":
			if err := validateGitHubMergeSource(name, src); err != nil {
				return err
			}
		case "":
			return fmt.Errorf("source %q must specify a type", name)
		default:
			return fmt.Errorf("source %q: unknown type %q", name, src.Type)
		}
	}

	// Legacy validation: if top-level Repo is set without Sources, validate it
	if c.Repo != "" && len(c.Sources) == 0 {
		parts := strings.Split(c.Repo, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("repo must be in owner/name format")
		}
		if len(c.Tasks) == 0 {
			return fmt.Errorf("tasks: at least one task is required")
		}
		for tname, task := range c.Tasks {
			if len(task.Labels) == 0 {
				return fmt.Errorf("task %q must include at least one labels entry", tname)
			}
			if strings.TrimSpace(task.Workflow) == "" {
				return fmt.Errorf("task %q must include a workflow", tname)
			}
		}
	}

	if err := c.validateHarness(); err != nil {
		return err
	}
	if err := c.validateObservability(); err != nil {
		return err
	}
	if err := c.validateCost(); err != nil {
		return err
	}

	if err := c.validateProviderDefaultModels(); err != nil {
		return err
	}

	return nil
}

// CleanupAfterDuration returns the parsed cleanup_after duration, defaulting to
// 168h (7 days) if the field is empty or unparseable.
func (c *Config) CleanupAfterDuration() time.Duration {
	if c.CleanupAfter == "" {
		return 168 * time.Hour
	}
	d, err := time.ParseDuration(c.CleanupAfter)
	if err != nil {
		return 168 * time.Hour
	}
	return d
}

func (c *Config) EffectiveProtectedSurfaces() []string {
	switch {
	case len(c.Harness.ProtectedSurfaces.Paths) == 0:
		return append([]string(nil), DefaultProtectedSurfaces...)
	case len(c.Harness.ProtectedSurfaces.Paths) == 1 && c.Harness.ProtectedSurfaces.Paths[0] == "none":
		return nil
	default:
		return append([]string(nil), c.Harness.ProtectedSurfaces.Paths...)
	}
}

func (c *Config) EffectiveAuditLogPath() string {
	if strings.TrimSpace(c.Harness.AuditLog) != "" {
		return c.Harness.AuditLog
	}
	return DefaultAuditLogPath
}

func (c *Config) ObservabilityEnabled() bool {
	if c.Observability.Enabled == nil {
		return true
	}
	return *c.Observability.Enabled
}

func (c *Config) ObservabilitySampleRate() float64 {
	if c.Observability.SampleRate <= 0 || c.Observability.SampleRate > 1.0 {
		return 1.0
	}
	return c.Observability.SampleRate
}

func (c *Config) VesselBudget() *cost.Budget {
	if c.Cost.Budget == nil {
		return nil
	}

	if c.Cost.Budget.MaxCostUSD <= 0 && c.Cost.Budget.MaxTokens <= 0 {
		return nil
	}

	return &cost.Budget{
		TokenLimit:   c.Cost.Budget.MaxTokens,
		CostLimitUSD: c.Cost.Budget.MaxCostUSD,
	}
}

func (c *Config) BuildIntermediaryPolicies() []intermediary.Policy {
	if len(c.Harness.Policy.Rules) == 0 {
		return []intermediary.Policy{DefaultPolicy()}
	}

	rules := make([]intermediary.Rule, len(c.Harness.Policy.Rules))
	for i, rule := range c.Harness.Policy.Rules {
		rules[i] = intermediary.Rule{
			Action:   rule.Action,
			Resource: rule.Resource,
			Effect:   intermediary.Effect(rule.Effect),
		}
	}

	return []intermediary.Policy{{
		Name:  "user",
		Rules: rules,
	}}
}

func DefaultPolicy() intermediary.Policy {
	return intermediary.Policy{
		Name: "default",
		Rules: []intermediary.Rule{
			{Action: "file_write", Resource: ".xylem/HARNESS.md", Effect: intermediary.Deny},
			{Action: "file_write", Resource: ".xylem.yml", Effect: intermediary.Deny},
			{Action: "file_write", Resource: ".xylem/workflows/*", Effect: intermediary.Deny},
			{Action: "file_write", Resource: ".xylem/prompts/*/*.md", Effect: intermediary.Deny},
			{Action: "git_push", Resource: "*", Effect: intermediary.RequireApproval},
			{Action: "pr_create", Resource: "*", Effect: intermediary.RequireApproval},
			{Action: "*", Resource: "*", Effect: intermediary.Allow},
		},
	}
}

func (c *Config) validateHarness() error {
	for _, pattern := range c.Harness.ProtectedSurfaces.Paths {
		if pattern == "none" {
			continue
		}
		if _, err := filepath.Match(pattern, "test"); err != nil {
			return fmt.Errorf("harness.protected_surfaces.paths: invalid glob %q: %w", pattern, err)
		}
	}

	for i, rule := range c.Harness.Policy.Rules {
		if rule.Action == "" {
			return fmt.Errorf("harness.policy.rules[%d]: action is required", i)
		}
		if rule.Resource == "" {
			return fmt.Errorf("harness.policy.rules[%d]: resource is required", i)
		}

		switch intermediary.Effect(rule.Effect) {
		case intermediary.Allow, intermediary.Deny, intermediary.RequireApproval:
		default:
			return fmt.Errorf("harness.policy.rules[%d]: invalid effect %q (must be allow, deny, or require_approval)", i, rule.Effect)
		}
	}

	return nil
}

func (c *Config) validateObservability() error {
	if c.Observability.SampleRate != 0 && (c.Observability.SampleRate < 0 || c.Observability.SampleRate > 1.0) {
		return fmt.Errorf("observability.sample_rate must be in [0.0, 1.0]")
	}
	return nil
}

func (c *Config) validateCost() error {
	if c.Cost.Budget == nil {
		return nil
	}
	if c.Cost.Budget.MaxCostUSD < 0 {
		return fmt.Errorf("cost.budget.max_cost_usd must be non-negative")
	}
	if c.Cost.Budget.MaxTokens < 0 {
		return fmt.Errorf("cost.budget.max_tokens must be non-negative")
	}
	return nil
}

// validateProviderDefaultModels ensures every active LLM provider has a
// default_model configured. A provider is active if it is the global llm value
// or referenced by any source's llm field.
func (c *Config) validateProviderDefaultModels() error {
	active := map[string]bool{}
	switch c.LLM {
	case "", "claude":
		active["claude"] = true
	case "copilot":
		active["copilot"] = true
	}
	for _, src := range c.Sources {
		switch src.LLM {
		case "claude":
			active["claude"] = true
		case "copilot":
			active["copilot"] = true
		}
	}
	if active["claude"] && c.Claude.DefaultModel == "" {
		return fmt.Errorf("claude.default_model must be set when claude is an active provider")
	}
	if active["copilot"] && c.Copilot.DefaultModel == "" {
		return fmt.Errorf("copilot.default_model must be set when copilot is an active provider")
	}
	return nil
}

func validateGitHubSource(name string, src SourceConfig) error {
	repo := strings.TrimSpace(src.Repo)
	if repo == "" {
		return fmt.Errorf("source %q (github): repo is required", name)
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("source %q (github): repo must be in owner/name format", name)
	}
	if len(src.Tasks) == 0 {
		return fmt.Errorf("source %q (github): at least one task is required", name)
	}
	for tname, task := range src.Tasks {
		if len(task.Labels) == 0 {
			return fmt.Errorf("source %q task %q: must include at least one labels entry", name, tname)
		}
		if strings.TrimSpace(task.Workflow) == "" {
			return fmt.Errorf("source %q task %q: must include a workflow", name, tname)
		}
	}
	return nil
}

func validateGitHubPREventsSource(name string, src SourceConfig) error {
	repo := strings.TrimSpace(src.Repo)
	if repo == "" {
		return fmt.Errorf("source %q (github-pr-events): repo is required", name)
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("source %q (github-pr-events): repo must be in owner/name format", name)
	}
	if len(src.Tasks) == 0 {
		return fmt.Errorf("source %q (github-pr-events): at least one task is required", name)
	}
	for tname, task := range src.Tasks {
		if strings.TrimSpace(task.Workflow) == "" {
			return fmt.Errorf("source %q task %q: must include a workflow", name, tname)
		}
		if task.On == nil {
			return fmt.Errorf("source %q task %q: must include an 'on' block with at least one trigger", name, tname)
		}
		if len(task.On.Labels) == 0 && !task.On.ReviewSubmitted && !task.On.ChecksFailed && !task.On.Commented {
			return fmt.Errorf("source %q task %q: 'on' block must specify at least one trigger (labels, review_submitted, checks_failed, or commented)", name, tname)
		}
		// Authored-event triggers must specify an author filter to prevent
		// self-trigger loops (e.g. xylem responds to its own review as hnipps,
		// that review triggers another vessel, ad infinitum).
		if (task.On.ReviewSubmitted || task.On.Commented) && len(task.On.AuthorAllow) == 0 && len(task.On.AuthorDeny) == 0 {
			return fmt.Errorf("source %q task %q: tasks with review_submitted or commented must specify author_allow or author_deny to prevent self-trigger loops", name, tname)
		}
	}
	return nil
}

func validateGitHubMergeSource(name string, src SourceConfig) error {
	repo := strings.TrimSpace(src.Repo)
	if repo == "" {
		return fmt.Errorf("source %q (github-merge): repo is required", name)
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("source %q (github-merge): repo must be in owner/name format", name)
	}
	if len(src.Tasks) == 0 {
		return fmt.Errorf("source %q (github-merge): at least one task is required", name)
	}
	for tname, task := range src.Tasks {
		if strings.TrimSpace(task.Workflow) == "" {
			return fmt.Errorf("source %q task %q: must include a workflow", name, tname)
		}
	}
	return nil
}
