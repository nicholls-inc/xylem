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
//	                   [--api-base-url <url>] [--api-key <key>]
//	                   [--back-translate-model <model>] [--diff-check-model <model>]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/nicholls-inc/xylem/cli/internal/intentcheck"
)

// diffResultJSONSchema is the JSON Schema used to request structured output
// from the diff-check LLM phase. Enforcing the schema at the API level is
// more robust than relying solely on prompt-level instructions.
//
// Note: strict json_schema response_format is an OpenAI-extension. Anthropic's
// OpenAI-compat endpoint (/v1) supports it via the beta header, but if a
// provider returns an unsupported-feature error, remove the schema field or
// fall back to the prompt-only JSON instruction retained at the bottom of
// runDiffChecker's prompt.
var diffResultJSONSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "match": {"type": "boolean"},
    "mismatch_reason": {"type": "string"}
  },
  "required": ["match", "mismatch_reason"],
  "additionalProperties": false
}`)

// runLLM is the function used to invoke the LLM. It is a package-level
// variable so tests can substitute a mock without making real API calls.
//
// schema is optional: if non-nil, structured JSON output is requested via the
// provider's response_format/json_schema mechanism.
var runLLM = func(ctx context.Context, model, prompt string, schema json.RawMessage) (string, error) {
	// Replaced at startup by makeLLMRunner. This stub prevents nil-panic if
	// somehow called before initialisation (e.g. in unit tests that forget to
	// set the mock).
	return "", fmt.Errorf("runLLM not initialised — call makeLLMRunner first")
}

// attestation is the JSON structure written to --attestation-out.
type attestation struct {
	ProtectedFiles []string `json:"protected_files"`
	ContentHash    string   `json:"content_hash"`
	Verdict        string   `json:"verdict"`
	CheckedAt      string   `json:"checked_at"`
	PipelineOutput string   `json:"pipeline_output"`
}

// makeLLMRunner builds the real runLLM implementation backed by an
// OpenAI-compatible API client.
func makeLLMRunner(client *openai.Client) func(ctx context.Context, model, prompt string, schema json.RawMessage) (string, error) {
	return func(ctx context.Context, model, prompt string, schema json.RawMessage) (string, error) {
		req := openai.ChatCompletionRequest{
			Model: model,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: prompt},
			},
		}

		if schema != nil {
			req.ResponseFormat = &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
				JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
					Name:   "diff_result",
					Schema: schema,
					Strict: true,
				},
			}
		}

		resp, err := client.CreateChatCompletion(ctx, req)
		if err != nil {
			return "", fmt.Errorf("chat completion: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("empty response from LLM")
		}
		return resp.Choices[0].Message.Content, nil
	}
}

func main() {
	repoRoot := flag.String("repo-root", ".", "repository root directory")
	attestOut := flag.String("attestation-out", "", "path to write attestation JSON (default: <repo-root>/.xylem/intent-check-attestation.json)")
	apiBaseURL := flag.String("api-base-url", envOr("LLM_API_BASE_URL", "https://api.anthropic.com/v1"), "OpenAI-compatible API base URL")
	apiKey := flag.String("api-key", envOr("LLM_API_KEY", os.Getenv("ANTHROPIC_API_KEY")), "API key for the LLM provider")
	backTranslateModel := flag.String("back-translate-model", "claude-opus-4-6", "model for Phase 1 back-translation")
	diffCheckModel := flag.String("diff-check-model", "claude-haiku-4-5-20251001", "model for Phase 2 diff-check")
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

	cfg := openai.DefaultConfig(*apiKey)
	cfg.BaseURL = *apiBaseURL
	client := openai.NewClientWithConfig(cfg)
	runLLM = makeLLMRunner(client)

	ctx := context.Background()
	if err := run(ctx, root, out, *backTranslateModel, *diffCheckModel); err != nil {
		fmt.Fprintf(os.Stderr, "intent-check: %v\n", err)
		os.Exit(1)
	}
}

// envOr returns the value of the environment variable name, or fallback if unset.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func run(ctx context.Context, repoRoot, attestOut, backTranslateModel, diffCheckModel string) error {
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
	changed = intentcheck.ResolveCounterparts(repoRoot, changed)

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
	contentHash, err := intentcheck.ComputeContentHash(repoRoot, changed)
	if err != nil {
		return fmt.Errorf("compute content hash: %w", err)
	}

	// Phase 1 — back-translation: read source/test, describe what it guarantees.
	backTranslation, err := runBackTranslator(ctx, repoRoot, changed, string(backTranslateTmpl), backTranslateModel)
	if err != nil {
		return fmt.Errorf("back-translation phase: %w", err)
	}

	// Phase 2 — diff-check: compare back-translation against invariant docs.
	verdictResult, rawDiff, err := runDiffChecker(ctx, repoRoot, changed, backTranslation, string(diffTmpl), diffCheckModel)
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

// runBackTranslator invokes the back-translation LLM phase.
// It feeds the source code and property tests (NOT invariant docs) to the LLM
// and asks it to describe what the code/tests actually guarantee.
func runBackTranslator(ctx context.Context, repoRoot string, files []string, promptTmpl, model string) (string, error) {
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

	return runLLM(ctx, model, sb.String(), nil)
}

// runDiffChecker invokes the diff-check LLM phase.
// It receives both the invariant doc prose and the back-translation, and
// returns the parsed verdict plus the raw LLM output.
//
// Structured output (response_format: json_schema) is used to enforce the
// response format at the API level, in addition to the prompt-level hint.
func runDiffChecker(ctx context.Context, repoRoot string, files []string, backTranslation, promptTmpl, model string) (intentcheck.DiffResult, string, error) {
	var sb strings.Builder
	sb.WriteString(promptTmpl)
	sb.WriteString("\n\n---\n\n## Invariant specification\n\n")

	for _, f := range files {
		if !strings.HasPrefix(f, "docs/invariants/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			return intentcheck.DiffResult{}, "", fmt.Errorf("read %s: %w", f, err)
		}
		fmt.Fprintf(&sb, "### %s\n\n%s\n\n", f, string(data))
	}

	sb.WriteString("## Back-translation output\n\n")
	sb.WriteString(backTranslation)
	sb.WriteString("\n\n---\n\nRespond with JSON only: {\"match\": <bool>, \"mismatch_reason\": \"<string>\"}\n")

	raw, err := runLLM(ctx, model, sb.String(), diffResultJSONSchema)
	if err != nil {
		return intentcheck.DiffResult{}, raw, fmt.Errorf("LLM diff-check: %w", err)
	}

	dr, parseErr := intentcheck.ParseDiffResult(raw)
	if parseErr != nil {
		// Fail closed: parse error means we cannot confirm match.
		return intentcheck.DiffResult{Match: false, MismatchReason: fmt.Sprintf("parse error: %v; raw output: %s", parseErr, raw)}, raw, nil
	}
	return dr, raw, nil
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
