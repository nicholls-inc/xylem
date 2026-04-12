# 005 — Hardcoded pricing table for cost estimation

**Status:** Accepted
**Date:** 2026-04-12

## Context

`cost-report.json` includes a `total_cost_usd` field. For this field to be non-zero, the runner must resolve a `*ModelPricing` value for each model string observed in phase output. The resolution happens inside the runner hot path, once per phase, synchronously.

Two design options were considered:

1. **Network fetch** — query the provider's pricing API at cost-accounting time.
2. **Hardcoded table** — embed known rates in `estimate.go`; update manually when providers change prices.

## Decision

Use a hardcoded table (`DefaultPricingTable` in `cli/internal/cost/estimate.go`).

Lookup is by exact match, then longest-prefix match, so versioned model strings like `claude-sonnet-4-6` match the `claude-sonnet-4` prefix without requiring a table entry per release.

## Rationale

- **No latency.** Cost accounting runs in the runner's critical path. A network call introduces unbounded latency and a new failure mode that could block vessel completion.
- **No credentials required.** Fetching provider pricing APIs typically requires authentication; the cost package has no business holding API credentials.
- **Testability.** A static table is trivially testable with no mocking. A network call requires fakes, stubs, or live integration tests.
- **Staleness is disclosed.** The `note` field on every `VesselSummary` and the `summaryDisclaimer` constant already state that costs are estimates. Operators are not misled into treating figures as provider-reported values.

## Override path

Operators who need custom rates (self-hosted models, negotiated pricing, new models not yet in the table) can supply per-model overrides in `.xylem.yml`:

```yaml
providers:
  copilot:
    kind: copilot
    pricing:
      gpt-5.4:
        input_per_1m: 2.50
        output_per_1m: 10.00
```

`LookupPricingWithOverrides` checks this map before falling back to `DefaultPricingTable`. Last-write-wins when multiple providers define pricing for the same model key.

## Consequences

- Table entries must be updated manually when provider pricing changes. The `gpt-5.4` and `gpt-5.4-mini` entries added in this decision reflect OpenAI's published rates at the time of writing.
- Unknown models (no exact match, no prefix match) produce `CostUSD: 0` — the pre-existing behaviour. This is intentional and correct: zero cost is preferable to a fabricated estimate.
- The override path provides an escape hatch for operators without requiring a code change.
