# Smoke Scenarios — Unit 3: Per-vessel summary artifact

These scenarios cover the observable behaviour of the per-vessel summary artifact
described in spec section 7 of `xylem-harness-impl-spec.md`.

---

### S1: Summary file written on vessel completion

**Spec ref:** Section 7.1, 7.3

**Preconditions:** A vessel with ID `vessel-abc123`, workflow `fix-bug`, source
`github`, and two phases completes successfully. State dir is `/tmp/xylem-state`.

**Action:** `completeVessel` is called after all phases finish.

**Expected outcome:** A file exists at
`/tmp/xylem-state/phases/vessel-abc123/summary.json`. The file is non-empty and
parses as valid JSON with top-level field `"vessel_id": "vessel-abc123"` and
`"state": "completed"`.

**Verification:** `os.Stat` the path; `json.Unmarshal` into `VesselSummary` and
assert `VesselID == "vessel-abc123"` and `State == "completed"`.

---

### S2: Summary file written on vessel failure (partial summary)

**Spec ref:** Section 7.3 (failure paths)

**Preconditions:** A vessel with ID `vessel-def456` has completed its first
phase. The runner fails before the second phase runs (e.g. worktree creation
fails). State dir is `/tmp/xylem-state`.

**Action:** The failure path calls `buildSummary("failed")` and
`SaveVesselSummary` with only the first phase in the accumulator.

**Expected outcome:** A file exists at
`/tmp/xylem-state/phases/vessel-def456/summary.json`. It parses as valid JSON
with `"state": "failed"`. The `"phases"` array contains exactly one entry
reflecting the completed first phase.

**Verification:** Parse the file; assert `State == "failed"` and
`len(Phases) == 1`.

---

### S3: Summary contains the disclaimer note

**Spec ref:** Section 7.1 (`summaryDisclaimer` constant, `SaveVesselSummary`)

**Preconditions:** Any vessel completes or fails, triggering `SaveVesselSummary`.

**Action:** `SaveVesselSummary` writes the summary to disk.

**Expected outcome:** The written JSON has a `"note"` field equal to `"Token
counts and costs are estimates (len/4 heuristic + static pricing). Not
provider-reported values."`.

**Verification:** Read `summary.json`, parse, and assert
`Note == summaryDisclaimer`.

---

### S4: Summary JSON is pretty-printed

**Spec ref:** Section 7.1 (`json.MarshalIndent`)

**Preconditions:** A vessel with any ID completes. State dir is
`/tmp/xylem-state`.

**Action:** `SaveVesselSummary` is called.

**Expected outcome:** The raw bytes of `summary.json` contain newlines and
two-space indentation. The first line is `{` and the second line starts with
`  "`.

**Verification:** Read the raw file bytes; check that `bytes.Contains(data, []byte("\n  "))` is true.

---

### S5: PhaseSummary records "completed" status for a successful phase

**Spec ref:** Section 7.1 (`PhaseSummary.Status`)

**Preconditions:** A vessel runs a single phase named `"implement"` that
finishes without error.

**Action:** `addPhase` is called with a `PhaseSummary{Status: "completed"}`;
`buildSummary` is then called.

**Expected outcome:** The resulting `VesselSummary.Phases[0].Status` is
`"completed"` and `Phases[0].Error` is empty.

**Verification:** Assert `summary.Phases[0].Status == "completed"` and
`summary.Phases[0].Error == ""`.

---

### S6: PhaseSummary records "failed" status for a failed phase

**Spec ref:** Section 7.1 (`PhaseSummary.Status`, `PhaseSummary.Error`)

**Preconditions:** A vessel phase named `"test"` fails with error message
`"exit status 1"`.

**Action:** `addPhase` is called with
`PhaseSummary{Status: "failed", Error: "exit status 1"}`; `buildSummary` is then
called.

**Expected outcome:** `Phases[0].Status == "failed"` and
`Phases[0].Error == "exit status 1"`.

**Verification:** Assert both fields on the returned summary.

---

### S7: PhaseSummary records "no-op" status for an early-completion phase

**Spec ref:** Section 7.1 (`PhaseSummary.Status`)

**Preconditions:** A vessel has a phase that was previously completed and is
skipped on resume (already present in phase outputs).

**Action:** `addPhase` is called with `PhaseSummary{Status: "no-op"}`;
`buildSummary` is called.

**Expected outcome:** `Phases[0].Status == "no-op"`. Token and cost fields for
that phase are zero (the phase did no work).

**Verification:** Assert `Phases[0].Status == "no-op"`,
`Phases[0].InputTokensEst == 0`, and `Phases[0].CostUSDEst == 0.0`.

---

### S8: vesselRunState.addPhase accumulates phases in insertion order

**Spec ref:** Section 7.2 (`vesselRunState.addPhase`)

**Preconditions:** A `vesselRunState` is created with no phases.

**Action:** `addPhase` is called three times with phases named `"plan"`,
`"implement"`, `"test"` in that order.

