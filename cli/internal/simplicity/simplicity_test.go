package simplicity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct {
	outputs map[string][]byte
	errs    map[string]error
	calls   [][]string
}

func (r *stubRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	key := strings.Join(call, "\x00")
	if err, ok := r.errs[key]; ok {
		return nil, err
	}
	if out, ok := r.outputs[key]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unexpected call %q", key)
}

func TestScanRecentChangesDeduplicatesAndSorts(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot, "cli/cmd/xylem/root.go")
	writeTestFile(t, repoRoot, "README.md")
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)

	runner := &stubRunner{
		outputs: map[string][]byte{
			strings.Join([]string{
				"git", "-C", repoRoot, "log", "--name-only", "--pretty=format:", "--diff-filter=ACMRTUXB", "--since",
				now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), "HEAD",
			}, "\x00"): []byte("\nREADME.md\ncli/cmd/xylem/root.go\nREADME.md\nmissing.txt\n"),
		},
	}

	manifest, err := ScanRecentChanges(context.Background(), runner, repoRoot, now, 7)
	if err != nil {
		t.Fatalf("ScanRecentChanges() error = %v", err)
	}
	wantManifest := &ChangeManifest{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		RepoRoot:    filepath.Clean(repoRoot),
		WindowDays:  7,
		Since:       now.Add(-7 * 24 * time.Hour).Format(time.RFC3339),
		Files: []ChangedFile{
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

func TestBuildPlanFiltersAndCapsEntries(t *testing.T) {
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)
	simplifications := &FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings: []Finding{
			{
				ID:         "extract-helper",
				Kind:       "simplification",
				Title:      "refactor: extract shared helper for queue summaries",
				Summary:    "Extract a small helper used by the queue summary paths.",
				Paths:      []string{"cli/internal/queue/summary.go"},
				Confidence: 0.92,
			},
			{
				ID:         "low-confidence",
				Kind:       "simplification",
				Title:      "refactor: risky simplification",
				Summary:    "This should be filtered out.",
				Paths:      []string{"cli/internal/runner/runner.go"},
				Confidence: 0.4,
			},
		},
	}
	duplicates := &FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings: []Finding{
			{
				ID:             "duplicate-git-json",
				Kind:           "duplication",
				Title:          "refactor: consolidate duplicated GitHub JSON decoding",
				Summary:        "The same gh JSON decoding logic appears in multiple command helpers.",
				Paths:          []string{"cli/cmd/xylem/gap_report.go", "cli/cmd/xylem/lessons.go", "cli/cmd/xylem/continuous_simplicity.go"},
				Confidence:     0.95,
				DuplicateLines: 18,
				LocationCount:  3,
			},
			{
				ID:             "duplicate-tests",
				Kind:           "duplication",
				Title:          "refactor: deduplicate test-only helper",
				Summary:        "Should be dropped because it only hits test paths.",
				Paths:          []string{"cli/internal/queue/queue_test.go", "cli/internal/runner/runner_test.go", "cli/internal/source/scheduled_test.go"},
				Confidence:     0.99,
				DuplicateLines: 40,
				LocationCount:  3,
			},
			{
				ID:             "small-duplication",
				Kind:           "duplication",
				Title:          "refactor: small duplication",
				Summary:        "Too small to include.",
				Paths:          []string{"cli/internal/a.go", "cli/internal/b.go", "cli/internal/c.go"},
				Confidence:     0.9,
				DuplicateLines: 6,
				LocationCount:  3,
			},
		},
	}

	plan, err := BuildPlan("owner/repo", "main", PlanOptions{
		MaxPRs:                2,
		MinConfidence:         0.8,
		MinDuplicateLines:     10,
		MinDuplicateLocations: 3,
		ExcludeGlobs:          DefaultExcludeGlobs,
	}, simplifications, duplicates, now)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Selected) != 2 {
		t.Fatalf("len(Selected) = %d, want 2", len(plan.Selected))
	}
	if plan.Selected[0].ID != "duplicate-git-json" {
		t.Fatalf("Selected[0].ID = %q, want duplicate-git-json", plan.Selected[0].ID)
	}
	if plan.Selected[1].ID != "extract-helper" {
		t.Fatalf("Selected[1].ID = %q, want extract-helper", plan.Selected[1].ID)
	}
	if plan.Selected[0].Branch != "continuous-simplicity/01-duplicate-git-json" {
		t.Fatalf("Selected[0].Branch = %q", plan.Selected[0].Branch)
	}

	gotReasons := make(map[string]string, len(plan.Skipped))
	for _, skipped := range plan.Skipped {
		gotReasons[skipped.ID] = skipped.Reason
	}
	if gotReasons["low-confidence"] != "below min_confidence" {
		t.Fatalf("skip reason for low-confidence = %q", gotReasons["low-confidence"])
	}
	if gotReasons["duplicate-tests"] != "matches excluded path" {
		t.Fatalf("skip reason for duplicate-tests = %q", gotReasons["duplicate-tests"])
	}
	if gotReasons["small-duplication"] != "below min_duplicate_lines" {
		t.Fatalf("skip reason for small-duplication = %q", gotReasons["small-duplication"])
	}
}

