package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// validPhaseName matches names starting with a lowercase letter, followed by
// lowercase letters, digits, or underscores. Names must start with a letter so
// they work as Go template identifiers in dot notation (e.g. .PreviousOutputs.analyze).
var validPhaseName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Workflow defines a multi-phase execution plan loaded from a YAML file.
type Workflow struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description,omitempty"`
	LLM         *string `yaml:"llm,omitempty"`
	Model       *string `yaml:"model,omitempty"`
	Phases      []Phase `yaml:"phases"`
}

// Phase represents a single step in a workflow's execution pipeline.
type Phase struct {
	Name         string  `yaml:"name"`
	Type         string  `yaml:"type,omitempty"`          // "prompt" (default) or "command"
	Run          string  `yaml:"run,omitempty"`            // shell command for type=command, supports template variables
	PromptFile   string  `yaml:"prompt_file"`
	MaxTurns     int     `yaml:"max_turns"`
	LLM          *string `yaml:"llm,omitempty"`
	Model        *string `yaml:"model,omitempty"`
	NoOp         *NoOp   `yaml:"noop,omitempty"`
	Gate         *Gate   `yaml:"gate,omitempty"`
	AllowedTools *string  `yaml:"allowed_tools,omitempty"`
	DependsOn    []string `yaml:"depends_on,omitempty"`
}

// NoOp defines an early-success completion rule for a phase.
type NoOp struct {
	Match string `yaml:"match"`
}

// Gate defines an inter-phase quality gate that must pass before the next phase begins.
type Gate struct {
	Type         string `yaml:"type"`                    // "command" or "label"
	Run          string `yaml:"run,omitempty"`           // shell command (type=command)
	Retries      int    `yaml:"retries,omitempty"`       // default 0
	RetryDelay   string `yaml:"retry_delay,omitempty"`   // default "10s"
	WaitFor      string `yaml:"wait_for,omitempty"`      // label name (type=label)
	Timeout      string `yaml:"timeout,omitempty"`       // default "24h" (type=label)
	PollInterval string `yaml:"poll_interval,omitempty"` // default "60s" (type=label)
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
	default:
		return fmt.Errorf("phase %q: gate: type must be \"command\" or \"label\"", phaseName)
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
