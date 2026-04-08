package visualize

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

// Build converts a loaded xylem Config and a workflows directory into a
// Graph. Workflows referenced by a task but missing from disk are collected
// into Graph.MissingWorkflows rather than failing the build; any other
// workflow-load error is returned.
//
// Sources and workflows in the returned graph are sorted by name to make
// output deterministic.
func Build(cfg *config.Config, workflowsDir string) (*Graph, error) {
	if cfg == nil {
		return nil, fmt.Errorf("visualize: nil config")
	}

	g := &Graph{}

	sourceNames := make([]string, 0, len(cfg.Sources))
	for name := range cfg.Sources {
		sourceNames = append(sourceNames, name)
	}
	sort.Strings(sourceNames)

	// Track unique workflow names so each workflow file is only loaded once.
	workflowSet := map[string]struct{}{}

	for _, name := range sourceNames {
		src := cfg.Sources[name]
		node := Source{
			Name:    name,
			Type:    src.Type,
			Repo:    src.Repo,
			LLM:     src.LLM,
			Model:   src.Model,
			Exclude: append([]string(nil), src.Exclude...),
		}

		taskNames := make([]string, 0, len(src.Tasks))
		for tname := range src.Tasks {
			taskNames = append(taskNames, tname)
		}
		sort.Strings(taskNames)

		for _, tname := range taskNames {
			task := src.Tasks[tname]
			trig := Trigger{
				TaskName: tname,
				Workflow: task.Workflow,
				Labels:   append([]string(nil), task.Labels...),
			}
			if task.On != nil {
				trig.OnReview = task.On.ReviewSubmitted
				trig.OnChecks = task.On.ChecksFailed
				trig.OnComment = task.On.Commented
				trig.AuthorAllow = append([]string(nil), task.On.AuthorAllow...)
				trig.AuthorDeny = append([]string(nil), task.On.AuthorDeny...)
				// For PR-event sources, labels may live under task.On.
				if len(trig.Labels) == 0 && len(task.On.Labels) > 0 {
					trig.Labels = append([]string(nil), task.On.Labels...)
				}
			}
			node.Triggers = append(node.Triggers, trig)
			if task.Workflow != "" {
				workflowSet[task.Workflow] = struct{}{}
			}
		}
		g.Sources = append(g.Sources, node)
	}

	workflowNames := make([]string, 0, len(workflowSet))
	for name := range workflowSet {
		workflowNames = append(workflowNames, name)
	}
	sort.Strings(workflowNames)

	for _, name := range workflowNames {
		path := filepath.Join(workflowsDir, name+".yaml")
		wf, err := workflow.Load(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				g.MissingWorkflows = append(g.MissingWorkflows, name)
				continue
			}
			return nil, fmt.Errorf("load workflow %q: %w", name, err)
		}
		g.Workflows = append(g.Workflows, convertWorkflow(wf))
	}

	return g, nil
}

func convertWorkflow(w *workflow.Workflow) Workflow {
	out := Workflow{
		Name:        w.Name,
		Description: w.Description,
		LLM:         derefString(w.LLM),
		Model:       derefString(w.Model),
	}
	for _, p := range w.Phases {
		out.Phases = append(out.Phases, convertPhase(p))
	}
	return out
}

func convertPhase(p workflow.Phase) Phase {
	out := Phase{
		Name:      p.Name,
		Type:      p.Type,
		LLM:       derefString(p.LLM),
		Model:     derefString(p.Model),
		DependsOn: append([]string(nil), p.DependsOn...),
		NoOp:      p.NoOp != nil,
	}
	if p.Gate != nil {
		out.Gate = &Gate{
			Type:    p.Gate.Type,
			Run:     truncate(p.Gate.Run, 60),
			WaitFor: p.Gate.WaitFor,
			Retries: p.Gate.Retries,
		}
	}
	return out
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
