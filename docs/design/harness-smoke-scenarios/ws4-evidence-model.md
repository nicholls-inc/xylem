# Smoke scenarios — Workstream 4: Verification evidence model

Spec reference: `docs/design/xylem-harness-impl-spec.md` section 8

---

### S1: Level.Valid() accepts all named levels including Untyped
**Spec ref:** Section 8.1
**Preconditions:** `cli/internal/evidence` package exists with `Level` type and `Valid()` method.
**Action:** Call `Valid()` on each of the five recognised constants: `Proved`, `MechanicallyChecked`, `BehaviorallyChecked`, `ObservedInSitu`, and `Untyped` (the empty string).
**Expected outcome:** All five calls return `true`.
**Verification:** Unit test asserts `true` for each level, including the zero-value empty string.

---

### S2: Level.Valid() rejects arbitrary strings
**Spec ref:** Section 8.1
**Preconditions:** `Level` type and `Valid()` method exist.
**Action:** Call `Valid()` on unrecognised strings such as `"high"`, `"none"`, `"PROVED"` (wrong case), and `"mechanically-checked"` (wrong separator).
**Expected outcome:** All calls return `false`.
**Verification:** Unit test asserts `false` for each input; case-sensitivity and separator format are confirmed to matter.

---

### S3: Level.Rank() ordering — Proved is strongest, Untyped is weakest
**Spec ref:** Section 8.1
**Preconditions:** `Rank()` method exists on `Level`.
**Action:** Call `Rank()` on all five levels and compare results.
**Expected outcome:** `Proved.Rank()` (4) > `MechanicallyChecked.Rank()` (3) > `BehaviorallyChecked.Rank()` (2) > `ObservedInSitu.Rank()` (1) > `Untyped.Rank()` (0).
**Verification:** Unit test asserts strict ordering and the exact integer values for all five levels.

---

### S4: Manifest.BuildSummary counts total, passed, and failed claims
**Spec ref:** Section 8.1
**Preconditions:** `Manifest` and `BuildSummary()` exist.
**Action:** Create a `Manifest` with three claims: two with `Passed: true` and one with `Passed: false`. Call `BuildSummary()`.
**Expected outcome:** `manifest.Summary.Total == 3`, `manifest.Summary.Passed == 2`, `manifest.Summary.Failed == 1`.
**Verification:** Unit test reads the three fields from `manifest.Summary` after the call.

---

### S5: Manifest.BuildSummary groups claims by level
**Spec ref:** Section 8.1
**Preconditions:** `BuildSummary()` populates `Summary.ByLevel`.
**Action:** Create a `Manifest` with four claims: two at `BehaviorallyChecked`, one at `MechanicallyChecked`, one at `Untyped`. Call `BuildSummary()`.
**Expected outcome:** `Summary.ByLevel["behaviorally_checked"] == 2`, `Summary.ByLevel["mechanically_checked"] == 1`, `Summary.ByLevel[""] == 1`. No entry exists for levels with zero claims.
**Verification:** Unit test checks the map entries; confirms absent keys are not present rather than set to zero.

---

### S6: Manifest.StrongestLevel returns highest-ranked passing claim level
**Spec ref:** Section 8.1
**Preconditions:** `StrongestLevel()` method exists.
**Action:** Create a `Manifest` with three passing claims at levels `ObservedInSitu`, `BehaviorallyChecked`, and `MechanicallyChecked`. Call `StrongestLevel()`.
**Expected outcome:** Returns `MechanicallyChecked`.
**Verification:** Unit test asserts the returned level equals `MechanicallyChecked`.

---

### S7: Manifest.StrongestLevel returns Untyped when no claims passed
**Spec ref:** Section 8.1
**Preconditions:** `StrongestLevel()` method exists.
**Action:** Call `StrongestLevel()` on a manifest whose claims all have `Passed: false` (including a claim at `Proved`). Also call it on a manifest with no claims at all.
**Expected outcome:** Returns `Untyped` (empty string) in both cases.
**Verification:** Unit test asserts the returned value equals `evidence.Untyped` for each case.

---

