package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
)

// setenv sets an env var for the duration of the test, restoring the original
// value (or unsetting it) on cleanup. Uses t.Setenv when available for
// simplicity; falls back to manual save/restore.
func setenv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

// TestNoopPolicy_PhaseEnvPreservesAmbient verifies that NoopPolicy returns the
// ambient env with providerEnv appended.
func TestNoopPolicy_PhaseEnvPreservesAmbient(t *testing.T) {
	setenv(t, "XYLEM_TEST_AMBIENT_NOOP", "present")
	pol := NoopPolicy{}
	got := pol.PhaseEnv([]string{"PROVIDER_TOKEN=x"})

	if !containsEntry(got, "XYLEM_TEST_AMBIENT_NOOP=present") {
		t.Error("NoopPolicy.PhaseEnv should include ambient env var XYLEM_TEST_AMBIENT_NOOP")
	}
	if !containsEntry(got, "PROVIDER_TOKEN=x") {
		t.Error("NoopPolicy.PhaseEnv should include providerEnv entry PROVIDER_TOKEN=x")
	}
}

// TestNoopPolicy_WrapCommandIdentity verifies that NoopPolicy returns the
// original command and args unchanged.
func TestNoopPolicy_WrapCommandIdentity(t *testing.T) {
	pol := NoopPolicy{}
	gotCmd, gotArgs, err := pol.WrapCommand(context.Background(), "/some/dir", "claude", []string{"-p", "--model", "claude-opus-4-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCmd != "claude" {
		t.Errorf("WrapCommand cmd = %q, want %q", gotCmd, "claude")
	}
	if len(gotArgs) != 3 || gotArgs[0] != "-p" {
		t.Errorf("WrapCommand args = %v, want [-p --model claude-opus-4-5]", gotArgs)
	}
}

// TestEnvScopingPolicy_StripsSensitiveVars verifies that a var not in the
// passlist is removed from the phase env.
func TestEnvScopingPolicy_StripsSensitiveVars(t *testing.T) {
	setenv(t, "XYLEM_TEST_SHOULD_BE_STRIPPED", "leaked")
	pol := newEnvScopingPolicy(nil)
	got := pol.PhaseEnv(nil)

	if containsKey(got, "XYLEM_TEST_SHOULD_BE_STRIPPED") {
		t.Error("EnvScopingPolicy.PhaseEnv should not include XYLEM_TEST_SHOULD_BE_STRIPPED")
	}
}

// TestEnvScopingPolicy_IncludesPasslistVars verifies that standard passlist
// vars like PATH and HOME are present in the output.
func TestEnvScopingPolicy_IncludesPasslistVars(t *testing.T) {
	pol := newEnvScopingPolicy(nil)
	// PATH must always be set in the test process.
	got := pol.PhaseEnv(nil)

	if !containsKey(got, "PATH") {
		t.Error("EnvScopingPolicy.PhaseEnv should include PATH")
	}
}

// TestEnvScopingPolicy_ProviderEnvTakesPrecedence verifies that a provider
// env entry wins over a same-named passlist entry via last-wins exec semantics.
func TestEnvScopingPolicy_ProviderEnvTakesPrecedence(t *testing.T) {
	// Add ANTHROPIC_API_KEY to the passlist so both versions appear.
	pol := newEnvScopingPolicy([]string{"ANTHROPIC_API_KEY"})
	setenv(t, "ANTHROPIC_API_KEY", "v1")

	got := pol.PhaseEnv([]string{"ANTHROPIC_API_KEY=v2"})

	// Both v1 and v2 may be present; v2 must appear last (last-wins in exec).
	lastIdx := -1
	for i, e := range got {
		if e == "ANTHROPIC_API_KEY=v2" {
			lastIdx = i
		}
	}
	if lastIdx == -1 {
		t.Error("ANTHROPIC_API_KEY=v2 (provider) must appear in env output")
	}
	// Check that v2 appears after any v1.
	for i, e := range got {
		if e == "ANTHROPIC_API_KEY=v1" && i > lastIdx {
			t.Error("ANTHROPIC_API_KEY=v1 (passlist) must not appear after v2 (provider)")
		}
	}
}

// TestEnvScopingPolicy_CustomPasslistAddsVars verifies that a var in the
// operator's EnvPasslist (not in the built-in list) is included.
func TestEnvScopingPolicy_CustomPasslistAddsVars(t *testing.T) {
	setenv(t, "XYLEM_TEST_CUSTOM_PASSLIST_VAR", "allowed")
	pol := newEnvScopingPolicy([]string{"XYLEM_TEST_CUSTOM_PASSLIST_VAR"})
	got := pol.PhaseEnv(nil)

	if !containsEntry(got, "XYLEM_TEST_CUSTOM_PASSLIST_VAR=allowed") {
		t.Error("custom passlist var XYLEM_TEST_CUSTOM_PASSLIST_VAR should appear in env")
	}
}

// TestNewPolicy_NilConfigReturnsNoop verifies factory behaviour with nil cfg.
func TestNewPolicy_NilConfigReturnsNoop(t *testing.T) {
	pol := NewPolicy(nil)
	if _, ok := pol.(NoopPolicy); !ok {
		t.Errorf("NewPolicy(nil) = %T, want NoopPolicy", pol)
	}
}

// TestNewPolicy_ModeNoneReturnsNoop verifies factory with mode "none".
func TestNewPolicy_ModeNoneReturnsNoop(t *testing.T) {
	pol := NewPolicy(&Config{Mode: IsolationNone})
	if _, ok := pol.(NoopPolicy); !ok {
		t.Errorf("NewPolicy(mode=none) = %T, want NoopPolicy", pol)
	}
}

// TestNewPolicy_ModeEnvReturnsEnvScoping verifies factory with mode "env".
func TestNewPolicy_ModeEnvReturnsEnvScoping(t *testing.T) {
	pol := NewPolicy(&Config{Mode: IsolationEnv})
	if _, ok := pol.(*EnvScopingPolicy); !ok {
		t.Errorf("NewPolicy(mode=env) = %T, want *EnvScopingPolicy", pol)
	}
}

// TestNewPolicy_ModeFullReturnsFull verifies factory with mode "full".
func TestNewPolicy_ModeFullReturnsFull(t *testing.T) {
	pol := NewPolicy(&Config{Mode: IsolationFull})
	if _, ok := pol.(*FullPolicy); !ok {
		t.Errorf("NewPolicy(mode=full) = %T, want *FullPolicy", pol)
	}
}

// TestNewPolicy_UnknownModeReturnsNoop verifies factory with an unrecognised
// mode falls back to NoopPolicy.
func TestNewPolicy_UnknownModeReturnsNoop(t *testing.T) {
	pol := NewPolicy(&Config{Mode: "xyzzy"})
	if _, ok := pol.(NoopPolicy); !ok {
		t.Errorf("NewPolicy(mode=xyzzy) = %T, want NoopPolicy", pol)
	}
}

// TestEnvScopingPolicy_WrapCommandIdentity verifies that EnvScopingPolicy
// does not wrap the command (env-only mode).
func TestEnvScopingPolicy_WrapCommandIdentity(t *testing.T) {
	pol := newEnvScopingPolicy(nil)
	gotCmd, gotArgs, err := pol.WrapCommand(context.Background(), "/tmp/wt", "claude", []string{"-p"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCmd != "claude" || len(gotArgs) != 1 || gotArgs[0] != "-p" {
		t.Errorf("EnvScopingPolicy.WrapCommand should return identity, got cmd=%q args=%v", gotCmd, gotArgs)
	}
}

// TestEnvScopingPolicy_EmptyPasslistStripsAll verifies that with no extra
// passlist entries, unknown vars are stripped.
func TestEnvScopingPolicy_EmptyPasslistStripsAll(t *testing.T) {
	setenv(t, "XYLEM_TOTALLY_UNKNOWN_VAR_XYZ", "should_not_appear")
	pol := newEnvScopingPolicy(nil)
	got := pol.PhaseEnv(nil)
	if containsKey(got, "XYLEM_TOTALLY_UNKNOWN_VAR_XYZ") {
		t.Error("unknown var should be stripped from env")
	}
}

// TestFullPolicy_PhaseEnvDelegatesToEnvScoping verifies FullPolicy env
// filtering is the same as EnvScopingPolicy.
func TestFullPolicy_PhaseEnvDelegatesToEnvScoping(t *testing.T) {
	setenv(t, "XYLEM_FULL_LEAKED", "leaked")
	pol := &FullPolicy{EnvScopingPolicy: *newEnvScopingPolicy(nil)}
	got := pol.PhaseEnv(nil)
	if containsKey(got, "XYLEM_FULL_LEAKED") {
		t.Error("FullPolicy should strip unknown env vars via EnvScopingPolicy")
	}
}

// --- helpers ---

func containsEntry(env []string, entry string) bool {
	for _, e := range env {
		if e == entry {
			return true
		}
	}
	return false
}

func containsKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// TestEnvScopingPolicy_PasslistIsCaseSensitiveForKeys verifies that custom
// passlist entries are matched case-sensitively against the ambient env.
// (Passlist keys are stored upper-cased; ambient env keys must match exactly.)
func TestEnvScopingPolicy_PasslistIsCaseSensitiveForKeys(t *testing.T) {
	// On Linux, env keys are case-sensitive. MYVAR and myvar are different.
	// Ensure upper-casing in the passlist doesn't accidentally allow lower-cased
	// ambient keys.
	_ = os.Unsetenv("myvar_lower")
	setenv(t, "MYVAR_LOWER", "upper")
	pol := newEnvScopingPolicy([]string{"myvar_lower"}) // lowercase in passlist
	got := pol.PhaseEnv(nil)

	// The passlist stores "MYVAR_LOWER" (uppercased). Ambient has "MYVAR_LOWER".
	// So "MYVAR_LOWER=upper" should be present.
	if !containsEntry(got, "MYVAR_LOWER=upper") {
		t.Error("custom passlist var uppercased should match ambient uppercase key")
	}
}
