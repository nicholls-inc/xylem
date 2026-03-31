# Xylem Harness Implementation Spec

**Status:** Draft
**Date:** 2026-03-31
**Scope:** Workstreams 1, 3, 4, 5 from the [SoTA Harness Plan](../plans/sota-harness-plan.md)
**Inputs:** [SoTA Agent Harness Spec](sota-agent-harness-spec.md), [Harness Scorecard Report](../reports/harness-scorecard-report-2.md)

## 1. Purpose

This document provides implementation-level specifications for the first four workstreams of the xylem harness plan. Each section contains exact Go type definitions, config YAML schemas, integration pseudocode with file/line references, error handling, backwards compatibility rules, and test strategies — enough detail for isolated AI agents to implement without assistance.

### 1.1 Scope

| Workstream | Summary | Scorecard gaps addressed |
|------------|---------|--------------------------|
| WS1 | Protect control surfaces and mediate high-risk actions | 8.3 (0/2), 8.5 (1/2) |
| WS3 | Wire run-level observability and cost telemetry into the CLI path | 7.1 (1/2), 10.1 (1/2) |
| WS4 | Formalize verification contracts and evidence levels | 6.2 (1/2), 6.4 (1/2) |
| WS5 | Harness eval suite interface (types and schemas only) | 7.2-7.4 (0/2 each) |

**Out of scope:** WS2 (OS-level containment, egress policy, secret scoping) — deferred until WS1 and WS3 are measured. WS6-8 (context compilation, evaluator separation, agent-readable observability) — deferred until Phase 1 is complete.

### 1.2 Technical decisions

These decisions were resolved during planning and are **not open for re-evaluation** during implementation.

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | SHA256 hash verification of protected files before/after each phase | Simpler than immutable copies, works on all platforms, no extra disk usage |
| 2 | Use `intermediary.Evaluate()` directly in runner, not `Submit()` | The runner IS the executor — Submit wraps evaluate+execute+audit, but the phase execution is the runner's own code. Manual audit append preserves the trail without circular indirection |
| 3 | `require_approval` treated as deny in v1 | True approval workflows need an external mechanism (label gate, webhook). Vessel failure message explains the limitation |
| 4 | Estimate tokens from prompt/output byte lengths (len/4) with static pricing table | Provider CLIs do not expose token counts via a parseable sideband. Estimates are consistent and sufficient for budget enforcement and cost comparison |
| 5 | Span hierarchy: `drain_run > vessel > phase > provider_call/gate`; tracer as struct field on Runner | Matches existing pattern where Config/Queue/Reporter are struct fields. Nil tracer = no tracing |
| 6 | WS2 deferred | OS-level containment is platform-specific and adds complexity disproportionate to current risk. WS1 provides detection + vessel failure, which raises scorecard 8.3 from 0 to at least 1. WS2 can be layered later without reworking WS1 |
| 7 | New `evidence` package with `Level` type; gates optionally declare evidence metadata | Gates without metadata produce `Untyped` claims — total backwards compatibility |
| 8 | Eval suite: Harbor-native, no custom runner | Scenarios are Harbor task directories with pytest verification scripts. Use `harbor run` for execution, `harbor analyze` for quality grading. No Go eval types or `xylem eval` CLI. Agents create the directory structure, helpers, and templates — NOT scenario content |
| 9 | All new config fields optional with safe defaults | Missing sections activate defaults. Zero existing config changes required |
| 10 | New `cli/internal/surface/` package for file integrity verification | Small, focused, no external dependencies beyond stdlib |
| 11 | New `cli/internal/evidence/` package for verification evidence types | Manifest persisted alongside phase outputs |

### 1.3 Notation

- **Line references** are to the codebase as of 2026-03-31 and may drift. Use function/type names as the stable anchor.
- **Go types** are shown with YAML tags. Fields with `omitempty` are optional.
- **Pseudocode** uses `→` for "then" and indentation for nesting. It is not Go syntax.

---

## 2. Config schema extensions

All new config sections are added to the top-level `Config` struct in `cli/internal/config/config.go`. All are optional with safe defaults.

### 2.1 Go type definitions

```go
// Add to Config struct (config.go, after Daemon field):
type Config struct {
    // ... existing fields unchanged ...
    Harness       HarnessConfig       `yaml:"harness,omitempty"`
    Observability ObservabilityConfig `yaml:"observability,omitempty"`
    Cost          CostConfig          `yaml:"cost,omitempty"`
}

// HarnessConfig holds security and policy enforcement settings.
type HarnessConfig struct {
    ProtectedSurfaces ProtectedSurfacesConfig `yaml:"protected_surfaces,omitempty"`
    Policy            PolicyConfig             `yaml:"policy,omitempty"`
    AuditLog          string                   `yaml:"audit_log,omitempty"`
}

// ProtectedSurfacesConfig defines which files are immutable during autonomous execution.
type ProtectedSurfacesConfig struct {
    // Paths is a list of filepath.Glob-compatible patterns relative to the worktree root.
    // Empty activates defaults. ["none"] disables protection entirely.
    Paths []string `yaml:"paths,omitempty"`
}

// PolicyConfig defines intermediary policy rules.
type PolicyConfig struct {
    Rules []PolicyRuleConfig `yaml:"rules,omitempty"`
}

// PolicyRuleConfig maps to intermediary.Rule.
type PolicyRuleConfig struct {
    Action   string `yaml:"action"`   // glob pattern
    Resource string `yaml:"resource"` // glob pattern
    Effect   string `yaml:"effect"`   // "allow", "deny", "require_approval"
}

// ObservabilityConfig controls tracing behavior.
type ObservabilityConfig struct {
    Enabled    *bool   `yaml:"enabled,omitempty"`     // default: true
    Endpoint   string  `yaml:"endpoint,omitempty"`     // OTLP gRPC endpoint; empty = stdout
    Insecure   bool    `yaml:"insecure,omitempty"`     // disable TLS for OTLP
    SampleRate float64 `yaml:"sample_rate,omitempty"`  // default: 1.0
}

// CostConfig controls budget enforcement.
type CostConfig struct {
    Budget *BudgetConfig `yaml:"budget,omitempty"`
}

// BudgetConfig defines per-vessel cost/token limits.
type BudgetConfig struct {
    MaxCostUSD float64 `yaml:"max_cost_usd,omitempty"` // 0 = no limit
    MaxTokens  int     `yaml:"max_tokens,omitempty"`    // 0 = no limit
}
```

### 2.2 Default constants and helpers

**New imports required in `config.go`:** `path/filepath`, `github.com/nicholls-inc/xylem/cli/internal/cost`, `github.com/nicholls-inc/xylem/cli/internal/intermediary`.

```go
var DefaultProtectedSurfaces = []string{
    ".xylem/HARNESS.md",
    ".xylem.yml",
    ".xylem/workflows/*.yaml",
    ".xylem/prompts/*/*.md",
}

const DefaultAuditLogPath = "audit.jsonl" // relative to StateDir

func (c *Config) EffectiveProtectedSurfaces() []string {
    if len(c.Harness.ProtectedSurfaces.Paths) == 0 {
        return DefaultProtectedSurfaces
    }
    if len(c.Harness.ProtectedSurfaces.Paths) == 1 &&
        c.Harness.ProtectedSurfaces.Paths[0] == "none" {
        return nil
    }
    return c.Harness.ProtectedSurfaces.Paths
}

func (c *Config) EffectiveAuditLogPath() string {
    if c.Harness.AuditLog != "" {
        return c.Harness.AuditLog
    }
    return DefaultAuditLogPath
}

func (c *Config) ObservabilityEnabled() bool {
    if c.Observability.Enabled == nil {
        return true
    }
    return *c.Observability.Enabled
}

func (c *Config) ObservabilitySampleRate() float64 {
    if c.Observability.SampleRate <= 0 || c.Observability.SampleRate > 1.0 {
        return 1.0
    }
    return c.Observability.SampleRate
}

func (c *Config) VesselBudget() *cost.Budget {
    if c.Cost.Budget == nil {
        return nil
    }
    b := c.Cost.Budget
    if b.MaxCostUSD <= 0 && b.MaxTokens <= 0 {
        return nil
    }
    return &cost.Budget{
        TokenLimit:   b.MaxTokens,
        CostLimitUSD: b.MaxCostUSD,
    }
}
```

### 2.3 Default policy

```go
func (c *Config) BuildIntermediaryPolicies() []intermediary.Policy {
    if len(c.Harness.Policy.Rules) == 0 {
        return []intermediary.Policy{DefaultPolicy()}
    }
    rules := make([]intermediary.Rule, len(c.Harness.Policy.Rules))
    for i, r := range c.Harness.Policy.Rules {
        rules[i] = intermediary.Rule{
            Action:   r.Action,
            Resource: r.Resource,
            Effect:   intermediary.Effect(r.Effect),
        }
    }
    return []intermediary.Policy{{Name: "user", Rules: rules}}
}

func DefaultPolicy() intermediary.Policy {
    return intermediary.Policy{
        Name: "default",
        Rules: []intermediary.Rule{
            {Action: "file_write", Resource: ".xylem/HARNESS.md", Effect: intermediary.Deny},
            {Action: "file_write", Resource: ".xylem.yml", Effect: intermediary.Deny},
            {Action: "file_write", Resource: ".xylem/workflows/*", Effect: intermediary.Deny},
            {Action: "file_write", Resource: ".xylem/prompts/*", Effect: intermediary.Deny},
            {Action: "git_push", Resource: "*", Effect: intermediary.RequireApproval},
            {Action: "pr_create", Resource: "*", Effect: intermediary.RequireApproval},
            {Action: "*", Resource: "*", Effect: intermediary.Allow},
        },
    }
}
```

### 2.4 Validation

