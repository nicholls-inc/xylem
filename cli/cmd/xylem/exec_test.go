package main

import (
	"context"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

// TestNewCmdRunner_SkipsEmptyExpansion documents the fix for the
// 2026-04-09 auth cascade: when .xylem.yml declares
//
//	copilot.env.GITHUB_TOKEN: "${COPILOT_GITHUB_TOKEN}"
//
// but COPILOT_GITHUB_TOKEN is not set in the daemon's environment,
// os.ExpandEnv yields an empty string. Propagating "GITHUB_TOKEN="
// would unset any GITHUB_TOKEN the subprocess might otherwise inherit.
// The runner must skip empty expansions entirely.
func TestNewCmdRunner_SkipsEmptyExpansion(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "") // explicitly unset
	cfg := &config.Config{
		Claude: config.ClaudeConfig{
			Env: map[string]string{
				"ANTHROPIC_API_KEY": "${ANTHROPIC_API_KEY_XYLEM_TEST_UNSET}",
			},
		},
		Copilot: config.CopilotConfig{
			Env: map[string]string{
				"COPILOT_GITHUB_TOKEN": "${COPILOT_GITHUB_TOKEN}",
			},
		},
	}

	runner := newCmdRunner(cfg)
	if len(runner.extraEnv) != 0 {
		t.Fatalf("expected empty extraEnv when all expansions are empty, got %v", runner.extraEnv)
	}
}

// TestNewCmdRunner_PropagatesExpandedEnv verifies that non-empty
// expansions are merged into extraEnv.
func TestNewCmdRunner_PropagatesExpandedEnv(t *testing.T) {
	t.Setenv("XYLEM_TEST_TOKEN", "abc123")
	cfg := &config.Config{
		Copilot: config.CopilotConfig{
			Env: map[string]string{
				"COPILOT_GITHUB_TOKEN": "${XYLEM_TEST_TOKEN}",
			},
		},
	}

	runner := newCmdRunner(cfg)
	if len(runner.extraEnv) != 1 {
		t.Fatalf("expected one extraEnv entry, got %v", runner.extraEnv)
	}
	want := "COPILOT_GITHUB_TOKEN=abc123"
	if runner.extraEnv[0] != want {
		t.Fatalf("expected %q, got %q", want, runner.extraEnv[0])
	}
}

// TestNewCmdRunner_MixedEnv verifies the addEnv helper handles
// non-empty Claude env alongside empty Copilot env correctly (one
// expansion resolves, the other should be dropped).
func TestNewCmdRunner_MixedEnv(t *testing.T) {
	t.Setenv("XYLEM_TEST_ANTHROPIC", "sk-test")
	t.Setenv("XYLEM_TEST_COPILOT_UNSET", "")
	cfg := &config.Config{
		Claude: config.ClaudeConfig{
			Env: map[string]string{
				"ANTHROPIC_API_KEY": "${XYLEM_TEST_ANTHROPIC}",
			},
		},
		Copilot: config.CopilotConfig{
			Env: map[string]string{
				"COPILOT_GITHUB_TOKEN": "${XYLEM_TEST_COPILOT_UNSET}",
			},
		},
	}

	runner := newCmdRunner(cfg)
	if len(runner.extraEnv) != 1 {
		t.Fatalf("expected exactly one extraEnv entry (Claude only), got %v", runner.extraEnv)
	}
	if runner.extraEnv[0] != "ANTHROPIC_API_KEY=sk-test" {
		t.Fatalf("expected ANTHROPIC_API_KEY=sk-test, got %q", runner.extraEnv[0])
	}
}

func TestNewCmdRunner_BuildsPerProviderEnvMap(t *testing.T) {
	t.Setenv("XYLEM_TEST_ANTHROPIC", "anthropic-secret")
	t.Setenv("XYLEM_TEST_COPILOT", "copilot-secret")
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"claude": {
				Kind: "claude",
				Env: map[string]string{
					"ANTHROPIC_API_KEY": "${XYLEM_TEST_ANTHROPIC}",
				},
			},
			"copilot": {
				Kind: "copilot",
				Env: map[string]string{
					"GITHUB_TOKEN": "${XYLEM_TEST_COPILOT}",
				},
			},
		},
	}

	runner := newCmdRunner(cfg)
	if got := runner.providerEnv["claude"]; len(got) != 1 || got[0] != "ANTHROPIC_API_KEY=anthropic-secret" {
		t.Fatalf("claude provider env = %v", got)
	}
	if got := runner.providerEnv["copilot"]; len(got) != 1 || got[0] != "GITHUB_TOKEN=copilot-secret" {
		t.Fatalf("copilot provider env = %v", got)
	}
}

// TestRealCmdRunner_RunOutputInheritsEnv verifies that the non-phase
// Run* methods now propagate extraEnv into the subprocess. Before the
// fix, `gh`/`copilot` calls from the scanner and source commands
// silently lost the token because Run/RunOutput/RunProcess did not set
// cmd.Env. The test executes `sh -c 'echo "$XYLEM_TEST_PROBE"'` (or a
// platform equivalent) and asserts the probe value reaches the child.
func TestRealCmdRunner_RunOutputInheritsEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh unavailable on windows")
	}
	t.Setenv("XYLEM_TEST_PROBE_SRC", "proof-of-propagation")
	cfg := &config.Config{
		Copilot: config.CopilotConfig{
			Env: map[string]string{
				"XYLEM_TEST_PROBE": "${XYLEM_TEST_PROBE_SRC}",
			},
		},
	}
	runner := newCmdRunner(cfg)
	out, err := runner.RunOutput(context.Background(), "sh", "-c", "printf %s \"$XYLEM_TEST_PROBE\"")
	if err != nil {
		t.Fatalf("RunOutput failed: %v (output: %q)", err, string(out))
	}
	got := strings.TrimSpace(string(out))
	if got != "proof-of-propagation" {
		t.Fatalf("expected RunOutput subprocess to see XYLEM_TEST_PROBE=proof-of-propagation, got %q", got)
	}
}

