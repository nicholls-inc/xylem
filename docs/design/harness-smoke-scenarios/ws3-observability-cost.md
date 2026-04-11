# Smoke Scenarios: Unit 2 — Observability and Cost

Workstream 3 of the xylem harness implementation spec.
Covers spec sections 5 (observability integration) and 6 (cost estimation and budget enforcement).

---

## Section 5: Observability integration

### S1: VesselSpanAttributes includes id, source, and workflow

**Spec ref:** Section 5.3

**Preconditions:** `observability/vessel.go` exists with `VesselSpanAttributes` implemented.

**Action:** Call `VesselSpanAttributes("vessel-123", "github", "fix-bug", "refs/pull/42/head")`.

**Expected outcome:** Returns a slice of four `SpanAttribute` values: `xylem.vessel.id = "vessel-123"`, `xylem.vessel.source = "github"`, `xylem.vessel.workflow = "fix-bug"`, `xylem.vessel.ref = "refs/pull/42/head"`.

**Verification:** Unit test asserting length == 4 and key/value pairs match the spec exactly.

---

### S2: VesselSpanAttributes omits ref when empty

**Spec ref:** Section 5.3

**Preconditions:** `VesselSpanAttributes` implemented.

**Action:** Call `VesselSpanAttributes("vessel-456", "manual", "implement-feature", "")`.

**Expected outcome:** Returns a slice of exactly three `SpanAttribute` values. No attribute with key `xylem.vessel.ref` is present.

**Verification:** Unit test asserting length == 3 and no element has Key == `"xylem.vessel.ref"`.

---

### S3: PhaseSpanAttributes includes resolved phase and LLM routing fields

**Spec ref:** Section 5.3

**Preconditions:** `PhaseSpanAttributes` implemented.

**Action:** Call `PhaseSpanAttributes(PhaseSpanData{Name: "analyse", Index: 0, Type: "prompt", Workflow: "fix-bug", Provider: "anthropic", Model: "claude-sonnet-4-20250514", Tier: "med", RetryAttempt: 2, SandboxMode: "dangerously-skip-permissions"})`.

**Expected outcome:** Returns a slice of ten `SpanAttribute` values: `xylem.phase.name = "analyse"`, `xylem.phase.index = "0"`, `xylem.phase.type = "prompt"`, `xylem.phase.workflow = "fix-bug"`, `xylem.phase.provider = "anthropic"`, `xylem.phase.model = "claude-sonnet-4-20250514"`, `xylem.phase.retry_attempt = "2"`, `xylem.phase.sandbox_mode = "dangerously-skip-permissions"`, `llm.provider = "anthropic"`, and `llm.tier = "med"`. The index field is stringified, not numeric.

**Verification:** Unit test asserting length == 10 and all ten key/value pairs are present with correct string values.

---

### S4: PhaseResultAttributes formats tokens and cost as strings

**Spec ref:** Section 5.3

**Preconditions:** `PhaseResultAttributes` implemented.

**Action:** Call `PhaseResultAttributes(1200, 300, 0.0081, 4500)`.

**Expected outcome:** Returns four `SpanAttribute` values: `xylem.phase.input_tokens_est = "1200"`, `xylem.phase.output_tokens_est = "300"`, `xylem.phase.cost_usd_est = "0.008100"` (six decimal places via `%.6f`), `xylem.phase.duration_ms = "4500"`.

**Verification:** Unit test asserting exact string formatting, including the six-decimal-place cost.

---

### S5: GateSpanAttributes formats boolean and int as strings

**Spec ref:** Section 5.3

**Preconditions:** `GateSpanAttributes` implemented.

**Action:** Call `GateSpanAttributes("command", true, 2)`.

**Expected outcome:** Returns three `SpanAttribute` values: `xylem.gate.type = "command"`, `xylem.gate.passed = "true"`, `xylem.gate.retry_attempt = "2"`.

**Verification:** Unit test asserting the boolean is rendered as the lowercase string `"true"` (not `"1"` or `"True"`) and the retry count is a decimal string.

---

### S6: Tracer initialization with default config uses stdout exporter

**Spec ref:** Section 5.4

**Preconditions:** `NewTracer` implemented; `TracerConfig` with empty `Endpoint`.

**Action:** Call `NewTracer(DefaultTracerConfig())`.

