package source

import (
	"path/filepath"
	"strings"
)

func controlPlanePathsTouched(paths []string) bool {
	for _, path := range paths {
		if isControlPlanePath(path) {
			return true
		}
	}
	return false
}

func isControlPlanePath(path string) bool {
	normalized := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	switch {
	case normalized == ".xylem.yml":
		return true
	case normalized == ".xylem/HARNESS.md":
		return true
	case normalized == ".xylem/workflows":
		return true
	case strings.HasPrefix(normalized, ".xylem/workflows/"):
		return true
	case normalized == ".xylem/prompts":
		return true
	case strings.HasPrefix(normalized, ".xylem/prompts/"):
		return true
	default:
		return false
	}
}
