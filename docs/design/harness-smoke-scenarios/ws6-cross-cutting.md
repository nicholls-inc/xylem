# Smoke Scenarios — Unit 6: Cross-Cutting Concerns

Sections 10–13 of the xylem harness implementation spec: error precedence, prompt-only vessels,
orchestrated execution, and backwards compatibility.

---

## Section 10: Error precedence and evaluation order

### S1: Policy denial short-circuits before surface snapshot
**Spec ref:** Section 10, Stage 1
**Preconditions:** Runner configured with an `Intermediary` whose policy denies `phase_execute` for the vessel's workflow. Surface snapshot machinery is available.
**Action:** Runner begins executing a phase for a vessel that matches the deny rule.
**Expected outcome:** The vessel is marked `failed` with a message containing "denied by policy". No surface snapshot is taken (surface snapshot function is never called).
**Verification:** Check `queue.Vessel.State == "failed"` and `Vessel.Error` contains "denied by policy". Confirm no `.xylem/phases/<id>/` snapshot artefacts exist.

---

### S2: Surface pre-snapshot failure short-circuits before phase execution
**Spec ref:** Section 10, Stage 2
**Preconditions:** Policy check passes (or Intermediary is nil). Surface snapshot is configured but the snapshot function returns an error (e.g., protected glob points to a directory that cannot be read).
**Action:** Runner attempts to execute a phase.
**Expected outcome:** Vessel is marked `failed` with a message containing "snapshot failed". The phase execution subprocess (`RunPhase`) is never invoked.
**Verification:** `Vessel.State == "failed"`, `Vessel.Error` contains "snapshot failed". Phase output file (`.xylem/phases/<id>/<phase>.output`) does not exist.

---

### S3: Phase execution failure short-circuits before surface post-verification
**Spec ref:** Section 10, Stage 3
**Preconditions:** Policy check passes, pre-snapshot succeeds. The phase subprocess (`RunPhase`) returns a non-zero exit code.
**Action:** Runner runs the phase; subprocess fails.
**Expected outcome:** Vessel is marked `failed` with the phase execution error. Surface post-verification (`surface.Compare`) is never called.
**Verification:** `Vessel.State == "failed"`. No surface violation message in the error. Confirm post-snapshot compare function was not invoked (stub call count = 0).

---

### S4: Surface violation short-circuits before budget check
**Spec ref:** Section 10, Stage 4
**Preconditions:** Policy passes, pre-snapshot succeeds, phase execution succeeds. Post-verification detects that a protected file was modified.
**Action:** Runner runs post-snapshot comparison; a violation is found.
**Expected outcome:** Vessel is marked `failed` with a message containing "violated protected surfaces". Budget check (`costTracker.BudgetExceeded`) is never called.
**Verification:** `Vessel.State == "failed"`, error contains "violated protected surfaces". Cost tracker budget-exceeded check was not invoked (stub call count = 0).

---

### S5: Budget exceeded short-circuits before gate evaluation
**Spec ref:** Section 10, Stage 5
**Preconditions:** Policy passes, snapshots pass, phase execution succeeds, surface verification passes. Cost tracker reports budget exceeded after recording the phase's token cost.
**Action:** Runner records phase cost and checks the budget; budget is exceeded.
**Expected outcome:** Vessel is marked `failed` with a message containing "budget exceeded". Gate evaluation (`gate.RunCommandGate` / `gate.CheckLabel`) is never invoked.
**Verification:** `Vessel.State == "failed"`, error contains "budget exceeded". Gate evaluation stub not called.

---

### S6: Evidence collection failure is non-fatal
**Spec ref:** Section 10, Stage 7
**Preconditions:** All earlier stages (1–6) succeed. The `buildGateClaim` call encounters an error (e.g., evidence level string is unrecognised).
**Action:** Phase completes successfully up to Stage 6 (gate passes). Evidence collection fails.
**Expected outcome:** Vessel is marked `completed`, not `failed`. A warning is logged (contains "warn" and references the evidence collection failure). No panic occurs.
**Verification:** `Vessel.State == "completed"`. Log output contains a warning line about evidence collection. Summary artefact is still written.

---

### S7: Summary write failure is non-fatal
**Spec ref:** Section 10, Stage 8
**Preconditions:** All earlier stages (1–7) succeed. The `SaveVesselSummary` write fails (e.g., the phases directory is made read-only after creation).
**Action:** Runner attempts to write `summary.json`; the write fails.
**Expected outcome:** Vessel is marked `completed`, not `failed`. A warning is logged referencing the summary write failure.
**Verification:** `Vessel.State == "completed"`. Log output contains a warning about summary write. No `summary.json` file exists (write failed), but vessel state in queue is still "completed".

