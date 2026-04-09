# Failed Vessel Recovery Spec

**Status:** Proposed
**Date:** 2026-04-09
**Scope:** Generalize failed-vessel re-enqueue logic from source-only fingerprint checks to remediation-aware recovery
**Inputs:** Research note “handling failed vessels in xylem without blind reenqueues” (2026-04-09), [10 Principles Alignment for xylem](10-principles-alignment.md), [SoTA Agent Harness Spec](sota-agent-harness-spec.md)

## 1. Purpose

xylem already prevents obvious fail loops by refusing to re-enqueue work when the latest failed vessel still matches the same source input. That invariant is good and should stay. The gap is that xylem currently has no first-class notion of **material remediation** beyond source-text change, so harness fixes, workflow fixes, structured failure diagnosis, or issue decomposition cannot unlock a retry in a principled way.

This spec defines a recovery system that keeps the anti-loop guard, adds deterministic failure handling, introduces a bounded diagnosis path for ambiguous failures, and unlocks retries only when a **composite remediation fingerprint** proves that something relevant changed.

## 2. Goals

1. Preserve the current “do not blindly retry identical failed work” invariant.
2. Allow safe re-enqueue when remediation happened outside the source text itself.
3. Keep the cheap path deterministic for obvious transient failures.
4. Reuse existing xylem review artifacts and the existing `lessons` workflow instead of creating a second memory system.
5. Keep recovery decisions inspectable, schedulable, and testable.

## 3. Non-goals

- Replacing the existing `lessons` workflow.
- Adding a new vessel state for recovery handling.
- Auto-retrying every failed or timed-out vessel.
- Redesigning queue storage away from JSONL.
- Solving every failure mode in one phase; rollout is incremental.

## 4. Current baseline

Today:

- `cli/internal/source/github.go` blocks re-enqueue when the latest vessel for an issue is `failed` or `timed_out` with the same `source_input_fingerprint`.
- `cli/internal/source/github_pr.go` uses the same source-fingerprint idea for PR workflows, but the blocking rules are still source-centric and workflow-specific.
- `cli/internal/runner/summary.go` and `cli/internal/review/load.go` already persist and load `summary.json`, evidence manifests, cost reports, budget alerts, and eval reports under `.xylem/phases/<vessel>/`.
- `cli/internal/lessons/lessons.go` already mines failed and timed-out runs into fleet-level HARNESS updates.

The missing piece is a recovery artifact and gating model that tells the scanner whether a prior failure is still materially identical.

## 5. User and operator workflows

### 5.1 Transient failure auto-retry

1. A vessel fails because of a rate limit, temporary network issue, or short-lived tool outage.
2. xylem writes a recovery artifact classifying the failure as `transient`, with a retry cap and next eligible retry time.
3. The scanner continues to suppress immediate re-enqueue.
4. Once cooldown expires, the scanner may enqueue exactly one retry if the remediation fingerprint changed because the retry window advanced and the retry budget is still available.

### 5.2 Ambiguous or repeated failure

1. A vessel fails and deterministic classification is low-confidence, or the same normalized failure repeats.
2. xylem runs a targeted diagnosis workflow against persisted artifacts.
3. The workflow emits a structured decision: `retry`, `split_issue`, `request_info`, `harness_patch`, `human_escalation`, or `suppress`.
4. The original work remains blocked until the decision’s retry preconditions are satisfied.

### 5.3 Harness or workflow fix unlocks retry

1. A vessel failed because the harness or workflow was missing guidance or validation.
2. `lessons` or a manual fix lands a relevant HARNESS/workflow change.
3. The new harness/workflow digest changes the remediation fingerprint.
4. The scanner may enqueue a retry without requiring the issue or PR body to change.

### 5.4 Scope/spec failures prefer refinement

1. Recovery class is `scope_gap` or `spec_gap`.
2. xylem does not retry the original failed vessel automatically.
3. The recommended path is issue refinement, child-task creation, or explicit operator input.
4. Only the refined work item is eligible for a new vessel.

## 6. Proposed behavior

### 6.1 Add a first-class recovery artifact

For every vessel that ends in `failed` or `timed_out`, xylem MUST persist `.xylem/phases/<vesselID>/failure-review.json`.

Suggested v1 shape:

