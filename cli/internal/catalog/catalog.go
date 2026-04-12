package catalog

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// PermissionScope represents the level of autonomy granted to a tool.
type PermissionScope int

const (
	ScopeReadOnly          PermissionScope = 1
	ScopeWriteWithApproval PermissionScope = 2
	ScopeFullAutonomy      PermissionScope = 3
)

// ParamType enumerates the supported parameter types.
type ParamType string

const (
	ParamString ParamType = "string"
	ParamInt    ParamType = "int"
	ParamBool   ParamType = "bool"
	ParamArray  ParamType = "array"
	ParamObject ParamType = "object"
)

// Param describes a single parameter accepted by a tool.
type Param struct {
	Name        string
	Type        ParamType
	Description string
	Required    bool
	Default     any
}

// Tool represents a registered tool in the catalog.
type Tool struct {
	Name            string
	Description     string
	Parameters      []Param
	ReturnFormat    string
	ErrorConditions []string
	Scope           PermissionScope
	Tags            []string
}

// Overlap describes a detected functional overlap between two tools.
type Overlap struct {
	ToolA      string
	ToolB      string
	Reason     string
	Similarity float64
}

// ToolMetrics tracks usage statistics for a tool.
type ToolMetrics struct {
	Calls          int
	Failures       int
	TotalLatency   time.Duration
	TotalTokenCost float64
	LastUsed       time.Time
}

// RolePermissions defines the maximum permission scope and allowed tools for a role.
type RolePermissions struct {
	Role         string
	MaxScope     PermissionScope
	AllowedTools []string
}

const (
	RoleDiagnostic   = "diagnostic"
	RoleDelivery     = "delivery"
	RoleHousekeeping = "housekeeping"
)

// Catalog is a centralised registry of tools, metrics, and role permissions.
type Catalog struct {
	mu      sync.RWMutex
	tools   map[string]*Tool
	metrics map[string]*ToolMetrics
	roles   map[string]*RolePermissions
}

// NewCatalog returns an empty Catalog ready for use.
func NewCatalog() *Catalog {
	return &Catalog{
		tools:   make(map[string]*Tool),
		metrics: make(map[string]*ToolMetrics),
		roles:   make(map[string]*RolePermissions),
	}
}

// NewDefaultPhaseCatalog returns a catalog preloaded with the common prompt tools
// xylem exposes to provider CLIs plus the default phase-role permissions used by
// runner prompt phases.
func NewDefaultPhaseCatalog() (*Catalog, error) {
	c := NewCatalog()
	for _, tool := range defaultPhaseTools() {
		if err := c.Register(tool); err != nil {
			return nil, fmt.Errorf("register default tool %q: %w", tool.Name, err)
		}
	}
	for _, rp := range defaultPhaseRolePermissions() {
		if err := c.SetRolePermissions(rp); err != nil {
			return nil, fmt.Errorf("set default role permissions %q: %w", rp.Role, err)
		}
	}
	return c, nil
}

// ValidateTool checks that a tool's required fields are populated and its
// parameter types and scope are valid.
func ValidateTool(tool Tool) error {
	if strings.TrimSpace(tool.Name) == "" {
		return fmt.Errorf("validate tool: name is required")
	}
	if strings.TrimSpace(tool.Description) == "" {
		return fmt.Errorf("validate tool: description is required")
	}
	if tool.Scope < ScopeReadOnly || tool.Scope > ScopeFullAutonomy {
		return fmt.Errorf("validate tool: invalid scope %d", tool.Scope)
	}
	seen := make(map[string]struct{}, len(tool.Parameters))
	for _, p := range tool.Parameters {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("validate tool: parameter name is required")
		}
		switch p.Type {
		case ParamString, ParamInt, ParamBool, ParamArray, ParamObject:
		default:
			return fmt.Errorf("validate tool: invalid parameter type %q", p.Type)
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("validate tool: duplicate parameter name: %q", p.Name)
		}
		seen[p.Name] = struct{}{}
	}
	return nil
}

// Register adds a tool to the catalog. It returns an error if a tool with the
// same name already exists or the tool fails validation.
func (c *Catalog) Register(tool Tool) error {
	if err := ValidateTool(tool); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.tools[tool.Name]; exists {
		return fmt.Errorf("register: tool %q already exists", tool.Name)
	}
	t := copyTool(tool)
	c.tools[tool.Name] = &t
	c.metrics[tool.Name] = &ToolMetrics{}
	return nil
}

// Get returns a copy of the tool with the given name, or an error if it does
// not exist.
func (c *Catalog) Get(name string) (*Tool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tools[name]
	if !ok {
		return nil, fmt.Errorf("get: tool %q not found", name)
	}
	cpy := copyTool(*t)
	return &cpy, nil
}

// List returns all registered tools in no guaranteed order.
func (c *Catalog) List() []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Tool, 0, len(c.tools))
	for _, t := range c.tools {
		out = append(out, copyTool(*t))
	}
	return out
}

