package profiles_test

import (
	"io/fs"
	"testing"

	. "github.com/nicholls-inc/xylem/cli/internal/profiles"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestImplementHarnessEmbeddedHonoursPRFixes guards against the regression in
// issue #651: PR #600 raised max_turns from 30 to 50 on several phases and
// PR #645 escalated test_critic to tier: high, but both edits landed only in
// .xylem/workflows/implement-harness.yaml — not in the embedded profile copy
// under cli/internal/profiles/self-hosting-xylem/workflows/. On daemon
// restart the materializer wrote the stale embedded copy back over .xylem/,
// silently reverting both PRs.
//
// This test pins the embedded copy — the one that actually ships in the
// binary — to the intent of those PRs. If a future refactor regresses
// max_turns below 50 on a critical phase, or drops tier: high on a phase
// that needs cross-vendor review, this test fails.
func TestImplementHarnessEmbeddedHonoursPRFixes(t *testing.T) {
	profile, err := Load("self-hosting-xylem")
	require.NoError(t, err)

	data, err := fs.ReadFile(profile.FS, "workflows/implement-harness.yaml")
	require.NoError(t, err)

	var wf struct {
		Name   string `yaml:"name"`
		Phases []struct {
			Name     string `yaml:"name"`
			MaxTurns int    `yaml:"max_turns"`
			Tier     string `yaml:"tier"`
		} `yaml:"phases"`
	}
	require.NoError(t, yaml.Unmarshal(data, &wf))
	require.Equal(t, "implement-harness", wf.Name)

	phases := make(map[string]struct {
		MaxTurns int
		Tier     string
	})
	for _, p := range wf.Phases {
		phases[p.Name] = struct {
			MaxTurns int
			Tier     string
		}{p.MaxTurns, p.Tier}
	}

	// PR #600: raise max_turns from 30 to 50 on analyze, plan, test_critic,
	// pr_draft. Vessels were dying at "Reached max turns (30)" on complex
	// multi-file root causes; 50 is the empirically-validated floor.
	for _, phase := range []string{"analyze", "plan", "test_critic", "pr_draft"} {
		p, ok := phases[phase]
		require.Truef(t, ok, "implement-harness phase %q missing from embedded workflow", phase)
		require.GreaterOrEqualf(t, p.MaxTurns, 50,
			"implement-harness phase %q max_turns=%d — PR #600 requires >=50 to avoid max-turns aborts",
			phase, p.MaxTurns)
	}

	// PR #645: escalate test_critic to tier: high so the critic runs on a
	// different model vendor than implement. Same-vendor critique was shown
	// to miss >50% of real bugs in the deterministic-assurance roadmap
	// research. Dropping tier: high on test_critic silently defeats #645.
	criticTier := phases["test_critic"].Tier
	require.Equalf(t, "high", criticTier,
		"implement-harness test_critic phase tier=%q — PR #645 requires 'high' for cross-vendor critique",
		criticTier)
}
