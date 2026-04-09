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

var (
	daemonGitPull  = gitPull
	daemonGoBuild  = goBuild
	daemonHashFile = hashFile
	daemonExec     = func(path string, args []string, env []string) error {
		return syscall.Exec(path, args, env)
	}
)

type daemonUpgradeTarget struct {
	repoDir        string
	executablePath string
}

func resolveDaemonUpgradeTarget(getwd func() (string, error), executable func() (string, error)) (daemonUpgradeTarget, error) {
	executablePath, err := executable()
	if err != nil {
		return daemonUpgradeTarget{}, fmt.Errorf("resolve executable path: %w", err)
	}

	workingDir, err := getwd()
	if err != nil {
		return daemonUpgradeTarget{}, fmt.Errorf("resolve working directory: %w", err)
	}

	return daemonUpgradeTargetFromPaths(workingDir, executablePath)
}

func daemonUpgradeTargetFromPaths(workingDir, executablePath string) (daemonUpgradeTarget, error) {
	repoDir, err := filepath.Abs(workingDir)
	if err != nil {
		return daemonUpgradeTarget{}, fmt.Errorf("absolute working directory: %w", err)
	}
	return daemonUpgradeTarget{
		repoDir:        repoDir,
		executablePath: executablePath,
	}, nil
}

// selfUpgrade pulls latest main, rebuilds the binary, and exec()s the new
// binary if it changed. On success (binary changed), this function does not
// return — the process image is replaced. On failure or no-change, it returns
// normally so the daemon continues with the current binary.
func selfUpgrade(repoDir, executablePath string) {
	slog.Info("daemon auto-upgrade pulling latest main")

	if err := daemonGitPull(repoDir); err != nil {
		slog.Warn("daemon auto-upgrade git pull failed", "error", err)
		return
	}

	oldHash, err := daemonHashFile(executablePath)
	if err != nil {
		slog.Warn("daemon auto-upgrade failed to hash current binary", "error", err)
		return
	}

	// Build to a temp file to avoid corrupting the running binary on failure.
	cliDir := filepath.Join(repoDir, "cli")
	tmpBin := executablePath + ".upgrade"
	if err := daemonGoBuild(cliDir, tmpBin); err != nil {
		slog.Warn("daemon auto-upgrade go build failed", "error", err)
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	newHash, err := daemonHashFile(tmpBin)
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
	execErr := daemonExec(executablePath, os.Args, os.Environ())
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
	// Resolve current HEAD commit so the new binary can report its version
	// via `xylem version`. Best-effort — if git rev-parse fails, fall back
	// to an unflagged build and let commitHash default to "unknown".
	commit := resolveHEADCommit(cliDir)
	args := []string{"build"}
	if commit != "" {
		args = append(args, "-ldflags", "-X main.commitHash="+commit)
	}
	args = append(args, "-o", outPath, "./cmd/xylem")

	cmd := exec.Command("go", args...)
	cmd.Dir = cliDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, out)
	}
	return nil
}

// resolveHEADCommit returns the current git HEAD commit from the given
// directory, or empty string if not available.
func resolveHEADCommit(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(bytesTrimSpace(out))
}

// bytesTrimSpace trims leading/trailing ASCII whitespace without importing
// strings. Keeps the upgrade module's dependency surface minimal.
func bytesTrimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\n' || b[start] == '\r' || b[start] == '\t') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == '\t') {
		end--
	}
	return b[start:end]
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
