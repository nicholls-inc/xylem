# Sandbox — Runtime Containment for Vessel Phases

xylem can optionally isolate vessel phase subprocesses (Claude sessions) to
limit the blast radius of a misbehaving or compromised phase. This is
**Workstream 2 (WS2)** of the harness plan.

## Isolation modes

Configure the isolation level via the `sandbox:` block in `.xylem.yml`:

```yaml
sandbox:
  mode: env          # "none" | "env" | "full"
  egress_allow:      # only used when mode: full (macOS only)
    - api.anthropic.com
    - api.github.com
  env_passlist:      # extra KEY names to include (mode: env or full)
    - MY_COMPANY_TOKEN
```

### `mode: none` (default)

No isolation. The phase subprocess inherits the daemon's full ambient
environment and has unrestricted network access. This is the backward-
compatible default.

Use this when: you are not concerned about ambient secret leakage, or when
your platform does not support `env` or `full` mode.

### `mode: env`

The phase subprocess environment is filtered to a **safe passlist** of
well-known keys (see below) plus any provider credentials for the current
phase. All other ambient env vars — including API keys, tokens, and sensitive
internal vars — are stripped before `exec`.

No OS privileges are required. This mode works on all platforms.

**Trust boundary:** The subprocess still has unrestricted network access.
`env` mode protects secrets from being read by the subprocess via its
environment, but does not prevent the subprocess from exfiltrating data over
the network.

### `mode: full`

Applies `env` mode plus platform-level network namespace enforcement:

- **macOS**: Uses `sandbox-exec` with a deny-network profile. Only loopback
  and hosts listed in `egress_allow` are reachable.
- **Linux**: Uses `unshare --net` to place the subprocess in a new network
  namespace with no external connectivity. Fine-grained `egress_allow` is
  not supported on Linux; use iptables/nftables rules externally instead.
- **Other platforms**: Returns an error at phase launch. Use `mode: env` or
  `mode: none`.

**Platform requirements:**

| Platform | Tool needed |
|----------|-------------|
| macOS    | `sandbox-exec` (ships with macOS; no install needed) |
| Linux    | `unshare` (part of `util-linux`; may need user namespaces enabled: `sysctl kernel.unprivileged_userns_clone=1`) |

## Built-in safe passlist (`mode: env` and `mode: full`)

The following environment variable names are always included when env scoping
is active:

| Category | Variables |
|----------|-----------|
| Shell identity | `HOME`, `USER`, `LOGNAME`, `SHELL`, `TERM` |
| Temp dirs | `TMPDIR`, `TEMP`, `TMP` |
| Filesystem | `PATH`, `PWD` |
| Locale | `LANG`, `LC_ALL`, `LC_CTYPE` |
| Git identity | `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL`, `GIT_SSH_COMMAND` |
| Go toolchain | `GOPATH`, `GOROOT`, `GOPROXY`, `GONOSUMDB`, `GOFLAGS`, `CGO_ENABLED`, `GOOS`, `GOARCH` |
| TLS roots | `SSL_CERT_FILE`, `SSL_CERT_DIR`, `CURL_CA_BUNDLE` |
| HTTP proxy | `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` (and lowercase variants) |
| XDG base dirs | `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`, `XDG_RUNTIME_DIR` |
| macOS dylib | `DYLD_LIBRARY_PATH`, `DYLD_FALLBACK_LIBRARY_PATH` |

Provider credentials (e.g. `ANTHROPIC_API_KEY`) are always injected for the
phase's selected provider, regardless of the passlist.

## Custom passlist entries

To allow additional vars through the filter:

```yaml
sandbox:
  mode: env
  env_passlist:
    - MY_COMPANY_TOKEN
    - INTERNAL_API_URL
```

Keys are case-insensitive in the passlist configuration but matched
case-sensitively against the ambient environment on case-sensitive
filesystems (Linux). Use the exact casing of the ambient var.

## Operator escape hatch

Set `sandbox.mode: none` (or omit the `sandbox:` block entirely) to revert
to pre-WS2 behaviour. No restart is required — the policy is constructed at
drain time.

## Known limitations

1. **Linux egress allowlists**: `unshare --net` provides a flat deny-all
   network namespace. Per-host allowlists require iptables/nftables rules
   configured externally (e.g. via a systemd unit that sets up the namespace
   before xylem starts, or a network policy on a container runtime).

2. **macOS sandbox-exec profile**: The profile is written to a temp file for
   each phase invocation. The temp file is not cleaned up after exec (the OS
   reclaims it when the process exits). A future PR can cache the profile in
   the state dir.

3. **IsolationFull on unsupported platforms**: Returns an error at phase
   launch (not at startup), so misconfiguration is surfaced when the first
   phase runs, not at daemon start.

4. **Command phases are not sandboxed**: Only LLM phase calls
   (`RunPhaseWithEnv`, `RunPhaseObservedWithEnv`) go through the sandbox
   policy. Command phases (`RunPhase`, `RunProcess`, `RunProcessWithEnv`)
   retain the full ambient environment because git and gh require ambient
   credentials that must not be filtered.
