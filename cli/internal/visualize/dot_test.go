package visualize

import (
	"strings"
	"testing"
)

func TestRenderDOT(t *testing.T) {
	g := fixtureGraph()
	var sb strings.Builder
	if err := RenderDOT(g, &sb); err != nil {
		t.Fatalf("RenderDOT: %v", err)
	}
	out := sb.String()

	mustContain := []string{
		"digraph xylem {",
		"rankdir=LR;",
		"src_bugs [",
		"src_pr_lifecycle [",
		"subgraph cluster_fix_bug {",
		"wf_fix_bug__analyze [",
		"wf_fix_bug__fix [",
		"wf_fix_bug__fix_gate [shape=diamond",
		"subgraph cluster_respond_to_pr_review {",
		// Trigger edge with labels.
		"src_bugs -> wf_fix_bug__analyze",
		"labels: bug, ready",
		"on: review_submitted",
		// Missing workflow placeholder.
		"wf_ghost [",
		"workflow file not found",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("dot output missing %q\n---\n%s", want, out)
		}
	}

	// Sequential edge chain.
	if !strings.Contains(out, "wf_fix_bug__analyze -> wf_fix_bug__fix") {
		t.Errorf("expected sequential analyze -> fix edge:\n%s", out)
	}
	if !strings.Contains(out, "wf_fix_bug__fix -> wf_fix_bug__fix_gate") {
		t.Errorf("expected fix -> gate edge:\n%s", out)
	}
}

func TestRenderDOT_EscapesLabel(t *testing.T) {
	// Verify that double quotes in strings are escaped.
	g := &Graph{
		Sources: []Source{
			{
				Name: "s",
				Type: "github",
				Triggers: []Trigger{
					{TaskName: "t", Workflow: "wf", Labels: []string{"quote\"label"}},
				},
			},
		},
		Workflows: []Workflow{
			{Name: "wf", Phases: []Phase{{Name: "only"}}},
		},
	}
	var sb strings.Builder
	if err := RenderDOT(g, &sb); err != nil {
		t.Fatalf("RenderDOT: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, `quote\"label`) {
		t.Errorf("expected escaped quote in output:\n%s", out)
	}
}

func TestDotLabel(t *testing.T) {
	if got := dotLabel("line1\nline2"); got != `line1\nline2` {
		t.Errorf("dotLabel newline escape: %q", got)
	}
	if got := dotLabel(`he said "hi"`); got != `he said \"hi\"` {
		t.Errorf("dotLabel quote escape: %q", got)
	}
}
