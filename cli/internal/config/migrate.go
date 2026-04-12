package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type flatRuntimeFileMigration struct {
	name      string
	moveLock  bool
	writeMark bool
}

// MigrateFlatStateToRuntime moves legacy flat runtime files beneath the
// profile-ready state/ subtree. It fails loudly on half-migrated conflicts so
// operators can repair the layout explicitly instead of silently splitting
// runtime state across both locations.
func MigrateFlatStateToRuntime(stateDir string) error {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" || !looksLikeControlPlaneDir(stateDir) {
		return nil
	}

	runtimeRoot := filepath.Join(stateDir, runtimeStateDirName)
	if err := os.MkdirAll(runtimeRoot, 0o755); err != nil {
		return fmt.Errorf("migrate flat state to runtime: create runtime dir: %w", err)
	}

	for _, spec := range []flatRuntimeFileMigration{
		{name: "queue.jsonl", moveLock: true, writeMark: true},
		{name: DefaultAuditLogPath, moveLock: true, writeMark: true},
	} {
		if err := migrateFlatRuntimeFile(stateDir, runtimeRoot, spec); err != nil {
			return err
		}
	}
	if err := migrateFlatDaemonPID(stateDir, runtimeRoot); err != nil {
		return err
	}
	return nil
}

func migrateFlatRuntimeFile(stateDir, runtimeRoot string, spec flatRuntimeFileMigration) error {
	legacyPath := filepath.Join(stateDir, spec.name)
	runtimePath := filepath.Join(runtimeRoot, spec.name)
	legacyLockPath := legacyPath + ".lock"
	runtimeLockPath := runtimePath + ".lock"

	if pathExists(legacyPath) && pathExists(runtimePath) {
		return fmt.Errorf("migrate flat state to runtime: both legacy and runtime %s exist", spec.name)
	}
	if spec.moveLock && pathExists(legacyLockPath) && pathExists(runtimeLockPath) {
		return fmt.Errorf("migrate flat state to runtime: both legacy and runtime %s.lock exist", spec.name)
	}
	if !pathExists(legacyPath) {
		return nil
	}

	if err := os.Rename(legacyPath, runtimePath); err != nil {
		return fmt.Errorf("migrate flat state to runtime: rename %s: %w", spec.name, err)
	}
	if spec.moveLock && pathExists(legacyLockPath) {
		if err := os.Rename(legacyLockPath, runtimeLockPath); err != nil {
			return fmt.Errorf("migrate flat state to runtime: rename %s.lock: %w", spec.name, err)
		}
	}
	if spec.writeMark {
		if err := writeMigrationMarker(legacyPath + ".migrated"); err != nil {
			return fmt.Errorf("migrate flat state to runtime: write %s marker: %w", spec.name, err)
		}
	}

	slog.Info("migrated legacy runtime file", "from", legacyPath, "to", runtimePath)
	return nil
}

func migrateFlatDaemonPID(stateDir, runtimeRoot string) error {
	legacyPath := filepath.Join(stateDir, "daemon.pid")
	runtimePath := filepath.Join(runtimeRoot, "daemon.pid")

	if pathExists(legacyPath) && pathExists(runtimePath) {
		return fmt.Errorf("migrate flat state to runtime: both legacy and runtime daemon.pid exist")
	}
	if !pathExists(legacyPath) {
		return nil
	}

	legacyPID, err := readPIDFile(legacyPath)
	if err != nil {
		return fmt.Errorf("migrate flat state to runtime: read daemon.pid: %w", err)
	}
	if legacyPID > 0 && legacyPID != os.Getpid() && processAlive(legacyPID) {
		return fmt.Errorf("migrate flat state to runtime: legacy daemon.pid still references live pid %d", legacyPID)
	}

	if pathExists(runtimePath) {
		return fmt.Errorf("migrate flat state to runtime: runtime daemon.pid unexpectedly appeared during migration")
	}
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("migrate flat state to runtime: remove legacy daemon.pid: %w", err)
	}
	if err := writeMigrationMarker(legacyPath + ".migrated"); err != nil {
		return fmt.Errorf("migrate flat state to runtime: write daemon.pid marker: %w", err)
	}

	slog.Info("prepared daemon pid migration", "from", legacyPath, "to", runtimePath, "action", "cleared-for-lock-rewrite")
	return nil
}

func writeMigrationMarker(path string) error {
	return os.WriteFile(path, nil, 0o644)
}

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}
	return pid, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
