package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/containment"
)

func envValue(env []string, key string) string {
	tok := key + "="
	for _, entry := range env {
		if len(entry) > len(tok) && entry[:len(tok)] == tok {
			return entry[len(tok):]
		}
	}
	return ""
}

func TestBuildContainedEnvWorkspaceIsolation(t *testing.T) {
	runtimeDir := t.TempDir()

	env, err := buildContainedEnv([]string{
		"PATH=/usr/bin",
		"HOME=/Users/example",
		"SECRET=ambient",
	}, containment.Request{
		Isolation:  containment.IsolationWorkspace,
		Network:    containment.NetworkDeny,
		RuntimeDir: runtimeDir,
		Env:        []string{"TOKEN=scoped"},
	})
	if err != nil {
		t.Fatalf("buildContainedEnv() error = %v", err)
	}

	if got := envValue(env, "HOME"); got != filepath.Join(runtimeDir, "home") {
		t.Fatalf("HOME = %q, want %q", got, filepath.Join(runtimeDir, "home"))
	}
	if got := envValue(env, "TMPDIR"); got != filepath.Join(runtimeDir, "tmp") {
		t.Fatalf("TMPDIR = %q, want %q", got, filepath.Join(runtimeDir, "tmp"))
	}
	if got := envValue(env, "TOKEN"); got != "scoped" {
		t.Fatalf("TOKEN = %q, want scoped", got)
	}
	if got := envValue(env, "SECRET"); got != "" {
		t.Fatalf("SECRET = %q, want empty", got)
	}
	if got := envValue(env, "HTTP_PROXY"); got != "http://127.0.0.1:9" {
		t.Fatalf("HTTP_PROXY = %q, want deny proxy", got)
	}
	if got := envValue(env, "GIT_CONFIG_NOSYSTEM"); got != "1" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM = %q, want 1", got)
	}
}

func TestBuildContainedEnvInheritPreservesProxy(t *testing.T) {
	env, err := buildContainedEnv([]string{
		"PATH=/usr/bin",
		"HTTP_PROXY=http://proxy.internal:8080",
	}, containment.Request{
		Isolation: containment.IsolationOff,
		Network:   containment.NetworkInherit,
	})
	if err != nil {
		t.Fatalf("buildContainedEnv() error = %v", err)
	}

	if got := envValue(env, "HTTP_PROXY"); got != "http://proxy.internal:8080" {
		t.Fatalf("HTTP_PROXY = %q, want inherited proxy", got)
	}
}

func TestBuildContainedEnvOffPreservesAmbientEnvWithScopedSecrets(t *testing.T) {
	env, err := buildContainedEnv([]string{
		"PATH=/usr/bin",
		"HOME=/Users/example",
		"SECRET=ambient",
		"CUSTOM=ambient",
	}, containment.Request{
		Isolation: containment.IsolationOff,
		Network:   containment.NetworkDeny,
		Env:       []string{"TOKEN=scoped", "SECRET=scoped"},
	})
	if err != nil {
		t.Fatalf("buildContainedEnv() error = %v", err)
	}

	if got := envValue(env, "CUSTOM"); got != "ambient" {
		t.Fatalf("CUSTOM = %q, want ambient", got)
	}
	if got := envValue(env, "HOME"); got != "/Users/example" {
		t.Fatalf("HOME = %q, want inherited home", got)
	}
	if got := envValue(env, "TOKEN"); got != "scoped" {
		t.Fatalf("TOKEN = %q, want scoped", got)
	}
	if got := envValue(env, "SECRET"); got != "scoped" {
		t.Fatalf("SECRET = %q, want scoped", got)
	}
	if got := envValue(env, "HTTP_PROXY"); got != "http://127.0.0.1:9" {
		t.Fatalf("HTTP_PROXY = %q, want deny proxy", got)
	}
}

func TestCommandWithRequestAppliesContainment(t *testing.T) {
	runtimeDir := t.TempDir()
	runner := &realCmdRunner{runtime: hostRuntime{}}
	ctx := containment.WithRequest(context.Background(), containment.Request{
		Isolation:  containment.IsolationWorkspace,
		Network:    containment.NetworkDeny,
		RuntimeDir: runtimeDir,
		Env:        []string{"TOKEN=scoped"},
	})

	cmd, err := runner.commandWithRequest(ctx, "env")
	if err != nil {
		t.Fatalf("commandWithRequest() error = %v", err)
	}

	if got := envValue(cmd.Env, "TOKEN"); got != "scoped" {
		t.Fatalf("cmd env TOKEN = %q, want scoped", got)
	}
	if got := envValue(cmd.Env, "HTTP_PROXY"); got != "http://127.0.0.1:9" {
		t.Fatalf("cmd env HTTP_PROXY = %q, want deny proxy", got)
	}
	if got := envValue(cmd.Env, "HOME"); got != filepath.Join(runtimeDir, "home") {
		t.Fatalf("cmd env HOME = %q, want %q", got, filepath.Join(runtimeDir, "home"))
	}
}
