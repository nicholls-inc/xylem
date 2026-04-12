# LLM routing with effort tiers and provider fallback

> **Status**: design locked, implementation split across issues #223 (config),
> #224 (workflow/vessel plumbing), and #225 (runner + exec). This document is
> the single source of truth for acceptance criteria and file-level guidance;
> issue bodies are summaries that defer to the sections below.

## Context

Today xylem invokes a single LLM per vessel (`claude` or `copilot`), picked via a 4-level hierarchy (`phase.llm > workflow.llm > source.llm > cfg.llm`) with a `DefaultModel` baked into each provider struct. The two providers live as separate `ClaudeConfig` / `CopilotConfig` blocks in `cli/internal/config/config.go:133-149`, and `runner.buildProviderPhaseArgs` (`cli/internal/runner/runner.go:3605`) dispatches on the hardcoded name. Rate‑limit retries (`runPhaseWithRateLimitRetry`, runner.go:3493) stay on the same provider forever, so a claude 429 or "Credit balance is too low" eventually fails the vessel even when copilot would have handled the work fine.

The goal is two-fold:

1. Let users classify vessels by **effort tier** (`high` / `med` / `low`) and map each tier to a specific model per provider.
2. Let the runner **route** a vessel to the first healthy provider in a per-tier preference list, and **fall back** to the next provider when the current one hits a provider-level auth/config/access or availability failure after preserving same-provider retries for credit/rate-limit errors.

Design decisions (per user, 2026-04-09):

- Tier comes from the workflow YAML **and** from `tasks.<name>.tier` in `.xylem.yml` (not GitHub labels).
- Retry the **same provider** on credit/rate-limit errors; fall back to the **next provider** on provider-auth, provider-config/access, or provider-unavailable failures.
- Provider preference is **per-tier** (`tiers.high.providers: [claude]`, `tiers.low.providers: [copilot, claude]`).
- Config moves to a **generic `providers:` map** so gemini (and any future CLI) plugs in without new structs.

## Target config shape

```yaml
providers:
  claude:
    kind: claude                 # arg-builder style (claude | copilot | future: gemini)
    command: "claude"
    flags: "--dangerously-skip-permissions"
    tiers:
      high: "claude-opus-4-6"
      med:  "claude-sonnet-4-6"
      low:  "claude-haiku-4-5"
    env:
      ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"

  copilot:
    kind: copilot
    command: "copilot"
    flags: "--yolo --autopilot"
    tiers:
      high: "gpt-5.4"
      med:  "gpt-5.2-codex"
      low:  "gpt-5-mini"
    env:
      GITHUB_TOKEN: "${COPILOT_GITHUB_TOKEN}"

llm_routing:
  default_tier: med
  tiers:
    high: { providers: [claude] }
    med:  { providers: [claude, copilot] }
    low:  { providers: [copilot, claude] }
```

Legacy top-level `claude:` / `copilot:` blocks keep working: `config.normalize()` rewrites them into `providers:` at load time, assigning `kind` from the block name and synthesizing a single-tier entry from the old `default_model`. If no `llm_routing` is present, the loader synthesizes a single-provider chain equivalent to today's `cfg.LLM`/`DefaultModel` behavior. Pure backward compat; no operator edit required on upgrade.

## Changes by file

### 1. `cli/internal/config/config.go`

- New types:
  ```go
  type ProviderConfig struct {
      Kind    string            `yaml:"kind"`          // "claude" | "copilot" (extensible)
      Command string            `yaml:"command"`
      Flags   string            `yaml:"flags,omitempty"`
      Tiers   map[string]string `yaml:"tiers,omitempty"`   // tier -> model
      Env     map[string]string `yaml:"env,omitempty"`
      AllowedTools []string     `yaml:"allowed_tools,omitempty"` // claude-kind only
  }

  type LLMRoutingConfig struct {
      DefaultTier string                    `yaml:"default_tier,omitempty"` // default "med"
      Tiers       map[string]TierRouting    `yaml:"tiers,omitempty"`
  }

  type TierRouting struct {
      Providers []string `yaml:"providers"`
  }
  ```
- Add `Providers map[string]ProviderConfig` and `LLMRouting LLMRoutingConfig` to `Config`.
- Keep `Claude ClaudeConfig` and `Copilot CopilotConfig` fields purely for parsing legacy YAML; migrate them in `normalize()`:
  - If `Providers` is empty but `Claude.Command != ""`, synthesize `Providers["claude"] = ProviderConfig{Kind: "claude", Command: ..., Flags: ..., Env: ..., AllowedTools: ..., Tiers: {cfg.LLMRouting.DefaultTier: Claude.DefaultModel}}`.
  - Same for copilot.
  - If `LLMRouting.Tiers` is empty, synthesize a single tier `{med: {providers: <ordered list of providers that were configured, with cfg.LLM first if set>}}` and set `DefaultTier=med`.
  - After migration, the rest of the runner only reads from `Providers` + `LLMRouting`; legacy structs become deprecated (but still parseable so old `.xylem.yml` files work).
