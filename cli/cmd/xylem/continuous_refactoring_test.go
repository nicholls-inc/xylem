package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/continuousrefactoring"
)

type continuousRefactoringRunnerStub struct {
	outputs map[string][]byte
	calls   [][]string
}

func (r *continuousRefactoringRunnerStub) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	key := strings.Join(call, "\x00")
	out, ok := r.outputs[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return out, nil
}

func TestContinuousRefactoringInspectCommandWritesManifest(t *testing.T) {
	setupTestDeps(t)

	repoRoot := t.TempDir()
	writeContinuousRefactoringTestFile(t, repoRoot, "cli/internal/example.go", `package example

func Small() {}

func BigFunction() {
	a := 1
	b := 2
	c := a + b
	if c > 0 {
		a++
	}
	if a > 1 {
		b++
	}
	if b > 2 {
		c++
	}
	if c > 3 {
		a += b
	}
	if a > 3 {
		c += a
	}
	println(a, b, c)
}
`)
	writeContinuousRefactoringTestFile(t, repoRoot, "cli/internal/example_test.go", `package example

func TestIgnored(t *testing.T) {}
`)

	deps.cfg = &config.Config{
		Repo: "owner/repo",
		Sources: map[string]config.SourceConfig{
			"continuous-refactoring-semantic": {
				Type:            "schedule",
				Repo:            "owner/repo",
				Workflow:        "continuous-refactoring",
				SourceDirs:      []string{"cli/internal"},
				FileExtensions:  []string{".go"},
				LOCThreshold:    10,
				MaxIssuesPerRun: 2,
				ExcludePatterns: []string{"**/*_test.go"},
			},
		},
	}

	now := time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC)
	oldNow := continuousRefactoringNow
	continuousRefactoringNow = func() time.Time { return now }
	t.Cleanup(func() { continuousRefactoringNow = oldNow })

	outputPath := filepath.Join(t.TempDir(), "manifest.json")
	cmd := newContinuousRefactoringCmd()
	cmd.SetArgs([]string{
		"inspect",
		"--repo-root", repoRoot,
		"--source", "continuous-refactoring-semantic",
		"--output", outputPath,
	})

	output := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if strings.TrimSpace(output) != "Wrote "+outputPath {
		t.Fatalf("stdout = %q, want %q", output, "Wrote "+outputPath)
	}

	manifest, err := continuousrefactoring.LoadManifest(outputPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if manifest.SourceName != "continuous-refactoring-semantic" {
		t.Fatalf("SourceName = %q, want continuous-refactoring-semantic", manifest.SourceName)
	}
	if manifest.Repo != "owner/repo" {
		t.Fatalf("Repo = %q, want owner/repo", manifest.Repo)
	}
	if manifest.LOCThreshold != 10 {
		t.Fatalf("LOCThreshold = %d, want 10", manifest.LOCThreshold)
	}
	if len(manifest.SemanticFindings) != 1 {
		t.Fatalf("len(SemanticFindings) = %d, want 1", len(manifest.SemanticFindings))
	}
	if got := manifest.SemanticFindings[0].Function; got != "BigFunction" {
		t.Fatalf("Function = %q, want BigFunction", got)
	}
	if len(manifest.FileFindings) != 1 || manifest.FileFindings[0].Path != "cli/internal/example.go" {
		t.Fatalf("FileFindings = %#v, want example.go only", manifest.FileFindings)
	}
}

