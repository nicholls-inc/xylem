package surface

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileHash struct {
	Path string `json:"path"` // relative to worktree root
	Hash string `json:"hash"` // hex-encoded SHA256
}

type Snapshot struct {
	Files []FileHash `json:"files"`
}

type Violation struct {
	Path   string `json:"path"`
	Before string `json:"before"` // hex hash, or "absent"
	After  string `json:"after"`  // hex hash, or "deleted"
}

var errSymlinkNotSupported = errors.New("symlinks are not supported")

func TakeSnapshot(worktreeRoot string, patterns []string) (Snapshot, error) {
	seen := make(map[string]struct{})
	files := make([]FileHash, 0)

	for _, pattern := range patterns {
		if err := validatePattern(pattern); err != nil {
			return Snapshot{}, fmt.Errorf("validate pattern %q: %w", pattern, err)
		}

		matches, err := filepath.Glob(filepath.Join(worktreeRoot, pattern))
		if err != nil {
			return Snapshot{}, fmt.Errorf("glob pattern %q: %w", pattern, err)
		}

		for _, match := range matches {
			info, err := os.Lstat(match)
			if err != nil {
				return Snapshot{}, fmt.Errorf("lstat %q: %w", match, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return Snapshot{}, fmt.Errorf("lstat %q: %w", match, errSymlinkNotSupported)
			}
			if info.IsDir() {
				continue
			}

			relPath, err := filepath.Rel(worktreeRoot, match)
			if err != nil {
				return Snapshot{}, fmt.Errorf("relative path for %q: %w", match, err)
			}
			if !isPathWithinRoot(relPath) {
				return Snapshot{}, fmt.Errorf("relative path for %q escapes worktree root: %q", match, relPath)
			}

			relPath = filepath.ToSlash(relPath)
			if _, ok := seen[relPath]; ok {
				continue
			}

			hash, err := hashFile(match)
			if err != nil {
				return Snapshot{}, fmt.Errorf("hash file %q: %w", match, err)
			}

			seen[relPath] = struct{}{}
			files = append(files, FileHash{
				Path: relPath,
				Hash: hash,
			})
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	return Snapshot{Files: files}, nil
}

func validatePattern(pattern string) error {
	if filepath.IsAbs(pattern) {
		return fmt.Errorf("pattern must be relative")
	}
	if !isPathWithinRoot(pattern) {
		return fmt.Errorf("pattern must stay within worktree root")
	}

	return nil
}

func isPathWithinRoot(path string) bool {
	cleaned := filepath.Clean(path)
	parentPrefix := ".." + string(filepath.Separator)

	return cleaned != ".." && !strings.HasPrefix(cleaned, parentPrefix)
}

func Compare(before, after Snapshot) []Violation {
	beforeByPath := make(map[string]string, len(before.Files))
	for _, file := range before.Files {
		beforeByPath[file.Path] = file.Hash
	}

	afterByPath := make(map[string]string, len(after.Files))
	for _, file := range after.Files {
		afterByPath[file.Path] = file.Hash
	}

	violations := make([]Violation, 0)
	for path, beforeHash := range beforeByPath {
		afterHash, ok := afterByPath[path]
		if !ok {
			violations = append(violations, Violation{
				Path:   path,
				Before: beforeHash,
				After:  "deleted",
			})
			continue
		}
		if beforeHash != afterHash {
			violations = append(violations, Violation{
				Path:   path,
				Before: beforeHash,
				After:  afterHash,
			})
		}
	}

	for path, afterHash := range afterByPath {
		if _, ok := beforeByPath[path]; ok {
			continue
		}
		violations = append(violations, Violation{
			Path:   path,
			Before: "absent",
			After:  afterHash,
		})
	}

	sort.Slice(violations, func(i, j int) bool {
		return violations[i].Path < violations[j].Path
	})

	return violations
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash contents: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
