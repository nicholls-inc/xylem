package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/policy"
	"gopkg.in/yaml.v3"
)

// validPhaseName matches names starting with a lowercase letter, followed by
// lowercase letters, digits, or underscores. Names must start with a letter so
// they work as Go template identifiers in dot notation (e.g. .PreviousOutputs.analyze).
var validPhaseName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Workflow defines a multi-phase execution plan loaded from a YAML file.
type Workflow struct {
	Name                          string  `yaml:"name"`
	Description                   string  `yaml:"description,omitempty"`
	Class                         string  `yaml:"class,omitempty"`
	LLM                           *string `yaml:"llm,omitempty"`
	Model                         *string `yaml:"model,omitempty"`
	AllowAdditiveProtectedWrites  bool    `yaml:"allow_additive_protected_writes,omitempty"`
	AllowCanonicalProtectedWrites bool    `yaml:"allow_canonical_protected_writes,omitempty"`
	Phases                        []Phase `yaml:"phases"`
}

// Phase represents a single step in a workflow's execution pipeline.
type Phase struct {
	Name         string   `yaml:"name"`
	Type         string   `yaml:"type,omitempty"` // "prompt" (default) or "command"
	Run          string   `yaml:"run,omitempty"`  // shell command for type=command, supports template variables
	PromptFile   string   `yaml:"prompt_file"`
	MaxTurns     int      `yaml:"max_turns"`
	LLM          *string  `yaml:"llm,omitempty"`
	Model        *string  `yaml:"model,omitempty"`
	NoOp         *NoOp    `yaml:"noop,omitempty"`
	Gate         *Gate    `yaml:"gate,omitempty"`
	AllowedTools *string  `yaml:"allowed_tools,omitempty"`
	DependsOn    []string `yaml:"depends_on,omitempty"`
}

// NoOp defines an early-success completion rule for a phase.
type NoOp struct {
	Match string `yaml:"match"`
}

// GateEvidence describes the verification evidence metadata attached to a gate.
type GateEvidence struct {
	Claim         string `yaml:"claim,omitempty"`
	Level         string `yaml:"level,omitempty"`
	Checker       string `yaml:"checker,omitempty"`
	TrustBoundary string `yaml:"trust_boundary,omitempty"`
}

// Gate defines an inter-phase quality gate that must pass before the next phase begins.
type Gate struct {
	Type         string        `yaml:"type"`                    // "command" or "label"
	Run          string        `yaml:"run,omitempty"`           // shell command (type=command), supports template variables
	Retries      int           `yaml:"retries,omitempty"`       // default 0
	RetryDelay   string        `yaml:"retry_delay,omitempty"`   // default "10s"
	WaitFor      string        `yaml:"wait_for,omitempty"`      // label name (type=label)
	Timeout      string        `yaml:"timeout,omitempty"`       // default "24h" (type=label)
	PollInterval string        `yaml:"poll_interval,omitempty"` // default "60s" (type=label)
	Live         *LiveGate     `yaml:"live,omitempty"`
	Evidence     *GateEvidence `yaml:"evidence,omitempty"`
}

// LiveGate defines runtime verification against a running system.
type LiveGate struct {
	Mode          string                 `yaml:"mode"`
	Timeout       string                 `yaml:"timeout,omitempty"`
	HTTP          *LiveHTTPGate          `yaml:"http,omitempty"`
	Browser       *LiveBrowserGate       `yaml:"browser,omitempty"`
	CommandAssert *LiveCommandAssertGate `yaml:"command_assert,omitempty"`
}

// LiveHTTPGate defines an HTTP verification sequence.
type LiveHTTPGate struct {
	BaseURL string         `yaml:"base_url,omitempty"`
	Steps   []LiveHTTPStep `yaml:"steps"`
}

