package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/cadence"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"gopkg.in/yaml.v3"
)

const minTimeout = 30 * time.Second

const DefaultAuditLogPath = "audit.jsonl"
const DefaultLLMRoutingTier = "med"
const DefaultAutoAdminMergeOptOutLabel = "no-auto-admin-merge"
const runtimeStateDirName = "state"

// DefaultProtectedSurfaces is the default list of paths that xylem's
// runtime verifier treats as off-limits to vessel modifications.
//
// Historical context: earlier versions of xylem seeded this with control-
// plane paths (.xylem/HARNESS.md, .xylem.yml, .xylem/workflows/*.yaml,
// .xylem/prompts/*/*.md) so that a rogue vessel in a guest repository
// couldn't silently rewrite xylem's own behaviour while running under an
// arbitrary issue. That default made sense when xylem was being used
// against external repositories where the control plane was managed by a
// separate authority.
//
// For xylem's primary use case — the nicholls-inc/xylem repo itself, where
// xylem is continuously self-improving — that default is self-defeating.
// Every vessel that implements a bug fix in .xylem/workflows/*.yaml or
// .xylem/prompts/*/*.md hits the runtime verifier, fails, and the only
// recovery path is a manual PR shipped by a human or rescue loop. Issues
// #188 (resolve-conflicts gate bypass) and #190 (merge-pr --auto flag)
// both failed at implement phase this way, despite the vessels correctly
// identifying the files to edit.
//
// The protected-surface chain (pre-verify restore in PR #180, alignment
// filter in PR #187, additive allow-list in PR #186) handled several
// adjacent cases but never the vessel-authored canonical-changing
// modification case — because by design, the policy prevented vessels
// from changing canonical state at all.
//
// The new default is EMPTY: protection is off by default, matching the
// self-improving use case. Deployments that want strict enforcement can
// still opt in via harness.protected_surfaces.paths in .xylem.yml.
// Human PR review remains the security gate: vessels propose changes
// via PRs, humans merge them. The runtime verifier was a belt-and-
// suspenders layer on top of that gate; removing the belt keeps the
// suspenders in place.
//
// See issue #194 for the design discussion.
var DefaultProtectedSurfaces = []string{}

type Config struct {
	Profiles            []string                  `yaml:"profiles,omitempty"`
	Repo                string                    `yaml:"repo,omitempty"`
	Sources             map[string]SourceConfig   `yaml:"sources,omitempty"`
	Tasks               map[string]Task           `yaml:"tasks,omitempty"`
	Concurrency         int                       `yaml:"concurrency"`
	ConcurrencyPerClass map[string]int            `yaml:"-"`
	MaxTurns            int                       `yaml:"max_turns"`
	Timeout             string                    `yaml:"timeout"`
	StateDir            string                    `yaml:"state_dir"`
	Exclude             []string                  `yaml:"exclude,omitempty"`
	DefaultBranch       string                    `yaml:"default_branch,omitempty"`
	CleanupAfter        string                    `yaml:"cleanup_after,omitempty"`
	LLM                 string                    `yaml:"llm,omitempty"`
	Model               string                    `yaml:"model,omitempty"`
	Providers           map[string]ProviderConfig `yaml:"providers,omitempty"`
	LLMRouting          LLMRoutingConfig          `yaml:"llm_routing,omitempty"`
	Claude              ClaudeConfig              `yaml:"claude"`
	Copilot             CopilotConfig             `yaml:"copilot,omitempty"`
	Validation          ValidationConfig          `yaml:"validation,omitempty"`
	Daemon              DaemonConfig              `yaml:"daemon,omitempty"`
	Harness             HarnessConfig             `yaml:"harness,omitempty"`
	Observability       ObservabilityConfig       `yaml:"observability,omitempty"`
	Cost                CostConfig                `yaml:"cost,omitempty"`
	Telemetry           TelemetryConfig           `yaml:"telemetry,omitempty"`
}