Add three new validation methods and call them from `Validate()` immediately before its final `return nil` (after all existing validation — source validation, gate validation, etc.):

```go
func (c *Config) validateHarness() error {
    for _, pattern := range c.Harness.ProtectedSurfaces.Paths {
        if pattern == "none" {
            continue
        }
        if _, err := filepath.Match(pattern, "test"); err != nil {
            return fmt.Errorf("harness.protected_surfaces.paths: invalid glob %q: %w", pattern, err)
        }
    }
    for i, rule := range c.Harness.Policy.Rules {
        if rule.Action == "" {
            return fmt.Errorf("harness.policy.rules[%d]: action is required", i)
        }
        if rule.Resource == "" {
            return fmt.Errorf("harness.policy.rules[%d]: resource is required", i)
        }
        switch intermediary.Effect(rule.Effect) {
        case intermediary.Allow, intermediary.Deny, intermediary.RequireApproval:
        default:
            return fmt.Errorf("harness.policy.rules[%d]: invalid effect %q (must be allow, deny, or require_approval)", i, rule.Effect)
        }
    }
    return nil
}

func (c *Config) validateObservability() error {
    if c.Observability.SampleRate != 0 {
        if c.Observability.SampleRate < 0 || c.Observability.SampleRate > 1.0 {
            return fmt.Errorf("observability.sample_rate must be in [0.0, 1.0]")
        }
    }
    return nil
}

func (c *Config) validateCost() error {
    if c.Cost.Budget != nil {
        if c.Cost.Budget.MaxCostUSD < 0 {
            return fmt.Errorf("cost.budget.max_cost_usd must be non-negative")
        }
        if c.Cost.Budget.MaxTokens < 0 {
            return fmt.Errorf("cost.budget.max_tokens must be non-negative")
        }
    }
    return nil
}
```

Call all three from `Validate()`:

```go
if err := c.validateHarness(); err != nil {
    return err
}
if err := c.validateObservability(); err != nil {
    return err
}
if err := c.validateCost(); err != nil {
    return err
}
```

### 2.5 YAML examples

**Full configuration:**

```yaml
harness:
  audit_log: ".xylem/audit.jsonl"
  protected_surfaces:
    paths:
      - ".xylem/HARNESS.md"
      - ".xylem.yml"
      - ".xylem/workflows/*.yaml"
      - ".xylem/prompts/*/*.md"
  policy:
    rules:
      - action: "file_write"
        resource: ".xylem/*"
        effect: "deny"
      - action: "git_push"
        resource: "*"
        effect: "require_approval"
      - action: "pr_create"
        resource: "*"
        effect: "require_approval"
      - action: "*"
        resource: "*"
        effect: "allow"

observability:
  enabled: true
  endpoint: "localhost:4317"
  insecure: true
  sample_rate: 1.0

cost:
  budget:
    max_cost_usd: 10.0
    max_tokens: 1000000
```

**Minimal (safe defaults, zero changes to existing configs):**

```yaml
# Existing .xylem.yml — no harness/observability/cost sections needed.
# Defaults: protected surfaces active, default policy, tracing to stdout, no budget limit.
```

**Disable tracing** (use `enabled: false`, not `sample_rate: 0`):

```yaml
observability:
  enabled: false
```

**Permissive override (no protection, no tracing):**

```yaml
harness:
  protected_surfaces:
    paths: ["none"]
  policy:
    rules:
      - action: "*"
        resource: "*"
        effect: "allow"
observability:
  enabled: false
```

### 2.6 Action vocabulary

| Action | When emitted | Resource value |
|--------|-------------|----------------|
| `file_write` | Protected surface mutation detected | Relative file path |
| `git_push` | Push phase in workflow | Branch name or `*` |
| `git_commit` | Commit phase in workflow | `*` |
| `pr_create` | PR creation phase | `owner/repo` |
| `pr_comment` | Issue/PR comment | `owner/repo#N` |
| `external_command` | Command-type phase execution | Phase name |
| `phase_execute` | Prompt-type phase execution | Phase name |

---

## 3. Protected surface verification

New package: `cli/internal/surface/`

### 3.1 Types

```go
package surface

// FileHash records the SHA256 digest of a single file.
type FileHash struct {
    Path string `json:"path"` // relative to worktree root
    Hash string `json:"hash"` // hex-encoded SHA256
}

// Snapshot is an ordered set of file hashes.
type Snapshot struct {
    Files []FileHash `json:"files"`
}

// Violation records a detected change to a protected file.
type Violation struct {
    Path   string `json:"path"`
    Before string `json:"before"` // hex hash, or "absent"
    After  string `json:"after"`  // hex hash, or "deleted"
}
```

### 3.2 Functions

**`TakeSnapshot(worktreeRoot string, patterns []string) (Snapshot, error)`**

For each pattern, calls `filepath.Glob(filepath.Join(worktreeRoot, pattern))`. For each matching file, computes SHA256 via streaming read. Deduplicates by path. Returns files sorted by path.

- Missing files for a given pattern are silently skipped (the pattern may not match anything in this worktree).
- Returns error only for I/O failures on files that DO exist and match.

**Invariant:** The returned Snapshot is sorted by `Path` for deterministic comparison.

**`Compare(before, after Snapshot) []Violation`**

Builds maps from both snapshots. Reports:
- Hash changes (same path, different hash)
- Deletions (path in `before` but not in `after`)
- Creations (path in `after` but not in `before`)

Returns violations sorted by path. Empty slice means no violations.

**`hashFile(path string) (string, error)`** (unexported)

Opens file, streams through `sha256.New()`, returns lowercase hex-encoded digest.

### 3.3 Design note on `**` patterns

`filepath.Glob` does not support `**` (recursive). The default protected surface set uses single-level globs (`*.yaml`, `*/*.md`). Users who need deeper nesting add explicit patterns. This avoids introducing a dependency like `doublestar` into security-critical code.

---

## 4. Policy enforcement

### 4.1 Runner struct additions

Add to the `Runner` struct in `cli/internal/runner/runner.go` (after `Reporter` field):

```go
type Runner struct {
    Config       *config.Config
    Queue        *queue.Queue
    Worktree     WorktreeManager
    Runner       CommandRunner
    Sources      map[string]source.Source
    Reporter     *reporter.Reporter
    Intermediary *intermediary.Intermediary // nil = no policy enforcement
    AuditLog     *intermediary.AuditLog     // nil = no audit logging
    Tracer       *observability.Tracer      // nil = no tracing
}
```

### 4.2 CLI startup wiring

In `cli/cmd/xylem/drain.go`, in the `cmdDrain` function, after loading config and before calling `r.Drain()`:

```go
auditLogPath := filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath())
auditLog := intermediary.NewAuditLog(auditLogPath)
policies := cfg.BuildIntermediaryPolicies()
inter := intermediary.NewIntermediary(policies, auditLog, nil) // nil executor — we use Evaluate, not Submit

r := runner.New(cfg, q, wt, cmdRunner)
r.Intermediary = inter
r.AuditLog = auditLog
```

**Apply the same wiring in `cli/cmd/xylem/daemon.go`**, in the `runDrain` function (around line 159). The daemon currently creates a runner with only `Sources` set. After the existing `r.Sources = ...` line, add:

```go
auditLogPath := filepath.Join(cfg.StateDir, cfg.EffectiveAuditLogPath())
r.AuditLog = intermediary.NewAuditLog(auditLogPath)
policies := cfg.BuildIntermediaryPolicies()
r.Intermediary = intermediary.NewIntermediary(policies, r.AuditLog, nil)
// Tracer wiring also goes here — see section 5.4
```

Both paths must wire Intermediary, AuditLog, and Tracer. The daemon's `runDrain` should also set `Reporter` if it doesn't already (check current state — as of 2026-03-31 it does not).

### 4.3 Integration into phase execution

Policy enforcement and surface verification wrap each phase in the sequential loop (`runVessel`, starting at line 253) and the orchestrated loop (`runSinglePhase`, starting at line 732). The insertion pattern is identical in both paths.

**Before the inner gate-retry `for {` loop, in this order (matching section 10 error precedence):**

```
// 1. Policy check (short-circuits before any I/O)
if r.Intermediary != nil {
    intent := intermediary.Intent{
        Action:        phaseActionType(p),
        Resource:      p.Name,
        AgentID:       vessel.ID,
        Justification: fmt.Sprintf("executing phase %q of workflow %q", p.Name, sk.Name),
    }
    result := r.Intermediary.Evaluate(intent)
    if r.AuditLog != nil {
        _ = r.AuditLog.Append(intermediary.AuditEntry{
            Intent:    intent,
            Decision:  result.Effect,
            Timestamp: time.Now(),
        })
    }
    if result.Effect == intermediary.Deny {
        → fail vessel: "phase %q denied by policy: %s", p.Name, result.Reason
    }
    if result.Effect == intermediary.RequireApproval {
        → fail vessel: "phase %q requires approval (automatic approval not yet supported): %s", p.Name, result.Reason
    }
}

// 2. Surface pre-snapshot (only after policy allows execution)
protectedGlobs := r.Config.EffectiveProtectedSurfaces()
var preSnapshot surface.Snapshot
if len(protectedGlobs) > 0 {
    preSnapshot, err = surface.TakeSnapshot(worktreePath, protectedGlobs)
    if err != nil → fail vessel: "protected surface snapshot failed: %v"
}
```

**After phase output is written, before gate evaluation:**

