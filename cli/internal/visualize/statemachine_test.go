package visualize

import (
	"strings"
	"testing"
)

// runRender is a helper that asserts RenderStateMachine succeeds and returns
// the rendered text. All state-machine tests operate on the string form so
// we're testing the stable Mermaid representation, not the internal builder.
func runRender(t *testing.T, g *Graph) string {
	t.Helper()
	var sb strings.Builder
	if err := RenderStateMachine(g, &sb); err != nil {
		t.Fatalf("RenderStateMachine: %v", err)
	}
	return sb.String()
}

func TestRenderStateMachine_IssueLabelBasic(t *testing.T) {
	g := &Graph{
		Sources: []Source{
			{
				Name: "bugs",
				Type: "github",
				Repo: "owner/repo",
				Triggers: []Trigger{
					{
						TaskName: "fix-bugs",
						Workflow: "fix-bug",
						Labels:   []string{"ready-for-work", "bug"},
						StatusLabels: map[string]string{
							"running": "in-progress",
							"failed":  "xylem-failed",
						},
					},
				},
			},
		},
		Workflows: []Workflow{
			{Name: "fix-bug", Phases: []Phase{{Name: "analyze"}}},
		},
	}
	out := runRender(t, g)

	mustContain := []string{
		"stateDiagram-v2",
		`state "Issue labels" as issues`,
		// Canonical sorted label set (bug before ready-for-work).
		"issue_bug__ready_for_work",
		`"{bug, ready-for-work}"`,
		// Running workflow state.
		"run_fix_bug",
		// Trigger edge uses source.task notation.
		"issue_bug__ready_for_work --> run_fix_bug : bugs.fix-bugs",
		// Status label fan-out: running → single-label state.
		"run_fix_bug --> issue_in_progress : running",
		"run_fix_bug --> issue_xylem_failed : failed",
		// classDef applied.
		"class run_fix_bug workflow",
		"class issue_bug__ready_for_work label",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("state machine output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderStateMachine_LabelOrderCanonical(t *testing.T) {
	// Two triggers with the same set of labels in different orders should
	// collapse into a single issue-label state.
	g := &Graph{
		Sources: []Source{
			{
				Name: "a", Type: "github", Repo: "o/r",
				Triggers: []Trigger{{TaskName: "t1", Workflow: "wf", Labels: []string{"y", "x"}}},
			},
			{
				Name: "b", Type: "github", Repo: "o/r",
				Triggers: []Trigger{{TaskName: "t2", Workflow: "wf", Labels: []string{"x", "y"}}},
			},
		},
		Workflows: []Workflow{{Name: "wf", Phases: []Phase{{Name: "p"}}}},
	}
	out := runRender(t, g)

	// Both transitions should point at the same canonical state ID.
	if strings.Count(out, "issue_x__y") < 2 {
		t.Errorf("expected both triggers to share canonical state ID issue_x__y\n%s", out)
	}
	// There should be exactly one definition of the state.
	if strings.Count(out, `state "{x, y}" as issue_x__y`) != 1 {
		t.Errorf("expected a single state definition for {x, y}\n%s", out)
	}
}

func TestRenderStateMachine_PREventsWithAuthorAllow(t *testing.T) {
	g := &Graph{
		Sources: []Source{
			{
				Name: "harness-pr-lifecycle",
				Type: "github-pr-events",
				Repo: "owner/repo",
				Triggers: []Trigger{
					{
						TaskName:    "respond-reviews",
						Workflow:    "respond-to-pr-review",
						OnReview:    true,
						AuthorAllow: []string{"copilot-pull-request-reviewer[bot]"},
					},
					{
						TaskName: "fix-checks",
						Workflow: "fix-pr-checks",
						OnChecks: true,
					},
				},
			},
		},
		Workflows: []Workflow{
			{Name: "respond-to-pr-review", Phases: []Phase{{Name: "r"}}},
			{Name: "fix-pr-checks", Phases: []Phase{{Name: "r"}}},
		},
	}
	out := runRender(t, g)

	mustContain := []string{
		`state "PR lifecycle" as pr`,
		"pr_review_submitted",
		// Author allow list appears in the display label.
		"allow: copilot-pull-request-reviewer[bot]",
		"pr_checks_failed",
		"run_respond_to_pr_review",
		"run_fix_pr_checks",
		"pr_review_submitted --> run_respond_to_pr_review : harness-pr-lifecycle.respond-reviews",
		"pr_checks_failed --> run_fix_pr_checks : harness-pr-lifecycle.fix-checks",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderStateMachine_MergeSource(t *testing.T) {
	g := &Graph{
		Sources: []Source{
			{
				Name: "harness-post-merge",
				Type: "github-merge",
				Repo: "owner/repo",
				Triggers: []Trigger{{
					TaskName: "unblock-deps",
					Workflow: "unblock-wave",
				}},
			},
		},
		Workflows: []Workflow{{Name: "unblock-wave", Phases: []Phase{{Name: "p"}}}},
	}
	out := runRender(t, g)
	if !strings.Contains(out, "pr_merged") {
		t.Errorf("expected synthetic pr_merged state\n%s", out)
	}
	if !strings.Contains(out, "pr_merged --> run_unblock_wave : harness-post-merge.unblock-deps") {
		t.Errorf("expected merge → workflow transition\n%s", out)
	}
}

func TestRenderStateMachine_MissingWorkflowDashed(t *testing.T) {
	g := &Graph{
		Sources: []Source{
			{
				Name: "s", Type: "github", Repo: "o/r",
				Triggers: []Trigger{{TaskName: "t", Workflow: "ghost", Labels: []string{"x"}}},
			},
		},
		MissingWorkflows: []string{"ghost"},
	}
	out := runRender(t, g)
	if !strings.Contains(out, "run_ghost") {
		t.Errorf("expected run_ghost state\n%s", out)
	}
	if !strings.Contains(out, "missing") {
		t.Errorf("expected 'missing' annotation in state label\n%s", out)
	}
	if !strings.Contains(out, "class run_ghost missing") {
		t.Errorf("expected missing class assignment on run_ghost\n%s", out)
	}
	// Missing workflow should NOT appear inside the issue block as a normal
	// running state — it's listed only in the outer "no trigger path"
	// section with its dashed style.
	issueBlock := extractBlock(out, "state \"Issue labels\" as issues {", "}")
	if strings.Contains(issueBlock, "state \"running(ghost)") {
		t.Errorf("missing workflow should not be rendered as a normal running state inside the issue block:\n%s", issueBlock)
	}
}

func TestRenderStateMachine_OrphanWorkflow(t *testing.T) {
	g := &Graph{
		Sources: []Source{
			{
				Name: "s", Type: "github", Repo: "o/r",
				Triggers: []Trigger{{TaskName: "t", Workflow: "alive", Labels: []string{"x"}}},
			},
		},
		Workflows:         []Workflow{{Name: "alive", Phases: []Phase{{Name: "p"}}}},
		OrphanedWorkflows: []string{"lonely"},
	}
	out := runRender(t, g)
	if !strings.Contains(out, "run_lonely") {
		t.Errorf("expected run_lonely orphan state\n%s", out)
	}
	if !strings.Contains(out, "orphan") {
		t.Errorf("expected 'orphan' annotation\n%s", out)
	}
	if !strings.Contains(out, "class run_lonely orphan") {
		t.Errorf("expected orphan class assignment\n%s", out)
	}
	// Orphan should not appear as a trigger target — no incoming edges.
	if strings.Contains(out, "--> run_lonely") {
		t.Errorf("orphan workflow unexpectedly has incoming edges\n%s", out)
	}
}

func TestRenderStateMachine_LabelGateHandoff(t *testing.T) {
	g := &Graph{
		Sources: []Source{
			{
				Name: "s", Type: "github", Repo: "o/r",
				Triggers: []Trigger{{TaskName: "t", Workflow: "wait-wf", Labels: []string{"trigger"}}},
			},
		},
		Workflows: []Workflow{
			{
				Name: "wait-wf",
				Phases: []Phase{
					{
						Name: "p",
						Type: "prompt",
						Gate: &Gate{Type: "label", WaitFor: "reviewed"},
					},
				},
			},
		},
	}
	out := runRender(t, g)
	if !strings.Contains(out, "Label gate handoffs") {
		t.Errorf("expected cross-machine handoff section\n%s", out)
	}
	if !strings.Contains(out, "run_wait_wf --> issue_reviewed : gate") {
		t.Errorf("expected cross-machine gate edge\n%s", out)
	}
	// The wait-for label is registered as an issue-label state even though
	// no source uses it as a trigger.
	if !strings.Contains(out, "issue_reviewed") {
		t.Errorf("expected issue_reviewed state from gate wait_for\n%s", out)
	}
}

func TestRenderStateMachine_RealConfigLoops(t *testing.T) {
	// Mirrors the real .xylem.yml pattern: harness-impl writes
	// status_labels.running = in-progress, other sources exclude it. We
	// don't model exclude here, but the `in-progress` state node should be
	// visible with an incoming edge from run_implement_harness.
	g := &Graph{
		Sources: []Source{
			{
				Name: "harness-impl", Type: "github", Repo: "o/r",
				Triggers: []Trigger{{
					TaskName: "implement",
					Workflow: "implement-harness",
					Labels:   []string{"harness-impl", "ready-for-work"},
					StatusLabels: map[string]string{
						"running": "in-progress",
						"failed":  "xylem-failed",
					},
				}},
			},
			{
				Name: "harness-merge", Type: "github-pr", Repo: "o/r",
				Triggers: []Trigger{{
					TaskName: "merge-ready",
					Workflow: "merge-pr",
					Labels:   []string{"ready-to-merge", "harness-impl"},
					StatusLabels: map[string]string{
						"running":   "pr-vessel-active",
						"completed": "pr-vessel-merged",
						"failed":    "xylem-failed",
					},
				}},
			},
		},
		Workflows: []Workflow{
			{Name: "implement-harness", Phases: []Phase{{Name: "p"}}},
			{Name: "merge-pr", Phases: []Phase{{Name: "p"}}},
		},
	}
	out := runRender(t, g)

	// Both workflows fan out into the shared issue-label state xylem-failed.
	if strings.Count(out, "--> issue_xylem_failed : failed") != 2 {
		t.Errorf("expected both workflows to emit failure edges to issue_xylem_failed\n%s", out)
	}
	// pr-vessel-active shows up as its own state.
	if !strings.Contains(out, "issue_pr_vessel_active") {
		t.Errorf("expected issue_pr_vessel_active state\n%s", out)
	}
	// in-progress shows up as its own state.
	if !strings.Contains(out, "issue_in_progress") {
		t.Errorf("expected issue_in_progress state\n%s", out)
	}
}

func TestRenderStateMachine_Deterministic(t *testing.T) {
	g := &Graph{
		Sources: []Source{
			{
				Name: "z", Type: "github", Repo: "o/r",
				Triggers: []Trigger{{TaskName: "t", Workflow: "wf-z", Labels: []string{"z"}}},
			},
			{
				Name: "a", Type: "github", Repo: "o/r",
				Triggers: []Trigger{{TaskName: "t", Workflow: "wf-a", Labels: []string{"a"}}},
			},
		},
		Workflows: []Workflow{
			{Name: "wf-a", Phases: []Phase{{Name: "p"}}},
			{Name: "wf-z", Phases: []Phase{{Name: "p"}}},
		},
	}
	first := runRender(t, g)
	second := runRender(t, g)
	if first != second {
		t.Errorf("RenderStateMachine output is not deterministic")
	}
}

func TestIssueLabelStateKey(t *testing.T) {
	cases := []struct {
		name        string
		in          []string
		wantID      string
		wantDisplay string
	}{
		{"empty", nil, "issue_empty", "{}"},
		{"single", []string{"bug"}, "issue_bug", "{bug}"},
		{"sorted", []string{"ready-for-work", "bug"}, "issue_bug__ready_for_work", "{bug, ready-for-work}"},
		{"dedup", []string{"bug", "bug"}, "issue_bug", "{bug}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, display := issueLabelStateKey(tc.in)
			if id != tc.wantID {
				t.Errorf("id: got %q want %q", id, tc.wantID)
			}
			if display != tc.wantDisplay {
				t.Errorf("display: got %q want %q", display, tc.wantDisplay)
			}
		})
	}
}

func TestPREventStateKey(t *testing.T) {
	cases := []struct {
		name    string
		trigger Trigger
		wantID  string
	}{
		{
			name:    "review submitted",
			trigger: Trigger{OnReview: true},
			wantID:  "pr_review_submitted",
		},
		{
			name:    "checks failed",
			trigger: Trigger{OnChecks: true},
			wantID:  "pr_checks_failed",
		},
		{
			name:    "review + comment",
			trigger: Trigger{OnReview: true, OnComment: true},
			wantID:  "pr_review_submitted__commented",
		},
		{
			name:    "allow list does not affect ID",
			trigger: Trigger{OnReview: true, AuthorAllow: []string{"bot"}},
			wantID:  "pr_review_submitted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := prEventStateKey(tc.trigger)
			if id != tc.wantID {
				t.Errorf("id: got %q want %q", id, tc.wantID)
			}
		})
	}
}

// extractBlock returns the substring between the first occurrence of start
// and the next occurrence of end (inclusive of both). Used to scope test
// assertions to a particular nested state block.
func extractBlock(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		return rest
	}
	return rest[:j+len(end)]
}