func TestContinuousRefactoringOpenIssuesCommandWritesResult(t *testing.T) {
	setupTestDeps(t)

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	outputPath := filepath.Join(dir, "issues.json")
	now := time.Date(2026, 4, 10, 8, 15, 0, 0, time.UTC)
	requireManifest := continuousrefactoring.Manifest{
		Version:         1,
		GeneratedAt:     now.Format(time.RFC3339),
		Repo:            "owner/repo",
		RepoRoot:        dir,
		SourceName:      "continuous-refactoring-semantic",
		LOCThreshold:    80,
		MaxIssuesPerRun: 2,
		SemanticFindings: []continuousrefactoring.SemanticCandidate{
			{
				ID:             "semantic-runner-drain",
				Path:           "cli/internal/runner/runner.go",
				Function:       "Drain",
				StartLine:      10,
				EndLine:        140,
				LOC:            131,
				StatementCount: 24,
			},
			{
				ID:             "semantic-runner-build-template-data",
				Path:           "cli/internal/runner/runner.go",
				Function:       "buildTemplateData",
				StartLine:      200,
				EndLine:        320,
				LOC:            121,
				StatementCount: 16,
			},
		},
	}
	writeJSONFixture(t, manifestPath, requireManifest)

	runner := &continuousRefactoringRunnerStub{
		outputs: map[string][]byte{
			strings.Join([]string{"gh", "issue", "list", "--repo", "owner/repo", "--state", "open", "--json", "number,title,body", "--limit", "100"}, "\x00"):   []byte(`[]`),
			strings.Join([]string{"gh", "issue", "list", "--repo", "owner/repo", "--state", "closed", "--json", "number,title,body", "--limit", "100"}, "\x00"): []byte(`[{"number":41,"title":"[refactor] split old thing","body":"<!-- xylem:continuous-refactoring id:other -->"}]`),
			strings.Join([]string{"gh", "issue", "create", "--repo", "owner/repo", "--title", "[refactor] split Drain in cli/internal/runner/runner.go", "--body", strings.TrimSpace(`
<!-- xylem:continuous-refactoring id:semantic-runner-drain -->
## Why this function is a refactor target
- Path: cli/internal/runner/runner.go
- Function: Drain
- Lines: 10-140 (131 LOC)
- Statements: 24
- Scope: continuous-refactoring-semantic

## Suggested outcome
- Extract smaller helpers so the function falls below the configured 80 LOC threshold.
- Preserve current behavior and public APIs.
- Add or update focused tests if the refactor exposes missing coverage seams.
`)}, "\x00"): []byte("https://github.com/owner/repo/issues/42\n"),
			strings.Join([]string{"gh", "issue", "create", "--repo", "owner/repo", "--title", "[refactor] split buildTemplateData in cli/internal/runner/runner.go", "--body", strings.TrimSpace(`
<!-- xylem:continuous-refactoring id:semantic-runner-build-template-data -->
## Why this function is a refactor target
- Path: cli/internal/runner/runner.go
- Function: buildTemplateData
- Lines: 200-320 (121 LOC)
- Statements: 16
- Scope: continuous-refactoring-semantic

## Suggested outcome
- Extract smaller helpers so the function falls below the configured 80 LOC threshold.
- Preserve current behavior and public APIs.
- Add or update focused tests if the refactor exposes missing coverage seams.
`)}, "\x00"): []byte("https://github.com/owner/repo/issues/43\n"),
		},
	}

	oldNow := continuousRefactoringNow
	oldRunner := newContinuousRefactoringRunner
	continuousRefactoringNow = func() time.Time { return now }
	newContinuousRefactoringRunner = func() continuousrefactoring.CommandRunner { return runner }
	t.Cleanup(func() {
		continuousRefactoringNow = oldNow
		newContinuousRefactoringRunner = oldRunner
	})

	cmd := newContinuousRefactoringCmd()
	cmd.SetArgs([]string{
		"open-issues",
		"--manifest", manifestPath,
		"--output", outputPath,
		"--mode", "semantic",
	})

	output := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if strings.TrimSpace(output) != "Wrote "+outputPath {
		t.Fatalf("stdout = %q, want %q", output, "Wrote "+outputPath)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", outputPath, err)
	}
	var result continuousrefactoring.OpenResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(result.Created) != 2 {
		t.Fatalf("len(Created) = %d, want 2", len(result.Created))
	}
	if result.Created[0].URL != "https://github.com/owner/repo/issues/42" {
		t.Fatalf("Created[0].URL = %q, want issue URL", result.Created[0].URL)
	}
}

func writeContinuousRefactoringTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
