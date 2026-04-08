package visualize

import (
	"fmt"
	"io"
	"strings"
)

// RenderDOT writes the graph as a Graphviz digraph. The output can be piped
// through `dot -Tpng` (or any Graphviz frontend) to produce an image.
func RenderDOT(g *Graph, w io.Writer) error {
	var b strings.Builder

	b.WriteString("digraph xylem {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [fontname=\"Helvetica\"];\n")
	b.WriteString("  edge [fontname=\"Helvetica\", fontsize=10];\n\n")

	// Sources.
	b.WriteString("  // sources\n")
	for _, src := range g.Sources {
		id := sourceID(src.Name)
		label := dotLabel(sourceHeader(src))
		fmt.Fprintf(&b, "  %s [shape=box, style=\"filled,rounded\", fillcolor=\"#dbeafe\", color=\"#1d4ed8\", label=\"%s\"];\n", id, label)
	}
	b.WriteString("\n")

	// Workflows as clusters.
	workflowByName := map[string]*Workflow{}
	for i := range g.Workflows {
		workflowByName[g.Workflows[i].Name] = &g.Workflows[i]
	}

	for _, wf := range g.Workflows {
		writeDOTCluster(&b, &wf)
		b.WriteString("\n")
	}

	// Missing workflow placeholders.
	for _, name := range g.MissingWorkflows {
		id := workflowID(name)
		label := dotLabel(fmt.Sprintf("%s\n(workflow file not found)", name))
		fmt.Fprintf(&b, "  %s [shape=box, style=\"filled,rounded,dashed\", fillcolor=\"#fee2e2\", color=\"#b91c1c\", label=\"%s\"];\n", id, label)
	}
	if len(g.MissingWorkflows) > 0 {
		b.WriteString("\n")
	}

	// Source -> workflow edges.
	b.WriteString("  // triggers\n")
	for _, src := range g.Sources {
		for _, trig := range src.Triggers {
			if trig.Workflow == "" {
				continue
			}
			from := sourceID(src.Name)
			to := workflowEntryID(trig.Workflow, workflowByName)
			label := dotEdgeLabel(triggerLabel(trig))
			if label == "" {
				fmt.Fprintf(&b, "  %s -> %s;\n", from, to)
			} else {
				fmt.Fprintf(&b, "  %s -> %s [label=\"%s\"];\n", from, to, label)
			}
		}
	}

	b.WriteString("}\n")

	_, err := io.WriteString(w, b.String())
	return err
}

func writeDOTCluster(b *strings.Builder, wf *Workflow) {
	fmt.Fprintf(b, "  subgraph cluster_%s {\n", sanitizeID(wf.Name))
	title := wf.Name
	if wf.Description != "" {
		title = fmt.Sprintf("%s: %s", wf.Name, wf.Description)
	}
	fmt.Fprintf(b, "    label=\"%s\";\n", dotLabel(title))
	b.WriteString("    style=\"filled,rounded\";\n")
	b.WriteString("    color=\"#6d28d9\";\n")
	b.WriteString("    fillcolor=\"#ede9fe\";\n")

	for _, p := range wf.Phases {
		nodeID := phaseID(wf.Name, p.Name)
		label := dotLabel(phaseLabel(p))
		fmt.Fprintf(b, "    %s [shape=box, style=\"filled,rounded\", fillcolor=\"#ecfdf5\", color=\"#059669\", label=\"%s\"];\n", nodeID, label)
		if p.Gate != nil {
			gid := gateID(wf.Name, p.Name)
			gl := dotLabel(gateLabel(p.Gate))
			fmt.Fprintf(b, "    %s [shape=diamond, style=\"filled\", fillcolor=\"#fef3c7\", color=\"#b45309\", label=\"%s\"];\n", gid, gl)
		}
	}

	writeDOTPhaseEdges(b, wf)

	b.WriteString("  }\n")
}

func writeDOTPhaseEdges(b *strings.Builder, wf *Workflow) {
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
				fmt.Fprintf(b, "    %s -> %s;\n", from, to)
			}
		}
		for _, p := range wf.Phases {
			if p.Gate == nil {
				continue
			}
			from := phaseID(wf.Name, p.Name)
			fmt.Fprintf(b, "    %s -> %s;\n", from, gateID(wf.Name, p.Name))
		}
		return
	}

	for i := 0; i < len(wf.Phases); i++ {
		cur := wf.Phases[i]
		curID := phaseID(wf.Name, cur.Name)
		if i == len(wf.Phases)-1 {
			if cur.Gate != nil {
				fmt.Fprintf(b, "    %s -> %s;\n", curID, gateID(wf.Name, cur.Name))
			}
			continue
		}
		next := wf.Phases[i+1]
		nextID := phaseID(wf.Name, next.Name)
		if cur.Gate != nil {
			gid := gateID(wf.Name, cur.Name)
			fmt.Fprintf(b, "    %s -> %s -> %s;\n", curID, gid, nextID)
		} else {
			fmt.Fprintf(b, "    %s -> %s;\n", curID, nextID)
		}
	}
}

// dotLabel escapes a multi-line label for a DOT string. DOT uses \n for
// line breaks inside a label, and " must be escaped.
func dotLabel(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return strings.ReplaceAll(s, "\n", "\\n")
}

// dotEdgeLabel collapses newlines into a space so edge labels stay single-line.
func dotEdgeLabel(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return strings.ReplaceAll(s, "\n", " ")
}
