package cost

const tokenEstimateDivisor = 4

// EstimateTokens provides a rough token count using the len/4 heuristic.
// Matches ctxmgr.EstimateTokens for consistency.
func EstimateTokens(content string) int {
	return len(content) / tokenEstimateDivisor
}

// ModelPricing holds per-token cost rates for a model.
type ModelPricing struct {
	InputPer1M  float64 // USD per 1M input tokens
	OutputPer1M float64 // USD per 1M output tokens
}

// DefaultPricingTable maps model name prefixes to pricing.
var DefaultPricingTable = map[string]ModelPricing{
	"claude-sonnet-4": {InputPer1M: 3.00, OutputPer1M: 15.00},
	"claude-opus-4":   {InputPer1M: 15.00, OutputPer1M: 75.00},
	"claude-haiku-4":  {InputPer1M: 0.80, OutputPer1M: 4.00},
	"claude-haiku-3":  {InputPer1M: 0.25, OutputPer1M: 1.25},
	"claude-sonnet-3": {InputPer1M: 3.00, OutputPer1M: 15.00},
	"gpt-5.4-mini":    {InputPer1M: 0.15, OutputPer1M: 0.60},
	"gpt-5.4":         {InputPer1M: 2.50, OutputPer1M: 10.00},
}

// EstimateCost computes approximate cost from token counts and pricing.
// Returns 0 if pricing is nil.
func EstimateCost(inputTokens, outputTokens int, pricing *ModelPricing) float64 {
	if pricing == nil {
		return 0
	}

	return float64(inputTokens)*pricing.InputPer1M/1_000_000 +
		float64(outputTokens)*pricing.OutputPer1M/1_000_000
}

// LookupPricing finds pricing for a model name.
// Tries exact match, then longest prefix match. Returns nil if no match.
func LookupPricing(model string) *ModelPricing {
	if pricing, ok := DefaultPricingTable[model]; ok {
		return &pricing
	}

	bestKey := ""
	for key := range DefaultPricingTable {
		if len(key) <= len(model) && model[:len(key)] == key && len(key) > len(bestKey) {
			bestKey = key
		}
	}

	if bestKey == "" {
		return nil
	}

	pricing := DefaultPricingTable[bestKey]
	return &pricing
}

// LookupPricingWithOverrides checks overrides first, then falls back to
// DefaultPricingTable via LookupPricing. Returns nil if no match in either.
func LookupPricingWithOverrides(model string, overrides map[string]ModelPricing) *ModelPricing {
	if p, ok := overrides[model]; ok {
		return &p
	}
	return LookupPricing(model)
}
