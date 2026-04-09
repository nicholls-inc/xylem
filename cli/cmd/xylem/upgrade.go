package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
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
	log.Println("daemon: auto-upgrade: pulling latest main")

	if err := gitPull(repoDir); err != nil {
		log.Printf("daemon: auto-upgrade: git pull failed: %v (continuing with current binary)", err)
		return
	}

	oldHash, err := hashFile(executablePath)
	if err != nil {
		log.Printf("daemon: auto-upgrade: hash current binary: %v (continuing with current binary)", err)
		return
	}

	// Build to a temp file to avoid corrupting the running binary on failure.
	cliDir := filepath.Join(repoDir, "cli")
	tmpBin := executablePath + ".upgrade"
	if err := goBuild(cliDir, tmpBin); err != nil {
		log.Printf("daemon: auto-upgrade: go build failed: %v (continuing with current binary)", err)
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	newHash, err := hashFile(tmpBin)
	if err != nil {
		log.Printf("daemon: auto-upgrade: hash new binary: %v (continuing with current binary)", err)
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	if oldHash == newHash {
		log.Println("daemon: auto-upgrade: binary unchanged after rebuild")
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	// Atomic rename new binary over old (same filesystem).
	if err := os.Rename(tmpBin, executablePath); err != nil {
		log.Printf("daemon: auto-upgrade: rename failed: %v (continuing with current binary)", err)
		os.Remove(tmpBin) //nolint:errcheck
		return
	}

	log.Printf("daemon: auto-upgrade: binary changed (%s -> %s), exec()ing new binary", oldHash[:12], newHash[:12])
	execErr := syscall.Exec(executablePath, os.Args, os.Environ())
	// If we reach here, exec() failed.
	log.Printf("daemon: auto-upgrade: exec() failed: %v (continuing with current binary)", execErr)
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
