# xylem-intent-check

Two-LLM intent-check pipeline for detecting spec-intent drift in invariant docs
and property tests. Layer 5 of xylem's assurance hierarchy.

Implements the round-trip informalization pattern from Midspiral's claimcheck:
a back-translator describes what the code and test actually enforce (without
seeing the spec), then a diff-checker compares that description against the
invariant doc prose. Structural adversariality from using two separate models
catches gaps that single-model review misses.

Runs automatically after the `implement` phase in xylem workflows when a change
touches protected surfaces.

## Installation

```sh
cd cli && go install ./cmd/xylem-intent-check
```

## Usage

```
xylem-intent-check [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--repo-root <dir>` | `.` | Repository root directory |
| `--attestation-out <path>` | `<repo-root>/.xylem/intent-check-attestation.json` | Path to write attestation JSON |
| `--api-base-url <url>` | `https://api.anthropic.com/v1` | OpenAI-compatible API base URL |
| `--api-key <key>` | See env vars below | API key for the LLM provider |
| `--back-translate-model <model>` | `claude-opus-4-6` | Model for Phase 1 back-translation |
| `--diff-check-model <model>` | `claude-haiku-4-5-20251001` | Model for Phase 2 diff-check |

### Environment variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | API key fallback (lowest priority) |
| `LLM_API_KEY` | API key (takes precedence over `ANTHROPIC_API_KEY`) |
| `LLM_API_BASE_URL` | API base URL override (takes precedence over `--api-base-url` default) |

Flag values always win over environment variables.

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | No protected files changed (skipped), or pipeline completed and attestation written |
| `1` | Binary error: prompt files missing, LLM unreachable, git error. No attestation is written. |

Note: exit code 0 does not imply a passing verdict. The verdict (`"pass"` or
`"fail"`) is inside the attestation file. The binary intentionally does not
exit non-zero on a `"fail"` verdict so that callers (pre-commit hooks,
workflow phases) can decide how to handle it; the pre-commit hook reads the
verdict from the attestation.

## When it runs

The binary calls `git diff --name-only HEAD` and matches against:

```
docs/invariants/
cli/internal/*/invariants_prop_test.go
cli/internal/*/*_invariants_prop_test.go
```

If no staged files match, it exits 0 immediately without making any LLM calls.
When at least one file matches, the binary also resolves the counterpart for
each changed file so that both sides of each invariant-doc / property-test pair
are always present in the pipeline, even when only one side was modified.

## Pipeline

**Phase 1 — back-translation** (`--back-translate-model`)

The model sees the property test source and surrounding code, but never the
invariant doc prose. It describes in plain English what the code and test
actually guarantee: the observable promise, the boundary conditions the test
covers, and what the test does not cover.

Prompt template: `.xylem/prompts/intent-check/back_translate.md`

**Phase 2 — diff-check** (`--diff-check-model`)

The model sees both the invariant doc prose and the back-translation from
Phase 1. It determines whether the back-translation captures the essential
guarantee stated in the invariant, returning structured JSON:

```json
{"match": true, "mismatch_reason": ""}
```

Structured output (`response_format: json_schema`) is requested at the API
level for reliable parsing. The binary fails closed on parse errors.

Prompt template: `.xylem/prompts/intent-check/diff.md`

## Attestation

On completion (regardless of verdict), the binary writes an attestation file:

```
.xylem/intent-check-attestation.json
```

```json
{
  "protected_files": ["cli/internal/queue/invariants_prop_test.go", "docs/invariants/queue.md"],
  "content_hash": "<sha256 of concatenated file contents in sorted order>",
  "verdict": "pass",
  "checked_at": "2026-04-23T10:00:00Z",
  "pipeline_output": "{\"back_translation\": \"...\", \"diff_verdict\": \"match\", \"mismatch_reason\": \"\"}"
}
```

The `content_hash` is SHA-256 over the concatenated contents of the
`protected_files` list in sorted order. The `pipeline_output` field contains
the back-translation and diff-checker verdict as a JSON-encoded string.

The attestation must be staged alongside any protected-surface changes before
committing. The pre-commit hook reads and verifies it.

## Pre-commit hook

`scripts/check-intent-attestation.sh` enforces that a valid, current
attestation exists before any commit that touches protected surfaces.

The hook:
1. Collects staged files and filters against the protected-surface patterns.
2. If no protected files are staged, exits 0.
3. Reads `.xylem/intent-check-attestation.json` and checks:
   - The file exists.
   - `verdict` is `"pass"`.
   - The recomputed SHA-256 matches `content_hash`.
   - All staged protected files appear in `protected_files`.
4. On failure, emits:

```
intent-check attestation missing or stale. Run: xylem-intent-check — then re-stage .xylem/intent-check-attestation.json and retry the commit.
```

Register it in `.pre-commit-config.yaml`:

```yaml
- repo: local
  hooks:
    - id: intent-check-attestation
      name: intent-check attestation
      entry: bash scripts/check-intent-attestation.sh
      language: system
      pass_filenames: false
      stages: [pre-commit]
```

Requires `jq` and `python3` on the PATH.

## Kill criteria

The feature is tracked in `docs/assurance/next/07-intent-check-phase.md`.
False-positive rates are tracked per-run in `docs/assurance/next/07-fp-tracker.csv`.

The feature will be pulled if:
- FP rate exceeds 30% after 2 weeks of real-PR runs.
- The seeded mismatch fixture produces a false negative (fundamental plumbing
  failure).
- Latency exceeds 10 minutes per invocation after tuning.
