//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// platformWrapCommand wraps cmd with sandbox-exec using a deny-network profile.
//
// The generated profile:
//   - Allows all file reads.
//   - Allows file writes only within dir (the worktree path).
//   - Allows outbound TCP only to hosts listed in egressAllow, plus loopback.
//   - Denies everything else by default.
//
// The profile is written to a temp file and passed to sandbox-exec via -f.
// sandbox-exec reads it synchronously before exec, so cleanup is safe
// immediately after the subprocess starts. The -D WORKTREE=<dir> flag passes
// the worktree path as a profile parameter.
func platformWrapCommand(_ context.Context, dir, cmd string, args []string, egressAllow []string) (string, []string, error) {
	profile := darwinSandboxProfile(egressAllow)

	f, err := os.CreateTemp("", "xylem-sandbox-*.sb")
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: create profile temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(profile); err != nil {
		return "", nil, fmt.Errorf("sandbox: write sandbox profile: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", nil, fmt.Errorf("sandbox: close sandbox profile: %w", err)
	}

	// sandbox-exec -f <profile> -D WORKTREE=<dir> <cmd> <args...>
	wrappedArgs := make([]string, 0, 5+len(args))
	wrappedArgs = append(wrappedArgs, "-f", f.Name(), "-D", "WORKTREE="+dir, cmd)
	wrappedArgs = append(wrappedArgs, args...)

	return "sandbox-exec", wrappedArgs, nil
}

// darwinSandboxProfile generates a sandbox-exec .sb profile in Scheme notation.
// The WORKTREE parameter is substituted at invocation time via -D.
func darwinSandboxProfile(egressAllow []string) string {
	var sb strings.Builder
	sb.WriteString(`(version 1)
(deny default)
(allow process-exec process-fork)
(allow file-read*)
(allow file-write* (subpath (param "WORKTREE")))
(allow network-outbound (local tcp))
(allow network-inbound (local tcp))
`)
	for _, host := range egressAllow {
		// sandbox-exec remote syntax: (remote tcp "host:port")
		// We allow any port on the given host by omitting port.
		fmt.Fprintf(&sb, "(allow network-outbound (remote tcp \"%s\"))\n", host)
	}
	return sb.String()
}