// LiveHTTPStep defines a single HTTP assertion step.
type LiveHTTPStep struct {
	Name            string              `yaml:"name,omitempty"`
	Method          string              `yaml:"method,omitempty"`
	URL             string              `yaml:"url"`
	Headers         map[string]string   `yaml:"headers,omitempty"`
	Body            string              `yaml:"body,omitempty"`
	Timeout         string              `yaml:"timeout,omitempty"`
	ExpectStatus    int                 `yaml:"expect_status,omitempty"`
	MaxLatency      string              `yaml:"max_latency,omitempty"`
	ExpectHeaders   []LiveHeaderAssert  `yaml:"expect_headers,omitempty"`
	ExpectJSON      []LiveJSONPathCheck `yaml:"expect_json,omitempty"`
	ExpectBodyRegex string              `yaml:"expect_body_regex,omitempty"`
}

// LiveHeaderAssert describes a response-header assertion.
type LiveHeaderAssert struct {
	Name   string `yaml:"name"`
	Equals string `yaml:"equals,omitempty"`
	Regex  string `yaml:"regex,omitempty"`
}

// LiveJSONPathCheck describes a JSONPath-based assertion.
type LiveJSONPathCheck struct {
	Path   string `yaml:"path"`
	Equals string `yaml:"equals,omitempty"`
	Regex  string `yaml:"regex,omitempty"`
	Exists *bool  `yaml:"exists,omitempty"`
}

// LiveBrowserGate defines a browser verification script.
type LiveBrowserGate struct {
	BaseURL  string            `yaml:"base_url,omitempty"`
	Headless *bool             `yaml:"headless,omitempty"`
	Steps    []LiveBrowserStep `yaml:"steps"`
}

// LiveBrowserStep defines one browser action or assertion.
type LiveBrowserStep struct {
	Name     string `yaml:"name,omitempty"`
	Action   string `yaml:"action"`
	URL      string `yaml:"url,omitempty"`
	Selector string `yaml:"selector,omitempty"`
	Text     string `yaml:"text,omitempty"`
	Value    string `yaml:"value,omitempty"`
	Timeout  string `yaml:"timeout,omitempty"`
}

// LiveCommandAssertGate defines a command+assert verification step.
type LiveCommandAssertGate struct {
	Run               string              `yaml:"run"`
	Timeout           string              `yaml:"timeout,omitempty"`
	ExpectStdoutRegex string              `yaml:"expect_stdout_regex,omitempty"`
	ExpectJSON        []LiveJSONPathCheck `yaml:"expect_json,omitempty"`
}

// Load reads and validates a workflow definition YAML file at path.
func Load(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow file %q: %w", path, err)
	}

	var s Workflow
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse workflow yaml %q: %w", path, err)
	}

	s.normalizeClass()

	if err := s.Validate(path); err != nil {
		return nil, fmt.Errorf("validate workflow %q: %w", path, err)
	}

	return &s, nil
}