func TestBuildPlanDeduplicatesIDsAcrossFindingFiles(t *testing.T) {
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)
	simplifications := &FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings: []Finding{{
			ID:         "shared-id",
			Kind:       "simplification",
			Title:      "refactor: lower-value simplification",
			Summary:    "This should lose to the stronger duplicate candidate.",
			Paths:      []string{"cli/internal/queue/summary.go"},
			Confidence: 0.85,
		}},
	}
	duplicates := &FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings: []Finding{{
			ID:             "shared-id",
			Kind:           "duplication",
			Title:          "refactor: consolidate shared helper",
			Summary:        "This should be retained because it scores higher.",
			Paths:          []string{"cli/cmd/xylem/gap_report.go", "cli/cmd/xylem/lessons.go", "cli/cmd/xylem/continuous_simplicity.go"},
			Confidence:     0.95,
			DuplicateLines: 18,
			LocationCount:  3,
		}},
	}

	plan, err := BuildPlan("owner/repo", "main", PlanOptions{
		MaxPRs:                3,
		MinConfidence:         0.8,
		MinDuplicateLines:     10,
		MinDuplicateLocations: 3,
		ExcludeGlobs:          DefaultExcludeGlobs,
	}, simplifications, duplicates, now)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Selected) != 1 {
		t.Fatalf("len(Selected) = %d, want 1", len(plan.Selected))
	}
	if plan.Selected[0].Kind != "duplication" {
		t.Fatalf("Selected[0].Kind = %q, want duplication", plan.Selected[0].Kind)
	}
	foundDuplicateSkip := false
	for _, skipped := range plan.Skipped {
		if skipped.ID == "shared-id" && skipped.Reason == "duplicate finding id" {
			foundDuplicateSkip = true
			break
		}
	}
	if !foundDuplicateSkip {
		t.Fatalf("Skipped = %#v, want duplicate finding id entry", plan.Skipped)
	}
}

func TestOpenPRsCreatesMissingAndSkipsExisting(t *testing.T) {
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)
	plan := &PRPlan{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Repo:        "owner/repo",
		BaseBranch:  "main",
		Options: PlanOptions{
			MaxPRs:                3,
			MinConfidence:         0.8,
			MinDuplicateLines:     10,
			MinDuplicateLocations: 3,
		},
		Selected: []PlannedPR{
			{
				ID:         "existing",
				Kind:       "simplification",
				Branch:     "continuous-simplicity/01-existing",
				Title:      "refactor: existing",
				Body:       "body",
				Summary:    "summary",
				Paths:      []string{"cli/internal/a.go"},
				Confidence: 0.9,
			},
			{
				ID:         "new",
				Kind:       "duplication",
				Branch:     "continuous-simplicity/02-new",
				Title:      "refactor: new",
				Body:       "body",
				Summary:    "summary",
				Paths:      []string{"cli/internal/b.go"},
				Confidence: 0.95,
			},
		},
	}
	runner := &stubRunner{
		outputs: map[string][]byte{
			strings.Join([]string{"gh", "pr", "list", "--repo", "owner/repo", "--head", "continuous-simplicity/01-existing", "--state", "open", "--json", "url", "--limit", "1"}, "\x00"):          []byte(`[{"url":"https://github.com/owner/repo/pull/1"}]`),
			strings.Join([]string{"gh", "pr", "list", "--repo", "owner/repo", "--head", "continuous-simplicity/02-new", "--state", "open", "--json", "url", "--limit", "1"}, "\x00"):               []byte(`[]`),
			strings.Join([]string{"gh", "pr", "create", "--repo", "owner/repo", "--head", "continuous-simplicity/02-new", "--base", "main", "--title", "refactor: new", "--body", "body"}, "\x00"): []byte("https://github.com/owner/repo/pull/2\n"),
		},
	}

	result, err := OpenPRs(context.Background(), runner, plan, now)
	if err != nil {
		t.Fatalf("OpenPRs() error = %v", err)
	}
	if len(result.Created) != 1 || result.Created[0].URL != "https://github.com/owner/repo/pull/2" {
		t.Fatalf("Created = %#v, want created PR #2", result.Created)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != "already open" {
		t.Fatalf("Skipped = %#v, want already-open skip", result.Skipped)
	}
}