### S8: SaveManifest writes JSON to the expected path
**Spec ref:** Section 8.1
**Preconditions:** `SaveManifest` function exists; a temporary directory is available as `stateDir`.
**Action:** Call `SaveManifest(stateDir, "vessel-abc123", manifest)` where `manifest` has one claim.
**Expected outcome:** A file is created at `<stateDir>/phases/vessel-abc123/evidence-manifest.json`. The file contains valid JSON with `vessel_id`, `claims`, and `summary` keys.
**Verification:** Unit test checks the file exists at the exact path and that `json.Unmarshal` succeeds with the correct `VesselID`.

---

### S9: SaveManifest calls BuildSummary before writing
**Spec ref:** Section 8.1
**Preconditions:** `SaveManifest` and `BuildSummary` exist.
**Action:** Create a `Manifest` with two claims (one passed, one failed) but do not call `BuildSummary()` first. Call `SaveManifest(stateDir, "vessel-xyz", manifest)`.
**Expected outcome:** The written JSON file contains a `summary` block with `total: 2`, `passed: 1`, `failed: 1` — populated automatically by `SaveManifest`.
**Verification:** Unit test reads back the file, unmarshals it, and asserts the summary fields are populated without a prior manual call to `BuildSummary`.

---

### S10: LoadManifest round-trips correctly
**Spec ref:** Section 8.1
**Preconditions:** `SaveManifest` and `LoadManifest` both exist.
**Action:** Build a `Manifest` with vessel ID `"vessel-roundtrip"`, workflow `"fix-bug"`, and two claims. Save it with `SaveManifest`. Then load it with `LoadManifest(stateDir, "vessel-roundtrip")`.
**Expected outcome:** The loaded manifest has identical `VesselID`, `Workflow`, and `Claims` slice (same claim text, level, checker, passed status). The `Summary` is also populated.
**Verification:** Unit test performs a field-by-field comparison of the saved and loaded values.

---

### S11: Gate without evidence metadata loads cleanly with nil Evidence field
**Spec ref:** Section 8.2
**Preconditions:** `GateEvidence` struct is added to `workflow.go`; `Gate` has an `Evidence *GateEvidence` field tagged `yaml:"evidence,omitempty"`.
**Action:** Load a valid workflow YAML file whose gate specifies only `type: command` and `run: "go test ./..."` with no `evidence:` section.
**Expected outcome:** `workflow.Phases[0].Gate.Evidence` is `nil`. The gate validates and loads without error.
**Verification:** Unit test checks `gate.Evidence == nil` and that `workflow.Load` returns no error.

---

### S12: Gate with valid evidence metadata parses correctly
**Spec ref:** Section 8.2
**Preconditions:** `GateEvidence` struct and `Gate.Evidence` field exist.
**Action:** Load a workflow YAML whose gate contains:
```yaml
evidence:
  claim: "All tests pass"
  level: behaviorally_checked
  checker: "go test"
  trust_boundary: "Package-level only"
```
**Expected outcome:** `gate.Evidence.Claim == "All tests pass"`, `gate.Evidence.Level == "behaviorally_checked"`, `gate.Evidence.Checker == "go test"`, `gate.Evidence.TrustBoundary == "Package-level only"`.
**Verification:** Unit test asserts each field value on the parsed struct.

---

### S13: Gate with invalid evidence level is rejected by validateGate
**Spec ref:** Section 8.2
**Preconditions:** `validateGate` checks evidence level validity.
**Action:** Attempt to load a workflow YAML whose gate has `evidence.level: "high"` (not a recognised level).
**Expected outcome:** `workflow.Load` returns a non-nil error whose message includes the invalid level string `"high"` and guidance about valid values.
**Verification:** Unit test asserts the error is non-nil and contains `"high"` in the message.

---

### S14: Gate with partial evidence (claim and level only) parses without error
**Spec ref:** Section 8.2
**Preconditions:** All `GateEvidence` fields are optional except the level is validated when present.
**Action:** Load a workflow YAML whose gate has:
```yaml
evidence:
  claim: "Tests pass"
  level: mechanically_checked
```
with no `checker` or `trust_boundary` fields.
**Expected outcome:** The workflow loads without error. `gate.Evidence.Checker` is an empty string and `gate.Evidence.TrustBoundary` is an empty string.
**Verification:** Unit test asserts no error and the two unpopulated fields are empty strings.

