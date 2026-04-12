# Smoke Scenarios: Workstream 1 — Protect Control Surfaces and Mediate High-Risk Actions

**Spec ref:** `docs/design/xylem-harness-impl-spec.md` sections 2–4
**Date:** 2026-03-31
**Scope:** Config schema extensions (§2), protected surface verification (§3), policy enforcement (§4)

---

> **Numbering:** S1–S32 follow the ordered list in the task brief. Scenarios S29–S32 cover config helper methods and validation that logically belong with §2 but were assigned higher numbers in the brief — they appear in §2 here for reading coherence.

## Section 2 — Config schema extensions

### S1: Config loads with full harness section

**Spec ref:** Section 2.1, 2.5

**Preconditions:** A `.xylem.yml` file containing all new top-level sections — `harness`, `observability`, and `cost` — as shown in the full YAML example in §2.5.

**Action:** Call `config.Load(".xylem.yml")`.

**Expected outcome:** Returns a non-nil `*Config` with no error. `cfg.Harness.AuditLog` equals `"audit.jsonl"`. `cfg.Harness.ProtectedSurfaces.Paths` contains four entries. `cfg.Harness.Policy.Rules` contains four entries. `cfg.Observability.Enabled` is non-nil and `*cfg.Observability.Enabled` is `true`. `cfg.Observability.SampleRate` equals `1.0`. `cfg.Cost.Budget` is non-nil with `MaxCostUSD == 10.0` and `MaxTokens == 1000000`.

**Verification:** Assert no error returned; assert each field value using direct struct access.

---

### S2: Config loads with no harness section (defaults activate)

**Spec ref:** Section 2.2, 2.3, 2.5

**Preconditions:** An existing valid `.xylem.yml` with no `harness`, `observability`, or `cost` keys.

**Action:** Call `config.Load(".xylem.yml")`.

**Expected outcome:** Returns a non-nil `*Config` with no error. `cfg.EffectiveProtectedSurfaces()` returns the four-element `DefaultProtectedSurfaces` slice. `cfg.EffectiveAuditLogPath()` returns `"audit.jsonl"`. `cfg.ObservabilityEnabled()` returns `true`. `cfg.ObservabilitySampleRate()` returns `1.0`. `cfg.VesselBudget()` returns `nil`. `cfg.BuildIntermediaryPolicies()` returns a single-element slice containing the default policy named `"default"`.

**Verification:** Assert no error; call each helper method and compare return values.

---

### S3: Config with `paths: ["none"]` disables surface protection

**Spec ref:** Section 2.2 (`EffectiveProtectedSurfaces`)

**Preconditions:** A `.xylem.yml` with:
```yaml
harness:
  protected_surfaces:
    paths: ["none"]
```

**Action:** Call `config.Load(".xylem.yml")`, then call `cfg.EffectiveProtectedSurfaces()`.

**Expected outcome:** Load returns no error. `EffectiveProtectedSurfaces()` returns `nil` — not `[]string{}` and not the defaults. The function must return the untyped nil, not an empty allocated slice.

**Verification:** Assert `result == nil` (Go nil slice equality, not just `len == 0`).

---

### S4: Config validation rejects invalid glob patterns in protected_surfaces

**Spec ref:** Section 2.4 (`validateHarness`)

**Preconditions:** A `.xylem.yml` with:
```yaml
harness:
  protected_surfaces:
    paths:
      - "[invalid-glob"
```

**Action:** Call `config.Load(".xylem.yml")`.

**Expected outcome:** Returns a non-nil error. The error message contains `"harness.protected_surfaces.paths"` and `"invalid glob"` and the pattern `"[invalid-glob"`.

**Verification:** Assert `err != nil`; assert `strings.Contains(err.Error(), "harness.protected_surfaces.paths")`.

---

### S5: Config validation rejects unknown policy effect values

**Spec ref:** Section 2.4 (`validateHarness`)

