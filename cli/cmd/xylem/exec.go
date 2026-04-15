package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/sandbox"
)

// maxStderrBytes is the maximum amount of stderr captured from a phase subprocess.
// Anything beyond this limit is silently discarded to prevent unbounded memory growth.
const maxStderrBytes = 256 * 1024 // 256 KiB

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// limitedWriter captures up to max bytes; additional writes are silently discarded.
type limitedWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newLimitedWriter(max int) *limitedWriter {
	return &limitedWriter{max: max}
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	remaining := lw.max - lw.buf.Len()
	if remaining <= 0 {
		lw.truncated = true
		return len(p), nil // discard silently so the subprocess doesn't block
	}
	if len(p) > remaining {
		lw.buf.Write(p[:remaining])
		lw.truncated = true
		return len(p), nil
	}
	lw.buf.Write(p)
	return len(p), nil
}

func (lw *limitedWriter) String() string {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	if lw.truncated {
		return lw.buf.String() + "\n... [stderr truncated]"
	}
	return lw.buf.String()
}

func (lw *limitedWriter) Len() int {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.buf.Len()
}

type realCmdRunner struct {
	// extraEnv holds additional KEY=VALUE pairs merged into the subprocess
	// environment for non-phase subprocesses. Populated from all provider env
	// blocks in config.
	extraEnv []string
	// providerEnv holds provider-scoped env vars for phase subprocesses so each
	// LLM only receives its own credentials.
	providerEnv map[string][]string
	// isolation is the sandbox policy applied to phase subprocesses.
	// Nil is treated as NoopPolicy (no isolation).
	isolation sandbox.Policy
}

// newCmdRunner creates a realCmdRunner with extra env vars merged from
// claude.env and copilot.env config sections.
//
// When an env value contains an unset shell variable reference (e.g.
// "${COPILOT_GITHUB_TOKEN}" when COPILOT_GITHUB_TOKEN is not exported),
// os.ExpandEnv returns an empty string. Propagating an empty value into
// the subprocess environment is actively harmful because it *unsets*
// (shadows) any matching variable the subprocess might otherwise inherit
// from the daemon's own environment. For example, if the daemon has
// GITHUB_TOKEN set directly but .xylem.yml declares
//
//	copilot.env.GITHUB_TOKEN: "${COPILOT_GITHUB_TOKEN}"
//
// then a naive expansion would emit "GITHUB_TOKEN=" and the gh/copilot
// subprocess would see no token at all — producing the "No
// authentication information found" cascade observed on 2026-04-09.
//
// Skip any empty-value expansions so the subprocess falls back to the
// daemon's own env. Operators who intentionally want to unset a var can
// remove it from .xylem.yml rather than relying on implicit blanking.
func newCmdRunner(cfg *config.Config) *realCmdRunner {
	if cfg == nil {
		return &realCmdRunner{}
	}
	addEnv := func(dst *[]string, k, v string) {
		expanded := os.ExpandEnv(v)
		if expanded == "" {
			return
		}
		*dst = append(*dst, k+"="+expanded)
	}

	providerEnv := make(map[string][]string, len(cfg.Providers))
	var env []string
	for name, provider := range cfg.Providers {
		keys := make([]string, 0, len(provider.Env))
		for k := range provider.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := provider.Env[k]
			addEnv(&env, k, v)
			providerSlice := providerEnv[name]
			addEnv(&providerSlice, k, v)
			providerEnv[name] = providerSlice
		}
	}
	if len(providerEnv) == 0 {
		keys := make([]string, 0, len(cfg.Claude.Env))
		for k := range cfg.Claude.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			addEnv(&env, k, cfg.Claude.Env[k])
		}
		keys = keys[:0]
		for k := range cfg.Copilot.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			addEnv(&env, k, cfg.Copilot.Env[k])
		}
	}
	return &realCmdRunner{extraEnv: env, providerEnv: providerEnv}
}