func TestLoadersRoundTripJSON(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)
	findings := &FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings: []Finding{{
			ID:         "extract-helper",
			Kind:       "simplification",
			Title:      "refactor: extract helper",
			Summary:    "summary",
			Paths:      []string{"cli/internal/example.go"},
			Confidence: 0.9,
		}},
	}
	findingsPath := filepath.Join(dir, "findings.json")
	if err := WriteJSON(findingsPath, findings); err != nil {
		t.Fatalf("WriteJSON(findings) error = %v", err)
	}
	rawFindings, err := os.ReadFile(findingsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", findingsPath, err)
	}
	if !strings.HasSuffix(string(rawFindings), "\n") {
		t.Fatalf("findings json should end with newline: %q", string(rawFindings))
	}
	loadedFindings, err := LoadFindings(findingsPath)
	if err != nil {
		t.Fatalf("LoadFindings() error = %v", err)
	}
	if !reflect.DeepEqual(loadedFindings, findings) {
		t.Fatalf("loaded findings = %#v, want %#v", loadedFindings, findings)
	}

	plan := &PRPlan{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Repo:        "owner/repo",
		BaseBranch:  "main",
		Options: PlanOptions{
			MaxPRs:                1,
			MinConfidence:         0.8,
			MinDuplicateLines:     10,
			MinDuplicateLocations: 3,
		},
		Selected: []PlannedPR{{
			ID:         "extract-helper",
			Kind:       "simplification",
			Branch:     "continuous-simplicity/01-extract-helper",
			Title:      "refactor: extract helper",
			Body:       "body",
			Summary:    "summary",
			Paths:      []string{"cli/internal/example.go"},
			Confidence: 0.9,
		}},
	}
	planPath := filepath.Join(dir, "plan.json")
	if err := WriteJSON(planPath, plan); err != nil {
		t.Fatalf("WriteJSON(plan) error = %v", err)
	}
	rawPlan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", planPath, err)
	}
	if !strings.HasSuffix(string(rawPlan), "\n") {
		t.Fatalf("plan json should end with newline: %q", string(rawPlan))
	}
	loadedPlan, err := LoadPlan(planPath)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}
	if !reflect.DeepEqual(loadedPlan, plan) {
		t.Fatalf("loaded plan = %#v, want %#v", loadedPlan, plan)
	}
}

