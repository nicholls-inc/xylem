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

func TestBuildReverseIndex(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd) //nolint:errcheck

	writePrompt(t, "prompts/a.md")
	writeWorkflowYAML(t, "workflows", "shared", `name: shared
phases:
  - name: only
    prompt_file: prompts/a.md
    max_turns: 1
`)

	// Two sources fire the same workflow; one source references a missing
	// workflow. The reverse index should cover both.
	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"bugs": {
				Type: "github", Repo: "o/r",
				Tasks: map[string]config.Task{
					"fix": {
						Labels:   []string{"bug"},
						Workflow: "shared",
						StatusLabels: &config.StatusLabels{
							Running: "in-progress",
							Failed:  "xylem-failed",
						},
					},
				},
			},
			"features": {
				Type: "github", Repo: "o/r",
				Tasks: map[string]config.Task{
					"impl": {
						Labels:   []string{"enhancement"},
						Workflow: "shared",
					},
				},
			},
			"gap": {
				Type: "github", Repo: "o/r",
				Tasks: map[string]config.Task{
					"t": {
						Labels:   []string{"x"},
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

	if g.TriggersPerWorkflow == nil {
		t.Fatal("TriggersPerWorkflow was not populated")
	}
	sharedRefs, ok := g.TriggersPerWorkflow["shared"]
	if !ok || len(sharedRefs) != 2 {
		t.Fatalf("expected 2 triggers for shared, got %+v", sharedRefs)
	}
	// Sorted by (Source, Task) — bugs < features alphabetically.
	if sharedRefs[0].Source != "bugs" || sharedRefs[0].Task != "fix" {
		t.Errorf("first ref: got %+v, want bugs.fix", sharedRefs[0])
	}
	if sharedRefs[1].Source != "features" || sharedRefs[1].Task != "impl" {
		t.Errorf("second ref: got %+v, want features.impl", sharedRefs[1])
	}
	// Status labels were lifted onto the trigger ref.
	if sharedRefs[0].StatusLabels["running"] != "in-progress" {
		t.Errorf("status label 'running' not lifted onto TriggerRef: %+v", sharedRefs[0].StatusLabels)
	}
	if sharedRefs[0].StatusLabels["failed"] != "xylem-failed" {
		t.Errorf("status label 'failed' not lifted onto TriggerRef: %+v", sharedRefs[0].StatusLabels)
	}

	// Missing workflow still gets an entry in the reverse index so the
	// audit can answer "which triggers point at the gap?".
	missingRefs, ok := g.TriggersPerWorkflow["does-not-exist"]
	if !ok || len(missingRefs) != 1 {
		t.Fatalf("expected 1 trigger for missing workflow, got %+v", missingRefs)
	}
	if missingRefs[0].Source != "gap" {
		t.Errorf("missing workflow ref: got %+v", missingRefs[0])
	}

	// Trigger.StatusLabels lifts the config map for the PR/state-machine path.
	var bugs *Source
	for i := range g.Sources {
		if g.Sources[i].Name == "bugs" {
			bugs = &g.Sources[i]
			break
		}
	}
	if bugs == nil {
		t.Fatal("bugs source not found in graph")
	}
	if bugs.Triggers[0].StatusLabels["running"] != "in-progress" {
		t.Errorf("Trigger.StatusLabels not populated: %+v", bugs.Triggers[0].StatusLabels)
	}
}

func TestBuildDetectsOrphanWorkflows(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd) //nolint:errcheck

	writePrompt(t, "prompts/a.md")
	writeWorkflowYAML(t, "workflows", "used", `name: used
phases:
  - name: p
    prompt_file: prompts/a.md
    max_turns: 1
`)
	// Orphan: on disk but no task references it. Its prompt path is
	// intentionally not created — the orphan scan must list the filename
	// without loading the YAML, so a missing prompt doesn't fail the build.
	writeWorkflowYAML(t, "workflows", "orphan", `name: orphan
phases:
  - name: p
    prompt_file: prompts/missing.md
    max_turns: 1
`)

	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"s": {
				Type: "github", Repo: "o/r",
				Tasks: map[string]config.Task{
					"t": {Labels: []string{"x"}, Workflow: "used"},
				},
			},
		},
	}

	g, err := Build(cfg, "workflows")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(g.OrphanedWorkflows) != 1 || g.OrphanedWorkflows[0] != "orphan" {
		t.Errorf("expected [orphan] in OrphanedWorkflows, got %+v", g.OrphanedWorkflows)
	}
	// The "used" workflow should still be in g.Workflows (loaded normally).
	if len(g.Workflows) != 1 || g.Workflows[0].Name != "used" {
		t.Errorf("expected 1 loaded workflow 'used', got %+v", g.Workflows)
	}
}

func TestBuildOrphanScanHandlesMissingDir(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd) //nolint:errcheck

	// No workflows directory exists. The build should still succeed with
	// the referenced workflow recorded as missing.
	cfg := &config.Config{
		Sources: map[string]config.SourceConfig{
			"s": {
				Type: "github", Repo: "o/r",
				Tasks: map[string]config.Task{
					"t": {Labels: []string{"x"}, Workflow: "whatever"},
				},
			},
		},
	}
	g, err := Build(cfg, "workflows")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(g.OrphanedWorkflows) != 0 {
		t.Errorf("expected no orphans when dir is missing, got %+v", g.OrphanedWorkflows)
	}
	if len(g.MissingWorkflows) != 1 {
		t.Errorf("expected 1 missing workflow, got %+v", g.MissingWorkflows)
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