**Preconditions:** A `.xylem.yml` with:
```yaml
harness:
  policy:
    rules:
      - action: "file_write"
        resource: "*"
        effect: "approve_maybe"
```

**Action:** Call `config.Load(".xylem.yml")`.

**Expected outcome:** Returns a non-nil error. The error message references `"harness.policy.rules[0]"`, the string `"invalid effect"`, and the value `"approve_maybe"`.

**Verification:** Assert `err != nil`; assert `strings.Contains(err.Error(), "invalid effect")` and `strings.Contains(err.Error(), "approve_maybe")`.

---

### S6: Default policy denies file_write to `.xylem/HARNESS.md`

**Spec ref:** Section 2.3 (`DefaultPolicy`)

**Preconditions:** A `Config` with no policy rules configured (so `BuildIntermediaryPolicies()` returns the default policy).

**Action:** Call `cfg.BuildIntermediaryPolicies()`, construct an `Intermediary` from the result, then call `Evaluate` with intent `{Action: "file_write", Resource: ".xylem/HARNESS.md", AgentID: "vessel-001"}`.

**Expected outcome:** `PolicyResult.Effect` equals `intermediary.Deny`. `PolicyResult.MatchedRule` is non-nil with `Action == "file_write"` and `Resource == ".xylem/HARNESS.md"`.

**Verification:** Assert `result.Effect == intermediary.Deny`.

---

### S7: Default policy allows classified publication actions

**Spec ref:** Section 2.3 (`DefaultPolicy`)

**Preconditions:** A `Config` with no policy rules configured (default policy active).

**Action:** Construct an `Intermediary` from `cfg.BuildIntermediaryPolicies()`, then call `Evaluate` with intents for `{Action: "git_commit", Resource: "*", AgentID: "vessel-002"}`, `{Action: "git_push", Resource: "main", AgentID: "vessel-003"}`, and `{Action: "pr_create", Resource: "owner/name", AgentID: "vessel-004"}`.

**Expected outcome:** Each `PolicyResult.Effect` equals `intermediary.Allow`, with the wildcard allow rule recorded as the match.

**Verification:** Assert each result equals `intermediary.Allow`.

---

### S8: Default policy allows general phase_execute actions

**Spec ref:** Section 2.3 (`DefaultPolicy`)

**Preconditions:** A `Config` with no policy rules configured (default policy active).

**Action:** Construct an `Intermediary` from `cfg.BuildIntermediaryPolicies()`, call `Evaluate` with intent `{Action: "phase_execute", Resource: "lint", AgentID: "vessel-003"}`.

**Expected outcome:** `PolicyResult.Effect` equals `intermediary.Allow`. The matched rule has `Action == "*"` and `Resource == "*"`.

**Verification:** Assert `result.Effect == intermediary.Allow`.

---

### S29: ObservabilityConfig defaults apply when section is absent

**Spec ref:** Section 2.2 (`ObservabilityEnabled`, `ObservabilitySampleRate`)

**Preconditions:** A `Config` with `Observability` as its zero value (no YAML section provided).

**Action:** Call `cfg.ObservabilityEnabled()` and `cfg.ObservabilitySampleRate()`.

**Expected outcome:** `ObservabilityEnabled()` returns `true` (nil pointer treated as enabled). `ObservabilitySampleRate()` returns `1.0` (zero value for `SampleRate` triggers the default).

**Verification:** Assert both return values directly.

---

### S30: CostConfig with budget fields loads correctly

**Spec ref:** Section 2.1, 2.2 (`VesselBudget`)

**Preconditions:** A `Config` with:
```yaml
cost:
  budget:
    max_cost_usd: 5.0
    max_tokens: 500000
```

**Action:** Call `config.Load(".xylem.yml")`, then call `cfg.VesselBudget()`.

**Expected outcome:** `VesselBudget()` returns a non-nil `*cost.Budget` with `CostLimitUSD == 5.0` and `TokenLimit == 500000`.

