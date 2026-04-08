package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/cost"
)

type executionUsage struct {
	Source            cost.UsageSource
	InputTokens       int
	OutputTokens      int
	CostUSD           *float64
	UnavailableReason string
}

// UsageExtractor extracts provider-reported usage from phase output.
type UsageExtractor interface {
	ExtractUsage(provider, model string, output []byte) executionUsage
}

type defaultUsageExtractor struct{}

func (defaultUsageExtractor) ExtractUsage(provider, model string, output []byte) executionUsage {
	if usage, ok := parseStructuredUsage(output); ok {
		return usage
	}

	name := provider
	if name == "" {
		name = "provider"
	}
	reason := fmt.Sprintf("%s output did not include structured usage metadata", name)
	if len(bytes.TrimSpace(output)) == 0 {
		reason = fmt.Sprintf("%s output was empty; no structured usage metadata was available", name)
	}
	return executionUsage{
		Source:            cost.UsageSourceUnavailable,
		UnavailableReason: reason,
	}
}

func parseStructuredUsage(output []byte) (executionUsage, bool) {
	candidates := [][]byte{bytes.TrimSpace(output)}
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			candidates = append(candidates, line)
		}
	}

	for i := len(candidates) - 1; i >= 0; i-- {
		usage, ok := parseUsageCandidate(candidates[i])
		if ok {
			return usage, true
		}
	}
	return executionUsage{}, false
}

func parseUsageCandidate(candidate []byte) (executionUsage, bool) {
	if len(candidate) == 0 {
		return executionUsage{}, false
	}

	var raw map[string]any
	if err := json.Unmarshal(candidate, &raw); err != nil {
		return executionUsage{}, false
	}

	if nested, ok := raw["usage"].(map[string]any); ok {
		raw = nested
	}

	inputTokens, okIn := usageInt(raw, "input_tokens", "prompt_tokens", "inputTokens", "promptTokens")
	outputTokens, okOut := usageInt(raw, "output_tokens", "completion_tokens", "outputTokens", "completionTokens")
	if !okIn || !okOut {
		return executionUsage{}, false
	}

	var costUSD *float64
	if value, ok := usageFloat(raw, "cost_usd", "costUSD"); ok {
		costUSD = &value
	}

	return executionUsage{
		Source:       cost.UsageSourceReported,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CostUSD:      costUSD,
	}, true
}

func usageInt(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		case json.Number:
			n, err := v.Int64()
			if err == nil {
				return int(n), true
			}
		case string:
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func usageFloat(raw map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return v, true
		case json.Number:
			n, err := v.Float64()
			if err == nil {
				return n, true
			}
		case string:
			var n float64
			if _, err := fmt.Sscanf(strings.TrimSpace(v), "%f", &n); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}
