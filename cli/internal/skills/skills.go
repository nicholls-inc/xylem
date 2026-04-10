package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DirName      = ".claude/skills"
	EntryFile    = "SKILL.md"
	maxNameChars = 64
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// Definition is a project-local Claude Code skill definition.
type Definition struct {
	Directory              string
	File                   string
	Name                   string
	Description            string
	ArgumentHint           string
	DisableModelInvocation bool
	UserInvocable          *bool
	AllowedTools           []string
	Body                   string
}

type frontmatter struct {
	Name                   string     `yaml:"name,omitempty"`
	Description            string     `yaml:"description,omitempty"`
	ArgumentHint           string     `yaml:"argument-hint,omitempty"`
	DisableModelInvocation bool       `yaml:"disable-model-invocation,omitempty"`
	UserInvocable          *bool      `yaml:"user-invocable,omitempty"`
	AllowedTools           stringList `yaml:"allowed-tools,omitempty"`
}

type stringList []string

func (l *stringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case 0:
		return nil
	case yaml.ScalarNode:
		var raw string
		if err := node.Decode(&raw); err != nil {
			return err
		}
		*l = splitTools(raw)
		return nil
	case yaml.SequenceNode:
		var values []string
		if err := node.Decode(&values); err != nil {
			return err
		}
		*l = normalizeTools(values)
		return nil
	default:
		return fmt.Errorf("allowed-tools must be a string or list, got YAML kind %d", node.Kind)
	}
}

// Discover loads and validates all project skills rooted at repoRoot.
func Discover(repoRoot string) ([]Definition, error) {
	skillsDir := filepath.Join(repoRoot, filepath.FromSlash(DirName))
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("discover skills: read %q: %w", skillsDir, err)
	}

	definitions := make([]Definition, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillsDir, entry.Name(), EntryFile)
		data, err := os.ReadFile(skillPath)
		if err != nil {
			return nil, fmt.Errorf("discover skills: read %q: %w", skillPath, err)
		}
		def, err := Parse(skillPath, data)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, *def)
	}

	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].Name < definitions[j].Name
	})
	return definitions, nil
}

// Parse decodes a SKILL.md file and validates the resulting definition.
func Parse(path string, data []byte) (*Definition, error) {
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	fmText, body, hasFrontmatter, err := splitFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parse skill %q: %w", path, err)
	}

	var meta frontmatter
	if hasFrontmatter {
		if err := yaml.Unmarshal([]byte(fmText), &meta); err != nil {
			return nil, fmt.Errorf("parse skill %q: decode frontmatter: %w", path, err)
		}
	}

	dirName := filepath.Base(filepath.Dir(path))
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = dirName
	}
	def := &Definition{
		Directory:              dirName,
		File:                   path,
		Name:                   name,
		Description:            strings.TrimSpace(meta.Description),
		ArgumentHint:           strings.TrimSpace(meta.ArgumentHint),
		DisableModelInvocation: meta.DisableModelInvocation,
		UserInvocable:          meta.UserInvocable,
		AllowedTools:           normalizeTools(meta.AllowedTools),
		Body:                   strings.TrimSpace(body),
	}
	if err := Validate(*def); err != nil {
		return nil, fmt.Errorf("parse skill %q: %w", path, err)
	}
	return def, nil
}

// Render serializes a definition into a SKILL.md payload.
func Render(def Definition) (string, error) {
	if err := Validate(def); err != nil {
		return "", err
	}

	meta := frontmatter{
		Name:                   def.Name,
		Description:            def.Description,
		ArgumentHint:           def.ArgumentHint,
		DisableModelInvocation: def.DisableModelInvocation,
		UserInvocable:          def.UserInvocable,
		AllowedTools:           normalizeTools(def.AllowedTools),
	}
	yamlBytes, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("render skill %q: marshal frontmatter: %w", def.Name, err)
	}

	return fmt.Sprintf("---\n%s---\n\n%s\n", string(yamlBytes), strings.TrimSpace(def.Body)), nil
}

// Validate enforces the project contract for checked-in skill definitions.
func Validate(def Definition) error {
	if !skillNamePattern.MatchString(def.Name) {
		return fmt.Errorf("invalid skill name %q (expected lowercase letters, numbers, and hyphens; max %d chars)", def.Name, maxNameChars)
	}
	if def.Directory != "" && def.Directory != def.Name {
		return fmt.Errorf("skill directory %q must match name %q", def.Directory, def.Name)
	}
	if strings.TrimSpace(def.Description) == "" {
		return fmt.Errorf("missing description")
	}
	if !strings.Contains(def.Description, "Use when") {
		return fmt.Errorf(`description must include "Use when"`)
	}
	if strings.TrimSpace(def.Body) == "" {
		return fmt.Errorf("missing body")
	}
	return nil
}

func splitFrontmatter(content string) (frontmatterText, body string, hasFrontmatter bool, err error) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content, false, nil
	}

	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end >= 0 {
		return rest[:end], rest[end+len("\n---\n"):], true, nil
	}
	if strings.HasSuffix(rest, "\n---") {
		return strings.TrimSuffix(rest, "\n---"), "", true, nil
	}
	return "", "", false, fmt.Errorf("frontmatter opening delimiter without closing delimiter")
}

func splitTools(raw string) []string {
	return normalizeTools(strings.Fields(raw))
}

func normalizeTools(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	tools := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		tools = append(tools, trimmed)
	}
	if len(tools) == 0 {
		return nil
	}
	return tools
}
