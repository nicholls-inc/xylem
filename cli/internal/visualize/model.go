// Package visualize builds an intermediate graph representation of a xylem
// project's triggers and workflows, then renders it to various formats.
//
// The graph is a normalized, renderer-agnostic view of the two configuration
// layers:
//
//   - .xylem.yml declares sources (triggers) and maps labels / PR events to
//     tasks, which reference workflows by name.
//   - .xylem/workflows/<name>.yaml declares phases with optional gates and
//     depends_on edges.
//
// Build() stitches these together into a Graph. Renderers (RenderMermaid,
// RenderDOT, RenderJSON) turn the Graph into a specific output format.
package visualize

// Graph is the intermediate representation that renderers consume.
type Graph struct {
	Sources   []Source   `json:"sources"`
	Workflows []Workflow `json:"workflows"`
	// MissingWorkflows lists workflow names referenced by a task whose YAML
	// file could not be found on disk. The graph is still rendered, but these
	// workflows appear as empty placeholders so the user sees the gap.
	MissingWorkflows []string `json:"missing_workflows,omitempty"`
	// OrphanedWorkflows lists workflow YAML files present under the workflows
	// directory that are not referenced by any task. They're collected by
	// filename stem only (no parsing) so detection works even when a non-root
	// cwd would cause prompt-file validation to fail.
	OrphanedWorkflows []string `json:"orphaned_workflows,omitempty"`
	// TriggersPerWorkflow is a reverse index: workflow name → the (source,
	// task) entries that fire it. Keys include both loaded and missing
	// workflows so the index still surfaces which triggers point at a gap.
	// Values are sorted by (Source, Task) for deterministic output.
	TriggersPerWorkflow map[string][]TriggerRef `json:"triggers_per_workflow,omitempty"`
}

// TriggerRef names a (source, task) pair that fires a specific workflow.
// It's a flattened view used by the reverse index on Graph and by the
// state-machine renderer.
type TriggerRef struct {
	Source       string            `json:"source"`
	Task         string            `json:"task"`
	SourceType   string            `json:"source_type,omitempty"`
	Repo         string            `json:"repo,omitempty"`
	Labels       []string          `json:"labels,omitempty"`
	Events       []string          `json:"events,omitempty"` // flattened OnReview/OnChecks/OnComment
	AuthorAllow  []string          `json:"author_allow,omitempty"`
	AuthorDeny   []string          `json:"author_deny,omitempty"`
	StatusLabels map[string]string `json:"status_labels,omitempty"`
}

// Source corresponds to one entry under .xylem.yml#sources.
type Source struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Repo     string    `json:"repo,omitempty"`
	LLM      string    `json:"llm,omitempty"`
	Model    string    `json:"model,omitempty"`
	Exclude  []string  `json:"exclude,omitempty"`
	Triggers []Trigger `json:"triggers"`
}

// Trigger describes one task within a source: the condition that fires it
// and the workflow it runs.
type Trigger struct {
	TaskName    string   `json:"task_name"`
	Workflow    string   `json:"workflow"`
	Labels      []string `json:"labels,omitempty"`
	OnReview    bool     `json:"on_review_submitted,omitempty"`
	OnChecks    bool     `json:"on_checks_failed,omitempty"`
	OnComment   bool     `json:"on_commented,omitempty"`
	AuthorAllow []string `json:"author_allow,omitempty"`
	AuthorDeny  []string `json:"author_deny,omitempty"`
	// StatusLabels is the per-task status_labels map flattened into a
	// plain map (e.g. {"running": "in-progress", "failed": "xylem-failed"}).
	// It's lifted here so the state-machine renderer can walk triggers
	// without also threading config.Config through every helper.
	StatusLabels map[string]string `json:"status_labels,omitempty"`
}

// Workflow is a flattened view of workflow.Workflow.
type Workflow struct {
	Name        string  `json:"name"`
	Class       string  `json:"class,omitempty"`
	Description string  `json:"description,omitempty"`
	LLM         string  `json:"llm,omitempty"`
	Model       string  `json:"model,omitempty"`
	Phases      []Phase `json:"phases"`
}

// Phase is a flattened view of workflow.Phase with *string fields
// dereferenced and the gate summarized.
type Phase struct {
	Name      string   `json:"name"`
	Type      string   `json:"type,omitempty"` // "prompt" | "command"
	LLM       string   `json:"llm,omitempty"`
	Model     string   `json:"model,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
	NoOp      bool     `json:"noop,omitempty"`
	Gate      *Gate    `json:"gate,omitempty"`
}

// Gate is a flattened view of workflow.Gate.
type Gate struct {
	Type    string `json:"type"` // "command" | "label"
	Run     string `json:"run,omitempty"`
	WaitFor string `json:"wait_for,omitempty"`
	Retries int    `json:"retries,omitempty"`
}