---

### S15: buildGateClaim with evidence metadata produces a typed claim
**Spec ref:** Section 8.3
**Preconditions:** `buildGateClaim` helper exists in `runner.go`.
**Action:** Call `buildGateClaim` with a `Phase` whose gate has `Evidence` set to `{Claim: "All tests pass", Level: "behaviorally_checked", Checker: "go test", TrustBoundary: "Package-level only"}`, and `passed = true`.
**Expected outcome:** The returned `Claim` has `Claim == "All tests pass"`, `Level == evidence.BehaviorallyChecked`, `Checker == "go test"`, `TrustBoundary == "Package-level only"`, `Passed == true`.
**Verification:** Unit test asserts all five fields on the returned struct.

---

### S16: buildGateClaim without evidence metadata produces an Untyped claim
**Spec ref:** Section 8.3
**Preconditions:** `buildGateClaim` exists.
**Action:** Call `buildGateClaim` with a `Phase` whose gate has `Evidence == nil` and `passed = true`.
**Expected outcome:** The returned `Claim` has `Level == evidence.Untyped` (empty string), `TrustBoundary == "No trust boundary declared"`, and `Claim` is a generated string containing the phase name (e.g. `"Gate passed for phase \"implement\""`).
**Verification:** Unit test asserts `Level` is `""`, `TrustBoundary` matches exactly, and `Claim` contains the phase name.

---

### S17: buildGateClaim sets Checker from gate Run command when no evidence
**Spec ref:** Section 8.3
**Preconditions:** `buildGateClaim` exists.
**Action:** Call `buildGateClaim` with a `Phase` whose gate has `Run: "cd cli && go test ./..."` and `Evidence == nil`.
**Expected outcome:** The returned `Claim.Checker` equals `"cd cli && go test ./..."`.
**Verification:** Unit test asserts `claim.Checker == "cd cli && go test ./..."`.

---

### S18: Evidence claims are accumulated across multiple phases
**Spec ref:** Section 8.3
**Preconditions:** Runner accumulates claims in a `[]evidence.Claim` slice during vessel execution.
**Action:** Execute a vessel through two phases, each with a passing gate that has distinct evidence metadata.
**Expected outcome:** After both phases complete, the manifest saved to disk contains exactly two claims — one for each phase — with the correct `Phase` field identifying each.
**Verification:** Load the manifest from `<stateDir>/phases/<vesselID>/evidence-manifest.json` and assert `len(manifest.Claims) == 2` with correct phase names.

---

### S19: Gate failure produces no claim but preserves claims from prior phases
**Spec ref:** Section 8.3
**Preconditions:** Runner accumulates claims only after a gate passes; a gate failure stops vessel execution.
**Action:** Execute a vessel through three phases where phases 1 and 2 pass their gates but phase 3 fails its gate.
**Expected outcome:** The manifest saved at vessel completion contains exactly two claims (for phases 1 and 2). No claim exists for phase 3 because claims are only appended on gate pass. The vessel status is `failed`.
**Verification:** Load the manifest and assert `len(manifest.Claims) == 2`, with no claim whose `Phase` field equals the third phase's name.

---

### S20: VesselCompleted with nil manifest produces output identical to current behaviour
**Spec ref:** Section 8.4
**Preconditions:** `VesselCompleted` signature updated to accept `*evidence.Manifest` as a fourth parameter.
**Action:** Call `VesselCompleted(ctx, 42, phases, nil)` with a normal phases slice.
**Expected outcome:** The comment body is identical to what the current implementation produces — phase table with Name/Duration/Status columns and a Total line. No "Verification evidence" section appears.
**Verification:** Unit test for the nil-manifest case asserts the output string does not contain `"Verification evidence"` and matches the format produced by the pre-evidence implementation.

---

### S21: VesselCompleted with evidence renders a table with the correct columns
**Spec ref:** Section 8.4
**Preconditions:** `VesselCompleted` accepts a non-nil `*evidence.Manifest`; `formatEvidenceSection` is implemented.
**Action:** Call `VesselCompleted(ctx, 42, phases, manifest)` where `manifest` contains one claim: `{Claim: "All tests pass", Level: "behaviorally_checked", Checker: "go test", Passed: true}`.
**Expected outcome:** The comment body contains a `### Verification evidence` heading, followed by a markdown table with columns `Claim`, `Level`, `Checker`, and `Result`. The single row contains `All tests pass`, `behaviorally_checked`, `go test`, and a checkmark symbol.
**Verification:** Unit test asserts all four column headers and the row values are present in the rendered string.

