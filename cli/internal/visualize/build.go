package visualize

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
				TaskName:     tname,
				Workflow:     task.Workflow,
				Labels:       append([]string(nil), task.Labels...),
				StatusLabels: statusLabelsMap(task.StatusLabels),
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

	// Reverse index: workflow name → (source, task) refs that fire it.
	// Walked after source construction so the values see the fully-built
	// Trigger nodes (including lifted status_labels).
	g.TriggersPerWorkflow = buildReverseIndex(g.Sources)

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

	// Orphan detection: any *.yaml under workflowsDir whose stem isn't in
	// workflowSet is a workflow file nobody references. We only collect the
	// stem here — no workflow.Load — so detection works from any cwd (the
	// loader's prompt_file stat would otherwise fail for orphans whose
	// prompts are relative to the repo root).
	orphans, err := findOrphanWorkflows(workflowsDir, workflowSet)
	if err != nil {
		return nil, fmt.Errorf("scan orphan workflows: %w", err)
	}
	g.OrphanedWorkflows = orphans

	return g, nil
}

// buildReverseIndex walks every source + trigger and groups them by the
// workflow they target. Keys include workflow names that reference missing
// workflow files so the reverse index still surfaces "which triggers point
// at the gap". Values are sorted by (Source, Task) for stable output.
func buildReverseIndex(sources []Source) map[string][]TriggerRef {
	if len(sources) == 0 {
		return nil
	}
	out := map[string][]TriggerRef{}
	for _, src := range sources {
		for _, trig := range src.Triggers {
			if trig.Workflow == "" {
				continue
			}
			ref := TriggerRef{
				Source:       src.Name,
				Task:         trig.TaskName,
				SourceType:   src.Type,
				Repo:         src.Repo,
				Labels:       append([]string(nil), trig.Labels...),
				Events:       flattenEvents(trig),
				AuthorAllow:  append([]string(nil), trig.AuthorAllow...),
				AuthorDeny:   append([]string(nil), trig.AuthorDeny...),
				StatusLabels: copyStringMap(trig.StatusLabels),
			}
			out[trig.Workflow] = append(out[trig.Workflow], ref)
		}
	}
	if len(out) == 0 {
		return nil
	}
	for wf := range out {
		refs := out[wf]
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].Source != refs[j].Source {
				return refs[i].Source < refs[j].Source
			}
			return refs[i].Task < refs[j].Task
		})
		out[wf] = refs
	}
	return out
}

// flattenEvents turns the three boolean PR event flags into a sorted slice
// of lowercase event names, so downstream audit and rendering code doesn't
// need to care about which bool maps to which name.
func flattenEvents(t Trigger) []string {
	var ev []string
	if t.OnReview {
		ev = append(ev, "review_submitted")
	}
	if t.OnChecks {
		ev = append(ev, "checks_failed")
	}
	if t.OnComment {
		ev = append(ev, "commented")
	}
	return ev
}

// statusLabelsMap flattens a *config.StatusLabels into a plain map with
// only the populated keys, so the reverse index and state-machine renderer
// can iterate it without a nil check on the pointer.
func statusLabelsMap(sl *config.StatusLabels) map[string]string {
	if sl == nil {
		return nil
	}
	out := map[string]string{}
	if sl.Queued != "" {
		out["queued"] = sl.Queued
	}
	if sl.Running != "" {
		out["running"] = sl.Running
	}
	if sl.Completed != "" {
		out["completed"] = sl.Completed
	}
	if sl.Failed != "" {
		out["failed"] = sl.Failed
	}
	if sl.TimedOut != "" {
		out["timed_out"] = sl.TimedOut
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func copyStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// findOrphanWorkflows returns the alphabetically sorted list of workflow
// YAML filename stems under workflowsDir that aren't in referenced. A
// missing or unreadable directory is not an error — the structural graph
// simply has no orphans to report.
func findOrphanWorkflows(workflowsDir string, referenced map[string]struct{}) ([]string, error) {
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var orphans []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".yml"), ".yaml")
		if _, ok := referenced[stem]; ok {
			continue
		}
		orphans = append(orphans, stem)
	}
	if len(orphans) == 0 {
		return nil, nil
	}
	sort.Strings(orphans)
	return orphans, nil
}

func convertWorkflow(w *workflow.Workflow) Workflow {
	out := Workflow{
		Name:        w.Name,
		Class:       string(w.Class),
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