---

### S8: Claims from prior completed phases preserved when a later phase fails
**Spec ref:** Section 10, "Evidence from failed phases"
**Preconditions:** A two-phase workflow. Phase 1 completes successfully and its gate produces an evidence claim. Phase 2 fails at phase execution (Stage 3).
**Action:** Runner executes both phases sequentially; phase 2 fails.
**Expected outcome:** The evidence manifest (if written as a partial artefact) contains the claim from Phase 1. Phase 2 contributes no claim to the manifest.
**Verification:** `evidence-manifest.json` (if present) contains exactly one claim with `phase == "phase-1"`. No claim with `phase == "phase-2"` exists.

---

### S9: Claims from a failed phase are discarded
**Spec ref:** Section 10, "Evidence from failed phases"
**Preconditions:** A single-phase workflow. The phase fails at Stage 3 (phase execution error); no evidence claim is generated for it.
**Action:** Runner attempts to collect evidence for the failing phase.
**Expected outcome:** No evidence claim for the failed phase appears in any output artefact. The manifest is either absent or contains zero claims.
**Verification:** `evidence-manifest.json` is absent or `claims` array is empty. `Vessel.State == "failed"`.

---

### S10: Phase span is always ended, even on failure
**Spec ref:** Section 10, "Span lifecycle across failures"
**Preconditions:** Runner is configured with a non-nil Tracer. A phase fails at any stage (1–5).
**Action:** Runner processes a phase that fails.
**Expected outcome:** The phase span is ended (its `End()` method is called) even though the phase failed. The span is not left open/dangling.
**Verification:** Tracer stub records that `End()` was called on the phase span exactly once after the failure. No open spans remain after the vessel's goroutine exits.

---

### S11: Error recorded on span via RecordError before End
**Spec ref:** Section 10, "Span lifecycle across failures"
**Preconditions:** Runner is configured with a non-nil Tracer. A phase fails (e.g., policy denial or surface violation).
**Action:** Runner processes a phase that fails.
**Expected outcome:** The span's `RecordError` method is called with the failure error before `End` is called.
**Verification:** Tracer stub records call order: `RecordError(err)` appears before `End()` in the call log for the phase span.

---

## Section 11: Prompt-only vessels

### S12: Prompt-only vessel gets a vessel span
**Spec ref:** Section 11
**Preconditions:** Runner is configured with a non-nil Tracer. Vessel has `Prompt` set and `Workflow` empty (prompt-only path).
**Action:** Runner drains the vessel via `runPromptOnly`.
**Expected outcome:** A vessel-level span is started and ended for the vessel. The span carries the vessel ID as an attribute.
**Verification:** Tracer stub records one span with name matching "vessel" (or equivalent) and attribute `vessel.id == vessel.ID`. Span is ended after `runPromptOnly` returns.

---

### S13: Prompt-only vessel gets cost tracking
**Spec ref:** Section 11
**Preconditions:** Runner is configured with a cost tracker. Vessel has `Prompt` set and `Workflow` empty.
**Action:** Runner runs the prompt-only vessel; `RunPhase` returns successfully.
**Expected outcome:** Token usage is estimated from the prompt and output and recorded in the cost tracker. The summary artefact includes a non-zero cost estimate.
**Verification:** `summary.json` contains a `cost` field with a value greater than zero. Cost tracker stub records at least one `Record(...)` call with a positive token count.

---

### S14: Prompt-only vessel gets surface verification (pre and post snapshot)
**Spec ref:** Section 11
**Preconditions:** Runner is configured with surface verification globs. Vessel is prompt-only.
**Action:** Runner executes the prompt-only vessel.
**Expected outcome:** Surface pre-snapshot is taken before `RunPhase` is called. Surface post-verification is performed after `RunPhase` returns. If no protected files are modified, the vessel completes.
**Verification:** Surface stub records `TakeSnapshot()` called before `RunPhase` and `Compare()` called after. `Vessel.State == "completed"` when no violation occurs.

---

### S15: Prompt-only vessel does NOT get policy evaluation
**Spec ref:** Section 11
**Preconditions:** Runner is configured with an Intermediary (policy evaluator). Vessel is prompt-only.
**Action:** Runner executes the prompt-only vessel.
**Expected outcome:** `intermediary.Evaluate()` is never called. The vessel runs without policy checks.
**Verification:** Intermediary stub records zero calls to `Evaluate`. Vessel completes normally.

---