**Verification:** Assert non-nil return; assert both numeric fields.

---

### S31: Validation rejects negative sample_rate

**Spec ref:** Section 2.4 (`validateObservability`)

**Preconditions:** A `.xylem.yml` with:
```yaml
observability:
  sample_rate: -0.5
```

**Action:** Call `config.Load(".xylem.yml")`.

**Expected outcome:** Returns a non-nil error containing `"observability.sample_rate"`.

**Verification:** Assert `err != nil`; assert `strings.Contains(err.Error(), "observability.sample_rate")`.

---

### S32: Validation rejects negative max_cost_usd

**Spec ref:** Section 2.4 (`validateCost`)

**Preconditions:** A `.xylem.yml` with:
```yaml
cost:
  budget:
    max_cost_usd: -1.0
```

**Action:** Call `config.Load(".xylem.yml")`.

**Expected outcome:** Returns a non-nil error containing `"cost.budget.max_cost_usd"`.

**Verification:** Assert `err != nil`; assert `strings.Contains(err.Error(), "cost.budget.max_cost_usd")`.

---

## Section 3 — Protected surface verification

### S9: TakeSnapshot on empty directory returns empty snapshot

**Spec ref:** Section 3.2 (`TakeSnapshot`)

**Preconditions:** A temporary empty directory created via `t.TempDir()`. Patterns: `[".xylem/HARNESS.md", ".xylem.yml"]`.

**Action:** Call `surface.TakeSnapshot(dir, patterns)` where `dir` is the temp directory path.

**Expected outcome:** Returns an empty `Snapshot` with no error. `snapshot.Files` is nil or an empty slice (len == 0). No error is returned because missing files for a pattern are silently skipped.

**Verification:** Assert `err == nil`; assert `len(snapshot.Files) == 0`.

---

### S10: TakeSnapshot matches globs and hashes correctly

**Spec ref:** Section 3.2 (`TakeSnapshot`)

**Preconditions:** A temporary directory created via `t.TempDir()` containing:
- `.xylem/HARNESS.md` with content `"harness content"`
- `.xylem.yml` with content `"config content"`
- `src/main.go` with content `"package main"` (not matched by patterns)

Patterns: `[".xylem/HARNESS.md", ".xylem.yml"]`.

**Action:** Call `surface.TakeSnapshot(dir, patterns)` where `dir` is the temp directory path.

**Expected outcome:** Returns a `Snapshot` with exactly two entries. Each entry has a non-empty `Hash` field. The hash for `.xylem/HARNESS.md` equals the lowercase hex-encoded SHA256 of `"harness content"`. `src/main.go` is not included. No error is returned.

**Verification:** Assert `len(snapshot.Files) == 2`; compute expected SHA256 hashes in test and compare.

---

### S11: TakeSnapshot is deterministic (two calls return identical results)

**Spec ref:** Section 3.2 (`TakeSnapshot`), §1.2 decision 1

**Preconditions:** A temporary directory created via `t.TempDir()` containing `.xylem.yml` with content `"stable content"`. Patterns: `[".xylem.yml"]`.

**Action:** Call `surface.TakeSnapshot(dir, patterns)` twice without modifying any files between calls.

**Expected outcome:** Both calls return identical `Snapshot` values: same number of files, same paths, same hashes. No error on either call.

**Verification:** Assert `reflect.DeepEqual(snap1, snap2)`.

---

### S12: TakeSnapshot sorts results by path

**Spec ref:** Section 3.2 (`TakeSnapshot`, "Invariant: sorted by Path")

**Preconditions:** A temporary directory created via `t.TempDir()` containing:
- `.xylem/workflows/fix-bug.yaml`
- `.xylem/workflows/implement-feature.yaml`
- `.xylem.yml`

Patterns: `[".xylem/workflows/*.yaml", ".xylem.yml"]`.

