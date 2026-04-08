package config

import (
	"fmt"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

func protectedSurfacePathsGen() *rapid.Generator[[]string] {
	return rapid.Custom(func(t *rapid.T) []string {
		n := rapid.IntRange(1, 8).Draw(t, "count")
		paths := make([]string, n)
		for i := range n {
			paths[i] = rapid.StringMatching(`[A-Za-z0-9._/*-]+`).Draw(t, fmt.Sprintf("path-%d", i))
		}
		if len(paths) == 1 && paths[0] == "none" {
			paths[0] = ".xylem/HARNESS.md"
		}
		return paths
	})
}

func outOfRangeSampleRateGen() *rapid.Generator[float64] {
	return rapid.Custom(func(t *rapid.T) float64 {
		if rapid.Bool().Draw(t, "below-range") {
			return rapid.Float64Range(-1e9, 0).Draw(t, "sample-rate")
		}
		return rapid.Float64Range(1.001, 1e9).Draw(t, "sample-rate")
	})
}

func TestPropEffectiveProtectedSurfacesNeverAliasesInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		paths := protectedSurfacePathsGen().Draw(t, "paths")
		cfg := Config{
			Harness: HarnessConfig{
				ProtectedSurfaces: ProtectedSurfacesConfig{
					Paths: append([]string(nil), paths...),
				},
			},
		}

		got := cfg.EffectiveProtectedSurfaces()
		if !reflect.DeepEqual(got, paths) {
			t.Fatalf("EffectiveProtectedSurfaces() = %#v, want %#v", got, paths)
		}

		got[0] = got[0] + "-mutated"

		if again := cfg.EffectiveProtectedSurfaces(); !reflect.DeepEqual(again, paths) {
			t.Fatalf("EffectiveProtectedSurfaces() after mutation = %#v, want %#v", again, paths)
		}
	})
}

func TestPropObservabilitySampleRateDefaultForOutOfRange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := Config{
			Observability: ObservabilityConfig{
				SampleRate: outOfRangeSampleRateGen().Draw(t, "sample-rate"),
			},
		}

		if got := cfg.ObservabilitySampleRate(); got != 1.0 {
			t.Fatalf("ObservabilitySampleRate() = %v, want 1.0", got)
		}
	})
}

func TestPropVesselBudgetNilWhenBothLimitsNonPositive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := Config{
			Cost: CostConfig{
				Budget: &BudgetConfig{
					MaxCostUSD: rapid.Float64Range(-1e6, 0).Draw(t, "max-cost-usd"),
					MaxTokens:  rapid.IntRange(-1_000_000, 0).Draw(t, "max-tokens"),
				},
			},
		}

		if budget := cfg.VesselBudget(); budget != nil {
			t.Fatalf("VesselBudget() = %#v, want nil", budget)
		}
	})
}

func TestPropValidateHarnessAcceptsValidGlobs(t *testing.T) {
	validPatterns := []string{
		".xylem/*.md",
		"*.yaml",
		"**/*",
		"foo/bar",
		".xylem/workflows/*.yaml",
		".xylem/prompts/*/*.md",
	}

	rapid.Check(t, func(t *rapid.T) {
		pattern := rapid.SampledFrom(validPatterns).Draw(t, "pattern")
		cfg := Config{
			Harness: HarnessConfig{
				ProtectedSurfaces: ProtectedSurfacesConfig{
					Paths: []string{pattern},
				},
			},
		}

		if err := cfg.validateHarness(); err != nil {
			t.Fatalf("validateHarness() error for %q: %v", pattern, err)
		}
	})
}
