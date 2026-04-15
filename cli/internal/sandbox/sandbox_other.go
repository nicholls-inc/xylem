//go:build !darwin && !linux

package sandbox

import (
	"context"
	"fmt"
	"runtime"
)

// platformWrapCommand returns an error on unsupported platforms. Operators on
// Windows or other platforms must use mode: env or mode: none instead.
func platformWrapCommand(_ context.Context, _, cmd string, args []string, _ []string) (string, []string, error) {
	return "", nil, fmt.Errorf(
		"sandbox: IsolationFull is not supported on %s; set mode: env or mode: none in .xylem.yml",
		runtime.GOOS,
	)
}