**Action:** Call `surface.TakeSnapshot(dir, patterns)` where `dir` is the temp directory path.

**Expected outcome:** `snapshot.Files` is sorted in lexicographic ascending order by `Path`. The invariant is that entries are always sorted — regardless of filesystem traversal order.

**Verification:** Assert each `snapshot.Files[i].Path <= snapshot.Files[i+1].Path` for all consecutive pairs.

---

### S13: Compare detects no violations for identical snapshots

**Spec ref:** Section 3.2 (`Compare`)

**Preconditions:** Two identical `Snapshot` values:
```go
snap := surface.Snapshot{Files: []surface.FileHash{{Path: ".xylem.yml", Hash: "abc123"}}}
```

**Action:** Call `surface.Compare(snap, snap)`.

**Expected outcome:** Returns an empty (nil or zero-length) `[]Violation`. No panics.

**Verification:** Assert `len(violations) == 0`.

---

### S14: Compare detects a modified file (changed hash)

**Spec ref:** Section 3.2 (`Compare`)

**Preconditions:**
```go
before := surface.Snapshot{Files: []surface.FileHash{{Path: ".xylem.yml", Hash: "aaa"}}}
after  := surface.Snapshot{Files: []surface.FileHash{{Path: ".xylem.yml", Hash: "bbb"}}}
```

**Action:** Call `surface.Compare(before, after)`.

**Expected outcome:** Returns a single `Violation` with `Path == ".xylem.yml"`, `Before == "aaa"`, `After == "bbb"`.

**Verification:** Assert `len(violations) == 1`; assert all three fields of `violations[0]`.

---

### S15: Compare detects a deleted file

**Spec ref:** Section 3.2 (`Compare`)

**Preconditions:**
```go
before := surface.Snapshot{Files: []surface.FileHash{{Path: ".xylem/HARNESS.md", Hash: "ddd"}}}
after  := surface.Snapshot{Files: []surface.FileHash{}}
```

**Action:** Call `surface.Compare(before, after)`.

**Expected outcome:** Returns a single `Violation` with `Path == ".xylem/HARNESS.md"`, `Before == "ddd"`, `After == "deleted"`.

**Verification:** Assert `violations[0].After == "deleted"`.

---

### S16: Compare detects a created file (new file in after)

**Spec ref:** Section 3.2 (`Compare`)

**Preconditions:**
```go
before := surface.Snapshot{Files: []surface.FileHash{}}
after  := surface.Snapshot{Files: []surface.FileHash{{Path: ".xylem/workflows/new.yaml", Hash: "eee"}}}
```

**Action:** Call `surface.Compare(before, after)`.

**Expected outcome:** Returns a single `Violation` with `Path == ".xylem/workflows/new.yaml"`, `Before == "absent"`, `After == "eee"`.

**Verification:** Assert `violations[0].Before == "absent"`.

---

## Section 4 — Policy enforcement

### S17: Runner with nil Intermediary skips policy check

**Spec ref:** Section 4.3

**Preconditions:** A `Runner` with `Intermediary == nil`. A vessel with one prompt-type phase. Use a stub `CommandRunner` that records calls and returns empty output successfully.

**Action:** Execute the vessel through `runVessel` (or the equivalent orchestrated path).

**Expected outcome:** The vessel completes without error. The stub `CommandRunner` was called, confirming phase execution proceeded normally. No policy-related error message appears in the vessel failure state.

**Verification:** Assert vessel final state is `completed`; assert stub was invoked.

---

### S18: Runner policy denies a phase — vessel fails with "denied by policy"

**Spec ref:** Section 4.3 (policy check block)

**Preconditions:** A `Runner` with an `Intermediary` configured with a single rule: `{Action: "*", Resource: "*", Effect: intermediary.Deny}`. A vessel with one prompt-type phase named `"solve"`.

**Action:** Execute the vessel.