---

### S22: VesselCompleted renders checkmark for passed claims and X for failed
**Spec ref:** Section 8.4
**Preconditions:** `formatEvidenceSection` renders per-claim result symbols.
**Action:** Call `VesselCompleted` with a manifest containing two claims — one with `Passed: true` and one with `Passed: false`.
**Expected outcome:** The evidence table row for the passing claim contains `:white_check_mark:`. The row for the failing claim contains `:x:` (or the equivalent rendered symbol). Each is on a separate row.
**Verification:** Unit test asserts both symbols appear in the rendered string and are on separate lines corresponding to their respective claims.

---

### S23: VesselCompleted renders trust boundaries in a collapsible details block
**Spec ref:** Section 8.4
**Preconditions:** `formatEvidenceSection` emits a `<details>` block for trust boundaries.
**Action:** Call `VesselCompleted` with a manifest containing one claim: `{Claim: "All tests pass", TrustBoundary: "Package-level only", Passed: true}`.
**Expected outcome:** The comment body contains `<details>`, `<summary>Trust boundaries</summary>`, and a bullet item `**All tests pass** — Package-level only` inside the block.
**Verification:** Unit test asserts the `<details>` tag, the summary text, and the formatted trust boundary bullet are all present in the output.

---

### S24: Existing VesselCompleted tests pass nil as the manifest parameter
**Spec ref:** Section 8.4
**Preconditions:** Existing tests in `reporter_test.go` (e.g. `TestVesselCompleted`, `TestVesselCompletedNoOp`) call `VesselCompleted` with the old three-argument signature.
**Action:** Update all call sites in `reporter_test.go` to pass `nil` as the new fourth `*evidence.Manifest` argument. Run `go test ./internal/reporter/...`.
**Expected outcome:** All existing reporter tests pass without modification to their assertions. No test output changes — the nil manifest case is backward-compatible.
**Verification:** `go test ./internal/reporter/...` exits with status 0 and no test failures.

---

## Manual Smoke Tests

Use the checked-in Guide 4B seed below instead of the live repo-root `.xylem`
tree.

| Scenario IDs | DTU status | Manifest | Seeded workdir | Notes |
| --- | --- | --- | --- | --- |
| S1-S10 | **Expected pass** | `cli/internal/dtu/testdata/ws4-evidence-model.yaml` | `cli/internal/dtu/testdata/manual-smoke/ws4-evidence-model/` | `package_probes` runs targeted `go test ./internal/evidence`, then the seeded prompt phase and command gate complete in the dedicated smoke repo. |
| S11-S24 | **Known-fail / manual-triage** | same | same | The seeded workflow carries `gate.evidence` metadata, but the current workflow/runner/reporter path still does not persist `evidence-manifest.json` or append verification-evidence tables. |

### Run the seed

```bash
cd /Users/harry.nicholls/repos/xylem/cli
go build ./cmd/xylem
XYLEM_BIN="$PWD/xylem"
MANIFEST="$PWD/internal/dtu/testdata/ws4-evidence-model.yaml"
WORKDIR="$PWD/internal/dtu/testdata/manual-smoke/ws4-evidence-model"
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
cat .xylem-state/phases/*/package_probes.output
find .xylem-state/phases -maxdepth 2 -type f | sort
test -f .xylem-state/phases/*/evidence-manifest.json && cat .xylem-state/phases/*/evidence-manifest.json || echo "evidence-manifest.json missing"
```

**Expected pass right now**
- `package_probes.output` contains a passing `go test ./internal/evidence` run.
- `implement.output` exists and the seeded gate command succeeds inside the
  dedicated smoke repo.

**Known-fail / manual-triage right now**
- No `evidence-manifest.json` is written under `.xylem-state/phases/<vessel>/`.
- Reporter output remains the legacy phase table only; there is no verification
  evidence section yet.