```
if len(protectedGlobs) > 0 && runErr == nil {
    postSnapshot, snapErr := surface.TakeSnapshot(worktreePath, protectedGlobs)
    if snapErr != nil {
        log.Printf("warn: post-phase snapshot failed: %v", snapErr)
        // continue — snapshot failure is non-fatal
    } else {
        violations := surface.Compare(preSnapshot, postSnapshot)
        if len(violations) > 0 {
            if r.AuditLog != nil {
                for _, v := range violations {
                    _ = r.AuditLog.Append(intermediary.AuditEntry{
                        Intent: intermediary.Intent{
                            Action:   "file_write",
                            Resource: v.Path,
                            AgentID:  vessel.ID,
                        },
                        Decision:  intermediary.Deny,
                        Timestamp: time.Now(),
                    })
                }
            }
            → fail vessel: "phase %q violated protected surfaces: %s", p.Name, formatViolations(violations)
        }
    }
}
```

### 4.4 Helper

```go
func phaseActionType(p workflow.Phase) string {
    if p.Type == "command" {
        return "external_command"
    }
    return "phase_execute"
}

func formatViolations(violations []surface.Violation) string {
    var parts []string
    for _, v := range violations {
        parts = append(parts, fmt.Sprintf("%s (before: %s, after: %s)", v.Path, v.Before, v.After))
    }
    return strings.Join(parts, "; ")
}
```

---

## 5. Observability integration

### 5.1 Span hierarchy

```
drain_run                          (one per Drain() call)
  vessel:{vessel-id}               (one per vessel)
    phase:{phase-name}             (one per phase execution attempt)
      gate:{gate-type}             (one per gate evaluation, if gate exists)
```

Note: there is no separate `provider_call` span. The phase span covers the provider call, and provider-specific attributes (model, estimated tokens, estimated cost) are added to the phase span after the call returns. This avoids span proliferation.

### 5.2 Span attributes

**drain_run:**
- `xylem.drain.concurrency` = config concurrency (string)
- `xylem.drain.timeout` = config timeout string

**vessel:**
- `xylem.vessel.id` = vessel.ID
- `xylem.vessel.source` = vessel.Source
- `xylem.vessel.workflow` = vessel.Workflow
- `xylem.vessel.ref` = vessel.Ref (omitted if empty)

**phase:**
- `xylem.phase.name` = phase name
- `xylem.phase.index` = phase index (string)
- `xylem.phase.type` = "prompt" or "command"
- `xylem.phase.provider` = resolved provider
- `xylem.phase.model` = resolved model
- `xylem.phase.input_tokens_est` = estimated input tokens (string, added after execution)
- `xylem.phase.output_tokens_est` = estimated output tokens (string, added after execution)
- `xylem.phase.cost_usd_est` = estimated cost (string, added after execution)
- `xylem.phase.duration_ms` = phase duration in milliseconds (string, added after execution)

**gate:**
- `xylem.gate.type` = "command" or "label"
- `xylem.gate.passed` = "true" or "false"
- `xylem.gate.retry_attempt` = current retry number (string)

### 5.3 Attribute helper functions

New file: `cli/internal/observability/vessel.go`

```go
package observability

import "fmt"

func VesselSpanAttributes(id, source, workflow, ref string) []SpanAttribute {
    attrs := []SpanAttribute{
        {Key: "xylem.vessel.id", Value: id},
        {Key: "xylem.vessel.source", Value: source},
        {Key: "xylem.vessel.workflow", Value: workflow},
    }
    if ref != "" {
        attrs = append(attrs, SpanAttribute{Key: "xylem.vessel.ref", Value: ref})
    }
    return attrs
}

func PhaseSpanAttributes(name string, index int, phaseType, provider, model string) []SpanAttribute {
    return []SpanAttribute{
        {Key: "xylem.phase.name", Value: name},
        {Key: "xylem.phase.index", Value: fmt.Sprintf("%d", index)},
        {Key: "xylem.phase.type", Value: phaseType},
        {Key: "xylem.phase.provider", Value: provider},
        {Key: "xylem.phase.model", Value: model},
    }
}

func PhaseResultAttributes(inputTokensEst, outputTokensEst int, costUSDEst float64, durationMS int64) []SpanAttribute {
    return []SpanAttribute{
        {Key: "xylem.phase.input_tokens_est", Value: fmt.Sprintf("%d", inputTokensEst)},
        {Key: "xylem.phase.output_tokens_est", Value: fmt.Sprintf("%d", outputTokensEst)},
        {Key: "xylem.phase.cost_usd_est", Value: fmt.Sprintf("%.6f", costUSDEst)},
        {Key: "xylem.phase.duration_ms", Value: fmt.Sprintf("%d", durationMS)},
    }
}

func GateSpanAttributes(gateType string, passed bool, retryAttempt int) []SpanAttribute {
    return []SpanAttribute{
        {Key: "xylem.gate.type", Value: gateType},
        {Key: "xylem.gate.passed", Value: fmt.Sprintf("%t", passed)},
        {Key: "xylem.gate.retry_attempt", Value: fmt.Sprintf("%d", retryAttempt)},
    }
}
```

### 5.4 Tracer initialization

Initialize in both `cmdDrain` and the daemon's `runDrain`, after loading config, before creating runner:

```go
var tracer *observability.Tracer
if cfg.ObservabilityEnabled() {
    tracerCfg := observability.TracerConfig{
        ServiceName:    "xylem",
        ServiceVersion: "", // no version injection exists yet — use empty string
        Endpoint:       cfg.Observability.Endpoint,
        Insecure:       cfg.Observability.Insecure,
        SampleRate:     cfg.ObservabilitySampleRate(),
    }
    var err error
    tracer, err = observability.NewTracer(tracerCfg)
    if err != nil {
        log.Printf("warn: failed to initialize tracer: %v", err)
    }
}
if tracer != nil {
    defer tracer.Shutdown(context.Background())
}

r := runner.New(cfg, q, wt, cmdRunner)
r.Tracer = tracer
```

Apply the same tracer initialization in `daemon.go:runDrain`, after the intermediary wiring shown in section 4.2.

### 5.5 Span insertion in Drain()

```
func (r *Runner) Drain(ctx context.Context) (DrainResult, error) {
    // NEW: drain_run span
    if r.Tracer != nil {
        drainSpan := r.Tracer.StartSpan(ctx, "drain_run", []SpanAttribute{...})
        ctx = drainSpan.Context()
        defer drainSpan.End()
    }

    // ... existing timeout, sem, wg, mu ...

    for {
        // ... existing dequeue ...

        go func(j queue.Vessel) {
            // NEW: vessel span — created from drain ctx for parent-child linkage
            var vesselCtx context.Context
            var vesselSpan observability.SpanContext
            if r.Tracer != nil {
                vesselSpan = r.Tracer.StartSpan(ctx, "vessel:"+j.ID,
                    observability.VesselSpanAttributes(j.ID, j.Source, j.Workflow, j.Ref))
                defer vesselSpan.End()
                // Use span's trace context but independent timeout
                vesselCtx = vesselSpan.Context()
            } else {
                vesselCtx = context.Background()
            }
            vesselCtx, cancel := context.WithTimeout(vesselCtx, timeout)
            defer cancel()

            outcome := r.runVessel(vesselCtx, j)
            // ...
        }(*vessel)
    }
}
```

### 5.6 Span insertion in phase loop

Inside the gate-retry `for {` loop, wrapping each execution attempt:

```
phaseStart := time.Now()

// NEW: phase span
var phaseSpan observability.SpanContext
if r.Tracer != nil {
    provider := resolveProvider(r.Config, sk, &p)
    model := resolveModel(r.Config, sk, &p, provider)
    phaseType := p.Type
    if phaseType == "" { phaseType = "prompt" }
    phaseSpan = r.Tracer.StartSpan(ctx, "phase:"+p.Name,
        observability.PhaseSpanAttributes(p.Name, i, phaseType, provider, model))
}

// ... existing: render prompt, RunPhase/RunCommand ...

// NEW: add result attributes after execution
if r.Tracer != nil {
    durationMS := time.Since(phaseStart).Milliseconds()
    phaseSpan.AddAttributes(observability.PhaseResultAttributes(
        inputTokensEst, outputTokensEst, costEst, durationMS))
}

// ... existing: error handling, output persistence ...

// NEW: gate span (nested under phase)
if p.Gate != nil {
    var gateSpan observability.SpanContext
    if r.Tracer != nil {
        gateSpan = r.Tracer.StartSpan(phaseSpan.Context(), "gate:"+p.Gate.Type, nil)
    }
    // ... existing gate evaluation ...
    if r.Tracer != nil {
        gateSpan.AddAttributes(observability.GateSpanAttributes(p.Gate.Type, passed, retryAttempt))
        gateSpan.End()
    }
}

// NEW: end phase span before break/continue/return
if r.Tracer != nil {
    if runErr != nil { phaseSpan.RecordError(runErr) }
    phaseSpan.End()
}
```

---

## 6. Cost estimation and budget enforcement

### 6.1 Token estimation

New file: `cli/internal/cost/estimate.go`

```go
package cost

// EstimateTokens provides a rough token count using the len/4 heuristic.
// Matches ctxmgr.EstimateTokens for consistency.
func EstimateTokens(content string) int {
    return len(content) / 4
}

// ModelPricing holds per-token cost rates for a model.
type ModelPricing struct {
    InputPer1M  float64 // USD per 1M input tokens
    OutputPer1M float64 // USD per 1M output tokens
}

// EstimateCost computes approximate cost from token counts and pricing.
// Returns 0 if pricing is nil.
func EstimateCost(inputTokens, outputTokens int, pricing *ModelPricing) float64 {
    if pricing == nil {
        return 0
    }
    return float64(inputTokens)*pricing.InputPer1M/1_000_000 +
        float64(outputTokens)*pricing.OutputPer1M/1_000_000
}

// DefaultPricingTable maps model name prefixes to pricing.
// Unknown models get zero cost. Update when pricing changes.
var DefaultPricingTable = map[string]ModelPricing{
    "claude-sonnet-4":  {InputPer1M: 3.00, OutputPer1M: 15.00},
    "claude-opus-4":    {InputPer1M: 15.00, OutputPer1M: 75.00},
    "claude-haiku-4":   {InputPer1M: 0.80, OutputPer1M: 4.00},
    "claude-haiku-3":   {InputPer1M: 0.25, OutputPer1M: 1.25},
    "claude-sonnet-3":  {InputPer1M: 3.00, OutputPer1M: 15.00},
}

// LookupPricing finds pricing for a model name.
// Tries exact match, then longest prefix match.
// Returns nil if no match.
func LookupPricing(model string) *ModelPricing {
    if p, ok := DefaultPricingTable[model]; ok {
        return &p
    }
    var bestKey string
    for key := range DefaultPricingTable {
        if len(key) <= len(model) && model[:len(key)] == key && len(key) > len(bestKey) {
            bestKey = key
        }
    }
    if bestKey != "" {
        p := DefaultPricingTable[bestKey]
        return &p
    }
    return nil
}
```