**Expected outcome:** The vessel transitions to `failed` state. The failure message contains `"denied by policy"` and the phase name `"solve"`. The stub `CommandRunner` is never called (no phase execution occurred).

**Verification:** Assert vessel state is `failed`; assert failure message contains `"denied by policy"` and `"solve"`; assert stub was not invoked.

---

### S19: Runner policy require_approval — vessel fails with approval message

**Spec ref:** Section 4.3 (policy check block), §1.2 decision 3

**Preconditions:** A `Runner` with an `Intermediary` whose policy maps the phase's action to `RequireApproval`. A vessel with one prompt-type phase named `"deploy"`.

**Action:** Execute the vessel.

**Expected outcome:** The vessel transitions to `failed` state. The failure message contains `"requires approval"` and `"deploy"` and some indication that automatic approval is not yet supported.

**Verification:** Assert vessel state is `failed`; assert failure message contains `"requires approval"` and `"automatic approval not yet supported"`.

---

### S20: Surface pre-snapshot is taken before phase execution

**Spec ref:** Section 4.3 (surface pre-snapshot block)

**Preconditions:** A `Runner` with a non-empty `EffectiveProtectedSurfaces()` list. A vessel with a prompt-type phase. A stub `CommandRunner` that records when it is called. The worktree directory contains a `.xylem.yml` file.

**Action:** Execute the vessel using a stub `CommandRunner` that records its invocation timestamp and a test-double snapshot function (or a real temp directory that the snapshot reads before the stub runs).

**Expected outcome:** `surface.TakeSnapshot` is called and returns a non-empty snapshot before the `CommandRunner` records its invocation. The pre-snapshot is taken even when the policy check passes.

**Verification:** In the test double, assert that the snapshot timestamp (or call-order index) precedes the CommandRunner invocation index.

---

### S21: Surface post-verification detects mutation — vessel fails

**Spec ref:** Section 4.3 (surface post-verification block)

**Preconditions:** A `Runner` configured with protected surface patterns. A stub `CommandRunner` that, when invoked for a phase, modifies `.xylem.yml` in the worktree (simulating an agent that mutated a protected file). The worktree contains `.xylem.yml` initially.

**Action:** Execute the vessel.

**Expected outcome:** The vessel transitions to `failed` state. The failure message contains `"violated protected surfaces"` and the path `.xylem.yml`.

**Verification:** Assert vessel state is `failed`; assert failure message contains `"violated protected surfaces"` and `".xylem.yml"`.

---

### S22: Audit log records policy decisions

**Spec ref:** Section 4.3 (audit log append inside policy check block)

**Preconditions:** A `Runner` with an `Intermediary` and a configured `AuditLog` pointing to a temporary JSONL file. A vessel with one prompt-type phase.

**Action:** Execute the vessel. Use a policy that allows the phase so the audit entry is always written regardless of the phase outcome.

**Expected outcome:** After the vessel completes (or fails), the audit log JSONL file contains at least one entry. The entry has `intent.action` set to `"phase_execute"`, `intent.agent_id` equal to the vessel ID, and `decision` set to `"allow"`.

**Verification:** Call `auditLog.Entries()`, assert `len(entries) >= 1`, assert field values on `entries[0]`.

---

### S23: Audit log records surface violations

**Spec ref:** Section 4.3 (audit log append inside surface violation block)

**Preconditions:** A `Runner` with a configured `AuditLog` and protected surface patterns. A stub `CommandRunner` that mutates `.xylem.yml` during phase execution. Policy allows the phase to run.

**Action:** Execute the vessel.

**Expected outcome:** After the vessel fails, the audit log contains an entry with `intent.action == "file_write"`, `intent.resource == ".xylem.yml"`, and `decision == "deny"`.

**Verification:** Call `auditLog.Entries()`, find the `file_write` entry, assert all three fields.

---

### S24: phaseActionType returns "external_command" for command phases