**Expected outcome:** After the third call, `vrs.phases` has length 3 and the
names appear in insertion order: `phases[0].Name == "plan"`,
`phases[1].Name == "implement"`, `phases[2].Name == "test"`.

**Verification:** Inspect `vrs.phases` directly in a unit test; assert order and
length.

---

### S9: buildSummary computes TotalTokensEst as sum of phase token fields

**Spec ref:** Section 7.2 (`buildSummary`)

**Preconditions:** A `vesselRunState` has two phases:
- Phase A: `InputTokensEst=100`, `OutputTokensEst=50`
- Phase B: `InputTokensEst=200`, `OutputTokensEst=80`

**Action:** `buildSummary("completed")` is called.

**Expected outcome:** The returned summary has
`TotalInputTokensEst == 300`, `TotalOutputTokensEst == 130`,
and `TotalTokensEst == 430`.

**Verification:** Assert all three total fields on the returned `*VesselSummary`.

---

### S10: buildSummary computes TotalCostUSDEst as sum of phase costs

**Spec ref:** Section 7.2 (`buildSummary`)

**Preconditions:** A `vesselRunState` has two phases:
- Phase A: `CostUSDEst=0.0012`
- Phase B: `CostUSDEst=0.0034`

**Action:** `buildSummary("completed")` is called.

**Expected outcome:** `TotalCostUSDEst` is approximately `0.0046` (sum of both
phases, within floating-point tolerance).

**Verification:** Assert `math.Abs(summary.TotalCostUSDEst - 0.0046) < 1e-9`.

---

### S11: buildSummary sets DurationMS from startedAt to call time

**Spec ref:** Section 7.2 (`buildSummary`)

**Preconditions:** A `vesselRunState` is created with `startedAt` set to a fixed
time `T` in the past.

**Action:** `buildSummary("completed")` is called.

**Expected outcome:** The returned summary has `DurationMS > 0`,
`StartedAt == T`, and `EndedAt` is after `T`.

**Verification:** Assert `summary.DurationMS > 0` and
`summary.EndedAt.After(summary.StartedAt)`.

---

### S12: buildSummary reads BudgetExceeded from the costTracker

**Spec ref:** Section 7.2 (`buildSummary`, `cost.Tracker.BudgetExceeded`)

**Preconditions:** A `vesselRunState` holds a `costTracker` that has exceeded its
budget (i.e. `BudgetExceeded()` returns `true`).

**Action:** `buildSummary("completed")` is called.

**Expected outcome:** The returned `VesselSummary.BudgetExceeded` is `true`.

**Verification:** Assert `summary.BudgetExceeded == true`.

---

### S13: SaveVesselSummary creates the phases/<vessel-id> directory if absent

**Spec ref:** Section 7.1 (`SaveVesselSummary`, `os.MkdirAll`)

**Preconditions:** The directory `<state_dir>/phases/vessel-new999` does not
exist.

**Action:** `SaveVesselSummary(stateDir, &VesselSummary{VesselID: "vessel-new999", ...})` is called.

**Expected outcome:** The call returns `nil`. The directory
`<state_dir>/phases/vessel-new999` exists and contains `summary.json`.

**Verification:** `os.Stat` the directory and file; assert both exist with no
error.

---

### S14: SaveVesselSummary failure is non-fatal — caller continues

**Spec ref:** Section 7.3 (`SaveVesselSummary` non-fatal note)

**Preconditions:** The state directory path points to a location where directory
creation will fail (e.g. `/dev/null/phases` on Linux, or a path inside a
read-only mount) so that `os.MkdirAll` returns an error.

**Action:** `SaveVesselSummary` is called from a failure path in `runVessel`.

**Expected outcome:** `runVessel` does not panic or return an error to the queue.
The vessel state is updated to `failed` in the queue as normal. A warning is
written to the log (`log.Printf("warn: save vessel summary: ..."`)).

**Verification:** Assert `Queue.Update` was called with `StateFailed`; assert no
panic; capture log output and verify the `"warn: save vessel summary"` line is
present.

---

### S15: completeVessel updated signature accepts vesselRunState

**Spec ref:** Section 7.3 (updated `completeVessel` signature)

**Preconditions:** A `vesselRunState` has been populated with two completed
phases during a run of `runVessel`.

**Action:** `completeVessel(ctx, vessel, worktreePath, phaseResults, vrs, nil)`
is called (nil claims).

**Expected outcome:** The call compiles and returns `"completed"`. No nil-pointer
panic occurs when `claims` is nil.

**Verification:** Unit test calls the updated signature; asserts return value is
`"completed"` and that `summary.json` is written to the state dir.

---

### S16: completeVessel saves summary after existing completion logic

**Spec ref:** Section 7.3 (ordering — summary saved after queue update)

**Preconditions:** A vessel completes the sequential phase loop. Queue and
Reporter stubs are in place.

**Action:** `completeVessel` is called.

**Expected outcome:** After `completeVessel` returns, the vessel state in the
queue is `StateCompleted` and `summary.json` exists with `"state": "completed"`.

