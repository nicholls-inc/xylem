package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/simplicity"
)

type simplicityRunnerStub struct {
	outputs map[string][]byte
	calls   [][]string
}

func (r *simplicityRunnerStub) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	key := strings.Join(call, "\x00")
	out, ok := r.outputs[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return out, nil
}

func TestContinuousSimplicityScanChangesCommandWritesManifest(t *testing.T) {
	setupTestDeps(t)

	repoRoot := t.TempDir()
	writeCommandTestFile(t, repoRoot, "README.md")
	writeCommandTestFile(t, repoRoot, "cli/cmd/xylem/root.go")
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)
	runner := &simplicityRunnerStub{
		outputs: map[string][]byte{
			strings.Join([]string{
				"git", "-C", repoRoot, "log", "--name-only", "--pretty=format:", "--diff-filter=ACMRTUXB", "--since",
				now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), "HEAD",
			}, "\x00"): []byte("README.md\ncli/cmd/xylem/root.go\n"),
		},
	}

	oldNow := simplicityNow
	oldRunner := newSimplicityRunner
	simplicityNow = func() time.Time { return now }
	newSimplicityRunner = func() simplicity.CommandRunner { return runner }
	t.Cleanup(func() {
		simplicityNow = oldNow
		newSimplicityRunner = oldRunner
	})

	outputPath := filepath.Join(t.TempDir(), "manifest.json")
	cmd := newContinuousSimplicityCmd()
	cmd.SetArgs([]string{"scan-changes", "--repo-root", repoRoot, "--output", outputPath})

	output := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if strings.TrimSpace(output) != "Wrote "+outputPath {
		t.Fatalf("stdout = %q, want %q", output, "Wrote "+outputPath)
	}

	manifestData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", outputPath, err)
	}
	var manifest simplicity.ChangeManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	wantManifest := simplicity.ChangeManifest{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		RepoRoot:    filepath.Clean(repoRoot),
		WindowDays:  simplicity.DefaultWindowDays,
		Since:       now.Add(-7 * 24 * time.Hour).Format(time.RFC3339),
		Files: []simplicity.ChangedFile{
			{Path: "README.md"},
			{Path: "cli/cmd/xylem/root.go"},
		},
	}
	if !reflect.DeepEqual(manifest, wantManifest) {
		t.Fatalf("manifest = %#v, want %#v", manifest, wantManifest)
	}
	wantCalls := [][]string{{
		"git", "-C", repoRoot, "log", "--name-only", "--pretty=format:", "--diff-filter=ACMRTUXB", "--since",
		now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), "HEAD",
	}}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("runner calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestContinuousSimplicityPlanPRsCommandUsesConfigRepoDefaults(t *testing.T) {
	setupTestDeps(t)

	dir := t.TempDir()
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)
	simplificationsPath := filepath.Join(dir, "simplifications.json")
	duplicationsPath := filepath.Join(dir, "duplications.json")
	outputPath := filepath.Join(dir, "plan.json")

	writeJSONFixture(t, simplificationsPath, simplicity.FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings: []simplicity.Finding{{
			ID:         "extract-helper",
			Kind:       "simplification",
			Title:      "refactor: extract helper",
			Summary:    "summary",
			Paths:      []string{"cli/internal/example.go"},
			Confidence: 0.9,
		}},
	})
	writeJSONFixture(t, duplicationsPath, simplicity.FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings:    []simplicity.Finding{},
	})

	oldNow := simplicityNow
	simplicityNow = func() time.Time { return now }
	t.Cleanup(func() {
		simplicityNow = oldNow
	})

	cmd := newContinuousSimplicityCmd()
	cmd.SetArgs([]string{
		"plan-prs",
		"--simplifications", simplificationsPath,
		"--duplications", duplicationsPath,
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

	planData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", outputPath, err)
	}
	var plan simplicity.PRPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if plan.Repo != "owner/repo" {
		t.Fatalf("Repo = %q, want owner/repo", plan.Repo)
	}
	if plan.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want main", plan.BaseBranch)
	}
	if !reflect.DeepEqual(plan.Options.ExcludeGlobs, simplicity.DefaultExcludeGlobs) {
		t.Fatalf("ExcludeGlobs = %#v, want %#v", plan.Options.ExcludeGlobs, simplicity.DefaultExcludeGlobs)
	}
	if len(plan.Selected) != 1 {
		t.Fatalf("len(Selected) = %d, want 1", len(plan.Selected))
	}
	wantSelected := simplicity.PlannedPR{
		ID:         "extract-helper",
		Kind:       "simplification",
		Branch:     "continuous-simplicity/01-extract-helper",
		Title:      "refactor: extract helper",
		Body:       "## Summary\n- summary\n\n## Paths\n- `cli/internal/example.go`",
		Summary:    "summary",
		Paths:      []string{"cli/internal/example.go"},
		Confidence: 0.9,
	}
	if !reflect.DeepEqual(plan.Selected[0], wantSelected) {
		t.Fatalf("Selected[0] = %#v, want %#v", plan.Selected[0], wantSelected)
	}
	if len(plan.Skipped) != 0 {
		t.Fatalf("Skipped = %#v, want none", plan.Skipped)
	}
}