// Validate checks that the workflow definition is well-formed. workflowFilePath is
// the path to the workflow YAML file, used to verify the workflow name matches the
// filename. Prompt file paths are resolved relative to the current working
// directory (repo root).
func (s *Workflow) Validate(workflowFilePath string) error {
	s.normalizeClass()

	if s.Name == "" {
		return fmt.Errorf(`"name" is required`)
	}

	expectedName := strings.TrimSuffix(filepath.Base(workflowFilePath), filepath.Ext(workflowFilePath))
	if s.Name != expectedName {
		return fmt.Errorf("workflow name %q does not match filename %q", s.Name, filepath.Base(workflowFilePath))
	}

	if len(s.Phases) == 0 {
		return fmt.Errorf(`"phases" is required`)
	}

	if err := validateLLM(s.LLM, "workflow"); err != nil {
		return err
	}
	if !policy.NormalizeClass(s.Class).Valid() || s.Class != string(policy.NormalizeClass(s.Class)) {
		return fmt.Errorf("workflow class %q is invalid; must be delivery, harness-maintenance, or ops", s.Class)
	}
	switch policy.Class(s.Class) {
	case policy.Delivery, policy.Ops:
		if s.AllowAdditiveProtectedWrites {
			return fmt.Errorf("workflow class %q must not set allow_additive_protected_writes", s.Class)
		}
		if s.AllowCanonicalProtectedWrites {
			return fmt.Errorf("workflow class %q must not set allow_canonical_protected_writes", s.Class)
		}
	}

	// Collect all phase names upfront for dependency reference validation.
	allNames := make(map[string]bool, len(s.Phases))
	for _, p := range s.Phases {
		allNames[p.Name] = true
	}

	seen := make(map[string]bool, len(s.Phases))
	for _, p := range s.Phases {
		if p.Name == "" {
			return fmt.Errorf("each phase must have a non-empty name")
		}

		if seen[p.Name] {
			return fmt.Errorf("duplicate phase name %q", p.Name)
		}
		seen[p.Name] = true

		if !validPhaseName.MatchString(p.Name) {
			return fmt.Errorf("phase name %q is invalid; must start with a lowercase letter and contain only lowercase letters, digits, and underscores", p.Name)
		}

		switch p.Type {
		case "", "prompt":
			if p.PromptFile == "" {
				return fmt.Errorf("phase %q: prompt_file is required", p.Name)
			}

			if _, err := os.Stat(p.PromptFile); err != nil {
				return fmt.Errorf("phase %q: prompt_file not found: %s", p.Name, p.PromptFile)
			}

			if p.MaxTurns <= 0 {
				return fmt.Errorf("phase %q: max_turns must be greater than 0", p.Name)
			}
		case "command":
			if strings.TrimSpace(p.Run) == "" {
				return fmt.Errorf("phase %q: run is required for command phase", p.Name)
			}
		default:
			return fmt.Errorf("phase %q: type must be \"prompt\" or \"command\", got %q", p.Name, p.Type)
		}

		if p.Gate != nil {
			if err := validateGate(p.Name, p.Gate); err != nil {
				return err
			}
		}

		if p.NoOp != nil {
			if err := validateNoOp(p.Name, p.NoOp); err != nil {
				return err
			}
		}

		if p.AllowedTools != nil && *p.AllowedTools == "" {
			return fmt.Errorf("phase %q: allowed_tools must not be empty when specified", p.Name)
		}

		if err := validateLLM(p.LLM, fmt.Sprintf("phase %q", p.Name)); err != nil {
			return err
		}

		seenDeps := make(map[string]bool, len(p.DependsOn))
		for _, dep := range p.DependsOn {
			if seenDeps[dep] {
				return fmt.Errorf("phase %q: depends_on contains duplicate entry %q", p.Name, dep)
			}
			seenDeps[dep] = true
			if dep == p.Name {
				return fmt.Errorf("phase %q: depends_on contains self-reference", p.Name)
			}
			if !allNames[dep] {
				return fmt.Errorf("phase %q: depends_on references unknown phase %q", p.Name, dep)
			}
		}
	}

	if err := validateDependencyCycles(s.Phases); err != nil {
		return err
	}

	return nil
}

func (s *Workflow) normalizeClass() {
	if strings.TrimSpace(s.Class) != "" {
		if policy.Class(s.Class).Valid() {
			s.Class = string(policy.Class(s.Class))
		}
		return
	}
	if s.AllowAdditiveProtectedWrites || s.AllowCanonicalProtectedWrites {
		s.Class = string(policy.HarnessMaintenance)
		return
	}
	s.Class = string(policy.Delivery)
}

// HasDependencies returns true if any phase declares explicit depends_on.
func (s *Workflow) HasDependencies() bool {
	for _, p := range s.Phases {
		if len(p.DependsOn) > 0 {
			return true
		}
	}
	return false
}