type ProviderConfig struct {
	Kind         string            `yaml:"kind"`
	Command      string            `yaml:"command"`
	Flags        string            `yaml:"flags,omitempty"`
	Tiers        map[string]string `yaml:"tiers,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	AllowedTools []string          `yaml:"allowed_tools,omitempty"`
}

type LLMRoutingConfig struct {
	DefaultTier string                 `yaml:"default_tier,omitempty"`
	Tiers       map[string]TierRouting `yaml:"tiers,omitempty"`
}

type TierRouting struct {
	Providers []string `yaml:"providers"`
}

type SourceConfig struct {
	Type     string          `yaml:"type"`
	Repo     string          `yaml:"repo,omitempty"`
	Schedule string          `yaml:"schedule,omitempty"`
	Cadence  string          `yaml:"cadence,omitempty"`
	Workflow string          `yaml:"workflow,omitempty"`
	LLM      string          `yaml:"llm,omitempty"`
	Model    string          `yaml:"model,omitempty"`
	Timeout  string          `yaml:"timeout,omitempty"`
	Exclude  []string        `yaml:"exclude,omitempty"`
	Tasks    map[string]Task `yaml:"tasks,omitempty"`
}

type StatusLabels struct {
	Queued    string `yaml:"queued,omitempty"`
	Running   string `yaml:"running,omitempty"`
	Completed string `yaml:"completed,omitempty"`
	Failed    string `yaml:"failed,omitempty"`
	TimedOut  string `yaml:"timed_out,omitempty"`
}

type LabelGateLabels struct {
	Waiting string `yaml:"waiting,omitempty"`
	Ready   string `yaml:"ready,omitempty"`
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
	PROpened        bool     `yaml:"pr_opened,omitempty"`
	PRHeadUpdated   bool     `yaml:"pr_head_updated,omitempty"`
	// AuthorAllow is an allowlist of GitHub logins whose reviews/comments
	// create vessels. If non-empty, events from any other login are skipped.
	// YAML footgun: bot logins like "copilot-pull-request-reviewer[bot]"
	// must be quoted because "[" starts a YAML flow sequence.
	AuthorAllow []string `yaml:"author_allow,omitempty"`
	// AuthorDeny is a denylist of GitHub logins whose reviews/comments
	// never create vessels. AuthorDeny takes precedence over AuthorAllow.
	AuthorDeny []string `yaml:"author_deny,omitempty"`
	Debounce   string   `yaml:"debounce,omitempty"`
}

type Task struct {
	Labels          []string         `yaml:"labels,omitempty"`
	Workflow        string           `yaml:"workflow"`
	Tier            string           `yaml:"tier,omitempty"`
	Ref             string           `yaml:"ref,omitempty"`
	StatusLabels    *StatusLabels    `yaml:"status_labels,omitempty"`
	LabelGateLabels *LabelGateLabels `yaml:"label_gate_labels,omitempty"`
	On              *PREventsConfig  `yaml:"on,omitempty"`
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

type ValidationConfig struct {
	Format string `yaml:"format,omitempty"`
	Lint   string `yaml:"lint,omitempty"`
	Build  string `yaml:"build,omitempty"`
	Test   string `yaml:"test,omitempty"`
}

type DaemonConfig struct {
	ScanInterval  string             `yaml:"scan_interval,omitempty"`
	DrainInterval string             `yaml:"drain_interval,omitempty"`
	StallMonitor  StallMonitorConfig `yaml:"stall_monitor,omitempty"`
	AutoUpgrade   bool               `yaml:"auto_upgrade,omitempty"`
	// UpgradeInterval controls how often the daemon re-runs the
	// auto_upgrade check while the loop is running. Only meaningful when
	// AutoUpgrade is true. Defaults to 5m. Accepts any Go duration string.
	UpgradeInterval string `yaml:"upgrade_interval,omitempty"`
	// AutoMerge enables the daemon's automatic reviewer-request +
	// admin-merge loop for merge-ready vessel PRs. Only PRs matching the
	// configured labels and branch pattern are eligible, and the
	// no-auto-admin-merge label always opts a PR out.
	AutoMerge bool `yaml:"auto_merge,omitempty"`
	// AutoMergeRepo is the GitHub repo slug (owner/name) for auto-merge.
	// If empty, gh CLI uses the current directory's origin remote.
	AutoMergeRepo string `yaml:"auto_merge_repo,omitempty"`
	// AutoMergeLabels are the labels a PR must carry before the daemon treats
	// it as merge-ready. Defaults to ["ready-to-merge"].
	AutoMergeLabels []string `yaml:"auto_merge_labels,omitempty"`
	// AutoMergeBranchPattern filters which PR head refs the daemon manages.
	// Defaults to ".*".
	AutoMergeBranchPattern string `yaml:"auto_merge_branch_pattern,omitempty"`
	// AutoMergeReviewer is the GitHub login to request before enabling
	// auto-merge. Defaults to empty, which skips reviewer requests.
	AutoMergeReviewer string `yaml:"auto_merge_reviewer,omitempty"`
}

type StallMonitorConfig struct {
	PhaseStallThreshold  string `yaml:"phase_stall_threshold,omitempty"`
	ScannerIdleThreshold string `yaml:"scanner_idle_threshold,omitempty"`
	OrphanCheckEnabled   bool   `yaml:"orphan_check_enabled,omitempty"`
}

type HarnessConfig struct {
	ProtectedSurfaces ProtectedSurfacesConfig `yaml:"protected_surfaces,omitempty"`
	Policy            PolicyConfig            `yaml:"policy,omitempty"`
	AuditLog          string                  `yaml:"audit_log,omitempty"`
	Review            HarnessReviewConfig     `yaml:"review,omitempty"`
}

type HarnessReviewConfig struct {
	Enabled      bool   `yaml:"enabled,omitempty"`
	Cadence      string `yaml:"cadence,omitempty"`
	EveryNRuns   int    `yaml:"every_n_runs,omitempty"`
	LookbackRuns int    `yaml:"lookback_runs,omitempty"`
	MinSamples   int    `yaml:"min_samples,omitempty"`
	OutputDir    string `yaml:"output_dir,omitempty"`
}

type ProtectedSurfacesConfig struct {
	Paths []string `yaml:"paths,omitempty"`
}

type PolicyConfig struct {
	Mode  string             `yaml:"mode,omitempty"`
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

type TelemetryConfig struct {
	Enabled    *bool  `yaml:"enabled,omitempty"`
	TargetRepo string `yaml:"target_repo,omitempty"`
	Extended   bool   `yaml:"extended,omitempty"`
}

type CostConfig struct {
	Budget *BudgetConfig `yaml:"budget,omitempty"`
}

type BudgetConfig struct {
	MaxCostUSD float64 `yaml:"max_cost_usd,omitempty"`
	MaxTokens  int     `yaml:"max_tokens,omitempty"`
}

type parsedConcurrency struct {
	Global   int
	PerClass map[string]int
	Set      bool
}

func Load(path string) (*Config, error) {
	return load(path, true)
}

func LoadUnvalidated(path string) (*Config, error) {
	return load(path, false)
}

func load(path string, validate bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}

	concurrency, err := parseConcurrencyNode(&root)
	if err != nil {
		return nil, err
	}
	removeYAMLField(&root, "concurrency")

	cfg := &Config{
		Concurrency:         2,
		ConcurrencyPerClass: nil,
		MaxTurns:            50,
		Timeout:             "30m",
		StateDir:            ".xylem",
		CleanupAfter:        "168h",
		Exclude:             []string{"wontfix", "duplicate", "in-progress", "no-bot"},
		Claude: ClaudeConfig{
			Command: "claude",
		},
		Copilot: CopilotConfig{
			Command: "copilot",
		},
		Daemon: DaemonConfig{
			ScanInterval:  "60s",
			DrainInterval: "30s",
			StallMonitor: StallMonitorConfig{
				PhaseStallThreshold:  "30m",
				ScannerIdleThreshold: "5m",
				OrphanCheckEnabled:   true,
			},
		},
	}

	if err := root.Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode config yaml: %w", err)
	}
	if concurrency.Set {
		cfg.Concurrency = concurrency.Global
		cfg.ConcurrencyPerClass = concurrency.PerClass
	}

	cfg.normalize()

	if validate {
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// ResolveStateDir rebases a relative state_dir under root.
func ResolveStateDir(root, stateDir string) string {
	root = strings.TrimSpace(root)
	stateDir = strings.TrimSpace(stateDir)
	if root == "" || stateDir == "" || filepath.IsAbs(stateDir) {
		return stateDir
	}
	return filepath.Join(root, stateDir)
}

// RuntimeRoot returns the resolved runtime-state root for a control-plane
// state_dir. It prefers the profile-ready .xylem/state/ layout, but keeps the
// legacy flat layout alive while those artifacts still exist.
func RuntimeRoot(stateDir string) string {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return ""
	}
	if !looksLikeControlPlaneDir(stateDir) {
		return stateDir
	}

	runtimeRoot := filepath.Join(stateDir, runtimeStateDirName)
	if pathExists(runtimeRoot) {
		return runtimeRoot
	}
	if hasLegacyRuntimeArtifacts(stateDir) {
		return stateDir
	}
	return runtimeRoot
}

// RuntimePath resolves a runtime-state path beneath state_dir. Control-plane
// directories use the new .xylem/state/ subtree by default, but legacy flat
// artifacts are still honored during the migration window.
func RuntimePath(stateDir string, elems ...string) string {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return filepath.Join(elems...)
	}
	if !looksLikeControlPlaneDir(stateDir) {
		return filepath.Join(append([]string{stateDir}, elems...)...)
	}
	if hasRuntimePrefix(elems...) {
		return filepath.Join(append([]string{stateDir}, elems...)...)
	}
	runtimePath := filepath.Join(append([]string{stateDir, runtimeStateDirName}, elems...)...)
	legacyPath := filepath.Join(append([]string{stateDir}, elems...)...)
	if pathExists(runtimePath) {
		return runtimePath
	}
	if pathExists(legacyPath) {
		return legacyPath
	}
	return runtimePath
}

func hasRuntimePrefix(elems ...string) bool {
	if len(elems) == 0 {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.Join(elems...)))
	return clean == runtimeStateDirName || strings.HasPrefix(clean, runtimeStateDirName+"/")
}

func looksLikeControlPlaneDir(stateDir string) bool {
	if filepath.Base(filepath.Clean(stateDir)) == ".xylem" {
		return true
	}
	for _, marker := range []string{".gitignore", "HARNESS.md", "profile.lock", "workflows", "prompts"} {
		if pathExists(filepath.Join(stateDir, marker)) {
			return true
		}
	}
	return false
}

func hasLegacyRuntimeArtifacts(stateDir string) bool {
	for _, marker := range []string{
		"queue.jsonl",
		"phases",
		"schedules",
		"reviews",
		"locks",
		"traces",
		"dtu",
		"paused",
		"daemon.pid",
		"daemon.log",
		"daemon-supervisor.pid",
		"daemon-supervisor.stop",
		"schedule-state.json",
	} {
		if pathExists(filepath.Join(stateDir, marker)) {
			return true
		}
	}
	return false
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
	c.ConcurrencyPerClass = normalizeConcurrencyPerClass(c.ConcurrencyPerClass)
	c.normalizeProviders()
}

func (c *Config) Validate() error {
	if c.Concurrency <= 0 {
		return fmt.Errorf("concurrency must be greater than 0")
	}
	for class, limit := range c.ConcurrencyPerClass {
		if strings.TrimSpace(class) == "" {
			return fmt.Errorf("concurrency.per_class keys must be non-empty")
		}
		if limit <= 0 {
			return fmt.Errorf("concurrency.per_class[%q] must be greater than 0", class)
		}
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

	c.normalizeProviders()

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
	if c.Daemon.StallMonitor.PhaseStallThreshold != "" {
		if _, err := time.ParseDuration(c.Daemon.StallMonitor.PhaseStallThreshold); err != nil {
			return fmt.Errorf("daemon.stall_monitor.phase_stall_threshold must be a valid duration: %w", err)
		}
	}
	if c.Daemon.StallMonitor.ScannerIdleThreshold != "" {
		if _, err := time.ParseDuration(c.Daemon.StallMonitor.ScannerIdleThreshold); err != nil {
			return fmt.Errorf("daemon.stall_monitor.scanner_idle_threshold must be a valid duration: %w", err)
		}
	}
	if strings.TrimSpace(c.Daemon.AutoMergeBranchPattern) != "" {
		if _, err := regexp.Compile(strings.TrimSpace(c.Daemon.AutoMergeBranchPattern)); err != nil {
			return fmt.Errorf("daemon.auto_merge_branch_pattern must be a valid regexp: %w", err)
		}
	}
	for i, label := range c.Daemon.AutoMergeLabels {
		if strings.TrimSpace(label) == "" {
			return fmt.Errorf("daemon.auto_merge_labels[%d] must be non-empty", i)
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
		case "scheduled":
			if err := validateScheduledSource(name, src); err != nil {
				return err
			}
		case "schedule":
			if err := validateScheduleSource(name, src); err != nil {
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
	if err := c.validateTelemetry(); err != nil {
		return err
	}

	if err := c.validateProviders(); err != nil {
		return err
	}
	if err := c.validateWorkflowRequirements(); err != nil {
		return err
	}

	return nil
}

func (c *Config) ConcurrencyLimit(class string) (int, bool) {
	if c == nil {
		return 0, false
	}
	limit, ok := c.ConcurrencyPerClass[strings.TrimSpace(class)]
	return limit, ok
}

func (c Config) ValidationConfigured() bool {
	return !c.Validation.IsEmpty()
}

func (v ValidationConfig) IsEmpty() bool {
	return strings.TrimSpace(v.Format) == "" &&
		strings.TrimSpace(v.Lint) == "" &&
		strings.TrimSpace(v.Build) == "" &&
		strings.TrimSpace(v.Test) == ""
}

func (d DaemonConfig) EffectiveAutoMergeLabels() []string {
	if len(d.AutoMergeLabels) == 0 {
		return []string{"ready-to-merge"}
	}
	labels := make([]string, 0, len(d.AutoMergeLabels))
	for _, label := range d.AutoMergeLabels {
		trimmed := strings.TrimSpace(label)
		if trimmed == "" {
			continue
		}
		labels = append(labels, trimmed)
	}
	if len(labels) == 0 {
		return []string{"ready-to-merge"}
	}
	return labels
}

func (d DaemonConfig) EffectiveAutoMergeBranchPattern() string {
	if trimmed := strings.TrimSpace(d.AutoMergeBranchPattern); trimmed != "" {
		return trimmed
	}
	return ".*"
}

func (d DaemonConfig) EffectiveAutoMergeReviewer() string {
	return strings.TrimSpace(d.AutoMergeReviewer)
}

func (d DaemonConfig) EffectiveAutoAdminMergeOptOutLabel() string {
	return DefaultAutoAdminMergeOptOutLabel
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

// HarnessPolicyMode returns the effective policy mode for the harness.
//
// The zero-value (empty string) resolves to warn mode so that xylem ships in
// observe-and-warn by default: guest repos can adopt the harness incrementally
// without their existing workflows hitting the first deny rule cold. Operators
// who want hard enforcement set harness.policy.mode: "enforce" explicitly.
//
// Values that fail to parse still fall back to warn (not enforce) so that a
// typo in the config file cannot escalate a previously-permissive deployment
// into a blocking one. Config load-time validation at validateHarness rejects
// unknown modes up-front, so this branch is a belt-and-suspenders fallback.
func (c *Config) HarnessPolicyMode() intermediary.PolicyMode {
	if strings.TrimSpace(c.Harness.Policy.Mode) == "" {
		return intermediary.PolicyModeWarn
	}
	mode, err := intermediary.ParsePolicyMode(c.Harness.Policy.Mode)
	if err != nil {
		return intermediary.PolicyModeWarn
	}
	return mode
}

func (c *Config) HarnessReviewCadence() string {
	if !c.Harness.Review.Enabled {
		return "manual"
	}
	switch c.Harness.Review.Cadence {
	case "", "manual":
		return "manual"
	case "every_drain", "every_n_runs":
		return c.Harness.Review.Cadence
	default:
		return "manual"
	}
}

func (c *Config) HarnessReviewEveryNRuns() int {
	if c.Harness.Review.EveryNRuns > 0 {
		return c.Harness.Review.EveryNRuns
	}
	return 10
}

func (c *Config) HarnessReviewLookbackRuns() int {
	if c.Harness.Review.LookbackRuns > 0 {
		return c.Harness.Review.LookbackRuns
	}
	return 50
}

func (c *Config) HarnessReviewMinSamples() int {
	if c.Harness.Review.MinSamples > 0 {
		return c.Harness.Review.MinSamples
	}
	return 3
}

func (c *Config) HarnessReviewOutputDir() string {
	if strings.TrimSpace(c.Harness.Review.OutputDir) != "" {
		return c.Harness.Review.OutputDir
	}
	return "reviews"
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

// TelemetryEnabled returns whether field-report telemetry is active.
// Defaults to true (opt-out). The XYLEM_TELEMETRY env var overrides the
// config value: "off" or "false" disables telemetry regardless of config.
func (c *Config) TelemetryEnabled() bool {
	if env := strings.TrimSpace(os.Getenv("XYLEM_TELEMETRY")); env != "" {
		switch strings.ToLower(env) {
		case "off", "false", "0", "no":
			return false
		case "on", "true", "1", "yes":
			return true
		}
	}
	if c.Telemetry.Enabled == nil {
		return true
	}
	return *c.Telemetry.Enabled
}

// TelemetryTargetRepo returns the GitHub repo where field reports are filed.
// Defaults to "nicholls-inc/xylem".
func (c *Config) TelemetryTargetRepo() string {
	if repo := strings.TrimSpace(c.Telemetry.TargetRepo); repo != "" {
		return repo
	}
	return "nicholls-inc/xylem"
}

func parseConcurrencyNode(root *yaml.Node) (parsedConcurrency, error) {
	mapping := root
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return parsedConcurrency{}, nil
		}
		mapping = root.Content[0]
	}
	if mapping.Kind != yaml.MappingNode {
		return parsedConcurrency{}, nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key.Value != "concurrency" {
			continue
		}
		value := mapping.Content[i+1]
		switch value.Kind {
		case yaml.ScalarNode:
			var global int
			if err := value.Decode(&global); err != nil {
				return parsedConcurrency{}, fmt.Errorf("parse concurrency: %w", err)
			}
			return parsedConcurrency{Global: global, Set: true}, nil
		case yaml.MappingNode:
			var raw struct {
				Global   *int           `yaml:"global"`
				PerClass map[string]int `yaml:"per_class,omitempty"`
			}
			if err := value.Decode(&raw); err != nil {
				return parsedConcurrency{}, fmt.Errorf("parse concurrency: %w", err)
			}
			if raw.Global == nil {
				return parsedConcurrency{}, fmt.Errorf("concurrency.global is required when concurrency is a map")
			}
			return parsedConcurrency{
				Global:   *raw.Global,
				PerClass: normalizeConcurrencyPerClass(raw.PerClass),
				Set:      true,
			}, nil
		default:
			return parsedConcurrency{}, fmt.Errorf("parse concurrency: must be an integer or map")
		}
	}
	return parsedConcurrency{}, nil
}

func removeYAMLField(root *yaml.Node, field string) {
	mapping := root
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return
		}
		mapping = root.Content[0]
	}
	if mapping.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != field {
			continue
		}
		mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
		return
	}
}

func normalizeConcurrencyPerClass(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for class, limit := range in {
		out[strings.TrimSpace(class)] = limit
	}
	return out
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

// DefaultPolicy returns the default user policy layer. Workflow-class matrix
// decisions are enforced separately inside the intermediary; this default layer
// simply keeps autonomous execution unblocked unless operators opt into tighter
// rules via harness.policy.rules.
func DefaultPolicy() intermediary.Policy {
	return intermediary.Policy{
		Name: "default",
		Rules: []intermediary.Rule{
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

	if _, err := intermediary.ParsePolicyMode(c.Harness.Policy.Mode); err != nil {
		return fmt.Errorf("harness.policy.mode: %w", err)
	}

	if cadence := c.Harness.Review.Cadence; cadence != "" {
		switch cadence {
		case "manual", "every_drain", "every_n_runs":
		default:
			return fmt.Errorf("harness.review.cadence: invalid value %q (must be manual, every_drain, or every_n_runs)", cadence)
		}
	}
	if c.Harness.Review.EveryNRuns < 0 {
		return fmt.Errorf("harness.review.every_n_runs must be non-negative")
	}
	if c.Harness.Review.MinSamples < 0 {
		return fmt.Errorf("harness.review.min_samples must be non-negative")
	}
	if c.Harness.Review.LookbackRuns < 0 {
		return fmt.Errorf("harness.review.lookback_runs must be non-negative")
	}
	if c.Harness.Review.Enabled && c.HarnessReviewCadence() == "every_n_runs" && c.HarnessReviewEveryNRuns() <= 0 {
		return fmt.Errorf("harness.review.every_n_runs must be greater than 0 when cadence is every_n_runs")
	}
	if outputDir := strings.TrimSpace(c.Harness.Review.OutputDir); outputDir != "" {
		if filepath.IsAbs(outputDir) {
			return fmt.Errorf("harness.review.output_dir must be relative to state_dir")
		}
		cleaned := filepath.Clean(outputDir)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("harness.review.output_dir must stay within state_dir")
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

func (c *Config) validateTelemetry() error {
	if repo := strings.TrimSpace(c.Telemetry.TargetRepo); repo != "" {
		parts := strings.Split(repo, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("telemetry.target_repo must be in owner/name format")
		}
	}
	return nil
}

func (c *Config) validateWorkflowRequirements() error {
	active := c.validationRequiredWorkflows()
	if len(active) == 0 {
		return nil
	}
	if !c.ValidationConfigured() {
		return fmt.Errorf("validation: at least one of format, lint, build, or test must be set when workflows are active: %s", strings.Join(active, ", "))
	}
	if err := c.validateValidationCommands(); err != nil {
		return fmt.Errorf("%w (active workflows: %s)", err, strings.Join(active, ", "))
	}
	return nil
}

func (c *Config) validationRequiredWorkflows() []string {
	required := []string{"adapt-repo", "fix-pr-checks", "resolve-conflicts"}
	active := make([]string, 0, len(required))
	for _, workflowName := range required {
		if c.workflowActive(workflowName) {
			active = append(active, workflowName)
		}
	}
	if len(active) > 0 {
		sort.Strings(active)
	}
	return active
}

func (c *Config) validateValidationCommands() error {
	if target, ok := invalidGoimportsPackagePatternTarget(c.Validation.Format); ok {
		return fmt.Errorf(`validation.format uses goimports package pattern %q; goimports expects directories or files, use "goimports -l ." or "cd cli && goimports -l ."`, target)
	}
	for _, check := range []struct {
		name    string
		command string
		example string
	}{
		{name: "lint", command: c.Validation.Lint, example: `cd cli && go vet ./...`},
		{name: "build", command: c.Validation.Build, example: `cd cli && go build ./cmd/xylem`},
		{name: "test", command: c.Validation.Test, example: `cd cli && go test ./...`},
	} {
		if target, ok := invalidRepoRootGoCLITarget(check.command); ok {
			return fmt.Errorf(`validation.%s runs go from repo root against %q; xylem executes validation from the worktree root, use %q`, check.name, target, check.example)
		}
	}
	return nil
}

func invalidGoimportsPackagePatternTarget(command string) (string, bool) {
	fields := validationCommandFields(command)
	for i := 0; i < len(fields); i++ {
		token := trimValidationCommandField(fields[i])
		if filepath.Base(token) != "goimports" {
			continue
		}
		for j := i + 1; j < len(fields); j++ {
			rawArg := fields[j]
			arg := trimValidationCommandField(rawArg)
			if isValidationCommandSeparator(arg) {
				break
			}
			if arg == "" {
				continue
			}
			if strings.HasPrefix(arg, "-") {
				if goimportsFlagConsumesValue(arg) {
					j++
				}
				continue
			}
			if strings.Contains(arg, "...") {
				return arg, true
			}
		}
	}
	return "", false
}

func invalidRepoRootGoCLITarget(command string) (string, bool) {
	fields := validationCommandFields(command)
	for i := 0; i < len(fields); i++ {
		token := trimValidationCommandField(fields[i])
		if filepath.Base(token) != "go" {
			continue
		}
		for j := i + 1; j < len(fields); j++ {
			arg := trimValidationCommandField(fields[j])
			if isValidationCommandSeparator(arg) {
				break
			}
			if strings.HasPrefix(arg, "./cli/") || strings.HasPrefix(arg, "cli/") {
				return arg, true
			}
		}
	}
	return "", false
}

func goimportsFlagConsumesValue(arg string) bool {
	flag, _, hasInlineValue := strings.Cut(arg, "=")
	if hasInlineValue {
		return false
	}
	switch flag {
	case "-cpuprofile", "-local", "-memprofile", "-memrate", "-srcdir", "-trace":
		return true
	default:
		return false
	}
}

func validationCommandFields(command string) []string {
	replacer := strings.NewReplacer(
		"&&", " && ",
		"||", " || ",
		";", " ; ",
		"\n", " ; ",
		"\t", " ",
	)
	return strings.Fields(replacer.Replace(command))
}

func trimValidationCommandField(field string) string {
	return strings.Trim(field, `"'`)
}

