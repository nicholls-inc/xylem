package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/intentcheck"
)

// TestSeededMismatch verifies that the pipeline correctly identifies a mismatch
// when the back-translation says the test "only checks non-negativity" but the
// invariant doc requires "monotonically non-decreasing".
//
// The test uses mock LLM responses and does not make real API calls.
func TestSeededMismatch(t *testing.T) {
	// Load fixture files.
	fixtureDir := filepath.Join("testdata", "seeded-mismatch")

	invariantDoc, err := os.ReadFile(filepath.Join(fixtureDir, "invariant.md"))
	if err != nil {
		t.Fatalf("read invariant.md: %v", err)
	}
	testFile, err := os.ReadFile(filepath.Join(fixtureDir, "test.go"))
	if err != nil {
		t.Fatalf("read test.go: %v", err)
	}
	expectedBackTranslation, err := os.ReadFile(filepath.Join(fixtureDir, "expected_back_translation.txt"))
	if err != nil {
		t.Fatalf("read expected_back_translation.txt: %v", err)
	}

	// Verify fixture files are non-empty.
	if len(invariantDoc) == 0 {
		t.Fatal("invariant.md is empty")
	}
	if len(testFile) == 0 {
		t.Fatal("test.go is empty")
	}

	// The mock back-translation response — says "only checks non-negativity"
	// (intentionally does NOT mention monotonicity).
	mockBackTranslation := strings.TrimSpace(string(expectedBackTranslation))

	// The mock diff-checker response — identifies the mismatch.
	mockDiffResponse := `{"match": false, "mismatch_reason": "The invariant requires monotonically non-decreasing tally (I1), but the test only checks non-negativity. The monotonicity property is not verified."}`

	// Override runLLM with a model-dispatching mock. The schema parameter is
	// ignored in the mock — real schema enforcement happens at the API level.
	origRunLLM := runLLM
	defer func() { runLLM = origRunLLM }()

	runLLM = func(ctx context.Context, model, prompt string, schema json.RawMessage) (string, error) {
		switch model {
		case "claude-opus-4-6":
			// Back-translator: returns the canned back-translation.
			return mockBackTranslation, nil
		case "claude-haiku-4-5-20251001":
			// Diff-checker: returns the mismatch JSON.
			return mockDiffResponse, nil
		default:
			t.Errorf("unexpected model: %s", model)
			return "", nil
		}
	}

	// Build a fake repo layout in a temp dir.
	repoRoot := t.TempDir()

	// Create the protected file structure the pipeline expects.
	invariantPath := filepath.Join(repoRoot, "docs", "invariants", "tally.md")
	testPath := filepath.Join(repoRoot, "cli", "internal", "tally", "tally_invariants_prop_test.go")
	promptDir := filepath.Join(repoRoot, ".xylem", "prompts", "intent-check")

	for _, dir := range []string{
		filepath.Dir(invariantPath),
		filepath.Dir(testPath),
		promptDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	if err := os.WriteFile(invariantPath, invariantDoc, 0o644); err != nil {
		t.Fatalf("write invariant.md: %v", err)
	}
	if err := os.WriteFile(testPath, testFile, 0o644); err != nil {
		t.Fatalf("write test.go: %v", err)
	}

	// Write minimal prompt templates (content does not matter since LLM is mocked).
	for _, name := range []string{"back_translate.md", "diff.md"} {
		p := filepath.Join(promptDir, name)
		if err := os.WriteFile(p, []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write prompt %s: %v", name, err)
		}
	}

	// Write the attestation to a temp file.
	attestOut := filepath.Join(t.TempDir(), "attestation.json")

	// Run the pipeline directly (bypassing git discovery).
	files := []string{
		"docs/invariants/tally.md",
		"cli/internal/tally/tally_invariants_prop_test.go",
	}

	// Phase 1.
	backTranslateTmpl, _ := os.ReadFile(filepath.Join(promptDir, "back_translate.md"))
	backTranslation, err := runBackTranslator(context.Background(), repoRoot, files, string(backTranslateTmpl), "claude-opus-4-6")
	if err != nil {
		t.Fatalf("runBackTranslator: %v", err)
	}
	if !strings.Contains(backTranslation, "non-negative") {
		t.Errorf("back-translation should mention non-negativity; got: %s", backTranslation)
	}

	// Phase 2.
	diffTmpl, _ := os.ReadFile(filepath.Join(promptDir, "diff.md"))
	verdictResult, _, err := runDiffChecker(context.Background(), repoRoot, files, backTranslation, string(diffTmpl), "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("runDiffChecker: %v", err)
	}

	// The pipeline must detect a mismatch.
	if verdictResult.Match {
		t.Errorf("expected mismatch verdict, got match=true")
	}
	if verdictResult.MismatchReason == "" {
		t.Errorf("expected non-empty mismatch_reason")
	}
	if !strings.Contains(verdictResult.MismatchReason, "monoton") {
		t.Errorf("mismatch_reason should mention monotonicity; got: %s", verdictResult.MismatchReason)
	}

	// Write attestation and verify it has verdict=fail.
	contentHash, err := intentcheck.ComputeContentHash(repoRoot, files)
	if err != nil {
		t.Fatalf("computeContentHash: %v", err)
	}

	pipelineOutput, _ := json.Marshal(map[string]string{
		"back_translation": backTranslation,
		"diff_verdict":     "mismatch",
		"mismatch_reason":  verdictResult.MismatchReason,
	})

	a := attestation{
		ProtectedFiles: files,
		ContentHash:    contentHash,
		Verdict:        "fail",
		CheckedAt:      "2026-04-21T10:00:00Z",
		PipelineOutput: string(pipelineOutput),
	}
	if err := writeAttestation(attestOut, a); err != nil {
		t.Fatalf("writeAttestation: %v", err)
	}

	// Read back and verify.
	data, err := os.ReadFile(attestOut)
	if err != nil {
		t.Fatalf("read attestation: %v", err)
	}
	var readBack attestation
	if err := json.Unmarshal(data, &readBack); err != nil {
		t.Fatalf("unmarshal attestation: %v", err)
	}
	if readBack.Verdict != "fail" {
		t.Errorf("attestation verdict: got %q, want %q", readBack.Verdict, "fail")
	}
	if readBack.ContentHash == "" {
		t.Errorf("attestation content_hash is empty")
	}
	if len(readBack.ProtectedFiles) != 2 {
		t.Errorf("attestation protected_files: got %d, want 2", len(readBack.ProtectedFiles))
	}
}

// TestParseDiffResult verifies JSON extraction from LLM output with various
// formatting patterns (markdown fences, preamble text). Delegates to the
// intentcheck package's ParseDiffResult.
func TestParseDiffResult(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantMatch bool
		wantErr   bool
	}{
		{
			name:      "bare JSON",
			input:     `{"match": true, "mismatch_reason": ""}`,
			wantMatch: true,
		},
		{
			name:      "JSON with preamble",
			input:     "Here is the result:\n\n{\"match\": false, \"mismatch_reason\": \"monotonicity not verified\"}",
			wantMatch: false,
		},
		{
			name:      "JSON in markdown fence",
			input:     "```json\n{\"match\": true, \"mismatch_reason\": \"\"}\n```",
			wantMatch: true,
		},
		{
			name:    "no JSON",
			input:   "I cannot determine the answer.",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dr, err := intentcheck.ParseDiffResult(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dr.Match != tc.wantMatch {
				t.Errorf("match: got %v, want %v", dr.Match, tc.wantMatch)
			}
		})
	}
}

