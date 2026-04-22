// Package intentcheck contains the pure algorithmic core of the xylem-intent-check
// pipeline. These functions are extracted from the binary so they can be covered
// by invariant property tests and formally reasoned about independently of I/O.
package intentcheck

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DiffResult is the structured verdict produced by the diff-check LLM phase.
type DiffResult struct {
	Match          bool   `json:"match"`
	MismatchReason string `json:"mismatch_reason"`
}

// JSONBlockRE matches the first {...} block in LLM output, tolerating markdown
// fences and preamble text.
var JSONBlockRE = regexp.MustCompile(`(?s)\{.*\}`)

// ResolveCounterparts augments the changed-file list so that both sides of each
// invariant doc ↔ property test pair are always present. This handles asymmetric
// changes (e.g. only the doc was edited, or only the test was edited): the pipeline
// needs the invariant prose for the diff-checker and the test code for the
// back-translator regardless of which side triggered the run.
//
// Path convention: docs/invariants/<module>.md ↔ cli/internal/<module>/*_invariants_prop_test.go
//
// Invariants (see docs/invariants/intentcheck.md):
//   - I1: every input file is present in the output
//   - I2: if a counterpart exists on disk, it is present in the output
//   - I3: no file appears more than once in the output
//   - I4: output is lexicographically sorted
//   - I5: idempotent — applying twice produces the same result
func ResolveCounterparts(repoRoot string, files []string) []string {
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f] = true
	}

	var extra []string
	for _, f := range files {
		var candidate string
		if strings.HasPrefix(f, "docs/invariants/") && strings.HasSuffix(f, ".md") {
			module := strings.TrimSuffix(strings.TrimPrefix(f, "docs/invariants/"), ".md")
			candidate = FindTestFile(repoRoot, module)
		} else {
			// cli/internal/<module>/... — extract module from path segment 2
			parts := strings.SplitN(f, "/", 4)
			if len(parts) >= 3 {
				doc := "docs/invariants/" + parts[2] + ".md"
				if _, err := os.Stat(filepath.Join(repoRoot, doc)); err == nil {
					candidate = doc
				}
			}
		}
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			extra = append(extra, candidate)
		}
	}

	result := make([]string, len(files)+len(extra))
	copy(result, files)
	copy(result[len(files):], extra)
	sort.Strings(result)
	return result
}

// FindTestFile returns the relative path of the property test file for the given
// module, or "" if none exists on disk. Checks two naming conventions.
func FindTestFile(repoRoot, module string) string {
	candidates := []string{
		filepath.Join("cli", "internal", module, "invariants_prop_test.go"),
		filepath.Join("cli", "internal", module, module+"_invariants_prop_test.go"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(repoRoot, c)); err == nil {
			return c
		}
	}
	return ""
}

// ComputeContentHash returns the SHA-256 of the concatenated contents of files
// (in sorted order, paths relative to repoRoot).
func ComputeContentHash(repoRoot string, files []string) (string, error) {
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", f, err)
		}
		if _, err := h.Write(data); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// ParseDiffResult extracts and parses the JSON verdict from LLM output.
// It tolerates markdown fences and preamble text.
//
// Invariant I6 (fail-closed): if parsing fails for any reason, the returned
// DiffResult has Match=false. The caller must treat a non-nil error as a
// definitive non-match (fail-safe, not fail-open).
func ParseDiffResult(raw string) (DiffResult, error) {
	match := JSONBlockRE.FindString(raw)
	if match == "" {
		return DiffResult{}, fmt.Errorf("no JSON block found in output")
	}
	var dr DiffResult
	if err := json.Unmarshal([]byte(match), &dr); err != nil {
		return DiffResult{}, fmt.Errorf("unmarshal: %w", err)
	}
	return dr, nil
}