```json
{
  "vessel_id": "issue-42",
  "failure_fingerprint": "fail-...",
  "source_ref": "https://github.com/owner/repo/issues/42",
  "workflow": "fix-bug",
  "failed_phase": "verify",
  "class": "transient|spec_gap|scope_gap|harness_gap|unknown",
  "confidence": 0.0,
  "recommended_action": "retry|split_issue|request_info|harness_patch|human_escalation|suppress",
  "retry_count": 0,
  "retry_cap": 2,
  "retry_after": "2026-04-10T00:00:00Z",
  "requires_harness_change": false,
  "requires_source_change": false,
  "requires_decision_refresh": false,
  "evidence_paths": [
    "phases/issue-42/summary.json",
    "phases/issue-42/evidence-manifest.json"
  ],
  "hypothesis": "short explanation",
  "unlock": {
    "source_input_fingerprint": "src-...",
    "harness_digest": "har-...",
    "workflow_digest": "wf-...",
    "decision_digest": "dec-..."
  }
}
```

This artifact is the machine-readable recovery contract. It replaces freeform “why did this fail?” reasoning with structured data that other packages can consume.

### 6.2 Deterministic classifier first

At vessel completion time, xylem SHOULD run a deterministic classifier before invoking any diagnosis workflow. The classifier uses:

- terminal vessel state (`failed` or `timed_out`);
- failed phase name and error message;
- evidence manifest claims;
- eval report outcome;
- normalized tool/runtime errors.

Initial classes:

| Class | Typical signals | Default action |
| --- | --- | --- |
| `transient` | rate limit, network, temporary auth/tool outage, flaky dependency | capped backoff retry |
| `spec_gap` | missing reproduction steps, ambiguous expected behavior, missing operator inputs | request info / refine issue |
| `scope_gap` | repeated partial progress, broad acceptance surface, too many unresolved requirements | split issue / planning follow-up |
| `harness_gap` | repeated pattern across vessels, missing HARNESS rule, missing workflow check | lessons / harness patch |
| `unknown` | low-confidence or conflicting evidence | diagnosis workflow / human escalation |

The deterministic classifier MUST be the default path. Diagnosis is only for ambiguous or repeated cases.

### 6.3 Add a targeted diagnosis workflow

xylem SHOULD add a recovery-specific workflow that reads persisted review artifacts and emits a stricter `failure-review.json` update for ambiguous or repeated failures.

Requirements:

- It MUST cite exact artifact paths used as evidence.
- It MUST output the same structured schema as the deterministic classifier.
- It MUST define retry preconditions explicitly.
- It MUST NOT authorize a retry just because “the agent thinks it will work now.”

This workflow is a per-failure decision maker, not a fleet-level institutional-memory job. `lessons` remains fleet-level.

### 6.4 Replace source-only gating with remediation-aware gating

The scanner SHOULD stop treating `source_input_fingerprint` as the only unblock signal. Instead, it SHOULD compare a persisted remediation fingerprint:

`hash(source_input_fingerprint, harness_digest, workflow_digest, decision_digest, remediation_epoch)`

Where:

- `source_input_fingerprint` is the current issue/PR fingerprint.
- `harness_digest` hashes the effective HARNESS inputs relevant to the vessel.
- `workflow_digest` hashes the effective workflow definition used for the vessel.
- `decision_digest` hashes the latest recovery decision artifact.
- `remediation_epoch` is an explicit unlock dimension for capped retries and future policy changes.

Re-enqueue is allowed only when:

1. the current recovery action is `retry`;
2. required cooldown/cap constraints pass; and
3. the new remediation fingerprint differs from the fingerprint stored on the failed or timed-out vessel.

### 6.5 Keep queue state simple

This design does **not** add a new `VesselState`.

- Failed and timed-out vessels stay terminal.
- Retries are represented as new vessels, linked via `RetryOf`.
- Existing `failed -> pending` support remains available for manual/operator-driven retry flows, but scanner-driven recovery should prefer enqueueing a new vessel with lineage.

### 6.6 Keep `lessons` narrow and valuable

`lessons` SHOULD continue to mine repeated failures into HARNESS updates and review PRs. It SHOULD NOT become the authority that approves individual retries.

Instead:

- per-vessel recovery decisions live in `failure-review.json`;
- fleet-level repeated-pattern synthesis stays in `reviews/lessons.json` and `reviews/lessons.md`.

## 7. State and data model impacts

### 7.1 Queue and vessel metadata

No new vessel state is required.

On newly enqueued retry vessels:

