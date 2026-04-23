package intentcheck_test

// Invariant property tests for cli/internal/intentcheck.
// Spec: docs/invariants/intentcheck.md
//
// Every test function references its invariant via a comment:
//   // Invariant IN: <Name>

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/nicholls-inc/xylem/cli/internal/intentcheck"
)

// ---------------------------------------------------------------------------
// Generators
// ---------------------------------------------------------------------------

// drawModule returns a short module name from a small pool to trigger
// collisions between doc and test paths.
func drawModule(rt *rapid.T) string {
	return rapid.SampledFrom([]string{"queue", "scanner", "runner", "memory", "alpha"}).Draw(rt, "module")
}

// drawDocPath returns a random docs/invariants/<module>.md path.
func drawDocPath(rt *rapid.T) string {
	return "docs/invariants/" + drawModule(rt) + ".md"
}

// drawTestPath returns a random cli/internal/<module>/... test path using one
// of the two supported naming conventions.
func drawTestPath(rt *rapid.T) string {
	module := drawModule(rt)
	convention := rapid.IntRange(0, 1).Draw(rt, "convention")
	if convention == 0 {
		return fmt.Sprintf("cli/internal/%s/invariants_prop_test.go", module)
	}
	return fmt.Sprintf("cli/internal/%s/%s_invariants_prop_test.go", module, module)
}

// drawProtectedFile returns either a doc path or a test path.
func drawProtectedFile(rt *rapid.T) string {
	if rapid.Bool().Draw(rt, "is_doc") {
		return drawDocPath(rt)
	}
	return drawTestPath(rt)
}

// drawFileList returns a deduplicated, non-empty list of protected file paths.
func drawFileList(rt *rapid.T) []string {
	n := rapid.IntRange(1, 6).Draw(rt, "n")
	seen := make(map[string]bool)
	var files []string
	for i := 0; i < n; i++ {
		f := drawProtectedFile(rt)
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		files = append(files, "docs/invariants/queue.md")
	}
	return files
}

