// Package sandbox provides execution-isolation abstractions for vessel phase
// subprocesses. It implements WS2 of the harness plan:
// docs/plans/sota-harness-plan.md §WS2.
//
// Three isolation modes are available:
//
//   - "none" (default): No-op — current behaviour. The phase subprocess
//     inherits the full ambient environment. Backward-compatible.
//
//   - "env": Environment scoping only. The subprocess receives a filtered
//     environment containing only entries from the built-in safe passlist
//     plus any operator-configured extras, with provider credentials appended
//     last. No OS privileges are required.
//
//   - "full": Environment scoping plus platform-level egress enforcement.
//     On macOS this uses sandbox-exec with a deny-network profile. On Linux
//     this uses unshare --net. See docs/sandbox.md for platform requirements
//     and operator escape hatches.
//
// Operators configure sandbox behaviour via the sandbox: block in .xylem.yml.
// See [Config] for available fields.
package sandbox

import (
	"context"
	"os"
	"strings"
)

// IsolationMode controls execution-time containment for vessel phase
// subprocesses.
type IsolationMode string

const (
	// IsolationNone is the default: no-op, current behaviour.
	IsolationNone IsolationMode = "none"
	// IsolationEnv strips the subprocess environment to a safe passlist plus
	// provider credentials. No OS privileges required.
	IsolationEnv IsolationMode = "env"
	// IsolationFull applies environment scoping plus platform-level egress
	// enforcement. Requires sandbox-exec (macOS) or unshare (Linux).
	IsolationFull IsolationMode = "full"
)

// Config holds operator-configured sandbox parameters, loaded from the
// sandbox: block in .xylem.yml.
type Config struct {
	// Mode selects the isolation level: "none", "env", or "full".
	// Unrecognised values fall back to "none".
	Mode IsolationMode `yaml:"mode,omitempty"`
	// EgressAllow lists hostnames or CIDRs that outbound connections may
	// reach when Mode is "full". Empty means deny-all (loopback only).
	// Linux mode does not support fine-grained allowlists; leave this empty
	// and configure iptables/nftables rules externally.
	EgressAllow []string `yaml:"egress_allow,omitempty"`
	// EnvPasslist lists additional KEY names (not values) to include in the
	// filtered environment when Mode is "env" or "full". Entries here
	// supplement the built-in safe passlist — they do not replace it.
	EnvPasslist []string `yaml:"env_passlist,omitempty"`
}

// builtinPasslist is the set of environment variable names always included
// by EnvScopingPolicy and FullPolicy. Keys are upper-cased for comparison.
//
// Covers: shell identity, filesystem helpers, locale, git identity, Go
// toolchain, TLS roots, proxy settings, XDG dirs, and macOS dylib helpers.
// Anything not in this list (or EnvPasslist) is stripped from phase envs.
var builtinPasslist = []string{
	// Shell identity
	"HOME", "USER", "LOGNAME", "SHELL", "TERM",
	// Temp dirs
	"TMPDIR", "TEMP", "TMP",
	// Filesystem
	"PATH", "PWD",
	// Locale
	"LANG", "LC_ALL", "LC_CTYPE",
	// Git identity (needed by git commit inside a phase)
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
	"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	"GIT_SSH_COMMAND",
	// Go toolchain (needed if a phase runs go build/test)
	"GOPATH", "GOROOT", "GOPROXY", "GONOSUMDB", "GOFLAGS",
	"CGO_ENABLED", "GOOS", "GOARCH",
	// TLS roots
	"SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_CA_BUNDLE",
	// HTTP proxy (lowercase and uppercase variants)
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
	// XDG base dirs (used by many tools for config/cache lookup)
	"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR",
	// macOS dylib resolution (stripped on Linux, harmless to include)
	"DYLD_LIBRARY_PATH", "DYLD_FALLBACK_LIBRARY_PATH",
}

