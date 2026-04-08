package cost

import (
	"math"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
)

func TestSmoke_S17_EstimateTokensKnownString(t *testing.T) {
	content := "Hello, world! This is a test string."

	if got := EstimateTokens(content); got != 9 {
		t.Fatalf("EstimateTokens(%q) = %d, want 9", content, got)
	}
}

func TestSmoke_S18_EstimateTokensEmptyString(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf(`EstimateTokens("") = %d, want 0`, got)
	}
}

func TestSmoke_S19_EstimateCostKnownPricing(t *testing.T) {
	got := EstimateCost(1_000_000, 500_000, &ModelPricing{
		InputPer1M:  3.00,
		OutputPer1M: 15.00,
	})

	if math.Abs(got-10.5) > 1e-9 {
		t.Fatalf("EstimateCost() = %f, want 10.5", got)
	}
}

func TestSmoke_S20_EstimateCostNilPricing(t *testing.T) {
	if got := EstimateCost(5000, 1000, nil); got != 0 {
		t.Fatalf("EstimateCost() = %f, want 0", got)
	}
}

func TestSmoke_S21_LookupPricingExactMatch(t *testing.T) {
	pricing := LookupPricing("claude-sonnet-4")
	if pricing == nil {
		t.Fatal("LookupPricing() returned nil, want pricing")
		return
	}
	if pricing.InputPer1M != 3.00 || pricing.OutputPer1M != 15.00 {
		t.Fatalf("LookupPricing() = %+v, want input=3.00 output=15.00", *pricing)
	}
}

func TestSmoke_S22_LookupPricingPrefixFallback(t *testing.T) {
	pricing := LookupPricing("claude-sonnet-4-20250514")
	if pricing == nil {
		t.Fatal("LookupPricing() returned nil, want pricing")
		return
	}
	if pricing.InputPer1M != 3.00 || pricing.OutputPer1M != 15.00 {
		t.Fatalf("LookupPricing() = %+v, want input=3.00 output=15.00", *pricing)
	}
}

func TestSmoke_S23_LookupPricingLongestPrefix(t *testing.T) {
	original := DefaultPricingTable
	DefaultPricingTable = map[string]ModelPricing{
		"claude":         {InputPer1M: 99.0, OutputPer1M: 99.0},
		"claude-haiku":   {InputPer1M: 9.0, OutputPer1M: 9.0},
		"claude-haiku-4": {InputPer1M: 0.80, OutputPer1M: 4.00},
	}
	t.Cleanup(func() {
		DefaultPricingTable = original
	})

	pricing := LookupPricing("claude-haiku-4-20250514")
	if pricing == nil {
		t.Fatal("LookupPricing() returned nil, want pricing")
		return
	}
	if pricing.InputPer1M != 0.80 || pricing.OutputPer1M != 4.00 {
		t.Fatalf("LookupPricing() = %+v, want input=0.80 output=4.00", *pricing)
	}
}

func TestSmoke_S24_LookupPricingUnrecognised(t *testing.T) {
	if pricing := LookupPricing("gpt-4o"); pricing != nil {
		t.Fatalf("LookupPricing() = %+v, want nil", *pricing)
	}
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
