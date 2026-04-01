package config

import (
	"math"
	"reflect"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"pgregory.net/rapid"
)

func protectedSurfacePathGen() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		".xylem/HARNESS.md",
		".xylem.yml",
		".xylem/workflows/*.yaml",
		".xylem/prompts/*/*.md",
		"docs/*.md",
		"configs/*.yml",
	})
}

func policyRuleConfigGen() *rapid.Generator[PolicyRuleConfig] {
	effects := []string{string(intermediary.Allow), string(intermediary.Deny), string(intermediary.RequireApproval)}

	return rapid.Custom(func(t *rapid.T) PolicyRuleConfig {
		return PolicyRuleConfig{
			Action:   rapid.SampledFrom([]string{"file_write", "git_push", "phase_execute", "*"}).Draw(t, "action"),
			Resource: rapid.SampledFrom([]string{".xylem/HARNESS.md", ".xylem.yml", "*", "main", "lint"}).Draw(t, "resource"),
			Effect:   rapid.SampledFrom(effects).Draw(t, "effect"),
		}
	})
}

func TestPropEffectiveProtectedSurfacesIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := &Config{
			Harness: HarnessConfig{
				ProtectedSurfaces: ProtectedSurfacesConfig{
					Paths: rapid.SliceOf(protectedSurfacePathGen()).Draw(t, "paths"),
				},
			},
		}

		first := cfg.EffectiveProtectedSurfaces()
		second := cfg.EffectiveProtectedSurfaces()
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("EffectiveProtectedSurfaces not idempotent: first=%v second=%v", first, second)
		}
	})
}

func TestPropEffectiveProtectedSurfacesNoneIsNil(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := &Config{
			Harness: HarnessConfig{
				ProtectedSurfaces: ProtectedSurfacesConfig{Paths: []string{"none"}},
			},
		}

		if got := cfg.EffectiveProtectedSurfaces(); got != nil {
			t.Fatalf("EffectiveProtectedSurfaces() = %v, want nil", got)
		}
	})
}

func TestPropObservabilitySampleRateInRange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := &Config{
			Concurrency: 1,
			MaxTurns:    1,
			Timeout:     "30s",
			Claude:      ClaudeConfig{Command: "claude"},
			Observability: ObservabilityConfig{
				SampleRate: rapid.Float64Range(0.0, 1.0).Draw(t, "sampleRate"),
			},
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() returned error: %v", err)
		}

		got := cfg.ObservabilitySampleRate()
		if got <= 0.0 || got > 1.0 {
			t.Fatalf("ObservabilitySampleRate() = %f, want in (0.0, 1.0]", got)
		}
	})
}

func TestPropVesselBudgetNilWhenNoLimits(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := &Config{
			Cost: CostConfig{
				Budget: &BudgetConfig{
					MaxCostUSD: rapid.Float64Range(-100.0, 0.0).Draw(t, "maxCostUSD"),
					MaxTokens:  rapid.IntRange(-1000000, 0).Draw(t, "maxTokens"),
				},
			},
		}

		if got := cfg.VesselBudget(); got != nil {
			t.Fatalf("VesselBudget() = %#v, want nil", got)
		}
	})
}

func TestPropBuildIntermediaryPoliciesNonEmpty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := &Config{}
		if rapid.Bool().Draw(t, "hasRules") {
			cfg.Harness.Policy.Rules = rapid.SliceOfN(policyRuleConfigGen(), 1, 5).Draw(t, "rules")
		}

		policies := cfg.BuildIntermediaryPolicies()
		if len(policies) == 0 {
			t.Fatal("BuildIntermediaryPolicies() returned no policies")
		}
		for i, policy := range policies {
			if len(policy.Rules) == 0 {
				t.Fatalf("BuildIntermediaryPolicies()[%d] has no rules", i)
			}
		}
	})
}

func TestPropDefaultPolicyLastRuleIsAllowAll(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		policy := DefaultPolicy()
		if len(policy.Rules) == 0 {
			t.Fatal("DefaultPolicy() returned no rules")
		}
		last := policy.Rules[len(policy.Rules)-1]
		if last.Action != "*" || last.Resource != "*" || last.Effect != intermediary.Allow {
			t.Fatalf("last rule = %+v, want allow-all", last)
		}
	})
}

func TestPropValidateRejectsOutOfRangeSampleRate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rate := rapid.Custom(func(t *rapid.T) float64 {
			if rapid.Bool().Draw(t, "negative") {
				return rapid.Float64Range(-1000, -math.SmallestNonzeroFloat64).Draw(t, "negativeRate")
			}
			return 1 + rapid.Float64Range(0.125, 1000).Draw(t, "overflowRate")
		}).Draw(t, "sampleRate")

		cfg := &Config{
			Observability: ObservabilityConfig{
				SampleRate: rate,
			},
		}

		if err := cfg.validateObservability(); err == nil {
			t.Fatalf("validateObservability() succeeded for out-of-range sample rate %f", rate)
		}
	})
}