// populateFiles writes placeholder files for each path under root.
func populateFiles(t *testing.T, root string, paths []string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("placeholder"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// moduleOf extracts the module name from a protected file path.
func moduleOf(f string) string {
	if strings.HasPrefix(f, "docs/invariants/") && strings.HasSuffix(f, ".md") {
		return strings.TrimSuffix(strings.TrimPrefix(f, "docs/invariants/"), ".md")
	}
	parts := strings.SplitN(f, "/", 4)
	if len(parts) >= 3 && parts[0] == "cli" && parts[1] == "internal" {
		return parts[2]
	}
	return ""
}

// ---------------------------------------------------------------------------
// I1 — Completeness
// ---------------------------------------------------------------------------

// Invariant I1: every input file appears in the output of ResolveCounterparts.
func TestPropIntentcheck_I1_Completeness(t *testing.T) {
	// Invariant I1: Completeness
	rapid.Check(t, func(rt *rapid.T) {
		root := t.TempDir()
		files := drawFileList(rt)
		populateFiles(t, root, files)

		result := intentcheck.ResolveCounterparts(root, files)

		resultSet := make(map[string]bool, len(result))
		for _, r := range result {
			resultSet[r] = true
		}
		for _, f := range files {
			if !resultSet[f] {
				rt.Fatalf("I1 violated: input file %q missing from output %v", f, result)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// I2 — Counterpart coverage
// ---------------------------------------------------------------------------

// Invariant I2: if a counterpart file exists on disk, it appears in the output.
func TestPropIntentcheck_I2_CounterpartCoverage(t *testing.T) {
	// Invariant I2: CounterpartCoverage
	rapid.Check(t, func(rt *rapid.T) {
		root := t.TempDir()
		files := drawFileList(rt)
		populateFiles(t, root, files)

		// Populate one counterpart per input file on disk (convention 1 for test
		// files derived from docs; the doc path for test-derived inputs).
		for _, f := range files {
			module := moduleOf(f)
			if module == "" {
				continue
			}
			var cp string
			if strings.HasPrefix(f, "docs/invariants/") {
				cp = fmt.Sprintf("cli/internal/%s/%s_invariants_prop_test.go", module, module)
			} else {
				cp = "docs/invariants/" + module + ".md"
			}
			populateFiles(t, root, []string{cp})
		}

		result := intentcheck.ResolveCounterparts(root, files)
		resultSet := make(map[string]bool, len(result))
		for _, r := range result {
			resultSet[r] = true
		}

		// Verify using the same lookup logic as ResolveCounterparts (FindTestFile /
		// os.Stat) so we check the counterpart that was actually found, not a
		// hardcoded convention.
		for _, f := range files {
			module := moduleOf(f)
			if module == "" {
				continue
			}
			var expectedCP string
			if strings.HasPrefix(f, "docs/invariants/") {
				// Use FindTestFile — same as ResolveCounterparts does internally.
				expectedCP = intentcheck.FindTestFile(root, module)
			} else {
				docPath := "docs/invariants/" + module + ".md"
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(docPath))); err == nil {
					expectedCP = docPath
				}
			}
			if expectedCP != "" && !resultSet[expectedCP] {
				rt.Fatalf("I2 violated: counterpart %q exists on disk but absent from output %v (input: %q)", expectedCP, result, f)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// I3 — Deduplication
// ---------------------------------------------------------------------------

// Invariant I3: no file appears more than once in the output.
func TestPropIntentcheck_I3_Deduplication(t *testing.T) {
	// Invariant I3: Deduplication
	rapid.Check(t, func(rt *rapid.T) {
		root := t.TempDir()
		files := drawFileList(rt)
		populateFiles(t, root, files)

		result := intentcheck.ResolveCounterparts(root, files)

		seen := make(map[string]int)
		for _, r := range result {
			seen[r]++
		}
		for path, count := range seen {
			if count > 1 {
				rt.Fatalf("I3 violated: %q appears %d times in output %v", path, count, result)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// I4 — Sorted output
// ---------------------------------------------------------------------------

// Invariant I4: output is lexicographically sorted.
func TestPropIntentcheck_I4_Sorted(t *testing.T) {
	// Invariant I4: Sorted
	rapid.Check(t, func(rt *rapid.T) {
		root := t.TempDir()
		files := drawFileList(rt)
		populateFiles(t, root, files)

		result := intentcheck.ResolveCounterparts(root, files)

		if !sort.StringsAreSorted(result) {
			rt.Fatalf("I4 violated: output is not sorted: %v", result)
		}
	})
}

// ---------------------------------------------------------------------------
// I5 — Idempotence
// ---------------------------------------------------------------------------

// Invariant I5: applying ResolveCounterparts twice produces the same result as once.
func TestPropIntentcheck_I5_Idempotent(t *testing.T) {
	// Invariant I5: Idempotent
	rapid.Check(t, func(rt *rapid.T) {
		root := t.TempDir()
		files := drawFileList(rt)
		populateFiles(t, root, files)

		once := intentcheck.ResolveCounterparts(root, files)
		twice := intentcheck.ResolveCounterparts(root, once)

		if len(once) != len(twice) {
			rt.Fatalf("I5 violated: len mismatch — once=%v twice=%v", once, twice)
		}
		for i := range once {
			if once[i] != twice[i] {
				rt.Fatalf("I5 violated: index %d differs — once=%v twice=%v", i, once, twice)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// I6 — Fail-closed parsing
// ---------------------------------------------------------------------------

// Invariant I6: ParseDiffResult with invalid input returns an error and
// Match=false. The pipeline must never treat a parse failure as a pass.
func TestPropIntentcheck_I6_FailClosed(t *testing.T) {
	// Invariant I6: FailClosed
	rapid.Check(t, func(rt *rapid.T) {
		garbage := rapid.SampledFrom([]string{
			"",
			"not json",
			"[1,2,3]",
			"null",
			"true",
			"just some text from an LLM that refused to respond",
			"```\nnot json\n```",
		}).Draw(rt, "garbage")

		dr, err := intentcheck.ParseDiffResult(garbage)

		// If parsing errors out, Match must be false (zero value).
		if err != nil && dr.Match {
			rt.Fatalf("I6 violated: parse error but Match=true; input=%q err=%v", garbage, err)
		}
	})
}

// TestParseDiffResult_FailClosedOnNoJSON specifically verifies the fail-closed
// path for inputs containing no JSON block at all.
func TestParseDiffResult_FailClosedOnNoJSON(t *testing.T) {
	// Invariant I6: FailClosed
	inputs := []string{
		"",
		"no json here",
		"just text",
	}
	for _, input := range inputs {
		dr, err := intentcheck.ParseDiffResult(input)
		if err == nil {
			t.Errorf("expected error for input %q, got nil (dr=%+v)", input, dr)
			continue
		}
		if dr.Match {
			t.Errorf("I6 violated: Match=true on parse error for input %q", input)
		}
	}
}