### 6.2 Per-vessel cost tracking

A fresh `cost.Tracker` is created per vessel (not per drain) because the budget is per-vessel.

In `runVessel`, at the start:

```go
vesselTracker := cost.NewTracker(r.Config.VesselBudget())
```

After each prompt-type phase's `RunPhase` returns successfully. Note: `provider` and `model` are already resolved earlier in the prompt-type branch — `resolveProvider` is called before `buildProviderPhaseArgs` which calls `resolveModel`. Reuse those values:

```go
// provider and model were resolved earlier via resolveProvider/resolveModel
inputTokensEst := cost.EstimateTokens(rendered)
outputTokensEst := cost.EstimateTokens(string(output))
pricing := cost.LookupPricing(model)
costEst := cost.EstimateCost(inputTokensEst, outputTokensEst, pricing)

_ = vesselTracker.Record(cost.UsageRecord{
    MissionID:    vessel.ID,
    AgentRole:    cost.RoleGenerator,
    Purpose:      cost.PurposeReasoning,
    Model:        model,
    InputTokens:  inputTokensEst,
    OutputTokens: outputTokensEst,
    CostUSD:      costEst,
    Timestamp:    time.Now(),
})
```

Command-type phases do not generate cost records (no LLM invocation).

### 6.3 Budget enforcement

After each phase's cost is recorded, check the budget:

```go
if vesselTracker.BudgetExceeded() {
    errMsg := fmt.Sprintf("budget exceeded after phase %q: estimated cost $%.4f, estimated tokens %d",
        p.Name, vesselTracker.TotalCost(), vesselTracker.TotalTokens())
    → fail vessel with errMsg
}
```

Budget enforcement is per-vessel. Each new vessel gets a fresh tracker with the configured limits.

---

## 7. Per-vessel summary artifact

### 7.1 Types

New file: `cli/internal/runner/summary.go`

```go
package runner

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"
)

// VesselSummary is the JSON artifact written after vessel completion or failure.
// Path: <state_dir>/phases/<vessel-id>/summary.json
//
// All token counts and cost values are ESTIMATES derived from prompt/output
// byte lengths (len/4 heuristic) and static pricing tables. They are NOT
// provider-reported values.
type VesselSummary struct {
    VesselID   string    `json:"vessel_id"`
    Source     string    `json:"source"`
    Workflow   string    `json:"workflow"`
    Ref        string    `json:"ref,omitempty"`
    State      string    `json:"state"`
    StartedAt  time.Time `json:"started_at"`
    EndedAt    time.Time `json:"ended_at"`
    DurationMS int64     `json:"duration_ms"`

    Phases []PhaseSummary `json:"phases"`

    TotalInputTokensEst  int     `json:"total_input_tokens_est"`
    TotalOutputTokensEst int     `json:"total_output_tokens_est"`
    TotalTokensEst       int     `json:"total_tokens_est"`
    TotalCostUSDEst      float64 `json:"total_cost_usd_est"`

    BudgetMaxCostUSD *float64 `json:"budget_max_cost_usd,omitempty"`
    BudgetMaxTokens  *int     `json:"budget_max_tokens,omitempty"`
    BudgetExceeded   bool     `json:"budget_exceeded"`

    EvidenceManifestPath string `json:"evidence_manifest_path,omitempty"`

    Note string `json:"note"`
}

// PhaseSummary records the outcome of a single phase.
type PhaseSummary struct {
    Name            string  `json:"name"`
    Type            string  `json:"type"`
    Provider        string  `json:"provider"`
    Model           string  `json:"model"`
    DurationMS      int64   `json:"duration_ms"`
    Status          string  `json:"status"` // "completed", "failed", "no-op"
    InputTokensEst  int     `json:"input_tokens_est"`
    OutputTokensEst int     `json:"output_tokens_est"`
    CostUSDEst      float64 `json:"cost_usd_est"`
    GateType        string  `json:"gate_type,omitempty"`
    GatePassed      *bool   `json:"gate_passed,omitempty"`
    Error           string  `json:"error,omitempty"`
}

const summaryDisclaimer = "Token counts and costs are estimates (len/4 heuristic + static pricing). Not provider-reported values."

func SaveVesselSummary(stateDir string, summary *VesselSummary) error {
    dir := filepath.Join(stateDir, "phases", summary.VesselID)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return fmt.Errorf("create summary dir: %w", err)
    }
    summary.Note = summaryDisclaimer
    data, err := json.MarshalIndent(summary, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal summary: %w", err)
    }
    return os.WriteFile(filepath.Join(dir, "summary.json"), data, 0o644)
}
```

### 7.2 Accumulator

The `vesselRunState` struct accumulates telemetry during vessel execution. It is created at the start of `runVessel` and used to build the summary at completion or failure.

```go
type vesselRunState struct {
    startedAt   time.Time
    phases      []PhaseSummary
    costTracker *cost.Tracker
    vesselID    string
    source      string
    workflow    string
    ref         string
}

func (s *vesselRunState) addPhase(ps PhaseSummary) {
    s.phases = append(s.phases, ps)
}

func (s *vesselRunState) buildSummary(state string) *VesselSummary {
    now := time.Now()
    summary := &VesselSummary{
        VesselID:  s.vesselID,
        Source:    s.source,
        Workflow:  s.workflow,
        Ref:       s.ref,
        State:     state,
        StartedAt: s.startedAt,
        EndedAt:   now,
        DurationMS: now.Sub(s.startedAt).Milliseconds(),
        Phases:    s.phases,
    }
    for _, p := range s.phases {
        summary.TotalInputTokensEst += p.InputTokensEst
        summary.TotalOutputTokensEst += p.OutputTokensEst
        summary.TotalCostUSDEst += p.CostUSDEst
    }
    summary.TotalTokensEst = summary.TotalInputTokensEst + summary.TotalOutputTokensEst
    if s.costTracker != nil {
        summary.BudgetExceeded = s.costTracker.BudgetExceeded()
    }
    return summary
}
```

### 7.3 Generation points and `completeVessel` changes

The `completeVessel` method (runner.go:573, called from 3 sites) must be updated to accept the accumulator and evidence claims:

```go
// Updated signature — add vesselRunState and claims parameters:
func (r *Runner) completeVessel(
    ctx context.Context,
    vessel queue.Vessel,
    worktreePath string,
    phaseResults []reporter.PhaseResult,
    vrs *vesselRunState,       // NEW
    claims []evidence.Claim,   // NEW
) string
```

**Call sites to update:**
- Sequential path: `runVessel`, around line 496
- Orchestrated path: `runVesselOrchestrated`, around lines 708 and 718

Inside `completeVessel`, after the existing completion logic:

```go
// Save summary artifact
summary := vrs.buildSummary("completed")
if err := SaveVesselSummary(r.Config.StateDir, summary); err != nil {
    log.Printf("warn: save vessel summary: %v", err)
}

// Save evidence manifest
if len(claims) > 0 {
    manifest := &evidence.Manifest{
        VesselID:  vessel.ID,
        Workflow:  vessel.Workflow,
        Claims:    claims,
        CreatedAt: time.Now(),
    }
    if err := evidence.SaveManifest(r.Config.StateDir, vessel.ID, manifest); err != nil {
        log.Printf("warn: save evidence manifest: %v", err)
    }
    summary.EvidenceManifestPath = filepath.Join("phases", vessel.ID, "evidence-manifest.json")
}
```

**Failure paths**: The `vesselRunState` is created at the start of `runVessel`/`runVesselOrchestrated` and is in scope for all failure paths within those functions. At each `→ fail vessel` point, call:

```go
summary := vrs.buildSummary("failed")
_ = SaveVesselSummary(r.Config.StateDir, summary)
```

Summary/manifest writing is non-fatal — if either fails, log a warning and continue.

---

## 8. Verification evidence model

### 8.1 Evidence package

New package: `cli/internal/evidence/`