// validateDependencyCycles checks for cycles in the phase dependency graph.
func validateDependencyCycles(phases []Phase) error {
	hasDeps := false
	for _, p := range phases {
		if len(p.DependsOn) > 0 {
			hasDeps = true
			break
		}
	}
	if !hasDeps {
		return nil
	}

	// Build adjacency list: edge from dep -> phase (dep must complete before phase).
	adj := make(map[string][]string, len(phases))
	for _, p := range phases {
		adj[p.Name] = nil // ensure all nodes present
	}
	for _, p := range phases {
		for _, dep := range p.DependsOn {
			adj[dep] = append(adj[dep], p.Name)
		}
	}

	const (
		white = 0 // unvisited
		gray  = 1 // in current path
		black = 2 // fully explored
	)
	color := make(map[string]int, len(phases))

	var dfs func(string) bool
	dfs = func(u string) bool {
		color[u] = gray
		for _, v := range adj[u] {
			if color[v] == gray {
				return true
			}
			if color[v] == white && dfs(v) {
				return true
			}
		}
		color[u] = black
		return false
	}

	for _, p := range phases {
		if color[p.Name] == white {
			if dfs(p.Name) {
				return fmt.Errorf("depends_on creates a cycle in the phase graph")
			}
		}
	}
	return nil
}

func validateNoOp(phaseName string, n *NoOp) error {
	if strings.TrimSpace(n.Match) == "" {
		return fmt.Errorf("phase %q: noop: match is required", phaseName)
	}
	return nil
}

func validateGate(phaseName string, g *Gate) error {
	switch g.Type {
	case "command":
		if g.Run == "" {
			return fmt.Errorf("phase %q: gate: run is required for command gate", phaseName)
		}
	case "label":
		if g.WaitFor == "" {
			return fmt.Errorf("phase %q: gate: wait_for is required for label gate", phaseName)
		}
	case "live":
		if g.Live == nil {
			return fmt.Errorf("phase %q: gate: live config is required for live gate", phaseName)
		}
		if err := validateLiveGate(phaseName, g.Live); err != nil {
			return err
		}
	default:
		return fmt.Errorf("phase %q: gate: type must be \"command\", \"label\", or \"live\"", phaseName)
	}

	for _, d := range []struct {
		name, value string
	}{
		{"retry_delay", g.RetryDelay},
		{"timeout", g.Timeout},
		{"poll_interval", g.PollInterval},
	} {
		if d.value != "" {
			if _, err := time.ParseDuration(d.value); err != nil {
				return fmt.Errorf("phase %q: gate: invalid %s %q: %w", phaseName, d.name, d.value, err)
			}
		}
	}

	if err := validateGateEvidence(phaseName, g.Evidence); err != nil {
		return err
	}

	return nil
}

func validateGateEvidence(phaseName string, e *GateEvidence) error {
	if e == nil || e.Level == "" {
		return nil
	}

	level := evidence.Level(e.Level)
	if !level.Valid() || level == evidence.Untyped {
		return fmt.Errorf("phase %q: gate evidence level %q is not valid (must be proved, mechanically_checked, behaviorally_checked, or observed_in_situ)", phaseName, e.Level)
	}

	return nil
}

func validateLiveGate(phaseName string, g *LiveGate) error {
	if g == nil {
		return fmt.Errorf("phase %q: gate: live config is required", phaseName)
	}
	if g.Timeout != "" {
		if _, err := time.ParseDuration(g.Timeout); err != nil {
			return fmt.Errorf("phase %q: gate: invalid live timeout %q: %w", phaseName, g.Timeout, err)
		}
	}

	switch g.Mode {
	case "http":
		if g.HTTP == nil {
			return fmt.Errorf("phase %q: gate: live.http is required for mode %q", phaseName, g.Mode)
		}
		if g.Browser != nil || g.CommandAssert != nil {
			return fmt.Errorf("phase %q: gate: live mode %q must not set browser or command_assert config", phaseName, g.Mode)
		}
		return validateLiveHTTPGate(phaseName, g.HTTP)
	case "browser":
		if g.Browser == nil {
			return fmt.Errorf("phase %q: gate: live.browser is required for mode %q", phaseName, g.Mode)
		}
		if g.HTTP != nil || g.CommandAssert != nil {
			return fmt.Errorf("phase %q: gate: live mode %q must not set http or command_assert config", phaseName, g.Mode)
		}
		return validateLiveBrowserGate(phaseName, g.Browser)
	case "command+assert":
		if g.CommandAssert == nil {
			return fmt.Errorf("phase %q: gate: live.command_assert is required for mode %q", phaseName, g.Mode)
		}
		if g.HTTP != nil || g.Browser != nil {
			return fmt.Errorf("phase %q: gate: live mode %q must not set http or browser config", phaseName, g.Mode)
		}
		return validateLiveCommandAssertGate(phaseName, g.CommandAssert)
	default:
		return fmt.Errorf("phase %q: gate: live mode must be \"http\", \"browser\", or \"command+assert\"", phaseName)
	}
}

