package cost

import (
	"math"
	"slices"
	"testing"

	"pgregory.net/rapid"
)

// genPricedModel generates a model name present in DefaultPricingTable.
func genPricedModel() *rapid.Generator[string] {
	models := make([]string, 0, len(DefaultPricingTable))
	for model := range DefaultPricingTable {
		models = append(models, model)
	}
	slices.Sort(models)
	return rapid.SampledFrom(models)
}

func TestProp_EstimateTokensAlwaysNonNegative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		content := rapid.String().Draw(t, "content")
		if got := EstimateTokens(content); got < 0 {
			t.Fatalf("EstimateTokens(%q) = %d, want >= 0", content, got)
		}
	})
}

func TestProp_EstimateTokensMonotonicallyIncreasing(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.String().Draw(t, "base")
		suffix := rapid.String().Draw(t, "suffix")

		shorter := EstimateTokens(base)
		longer := EstimateTokens(base + suffix)
		if longer < shorter {
			t.Fatalf("EstimateTokens should be monotonic: shorter=%d longer=%d", shorter, longer)
		}
	})
}

func TestProp_EstimateCostNonNegativeForValidInputs(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		inputTokens := rapid.IntRange(0, 1_000_000).Draw(t, "input_tokens")
		outputTokens := rapid.IntRange(0, 1_000_000).Draw(t, "output_tokens")
		pricing := &ModelPricing{
			InputPer1M:  rapid.Float64Range(0, 100).Draw(t, "input_per_1m"),
			OutputPer1M: rapid.Float64Range(0, 100).Draw(t, "output_per_1m"),
		}

		if got := EstimateCost(inputTokens, outputTokens, pricing); got < 0 {
			t.Fatalf("EstimateCost() = %f, want >= 0", got)
		}
	})
}

func TestProp_EstimateCostNilPricingAlwaysZero(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		inputTokens := rapid.IntRange(0, 1_000_000).Draw(t, "input_tokens")
		outputTokens := rapid.IntRange(0, 1_000_000).Draw(t, "output_tokens")

		if got := EstimateCost(inputTokens, outputTokens, nil); got != 0 {
			t.Fatalf("EstimateCost() = %f, want 0", got)
		}
	})
}

func TestProp_EstimateCostLinearInTokens(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		inputTokens := rapid.IntRange(0, 500_000).Draw(t, "input_tokens")
		outputTokens := rapid.IntRange(0, 500_000).Draw(t, "output_tokens")
		pricing := &ModelPricing{
			InputPer1M:  rapid.Float64Range(0, 100).Draw(t, "input_per_1m"),
			OutputPer1M: rapid.Float64Range(0, 100).Draw(t, "output_per_1m"),
		}

		base := EstimateCost(inputTokens, outputTokens, pricing)
		doubled := EstimateCost(2*inputTokens, 2*outputTokens, pricing)
		if math.Abs(doubled-(2*base)) > 1e-9 {
			t.Fatalf("EstimateCost should be linear: doubled=%f base=%f", doubled, base)
		}
	})
}

func TestProp_LookupPricingExactMatchAlwaysReturnsEntry(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		model := genPricedModel().Draw(t, "model")
		got := LookupPricing(model)
		if got == nil {
			t.Fatalf("LookupPricing(%q) returned nil", model)
			return
		}

		want := DefaultPricingTable[model]
		if got.InputPer1M != want.InputPer1M || got.OutputPer1M != want.OutputPer1M {
			t.Fatalf("LookupPricing(%q) = %+v, want %+v", model, *got, want)
		}
	})
}

func TestProp_LookupPricingPrefixAppendNeverReturnsNil(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		model := genPricedModel().Draw(t, "model")
		suffix := rapid.StringMatching(`[a-z0-9-]{1,20}`).Draw(t, "suffix")
		got := LookupPricing(model + suffix)
		if got == nil {
			t.Fatalf("LookupPricing(%q) returned nil", model+suffix)
		}
	})
}