**Verification:** Read the queue and assert vessel state is `completed`; parse
`summary.json` and assert its `state` field is `"completed"`.

---

### S17: completeVessel saves evidence manifest when claims are present

**Spec ref:** Section 7.3 (evidence manifest block)

**Preconditions:** `completeVessel` is called with a non-empty `claims` slice
(e.g. one `evidence.Claim` for the `"implement"` phase).

**Action:** `completeVessel` executes the evidence manifest block.

**Expected outcome:** A file `<state_dir>/phases/<vessel-id>/evidence-manifest.json`
is created. It is valid JSON. `summary.EvidenceManifestPath` is
`"phases/<vessel-id>/evidence-manifest.json"` (the relative path).

**Verification:** `os.Stat` the manifest file; parse it; assert
`summary.EvidenceManifestPath` is set to the expected relative path.

---

### S18: EvidenceManifestPath is empty in summary when no claims provided

**Spec ref:** Section 7.3 (`EvidenceManifestPath` omitempty)

**Preconditions:** `completeVessel` is called with an empty or nil `claims`
slice.

**Action:** `completeVessel` skips the evidence manifest block.

**Expected outcome:** The `evidence-manifest.json` file is not created.
`summary.EvidenceManifestPath` is empty string, which means it is omitted from
the JSON output (`omitempty`).

**Verification:** Assert the manifest file does not exist; parse `summary.json`
and confirm the `"evidence_manifest_path"` key is absent.

---

### S19: Failure path builds summary with state "failed" and calls SaveVesselSummary

**Spec ref:** Section 7.3 (failure paths)

**Preconditions:** A vessel is running phase 2 of 3. Phase 2's gate fails with
retries exhausted.

**Action:** The gate-failure branch in `runVessel` executes:
`summary := vrs.buildSummary("failed"); _ = SaveVesselSummary(...)`.

**Expected outcome:** `summary.json` is written for the vessel. Parsing it shows
`"state": "failed"`. The `phases` array contains the entries recorded before
the failure (phase 1 completed, phase 2 added with `Status: "failed"`).

**Verification:** Trigger the gate-failure path in a test; read the written
`summary.json`; assert state and phase count.

---

### S20: BudgetMaxCostUSD and BudgetMaxTokens appear in summary when budget is configured

**Spec ref:** Section 7.1 (`BudgetMaxCostUSD`, `BudgetMaxTokens` omitempty)

**Preconditions:** The runner is configured with a budget of `max_cost_usd: 1.00`
and `max_tokens: 50000`. The `VesselSummary` is populated with these values
before calling `SaveVesselSummary`.

**Action:** `SaveVesselSummary` writes the summary.

**Expected outcome:** The written JSON contains both
`"budget_max_cost_usd": 1.0` and `"budget_max_tokens": 50000`.

**Verification:** Parse `summary.json`; assert `*BudgetMaxCostUSD == 1.0` and
`*BudgetMaxTokens == 50000`.

---

## Manual Smoke Tests

Use the checked-in Guide 4B seed below instead of the live repo-root `.xylem`
tree.

| Scenario IDs | DTU status | Manifest | Seeded workdir | Notes |
| --- | --- | --- | --- | --- |
| S1-S20 | **Seeded / passing artifact smoke** | `cli/internal/dtu/testdata/ws3-summary-artifacts.yaml` | `cli/internal/dtu/testdata/manual-smoke/ws3-summary-artifacts/` | The seeded two-phase workflow should now write `summary.json`, `evidence-manifest.json`, and the per-phase output files under `.xylem-state/phases/<vessel>/`. Use this seed to verify the full WS3 artifact surface end-to-end. |

### Run the seed

```bash
cd /Users/harry.nicholls/repos/xylem/cli
go build ./cmd/xylem
XYLEM_BIN="$PWD/xylem"
MANIFEST="$PWD/internal/dtu/testdata/ws3-summary-artifacts.yaml"
WORKDIR="$PWD/internal/dtu/testdata/manual-smoke/ws3-summary-artifacts"
STATE_DIR="$WORKDIR/.xylem-state"

eval "$("$XYLEM_BIN" dtu env \
  --manifest "$MANIFEST" \
  --state-dir "$STATE_DIR" \
  --workdir "$WORKDIR")"

(
  cd "$WORKDIR" || exit 1
  "$XYLEM_BIN" --config .xylem.yml scan
  "$XYLEM_BIN" --config .xylem.yml drain
)
```

### Verify

```bash
cd "$WORKDIR" || exit 1
find .xylem-state/phases -maxdepth 2 -type f | sort
cat .xylem-state/phases/*/summary.json
cat .xylem-state/phases/*/evidence-manifest.json
```

**Expected pass right now**
- `analyze.output` and `implement.output` prove the seeded repo, manifest, and
  workflow wiring are correct.
- `summary.json` exists under `.xylem-state/phases/<vessel>/` and includes the
  vessel state, workflow metadata, phase summaries, and token/cost estimates.
- `evidence-manifest.json` exists under the same vessel directory and is linked
  from `summary.json` via `evidence_manifest_path`.