```go
package evidence

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"
)

// Level represents the strength of verification evidence.
type Level string

const (
    Proved              Level = "proved"
    MechanicallyChecked Level = "mechanically_checked"
    BehaviorallyChecked Level = "behaviorally_checked"
    ObservedInSitu      Level = "observed_in_situ"
    Untyped             Level = "" // zero value for gates without evidence metadata
)

// Valid returns true for recognized evidence levels (including Untyped).
func (l Level) Valid() bool {
    switch l {
    case Proved, MechanicallyChecked, BehaviorallyChecked, ObservedInSitu, Untyped:
        return true
    }
    return false
}

// Rank returns a numeric ordering for comparison. Higher = stronger.
func (l Level) Rank() int {
    switch l {
    case Proved:
        return 4
    case MechanicallyChecked:
        return 3
    case BehaviorallyChecked:
        return 2
    case ObservedInSitu:
        return 1
    default:
        return 0
    }
}

// Claim records a single verification assertion.
type Claim struct {
    Claim         string    `json:"claim"`
    Level         Level     `json:"level"`
    Checker       string    `json:"checker"`
    TrustBoundary string    `json:"trust_boundary"`
    ArtifactPath  string    `json:"artifact_path,omitempty"`
    Phase         string    `json:"phase"`
    Passed        bool      `json:"passed"`
    Timestamp     time.Time `json:"timestamp"`
}

// Manifest is the per-vessel evidence record.
type Manifest struct {
    VesselID  string          `json:"vessel_id"`
    Workflow  string          `json:"workflow"`
    Claims    []Claim         `json:"claims"`
    CreatedAt time.Time       `json:"created_at"`
    Summary   ManifestSummary `json:"summary"`
}

// ManifestSummary provides aggregate counts.
type ManifestSummary struct {
    Total   int           `json:"total"`
    Passed  int           `json:"passed"`
    Failed  int           `json:"failed"`
    ByLevel map[Level]int `json:"by_level"`
}

// BuildSummary computes the summary from claims.
func (m *Manifest) BuildSummary() {
    m.Summary = ManifestSummary{
        Total:   len(m.Claims),
        ByLevel: make(map[Level]int),
    }
    for _, c := range m.Claims {
        if c.Passed {
            m.Summary.Passed++
        } else {
            m.Summary.Failed++
        }
        m.Summary.ByLevel[c.Level]++
    }
}

// StrongestLevel returns the highest-ranked passing claim level,
// or Untyped if no claims passed.
func (m *Manifest) StrongestLevel() Level {
    best := Untyped
    for _, c := range m.Claims {
        if c.Passed && c.Level.Rank() > best.Rank() {
            best = c.Level
        }
    }
    return best
}

// SaveManifest writes the manifest to the vessel's phases directory.
// Path: <stateDir>/phases/<vesselID>/evidence-manifest.json
func SaveManifest(stateDir, vesselID string, manifest *Manifest) error {
    dir := filepath.Join(stateDir, "phases", vesselID)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return fmt.Errorf("create manifest dir: %w", err)
    }
    manifest.BuildSummary()
    data, err := json.MarshalIndent(manifest, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal manifest: %w", err)
    }
    return os.WriteFile(filepath.Join(dir, "evidence-manifest.json"), data, 0o644)
}

func LoadManifest(stateDir, vesselID string) (*Manifest, error) {
    path := filepath.Join(stateDir, "phases", vesselID, "evidence-manifest.json")
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    var m Manifest
    if err := json.Unmarshal(data, &m); err != nil {
        return nil, fmt.Errorf("unmarshal manifest: %w", err)
    }
    return &m, nil
}
```

### 8.2 Workflow YAML extension

Add optional evidence metadata to the `Gate` struct in `cli/internal/workflow/workflow.go`:

```go
type GateEvidence struct {
    Claim         string `yaml:"claim,omitempty"`
    Level         string `yaml:"level,omitempty"`
    Checker       string `yaml:"checker,omitempty"`
    TrustBoundary string `yaml:"trust_boundary,omitempty"`
}

type Gate struct {
    // ... existing fields unchanged ...
    Evidence *GateEvidence `yaml:"evidence,omitempty"`
}
```

Add validation in `validateGate`:

```go
if g.Evidence != nil {
    if g.Evidence.Level != "" {
        if !evidence.Level(g.Evidence.Level).Valid() || evidence.Level(g.Evidence.Level) == evidence.Untyped {
            return fmt.Errorf("gate evidence level %q is not valid (must be proved, mechanically_checked, behaviorally_checked, or observed_in_situ)", g.Evidence.Level)
        }
    }
}
```

**Example workflow YAML with evidence:**

```yaml
phases:
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 50
    gate:
      type: command
      run: "cd cli && go test ./..."
      retries: 2
      evidence:
        claim: "All tests pass"
        level: behaviorally_checked
        checker: "go test"
        trust_boundary: "Tests exercise package-level behavior; does not prove integration correctness"
```

### 8.3 Claim construction in runner

New helper in `runner.go`:

```go
func buildGateClaim(p workflow.Phase, passed bool, vesselID string) evidence.Claim {
    claim := evidence.Claim{
        Phase:     p.Name,
        Passed:    passed,
        Timestamp: time.Now(),
    }
    if p.Gate != nil && p.Gate.Evidence != nil {
        claim.Claim = p.Gate.Evidence.Claim
        claim.Level = evidence.Level(p.Gate.Evidence.Level)
        claim.Checker = p.Gate.Evidence.Checker
        claim.TrustBoundary = p.Gate.Evidence.TrustBoundary
    } else {
        claim.Claim = fmt.Sprintf("Gate passed for phase %q", p.Name)
        claim.Level = evidence.Untyped
        if p.Gate != nil {
            claim.Checker = p.Gate.Run
        }
        claim.TrustBoundary = "No trust boundary declared"
    }
    return claim
}
```

Evidence claims are accumulated in a `[]evidence.Claim` slice during vessel execution. After a gate passes, a claim is appended. At vessel completion, a manifest is built and saved:

```go
manifest := &evidence.Manifest{
    VesselID:  vessel.ID,
    Workflow:  vessel.Workflow,
    Claims:    claims,
    CreatedAt: time.Now(),
}
_ = evidence.SaveManifest(r.Config.StateDir, vessel.ID, manifest)
```

### 8.4 Reporter enhancement

Extend `VesselCompleted` in `cli/internal/reporter/reporter.go`:

```go
func (r *Reporter) VesselCompleted(ctx context.Context, issueNum int,
    phases []PhaseResult, manifest *evidence.Manifest) error
```

When `manifest` is nil or has zero claims, output is identical to today. When evidence is present, append after the phase table:

```markdown
### Verification evidence

| Claim | Level | Checker | Result |
|-------|-------|---------|--------|
| All tests pass | behaviorally_checked | go test | :white_check_mark: |

<details>
<summary>Trust boundaries</summary>

- **All tests pass** — Tests exercise package-level behavior; does not prove integration correctness

</details>
```

The `formatEvidenceSection(manifest *evidence.Manifest) string` function handles rendering. Returns empty string for nil or empty manifests.

**Existing call sites to update:** The `VesselCompleted` signature change affects:
- `runner.go:completeVessel` (around line 585) — pass the manifest
- All existing tests in `reporter_test.go` that call `VesselCompleted` — update to pass `nil` as the new manifest parameter. As of 2026-03-31, these include `TestVesselCompleted`, `TestVesselCompletedNoOp`, and any other test functions that invoke `VesselCompleted`. Grep for `VesselCompleted` in `reporter_test.go` to find all call sites.

---

## 9. Eval suite (Harbor-native)