func isValidationCommandSeparator(field string) bool {
	switch field {
	case "", "&&", "||", ";", "|":
		return true
	default:
		return false
	}
}

func (c *Config) workflowActive(name string) bool {
	for _, task := range c.Tasks {
		if strings.TrimSpace(task.Workflow) == name {
			return true
		}
	}
	for _, src := range c.Sources {
		if strings.TrimSpace(src.Workflow) == name {
			return true
		}
		for _, task := range src.Tasks {
			if strings.TrimSpace(task.Workflow) == name {
				return true
			}
		}
	}
	return false
}

// validateProviderDefaultModels ensures every active LLM provider has a
// default_model configured. A provider is active if it is the global llm value
// or referenced by any source's llm field.
func (c *Config) validateProviders() error {
	if len(c.Providers) == 0 && len(c.LLMRouting.Tiers) == 0 {
		return nil
	}

	for name, provider := range c.Providers {
		switch provider.Kind {
		case "claude", "copilot":
		default:
			return fmt.Errorf("providers.%s.kind must be one of claude or copilot, got %q", name, provider.Kind)
		}

		for tierName := range c.LLMRouting.Tiers {
			model, ok := provider.Tiers[tierName]
			if !ok || strings.TrimSpace(model) == "" {
				return fmt.Errorf("providers.%s.tiers.%s must be set because llm_routing.tiers.%s is configured", name, tierName, tierName)
			}
		}

		if provider.Kind == "claude" && strings.Contains(provider.Flags, "--bare") {
			if provider.Env == nil || strings.TrimSpace(provider.Env["ANTHROPIC_API_KEY"]) == "" {
				return fmt.Errorf("--bare requires ANTHROPIC_API_KEY in providers.%s.env", name)
			}
		}
	}

	if strings.TrimSpace(c.LLMRouting.DefaultTier) == "" {
		return fmt.Errorf("llm_routing.default_tier must be set")
	}
	if _, ok := c.LLMRouting.Tiers[c.LLMRouting.DefaultTier]; !ok {
		return fmt.Errorf("llm_routing.default_tier %q must exist in llm_routing.tiers", c.LLMRouting.DefaultTier)
	}

	for tierName, routing := range c.LLMRouting.Tiers {
		for _, providerName := range routing.Providers {
			if _, ok := c.Providers[providerName]; !ok {
				return fmt.Errorf("llm_routing.tiers.%s.providers references unknown provider %q", tierName, providerName)
			}
		}
	}

	return nil
}