// Policy abstracts execution environment preparation for phase subprocesses.
// The runner calls PhaseEnv to build the subprocess env and WrapCommand to
// optionally wrap the command with a containment tool before exec.
type Policy interface {
	// PhaseEnv returns the env slice for the phase subprocess.
	// providerEnv contains KEY=VALUE pairs for the selected provider only
	// (e.g. ANTHROPIC_API_KEY=…). These are always included in the output,
	// appended last so they take precedence over any passlist collision.
	PhaseEnv(providerEnv []string) []string

	// WrapCommand optionally wraps cmd/args with a containment tool.
	// dir is the worktree path; containment tools may need it to set
	// write-allow rules. Returns the (possibly wrapped) cmd and args.
	// Returns an error if the platform does not support the requested mode.
	WrapCommand(ctx context.Context, dir, cmd string, args []string) (string, []string, error)
}

// NewPolicy constructs the appropriate Policy from cfg.
// Returns NoopPolicy when cfg is nil, or Mode is "none" or unrecognised.
func NewPolicy(cfg *Config) Policy {
	if cfg == nil {
		return NoopPolicy{}
	}
	switch cfg.Mode {
	case IsolationEnv:
		return newEnvScopingPolicy(cfg.EnvPasslist)
	case IsolationFull:
		return &FullPolicy{
			EnvScopingPolicy: *newEnvScopingPolicy(cfg.EnvPasslist),
			egressAllow:      cfg.EgressAllow,
		}
	default:
		// "none", empty string, or any unrecognised value → no-op
		return NoopPolicy{}
	}
}

// NoopPolicy is the default policy: no changes to the subprocess environment
// or command. Preserves current (pre-WS2) behaviour exactly.
type NoopPolicy struct{}

// PhaseEnv returns os.Environ() with providerEnv appended, matching the
// behaviour of the runner before sandbox support was added.
func (NoopPolicy) PhaseEnv(providerEnv []string) []string {
	base := os.Environ()
	if len(providerEnv) == 0 {
		return base
	}
	return append(base, providerEnv...)
}

// WrapCommand returns cmd and args unchanged.
func (NoopPolicy) WrapCommand(_ context.Context, _, cmd string, args []string) (string, []string, error) {
	return cmd, args, nil
}

// EnvScopingPolicy strips the ambient environment to the built-in passlist
// (plus any operator-configured extras) and appends provider credentials.
type EnvScopingPolicy struct {
	// passlist is the complete set of allowed KEY names (union of builtinPasslist
	// and any operator-configured EnvPasslist entries).
	passlist map[string]struct{}
}

func newEnvScopingPolicy(extra []string) *EnvScopingPolicy {
	pl := make(map[string]struct{}, len(builtinPasslist)+len(extra))
	for _, k := range builtinPasslist {
		pl[k] = struct{}{}
	}
	for _, k := range extra {
		pl[strings.ToUpper(k)] = struct{}{}
	}
	return &EnvScopingPolicy{passlist: pl}
}

// PhaseEnv returns only the ambient env entries whose key appears in the
// passlist, followed by providerEnv. providerEnv entries are always included
// regardless of the passlist.
func (p *EnvScopingPolicy) PhaseEnv(providerEnv []string) []string {
	ambient := os.Environ()
	out := make([]string, 0, len(p.passlist)+len(providerEnv))
	for _, entry := range ambient {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, ok := p.passlist[key]; ok {
			out = append(out, entry)
		}
	}
	// Provider env appended last: later entries take precedence in exec
	// semantics (last KEY= wins), so provider creds override any passlist
	// collision.
	out = append(out, providerEnv...)
	return out
}

// WrapCommand returns cmd and args unchanged (no OS-level wrapping for env
// mode; containment is env-only).
func (p *EnvScopingPolicy) WrapCommand(_ context.Context, _, cmd string, args []string) (string, []string, error) {
	return cmd, args, nil
}

// FullPolicy applies environment scoping plus platform-level egress
// enforcement. The OS-specific command wrapping is implemented in
// sandbox_darwin.go and sandbox_linux.go.
type FullPolicy struct {
	EnvScopingPolicy
	egressAllow []string
}

// PhaseEnv delegates to EnvScopingPolicy.PhaseEnv.
func (p *FullPolicy) PhaseEnv(providerEnv []string) []string {
	return p.EnvScopingPolicy.PhaseEnv(providerEnv)
}

// WrapCommand wraps cmd with the platform-specific containment tool.
func (p *FullPolicy) WrapCommand(ctx context.Context, dir, cmd string, args []string) (string, []string, error) {
	return platformWrapCommand(ctx, dir, cmd, args, p.egressAllow)
}