The eval suite measures the quality of the xylem harness — prompts, tool configuration, workflow structure, gate design, and orchestration behavior — by running representative scenarios through [Harbor](https://github.com/codeintegrity-ai/harbor) (v0.2.0) and verifying outcomes against xylem's artifact format.

There is **no custom eval runner**, no custom Go eval types, and no `xylem eval` subcommand. Everything runs through `harbor run`, `harbor analyze`, and `harbor view`.

### 9.1 Architecture

```
harbor run  →  Docker container  →  claude-code  →  xylem enqueue + drain  →  artifacts
                                                                                  |
harbor analyze  ←  rubrics  ←  reward.txt  ←  pytest  ←  artifact verification  ←+
```

Each eval scenario is a Harbor task directory. Harbor launches `claude-code` inside an isolated Docker container. The task's `instruction.md` tells claude-code to set up and run a xylem scenario. After xylem completes, a pytest verification script inspects the output artifacts (`.xylem/phases/<vessel-id>/summary.json`, `evidence-manifest.json`, `<phase>.output`) and writes a `reward.txt` score (0.0–1.0).

The agent is `claude-code` (Harbor's built-in), not xylem directly. This exercises the full xylem pipeline — config loading, workflow resolution, phase execution, gate evaluation, evidence collection, and summary generation — inside Harbor's managed environment.

### 9.2 Directory layout

```
.xylem/eval/
  harbor.yaml                        # Job-level config (9.4)
  scenarios/
    fix-simple-null-pointer/
      instruction.md                 # What the agent should do (9.3)
      task.toml                      # Harbor task metadata (9.3)
      environment/
        Dockerfile                   # Docker environment setup (9.3)
      tests/
        test.sh                      # Entry point for Harbor verification
        test_verification.py         # pytest checks on xylem artifacts (9.6)
      fixture/
        main.go                      # Source files baked into Docker image
        main_test.go
        go.mod
    diagnose-test-failure/
      ...
    plan-quality-feature/
      ...
  rubrics/
    plan_quality.toml                # Harbor rubric for plan analysis (9.7)
    evidence_quality.toml            # Harbor rubric for evidence strength
  helpers/
    xylem_verify.py                  # Shared pytest helpers (9.5)
    conftest.py                      # pytest conftest exposing helpers
```

### 9.3 Task file formats

**task.toml** — Each scenario's metadata follows Harbor v0.2.0 conventions:

```toml
[task]
id = "fix-simple-null-pointer"
version = "1"

[task.environment]
timeout_seconds = 600

[task.metadata]
category = "workflow-execution"
tags = ["fix-bug", "gate-verification", "go"]
difficulty = "easy"
canary = "CANARY-XYLEM-EVAL-9a3f"
```

Mandatory fields: `task.id` (unique, matches directory name), `task.environment.timeout_seconds`. Optional: `task.metadata.category` (one of: `workflow-execution`, `gate-verification`, `plan-quality`, `evidence-strength`, `cost-accuracy`, `surface-protection`, `policy-enforcement`), `task.metadata.tags`, `task.metadata.difficulty` (`easy`/`medium`/`hard`), `task.metadata.canary`.

**instruction.md** — Tells claude-code what to do. Must not mention Harbor, scoring, or verification:

```markdown
# Task: Fix null pointer dereference in processItem

## Issue

The `processItem` function in `main.go` panics with a nil pointer
dereference when called with an item that has no metadata field.

## Context

You are working in a repository that uses xylem for autonomous task execution.
The repository has a `.xylem.yml` configuration and a `fix-bug` workflow.

## What to do

1. Run `xylem enqueue --source manual --prompt 'Fix nil pointer in processItem when metadata is nil' --workflow fix-bug`
2. Run `xylem drain`
3. After drain completes, verify the vessel completed with `xylem status`

## Constraints

- Do not modify `.xylem.yml` or any files under `.xylem/`.
- Work only within the repository root.
```

**environment/Dockerfile** — Prepares the Docker environment with Go, xylem, and the fixture repo:

```dockerfile
FROM golang:1.23-bookworm

# Install xylem
RUN git clone https://github.com/nicholls-inc/xylem.git /tmp/xylem \
    && cd /tmp/xylem/cli \
    && go build -o /usr/local/bin/xylem ./cmd/xylem \
    && rm -rf /tmp/xylem

# Install Claude Code (Harbor's agent setup handles this, but ensure PATH is correct)
ENV PATH="/usr/local/bin:$PATH"

# Set up the fixture repo
WORKDIR /workspace
COPY fixture/ /workspace/
RUN git init && git add -A && git commit -m "Initial commit"

# Add xylem configuration
COPY xylem-config/ /workspace/.xylem/
COPY .xylem.yml /workspace/.xylem.yml
RUN git add -A && git commit -m "Add xylem configuration"
```

Each scenario directory also contains a `.xylem.yml` and a `xylem-config/` directory (workflows, prompts) that get copied into the Docker image. This keeps xylem configuration co-located with the scenario.

**tests/test.sh** — Harbor's required verification entry point. Delegates to pytest:

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
pip install -q pytest > /dev/null 2>&1
pytest tests/test_verification.py -v --tb=short
```

### 9.4 harbor.yaml job config

```yaml
# .xylem/eval/harbor.yaml
agent: claude-code
model: claude-sonnet-4-6
path: scenarios/
n_attempts: 1
n_concurrent: 2
timeout_multiplier: 1.5
```

Run with: `harbor run -c .xylem/eval/harbor.yaml -o jobs/baseline/`

**API key provisioning:** Harbor injects `ANTHROPIC_API_KEY` from the host environment into the Docker container. Use `--env-file .env` if the key is stored in a file, or export it before running `harbor run`.

### 9.5 Shared verification helpers

File: `.xylem/eval/helpers/xylem_verify.py`

This Python module provides pytest-compatible functions for reading and asserting against xylem artifacts.

**Discovery:**

```python
def find_vessel_dir(work_dir: str) -> str:
    """Locate the single vessel directory under .xylem/phases/.
    Eval scenarios run exactly one vessel, so assert exactly one match."""
    pattern = os.path.join(work_dir, ".xylem", "phases", "*", "summary.json")
    matches = glob.glob(pattern)
    assert len(matches) == 1, f"Expected 1 vessel dir, found {len(matches)}: {matches}"
    return os.path.dirname(matches[0])
```

**Loaders:**

```python
def load_summary(work_dir: str) -> dict:
    """Load and parse .xylem/phases/<id>/summary.json."""
    vessel_dir = find_vessel_dir(work_dir)
    with open(os.path.join(vessel_dir, "summary.json")) as f:
        return json.load(f)

def load_evidence(work_dir: str) -> dict | None:
    """Load evidence-manifest.json, or None if absent."""
    vessel_dir = find_vessel_dir(work_dir)
    path = os.path.join(vessel_dir, "evidence-manifest.json")
    if not os.path.exists(path):
        return None
    with open(path) as f:
        return json.load(f)

def load_phase_output(work_dir: str, phase_name: str) -> str | None:
    """Load a phase's .output file, or None if absent."""
    vessel_dir = find_vessel_dir(work_dir)
    path = os.path.join(vessel_dir, f"{phase_name}.output")
    if not os.path.exists(path):
        return None
    with open(path) as f:
        return f.read()

def load_audit_log(work_dir: str) -> list[dict]:
    """Load .xylem/audit.jsonl as a list of entries."""
    path = os.path.join(work_dir, ".xylem", "audit.jsonl")
    if not os.path.exists(path):
        return []
    entries = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line:
                entries.append(json.loads(line))
    return entries
```

**Assertions:**

```python
EVIDENCE_RANK = {
    "proved": 4,
    "mechanically_checked": 3,
    "behaviorally_checked": 2,
    "observed_in_situ": 1,
    "": 0,  # Untyped
}

def assert_vessel_completed(work_dir: str):
    summary = load_summary(work_dir)
    assert summary["state"] == "completed", f"Vessel state: {summary['state']}"

def assert_vessel_failed(work_dir: str):
    summary = load_summary(work_dir)
    assert summary["state"] == "failed", f"Vessel state: {summary['state']}"

def assert_phases_completed(summary: dict, phase_names: list[str]):
    completed = [p["name"] for p in summary["phases"] if p["status"] == "completed"]
    for name in phase_names:
        assert name in completed, f"Phase {name} not completed. Completed: {completed}"

def assert_gates_passed(summary: dict, phase_names: list[str]):
    for phase in summary["phases"]:
        if phase["name"] in phase_names and phase.get("gate_type"):
            assert phase.get("gate_passed") is True, \
                f"Gate for phase {phase['name']} did not pass"

def assert_evidence_level(manifest: dict, phase_name: str, min_level: str):
    for claim in manifest["claims"]:
        if claim["phase"] == phase_name and claim["passed"]:
            actual_rank = EVIDENCE_RANK.get(claim["level"], 0)
            min_rank = EVIDENCE_RANK.get(min_level, 0)
            assert actual_rank >= min_rank, \
                f"Phase {phase_name}: evidence {claim['level']} < {min_level}"
            return
    assert False, f"No passing evidence claim found for phase {phase_name}"

def assert_cost_within_budget(summary: dict):
    assert not summary.get("budget_exceeded", False), "Budget exceeded"
```

**Scoring:**

```python
def compute_reward(checks: list[tuple[str, bool]], weights: dict[str, float] | None = None) -> float:
    """Compute 0.0-1.0 reward from named pass/fail checks with optional weights."""
    if not checks:
        return 0.0
    if weights is None:
        weights = {name: 1.0 for name, _ in checks}
    total_weight = sum(weights.get(name, 1.0) for name, _ in checks)
    earned = sum(weights.get(name, 1.0) for name, passed in checks if passed)
    return earned / total_weight if total_weight > 0 else 0.0

def write_reward(task_dir: str, score: float):
    """Write reward.txt for Harbor."""
    with open(os.path.join(task_dir, "reward.txt"), "w") as f:
        f.write(f"{score:.4f}\n")
```

**conftest.py:**

```python
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "helpers"))
import xylem_verify

import pytest

@pytest.fixture
def work_dir():
    return os.environ.get("WORK_DIR", "/workspace")

@pytest.fixture
def task_dir():
    return os.environ.get("TASK_DIR", os.path.dirname(os.path.dirname(__file__)))

@pytest.fixture
def verify():
    return xylem_verify
```

### 9.6 Verification script template

Each scenario's `tests/test_verification.py` checks xylem artifacts and writes `reward.txt`.

**Workflow-execution template (positive test):**

```python
import xylem_verify as xv

def test_vessel_outcome(work_dir, task_dir, verify):
    checks = []

    # Vessel completed
    summary = verify.load_summary(work_dir)
    checks.append(("vessel_completed", summary["state"] == "completed"))

    # Expected phases completed
    completed = {p["name"] for p in summary["phases"] if p["status"] == "completed"}
    checks.append(("phases_completed", {"diagnose", "implement"}.issubset(completed)))

    # Gate passed
    for p in summary["phases"]:
        if p["name"] == "implement" and p.get("gate_type") == "command":
            checks.append(("gate_passed", p.get("gate_passed") is True))

    # Evidence at minimum level
    manifest = verify.load_evidence(work_dir)
    if manifest:
        for claim in manifest["claims"]:
            if claim["phase"] == "implement" and claim["passed"]:
                checks.append(("evidence_level",
                    xv.EVIDENCE_RANK.get(claim["level"], 0) >= xv.EVIDENCE_RANK["behaviorally_checked"]))

    # Budget not exceeded
    checks.append(("budget_ok", not summary.get("budget_exceeded", False)))

    # Write reward
    weights = {"vessel_completed": 3.0, "phases_completed": 2.0,
               "gate_passed": 2.0, "evidence_level": 1.0, "budget_ok": 1.0}
    score = verify.compute_reward(checks, weights)
    verify.write_reward(task_dir, score)

    # Assert for pytest pass/fail
    assert score >= 0.8, f"Reward {score:.2f} below threshold. Checks: {checks}"
```

**Surface-protection template (negative test):**

```python
def test_surface_violation(work_dir, task_dir, verify):
    checks = []

    summary = verify.load_summary(work_dir)
    checks.append(("vessel_failed", summary["state"] == "failed"))

    audit = verify.load_audit_log(work_dir)
    has_violation = any(e.get("decision") == "deny" and "file_write" in e.get("intent", {}).get("action", "")
                       for e in audit)
    checks.append(("violation_logged", has_violation))

    score = verify.compute_reward(checks)
    verify.write_reward(task_dir, score)
    assert score >= 0.9, f"Reward {score:.2f}. Checks: {checks}"
```

### 9.7 Rubric files

Harbor rubrics (TOML) for `harbor analyze` to perform LLM-graded evaluation of subjective quality:

**rubrics/plan_quality.toml:**

```toml
[rubric]
name = "plan_quality"
description = "Evaluate the quality of xylem's diagnose/plan phase output"