func validateLiveHTTPGate(phaseName string, g *LiveHTTPGate) error {
	if len(g.Steps) == 0 {
		return fmt.Errorf("phase %q: gate: live.http.steps must contain at least one step", phaseName)
	}

	for i, step := range g.Steps {
		if strings.TrimSpace(step.URL) == "" {
			return fmt.Errorf("phase %q: gate: live.http.steps[%d].url is required", phaseName, i)
		}
		if step.Timeout != "" {
			if _, err := time.ParseDuration(step.Timeout); err != nil {
				return fmt.Errorf("phase %q: gate: invalid live.http.steps[%d].timeout %q: %w", phaseName, i, step.Timeout, err)
			}
		}
		if step.MaxLatency != "" {
			if _, err := time.ParseDuration(step.MaxLatency); err != nil {
				return fmt.Errorf("phase %q: gate: invalid live.http.steps[%d].max_latency %q: %w", phaseName, i, step.MaxLatency, err)
			}
		}
		if step.ExpectBodyRegex != "" {
			if _, err := regexp.Compile(step.ExpectBodyRegex); err != nil {
				return fmt.Errorf("phase %q: gate: invalid live.http.steps[%d].expect_body_regex %q: %w", phaseName, i, step.ExpectBodyRegex, err)
			}
		}
		for j, header := range step.ExpectHeaders {
			if err := validateLiveHeaderAssert(phaseName, fmt.Sprintf("live.http.steps[%d].expect_headers[%d]", i, j), header); err != nil {
				return err
			}
		}
		for j, check := range step.ExpectJSON {
			if err := validateLiveJSONPathCheck(phaseName, fmt.Sprintf("live.http.steps[%d].expect_json[%d]", i, j), check); err != nil {
				return err
			}
		}
		if step.ExpectStatus == 0 && step.MaxLatency == "" && step.ExpectBodyRegex == "" &&
			len(step.ExpectHeaders) == 0 && len(step.ExpectJSON) == 0 {
			return fmt.Errorf("phase %q: gate: live.http.steps[%d] must declare at least one expectation", phaseName, i)
		}
	}

	return nil
}

func validateLiveBrowserGate(phaseName string, g *LiveBrowserGate) error {
	if len(g.Steps) == 0 {
		return fmt.Errorf("phase %q: gate: live.browser.steps must contain at least one step", phaseName)
	}

	for i, step := range g.Steps {
		if step.Timeout != "" {
			if _, err := time.ParseDuration(step.Timeout); err != nil {
				return fmt.Errorf("phase %q: gate: invalid live.browser.steps[%d].timeout %q: %w", phaseName, i, step.Timeout, err)
			}
		}
		switch step.Action {
		case "navigate":
			if strings.TrimSpace(step.URL) == "" {
				return fmt.Errorf("phase %q: gate: live.browser.steps[%d].url is required for navigate action", phaseName, i)
			}
		case "click", "wait_visible", "assert_visible":
			if strings.TrimSpace(step.Selector) == "" {
				return fmt.Errorf("phase %q: gate: live.browser.steps[%d].selector is required for %s action", phaseName, i, step.Action)
			}
		case "type":
			if strings.TrimSpace(step.Selector) == "" {
				return fmt.Errorf("phase %q: gate: live.browser.steps[%d].selector is required for type action", phaseName, i)
			}
			if step.Value == "" {
				return fmt.Errorf("phase %q: gate: live.browser.steps[%d].value is required for type action", phaseName, i)
			}
		case "assert_text":
			if strings.TrimSpace(step.Selector) == "" {
				return fmt.Errorf("phase %q: gate: live.browser.steps[%d].selector is required for assert_text action", phaseName, i)
			}
			if step.Text == "" {
				return fmt.Errorf("phase %q: gate: live.browser.steps[%d].text is required for assert_text action", phaseName, i)
			}
		default:
			return fmt.Errorf("phase %q: gate: live.browser.steps[%d].action must be one of navigate, click, type, wait_visible, assert_visible, or assert_text", phaseName, i)
		}
	}

	return nil
}