// ListByTag returns tools that have the given tag.
func (c *Catalog) ListByTag(tag string) []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []Tool
	for _, t := range c.tools {
		for _, tg := range t.Tags {
			if tg == tag {
				out = append(out, copyTool(*t))
				break
			}
		}
	}
	return out
}

// Remove deletes a tool and its metrics from the catalog.
func (c *Catalog) Remove(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.tools[name]; !ok {
		return fmt.Errorf("remove: tool %q not found", name)
	}
	delete(c.tools, name)
	delete(c.metrics, name)
	return nil
}

// ComputeSimilarity returns a score in [0,1] based on shared tags and keyword
// overlap in descriptions between two tools.
func ComputeSimilarity(a, b Tool) float64 {
	tagScore := jaccardStrings(a.Tags, b.Tags)
	aWords := tokenize(a.Description)
	bWords := tokenize(b.Description)
	descScore := jaccardStrings(aWords, bWords)
	return 0.5*tagScore + 0.5*descScore
}

// DetectOverlaps returns pairs of tools whose similarity exceeds 0.3.
func (c *Catalog) DetectOverlaps() []Overlap {
	c.mu.RLock()
	defer c.mu.RUnlock()
	names := make([]string, 0, len(c.tools))
	for n := range c.tools {
		names = append(names, n)
	}
	var overlaps []Overlap
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			a := c.tools[names[i]]
			b := c.tools[names[j]]
			sim := ComputeSimilarity(*a, *b)
			if sim > 0.3 {
				overlaps = append(overlaps, Overlap{
					ToolA:      a.Name,
					ToolB:      b.Name,
					Reason:     "shared tags or description keywords",
					Similarity: sim,
				})
			}
		}
	}
	return overlaps
}

// SetRolePermissions registers or updates permissions for a role.
func (c *Catalog) SetRolePermissions(rp RolePermissions) error {
	if strings.TrimSpace(rp.Role) == "" {
		return fmt.Errorf("set role permissions: role name is required")
	}
	if rp.MaxScope < ScopeReadOnly || rp.MaxScope > ScopeFullAutonomy {
		return fmt.Errorf("set role permissions: invalid scope %d", rp.MaxScope)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r := rp
	c.roles[rp.Role] = &r
	return nil
}

// GetRolePermissions returns a copy of the permissions for the named role.
func (c *Catalog) GetRolePermissions(role string) (*RolePermissions, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rp, ok := c.roles[role]
	if !ok {
		return nil, fmt.Errorf("get role permissions: role %q not found", role)
	}
	cpy := *rp
	cpy.AllowedTools = append([]string(nil), rp.AllowedTools...)
	return &cpy, nil
}

// Authorize checks whether the given role is allowed to invoke the named tool.
// It verifies both that the tool appears in the role's allowed list and that the
// tool's scope does not exceed the role's maximum scope.
func (c *Catalog) Authorize(agentRole, toolName string) (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	tool, ok := c.tools[toolName]
	if !ok {
		return false, fmt.Errorf("authorize: tool %q not found", toolName)
	}
	rp, ok := c.roles[agentRole]
	if !ok {
		return false, fmt.Errorf("authorize: role %q not found", agentRole)
	}
	if tool.Scope > rp.MaxScope {
		return false, nil
	}
	for _, allowed := range rp.AllowedTools {
		if allowed == toolName {
			return true, nil
		}
	}
	return false, nil
}

// AllowedToolsForRole returns the configured tool list for a role after
// validating that every listed tool exists in the catalog.
func (c *Catalog) AllowedToolsForRole(role string) ([]string, error) {
	rp, err := c.GetRolePermissions(role)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rp.AllowedTools))
	for _, toolName := range rp.AllowedTools {
		if _, err := c.Get(toolName); err != nil {
			return nil, fmt.Errorf("allowed tools for role %q: %w", role, err)
		}
		out = append(out, toolName)
	}
	return out, nil
}

// ResolveRoleTools validates an explicit requested tool list against the named
// role. When requested is empty, it derives the role's full allowed set.
func (c *Catalog) ResolveRoleTools(role string, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return c.AllowedToolsForRole(role)
	}
	out := make([]string, 0, len(requested))
	for _, toolName := range requested {
		allowed, err := c.Authorize(role, toolName)
		if err != nil {
			return nil, fmt.Errorf("resolve role tools: %w", err)
		}
		if !allowed {
			return nil, fmt.Errorf("resolve role tools: role %q is not allowed to use tool %q", role, toolName)
		}
		out = append(out, toolName)
	}
	return out, nil
}

// RecordUsage records a single invocation of the named tool.
func (c *Catalog) RecordUsage(toolName string, success bool, latency time.Duration, tokenCost float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.metrics[toolName]
	if !ok {
		return fmt.Errorf("record usage: tool %q not found", toolName)
	}
	m.Calls++
	if !success {
		m.Failures++
	}
	m.TotalLatency += latency
	m.TotalTokenCost += tokenCost
	m.LastUsed = time.Now()
	return nil
}