[[rubric.criteria]]
name = "root_cause_identification"
description = "Did the agent correctly identify the root cause of the issue?"
weight = 0.4

[[rubric.criteria]]
name = "reasoning_chain"
description = "Is the reasoning from symptoms to root cause clear and logical?"
weight = 0.3

[[rubric.criteria]]
name = "scope_accuracy"
description = "Does the plan correctly scope the fix without unnecessary changes?"
weight = 0.3
```

**rubrics/evidence_quality.toml:**

```toml
[rubric]
name = "evidence_quality"
description = "Evaluate trust boundary clarity and evidence completeness"

[[rubric.criteria]]
name = "trust_boundary_clarity"
description = "Does the evidence manifest clearly articulate what was and was not verified?"
weight = 0.5

[[rubric.criteria]]
name = "evidence_completeness"
description = "Are all meaningful verification claims captured with appropriate levels?"
weight = 0.5
```

### 9.8 Harbor command reference

```bash
# Run full suite
harbor run -c .xylem/eval/harbor.yaml -o jobs/baseline/

# Run single scenario
harbor run -c .xylem/eval/harbor.yaml -t "fix-simple-null-pointer" -o jobs/single/

# Run with different model (A/B comparison)
harbor run -c .xylem/eval/harbor.yaml -m claude-sonnet-4-5 -o jobs/candidate/

# Run with multiple attempts for statistical significance
harbor run -c .xylem/eval/harbor.yaml -k 3 -o jobs/3x/

# Analyze results with rubrics
harbor analyze jobs/baseline/ -r .xylem/eval/rubrics/plan_quality.toml -o analysis-baseline.json
harbor analyze jobs/candidate/ -r .xylem/eval/rubrics/plan_quality.toml -o analysis-candidate.json

# Compare: diff the two analysis JSON outputs (Harbor has no built-in comparison command)
# Use jq, a Python script, or manual inspection to compare analysis-baseline.json vs analysis-candidate.json

# Browse results in web UI
harbor view jobs/baseline/

# Resume an incomplete job
harbor jobs resume jobs/baseline/

