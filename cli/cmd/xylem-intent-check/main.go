// xylem-intent-check runs a two-LLM intent-check pipeline against changed
// protected-surface files (invariant docs + property tests).
//
// Exit code policy:
//   - 0: no protected files changed (skipped), or pipeline completed and
//     attestation written (verdict may be "pass" or "fail")
//   - 1: binary error (prompt files missing, LLM unavailable, git error);
//     no attestation is written
//
// Usage:
//
//	xylem-intent-check [--repo-root <dir>] [--attestation-out <path>]
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// runClaude is the function used to invoke the claude binary. It is a
// package-level variable so tests can substitute a mock without shelling out.
var runClaude = func(ctx context.Context, model, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", model, "--max-turns", "1")
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// attestation is the JSON structure written to --attestation-out.
type attestation struct {
	ProtectedFiles []string `json:"protected_files"`
	ContentHash    string   `json:"content_hash"`
	Verdict        string   `json:"verdict"`
	CheckedAt      string   `json:"checked_at"`
	PipelineOutput string   `json:"pipeline_output"`
}

// diffResult is the JSON structure expected from the diff-checker LLM.
type diffResult struct {
	Match          bool   `json:"match"`
	MismatchReason string `json:"mismatch_reason"`
}

// jsonBlockRE matches the first {...} block in LLM output, tolerating markdown
// fences and preamble text.
var jsonBlockRE = regexp.MustCompile(`(?s)\{.*\}`)

func main() {
	repoRoot := flag.String("repo-root", ".", "repository root directory")
	attestOut := flag.String("attestation-out", "", "path to write attestation JSON (default: <repo-root>/.xylem/intent-check-attestation.json)")
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "intent-check: resolve repo-root: %v\n", err)
		os.Exit(1)
	}

	out := *attestOut
	if out == "" {
		out = filepath.Join(root, ".xylem", "intent-check-attestation.json")
	}

	ctx := context.Background()
	if err := run(ctx, root, out); err != nil {
		fmt.Fprintf(os.Stderr, "intent-check: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, repoRoot, attestOut string) error {
	// Discover changed protected files.
	changed, err := discoverChangedFiles(ctx, repoRoot)
	if err != nil {
		return fmt.Errorf("discover changed files: %w", err)
	}
	if len(changed) == 0 {
		fmt.Println("intent-check: no protected files changed — skipping")
		return nil
	}

	// Ensure both sides of each invariant doc ↔ property test pair are present,
	// even when only one side was modified.
	changed = resolveCounterparts(repoRoot, changed)

	// Load prompt templates.
	backTranslatePromptPath := filepath.Join(repoRoot, ".xylem", "prompts", "intent-check", "back_translate.md")
	diffPromptPath := filepath.Join(repoRoot, ".xylem", "prompts", "intent-check", "diff.md")

	backTranslateTmpl, err := os.ReadFile(backTranslatePromptPath)
	if err != nil {
		return fmt.Errorf("read back_translate prompt %s: %w", backTranslatePromptPath, err)
	}
	diffTmpl, err := os.ReadFile(diffPromptPath)
	if err != nil {
		return fmt.Errorf("read diff prompt %s: %w", diffPromptPath, err)
	}

	// Compute content hash over all changed files (sorted order).
	contentHash, err := computeContentHash(repoRoot, changed)
	if err != nil {
		return fmt.Errorf("compute content hash: %w", err)
	}

	// Phase 1 — back-translation: read source/test, describe what it guarantees.
	backTranslation, err := runBackTranslator(ctx, repoRoot, changed, string(backTranslateTmpl))
	if err != nil {
		return fmt.Errorf("back-translation phase: %w", err)
	}

	// Phase 2 — diff-check: compare back-translation against invariant docs.
	verdictResult, rawDiff, err := runDiffChecker(ctx, repoRoot, changed, backTranslation, string(diffTmpl))
	if err != nil {
		return fmt.Errorf("diff-check phase: %w", err)
	}

	verdict := "pass"
	if !verdictResult.Match {
		verdict = "fail"
	}

	diffVerdict := "mismatch"
	if verdictResult.Match {
		diffVerdict = "match"
	}
	pipelineOutput, _ := json.Marshal(map[string]string{
		"back_translation": backTranslation,
		"diff_verdict":     diffVerdict,
		"mismatch_reason":  verdictResult.MismatchReason,
	})
	_ = rawDiff // captured for debugging; not included in attestation

	a := attestation{
		ProtectedFiles: changed,
		ContentHash:    contentHash,
		Verdict:        verdict,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		PipelineOutput: string(pipelineOutput),
	}

	if err := writeAttestation(attestOut, a); err != nil {
		return fmt.Errorf("write attestation: %w", err)
	}

	if verdict == "fail" {
		fmt.Fprintf(os.Stderr, "intent-check: FAIL — %s\n", verdictResult.MismatchReason)
	} else {
		fmt.Println("intent-check: PASS")
	}
	return nil
}

// discoverChangedFiles runs git diff to find changed protected files.
func discoverChangedFiles(ctx context.Context, repoRoot string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "HEAD", "--",
		"docs/invariants/",
		"cli/internal/*/invariants_prop_test.go",
		"cli/internal/*/*_invariants_prop_test.go",
	)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		files = append(files, line)
	}
	sort.Strings(files)
	return files, nil
}