func TestSmoke_S2_ContinuousSimplicityPlanCapsReviewLoadAndFiltersLowValueDuplication(t *testing.T) {
	now := time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC)
	simplifications := &FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings: []Finding{
			{
				ID:         "extract-helper",
				Kind:       "simplification",
				Title:      "refactor: extract helper for queue summaries",
				Summary:    "Extract a shared helper for queue summary rendering.",
				Paths:      []string{"cli/internal/queue/summary.go", "cli/internal/queue/report.go"},
				Confidence: 0.91,
			},
			{
				ID:         "early-return",
				Kind:       "simplification",
				Title:      "refactor: simplify nested conditional in runner",
				Summary:    "Replace nested conditionals with an early return in the runner.",
				Paths:      []string{"cli/internal/runner/runner.go"},
				Confidence: 0.82,
			},
		},
	}
	duplicates := &FindingsFile{
		Version:     1,
		GeneratedAt: now.Format(time.RFC3339),
		Findings: []Finding{
			{
				ID:             "duplicate-gh-json",
				Kind:           "duplication",
				Title:          "refactor: consolidate duplicated gh JSON parsing",
				Summary:        "The same gh JSON parsing flow appears across multiple commands.",
				Paths:          []string{"cli/cmd/xylem/gap_report.go", "cli/cmd/xylem/lessons.go", "cli/cmd/xylem/continuous_simplicity.go"},
				Confidence:     0.95,
				DuplicateLines: 22,
				LocationCount:  3,
			},
			{
				ID:             "duplicate-queue-formatting",
				Kind:           "duplication",
				Title:          "refactor: extract repeated queue formatting helper",
				Summary:        "Several queue paths repeat the same formatting logic.",
				Paths:          []string{"cli/internal/queue/summary.go", "cli/internal/queue/queue.go", "cli/internal/queue/report.go"},
				Confidence:     0.9,
				DuplicateLines: 18,
				LocationCount:  3,
			},
			{
				ID:             "duplicate-test-only-helper",
				Kind:           "duplication",
				Title:          "refactor: deduplicate test helper",
				Summary:        "This should be skipped because it only touches test files.",
				Paths:          []string{"cli/internal/source/scheduled_test.go", "cli/internal/runner/runner_test.go", "cli/internal/queue/queue_test.go"},
				Confidence:     0.99,
				DuplicateLines: 40,
				LocationCount:  3,
			},
			{
				ID:             "duplicate-workflow-template",
				Kind:           "duplication",
				Title:          "refactor: deduplicate workflow prompt template",
				Summary:        "This should be skipped because prompt templates are excluded.",
				Paths:          []string{".xylem/prompts/continuous-simplicity/simplify.md", ".xylem/prompts/continuous-simplicity/dedup.md", ".xylem/prompts/implement-harness/smoke.md"},
				Confidence:     0.93,
				DuplicateLines: 14,
				LocationCount:  3,
			},
			{
				ID:             "small-duplication",
				Kind:           "duplication",
				Title:          "refactor: tiny duplicated guard",
				Summary:        "This should be skipped because it is below the configured size threshold.",
				Paths:          []string{"cli/internal/a.go", "cli/internal/b.go", "cli/internal/c.go"},
				Confidence:     0.88,
				DuplicateLines: 8,
				LocationCount:  3,
			},
		},
	}

	plan, err := BuildPlan("nicholls-inc/xylem", "main", PlanOptions{
		MaxPRs:                DefaultMaxPRs,
		MinConfidence:         DefaultMinConfidence,
		MinDuplicateLines:     DefaultMinDuplicateLines,
		MinDuplicateLocations: DefaultMinDuplicateTargets,
		ExcludeGlobs:          DefaultExcludeGlobs,
	}, simplifications, duplicates, now)
	require.NoError(t, err)
	require.Len(t, plan.Selected, 3)

	assert.Equal(t, []string{
		"duplicate-gh-json",
		"duplicate-queue-formatting",
		"extract-helper",
	}, []string{plan.Selected[0].ID, plan.Selected[1].ID, plan.Selected[2].ID})
	assert.Equal(t, []string{
		"continuous-simplicity/01-duplicate-gh-json",
		"continuous-simplicity/02-duplicate-queue-formatting",
		"continuous-simplicity/03-extract-helper",
	}, []string{plan.Selected[0].Branch, plan.Selected[1].Branch, plan.Selected[2].Branch})
	assert.Equal(t, "nicholls-inc/xylem", plan.Repo)
	assert.Equal(t, "main", plan.BaseBranch)
	assert.Equal(t, DefaultMaxPRs, plan.Options.MaxPRs)
	assert.Equal(t, "## Summary\n- Extract a shared helper for queue summary rendering.\n\n## Paths\n- `cli/internal/queue/report.go`\n- `cli/internal/queue/summary.go`", plan.Selected[2].Body)

	gotReasons := make(map[string]string, len(plan.Skipped))
	for _, skipped := range plan.Skipped {
		gotReasons[skipped.ID] = skipped.Reason
	}
	assert.Equal(t, "exceeds max_prs", gotReasons["early-return"])
	assert.Equal(t, "matches excluded path", gotReasons["duplicate-test-only-helper"])
	assert.Equal(t, "matches excluded path", gotReasons["duplicate-workflow-template"])
	assert.Equal(t, "below min_duplicate_lines", gotReasons["small-duplication"])
}

func writeTestFile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte("package test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
