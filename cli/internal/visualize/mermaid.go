package visualize

import (
	"fmt"
	"io"
	"strings"
)

// RenderMermaid writes the graph as a Mermaid flowchart. The output is text
// that can be pasted into a ```mermaid fenced block on GitHub/GitLab/VSCode
// or piped through the mermaid CLI.
func RenderMermaid(g *Graph, w io.Writer) error {
	var b strings.Builder

	b.WriteString("flowchart LR\n")

	// Class definitions for visual distinction.
	b.WriteString("  classDef source fill:#dbeafe,stroke:#1d4ed8,color:#0b1f4d;\n")
	b.WriteString("  classDef workflow fill:#ede9fe,stroke:#6d28d9,color:#2e1065;\n")
	b.WriteString("  classDef phase fill:#ecfdf5,stroke:#059669,color:#064e3b;\n")
	b.WriteString("  classDef gate fill:#fef3c7,stroke:#b45309,color:#451a03;\n")
	b.WriteString("  classDef missing fill:#fee2e2,stroke:#b91c1c,color:#450a0a,stroke-dasharray: 4 2;\n")
	b.WriteString("\n")

	// Source nodes.
	for _, src := range g.Sources {
		id := sourceID(src.Name)
		label := mermaidLabel(sourceHeader(src))
		fmt.Fprintf(&b, "  %s[%q]:::source\n", id, label)
	}
	b.WriteString("\n")

	// Workflow subgraphs with phase and gate nodes.
	workflowByName := map[string]*Workflow{}
	for i := range g.Workflows {
		workflowByName[g.Workflows[i].Name] = &g.Workflows[i]
	}

	for _, wf := range g.Workflows {
		writeWorkflowSubgraph(&b, &wf)
		b.WriteString("\n")
	}

	// Missing workflow placeholders.
	for _, name := range g.MissingWorkflows {
		id := workflowID(name)
		label := mermaidLabel(fmt.Sprintf("%s\n(workflow file not found)", name))
		fmt.Fprintf(&b, "  %s[%q]:::missing\n", id, label)
	}
	if len(g.MissingWorkflows) > 0 {
		b.WriteString("\n")
	}

	// Source -> workflow edges, labeled with the trigger condition.
	for _, src := range g.Sources {
		for _, trig := range src.Triggers {
			if trig.Workflow == "" {
				continue
			}
			srcNode := sourceID(src.Name)
			targetNode := workflowEntryID(trig.Workflow, workflowByName)
			label := mermaidEdgeLabel(triggerLabel(trig))
			if label == "" {
				fmt.Fprintf(&b, "  %s --> %s\n", srcNode, targetNode)
			} else {
				fmt.Fprintf(&b, "  %s -- %q --> %s\n", srcNode, label, targetNode)
			}
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// writeWorkflowSubgraph emits a Mermaid subgraph containing the workflow's
// phases, gates, and internal edges.
func writeWorkflowSubgraph(b *strings.Builder, wf *Workflow) {
	subID := workflowID(wf.Name)
	title := wf.Name
	if wf.Description != "" {
		title = fmt.Sprintf("%s: %s", wf.Name, wf.Description)
	}
	fmt.Fprintf(b, "  subgraph %s[%q]\n", subID, mermaidLabel(title))
	fmt.Fprintf(b, "    direction LR\n")

	for _, p := range wf.Phases {
		nodeID := phaseID(wf.Name, p.Name)
		label := mermaidLabel(phaseLabel(p))
		fmt.Fprintf(b, "    %s[%q]:::phase\n", nodeID, label)
		if p.Gate != nil {
			gid := gateID(wf.Name, p.Name)
			gl := mermaidLabel(gateLabel(p.Gate))
			// Mermaid's {{label}} renders a hexagon; good enough as a gate shape.
			fmt.Fprintf(b, "    %s{{%q}}:::gate\n", gid, gl)
		}
	}

	// Phase edges.
	writePhaseEdges(b, wf)

	b.WriteString("  end\n")
}

// writePhaseEdges emits edges between phases. If any phase has depends_on,
// those edges are drawn; otherwise phases are linked sequentially. Gates are
// inserted between a phase and its successor.
func writePhaseEdges(b *strings.Builder, wf *Workflow) {
	hasDeps := false
	for _, p := range wf.Phases {
		if len(p.DependsOn) > 0 {
			hasDeps = true
			break
		}
	}

	if hasDeps {
		for _, p := range wf.Phases {
			for _, dep := range p.DependsOn {
				from := phaseID(wf.Name, dep)
				to := phaseID(wf.Name, p.Name)
				fmt.Fprintf(b, "    %s --> %s\n", from, to)
			}
		}
		// Dependency graphs still want gates drawn, but there is no canonical
		// "next phase" to connect the gate to. Render the gate as a leaf from
		// the gated phase so it is visible.
		for _, p := range wf.Phases {
			if p.Gate == nil {
				continue
			}
			from := phaseID(wf.Name, p.Name)
			gid := gateID(wf.Name, p.Name)
			fmt.Fprintf(b, "    %s --> %s\n", from, gid)
		}
		return
	}

	for i := 0; i < len(wf.Phases); i++ {
		cur := wf.Phases[i]
		curID := phaseID(wf.Name, cur.Name)
		if i == len(wf.Phases)-1 {
			if cur.Gate != nil {
				fmt.Fprintf(b, "    %s --> %s\n", curID, gateID(wf.Name, cur.Name))
			}
			continue
		}
		next := wf.Phases[i+1]
		nextID := phaseID(wf.Name, next.Name)
		if cur.Gate != nil {
			gid := gateID(wf.Name, cur.Name)
			fmt.Fprintf(b, "    %s --> %s --> %s\n", curID, gid, nextID)
		} else {
			fmt.Fprintf(b, "    %s --> %s\n", curID, nextID)
		}
	}
}

// workflowEntryID returns the Mermaid node ID that an incoming edge should
// point at for a given workflow name. If the workflow is loaded and has
// phases, it's the first phase; otherwise it's the workflow ID (placeholder).
func workflowEntryID(name string, workflows map[string]*Workflow) string {
	if wf, ok := workflows[name]; ok && len(wf.Phases) > 0 {
		return phaseID(name, wf.Phases[0].Name)
	}
	return workflowID(name)
}

func sourceHeader(s Source) string {
	var parts []string
	parts = append(parts, s.Name)
	if s.Type != "" {
		parts = append(parts, "("+s.Type+")")
	}
	if s.Repo != "" {
		parts = append(parts, s.Repo)
	}
	return strings.Join(parts, "\n")
}

func phaseLabel(p Phase) string {
	var lines []string
	lines = append(lines, p.Name)
	typ := p.Type
	if typ == "" {
		typ = "prompt"
	}
	lines = append(lines, "("+typ+")")
	if p.LLM != "" {
		model := p.LLM
		if p.Model != "" {
			model += "/" + p.Model
		}
		lines = append(lines, model)
	}
	if p.NoOp {
		lines = append(lines, "noop-capable")
	}
	return strings.Join(lines, "\n")
}

func gateLabel(g *Gate) string {
	var lines []string
	lines = append(lines, "gate: "+g.Type)
	switch g.Type {
	case "command":
		if g.Run != "" {
			lines = append(lines, g.Run)
		}
		if g.Retries > 0 {
			lines = append(lines, fmt.Sprintf("retries=%d", g.Retries))
		}
	case "label":
		if g.WaitFor != "" {
			lines = append(lines, "wait_for: "+g.WaitFor)
		}
	}
	return strings.Join(lines, "\n")
}

func triggerLabel(t Trigger) string {
	var parts []string
	if len(t.Labels) > 0 {
		parts = append(parts, "labels: "+strings.Join(t.Labels, ", "))
	}
	var events []string
	if t.OnReview {
		events = append(events, "review_submitted")
	}
	if t.OnChecks {
		events = append(events, "checks_failed")
	}
	if t.OnComment {
		events = append(events, "commented")
	}
	if len(events) > 0 {
		parts = append(parts, "on: "+strings.Join(events, ", "))
	}
	return strings.Join(parts, " | ")
}

// mermaidLabel converts an internal multi-line label into Mermaid's quoted
// label form. Newlines become <br/>, and embedded double quotes are escaped
// using Mermaid's #quot; entity so fmt's %q quoting stays valid.
func mermaidLabel(s string) string {
	s = strings.ReplaceAll(s, "\"", "#quot;")
	return strings.ReplaceAll(s, "\n", "<br/>")
}

// mermaidEdgeLabel is like mermaidLabel but collapses newlines to a space so
// edge labels stay on one line.
func mermaidEdgeLabel(s string) string {
	s = strings.ReplaceAll(s, "\"", "#quot;")
	return strings.ReplaceAll(s, "\n", " ")
}

// sanitizeID turns an arbitrary name into a valid Mermaid node id
// (alphanumeric + underscore). Empty input becomes "_".
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

func sourceID(name string) string   { return "src_" + sanitizeID(name) }
func workflowID(name string) string { return "wf_" + sanitizeID(name) }
func phaseID(workflow, phase string) string {
	return "wf_" + sanitizeID(workflow) + "__" + sanitizeID(phase)
}
func gateID(workflow, phase string) string {
	return phaseID(workflow, phase) + "_gate"
}
