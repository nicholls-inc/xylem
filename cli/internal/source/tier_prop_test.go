package source

import (
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"pgregory.net/rapid"
)

func TestPropResolveTaskTierPrefersExplicitTaskTier(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		taskTier := rapid.StringMatching(`[a-z][a-z0-9-]{0,7}`).Draw(t, "task-tier")
		defaultTier := rapid.StringMatching(`[a-z][a-z0-9-]{0,7}`).Draw(t, "default-tier")
		paddedTaskTier := rapid.SampledFrom([]string{
			taskTier,
			" " + taskTier,
			taskTier + " ",
			"\t" + taskTier + "\n",
		}).Draw(t, "padded-task-tier")

		if got := ResolveTaskTier(paddedTaskTier, defaultTier); got != taskTier {
			t.Fatalf("ResolveTaskTier(%q, %q) = %q, want %q", paddedTaskTier, defaultTier, got, taskTier)
		}
	})
}

func TestPropResolveTaskTierFallsBackToDefaultOrMed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		defaultTier := rapid.SampledFrom([]string{
			"",
			"   ",
			rapid.StringMatching(`[a-z][a-z0-9-]{0,7}`).Draw(t, "configured-default"),
		}).Draw(t, "default-tier")

		got := ResolveTaskTier("", defaultTier)
		want := strings.TrimSpace(defaultTier)
		if want == "" {
			want = config.DefaultLLMRoutingTier
		}
		if got != want {
			t.Fatalf("ResolveTaskTier(%q, %q) = %q, want %q", "", defaultTier, got, want)
		}
	})
}