// newCmdRunnerWithPolicy is like newCmdRunner but accepts a pre-built sandbox
// Policy. Used by the drain path to inject execution isolation for phase
// subprocesses.
func newCmdRunnerWithPolicy(cfg *config.Config, p sandbox.Policy) *realCmdRunner {
	r := newCmdRunner(cfg)
	r.isolation = p
	return r
}

// policy returns the effective sandbox Policy, defaulting to NoopPolicy when
// no isolation has been configured.
func (r *realCmdRunner) policy() sandbox.Policy {
	if r.isolation != nil {
		return r.isolation
	}
	return sandbox.NoopPolicy{}
}

// cmdEnv returns the environment to use for a subprocess: the daemon's
// own env with extraEnv appended (extraEnv values take precedence
// because Go's exec package uses the last occurrence of a given key).
//
// Always returning a non-nil slice means cmd.Env is set explicitly,
// which makes the subprocess environment deterministic regardless of
// whether extraEnv is empty.
func (r *realCmdRunner) cmdEnv() []string {
	base := os.Environ()
	if len(r.extraEnv) == 0 {
		return base
	}
	return append(base, r.extraEnv...)
}

func (r *realCmdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = r.cmdEnv()
	return cmd.CombinedOutput()
}

func (r *realCmdRunner) RunOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = r.cmdEnv()
	return cmd.CombinedOutput()
}

func (r *realCmdRunner) RunProcess(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = r.cmdEnv()
	return cmd.Run()
}

func (r *realCmdRunner) RunProcessWithEnv(ctx context.Context, dir string, extraEnv []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Merge the caller-supplied extraEnv on top of the runner's own
	// configured env so caller overrides win.
	base := r.cmdEnv()
	cmd.Env = append(base, extraEnv...)
	return cmd.Run()
}

func (r *realCmdRunner) RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.runPhaseInternal(ctx, dir, r.cmdEnv(), stdin, nil, name, args...)
}

func (r *realCmdRunner) RunPhaseWithEnv(ctx context.Context, dir string, extraEnv []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	pol := r.policy()
	env := pol.PhaseEnv(extraEnv)
	wrappedCmd, wrappedArgs, err := pol.WrapCommand(ctx, dir, name, args)
	if err != nil {
		return nil, fmt.Errorf("sandbox wrap: %w", err)
	}
	return r.runPhaseInternal(ctx, dir, env, stdin, nil, wrappedCmd, wrappedArgs...)
}

func (r *realCmdRunner) RunPhaseObserved(ctx context.Context, dir string, stdin io.Reader, observer runner.PhaseProcessObserver, name string, args ...string) ([]byte, error) {
	return r.runPhaseInternal(ctx, dir, r.cmdEnv(), stdin, observer, name, args...)
}

func (r *realCmdRunner) RunPhaseObservedWithEnv(ctx context.Context, dir string, extraEnv []string, stdin io.Reader, observer runner.PhaseProcessObserver, name string, args ...string) ([]byte, error) {
	pol := r.policy()
	env := pol.PhaseEnv(extraEnv)
	wrappedCmd, wrappedArgs, err := pol.WrapCommand(ctx, dir, name, args)
	if err != nil {
		return nil, fmt.Errorf("sandbox wrap: %w", err)
	}
	return r.runPhaseInternal(ctx, dir, env, stdin, observer, wrappedCmd, wrappedArgs...)
}

func (r *realCmdRunner) runPhaseInternal(ctx context.Context, dir string, env []string, stdin io.Reader, observer runner.PhaseProcessObserver, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Env = env

	var stdout bytes.Buffer
	stderr := newLimitedWriter(maxStderrBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if observer != nil && cmd.Process != nil {
		observer.ProcessStarted(cmd.Process.Pid)
		defer observer.ProcessExited(cmd.Process.Pid)
	}
	err := cmd.Wait()
	if err != nil && stderr.Len() > 0 {
		return stdout.Bytes(), fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return stdout.Bytes(), err
}
