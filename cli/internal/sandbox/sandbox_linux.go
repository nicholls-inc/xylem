//go:build linux

package sandbox

import (
	"context"
	"fmt"
)

// platformWrapCommand wraps cmd with unshare --net, placing the phase
// subprocess in a new network namespace with no external connectivity.
//
// Fine-grained egress allowlists (egressAllow) are not supported in this
// mode because user-space allowlisting requires iptables/nftables setup
// that is outside the scope of this package. Operators who need per-host
// egress rules should configure iptables rules externally and leave
// egressAllow empty.
func platformWrapCommand(_ context.Context, _, cmd string, args []string, egressAllow []string) (string, []string, error) {
	if len(egressAllow) > 0 {
		return "", nil, fmt.Errorf(
			"sandbox: egress_allow is not supported in Linux unshare mode; " +
				"configure iptables/nftables rules externally or leave egress_allow empty",
		)
	}

	// unshare --net <cmd> <args...>
	wrappedArgs := make([]string, 0, 2+len(args))
	wrappedArgs = append(wrappedArgs, "--net", cmd)
	wrappedArgs = append(wrappedArgs, args...)

	return "unshare", wrappedArgs, nil
}