// resolveCounterparts augments the changed-file list so that both sides of each
// invariant doc ↔ property test pair are always present. This handles asymmetric
// changes (e.g. only the doc was edited, or only the test was edited): the pipeline
// needs the invariant prose for the diff-checker and the test code for the
// back-translator regardless of which side triggered the run.
//
// Path convention: docs/invariants/<module>.md ↔ cli/internal/<module>/*_invariants_prop_test.go
func resolveCounterparts(repoRoot string, files []string) []string {
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f] = true
	}

	var extra []string
	for _, f := range files {
		var candidate string
		if strings.HasPrefix(f, "docs/invariants/") && strings.HasSuffix(f, ".md") {
			module := strings.TrimSuffix(strings.TrimPrefix(f, "docs/invariants/"), ".md")
			candidate = findTestFile(repoRoot, module)
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

// findTestFile returns the relative path of the property test file for the given
// module, or "" if none exists on disk. Checks two naming conventions.
func findTestFile(repoRoot, module string) string {
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

// computeContentHash returns the SHA-256 of the concatenated contents of files
// (in sorted order, paths relative to repoRoot).
func computeContentHash(repoRoot string, files []string) (string, error) {
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

// runBackTranslator invokes the back-translation LLM phase.
// It feeds the source code and property tests (NOT invariant docs) to the LLM
// and asks it to describe what the code/tests actually guarantee.
func runBackTranslator(ctx context.Context, repoRoot string, files []string, promptTmpl string) (string, error) {
	var sb strings.Builder
	sb.WriteString(promptTmpl)
	sb.WriteString("\n\n---\n\n## Changed files\n\n")

	for _, f := range files {
		// Skip invariant doc files — back-translator must not see the spec.
		if strings.HasPrefix(f, "docs/invariants/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", f, err)
		}
		fmt.Fprintf(&sb, "### %s\n\n```go\n%s\n```\n\n", f, string(data))
	}

	return runClaude(ctx, "claude-opus-4-6", sb.String())
}

// runDiffChecker invokes the diff-check LLM phase.
// It receives both the invariant doc prose and the back-translation, and
// returns the parsed verdict plus the raw LLM output.
func runDiffChecker(ctx context.Context, repoRoot string, files []string, backTranslation, promptTmpl string) (diffResult, string, error) {
	var sb strings.Builder
	sb.WriteString(promptTmpl)
	sb.WriteString("\n\n---\n\n## Invariant specification\n\n")

	for _, f := range files {
		if !strings.HasPrefix(f, "docs/invariants/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			return diffResult{}, "", fmt.Errorf("read %s: %w", f, err)
		}
		fmt.Fprintf(&sb, "### %s\n\n%s\n\n", f, string(data))
	}

	sb.WriteString("## Back-translation output\n\n")
	sb.WriteString(backTranslation)
	sb.WriteString("\n\n---\n\nRespond with JSON only: {\"match\": <bool>, \"mismatch_reason\": \"<string>\"}\n")

	raw, err := runClaude(ctx, "claude-haiku-4-5-20251001", sb.String())
	if err != nil {
		return diffResult{}, raw, fmt.Errorf("claude haiku: %w", err)
	}

	dr, parseErr := parseDiffResult(raw)
	if parseErr != nil {
		// Fail closed: parse error means we cannot confirm match.
		return diffResult{Match: false, MismatchReason: fmt.Sprintf("parse error: %v; raw output: %s", parseErr, raw)}, raw, nil
	}
	return dr, raw, nil
}

// parseDiffResult extracts and parses the JSON verdict from LLM output.
// It tolerates markdown fences and preamble text.
func parseDiffResult(raw string) (diffResult, error) {
	match := jsonBlockRE.FindString(raw)
	if match == "" {
		return diffResult{}, fmt.Errorf("no JSON block found in output")
	}
	var dr diffResult
	if err := json.Unmarshal([]byte(match), &dr); err != nil {
		return diffResult{}, fmt.Errorf("unmarshal: %w", err)
	}
	return dr, nil
}

// writeAttestation marshals and writes the attestation to the given path,
// creating parent directories as needed.
func writeAttestation(path string, a attestation) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// Append newline for POSIX compliance.
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
