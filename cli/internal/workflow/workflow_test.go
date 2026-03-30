package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkflowFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write workflow file: %v", err)
	}

	return path
}

func createPromptFile(t *testing.T, dir, name string) {
	t.Helper()

	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("create prompt dir: %v", err)
	}
	if err := os.WriteFile(full, []byte("prompt content"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}

	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %q", want, err.Error())
	}
}

// chdirTemp changes the working directory to dir for the duration of the test.
func chdirTemp(t *testing.T, dir string) {
	t.Helper()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %q: %v", dir, err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		workflowName string // filename stem; defaults to "fix-bug"
		yaml      string
		prompts   []string // prompt files to create relative to repo root (cwd)
		wantErr   string   // empty means no error expected
		checkFunc func(t *testing.T, s *Workflow)
	}{
		{
			name:      "valid workflow file",
			workflowName: "fix-bug",
			yaml: `name: fix-bug
description: Fix a bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
`,
			prompts: []string{"prompts/analyze.md", "prompts/implement.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if s.Name != "fix-bug" {
					t.Fatalf("Name = %q, want fix-bug", s.Name)
				}
				if s.Description != "Fix a bug" {
					t.Fatalf("Description = %q, want 'Fix a bug'", s.Description)
				}
				if len(s.Phases) != 2 {
					t.Fatalf("len(Phases) = %d, want 2", len(s.Phases))
				}
				if s.Phases[0].Name != "analyze" {
					t.Fatalf("Phases[0].Name = %q, want analyze", s.Phases[0].Name)
				}
				if s.Phases[1].MaxTurns != 20 {
					t.Fatalf("Phases[1].MaxTurns = %d, want 20", s.Phases[1].MaxTurns)
				}
			},
		},
		{
			name:      "valid workflow with all features",
			workflowName: "deploy",
			yaml: `name: deploy
description: Deploy with gates
phases:
  - name: build
    prompt_file: prompts/build.md
    max_turns: 15
    allowed_tools: Bash,Read
    gate:
      type: command
      run: make test
      retries: 2
      retry_delay: "5s"
  - name: review
    prompt_file: prompts/review.md
    max_turns: 5
    gate:
      type: label
      wait_for: approved
      timeout: "12h"
      poll_interval: "30s"
`,
			prompts: []string{"prompts/build.md", "prompts/review.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if s.Phases[0].Gate.Type != "command" {
					t.Fatalf("gate type = %q, want command", s.Phases[0].Gate.Type)
				}
				if s.Phases[0].Gate.Retries != 2 {
					t.Fatalf("gate retries = %d, want 2", s.Phases[0].Gate.Retries)
				}
				if *s.Phases[0].AllowedTools != "Bash,Read" {
					t.Fatalf("AllowedTools = %q, want Bash,Read", *s.Phases[0].AllowedTools)
				}
				if s.Phases[1].Gate.Type != "label" {
					t.Fatalf("gate type = %q, want label", s.Phases[1].Gate.Type)
				}
				if s.Phases[1].Gate.WaitFor != "approved" {
					t.Fatalf("gate wait_for = %q, want approved", s.Phases[1].Gate.WaitFor)
				}
			},
		},
		{
			name:         "valid workflow with model",
			workflowName: "fix-bug",
			yaml: `name: fix-bug
description: Fix a bug
model: claude-sonnet-4-20250514
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
    model: claude-opus-4-20250514
`,
			prompts: []string{"prompts/analyze.md", "prompts/implement.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if s.Model == nil || *s.Model != "claude-sonnet-4-20250514" {
					t.Fatalf("Workflow.Model = %v, want claude-sonnet-4-20250514", s.Model)
				}
				if s.Phases[0].Model != nil {
					t.Fatalf("Phases[0].Model = %v, want nil", s.Phases[0].Model)
				}
				if s.Phases[1].Model == nil || *s.Phases[1].Model != "claude-opus-4-20250514" {
					t.Fatalf("Phases[1].Model = %v, want claude-opus-4-20250514", s.Phases[1].Model)
				}
			},
		},
		{
			name:      "missing phases key",
			workflowName: "test-workflow",
			yaml:      "name: test-workflow\n",
			wantErr:   `"phases" is required`,
		},
		{
			name:      "empty name",
			workflowName: "test-workflow",
			yaml: `phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `"name" is required`,
		},
		{
			name:      "duplicate phase names",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: implement
    prompt_file: prompts/a.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/b.md
    max_turns: 10
`,
			prompts: []string{"prompts/a.md", "prompts/b.md"},
			wantErr: `duplicate phase name "implement"`,
		},
		{
			name:      "missing prompt_file",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    max_turns: 10
`,
			wantErr: "prompt_file is required",
		},
		{
			name:      "non-existent prompt_file",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/missing.md
    max_turns: 10
`,
			wantErr: "prompt_file not found: prompts/missing.md",
		},
		{
			name:      "invalid gate type",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: webhook
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `type must be "command" or "label"`,
		},
		{
			name:      "command gate missing run",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "run is required for command gate",
		},
		{
			name:      "label gate missing wait_for",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: label
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "wait_for is required for label gate",
		},
		{
			name:      "invalid duration string",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: make test
      retry_delay: not-a-duration
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `invalid retry_delay "not-a-duration"`,
		},
		{
			name:      "max_turns zero",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 0
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "max_turns must be greater than 0",
		},
		{
			name:      "allowed_tools empty string",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    allowed_tools: ""
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "allowed_tools must not be empty when specified",
		},
		{
			name:      "name does not match filename",
			workflowName: "fix-bug",
			yaml: `name: wrong-name
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `workflow name "wrong-name" does not match filename "fix-bug.yaml"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)

			for _, p := range tt.prompts {
				createPromptFile(t, dir, p)
			}

			workflowName := tt.workflowName
			if workflowName == "" {
				workflowName = "fix-bug"
			}
			path := writeWorkflowFile(t, dir, workflowName, tt.yaml)
			s, err := Load(path)

			if tt.wantErr != "" {
				requireErrorContains(t, err, tt.wantErr)
				return
			}

			if err != nil {
				t.Fatalf("Load returned unexpected error: %v", err)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, s)
			}
		})
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeWorkflowFile(t, dir, "workflow", "name: [broken\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for malformed yaml")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name          string
		workflowFileName string // filename stem for the workflow file (used for name validation)
		wf         Workflow
		prompts       []string // prompt files to create relative to cwd
		wantErr       string
	}{
		{
			name:          "valid minimal workflow",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:          "empty name",
			workflowFileName: "test",
			wf: Workflow{
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `"name" is required`,
		},
		{
			name:          "no phases",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
			},
			wantErr: `"phases" is required`,
		},
		{
			name:          "phase with empty name",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "each phase must have a non-empty name",
		},
		{
			name:          "duplicate phase names",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "build", PromptFile: "a.md", MaxTurns: 5},
					{Name: "build", PromptFile: "b.md", MaxTurns: 5},
				},
			},
			prompts: []string{"a.md", "b.md"},
			wantErr: `duplicate phase name "build"`,
		},
		{
			name:          "missing prompt_file value",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "", MaxTurns: 5},
				},
			},
			wantErr: "prompt_file is required",
		},
		{
			name:          "non-existent prompt_file",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "does-not-exist.md", MaxTurns: 5},
				},
			},
			wantErr: "prompt_file not found: does-not-exist.md",
		},
		{
			name:          "max_turns zero",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 0},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "max_turns must be greater than 0",
		},
		{
			name:          "max_turns negative",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: -1},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "max_turns must be greater than 0",
		},
		{
			name:          "invalid gate type",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "webhook"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `type must be "command" or "label"`,
		},
		{
			name:          "command gate missing run",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "command"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "run is required for command gate",
		},
		{
			name:          "label gate missing wait_for",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "label"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "wait_for is required for label gate",
		},
		{
			name:          "invalid retry_delay duration",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "command", Run: "make test", RetryDelay: "bad"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `invalid retry_delay "bad"`,
		},
		{
			name:          "invalid timeout duration",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "label", WaitFor: "approved", Timeout: "forever"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `invalid timeout "forever"`,
		},
		{
			name:          "invalid poll_interval duration",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "label", WaitFor: "approved", PollInterval: "nope"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `invalid poll_interval "nope"`,
		},
		{
			name:          "allowed_tools empty string",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5, AllowedTools: strPtr("")},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "allowed_tools must not be empty when specified",
		},
		{
			name:          "allowed_tools nil is valid",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5, AllowedTools: nil},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:          "allowed_tools with value is valid",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5, AllowedTools: strPtr("Bash,Read")},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:          "valid command gate with all fields",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{
							Type:       "command",
							Run:        "go test ./...",
							Retries:    3,
							RetryDelay: "10s",
						},
					},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:          "valid label gate with all fields",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{
							Type:         "label",
							WaitFor:      "approved",
							Timeout:      "24h",
							PollInterval: "60s",
						},
					},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:          "name does not match filename",
			workflowFileName: "test",
			wf: Workflow{
				Name: "wrong-name",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `workflow name "wrong-name" does not match filename "test.yaml"`,
		},
		{
			name:          "phase name with hyphens is rejected",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "create-issues", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `phase name "create-issues" is invalid; must start with a lowercase letter and contain only lowercase letters, digits, and underscores`,
		},
		{
			name:          "phase name with uppercase is rejected",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "CreateIssues", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `phase name "CreateIssues" is invalid`,
		},
		{
			name:          "phase name with underscores is accepted",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "create_issues", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:          "phase name starting with digit is rejected",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "2step", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `phase name "2step" is invalid`,
		},
		{
			name:          "phase name with digits is accepted",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step2", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)

			for _, p := range tt.prompts {
				createPromptFile(t, dir, p)
			}

			workflowFileName := tt.workflowFileName
			if workflowFileName == "" {
				workflowFileName = "test"
			}
			workflowFilePath := filepath.Join(dir, workflowFileName+".yaml")

			err := tt.wf.Validate(workflowFilePath)

			if tt.wantErr != "" {
				requireErrorContains(t, err, tt.wantErr)
				return
			}

			if err != nil {
				t.Fatalf("Validate returned unexpected error: %v", err)
			}
		})
	}
}

