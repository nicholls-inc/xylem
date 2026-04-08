package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/containment"
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
	runtime executionRuntime
}

type executionRuntime interface {
	Apply(cmd *exec.Cmd, req containment.Request) error
}

type hostRuntime struct{}

// newCmdRunner creates a realCmdRunner for real subprocess execution.
func newCmdRunner(cfg *config.Config) *realCmdRunner {
	_ = cfg
	return &realCmdRunner{runtime: hostRuntime{}}
}

func (r *realCmdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (r *realCmdRunner) RunOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := r.commandWithRequest(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

func (r *realCmdRunner) RunProcess(ctx context.Context, dir string, name string, args ...string) error {
	cmd, err := r.commandWithRequest(ctx, name, args...)
	if err != nil {
		return err
	}
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (r *realCmdRunner) RunProcessWithEnv(ctx context.Context, dir string, extraEnv []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	if err := r.applyRequest(ctx, cmd); err != nil {
		return err
	}
	return cmd.Run()
}

func (r *realCmdRunner) RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd, err := r.commandWithRequest(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.Dir = dir
	cmd.Stdin = stdin

	var stdout bytes.Buffer
	stderr := newLimitedWriter(maxStderrBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	if err != nil && stderr.Len() > 0 {
		return stdout.Bytes(), fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return stdout.Bytes(), err
}

func (r *realCmdRunner) applyRequest(ctx context.Context, cmd *exec.Cmd) error {
	if r.runtime == nil {
		return nil
	}
	req, ok := containment.RequestFromContext(ctx)
	if !ok {
		return nil
	}
	if err := r.runtime.Apply(cmd, req); err != nil {
		return fmt.Errorf("apply runtime containment: %w", err)
	}
	return nil
}

func (r *realCmdRunner) commandWithRequest(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if err := r.applyRequest(ctx, cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (hostRuntime) Apply(cmd *exec.Cmd, req containment.Request) error {
	if req.Isolation == containment.IsolationOff && req.Network == containment.NetworkInherit && len(req.Env) == 0 {
		return nil
	}

	baseEnv := os.Environ()
	if len(cmd.Env) > 0 {
		baseEnv = cmd.Env
	}
	env, err := buildContainedEnv(baseEnv, req)
	if err != nil {
		return err
	}
	cmd.Env = env
	return nil
}

func buildContainedEnv(base []string, req containment.Request) ([]string, error) {
	env := filterRuntimeBaseEnv(base)
	if req.Isolation == containment.IsolationWorkspace {
		homeDir := filepath.Join(req.RuntimeDir, "home")
		tmpDir := filepath.Join(req.RuntimeDir, "tmp")
		cacheDir := filepath.Join(req.RuntimeDir, "xdg", "cache")
		configDir := filepath.Join(req.RuntimeDir, "xdg", "config")
		dataDir := filepath.Join(req.RuntimeDir, "xdg", "data")
		for _, dir := range []string{homeDir, tmpDir, cacheDir, configDir, dataDir} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create runtime dir %q: %w", dir, err)
			}
		}
		env = upsertEnv(env, "HOME", homeDir)
		env = upsertEnv(env, "TMPDIR", tmpDir)
		env = upsertEnv(env, "XDG_CACHE_HOME", cacheDir)
		env = upsertEnv(env, "XDG_CONFIG_HOME", configDir)
		env = upsertEnv(env, "XDG_DATA_HOME", dataDir)
		env = upsertEnv(env, "GIT_CONFIG_GLOBAL", os.DevNull)
		env = upsertEnv(env, "GIT_CONFIG_NOSYSTEM", "1")
	}

	switch req.Network {
	case containment.NetworkDeny:
		env = applyDeniedNetworkEnv(env)
	case containment.NetworkInherit:
		// Preserve proxy-related env from the filtered base set.
	}

	for _, entry := range req.Env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		env = upsertEnv(env, name, value)
	}
	env = upsertEnv(env, "XYLEM_RUNTIME_NETWORK", string(req.Network))
	env = upsertEnv(env, "XYLEM_RUNTIME_ISOLATION", string(req.Isolation))
	return env, nil
}

func filterRuntimeBaseEnv(base []string) []string {
	allowed := map[string]struct{}{
		"PATH": {}, "LANG": {}, "LC_ALL": {}, "LC_CTYPE": {}, "TERM": {}, "COLORTERM": {},
		"NO_COLOR": {}, "CI": {}, "TZ": {}, "SSL_CERT_FILE": {}, "SSL_CERT_DIR": {},
		"http_proxy": {}, "https_proxy": {}, "all_proxy": {}, "no_proxy": {},
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {}, "NO_PROXY": {},
	}
	var filtered []string
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, ok := allowed[name]; ok {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	replaced := false
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			replaced = true
		}
	}
	if !replaced {
		env = append(env, prefix+value)
	}
	return env
}

func applyDeniedNetworkEnv(env []string) []string {
	blockedHTTP := "http://127.0.0.1:9"
	blockedSOCKS := "socks5://127.0.0.1:9"
	env = upsertEnv(env, "http_proxy", blockedHTTP)
	env = upsertEnv(env, "https_proxy", blockedHTTP)
	env = upsertEnv(env, "all_proxy", blockedSOCKS)
	env = upsertEnv(env, "HTTP_PROXY", blockedHTTP)
	env = upsertEnv(env, "HTTPS_PROXY", blockedHTTP)
	env = upsertEnv(env, "ALL_PROXY", blockedSOCKS)
	env = upsertEnv(env, "NO_PROXY", "127.0.0.1,localhost,::1")
	env = upsertEnv(env, "no_proxy", "127.0.0.1,localhost,::1")
	return env
}
