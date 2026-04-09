package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// selfUpgrade pulls latest main, rebuilds the binary, and exec()s the new
// binary if it changed. On success (binary changed), this function does not
// return — the process image is replaced. On failure or no-change, it returns
// normally so the daemon continues with the current binary.
func selfUpgrade(repoDir, executablePath string) {
	slog.Info("daemon auto-upgrade pulling latest main")

	if err := gitPull(repoDir); err != nil {
		slog.Warn("daemon auto-upgrade git pull failed", "error", err)
		return
	}

	oldHash, err := hashFile(executablePath)
	if err != nil {
		slog.Warn("daemon auto-upgrade failed to hash current binary", "error", err)
		return
	}

	// Build to a temp file to avoid corrupting the running binary on failure.
	cliDir := filepath.Join(repoDir, "cli")
	tmpBin := executablePath + ".upgrade"
	if err := goBuild(cliDir, tmpBin); err != nil {
		slog.Warn("daemon auto-upgrade go build failed", "error", err)
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	newHash, err := hashFile(tmpBin)
	if err != nil {
		slog.Warn("daemon auto-upgrade failed to hash rebuilt binary", "error", err)
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	if oldHash == newHash {
		slog.Info("daemon auto-upgrade binary unchanged after rebuild")
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	// Atomic rename new binary over old (same filesystem).
	if err := os.Rename(tmpBin, executablePath); err != nil {
		slog.Warn("daemon auto-upgrade rename failed", "error", err)
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	slog.Info("daemon auto-upgrade execing rebuilt binary", "old_hash", oldHash[:12], "new_hash", newHash[:12])
	execErr := syscall.Exec(executablePath, os.Args, os.Environ())
	// If we reach here, exec() failed.
	slog.Warn("daemon auto-upgrade exec failed", "error", execErr)
}

func gitPull(repoDir string) error {
	fetch := exec.Command("git", "fetch", "origin", "main")
	fetch.Dir = repoDir
	if out, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %w\n%s", err, out)
	}

	reset := exec.Command("git", "reset", "--hard", "origin/main")
	reset.Dir = repoDir
	if out, err := reset.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset: %w\n%s", err, out)
	}

	return nil
}

func goBuild(cliDir, outPath string) error {
	cmd := exec.Command("go", "build", "-o", outPath, "./cmd/xylem")
	cmd.Dir = cliDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, out)
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