func validateLiveCommandAssertGate(phaseName string, g *LiveCommandAssertGate) error {
	if strings.TrimSpace(g.Run) == "" {
		return fmt.Errorf("phase %q: gate: live.command_assert.run is required", phaseName)
	}
	if g.Timeout != "" {
		if _, err := time.ParseDuration(g.Timeout); err != nil {
			return fmt.Errorf("phase %q: gate: invalid live.command_assert.timeout %q: %w", phaseName, g.Timeout, err)
		}
	}
	if g.ExpectStdoutRegex != "" {
		if _, err := regexp.Compile(g.ExpectStdoutRegex); err != nil {
			return fmt.Errorf("phase %q: gate: invalid live.command_assert.expect_stdout_regex %q: %w", phaseName, g.ExpectStdoutRegex, err)
		}
	}
	for i, check := range g.ExpectJSON {
		if err := validateLiveJSONPathCheck(phaseName, fmt.Sprintf("live.command_assert.expect_json[%d]", i), check); err != nil {
			return err
		}
	}
	if g.ExpectStdoutRegex == "" && len(g.ExpectJSON) == 0 {
		return fmt.Errorf("phase %q: gate: live.command_assert must declare expect_stdout_regex or expect_json", phaseName)
	}
	return nil
}

func validateLiveHeaderAssert(phaseName, field string, assert LiveHeaderAssert) error {
	if strings.TrimSpace(assert.Name) == "" {
		return fmt.Errorf("phase %q: gate: %s.name is required", phaseName, field)
	}
	if assert.Equals == "" && assert.Regex == "" {
		return fmt.Errorf("phase %q: gate: %s must declare equals or regex", phaseName, field)
	}
	if assert.Regex != "" {
		if _, err := regexp.Compile(assert.Regex); err != nil {
			return fmt.Errorf("phase %q: gate: invalid %s.regex %q: %w", phaseName, field, assert.Regex, err)
		}
	}
	return nil
}

func validateLiveJSONPathCheck(phaseName, field string, check LiveJSONPathCheck) error {
	if strings.TrimSpace(check.Path) == "" {
		return fmt.Errorf("phase %q: gate: %s.path is required", phaseName, field)
	}
	if !strings.HasPrefix(check.Path, "$") {
		return fmt.Errorf("phase %q: gate: %s.path must start with '$'", phaseName, field)
	}
	if check.Equals == "" && check.Regex == "" && check.Exists == nil {
		return fmt.Errorf("phase %q: gate: %s must declare equals, regex, or exists", phaseName, field)
	}
	if check.Regex != "" {
		if _, err := regexp.Compile(check.Regex); err != nil {
			return fmt.Errorf("phase %q: gate: invalid %s.regex %q: %w", phaseName, field, check.Regex, err)
		}
	}
	return nil
}

// validateLLM checks that the llm field, if set, is a known provider.
// context is a human-readable location string used in error messages (e.g. "workflow" or `phase "analyze"`).
func validateLLM(llm *string, context string) error {
	if llm == nil || *llm == "" {
		return nil
	}
	switch *llm {
	case "claude", "copilot":
		return nil
	default:
		return fmt.Errorf("%s: llm must be \"claude\" or \"copilot\", got %q", context, *llm)
	}
}
