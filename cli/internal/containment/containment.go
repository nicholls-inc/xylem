package containment

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type IsolationMode string

const (
	IsolationWorkspace IsolationMode = "workspace"
	IsolationOff       IsolationMode = "off"
)

type NetworkMode string

const (
	NetworkInherit NetworkMode = "inherit"
	NetworkDeny    NetworkMode = "deny"
)

type Request struct {
	Isolation  IsolationMode
	Network    NetworkMode
	RuntimeDir string
	Env        []string
	Metadata   map[string]string
}

var safePathComponent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

type contextKey struct{}

func WithRequest(ctx context.Context, req Request) context.Context {
	return context.WithValue(ctx, contextKey{}, req)
}

func RequestFromContext(ctx context.Context) (Request, bool) {
	req, ok := ctx.Value(contextKey{}).(Request)
	return req, ok
}

func BuildRuntimeDir(worktreePath, vesselID, operation, phaseName string) (string, error) {
	for _, component := range []struct {
		name  string
		value string
	}{
		{name: "vessel ID", value: vesselID},
		{name: "operation", value: operation},
		{name: "phase", value: phaseName},
	} {
		if err := validatePathComponent(component.value); err != nil {
			return "", fmt.Errorf("%s: %w", component.name, err)
		}
	}

	return filepath.Join(worktreePath, ".xylem", "runtime", vesselID, operation, phaseName), nil
}

func validatePathComponent(value string) error {
	if value == "" {
		return fmt.Errorf("path component must not be empty")
	}
	if strings.Contains(value, "..") {
		return fmt.Errorf("path component must not contain %q", "..")
	}
	if !safePathComponent.MatchString(value) {
		return fmt.Errorf("path component %q contains invalid characters (allowed: a-zA-Z0-9._-)", value)
	}
	return nil
}