func TestLoadWorkflowWithModel(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)

	createPromptFile(t, dir, "prompts/analyze.md")
	createPromptFile(t, dir, "prompts/implement.md")

	path := writeWorkflowFile(t, dir, "fix-bug", `name: fix-bug
description: Fix a bug
model: claude-sonnet-4-20250514
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    model: claude-opus-4-20250514
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
`)

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if s.Model == nil || *s.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("Workflow.Model = %v, want claude-sonnet-4-20250514", s.Model)
	}
	if s.Phases[0].Model == nil || *s.Phases[0].Model != "claude-opus-4-20250514" {
		t.Fatalf("Phases[0].Model = %v, want claude-opus-4-20250514", s.Phases[0].Model)
	}
	if s.Phases[1].Model != nil {
		t.Fatalf("Phases[1].Model = %v, want nil", s.Phases[1].Model)
	}
}

func TestLoadWorkflowWithoutModel(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)

	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "fix-bug", `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if s.Model != nil {
		t.Fatalf("Workflow.Model = %v, want nil", s.Model)
	}
	if s.Phases[0].Model != nil {
		t.Fatalf("Phases[0].Model = %v, want nil", s.Phases[0].Model)
	}
}

func strPtr(s string) *string {
	return &s
}

func TestWorkflowLLMField(t *testing.T) {
	tests := []struct {
		name         string
		workflowYAML string
		prompts      []string
		wantErr      string
		checkFunc    func(t *testing.T, wf *Workflow)
	}{
		{
			name: "workflow llm claude",
			workflowYAML: `name: test-workflow