### S16: Prompt-only vessel does NOT get evidence claims
**Spec ref:** Section 11
**Preconditions:** Runner is configured with evidence collection logic. Vessel is prompt-only (no gates).
**Action:** Runner executes and completes the prompt-only vessel.
**Expected outcome:** No evidence manifest file is written. `buildGateClaim` is never invoked.
**Verification:** `.xylem/phases/<id>/evidence-manifest.json` does not exist. Evidence collection stub records zero calls.

---

### S17: Prompt-only vessel gets a summary artifact written
**Spec ref:** Section 11
**Preconditions:** Vessel is prompt-only. Runner completes execution successfully.
**Action:** Runner finishes the prompt-only vessel.
**Expected outcome:** A `summary.json` file is written to `.xylem/phases/<id>/`. The file is valid JSON and contains at least `state`, `vessel_id`, and `cost` fields.
**Verification:** File exists at the expected path. `json.Unmarshal` succeeds. `summary["state"] == "completed"` and `summary["vessel_id"] == vessel.ID`.

---

### S18: Prompt-only vessel summary has an empty Phases slice
**Spec ref:** Section 11
**Preconditions:** Vessel is prompt-only (single execution, no phases).
**Action:** Runner completes the prompt-only vessel and writes the summary.
**Expected outcome:** `summary.json` contains a `phases` field that is an empty array (or absent), reflecting that there are no named phases in a prompt-only execution.
**Verification:** `summary["phases"]` is either `[]` or the key is absent. The summary does not contain any phase entry with a name or status.

---

## Section 12: Orchestrated execution

### S19: vesselRunState is owned by runVesselOrchestrated, not shared across goroutines
**Spec ref:** Section 12.1
**Preconditions:** A workflow with at least two phases in the same wave (no dependencies between them), so they run concurrently.
**Action:** Runner executes the orchestrated workflow; both phases run in parallel goroutines.
**Expected outcome:** Each goroutine writes its result into a per-goroutine `singlePhaseResult` slot. The `vesselRunState` accumulator is only updated in the merge loop after `wg.Wait()` — not directly from goroutine bodies. No data race is detected.
**Verification:** `go test -race` passes for the orchestrated runner test. No concurrent writes to shared accumulator fields are flagged by the race detector.

---

### S20: singlePhaseResult includes a phaseSummary field
**Spec ref:** Section 12.1
**Preconditions:** A single phase executes successfully within `runSinglePhase`.
**Action:** `runSinglePhase` returns its result.
**Expected outcome:** The returned `singlePhaseResult` has a `phaseSummary` field populated with at minimum the phase name, status, and duration.
**Verification:** In the test, inspect `result.phaseSummary.Name == p.Name` and `result.phaseSummary.Status == "completed"` and `result.phaseSummary.Duration > 0`.

---

### S21: singlePhaseResult evidenceClaim is nil when no gate is present
**Spec ref:** Section 12.1
**Preconditions:** A phase with no gate defined in the workflow YAML.
**Action:** `runSinglePhase` completes for the gateless phase.
**Expected outcome:** `result.evidenceClaim` is `nil` (no gate means no evidence claim is generated).
**Verification:** `result.evidenceClaim == nil` after `runSinglePhase` returns for the gateless phase.

---

### S22: Wave results merged into vesselRunState after wg.Wait()
**Spec ref:** Section 12.1
**Preconditions:** A workflow wave with two concurrent phases, both completing successfully.
**Action:** Both phase goroutines finish. The wave result merge loop runs.
**Expected outcome:** After `wg.Wait()` returns, both phases' `phaseSummary` values are merged into the `vesselRunState`. The accumulator contains exactly two phase entries, one per phase.
**Verification:** After the wave, `vesselRunState.phases` has length 2. Both entries have `Status == "completed"`. Order matches phase declaration order (or is deterministic).

---

### S23: cost.Tracker concurrent access from multiple goroutines is safe
**Spec ref:** Section 12.2
**Preconditions:** A workflow wave with two or more concurrent phases. Each goroutine calls the shared `cost.Tracker` to record token usage.
**Action:** Two goroutines call `tracker.Record(...)` concurrently.
**Expected outcome:** No data race. Both recordings are reflected in the tracker's total after both goroutines complete.
**Verification:** `go test -race` passes. After `wg.Wait()`, `tracker.Total()` equals the sum of both goroutines' recorded costs.

---