// GetMetrics returns usage metrics for the named tool.
func (c *Catalog) GetMetrics(toolName string) (*ToolMetrics, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.metrics[toolName]
	if !ok {
		return nil, fmt.Errorf("get metrics: tool %q not found", toolName)
	}
	cpy := *m
	return &cpy, nil
}

// AllMetrics returns a copy of all per-tool metrics.
func (c *Catalog) AllMetrics() map[string]ToolMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]ToolMetrics, len(c.metrics))
	for k, v := range c.metrics {
		out[k] = *v
	}
	return out
}

// FailureRate returns the fraction of calls that failed for the named tool.
// It returns 0 when no calls have been recorded.
func (c *Catalog) FailureRate(toolName string) (float64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.metrics[toolName]
	if !ok {
		return 0, fmt.Errorf("failure rate: tool %q not found", toolName)
	}
	if m.Calls == 0 {
		return 0, nil
	}
	return float64(m.Failures) / float64(m.Calls), nil
}

// AvgLatency returns the mean latency per call for the named tool.
// It returns 0 when no calls have been recorded.
func (c *Catalog) AvgLatency(toolName string) (time.Duration, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.metrics[toolName]
	if !ok {
		return 0, fmt.Errorf("avg latency: tool %q not found", toolName)
	}
	if m.Calls == 0 {
		return 0, nil
	}
	return m.TotalLatency / time.Duration(m.Calls), nil
}

// AvgTokenCost returns the mean token cost per call for the named tool.
// It returns 0 when no calls have been recorded.
func (c *Catalog) AvgTokenCost(toolName string) (float64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.metrics[toolName]
	if !ok {
		return 0, fmt.Errorf("avg token cost: tool %q not found", toolName)
	}
	if m.Calls == 0 {
		return 0, nil
	}
	return m.TotalTokenCost / float64(m.Calls), nil
}

// copyTool returns a deep copy of t, including its Parameters and Tags slices.
func copyTool(t Tool) Tool {
	t.Parameters = append([]Param(nil), t.Parameters...)
	t.Tags = append([]string(nil), t.Tags...)
	return t
}

// tokenize splits a string into lower-case words.
func tokenize(s string) []string {
	words := strings.Fields(strings.ToLower(s))
	out := make([]string, 0, len(words))
	for _, w := range words {
		trimmed := strings.Trim(w, ".,;:!?()[]{}\"'")
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// jaccardStrings computes the Jaccard index of two string slices.
func jaccardStrings(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, s := range a {
		setA[s] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, s := range b {
		setB[s] = struct{}{}
	}
	var intersection int
	for s := range setA {
		if _, ok := setB[s]; ok {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return math.Round(float64(intersection)/float64(union)*1000) / 1000
}

func defaultPhaseTools() []Tool {
	return []Tool{
		{Name: "Bash", Description: "Run shell commands in the repository worktree", Scope: ScopeFullAutonomy, Tags: []string{"shell", "command"}},
		{Name: "Edit", Description: "Modify an existing file", Scope: ScopeWriteWithApproval, Tags: []string{"file", "write"}},
		{Name: "Glob", Description: "Find files by glob pattern", Scope: ScopeReadOnly, Tags: []string{"file", "search"}},
		{Name: "Grep", Description: "Search file contents by pattern", Scope: ScopeReadOnly, Tags: []string{"file", "search"}},
		{Name: "LS", Description: "List files and directories", Scope: ScopeReadOnly, Tags: []string{"file", "read"}},
		{Name: "MultiEdit", Description: "Apply multiple edits to an existing file", Scope: ScopeWriteWithApproval, Tags: []string{"file", "write"}},
		{Name: "Read", Description: "Read file contents", Scope: ScopeReadOnly, Tags: []string{"file", "read"}},
		{Name: "Task", Description: "Delegate work to a sub-agent", Scope: ScopeFullAutonomy, Tags: []string{"agent", "orchestration"}},
		{Name: "WebFetch", Description: "Fetch content from the web", Scope: ScopeReadOnly, Tags: []string{"web", "read"}},
		{Name: "WebSearch", Description: "Search the web for current information", Scope: ScopeReadOnly, Tags: []string{"web", "search"}},
		{Name: "Write", Description: "Create or replace a file", Scope: ScopeWriteWithApproval, Tags: []string{"file", "write"}},
	}
}

func defaultPhaseRolePermissions() []RolePermissions {
	diagnosticTools := []string{"Bash", "Glob", "Grep", "LS", "Read", "WebFetch", "WebSearch"}
	return []RolePermissions{
		{
			Role:         RoleDiagnostic,
			MaxScope:     ScopeFullAutonomy,
			AllowedTools: append([]string(nil), diagnosticTools...),
		},
		{
			Role:     RoleDelivery,
			MaxScope: ScopeFullAutonomy,
			AllowedTools: append(append([]string(nil), diagnosticTools...),
				"Edit", "MultiEdit", "Write"),
		},
		{
			Role:         RoleHousekeeping,
			MaxScope:     ScopeFullAutonomy,
			AllowedTools: append(append([]string(nil), diagnosticTools...), "Write"),
		},
	}
}