**Spec ref:** Section 4.4 (`phaseActionType`)

**Preconditions:** A `workflow.Phase` with `Type == "command"`.

**Action:** Call `phaseActionType(phase)`.

**Expected outcome:** Returns the string `"external_command"`.

**Verification:** Assert `result == "external_command"`.

---

### S25: phaseActionType returns "phase_execute" for prompt phases

**Spec ref:** Section 4.4 (`phaseActionType`)

**Preconditions:** A `workflow.Phase` with `Type == ""` (the zero value, representing a prompt phase).

**Action:** Call `phaseActionType(phase)`.

**Expected outcome:** Returns the string `"phase_execute"`.

**Verification:** Assert `result == "phase_execute"`.

---

### S26: formatViolations produces human-readable output

**Spec ref:** Section 4.4 (`formatViolations`)

**Preconditions:**
```go
violations := []surface.Violation{
    {Path: ".xylem.yml",       Before: "abc", After: "xyz"},
    {Path: ".xylem/HARNESS.md", Before: "111", After: "deleted"},
}
```

**Action:** Call `formatViolations(violations)`.

**Expected outcome:** Returns a string containing both paths, their before/after values, and a separator between the two violations. Example acceptable output: `".xylem.yml (before: abc, after: xyz); .xylem/HARNESS.md (before: 111, after: deleted)"`.

**Verification:** Assert the returned string contains `".xylem.yml"`, `"before: abc"`, `"after: xyz"`, `".xylem/HARNESS.md"`, and `"deleted"`.

---

### S27: CLI wiring in drain.go creates Intermediary from config

**Spec ref:** Section 4.2 (drain.go `cmdDrain`)

**Preconditions:** A valid `.xylem.yml` with no harness section (defaults apply). The `drain` command is invoked (or the runner is constructed as `cmdDrain` would construct it).

**Action:** Inspect the `Runner` struct after `cmdDrain` constructs it, before `r.Drain()` is called.

**Expected outcome:** `r.Intermediary` is non-nil. `r.AuditLog` is non-nil. The audit log path is `config.RuntimePath(cfg.StateDir, "audit.jsonl")` (which resolves to `filepath.Join(cfg.StateDir, "state", "audit.jsonl")` in the standard layout). The intermediary's policies include the default policy with at least the `file_write` deny rule for `.xylem/HARNESS.md`.

**Verification:** In a unit test that stubs the queue and CommandRunner, assert `r.Intermediary != nil` and `r.AuditLog != nil` after construction.

---

### S28: CLI wiring in daemon.go creates Intermediary from config

**Spec ref:** Section 4.2 (daemon.go `runDrain`)

**Preconditions:** A valid `.xylem.yml` with no harness section (defaults apply). The `runDrain` function is invoked (or the runner is constructed as it would be).

**Action:** Inspect the `Runner` struct after `runDrain` constructs it.

**Expected outcome:** `r.Intermediary` is non-nil. `r.AuditLog` is non-nil. Both match the same construction logic as `cmdDrain` — same policy set, same audit log path derivation.

**Verification:** In a unit test or integration test, assert `r.Intermediary != nil` and `r.AuditLog != nil` after `runDrain` runs setup (before the drain loop).

---

## Manual Smoke Tests

For interactive verification of WS1 scenarios, use the DTU-based manual smoke tests below. These test the **real xylem CLI** against **twinned boundaries** defined in YAML manifests.

All WS1 manual smokes in this section now run from the checked-in fixture repo at `cli/internal/dtu/testdata/ws1-smoke-fixture/`. Do **not** reuse the live repo root `.xylem` tree here — that config targets `nicholls-inc/xylem` and different workflow layouts than the WS1 manifests.

**See also:** [Running Manual Smoke Tests](running-manual-smoke-tests.md) — General DTU setup and probe guide.

### Quick Reference