### S24: Vessel span context is propagated to goroutine child phase spans
**Spec ref:** Section 12.3
**Preconditions:** Runner is configured with a non-nil Tracer. A workflow wave has two concurrent phases.
**Action:** Both phase goroutines start. Each creates a child span using the vessel span's context.
**Expected outcome:** Each goroutine creates a phase span that is a child of the vessel span. Both phase spans are ended after their goroutines complete.
**Verification:** Tracer stub records two child spans, both with `parentSpanID == vesselSpan.ID`. Both are ended before `wg.Wait()` returns.

---

### S25: Concurrent phases may cause slight over-spend without retroactive failure
**Spec ref:** Section 12.2
**Preconditions:** Budget is set such that each phase's individual cost appears within budget when checked, but the combined cost of two concurrent phases exceeds the limit. Two phases run in the same wave.
**Action:** Both phase goroutines record cost and check `BudgetExceeded()` concurrently before either has seen the other's contribution.
**Expected outcome:** Both phases complete. The vessel is marked `completed` even though `tracker.Total()` exceeds the budget after the wave. No retroactive failure occurs and no panic is raised. This is the specified tolerable behaviour for concurrent cost estimates.
**Verification:** `Vessel.State == "completed"`. `tracker.Total() > budget` (over-spend occurred). No error is returned by either goroutine due to budget. The final summary reflects the actual total cost, not the budget cap.

---

## Section 13: Backwards compatibility

### S26: Existing .xylem.yml with no harness/observability/cost sections loads without error
**Spec ref:** Section 13.1
**Preconditions:** A `.xylem.yml` file that was valid before these sections were introduced — it has `sources`, `timeout`, `concurrency`, etc. but no `harness:`, `observability:`, or `cost:` top-level keys.
**Action:** `config.Load(".xylem.yml")` is called.
**Expected outcome:** Config loads successfully with no error. Missing sections default to Go zero values. Helper methods on the config return safe defaults (e.g., `cfg.Harness.PolicyEnabled()` returns false, budget is unbounded).
**Verification:** `err == nil`. `cfg.Harness` zero value does not cause a nil-pointer panic when any helper method is called. Existing `sources`/`timeout`/`concurrency` fields are correctly populated.

---

### S27: Existing workflow YAML with no evidence metadata loads without error
**Spec ref:** Section 13.2
**Preconditions:** A workflow YAML file with a `gate:` block that does not have an `evidence:` sub-field.
**Action:** `workflow.Load(...)` is called on the YAML file.
**Expected outcome:** The workflow loads without error. The `Gate.Evidence` field is `nil` (pointer with omitempty). When `buildGateClaim` processes this gate, it produces an `Untyped` claim.
**Verification:** `err == nil`. `wf.Phases[0].Gate.Evidence == nil`. No panic when `buildGateClaim` is called with a nil-evidence gate.

---

### S28: Queue JSONL format is unchanged — no new Vessel fields
**Spec ref:** Section 13.3
**Preconditions:** A `.xylem/queue.jsonl` file written by the current (pre-harness) version of xylem, containing valid vessel entries.
**Action:** `queue.Queue.Dequeue()` reads the file.
**Expected outcome:** All existing vessels deserialise correctly. No new fields appear in the JSONL output when a vessel is written back by the new code.
**Verification:** After a round-trip (dequeue + re-enqueue), the re-serialised JSONL line is byte-for-byte identical to the original (field names and values unchanged, no new fields added). `json.Unmarshal` into the `Vessel` struct succeeds with no errors.

---

### S29: Runner with nil Intermediary, AuditLog, and Tracer works identically to before
**Spec ref:** Section 13.4
**Preconditions:** Runner is constructed without setting `Intermediary`, `AuditLog`, or `Tracer` (all nil — the current default).
**Action:** Runner drains a vessel through a full workflow execution.
**Expected outcome:** All phases run and complete. No nil-pointer panic occurs. Behaviour is identical to the pre-harness runner: no policy checks, no audit entries, no spans emitted.
**Verification:** `Vessel.State == "completed"`. No panic. Log output contains no references to policy, audit, or tracing. Phase output files are written as before.

---

### S30: VesselCompleted with nil manifest produces identical output to current behavior
**Spec ref:** Section 13.5
**Preconditions:** `reporter.Reporter.VesselCompleted` is called with a nil `*evidence.Manifest` argument (as would happen for prompt-only vessels or vessels run without evidence collection enabled).
**Action:** `r.Reporter.VesselCompleted(ctx, issueNum, phaseResults, nil)` is called.
**Expected outcome:** The GitHub comment posted is identical in content to what would have been posted by the pre-harness version of `VesselCompleted`. No evidence table or manifest section is appended.
**Verification:** The comment body posted to GitHub matches a reference snapshot captured from the current code (before the manifest parameter was added). No "evidence" or "manifest" text appears in the output.
