package dtu

import (
	"fmt"
	"path/filepath"
)

const (
	// EnvUniverseID identifies the active DTU universe.
	EnvUniverseID = "XYLEM_DTU_UNIVERSE_ID"
	// EnvStatePath points shims at the materialized DTU state file.
	EnvStatePath = "XYLEM_DTU_STATE_PATH"
	// EnvStateDir points shims at the DTU state root.
	EnvStateDir = "XYLEM_DTU_STATE_DIR"
	// EnvManifestPath points to the source manifest, when known.
	EnvManifestPath = "XYLEM_DTU_MANIFEST"
	// EnvShimDir advertises the shim directory prepended to PATH.
	EnvShimDir = "XYLEM_DTU_SHIM_DIR"
	// EnvEventLogPath points to the append-only DTU event log.
	EnvEventLogPath = "XYLEM_DTU_EVENT_LOG_PATH"
	// EnvWorkDir points to the resolved DTU workdir for operator workflows.
	EnvWorkDir = "XYLEM_DTU_WORKDIR"
	// EnvPhase identifies the active DTU phase for provider and shim matching.
	EnvPhase = "XYLEM_DTU_PHASE"
	// EnvScript identifies the active DTU script name for provider and shim matching.
	EnvScript = "XYLEM_DTU_SCRIPT"
	// EnvAttempt identifies the active DTU attempt counter for provider and shim matching.
	EnvAttempt = "XYLEM_DTU_ATTEMPT"
	// EnvFault selects an explicit shim fault by name.
	EnvFault = "XYLEM_DTU_FAULT"
)

// UniverseDir returns the conventional DTU universe directory for a state root.
func UniverseDir(stateDir, universeID string) (string, error) {
	if err := validatePathComponent(universeID); err != nil {
		return "", fmt.Errorf("resolve DTU universe dir: invalid universe ID: %w", err)
	}
	return filepath.Join(stateDir, "dtu", universeID), nil
}

// DefaultEventLogPath returns the conventional event log path for a DTU universe.
func DefaultEventLogPath(stateDir, universeID string) (string, error) {
	rootDir, err := UniverseDir(stateDir, universeID)
	if err != nil {
		return "", err
	}
	return filepath.Join(rootDir, eventLogFileName), nil
}

// DefaultShimDir returns the conventional shim directory for a DTU state root.
func DefaultShimDir(stateDir string) string {
	return filepath.Join(stateDir, "dtu", "shims")
}

// RecordRuntimeEvent appends a DTU event to the active runtime store when one is configured.
func RecordRuntimeEvent(event *Event) error {
	if event == nil {
		return fmt.Errorf("record DTU runtime event: event must not be nil")
	}
	store, ok, err := runtimeStore()
	if err != nil {
		return fmt.Errorf("record DTU runtime event: %w", err)
	}
	if !ok {
		return nil
	}
	if err := store.RecordEvent(event); err != nil {
		return fmt.Errorf("record DTU runtime event: %w", err)
	}
	return nil
}