// TestResolveCounterparts verifies that ResolveCounterparts adds the missing side
// of each invariant doc ↔ property test pair.
func TestResolveCounterparts(t *testing.T) {
	root := t.TempDir()

	// Create file layout: docs/invariants/queue.md and two test file naming conventions.
	queueDoc := filepath.Join(root, "docs", "invariants", "queue.md")
	queueTest := filepath.Join(root, "cli", "internal", "queue", "invariants_prop_test.go")
	runnerDoc := filepath.Join(root, "docs", "invariants", "runner.md")
	runnerTest := filepath.Join(root, "cli", "internal", "runner", "runner_invariants_prop_test.go")

	for _, p := range []string{queueDoc, queueTest, runnerDoc, runnerTest} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("placeholder"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("only_invariant_doc_changed", func(t *testing.T) {
		got := intentcheck.ResolveCounterparts(root, []string{"docs/invariants/queue.md"})
		want := []string{
			"cli/internal/queue/invariants_prop_test.go",
			"docs/invariants/queue.md",
		}
		if !equalStringSlices(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("only_test_changed", func(t *testing.T) {
		got := intentcheck.ResolveCounterparts(root, []string{"cli/internal/runner/runner_invariants_prop_test.go"})
		want := []string{
			"cli/internal/runner/runner_invariants_prop_test.go",
			"docs/invariants/runner.md",
		}
		if !equalStringSlices(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("both_changed_no_duplicate", func(t *testing.T) {
		in := []string{
			"cli/internal/queue/invariants_prop_test.go",
			"docs/invariants/queue.md",
		}
		got := intentcheck.ResolveCounterparts(root, in)
		if !equalStringSlices(got, in) {
			t.Errorf("got %v, want %v (no duplicates)", got, in)
		}
	})

	t.Run("no_counterpart_on_disk", func(t *testing.T) {
		// "ghost" module has no files on disk
		in := []string{"docs/invariants/ghost.md"}
		got := intentcheck.ResolveCounterparts(root, in)
		if !equalStringSlices(got, in) {
			t.Errorf("got %v, want unchanged %v", got, in)
		}
	})
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestComputeContentHash verifies deterministic hash computation.
func TestComputeContentHash(t *testing.T) {
	dir := t.TempDir()

	aPath := filepath.Join(dir, "a.md")
	bPath := filepath.Join(dir, "b.go")
	if err := os.WriteFile(aPath, []byte("content a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("content b"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := []string{"a.md", "b.go"} // already sorted

	h1, err := intentcheck.ComputeContentHash(dir, files)
	if err != nil {
		t.Fatalf("hash error: %v", err)
	}
	h2, err := intentcheck.ComputeContentHash(dir, files)
	if err != nil {
		t.Fatalf("hash error: %v", err)
	}
	if h1 != h2 {
		t.Errorf("hash is not deterministic: %s != %s", h1, h2)
	}
	if h1 == "" {
		t.Errorf("hash is empty")
	}
}
