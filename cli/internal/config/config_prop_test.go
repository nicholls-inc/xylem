package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestPropResolveStateDirRebasesRelativePaths(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		root := filepath.Join(
			rapid.StringMatching(`[A-Za-z0-9._-]{1,8}`).Draw(t, "root-a"),
			rapid.StringMatching(`[A-Za-z0-9._-]{1,8}`).Draw(t, "root-b"),
		)
		stateDir := filepath.Join(
			rapid.StringMatching(`[A-Za-z0-9._-]{1,8}`).Draw(t, "state-a"),
			rapid.StringMatching(`[A-Za-z0-9._-]{1,8}`).Draw(t, "state-b"),
		)

		got := ResolveStateDir(root, stateDir)
		want := filepath.Join(root, stateDir)
		if got != want {
			t.Fatalf("ResolveStateDir(%q, %q) = %q, want %q", root, stateDir, got, want)
		}
	})
}

func TestPropRuntimePathAddsSingleStatePrefixForControlPlane(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		root, err := os.MkdirTemp("", "runtime-path-control-plane-*")
		require.NoError(t, err)
		defer os.RemoveAll(root)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("state/\n"), 0o644))

		segments := []string{
			rapid.StringMatching(`[A-Za-z0-9._-]{1,8}`).Draw(t, "segment-a"),
			rapid.StringMatching(`[A-Za-z0-9._-]{1,8}`).Draw(t, "segment-b"),
			rapid.StringMatching(`[A-Za-z0-9._-]{1,8}`).Draw(t, "segment-c"),
		}

		got := RuntimePath(root, segments...)
		want := filepath.Join(append([]string{root, "state"}, segments...)...)
		if got != want {
			t.Fatalf("RuntimePath(%q, %#v) = %q, want %q", root, segments, got, want)
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

func TestPropHarnessReviewOutputDirRejectsTraversal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := Config{
			Harness: HarnessConfig{
				Review: HarnessReviewConfig{
					OutputDir: "../" + rapid.StringMatching(`[A-Za-z0-9._-]{1,12}`).Draw(t, "segment"),
				},
			},
		}

		err := cfg.validateHarness()
		if err == nil {
			t.Fatal("validateHarness() error = nil, want traversal rejection")
		}
		if !strings.Contains(err.Error(), "harness.review.output_dir") {
			t.Fatalf("validateHarness() error = %v, want harness.review.output_dir", err)
		}
	})
}

func TestPropNormalizeLegacyProvidersPreservesDefaultTierModels(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		includeClaude := rapid.Bool().Draw(t, "include-claude")
		includeCopilot := rapid.Bool().Draw(t, "include-copilot")
		if !includeClaude && !includeCopilot {
			includeClaude = true
		}

		cfg := Config{
			Concurrency: 1,
			MaxTurns:    10,
			Timeout:     "30m",
			LLMRouting: LLMRoutingConfig{
				DefaultTier: rapid.SampledFrom([]string{"high", "med", "low"}).Draw(t, "default-tier"),
			},
			Claude: ClaudeConfig{
				Command:      "claude",
				DefaultModel: rapid.StringMatching(`[a-z0-9.-]{4,24}`).Draw(t, "claude-model"),
			},
			Copilot: CopilotConfig{
				Command:      "copilot",
				DefaultModel: rapid.StringMatching(`[a-z0-9.-]{4,24}`).Draw(t, "copilot-model"),
			},
		}
		cfg.LLM = rapid.SampledFrom([]string{"", "claude", "copilot"}).Draw(t, "llm")
		if !includeClaude {
			cfg.Claude = ClaudeConfig{Command: "claude"}
		}
		if !includeCopilot {
			cfg.Copilot = CopilotConfig{Command: "copilot"}
		}

		cfg.normalizeProviders()

		defaultTier := cfg.LLMRouting.DefaultTier
		if defaultTier == "" {
			t.Fatal("normalizeProviders() left default tier empty")
		}

		wantProviders := map[string]string{}
		if includeClaude || cfg.LLM == "claude" || cfg.LLM == "" {
			wantProviders["claude"] = cfg.Claude.DefaultModel
		}
		if includeCopilot || cfg.LLM == "copilot" {
			wantProviders["copilot"] = cfg.Copilot.DefaultModel
		}
		if len(cfg.Providers) != len(wantProviders) {
			t.Fatalf("normalizeProviders() produced providers %#v, want %#v", cfg.Providers, wantProviders)
		}
		for name, wantModel := range wantProviders {
			provider, ok := cfg.Providers[name]
			if !ok {
				t.Fatalf("normalizeProviders() missing provider %q in %#v", name, cfg.Providers)
			}
			if got := provider.Tiers[defaultTier]; got != wantModel {
				t.Fatalf("%s tier model = %q, want %q", name, got, wantModel)
			}
		}

		routing, ok := cfg.LLMRouting.Tiers[defaultTier]
		if !ok {
			t.Fatalf("normalizeProviders() missing routing tier %q in %#v", defaultTier, cfg.LLMRouting.Tiers)
		}

		wantOrder := make([]string, 0, len(wantProviders))
		for _, name := range []string{"claude", "copilot"} {
			if _, ok := wantProviders[name]; ok {
				wantOrder = append(wantOrder, name)
			}
		}
		if cfg.LLM != "" && containsString(wantOrder, cfg.LLM) && wantOrder[0] != cfg.LLM {
			wantOrder = append([]string{cfg.LLM}, removeString(wantOrder, cfg.LLM)...)
		}
		if !reflect.DeepEqual(routing.Providers, wantOrder) {
			t.Fatalf("default provider order = %#v, want %#v", routing.Providers, wantOrder)
		}
	})
}

func TestPropValidationRequirementAcceptsAnyNonEmptyValidationCommand(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := validConfig()
		cfg.Sources = map[string]SourceConfig{
			"github": {
				Type: "github",
				Repo: "owner/name",
				Tasks: map[string]Task{
					"fix-checks": {Labels: []string{"ci"}, Workflow: "fix-pr-checks"},
				},
			},
		}
		commands := []*string{
			&cfg.Validation.Format,
			&cfg.Validation.Lint,
			&cfg.Validation.Build,
			&cfg.Validation.Test,
		}
		idx := rapid.IntRange(0, len(commands)-1).Draw(t, "command-index")
		*commands[idx] = rapid.StringMatching(`[a-z0-9 ./_-]{4,32}`).Draw(t, "command")

		if err := cfg.validateWorkflowRequirements(); err != nil {
			t.Fatalf("validateWorkflowRequirements() error = %v", err)
		}
	})
}

func TestPropEffectiveAutoMergeLabelsNeverReturnsBlank(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(0, 6).Draw(t, "size")
		labels := make([]string, 0, size)
		for i := 0; i < size; i++ {
			labels = append(labels, rapid.SampledFrom([]string{"ready-to-merge", "harness-impl", " ci ", "   "}).Draw(t, fmt.Sprintf("label-%d", i)))
		}

		got := (DaemonConfig{AutoMergeLabels: labels}).EffectiveAutoMergeLabels()
		if len(got) == 0 {
			t.Fatal("EffectiveAutoMergeLabels() returned no labels")
		}
		for _, label := range got {
			if strings.TrimSpace(label) == "" {
				t.Fatalf("EffectiveAutoMergeLabels() returned blank label in %#v", got)
			}
		}
	})
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func removeString(values []string, skip string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != skip {
			out = append(out, value)
		}
	}
	return out
}