func (c *Config) normalizeProviders() {
	defaultTier := strings.TrimSpace(c.LLMRouting.DefaultTier)
	if defaultTier == "" {
		defaultTier = DefaultLLMRoutingTier
		c.LLMRouting.DefaultTier = defaultTier
	}

	if len(c.Providers) == 0 {
		providers := make(map[string]ProviderConfig)
		active := c.activeLegacyProviders()
		if c.shouldSynthesizeClaudeProvider(active["claude"]) {
			providers["claude"] = ProviderConfig{
				Kind:         "claude",
				Command:      c.Claude.Command,
				Flags:        c.Claude.Flags,
				Tiers:        map[string]string{defaultTier: c.Claude.DefaultModel},
				Env:          copyStringMap(c.Claude.Env),
				AllowedTools: append([]string(nil), c.Claude.AllowedTools...),
			}
		}
		if c.shouldSynthesizeCopilotProvider(active["copilot"]) {
			providers["copilot"] = ProviderConfig{
				Kind:    "copilot",
				Command: c.Copilot.Command,
				Flags:   c.Copilot.Flags,
				Tiers:   map[string]string{defaultTier: c.Copilot.DefaultModel},
				Env:     copyStringMap(c.Copilot.Env),
			}
		}
		if len(providers) > 0 {
			c.Providers = providers
		}
	}

	if len(c.LLMRouting.Tiers) == 0 {
		providerNames := orderedProviderNames(c.Providers, c.LLM)
		if len(providerNames) > 0 {
			c.LLMRouting.Tiers = map[string]TierRouting{
				defaultTier: {Providers: providerNames},
			}
		}
	}
}

