package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
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

type realCmdRunner struct{}

func (r *realCmdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (r *realCmdRunner) RunOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (r *realCmdRunner) RunProcess(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (r *realCmdRunner) RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin

	var stdout bytes.Buffer
	stderr := newLimitedWriter(maxStderrBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err != nil && stderr.Len() > 0 {
		return stdout.Bytes(), fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return stdout.Bytes(), err
}
