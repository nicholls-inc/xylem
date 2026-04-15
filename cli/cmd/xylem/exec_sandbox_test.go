package main

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/sandbox"
)

// TestRealCmdRunner_EnvScopingStripsAmbientSecret verifies that a secret in
// the ambient environment is not visible to the phase subprocess when the
// runner uses IsolationEnv.
func TestRealCmdRunner_EnvScopingStripsAmbientSecret(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec subprocess tests not supported on Windows")
	}
	t.Setenv("XYLEM_TEST_SECRET_STRIP", "leaked")

	pol := sandbox.NewPolicy(&sandbox.Config{Mode: sandbox.IsolationEnv})
	r := &realCmdRunner{isolation: pol}

	// Run a shell that prints the var (empty if unset).
	out, err := r.RunPhaseWithEnv(
		context.Background(),
		t.TempDir(),
		nil, // no provider env
		nil,
		"sh", "-c", `echo "val=${XYLEM_TEST_SECRET_STRIP}"`,
	)
	if err != nil {
		t.Fatalf("RunPhaseWithEnv: %v", err)
	}
	if strings.Contains(string(out), "val=leaked") {
		t.Errorf("secret XYLEM_TEST_SECRET_STRIP should not be visible to sandboxed subprocess")
	}
}

// TestRealCmdRunner_EnvScopingPreservesProviderCreds verifies that provider
// credentials are passed through to the subprocess despite env scoping.
func TestRealCmdRunner_EnvScopingPreservesProviderCreds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec subprocess tests not supported on Windows")
	}

	pol := sandbox.NewPolicy(&sandbox.Config{Mode: sandbox.IsolationEnv})
	r := &realCmdRunner{isolation: pol}

	providerEnv := []string{"XYLEM_TEST_PROVIDER_TOKEN=ok"}
	out, err := r.RunPhaseWithEnv(
		context.Background(),
		t.TempDir(),
		providerEnv,
		nil,
		"sh", "-c", `echo "token=${XYLEM_TEST_PROVIDER_TOKEN}"`,
	)
	if err != nil {
		t.Fatalf("RunPhaseWithEnv: %v", err)
	}
	if !strings.Contains(string(out), "token=ok") {
		t.Errorf("provider token should be visible to subprocess; got: %s", out)
	}
}

// TestRealCmdRunner_EnvScopingPreservesPath verifies that PATH is present in
// the subprocess environment even after env scoping.
func TestRealCmdRunner_EnvScopingPreservesPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec subprocess tests not supported on Windows")
	}

	pol := sandbox.NewPolicy(&sandbox.Config{Mode: sandbox.IsolationEnv})
	r := &realCmdRunner{isolation: pol}

	out, err := r.RunPhaseWithEnv(
		context.Background(),
		t.TempDir(),
		nil,
		nil,
		"sh", "-c", `echo "path=${PATH}"`,
	)
	if err != nil {
		t.Fatalf("RunPhaseWithEnv: %v", err)
	}
	if !strings.Contains(string(out), "path=/") {
		t.Errorf("PATH should be visible to sandboxed subprocess; got: %s", out)
	}
}

// TestRealCmdRunner_SandboxModeNoneInheritsAmbient verifies that with mode
// "none", ambient env vars are visible to the subprocess.
func TestRealCmdRunner_SandboxModeNoneInheritsAmbient(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec subprocess tests not supported on Windows")
	}
	t.Setenv("XYLEM_TEST_AMBIENT_NOOP", "yes")

	pol := sandbox.NewPolicy(&sandbox.Config{Mode: sandbox.IsolationNone})
	r := &realCmdRunner{isolation: pol}

	out, err := r.RunPhaseWithEnv(
		context.Background(),
		t.TempDir(),
		nil,
		nil,
		"sh", "-c", `echo "ambient=${XYLEM_TEST_AMBIENT_NOOP}"`,
	)
	if err != nil {
		t.Fatalf("RunPhaseWithEnv: %v", err)
	}
	if !strings.Contains(string(out), "ambient=yes") {
		t.Errorf("ambient var should be visible with mode=none; got: %s", out)
	}
}

// TestRealCmdRunner_SandboxModeEnvCustomPasslist verifies that a custom
// passlist entry makes it through to the subprocess.
func TestRealCmdRunner_SandboxModeEnvCustomPasslist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec subprocess tests not supported on Windows")
	}

	// Ensure the var is set before launching subprocess.
	if err := os.Setenv("XYLEM_TEST_CUSTOM_PASS", "ok"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() { os.Unsetenv("XYLEM_TEST_CUSTOM_PASS") })

	pol := sandbox.NewPolicy(&sandbox.Config{
		Mode:        sandbox.IsolationEnv,
		EnvPasslist: []string{"XYLEM_TEST_CUSTOM_PASS"},
	})
	r := &realCmdRunner{isolation: pol}

	out, err := r.RunPhaseWithEnv(
		context.Background(),
		t.TempDir(),
		nil,
		nil,
		"sh", "-c", `echo "custom=${XYLEM_TEST_CUSTOM_PASS}"`,
	)
	if err != nil {
		t.Fatalf("RunPhaseWithEnv: %v", err)
	}
	if !strings.Contains(string(out), "custom=ok") {
		t.Errorf("custom passlist var should be visible to subprocess; got: %s", out)
	}
}
