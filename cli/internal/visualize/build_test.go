package visualize

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

// writeWorkflowYAML writes a workflow YAML plus a stub prompt file so the
// workflow.Load validator (which stats prompt_file) is satisfied.
func writeWorkflowYAML(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePrompt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_GitHubSourceWithSequentialWorkflow(t *testing.T) {
	tmp := t.TempDir()
	// Workflow load validates prompt_file paths relative to cwd.
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd) //nolint:errcheck

	writePrompt(t, "prompts/analyze.md")
	writePrompt(t, "prompts/fix.md")
	writeWorkflowYAML(t, "workflows", "fix-bug", `name: fix-bug
description: Fix a reported bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 30
  - name: fix
    prompt_file: prompts/fix.md
    max_turns: 60
    gate:
      type: command
      run: "go test ./..."
      retries: 2
`)

	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"bugs": {
				Type: "github",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"fix-bugs": {
						Labels:   []string{"bug", "ready"},
						Workflow: "fix-bug",
					},
				},
			},
		},
	}

	g, err := Build(cfg, "workflows")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(g.Sources) != 1 {
		t.Fatalf("want 1 source, got %d", len(g.Sources))
	}
	src := g.Sources[0]
	if src.Name != "bugs" || src.Type != "github" || src.Repo != "owner/repo" {
		t.Errorf("unexpected source: %+v", src)
	}
	if len(src.Triggers) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(src.Triggers))
	}
	trig := src.Triggers[0]
	if trig.Workflow != "fix-bug" || len(trig.Labels) != 2 {
		t.Errorf("unexpected trigger: %+v", trig)
	}

	if len(g.Workflows) != 1 {
		t.Fatalf("want 1 workflow, got %d", len(g.Workflows))
	}
	wf := g.Workflows[0]
	if wf.Name != "fix-bug" || wf.Description != "Fix a reported bug" {
		t.Errorf("unexpected workflow: %+v", wf)
	}
	if len(wf.Phases) != 2 {
		t.Fatalf("want 2 phases, got %d", len(wf.Phases))
	}
	if wf.Phases[1].Gate == nil || wf.Phases[1].Gate.Type != "command" {
		t.Errorf("expected command gate on fix phase")
	}
	if wf.Phases[1].Gate.Retries != 2 {
		t.Errorf("gate retries: want 2, got %d", wf.Phases[1].Gate.Retries)
	}
}

func TestBuild_PREventsSource(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd) //nolint:errcheck

	writePrompt(t, "prompts/respond.md")
	writeWorkflowYAML(t, "workflows", "respond-to-pr-review", `name: respond-to-pr-review
phases:
  - name: respond
    prompt_file: prompts/respond.md
    max_turns: 10
`)

	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"pr-lifecycle": {
				Type: "github-pr-events",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"respond-reviews": {
						Workflow: "respond-to-pr-review",
						On: &config.PREventsConfig{
							ReviewSubmitted: true,
							AuthorAllow:     []string{"copilot[bot]"},
						},
					},
				},
			},
		},
	}

	g, err := Build(cfg, "workflows")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	trig := g.Sources[0].Triggers[0]
	if !trig.OnReview {
		t.Errorf("expected OnReview to be true")
	}
	if len(trig.AuthorAllow) != 1 || trig.AuthorAllow[0] != "copilot[bot]" {
		t.Errorf("unexpected author allow: %+v", trig.AuthorAllow)
	}
}

func TestBuild_MissingWorkflow(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd) //nolint:errcheck

	if err := os.MkdirAll("workflows", 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"bugs": {
				Type: "github",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"fix-bugs": {
						Labels:   []string{"bug"},
						Workflow: "does-not-exist",
					},
				},
			},
		},
	}

	g, err := Build(cfg, "workflows")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(g.MissingWorkflows) != 1 || g.MissingWorkflows[0] != "does-not-exist" {
		t.Errorf("expected missing workflow entry, got %+v", g.MissingWorkflows)
	}
	if len(g.Workflows) != 0 {
		t.Errorf("expected no loaded workflows, got %d", len(g.Workflows))
	}
}

func TestBuild_DependsOnPhases(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd) //nolint:errcheck

	writePrompt(t, "prompts/a.md")
	writePrompt(t, "prompts/b.md")
	writePrompt(t, "prompts/c.md")
	writeWorkflowYAML(t, "workflows", "fanout", `name: fanout
phases:
  - name: setup
    prompt_file: prompts/a.md
    max_turns: 10
  - name: left
    prompt_file: prompts/b.md
    max_turns: 10
    depends_on: [setup]
  - name: right
    prompt_file: prompts/c.md
    max_turns: 10
    depends_on: [setup]
`)

	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"src": {
				Type: "github",
				Repo: "owner/repo",
				Tasks: map[string]config.Task{
					"t": {Labels: []string{"x"}, Workflow: "fanout"},
				},
			},
		},
	}

	g, err := Build(cfg, "workflows")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	wf := g.Workflows[0]
	if len(wf.Phases) != 3 {
		t.Fatalf("want 3 phases, got %d", len(wf.Phases))
	}
	if len(wf.Phases[1].DependsOn) != 1 || wf.Phases[1].DependsOn[0] != "setup" {
		t.Errorf("expected left to depend on setup, got %+v", wf.Phases[1].DependsOn)
	}
}

func TestBuild_NilConfig(t *testing.T) {
	if _, err := Build(nil, "workflows"); err == nil {
		t.Errorf("expected error for nil config")
	}
}

func TestBuild_SortsSourcesAndWorkflows(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd) //nolint:errcheck

	writePrompt(t, "prompts/a.md")
	writeWorkflowYAML(t, "workflows", "a-wf", `name: a-wf
phases:
  - name: only
    prompt_file: prompts/a.md
    max_turns: 1
`)
	writeWorkflowYAML(t, "workflows", "z-wf", `name: z-wf
phases:
  - name: only
    prompt_file: prompts/a.md
    max_turns: 1
`)

	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"zebra": {
				Type: "github", Repo: "o/r",
				Tasks: map[string]config.Task{"t": {Labels: []string{"x"}, Workflow: "z-wf"}},
			},
			"alpha": {
				Type: "github", Repo: "o/r",
				Tasks: map[string]config.Task{"t": {Labels: []string{"x"}, Workflow: "a-wf"}},
			},
		},
	}
	g, err := Build(cfg, "workflows")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if g.Sources[0].Name != "alpha" || g.Sources[1].Name != "zebra" {
		t.Errorf("sources not sorted: %s, %s", g.Sources[0].Name, g.Sources[1].Name)
	}
	if g.Workflows[0].Name != "a-wf" || g.Workflows[1].Name != "z-wf" {
		t.Errorf("workflows not sorted: %s, %s", g.Workflows[0].Name, g.Workflows[1].Name)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is..."},
		{"abc", 2, "ab"},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
