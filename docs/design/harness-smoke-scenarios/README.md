# Harness Implementation Smoke Scenarios

Smoke scenarios for the [xylem harness implementation spec](../xylem-harness-impl-spec.md). Each file covers one workstream or cross-cutting concern. Use this index to find scenarios relevant to the code you are implementing or testing.

**Total: 156 scenarios across 6 files.**

## Index

| File | Workstream | Spec sections | Scenarios | Covers |
|------|-----------|---------------|-----------|--------|
| [ws1-config-surface-policy.md](ws1-config-surface-policy.md) | WS1 | 2, 3, 4 | 32 | Config schema extensions, protected surface verification (SHA256 snapshots, Compare), policy enforcement (Intermediary wiring, audit log, CLI startup) |
| [ws3-observability-cost.md](ws3-observability-cost.md) | WS3 | 5, 6 | 32 | Span hierarchy (drain_run > vessel > phase > gate), attribute helpers, tracer init/shutdown, EstimateTokens, EstimateCost, LookupPricing, budget enforcement |
| [ws3-summary-artifacts.md](ws3-summary-artifacts.md) | WS3 | 7 | 20 | VesselSummary/PhaseSummary types, SaveVesselSummary, vesselRunState accumulator, completeVessel signature change, failure-path summaries |
| [ws4-evidence-model.md](ws4-evidence-model.md) | WS4 | 8 | 24 | Evidence Level/Claim/Manifest types, GateEvidence workflow extension, buildGateClaim, reporter VesselCompleted with evidence table |
| [ws5-eval-suite.md](ws5-eval-suite.md) | WS5 | 9 | 18 | Eval scaffold (harbor.yaml, helpers, rubrics), scenario directory convention, xylem_verify API, deferred items confirmation |
| [ws6-cross-cutting.md](ws6-cross-cutting.md) | WS6 | 10, 11, 12, 13 | 30 | Error precedence chain, prompt-only vessel handling, orchestrated execution (race safety, span propagation), backwards compatibility |

## How to use

**Implementing a package?** Find the file matching your workstream. Each scenario has:
- `Spec ref` linking to the exact spec section
- `Preconditions` describing setup state
- `Action` describing what to trigger
- `Expected outcome` with concrete, assertable results
- `Verification` describing how to confirm in a test

**Writing tests?** Scenarios map directly to test cases. The scenario ID (e.g. S14) is stable within each file and can be referenced in test names (e.g. `TestS14_CompareDetectsModifiedFile`).

**Reviewing a PR?** Check whether the PR's workstream scenarios are all covered by the implementation's test suite.

## Scenario format

```
### S{N}: {Title}
**Spec ref:** Section {X.Y}
**Preconditions:** {setup state}
**Action:** {what happens}
**Expected outcome:** {concrete, verifiable result}
**Verification:** {how to confirm}
```
