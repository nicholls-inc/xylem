package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	xrunner "github.com/nicholls-inc/xylem/cli/internal/runner"
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
	// environment. Populated from claude.env and copilot.env in config.
	extraEnv []string
	mu       sync.Mutex
	tracked  map[string]*trackedProcess
}

type trackedProcess struct {
	cmd       *exec.Cmd
	phase     string
	startedAt time.Time
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
	var env []string
	addEnv := func(k, v string) {
		expanded := os.ExpandEnv(v)
		if expanded == "" {
			return
		}
		env = append(env, k+"="+expanded)
	}
	for k, v := range cfg.Claude.Env {
		addEnv(k, v)
	}
	for k, v := range cfg.Copilot.Env {
		addEnv(k, v)
	}
	return &realCmdRunner{extraEnv: env, tracked: make(map[string]*trackedProcess)}
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
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	err := r.runTrackedCommand(ctx, cmd)
	return stdout.Bytes(), err
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
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Env = r.cmdEnv()

	var stdout bytes.Buffer
	stderr := newLimitedWriter(maxStderrBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	err := r.runTrackedCommand(ctx, cmd)
	if err != nil && stderr.Len() > 0 {
		return stdout.Bytes(), fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return stdout.Bytes(), err
}

func (r *realCmdRunner) ProcessInfo(vesselID string) (xrunner.ProcessInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	proc, ok := r.tracked[vesselID]
	if !ok || proc.cmd == nil || proc.cmd.Process == nil {
		return xrunner.ProcessInfo{}, false
	}
	return xrunner.ProcessInfo{
		PID:       proc.cmd.Process.Pid,
		Phase:     proc.phase,
		StartedAt: proc.startedAt,
		Live:      true,
	}, true
}

func (r *realCmdRunner) TerminateProcess(vesselID string, gracePeriod time.Duration) error {
	info, ok := r.ProcessInfo(vesselID)
	if !ok {
		return fmt.Errorf("terminate process for vessel %s: not tracked", vesselID)
	}

	r.mu.Lock()
	proc := r.tracked[vesselID]
	r.mu.Unlock()
	if proc == nil || proc.cmd == nil || proc.cmd.Process == nil {
		return fmt.Errorf("terminate process for vessel %s: no process", vesselID)
	}

	if err := proc.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("sigterm pid %d: %w", info.PID, err)
	}
	if r.waitForExit(vesselID, gracePeriod) {
		return nil
	}
	if err := proc.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("sigkill pid %d: %w", info.PID, err)
	}
	r.waitForExit(vesselID, time.Second)
	return nil
}

func (r *realCmdRunner) waitForExit(vesselID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, ok := r.ProcessInfo(vesselID); !ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (r *realCmdRunner) runTrackedCommand(ctx context.Context, cmd *exec.Cmd) error {
	meta, ok := xrunner.PhaseExecutionMetadataFromContext(ctx)
	if !ok || meta.VesselID == "" {
		return cmd.Run()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.trackProcess(meta, cmd)
	defer r.untrackProcess(meta.VesselID)
	return cmd.Wait()
}

func (r *realCmdRunner) trackProcess(meta xrunner.PhaseExecutionMetadata, cmd *exec.Cmd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tracked == nil {
		r.tracked = make(map[string]*trackedProcess)
	}
	r.tracked[meta.VesselID] = &trackedProcess{
		cmd:       cmd,
		phase:     meta.PhaseName,
		startedAt: time.Now().UTC(),
	}
}

func (r *realCmdRunner) untrackProcess(vesselID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tracked, vesselID)
}