func TestContinuousSimplicityOpenPRsCommandWritesResult(t *testing.T) {
	setupTestDeps(t)

	dir := t.TempDir()
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)
	planPath := filepath.Join(dir, "plan.json")
	outputPath := filepath.Join(dir, "result.json")
	writeJSONFixture(t, planPath, simplicity.PRPlan{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Repo:        "owner/repo",
		BaseBranch:  "main",
		Options: simplicity.PlanOptions{
			MaxPRs:                1,
			MinConfidence:         0.8,
			MinDuplicateLines:     10,
			MinDuplicateLocations: 3,
		},
		Selected: []simplicity.PlannedPR{{
			ID:         "extract-helper",
			Kind:       "simplification",
			Branch:     "continuous-simplicity/01-extract-helper",
			Title:      "refactor: extract helper",
			Body:       "body",
			Summary:    "summary",
			Paths:      []string{"cli/internal/example.go"},
			Confidence: 0.9,
		}},
	})
	runner := &simplicityRunnerStub{
		outputs: map[string][]byte{
			strings.Join([]string{"gh", "pr", "list", "--repo", "owner/repo", "--head", "continuous-simplicity/01-extract-helper", "--state", "open", "--json", "url", "--limit", "1"}, "\x00"):                          []byte(`[]`),
			strings.Join([]string{"gh", "pr", "create", "--repo", "owner/repo", "--head", "continuous-simplicity/01-extract-helper", "--base", "main", "--title", "refactor: extract helper", "--body", "body"}, "\x00"): []byte("https://github.com/owner/repo/pull/5\n"),
		},
	}

	oldNow := simplicityNow
	oldRunner := newSimplicityRunner
	simplicityNow = func() time.Time { return now }
	newSimplicityRunner = func() simplicity.CommandRunner { return runner }
	t.Cleanup(func() {
		simplicityNow = oldNow
		newSimplicityRunner = oldRunner
	})

	cmd := newContinuousSimplicityCmd()
	cmd.SetArgs([]string{"open-prs", "--plan", planPath, "--output", outputPath})
	output := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if strings.TrimSpace(output) != "Wrote "+outputPath {
		t.Fatalf("stdout = %q, want %q", output, "Wrote "+outputPath)
	}

	resultData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", outputPath, err)
	}
	var result simplicity.OpenResult
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	wantResult := simplicity.OpenResult{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Repo:        "owner/repo",
		Created: []simplicity.OpenedPRResult{{
			ID:     "extract-helper",
			Branch: "continuous-simplicity/01-extract-helper",
			URL:    "https://github.com/owner/repo/pull/5",
		}},
	}
	if !reflect.DeepEqual(result, wantResult) {
		t.Fatalf("result = %#v, want %#v", result, wantResult)
	}
	wantCalls := [][]string{
		{"gh", "pr", "list", "--repo", "owner/repo", "--head", "continuous-simplicity/01-extract-helper", "--state", "open", "--json", "url", "--limit", "1"},
		{"gh", "pr", "create", "--repo", "owner/repo", "--head", "continuous-simplicity/01-extract-helper", "--base", "main", "--title", "refactor: extract helper", "--body", "body"},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("runner calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func writeCommandTestFile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