- Add `Task.Tier string `yaml:"tier,omitempty"` — optional per-task default.
- Validation (`validate()` around config.go:640-663):
  - Every provider referenced in `llm_routing.tiers[*].providers` must exist in `providers:`.
  - Every provider's `tiers` map must contain an entry for every tier referenced in `llm_routing.tiers`.
  - Provider `kind` must be one of `claude`/`copilot` (future: `gemini`).
  - `default_tier` must exist in `llm_routing.tiers`.

### 2. `cli/internal/workflow/workflow.go`

- Add `Tier *string `yaml:"tier,omitempty"` to both `Workflow` (line 21) and `Phase` (line 32). Pointer so "unset" is distinguishable from "" (mirrors the existing `LLM`/`Model` pattern).
- No other changes — phase execution already accepts these fields opaquely.

### 3. `cli/internal/queue/queue.go`

- Add `Tier string `json:"tier,omitempty"` to `Vessel` (after `Workflow`, ~line 70). Stores the resolved tier at enqueue time so the runner doesn't have to re-resolve per retry.
- No state-machine changes.

### 4. `cli/internal/scanner/*` and `cli/internal/source/*`

- At enqueue time in the GitHub and Manual sources, stamp `vessel.Tier` from `task.Tier` (falling back to `cfg.LLMRouting.DefaultTier`, which is already `"med"` after normalize).
- Grep for where `queue.Vessel{...}` literals are constructed in `internal/scanner`, `internal/source/github.go`, `internal/source/manual.go`, and the test fixtures; add the `Tier` field.

### 5. `cli/internal/runner/runner.go`

Replace the current provider/model resolution block (runner.go:3668-3709) with tier-aware routing.

- **New `resolveTier`** — hierarchy `Phase.Tier > Workflow.Tier > Vessel.Tier > cfg.LLMRouting.DefaultTier > "med"`.
- **New `resolveProviderChain(cfg, tier) []string`** — returns the ordered list from `cfg.LLMRouting.Tiers[tier].Providers`. If empty (pure legacy config post-normalize), falls back to the single synthesized provider.
- **New `modelForProvider(cfg, providerName, tier) string`** — returns `cfg.Providers[providerName].Tiers[tier]`.
- **`buildProviderPhaseArgs` (runner.go:3605)** dispatches on `cfg.Providers[name].Kind` instead of a hardcoded switch. `buildPhaseArgs` and `buildCopilotPhaseArgs` are refactored to take a `ProviderConfig` (not the top-level `cfg.Claude` / `cfg.Copilot`), so the arg builders no longer reach into removed fields. Model resolution inside the builders uses `modelForProvider` with the resolved tier.
- **New `runPhaseWithProviderFallback`** wraps `runPhaseWithRateLimitRetry` (runner.go:3488-3517):
  - Iterates `resolveProviderChain(cfg, tier)`.
  - For each provider: build args + env, invoke `runPhaseWithRateLimitRetry` as today.
  - If the final error is still classified as a same-provider retryable rate-limit/quota error after retries are exhausted **and** there is a next provider, log the fallback and continue.
  - If the final error is classified as a provider-auth/config/access or provider-unavailable failure **and** there is a next provider, fall through to the next provider immediately without same-provider backoff.
  - Any other non-provider-routing error returns immediately (preserves today's fail-fast semantics for real bugs).
  - On the last provider, propagate whatever error comes back.
- Callers of `runPhaseWithRateLimitRetry` inside the phase execution path (around runner.go:735) switch to the new wrapper and pass the resolved tier.

### 6. `cli/cmd/xylem/exec.go`

Today `realCmdRunner.extraEnv` is populated at startup from **both** `claude.env` and `copilot.env` merged into one slice (exec.go:98-117). That's fine for a single active provider but leaks `COPILOT_GITHUB_TOKEN` into claude subprocesses once we start switching per-call.

- Keep `newCmdRunner` building a per-provider env map: `providerEnv map[string][]string` keyed by provider name, populated from `cfg.Providers[name].Env`.
- Add `RunPhaseWithEnv(ctx, dir, extraEnv []string, stdin io.Reader, name string, args ...string) ([]byte, error)` mirroring the existing `RunProcessWithEnv` (exec.go:155). Merges `os.Environ()` + `extraEnv` (provider‑specific) so each LLM call sees only its own env.
- Extend the `Runner` interface in `internal/runner` to include the new method. The test stub `fakeCommandRunner` gets a matching method.
- Phase execution calls `RunPhaseWithEnv` with the resolved provider's env slice; `runPhaseWithRateLimitRetry` takes the env slice as an additional parameter.

### 7. Tests

- `internal/config/config_test.go`: new tests for
  - legacy `claude:` + `copilot:` blocks normalize into `providers:` and a synthesized `llm_routing`.
  - new `providers:` + `llm_routing:` round-trips without migration.
  - validation rejects: unknown provider in tier chain, missing tier model, unknown kind, bad default_tier.
- `internal/runner/runner_test.go`: tier/provider/model resolution table tests covering every level of the hierarchy, including legacy-shape configs.
- New `runner_fallback_test.go`: stub `Runner` that returns a credit-balance error for provider A on the first call and success for provider B; assert the second call uses provider B's command, args (including the tier→model mapping), and env.
- `internal/workflow/workflow_test.go`: parse a workflow with `tier: high` and assert it surfaces on `Workflow.Tier` / `Phase.Tier`.
- Update `internal/queue` fixtures that construct `Vessel` literals.

### 8. Docs

No user-visible doc file changes in this plan (CLAUDE.md already covers config surface at a high level; the new keys are self-describing in `config.go` and will be picked up by `xylem init` generation in a follow-up).

## Critical files to modify

- `cli/internal/config/config.go` — new types, `normalize()` migration, `validate()` rules
- `cli/internal/workflow/workflow.go:21-44` — `Tier` on `Workflow` and `Phase`
- `cli/internal/queue/queue.go:66-89` — `Tier` on `Vessel`
- `cli/internal/runner/runner.go:3460-3709` — resolution, fallback wrapper, arg-builder dispatch on `Kind`
- `cli/cmd/xylem/exec.go:72-183` — per-provider env, `RunPhaseWithEnv`
- `cli/internal/source/github.go`, `cli/internal/source/manual.go`, `cli/internal/scanner/*.go` — stamp `vessel.Tier`

## Reuse / do-not-duplicate

- `isRateLimitError` (`runner.go:3456-3486`) already matches claude 429, copilot quota, and "Credit balance is too low". Keep it as the **same-provider retry** trigger.
- `runPhaseWithRateLimitRetry` (`runner.go:3488-3517`) keeps per-provider retries with its existing exponential backoff; the new fallback wrapper composes it, not replaces it, and a classifier decides when the next provider should be tried.
- `resolveProvider` / `resolveModel` (`runner.go:3670-3709`) are deleted — their callers switch to `resolveTier` + `resolveProviderChain` + `modelForProvider`. The `Phase.LLM` / `Phase.Model` / `Workflow.LLM` / `Workflow.Model` fields remain for backward compat: if set and no `Tier` is set, normalize them into a one-off single-provider chain at runtime so we don't need a migration script.
- `stripModelFlag`, `stripBoolFlag`, `stripPromptFlag` utilities stay as-is and are called from the refactored kind-dispatched arg builders.
- `RunProcessWithEnv` (`exec.go:155`) is the model for the new `RunPhaseWithEnv` — same merge semantics.

## Verification

1. **Unit tests**: `cd cli && go test ./internal/config ./internal/workflow ./internal/queue ./internal/runner` — all four packages touched should pass, with new tests for legacy-normalization, tier resolution, fallback, and per-provider env isolation.
2. **Race detector**: `go test -race ./internal/runner` — fallback path touches goroutine-shared runner state, make sure there's no regression.
3. **Format check**: `goimports -l .` clean (CI parity).
4. **Backward-compat smoke**: take an unmodified `.xylem.yml` from a guest repo (only `claude:` block, no `providers:`) and run `xylem scan --dry-run` + unit tests for the config loader; assert the vessel uses claude with the old `default_model`, no behavior change.
5. **End-to-end fallback smoke** (manual, against local stub providers):
   - Write a `.xylem.yml` with two providers where `claude.command` is a shell stub that prints either `Error: Credit balance is too low` or an auth failure like `authentication failed: invalid x-api-key` to stderr and exits non-zero, and `copilot.command` is a stub that echoes a valid phase output to stdout.
   - Configure `llm_routing.tiers.med.providers: [claude, copilot]`.
   - Run `xylem drain` on a test vessel.
   - Assert in logs: rate-limit input retries on claude before falling back, auth-failure input skips same-provider backoff and falls through directly to copilot, and the successful phase uses copilot's env + model.
6. **Live observability**: with Jaeger running (`docker compose -f dev/docker-compose.yml up -d`), check that the phase span carries the final provider name (extend existing OTel span attributes in `cli/internal/observability` to include `llm.provider` and `llm.tier`).
