package visualize

import (
	"strings"
	"testing"
)

func fixtureGraph() *Graph {
	return &Graph{
		Sources: []Source{
			{
				Name: "bugs",
				Type: "github",
				Repo: "owner/repo",
				Triggers: []Trigger{
					{
						TaskName: "fix-bugs",
						Workflow: "fix-bug",
						Labels:   []string{"bug", "ready"},
					},
				},
			},
			{
				Name: "pr-lifecycle",
				Type: "github-pr-events",
				Repo: "owner/repo",
				Triggers: []Trigger{
					{
						TaskName: "respond",
						Workflow: "respond-to-pr-review",
						OnReview: true,
					},
				},
			},
		},
		Workflows: []Workflow{
			{
				Name:        "fix-bug",
				Description: "Fix a reported bug",
				Phases: []Phase{
					{Name: "analyze", Type: "prompt"},
					{
						Name: "fix",
						Type: "prompt",
						Gate: &Gate{Type: "command", Run: "go test ./...", Retries: 2},
					},
				},
			},
			{
				Name: "respond-to-pr-review",
				Phases: []Phase{
					{Name: "respond", Type: "prompt"},
				},
			},
		},
		MissingWorkflows: []string{"ghost"},
	}
}

func TestRenderMermaid(t *testing.T) {
	g := fixtureGraph()
	var sb strings.Builder
	if err := RenderMermaid(g, &sb); err != nil {
		t.Fatalf("RenderMermaid: %v", err)
	}
	out := sb.String()

	mustContain := []string{
		"flowchart LR",
		"classDef source",
		"classDef workflow",
		"classDef phase",
		"classDef gate",
		"src_bugs",
		"src_pr_lifecycle",
		"subgraph wf_fix_bug",
		"wf_fix_bug__analyze",
		"wf_fix_bug__fix",
		"wf_fix_bug__fix_gate",
		"subgraph wf_respond_to_pr_review",
		// Source -> first phase edge with labels trigger summary.
		"src_bugs -- ",
		"labels: bug, ready",
		"--> wf_fix_bug__analyze",
		// PR-event trigger label.
		"on: review_submitted",
		// Missing workflow placeholder.
		"wf_ghost",
		"workflow file not found",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("mermaid output missing %q\n---\n%s", want, out)
		}
	}

	// Sequential edge: analyze -> gate -> fix should be present when a phase
	// has a gate and a successor.
	// In this fixture, analyze has no gate but fix is the last phase with a
	// gate, so we should see: analyze --> fix, then fix --> fix_gate.
	if !strings.Contains(out, "wf_fix_bug__analyze --> wf_fix_bug__fix") {
		t.Errorf("expected analyze -> fix sequential edge, got:\n%s", out)
	}
	if !strings.Contains(out, "wf_fix_bug__fix --> wf_fix_bug__fix_gate") {
		t.Errorf("expected fix -> fix_gate edge, got:\n%s", out)
	}
}

func TestRenderMermaid_DependsOn(t *testing.T) {
	g := &Graph{
		Sources: []Source{
			{
				Name:     "s",
				Type:     "github",
				Triggers: []Trigger{{TaskName: "t", Workflow: "wf", Labels: []string{"x"}}},
			},
		},
		Workflows: []Workflow{
			{
				Name: "wf",
				Phases: []Phase{
					{Name: "setup"},
					{Name: "left", DependsOn: []string{"setup"}},
					{Name: "right", DependsOn: []string{"setup"}},
				},
			},
		},
	}
	var sb strings.Builder
	if err := RenderMermaid(g, &sb); err != nil {
		t.Fatalf("RenderMermaid: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "wf_wf__setup --> wf_wf__left") {
		t.Errorf("expected setup -> left depends_on edge:\n%s", out)
	}
	if !strings.Contains(out, "wf_wf__setup --> wf_wf__right") {
		t.Errorf("expected setup -> right depends_on edge:\n%s", out)
	}
}

func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"fix-bug":              "fix_bug",
		"github-pr-events":     "github_pr_events",
		"alphaNumeric123":      "alphaNumeric123",
		"":                     "_",
		"with spaces and dots": "with_spaces_and_dots",
	}
	for in, want := range cases {
		if got := sanitizeID(in); got != want {
			t.Errorf("sanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
}