llm: claude
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.LLM == nil || *wf.LLM != "claude" {
					t.Fatalf("Workflow.LLM = %v, want claude", wf.LLM)
				}
			},
		},
		{
			name: "workflow llm copilot",
			workflowYAML: `name: test-workflow
llm: copilot
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.LLM == nil || *wf.LLM != "copilot" {
					t.Fatalf("Workflow.LLM = %v, want copilot", wf.LLM)
				}
			},
		},
		{
			name: "workflow llm absent means nil",
			workflowYAML: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.LLM != nil {
					t.Fatalf("Workflow.LLM = %v, want nil", wf.LLM)
				}
			},
		},
		{
			name: "workflow llm invalid",
			workflowYAML: `name: test-workflow
llm: gpt4
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
`,
			prompts:  []string{"prompts/analyze.md"},
			wantErr: `workflow: llm must be "claude" or "copilot"`,
		},
		{
			name: "phase llm copilot",
			workflowYAML: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
    llm: copilot
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.Phases[0].LLM == nil || *wf.Phases[0].LLM != "copilot" {
					t.Fatalf("Phase.LLM = %v, want copilot", wf.Phases[0].LLM)
				}
			},
		},
		{
			name: "phase llm invalid",
			workflowYAML: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
    llm: openai
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `phase "analyze": llm must be "claude" or "copilot"`,
		},
		{
			name: "workflow and phase llm with model",
			workflowYAML: `name: test-workflow
llm: copilot
model: gpt-4o
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
    llm: claude
    model: claude-sonnet-4-5
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.LLM == nil || *wf.LLM != "copilot" {
					t.Fatalf("Workflow.LLM = %v, want copilot", wf.LLM)
				}
				if wf.Model == nil || *wf.Model != "gpt-4o" {
					t.Fatalf("Workflow.Model = %v, want gpt-4o", wf.Model)
				}
				if wf.Phases[0].LLM == nil || *wf.Phases[0].LLM != "claude" {
					t.Fatalf("Phase.LLM = %v, want claude", wf.Phases[0].LLM)
				}
				if wf.Phases[0].Model == nil || *wf.Phases[0].Model != "claude-sonnet-4-5" {
					t.Fatalf("Phase.Model = %v, want claude-sonnet-4-5", wf.Phases[0].Model)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)

			for _, p := range tt.prompts {
				createPromptFile(t, dir, p)
			}

			path := writeWorkflowFile(t, dir, "test-workflow", tt.workflowYAML)
			wf, err := Load(path)

			if tt.wantErr != "" {
				requireErrorContains(t, err, tt.wantErr)
				return
			}

			if err != nil {
				t.Fatalf("Load returned unexpected error: %v", err)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, wf)
			}
		})
	}
}