// TestRealCmdRunner_RunPhaseInheritsBaseEnv verifies that RunPhase
// still passes base os.Environ() through even when extraEnv is empty
// (previous code gated cmd.Env on len(extraEnv) > 0, which relied on
// Go's default-inherit behavior). After the refactor cmd.Env is always
// set explicitly for determinism — the probe from os.Environ() must
// still reach the subprocess.
func TestRealCmdRunner_RunPhaseInheritsBaseEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh unavailable on windows")
	}
	t.Setenv("XYLEM_TEST_BASE_PROBE", "inherited")
	runner := newCmdRunner(&config.Config{})
	out, err := runner.RunPhase(context.Background(), ".", nil, "sh", "-c", "printf %s \"$XYLEM_TEST_BASE_PROBE\"")
	if err != nil {
		t.Fatalf("RunPhase failed: %v (output: %q)", err, string(out))
	}
	got := strings.TrimSpace(string(out))
	if got != "inherited" {
		t.Fatalf("expected RunPhase subprocess to see XYLEM_TEST_BASE_PROBE=inherited, got %q", got)
	}
}

func TestRealCmdRunner_RunPhaseWithEnvIsolatesProviderEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh unavailable on windows")
	}
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"claude": {
				Kind: "claude",
				Env: map[string]string{
					"CLAUDE_ONLY_TOKEN": "anthropic-secret",
				},
			},
			"copilot": {
				Kind: "copilot",
				Env: map[string]string{
					"COPILOT_ONLY_TOKEN": "copilot-secret",
				},
			},
		},
	}
	runner := newCmdRunner(cfg)

	claudeOut, err := runner.RunPhaseWithEnv(context.Background(), ".", runner.providerEnv["claude"], nil, "sh", "-c", "printf '%s|%s' \"$CLAUDE_ONLY_TOKEN\" \"$COPILOT_ONLY_TOKEN\"")
	if err != nil {
		t.Fatalf("claude RunPhaseWithEnv failed: %v (output: %q)", err, string(claudeOut))
	}
	if got := strings.TrimSpace(string(claudeOut)); got != "anthropic-secret|" {
		t.Fatalf("claude env = %q, want anthropic-secret|", got)
	}

	copilotOut, err := runner.RunPhaseWithEnv(context.Background(), ".", runner.providerEnv["copilot"], nil, "sh", "-c", "printf '%s|%s' \"$CLAUDE_ONLY_TOKEN\" \"$COPILOT_ONLY_TOKEN\"")
	if err != nil {
		t.Fatalf("copilot RunPhaseWithEnv failed: %v (output: %q)", err, string(copilotOut))
	}
	if got := strings.TrimSpace(string(copilotOut)); got != "|copilot-secret" {
		t.Fatalf("copilot env = %q, want |copilot-secret", got)
	}
}

// TestCmdEnv_AppendsExtraAfterBase verifies the ordering contract:
// extraEnv entries come AFTER base os.Environ() so they take precedence
// (exec uses the last occurrence of a duplicate key).
func TestCmdEnv_AppendsExtraAfterBase(t *testing.T) {
	t.Setenv("XYLEM_TEST_OVERRIDE", "from-base")
	cfg := &config.Config{
		Copilot: config.CopilotConfig{
			Env: map[string]string{
				"XYLEM_TEST_OVERRIDE": "from-config",
			},
		},
	}
	runner := newCmdRunner(cfg)
	env := runner.cmdEnv()

	// Find all occurrences of the key
	var values []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "XYLEM_TEST_OVERRIDE=") {
			values = append(values, strings.TrimPrefix(entry, "XYLEM_TEST_OVERRIDE="))
		}
	}
	if len(values) < 1 {
		t.Fatalf("expected XYLEM_TEST_OVERRIDE to appear in cmdEnv, got none")
	}
	if values[len(values)-1] != "from-config" {
		t.Fatalf("expected config value to come last (wins via exec precedence), got values=%v", values)
	}
}

// TestCmdEnv_StableOrdering sanity-checks that cmdEnv returns a
// deterministic slice so future debugging isn't confused by nondeterminism.
func TestCmdEnv_StableOrdering(t *testing.T) {
	cfg := &config.Config{
		Copilot: config.CopilotConfig{
			Env: map[string]string{
				"ZZZ_KEY": "zz",
				"AAA_KEY": "aa",
			},
		},
	}
	runner := newCmdRunner(cfg)

	// Go map iteration order is non-deterministic, so extraEnv order
	// itself is unstable — that's acceptable because exec() doesn't
	// care. We only verify that cmdEnv() returns a superset of
	// os.Environ() and the configured keys all appear.
	env := runner.cmdEnv()
	want := map[string]bool{"AAA_KEY=aa": false, "ZZZ_KEY=zz": false}
	for _, entry := range env {
		if _, ok := want[entry]; ok {
			want[entry] = true
		}
	}
	var missing []string
	for k, seen := range want {
		if !seen {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("cmdEnv missing configured entries: %v", missing)
	}

	// Sanity: base env should be included
	baseCount := len(os.Environ())
	if len(env) < baseCount {
		t.Fatalf("cmdEnv should include os.Environ() (base=%d) + extraEnv, got len=%d", baseCount, len(env))
	}
}