func (c *Config) activeLegacyProviders() map[string]bool {
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
	return active
}

func (c *Config) shouldSynthesizeClaudeProvider(active bool) bool {
	if active {
		return true
	}
	return strings.TrimSpace(c.Claude.DefaultModel) != "" ||
		strings.TrimSpace(c.Claude.Flags) != "" ||
		len(c.Claude.Env) > 0 ||
		len(c.Claude.AllowedTools) > 0 ||
		strings.TrimSpace(c.Claude.Template) != "" ||
		(strings.TrimSpace(c.Claude.Command) != "" && strings.TrimSpace(c.Claude.Command) != "claude")
}

func (c *Config) shouldSynthesizeCopilotProvider(active bool) bool {
	if active {
		return true
	}
	return strings.TrimSpace(c.Copilot.DefaultModel) != "" ||
		strings.TrimSpace(c.Copilot.Flags) != "" ||
		len(c.Copilot.Env) > 0 ||
		(strings.TrimSpace(c.Copilot.Command) != "" && strings.TrimSpace(c.Copilot.Command) != "copilot")
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func orderedProviderNames(providers map[string]ProviderConfig, preferred string) []string {
	if len(providers) == 0 {
		return nil
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	if preferred == "" {
		return names
	}
	for i, name := range names {
		if name != preferred {
			continue
		}
		return append([]string{name}, append(names[:i], names[i+1:]...)...)
	}
	return names
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
		if len(task.On.Labels) == 0 &&
			!task.On.ReviewSubmitted &&
			!task.On.ChecksFailed &&
			!task.On.Commented &&
			!task.On.PROpened &&
			!task.On.PRHeadUpdated {
			return fmt.Errorf("source %q task %q: 'on' block must specify at least one trigger (labels, review_submitted, checks_failed, commented, pr_opened, or pr_head_updated)", name, tname)
		}
		// Authored-event triggers must specify an author filter to prevent
		// self-trigger loops (e.g. xylem responds to its own review as hnipps,
		// that review triggers another vessel, ad infinitum).
		if (task.On.ReviewSubmitted || task.On.Commented) && len(task.On.AuthorAllow) == 0 && len(task.On.AuthorDeny) == 0 {
			return fmt.Errorf("source %q task %q: tasks with review_submitted or commented must specify author_allow or author_deny to prevent self-trigger loops", name, tname)
		}
		if strings.TrimSpace(task.On.Debounce) != "" {
			debounce, err := time.ParseDuration(task.On.Debounce)
			if err != nil {
				return fmt.Errorf("source %q task %q: parse debounce %q: %w", name, tname, task.On.Debounce, err)
			}
			if debounce < 0 {
				return fmt.Errorf("source %q task %q: debounce must be non-negative", name, tname)
			}
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

func validateScheduledSource(name string, src SourceConfig) error {
	repo := strings.TrimSpace(src.Repo)
	if repo == "" {
		return fmt.Errorf("source %q (scheduled): repo is required", name)
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("source %q (scheduled): repo must be in owner/name format", name)
	}
	if strings.TrimSpace(src.Schedule) == "" {
		return fmt.Errorf("source %q (scheduled): schedule is required", name)
	}
	if _, err := parseScheduleValue(src.Schedule); err != nil {
		return fmt.Errorf("source %q (scheduled): schedule is invalid: %w", name, err)
	}
	if len(src.Tasks) == 0 {
		return fmt.Errorf("source %q (scheduled): at least one task is required", name)
	}
	for tname, task := range src.Tasks {
		if strings.TrimSpace(task.Workflow) == "" {
			return fmt.Errorf("source %q task %q: must include a workflow", name, tname)
		}
	}
	return nil
}

func parseScheduleValue(value string) (time.Duration, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "@hourly":
		return time.Hour, nil
	case "@daily":
		return 24 * time.Hour, nil
	case "@weekly":
		return 7 * 24 * time.Hour, nil
	case "@monthly":
		return 30 * 24 * time.Hour, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if interval <= 0 {
		return 0, fmt.Errorf("must be greater than 0")
	}
	return interval, nil
}
func validateScheduleSource(name string, src SourceConfig) error {
	if strings.TrimSpace(src.Workflow) == "" {
		return fmt.Errorf("source %q (schedule): workflow is required", name)
	}
	if len(src.Tasks) > 0 {
		return fmt.Errorf("source %q (schedule): tasks are not supported; configure workflow at the source level", name)
	}
	if _, err := cadence.Parse(src.Cadence); err != nil {
		return fmt.Errorf("source %q (schedule): cadence is invalid: %w", name, err)
	}
	return nil
}