- set top-level `RetryOf` to the parent failed/timed-out vessel ID;
- keep existing `source_input_fingerprint`;
- add the following metadata fields:

  - `remediation_fingerprint`;
  - `recovery_class`;
  - `recovery_action`;
  - `recovery_unlocked_by` (`source`, `harness`, `workflow`, `decision`, `cooldown`);
  - `failure_fingerprint`.

### 7.2 Summary and review artifacts

`runner.VesselSummary` and `runner.ReviewArtifacts` SHOULD gain an optional `failure_review` path so `review.LoadRuns` can reconstruct the recovery context exactly the same way it already reconstructs evidence, cost, and eval data.

### 7.3 New package surface

Expected implementation touchpoints:

- `cli/internal/runner/` — build and persist `failure-review.json`
- `cli/internal/review/` — load the artifact
- `cli/internal/source/github.go` and `github_pr.go` — remediation-aware dedup logic
- `cli/internal/lessons/` — optionally consume failure classes as signals, but not as retry authority
- `cli/internal/queue/` — no state changes required; lineage and metadata only

## 8. Rollout and backward compatibility

### Phase 1 — artifact-only

- Write `failure-review.json` for `failed` and `timed_out`.
- Extend review loading and reporting.
- Leave current scan-time blocking unchanged.

### Phase 2 — deterministic policy

- Turn obvious transient failures into capped cooldown retries.
- Keep source-only blocking as the fallback for everything else.

### Phase 3 — diagnosis workflow

- Invoke diagnosis only for low-confidence or repeated failures.
- Require structured output identical to the artifact schema.

### Phase 4 — remediation-aware gating

- Switch scanners from source-only comparison to composite remediation fingerprints.
- Record which dimension unlocked each retry.

Backward compatibility rules:

- Legacy runs without `failure-review.json` continue to behave exactly as they do today.
- Missing digests are treated as empty values; this preserves existing source-fingerprint behavior until the new artifacts exist.
- Existing `lessons` and review commands continue to work when the new artifact is absent.

## 9. Observability

Add recovery metrics and traces from day one:

- failures by `class`, `workflow`, and `phase`;
- retries attempted, retries suppressed, and retry outcome;
- retries unlocked by dimension (`source`, `harness`, `workflow`, `decision`, `cooldown`);
- diagnosis workflow invocation count, latency, and outcome;
- “recovered after remediation” rate;
- scope/spec failures converted into refined child work;
- human escalations for repeated `unknown` failures.

The recovery artifact path SHOULD be included in run summaries and any future workflow-health reporting so agents and operators can inspect the exact decision basis.

## 10. Risks and open questions

1. **Over-classification risk:** early deterministic rules may be too coarse. Mitigation: keep `unknown` as an explicit escape hatch and measure misclassification.
2. **Digest drift risk:** hashing too much data will create noisy retries; hashing too little will miss real remediation. Mitigation: start with source, HARNESS, workflow, and decision digests only.
3. **Workflow-specific exceptions:** some workflows may legitimately want different timeout handling. Mitigation: keep the remediation gate generic but allow workflow-specific policy overrides.
4. **Diagnosis cost creep:** a meta-workflow on every failure would be expensive. Mitigation: invoke only on ambiguity or repetition thresholds.

Open question for implementation: whether the diagnosis workflow ships as a built-in workflow, a scaffolded YAML workflow, or both. The recovery artifact schema and gating rules in this spec do not depend on that packaging choice.

## 11. Testing and acceptance guidance

### 11.1 Unit coverage

Add unit tests for:

- deterministic failure classification;
- remediation fingerprint calculation stability;
- scanner dedup behavior when only source changes;
- scanner dedup behavior when only HARNESS/workflow/decision digest changes;
- cooldown and retry-cap enforcement;
- backward compatibility when `failure-review.json` is absent.

### 11.2 Integration and DTU coverage

Add DTU scenarios for:

1. transient failure retries once cooldown expires;
2. harness change unlocks a retry without source edit;
3. spec-gap failure stays blocked and recommends refinement;
4. ambiguous repeated failure invokes diagnosis and stays blocked until decision change;
5. `lessons` still clusters repeated failures correctly after recovery artifacts exist.

### 11.3 Acceptance criteria

This spec is complete when xylem can demonstrate all of the following:

1. An unchanged failed vessel is still not blindly re-enqueued.
2. A materially remediated failed vessel can be retried without editing the source issue/PR text.
3. Obvious transient failures retry automatically under explicit caps.
4. Spec-gap and scope-gap failures route to refinement instead of churn.
5. Recovery decisions are reconstructable from persisted artifacts alone.