| Scenario IDs | Manifest | Fixture Config | Workflow | Test Focus | Manifest Path |
|---|---|---|---|---|---|
| S1, S8, S20, S22, S24–S25, S27–S28 | `ws1-policy-allow-happy-path` | `.xylem.yml` | `fix-bug-allow` | Policy allow, two-phase workflow, defaults | `cli/internal/dtu/testdata/ws1-policy-allow-happy-path.yaml` |
| S2, S29, S30 | `ws1-config-defaults-only` | `.xylem.defaults-only.yml` | `fix-bug-defaults` | Config with no harness section, defaults activate | `cli/internal/dtu/testdata/ws1-config-defaults-only.yaml` |
| S6, S18 | `ws1-policy-deny-blocks-phase` | `.xylem.policy-deny.yml` | `fix-bug-deny` | Policy deny prevents phase execution | `cli/internal/dtu/testdata/ws1-policy-deny-blocks-phase.yaml` |
| S7, S19 | `ws1-policy-require-approval` | `.xylem.require-approval.yml` | `fix-bug-require-approval` | Policy require_approval blocks phase | `cli/internal/dtu/testdata/ws1-policy-require-approval.yaml` |
| S10, S14, S21, S23 | `ws1-surface-violation` | `.xylem.surface-violation.yml` | `fix-bug-surface-violation` | Protected surface mutation detection | `cli/internal/dtu/testdata/ws1-surface-violation.yaml` |

### Setup (all scenarios)

```bash
cd /Users/harry.nicholls/repos/xylem/cli
go build ./cmd/xylem
XYLEM="$(pwd)/xylem"
FIXTURE_DIR="$(pwd)/internal/dtu/testdata/ws1-smoke-fixture"
cd "$FIXTURE_DIR"
```

### S1, S8, S20, S22, S24–S25, S27–S28: Policy Allow + Happy Path

**What it tests:** Policy allows phase execution; vessel completes normally; two-phase workflow (plan, implement).

**Manifest:** `ws1-policy-allow-happy-path.yaml`

**Run:**
```bash
eval "$("$XYLEM" dtu env --manifest ../ws1-policy-allow-happy-path.yaml)"
"$XYLEM" scan
"$XYLEM" drain
```

