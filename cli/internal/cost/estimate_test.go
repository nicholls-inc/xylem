package cost

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmoke_S17_EstimateTokensReturnsLen4ForKnownString(t *testing.T) {
	content := "Hello, world! This is a test string."

	assert.Equal(t, 9, EstimateTokens(content))
}

func TestSmoke_S18_EstimateTokensReturnsZeroForEmptyString(t *testing.T) {
	assert.Equal(t, 0, EstimateTokens(""))
}

func TestSmoke_S19_EstimateCostWithKnownPricingProducesCorrectArithmetic(t *testing.T) {
	got := EstimateCost(1_000_000, 500_000, &ModelPricing{
		InputPer1M:  3.00,
		OutputPer1M: 15.00,
	})

	assert.InDelta(t, 10.5, got, 1e-9)
}

func TestSmoke_S20_EstimateCostWithNilPricingReturnsZero(t *testing.T) {
	assert.Equal(t, 0.0, EstimateCost(5000, 1000, nil))
}

func TestSmoke_S21_LookupPricingExactMatchReturnsCorrectPricing(t *testing.T) {
	pricing := LookupPricing("claude-sonnet-4")

	require.NotNil(t, pricing)
	assert.Equal(t, 3.00, pricing.InputPer1M)
	assert.Equal(t, 15.00, pricing.OutputPer1M)
}

func TestSmoke_S22_LookupPricingFallsBackToPrefixMatchForVersionedModelName(t *testing.T) {
	pricing := LookupPricing("claude-sonnet-4-20250514")

	require.NotNil(t, pricing)
	assert.Equal(t, 3.00, pricing.InputPer1M)
	assert.Equal(t, 15.00, pricing.OutputPer1M)
}

func TestSmoke_S23_LookupPricingLongestPrefixWinsWhenMultiplePrefixesMatch(t *testing.T) {
	original := DefaultPricingTable
	DefaultPricingTable = map[string]ModelPricing{
		"claude-haiku":   {InputPer1M: 9.0, OutputPer1M: 9.0},
		"claude-haiku-3": {InputPer1M: 0.25, OutputPer1M: 1.25},
		"claude-haiku-4": {InputPer1M: 0.80, OutputPer1M: 4.00},
	}
	t.Cleanup(func() {
		DefaultPricingTable = original
	})

	pricing := LookupPricing("claude-haiku-4-5")

	require.NotNil(t, pricing)
	assert.Equal(t, 0.80, pricing.InputPer1M)
	assert.Equal(t, 4.00, pricing.OutputPer1M)
}

func TestSmoke_S24_LookupPricingReturnsNilForUnrecognisedModel(t *testing.T) {
	assert.Nil(t, LookupPricing("gpt-4o"))
}

func TestSmoke_S25_LookupPricingGPT54ReturnsNonNilWithPositiveRates(t *testing.T) {
	pricing := LookupPricing("gpt-5.4")

	require.NotNil(t, pricing)
	assert.Greater(t, pricing.InputPer1M, 0.0)
	assert.Greater(t, pricing.OutputPer1M, 0.0)
}

func TestSmoke_S26_LookupPricingGPT54MiniReturnsNonNilWithPositiveRates(t *testing.T) {
	pricing := LookupPricing("gpt-5.4-mini")

	require.NotNil(t, pricing)
	assert.Greater(t, pricing.InputPer1M, 0.0)
	assert.Greater(t, pricing.OutputPer1M, 0.0)
}

func TestSmoke_S27_LookupPricingWithOverridesOverrideWinsOverDefault(t *testing.T) {
	overrides := map[string]ModelPricing{
		"claude-sonnet-4": {InputPer1M: 99.0, OutputPer1M: 199.0},
	}
	pricing := LookupPricingWithOverrides("claude-sonnet-4", overrides)

	require.NotNil(t, pricing)
	assert.InDelta(t, 99.0, pricing.InputPer1M, 1e-9)
	assert.InDelta(t, 199.0, pricing.OutputPer1M, 1e-9)
}

func TestSmoke_S28_LookupPricingWithOverridesFallsBackToDefaultWhenModelAbsent(t *testing.T) {
	overrides := map[string]ModelPricing{
		"other-model": {InputPer1M: 1.0, OutputPer1M: 2.0},
	}
	pricing := LookupPricingWithOverrides("claude-sonnet-4", overrides)

	require.NotNil(t, pricing)
	assert.InDelta(t, 3.00, pricing.InputPer1M, 1e-9)
	assert.InDelta(t, 15.00, pricing.OutputPer1M, 1e-9)
}

func TestSmoke_S29_LookupPricingWithNilOverridesReturnsTableValues(t *testing.T) {
	got := LookupPricingWithOverrides("claude-haiku-4", nil)

	require.NotNil(t, got)
	assert.InDelta(t, 0.80, got.InputPer1M, 1e-9)
	assert.InDelta(t, 4.00, got.OutputPer1M, 1e-9)
}

func TestSmoke_S30_LookupPricingWithOverridesUnknownModelReturnsNil(t *testing.T) {
	pricing := LookupPricingWithOverrides("model-xyz-unknown", nil)

	assert.Nil(t, pricing)
}

func TestEstimateTokensMatchesCtxmgr(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "short", content: "abc"},
		{name: "sentence", content: "The quick brown fox jumps over the lazy dog."},
		{name: "multiline", content: "line one\nline two\nline three"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.content)
			want := ctxmgr.EstimateTokens(tt.content)
			if got != want {
				t.Fatalf("EstimateTokens(%q) = %d, ctxmgr.EstimateTokens() = %d", tt.content, got, want)
			}
		})
	}
}
