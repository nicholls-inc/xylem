package workflow

import "testing"

func TestLoadWorkflowGateWithoutEvidence(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Phases[0].Gate == nil {
		t.Fatal("Gate = nil, want non-nil gate")
	}
	if got.Phases[0].Gate.Evidence != nil {
		t.Fatalf("Gate.Evidence = %#v, want nil", got.Phases[0].Gate.Evidence)
	}
}

func TestLoadWorkflowGateWithEvidence(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
      evidence:
        claim: "All tests pass"
        level: behaviorally_checked
        checker: "go test"
        trust_boundary: "Package-level only"
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Phases[0].Gate == nil || got.Phases[0].Gate.Evidence == nil {
		t.Fatal("Gate.Evidence = nil, want evidence metadata")
	}

	e := got.Phases[0].Gate.Evidence
	if e.Claim != "All tests pass" {
		t.Fatalf("Evidence.Claim = %q, want %q", e.Claim, "All tests pass")
	}
	if e.Level != "behaviorally_checked" {
		t.Fatalf("Evidence.Level = %q, want %q", e.Level, "behaviorally_checked")
	}
	if e.Checker != "go test" {
		t.Fatalf("Evidence.Checker = %q, want %q", e.Checker, "go test")
	}
	if e.TrustBoundary != "Package-level only" {
		t.Fatalf("Evidence.TrustBoundary = %q, want %q", e.TrustBoundary, "Package-level only")
	}
}

func TestLoadWorkflowGateRejectsInvalidEvidenceLevel(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
      evidence:
        claim: "All tests pass"
        level: high
`)

	_, err := Load(path)
	requireErrorContains(t, err, `gate evidence level "high" is not valid`)
	requireErrorContains(t, err, "must be proved, mechanically_checked, behaviorally_checked, or observed_in_situ")
}

func TestLoadWorkflowGateWithPartialEvidence(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
      evidence:
        claim: "Tests pass"
        level: mechanically_checked
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Phases[0].Gate == nil || got.Phases[0].Gate.Evidence == nil {
		t.Fatal("Gate.Evidence = nil, want evidence metadata")
	}

	e := got.Phases[0].Gate.Evidence
	if e.Checker != "" {
		t.Fatalf("Evidence.Checker = %q, want empty string", e.Checker)
	}
	if e.TrustBoundary != "" {
		t.Fatalf("Evidence.TrustBoundary = %q, want empty string", e.TrustBoundary)
	}
}