**Expected output:**
- `scan` finds 1 issue (#10) labeled "bug"
- `drain` dequeues the vessel, executes the fixture's `fix-bug-allow` workflow (`plan`, then `implement`)
- Vessel completes with state "completed"

**Verify:**
```bash
"$XYLEM" status
jq '.repositories[0].issues[0] | {number, labels}' "$XYLEM_DTU_STATE_PATH"
```

### S2, S29, S30: Config Defaults (No Harness Section)

**What it tests:** The fixture's `.xylem.defaults-only.yml` with no harness, observability, or cost sections; all defaults activate; vessel completes normally.

**Manifest:** `ws1-config-defaults-only.yaml`

**Run:**
```bash
CONFIG=.xylem.defaults-only.yml
eval "$("$XYLEM" --config "$CONFIG" dtu env --manifest ../ws1-config-defaults-only.yaml)"
"$XYLEM" --config "$CONFIG" scan
"$XYLEM" --config "$CONFIG" drain
```

**Expected output:**
- `scan` finds 1 issue (#50) labeled "bug"
- `drain` dequeues and executes the fixture's single-phase `fix-bug-defaults` workflow
- Vessel completes with state "completed"

**Verify:**
```bash
jq '.repositories[0].issues[0].title' "$XYLEM_DTU_STATE_PATH"
```

### S6, S18: Policy Deny Blocks Phase

**What it tests:** Policy rule denies phase_execute action; vessel fails before provider is invoked.

**Manifest:** `ws1-policy-deny-blocks-phase.yaml`

> **Targeted rerun update (2026-04-03): PASS.** The checked-in `.xylem.policy-deny.yml` now defines an explicit deny rule, so `drain` fails the vessel with `"phase solve denied by policy"` before the provider is invoked.

**Run:**
```bash
CONFIG=.xylem.policy-deny.yml
eval "$("$XYLEM" --config "$CONFIG" dtu env --manifest ../ws1-policy-deny-blocks-phase.yaml)"
"$XYLEM" --config "$CONFIG" scan
"$XYLEM" --config "$CONFIG" drain
```

**Expected output:**
- `scan` finds 1 issue (#20) labeled "bug"
- `drain` dequeues the vessel, enters the fixture's `fix-bug-deny` workflow, and **should block the `solve` phase with "denied by policy"** before invoking the provider
- Vessel fails with state "failed"

**Verify:**
```bash
"$XYLEM" --config "$CONFIG" status | grep -i failed
```

### S7, S19: Policy Require Approval

**What it tests:** Policy rule blocks phase with "requires approval" effect; vessel fails because automatic approval is not yet supported.

**Manifest:** `ws1-policy-require-approval.yaml`

> **Targeted rerun update (2026-04-03): PASS.** The checked-in `.xylem.require-approval.yml` now defines an explicit `phase_execute -> require_approval` rule, so `drain` fails the vessel with the documented approval message before the provider is invoked.

**Run:**
```bash
CONFIG=.xylem.require-approval.yml
eval "$("$XYLEM" --config "$CONFIG" dtu env --manifest ../ws1-policy-require-approval.yaml)"
"$XYLEM" --config "$CONFIG" scan
"$XYLEM" --config "$CONFIG" drain
```

**Expected output:**
- `scan` finds 1 issue (#30) labeled "bug"
- `drain` dequeues the vessel, enters the fixture's `fix-bug-require-approval` workflow, and **should block the `deploy` phase with "requires approval"** before invoking the provider
- Vessel fails with state "failed"

**Verify:**
```bash
"$XYLEM" --config "$CONFIG" status | grep -i approval
```

### S10, S14, S21, S23: Protected Surface Violation

**What it tests:** Phase execution mutates a protected surface (.xylem.yml); post-verification detects the violation; vessel fails with "violated protected surfaces" error.

**Manifest:** `ws1-surface-violation.yaml`

> **Current checked-in DTU result (2026-04-02): FAIL.** The latest smoke run showed `drain` exiting `0`, the vessel not failing, and no protected-surface error surfacing for the documented `.xylem.yml` mutation. Treat this as a known xylem bug until surface verification is enforced.

**Run:**
```bash
CONFIG=.xylem.surface-violation.yml
eval "$("$XYLEM" --config "$CONFIG" dtu env --manifest ../ws1-surface-violation.yaml)"
"$XYLEM" --config "$CONFIG" scan
"$XYLEM" --config "$CONFIG" drain
```

**Expected output:**
- `scan` finds 1 issue (#40) labeled "bug"
- `drain` dequeues the vessel, runs the fixture's `tamper` command phase, and **should detect mutation of the fixture repo's `.xylem.yml`**
- Vessel fails with state "failed" and error containing "violated protected surfaces"

**Verify:**
```bash
"$XYLEM" --config "$CONFIG" status | grep -i violation
```

### Probing boundaries

If a test fails, probe the boundary to separate xylem bugs from DTU issues:

```bash
# After eval'ing a DTU environment:

# Check if gh shim works
"$XYLEM" shim-dispatch gh --help

# View the event log (all shim results)
jq -c 'select(.kind=="shim_result")' "$XYLEM_DTU_EVENT_LOG_PATH"

# View the DTU state (issue labels, provider scripts)
jq '.repositories[0] | {issues, providers}' "$XYLEM_DTU_STATE_PATH"
```

**Rule:** If direct shim probes fail, the problem is in the manifest or DTU matching, not xylem. See [Running Manual Smoke Tests](running-manual-smoke-tests.md#probing-boundaries-directly) for detailed probe instructions.