**Expected outcome:** Returns a non-nil `*Tracer` and nil error. No network connection is attempted. Subsequent `StartSpan` calls emit JSON-formatted span output to stdout.

**Verification:** Unit test calling `NewTracer` with empty endpoint, asserting no error returned and tracer is not nil.

---

### S7: Tracer initialization with OTLP endpoint uses gRPC exporter

**Spec ref:** Section 5.4

**Preconditions:** `NewTracer` implemented.

**Action:** Call `NewTracer(TracerConfig{ServiceName: "xylem", Endpoint: "localhost:4317", Insecure: true, SampleRate: 1.0})`.

**Expected outcome:** Returns a non-nil `*Tracer` and nil error (the OTLP exporter is created in lazy mode and does not attempt a connection at construction time). The returned tracer is wired to the `localhost:4317` endpoint.

**Verification:** Unit test asserting non-nil tracer and nil error; confirm the implementation does not block on a missing collector at init time.

---

### S8: Tracer initialization failure logs warning and continues without tracing

**Spec ref:** Section 5.4

**Preconditions:** Tracer initialization code in `drain.go` / `daemon.go` that wraps `NewTracer` in a log-and-continue pattern.

**Action:** Simulate `NewTracer` returning an error (e.g., by providing a malformed endpoint string that the OTLP library rejects at construction time).

**Expected outcome:** The drain command logs a warning message containing `"warn: failed to initialize tracer:"` and proceeds to create the runner with `r.Tracer = nil`. The drain run does not abort.

**Verification:** Integration test or log-capture test asserting the warning is logged and no fatal/panic occurs.

---

### S9: nil Tracer skips all span creation without panicking

**Spec ref:** Sections 5.5 and 5.6

**Preconditions:** `Runner` with `Tracer` field set to nil. A pending vessel exists in the queue.

**Action:** Call `runner.Drain(ctx)` with at least one pending vessel and `r.Tracer == nil`.

**Expected outcome:** The vessel executes normally — phases run, output is persisted, state transitions occur. No nil pointer dereference panic occurs anywhere in the span-creation code paths.

**Verification:** Existing drain tests with a nil tracer (the default) pass without modification. No additional guard is needed — the `if r.Tracer != nil` checks in the spec fully cover this.

---

### S10: drain_run span wraps the entire Drain() call

**Spec ref:** Section 5.5

**Preconditions:** `Runner` with a non-nil `Tracer`. Queue contains two pending vessels.

**Action:** Call `runner.Drain(ctx)` to completion.

**Expected outcome:** Exactly one span named `"drain_run"` appears in the trace output. It carries attributes `xylem.drain.concurrency` and `xylem.drain.timeout`. Its start time precedes all vessel spans and its end time follows all vessel spans.

**Verification:** Capture stdout trace output from a test with a stdout exporter. Assert one root span named `"drain_run"` with the two expected attributes, and that its time range encloses all child spans.

---

### S11: vessel span is a child of drain_run span

**Spec ref:** Section 5.5

**Preconditions:** `Runner` with a non-nil `Tracer`. Queue contains one pending vessel.

**Action:** Call `runner.Drain(ctx)` to completion.

**Expected outcome:** The trace output contains a span named `"vessel:<vessel-id>"` whose parent span ID matches the `drain_run` span. The vessel span carries the four vessel attributes (`xylem.vessel.id`, `xylem.vessel.source`, `xylem.vessel.workflow`, and optionally `xylem.vessel.ref`).

**Verification:** Parse the stdout JSON trace output and assert the vessel span's `parentSpanID` == `drain_run` span's `spanID`.

---

### S12: phase span is a child of vessel span

**Spec ref:** Section 5.6

**Preconditions:** `Runner` with non-nil `Tracer`. Vessel has a single-phase workflow.

**Action:** Run one vessel to completion through `Drain`.

**Expected outcome:** The trace output contains a span named `"phase:<phase-name>"` whose parent span ID matches the vessel span's span ID.

**Verification:** Parse stdout trace JSON and assert parent-child relationship between vessel span and phase span.

---

### S13: gate span is a child of phase span

**Spec ref:** Section 5.6

**Preconditions:** `Runner` with non-nil `Tracer`. Vessel workflow has a phase with a `command`-type gate.

**Action:** Run one vessel through the gate evaluation path.