# Provision API keys via environment
export ANTHROPIC_API_KEY=sk-...
harbor run -c .xylem/eval/harbor.yaml -o jobs/baseline/
# Or: harbor run -c .xylem/eval/harbor.yaml --env-file .env -o jobs/baseline/
```

**Note:** Harbor v0.2.0 does not have a built-in comparison command. To compare baseline vs. candidate runs, analyze each separately and diff the JSON outputs. A lightweight comparison script (Python or jq) is a recommended addition but is not part of Harbor itself.

### 9.9 Scenario categories

| Category | Scorecard gap | What it measures | Example scenario |
|----------|---------------|------------------|------------------|
| `workflow-execution` | 7.2 | End-to-end workflow completion | fix-simple-null-pointer |
| `gate-verification` | 6.2, 6.4 | Gate pass/fail + evidence collection | gate-retry-then-pass |
| `plan-quality` | 7.3 | Diagnose/plan phase output quality | diagnose-complex-race-condition |
| `evidence-strength` | 6.2, 6.4 | Evidence level and trust boundary accuracy | evidence-from-multiple-gates |
| `surface-protection` | 8.3 | Protected surface violation detection | modify-harness-md |
| `cost-accuracy` | 10.1 | Cost estimate reasonableness | budget-enforcement-on-large-task |
| `policy-enforcement` | 8.5 | Intermediary policy deny/allow | deny-force-push-policy |

### 9.10 WS5 deferred items

The following are **not implemented** by agents working from this spec:

1. **Scenario population** — fixture repos, issue descriptions, expected behaviors (requires human judgment about what's representative)
2. **Docker image** — building and publishing the xylem eval base image with Go toolchain, xylem binary, and pytest
3. **Rubric calibration** — running rubrics against known-good/bad outputs to validate scoring (requires human labels)
4. **CI pipeline** — wiring `harbor run` into GitHub Actions for regression detection
5. **Baseline establishment** — running the full suite to create initial comparison data
6. **Fixture repo curation** — creating minimal but realistic repository fixtures for each scenario
7. **Comparison tooling** — a lightweight script to diff `harbor analyze` JSON outputs across runs

**What agents DO implement (Step 6):** the directory structure, `harbor.yaml`, `xylem_verify.py` helper module, `conftest.py`, one verification script template per category (workflow-execution and surface-protection), and the rubric TOML files. This creates the scaffolding that humans then populate with scenarios.

---

## 10. Error precedence and evaluation order

When multiple workstream mechanisms could trigger on the same phase, they are evaluated in this fixed order. Each stage short-circuits on failure — later stages do not execute.

| Order | Stage | Mechanism | On failure |
|-------|-------|-----------|------------|
| 1 | Policy check | `intermediary.Evaluate()` | Fail vessel: "denied by policy" |
| 2 | Surface pre-snapshot | `surface.TakeSnapshot()` | Fail vessel: "snapshot failed" |
| 3 | Phase execution | `RunPhase()` / `RunCommand()` | Fail vessel: phase error |
| 4 | Surface post-verification | `surface.Compare()` | Fail vessel: "violated protected surfaces" |
| 5 | Budget check | `costTracker.BudgetExceeded()` | Fail vessel: "budget exceeded" |
| 6 | Gate evaluation | `gate.RunCommandGate()` / `gate.CheckLabel()` | Retry or fail vessel |
| 7 | Evidence collection | `buildGateClaim()` | Non-fatal (log warning) |
| 8 | Summary/manifest write | `SaveVesselSummary()` / `SaveManifest()` | Non-fatal (log warning) |

**Evidence from failed phases:** If a phase fails at any stage (3-5), any evidence claims accumulated for *that specific phase* are discarded. Claims from *prior completed phases* are preserved in the manifest.

**Span lifecycle across failures:** Phase spans are always ended, even on failure. Errors are recorded on the span via `RecordError()` before `End()` is called. This ensures traces are complete even for failed vessels.

---

## 11. Prompt-only vessels

The runner has a `runPromptOnly` path (`runner.go:500-529`) for vessels with a prompt but no workflow. This path bypasses workflow/phase logic entirely.

| Feature | Applies to prompt-only? | Rationale |
|---------|------------------------|-----------|
| Vessel span | Yes | All vessels should be traceable |
| Cost tracking | Yes | All LLM calls consume tokens |
| Surface verification | Yes | HARNESS.md exists in the worktree and should be protected |
| Policy evaluation | No | No structured phase actions to evaluate; the single prompt is the entire execution |
| Evidence claims | No | No gates, so no verification mechanisms |
| Summary artifact | Yes | Provides consistent per-vessel telemetry |

Integration: wrap `runPromptOnly` with vessel span creation, surface pre/post-snapshot, cost estimation after `RunPhase`, and summary generation. Policy evaluation and evidence collection are skipped.

---

## 12. Orchestrated execution details

When a workflow has `depends_on` declarations, the runner uses `runVesselOrchestrated` which executes phases in dependency-ordered waves. Multiple phases within a wave run concurrently via goroutines.

### 12.1 Accumulator lifecycle

The `vesselRunState` is owned by `runVesselOrchestrated`. It is **not shared** across goroutines. Instead:

1. Extend `singlePhaseResult` (runner.go:722) with telemetry fields:

```go
type singlePhaseResult struct {
    output          string
    status          string
    duration        time.Duration
    gateOut         string
    // NEW fields:
    phaseSummary    PhaseSummary
    evidenceClaim   *evidence.Claim // nil if no gate or gate had no evidence
}
```

2. The `waveResult` struct (runner.go:633) collects `singlePhaseResult` per-goroutine. No change needed to `waveResult` — it already wraps `singlePhaseResult`. After each wave's `wg.Wait()`, merge telemetry into the accumulator (in the existing result-processing loop around runner.go:676-710):

```go
// Inside the existing wave result merge loop:
for _, wr := range waveResults {
    // ... existing: check errors, collect previousOutputs, collect phaseResults ...
    // NEW: merge telemetry
    vrs.addPhase(wr.result.phaseSummary)
    if wr.result.evidenceClaim != nil {
        claims = append(claims, *wr.result.evidenceClaim)
    }
}
```

This follows the existing pattern where results are collected per-goroutine and merged after `wg.Wait()`.

### 12.2 Cost tracker thread safety

The `cost.Tracker` is already thread-safe (it has its own `sync.Mutex`). It is passed to each `runSinglePhase` goroutine and called concurrently. Budget checks happen per-phase after cost is recorded, which may cause slight over-spend if two phases complete simultaneously — this is acceptable for estimates.

### 12.3 Span propagation

The vessel span's context is passed to each goroutine. Each creates its own child phase span:

```go
// In runVesselOrchestrated, for each wave member:
go func(phaseIdx int) {
    // vesselCtx carries the vessel span for parent-child linkage
    var phaseSpan observability.SpanContext
    if r.Tracer != nil {
        phaseSpan = r.Tracer.StartSpan(vesselCtx, "phase:"+p.Name, ...)
        defer phaseSpan.End()
    }
    // ... runSinglePhase logic ...
}(idx)
```

OTel's SDK is thread-safe for concurrent span creation. No additional synchronization is needed.

---

## 13. Backwards compatibility

### 13.1 Config

All new YAML sections (`harness:`, `observability:`, `cost:`) use `omitempty`. Missing sections deserialize to Go zero values. Helper methods return safe defaults for zero values. **No existing `.xylem.yml` needs any change.**

### 13.2 Workflow YAML

The `evidence` field on `Gate` is a pointer with `omitempty`. Existing gates without evidence metadata unmarshal to `nil`. The runner's `buildGateClaim` produces `Untyped` claims for these gates. **No existing workflow file needs any change.**

### 13.3 Queue JSONL

No new fields are added to the `Vessel` struct in the queue package. Summary artifacts and evidence manifests are written as separate files in the phases directory, not as queue fields.

### 13.4 Runner

New fields on the `Runner` struct (`Intermediary`, `AuditLog`, `Tracer`) are all nilable. When nil, all new behavior is skipped via nil-checks. **Existing code paths that don't set these fields continue to work unchanged.**

### 13.5 Reporter

The `VesselCompleted` method gains an additional `*evidence.Manifest` parameter. Call sites are updated to pass the manifest (or nil for prompt-only vessels without evidence). When nil, output is identical to today.

---

## 14. Implementation sequence

### 14.1 Dependency graph

```
Step 1: Config types + validation        (no dependencies)
Step 2: surface package                   (no dependencies)
Step 3: evidence package                  (no dependencies)
Step 4: cost/estimate.go                  (no dependencies)
Step 5: observability/vessel.go           (no dependencies)
Step 6: .xylem/eval/ scaffolding           (no dependencies)
Step 7: workflow Gate.Evidence extension   (depends on step 3)
Step 8: runner/summary.go                 (depends on step 4)
Step 9: runner integration — policy       (depends on steps 1, 2)
Step 10: runner integration — observability (depends on steps 1, 5, 8)
Step 11: runner integration — cost         (depends on steps 1, 4, 8)
Step 12: runner integration — evidence     (depends on steps 3, 7, 8)
Step 13: reporter enhancement             (depends on step 3)
Step 14: CLI startup wiring               (depends on steps 1, 9, 10)
Step 15: docs update                      (depends on all above)
```

Steps 1-6 are fully parallelizable. Steps 9-12 are parallelizable after their dependencies. Steps 7-8 are parallelizable after their dependencies.

### 14.2 Per-step critical files

| Step | Files created/modified |
|------|----------------------|
| 1 | `cli/internal/config/config.go`, `cli/internal/config/config_test.go` |
| 2 | `cli/internal/surface/surface.go`, `cli/internal/surface/surface_test.go`, `cli/internal/surface/surface_prop_test.go` |
| 3 | `cli/internal/evidence/evidence.go`, `cli/internal/evidence/evidence_test.go` |
| 4 | `cli/internal/cost/estimate.go`, `cli/internal/cost/estimate_test.go` |
| 5 | `cli/internal/observability/vessel.go`, `cli/internal/observability/vessel_test.go` |
| 6 | `.xylem/eval/harbor.yaml`, `.xylem/eval/helpers/xylem_verify.py`, `.xylem/eval/helpers/conftest.py`, `.xylem/eval/rubrics/*.toml` |
| 7 | `cli/internal/workflow/workflow.go`, `cli/internal/workflow/workflow_test.go` |
| 8 | `cli/internal/runner/summary.go`, `cli/internal/runner/summary_test.go` |
| 9-12 | `cli/internal/runner/runner.go`, `cli/internal/runner/runner_test.go` |
| 13 | `cli/internal/reporter/reporter.go`, `cli/internal/reporter/reporter_test.go` |
| 14 | `cli/cmd/xylem/drain.go`, `cli/cmd/xylem/daemon.go` |
| 15 | `docs/configuration.md`, `docs/workflows.md` |

---

## 15. Test strategy

### 15.1 Surface package

Unit tests (`surface_test.go`):
- `TestTakeSnapshot_EmptyDir` — no matches = empty snapshot
- `TestTakeSnapshot_MatchesGlobs` — correct hashing, ignores non-matching
- `TestTakeSnapshot_Deterministic` — two calls on same files = identical result
- `TestTakeSnapshot_SortOrder` — sorted by path
- `TestCompare_NoViolations` — identical snapshots = empty violations
- `TestCompare_ModifiedFile` — detects changed hash
- `TestCompare_DeletedFile` — detects removal
- `TestCompare_CreatedFile` — detects addition

Property tests (`surface_prop_test.go`):
- `TestProp_SnapshotRoundtrip` — unchanged files always compare clean
- `TestProp_CompareDetectsAnyChange` — random byte change always yields violation

### 15.2 Config

Add to existing `config_test.go`:
- `TestLoadValid_WithHarness` — full harness config deserializes
- `TestLoadValid_NoHarness` — missing section uses defaults
- `TestValidate_BadGlob` — invalid glob fails
- `TestValidate_BadEffect` — unknown effect fails
- `TestDefaultPolicy_DeniesProtectedSurface` — file_write to `.xylem/HARNESS.md` returns Deny
- `TestDefaultPolicy_RequiresApprovalForPush` — git_push returns RequireApproval
- `TestDefaultPolicy_AllowsGeneral` — phase_execute returns Allow
- `TestLoadWithObservability` — full observability config
- `TestLoadWithObservabilityDefaults` — absent config = enabled, rate 1.0
- `TestLoadWithCostBudget` — budget config loads
- `TestValidateInvalidSampleRate` — negative/above 1.0 rejected

### 15.3 Cost estimation

New `estimate_test.go`:
- `TestEstimateTokens` — len/4 for known strings
- `TestEstimateCost` — arithmetic with known pricing
- `TestEstimateCostNilPricing` — returns 0
- `TestLookupPricingExactMatch` — "claude-sonnet-4" matches
- `TestLookupPricingPrefixMatch` — "claude-sonnet-4-20250514" matches "claude-sonnet-4"
- `TestLookupPricingNoMatch` — "unknown-model" returns nil
- `TestLookupPricingLongestPrefix` — "claude-haiku-4-5" matches "claude-haiku-4" not "claude-haiku-3"

Property tests:
- `TestProp_EstimateCostNonNegative` — cost >= 0 for all non-negative inputs
- `TestProp_EstimateTokensMonotonic` — longer strings produce >= token counts
- `TestProp_LookupPricingDeterministic` — same model always produces same result

### 15.4 Observability attributes

New `vessel_test.go`:
- `TestVesselSpanAttributes` — verify keys and values
- `TestVesselSpanAttributesRefOmitted` — empty ref = fewer attributes
- `TestPhaseSpanAttributes` — verify all fields
- `TestPhaseResultAttributes` — verify formatting
- `TestGateSpanAttributes` — verify boolean/int formatting

### 15.5 Evidence package

New `evidence_test.go`:
- `TestLevelValid` — all named levels valid, arbitrary strings rejected
- `TestLevelRank` — ordering: Proved > MechanicallyChecked > BehaviorallyChecked > ObservedInSitu > Untyped
- `TestManifestBuildSummary` — correct counts by level
- `TestManifestStrongestLevel` — returns highest-ranked passing claim
- `TestSaveLoadManifest` — JSON round-trip to temp directory

### 15.6 Workflow extension

Add to existing `workflow_test.go`:
- `TestValidateGateWithEvidence` — valid evidence metadata parses
- `TestValidateGateWithPartialEvidence` — only claim+level parses
- `TestValidateGateWithoutEvidence` — existing gates unchanged
- `TestValidateGateWithInvalidLevel` — rejected

### 15.7 Eval suite helpers

Python unit tests for `xylem_verify.py` using fixture JSON files (no running xylem instance):

- `test_find_vessel_dir_single` — finds the one vessel dir
- `test_find_vessel_dir_none` — asserts when no vessel dir exists
- `test_find_vessel_dir_multiple` — asserts when multiple vessel dirs exist
- `test_load_summary_parses_all_fields` — round-trip from fixture JSON
- `test_load_evidence_parses_claims` — evidence manifest with multiple claims
- `test_load_evidence_missing_file` — returns None gracefully
- `test_assert_vessel_completed_passes` — completed state accepted
- `test_assert_vessel_completed_fails` — failed state rejected
- `test_assert_phases_completed` — subset check logic
- `test_assert_gates_passed` — gate_passed field check
- `test_assert_evidence_level_minimum` — rank comparison (behaviorally_checked >= behaviorally_checked, not >= mechanically_checked)
- `test_compute_reward_equal_weights` — 2/3 checks pass = 0.667
- `test_compute_reward_weighted` — weighted scoring arithmetic
- `test_compute_reward_empty` — zero checks = 0.0
- `test_write_reward_format` — writes "0.8000\n" format

These tests live at `.xylem/eval/helpers/test_xylem_verify.py` and run via `pytest`.

### 15.8 Runner integration

Add to existing `runner_test.go` using `mockCmdRunner` and `mockWorktree`:
- `TestRunVessel_ProtectedSurfaceViolation` — phase modifies protected file, vessel fails
- `TestRunVessel_PolicyDeniesPhase` — deny rule prevents execution
- `TestRunVessel_PolicyAllowsPhase` — allow rule permits execution
- `TestRunVessel_NoHarnessConfig_Passthrough` — nil Intermediary, all phases run
- `TestRunVessel_AuditLogRecordsDecision` — audit log contains entry after phase
- `TestRunVessel_BudgetExceededFailsVessel` — vessel fails when budget exhausted
- `TestRunVessel_NoBudgetNoEnforcement` — nil budget = no enforcement
- `TestRunVessel_SummaryWrittenOnCompletion` — summary.json exists and parses
- `TestRunVessel_SummaryWrittenOnFailure` — partial summary on failure
- `TestRunVessel_EvidenceClaimsAccumulate` — claims from multiple gates collected
- `TestRunVessel_NilTracerNoSpans` — no panic when Tracer is nil
- `TestRunVessel_PolicyDenialRecordedInSpan` — error recorded on phase span

### 15.9 Reporter

Add to existing `reporter_test.go`:
- `TestVesselCompleted_NilManifest` — output identical to today
- `TestVesselCompleted_WithEvidence` — evidence table present
- `TestVesselCompleted_MixedPassFail` — checkmark/X rendered correctly

### 15.10 Verification command

After all implementation is complete:

```bash
cd cli && go vet ./... && go build ./cmd/xylem && go test ./...
```

All existing tests must continue to pass. No regressions.