**Expected outcome:** The trace output contains a span named `"gate:command"` whose parent span ID matches the enclosing phase span. The gate span carries attributes `xylem.gate.type`, `xylem.gate.passed`, and `xylem.gate.retry_attempt`.

**Verification:** Parse stdout trace JSON and assert parent-child relationship and gate attribute presence.

---

### S14: Phase span gets result attributes added after execution

**Spec ref:** Section 5.6

**Preconditions:** `Runner` with non-nil `Tracer`. Prompt-type phase that produces non-empty output.

**Action:** Run one vessel with a prompt-type phase.

**Expected outcome:** The phase span in the trace output contains all four result attributes: `xylem.phase.input_tokens_est`, `xylem.phase.output_tokens_est`, `xylem.phase.cost_usd_est`, and `xylem.phase.duration_ms`. The cost attribute is formatted to six decimal places.

**Verification:** Parse stdout trace JSON for the phase span and assert all four result attributes are present and non-empty.

---

### S15: Phase span records error on phase failure

**Spec ref:** Section 5.6

**Preconditions:** `Runner` with non-nil `Tracer`. Phase is configured to fail (e.g., command phase returns non-zero exit code).

**Action:** Run a vessel whose phase fails.

**Expected outcome:** The phase span in the trace output contains an OTel error event (the span's `RecordError` was called with the failure error). The span's status is set to error.

**Verification:** Parse stdout trace JSON for the phase span and assert an event of type `"exception"` or equivalent OTel error annotation is present.

---

### S16: Phase span always ends even when phase fails

**Spec ref:** Section 5.6

**Preconditions:** `Runner` with non-nil `Tracer`. Phase is configured to fail.

**Action:** Run a vessel whose phase fails and observe the trace output after `Drain` returns.

**Expected outcome:** The phase span appears in the trace output with a valid end time (it is not missing, which would indicate a span that was started but never ended). The `drain_run` and vessel spans also end cleanly.

**Verification:** After `Shutdown` is called on the tracer, count the spans in stdout output. Every started span (drain_run, vessel, phase) must appear as a completed span.

---

## Section 6: Cost estimation and budget enforcement

### S17: EstimateTokens returns len/4 for a known string

**Spec ref:** Section 6.1

**Preconditions:** `cost/estimate.go` exists with `EstimateTokens` implemented.

**Action:** Call `EstimateTokens("Hello, world! This is a test string.")` where the string length is 36 characters.

**Expected outcome:** Returns `9` (36 / 4 = 9).

**Verification:** Unit test asserting `EstimateTokens("Hello, world! This is a test string.") == 9`.

---

### S18: EstimateTokens returns 0 for empty string

**Spec ref:** Section 6.1

**Preconditions:** `EstimateTokens` implemented.

**Action:** Call `EstimateTokens("")`.

**Expected outcome:** Returns `0`. No panic or error.

**Verification:** Unit test asserting `EstimateTokens("") == 0`.

---

### S19: EstimateCost with known pricing produces correct arithmetic

**Spec ref:** Section 6.1

**Preconditions:** `EstimateCost` and `ModelPricing` implemented.

**Action:** Call `EstimateCost(1_000_000, 500_000, &ModelPricing{InputPer1M: 3.00, OutputPer1M: 15.00})`.

**Expected outcome:** Returns `10.5` (1M input tokens * $3.00/1M + 500K output tokens * $15.00/1M = $3.00 + $7.50 = $10.50).

**Verification:** Unit test asserting the return value equals `10.5` within floating-point tolerance.

---

### S20: EstimateCost with nil pricing returns 0

**Spec ref:** Section 6.1

**Preconditions:** `EstimateCost` implemented.

**Action:** Call `EstimateCost(5000, 1000, nil)`.

**Expected outcome:** Returns `0`. No panic.

**Verification:** Unit test asserting `EstimateCost(5000, 1000, nil) == 0.0`.

---

### S21: LookupPricing exact match returns correct pricing

**Spec ref:** Section 6.1

**Preconditions:** `LookupPricing` implemented with `DefaultPricingTable` containing `"claude-sonnet-4"`.

**Action:** Call `LookupPricing("claude-sonnet-4")`.

**Expected outcome:** Returns a non-nil `*ModelPricing` with `InputPer1M == 3.00` and `OutputPer1M == 15.00`.

**Verification:** Unit test asserting non-nil return and correct pricing values.

---

### S22: LookupPricing falls back to prefix match for versioned model name

**Spec ref:** Section 6.1

**Preconditions:** `LookupPricing` implemented. `DefaultPricingTable` contains `"claude-sonnet-4"` but not `"claude-sonnet-4-20250514"`.

**Action:** Call `LookupPricing("claude-sonnet-4-20250514")`.

**Expected outcome:** Returns a non-nil `*ModelPricing` with `InputPer1M == 3.00` and `OutputPer1M == 15.00` (matched via prefix `"claude-sonnet-4"`).

**Verification:** Unit test asserting non-nil return and pricing values match the `"claude-sonnet-4"` entry.

---

### S23: LookupPricing longest prefix wins when multiple prefixes match

**Spec ref:** Section 6.1

**Preconditions:** `LookupPricing` implemented. `DefaultPricingTable` contains both `"claude-haiku-3"` and `"claude-haiku-4"`. The model being looked up starts with both.

**Action:** Call `LookupPricing("claude-haiku-4-5")` where `DefaultPricingTable` has `"claude-haiku-3"` (`InputPer1M: 0.25`) and `"claude-haiku-4"` (`InputPer1M: 0.80`).

**Expected outcome:** Returns pricing for `"claude-haiku-4"` (`InputPer1M == 0.80`), not for `"claude-haiku-3"` (`InputPer1M == 0.25`), because `"claude-haiku-4"` is the longer matching prefix.

**Verification:** Unit test asserting `InputPer1M == 0.80` in the returned pricing.

---

### S24: LookupPricing returns nil for unrecognised model

**Spec ref:** Section 6.1

**Preconditions:** `LookupPricing` implemented.

**Action:** Call `LookupPricing("gpt-4o")`.

**Expected outcome:** Returns `nil`.

**Verification:** Unit test asserting `LookupPricing("gpt-4o") == nil`.

---

### S25: Per-vessel Tracker is created fresh for each vessel

**Spec ref:** Section 6.2

**Preconditions:** `Runner` configured. Two pending vessels in the queue. `r.Config.VesselBudget()` returns a non-nil budget.

**Action:** Call `runner.Drain(ctx)` to process both vessels sequentially.

**Expected outcome:** Each vessel accumulates cost independently. Spending on vessel A does not carry over to vessel B's tracker. If vessel A's total cost is $0.05, vessel B starts at $0.00.

**Verification:** Test with two sequential vessels and a tight cost budget that vessel A would exceed only after two phases — assert that vessel B starts fresh (its first phase does not immediately exceed the budget).

---

### S26: Cost recorded after each prompt-type phase

**Spec ref:** Section 6.2

**Preconditions:** `Runner` with a vessel running a two-phase prompt workflow. Each phase produces non-empty rendered input and output.

**Action:** Run the vessel to completion.

**Expected outcome:** The vessel's tracker has exactly two `UsageRecord` entries (one per prompt-type phase). Each record has non-zero `InputTokens`, non-zero `OutputTokens`, and a non-zero `CostUSD` (when using a model that has a pricing entry in `DefaultPricingTable`).

**Verification:** Expose the tracker state for test assertions or check the TotalTokens() is > 0 after both phases complete.

---

### S27: Command-type phases do not generate cost records

**Spec ref:** Section 6.2

**Preconditions:** `Runner` with a vessel running a workflow containing one prompt phase and one command phase.

**Action:** Run the vessel to completion.

**Expected outcome:** The vessel's tracker has exactly one `UsageRecord` entry (from the prompt phase only). `TotalTokens()` reflects only the prompt phase's tokens.

**Verification:** Assert `vesselTracker.TotalTokens()` equals the estimated tokens from the prompt phase's input/output, not double that amount.

---

### S28: Budget enforcement fails vessel when budget is exceeded

**Spec ref:** Section 6.3

**Preconditions:** `Runner` configured with a vessel budget of `CostLimitUSD: 0.0001` (very tight). Vessel has a three-phase prompt workflow where the first phase's estimated cost alone exceeds the limit.

**Action:** Run the vessel through `Drain`.

**Expected outcome:** The vessel enters the `failed` state after the first phase completes and the budget check fires. The failure reason stored on the vessel contains the string `"budget exceeded"`, the estimated cost value, and the estimated token count.

**Verification:** After drain completes, query the vessel's final state from the queue and assert `Status == "failed"` and the error message contains `"budget exceeded"`.

---

### S29: nil budget means no enforcement

**Spec ref:** Section 6.3

**Preconditions:** `Runner` where `r.Config.VesselBudget()` returns nil. Vessel has a prompt workflow that would be expensive.

**Action:** Run the vessel to completion.

**Expected outcome:** The vessel completes all phases successfully regardless of cost. `BudgetExceeded()` is never called in an enforcing way. No failure state is reached due to cost.

**Verification:** Assert the vessel reaches `completed` status and no `"budget exceeded"` message appears in the vessel's failure reason or drain logs.

---

### S30: Tracer wired in drain.go after config load

**Spec ref:** Section 5.4

**Preconditions:** `cli/cmd/drain.go` (or equivalent) contains the tracer initialization block after config loading.

**Action:** Run `xylem drain` with observability enabled in `.xylem.yml` (endpoint set to empty string for stdout mode).

**Expected outcome:** Span output appears on stdout during the drain run. The runner's `Tracer` field is non-nil for the duration of the drain call. `tracer.Shutdown` is deferred and fires when the command exits (all spans are flushed).

**Verification:** Run the CLI with a test vessel and a stdout-mode tracer config; confirm span JSON appears in stdout and includes a `"drain_run"` span.

---

### S31: Tracer wired in daemon.go runDrain

**Spec ref:** Section 5.4

**Preconditions:** `cli/cmd/daemon.go` contains the same tracer initialization block inside `runDrain`, after intermediary wiring.

**Action:** Run `xylem daemon` briefly (or call `runDrain` directly in a test) with observability enabled.

**Expected outcome:** Each invocation of `runDrain` initializes a fresh tracer and defers its shutdown. Spans appear in trace output for each drain cycle and are flushed before the next cycle begins.

**Verification:** Confirm the tracer initialization and shutdown code path is exercised in the daemon's drain loop — no drain cycle runs with a stale or shutdown tracer.

---

### S32: Tracer shutdown deferred in both drain and daemon paths

**Spec ref:** Section 5.4

**Preconditions:** Both `drain.go` and `daemon.go` implement the `defer tracer.Shutdown(context.Background())` pattern.

**Action:** Run `xylem drain` with two pending vessels and interrupt it cleanly (context cancellation).

**Expected outcome:** Even on early exit, the deferred `Shutdown` call fires and flushes any pending spans to the exporter. No spans are lost in the stdout output — all started spans appear as completed entries.

**Verification:** Count spans in stdout output after an interrupted drain. Assert all started spans (drain_run + one vessel span per dequeued vessel) appear as completed entries in the flushed output.

---

## Manual Smoke Tests

Use the checked-in Guide 4B seed below instead of the live repo-root `.xylem`
tree.

| Scenario IDs | DTU status | Manifest | Seeded workdir | Notes |
| --- | --- | --- | --- | --- |
| S1-S8, S17-S24 | **Expected pass** | `cli/internal/dtu/testdata/ws3-observability-cost.yaml` | `cli/internal/dtu/testdata/manual-smoke/ws3-observability-cost/` | `package_probes` runs targeted `go test` coverage for `internal/observability` and `internal/cost`, then the seeded prompt workflow completes. |
| S9-S16, S25-S32 | **Known-fail / manual-triage** | same | same | The seed carries `observability:` and `cost:` config, but the current CLI path still does not emit drain/vessel/phase spans or persist per-vessel budget artifacts during a smoke run. |

### Run the seed

```bash
cd /Users/harry.nicholls/repos/xylem/cli
go build ./cmd/xylem
XYLEM_BIN="$PWD/xylem"
MANIFEST="$PWD/internal/dtu/testdata/ws3-observability-cost.yaml"
WORKDIR="$PWD/internal/dtu/testdata/manual-smoke/ws3-observability-cost"
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
```

**Expected pass right now**
- `package_probes.output` contains passing `go test` lines for
  `./internal/observability` and `./internal/cost`.
- `plan.output` and `implement.output` exist under `.xylem-state/phases/<vessel>/`.
- `summary.json` exists under `.xylem-state/phases/<vessel>/` for the seeded
  vessel run.

**Known-fail / manual-triage right now**
- The seeded `observability:` / `cost:` blocks are currently inert in the CLI
  path, so there is no end-to-end span stream or vessel budget artifact to
  inspect yet.
