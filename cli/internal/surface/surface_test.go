package surface

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sha256hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func writeFileAt(dir, relPath, content string) error {
	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}

	return nil
}

func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()

	if err := writeFileAt(dir, relPath, content); err != nil {
		t.Fatalf("write %q: %v", filepath.Join(dir, relPath), err)
	}
}

func TestSmoke_S9_TakeSnapshotEmptyDir(t *testing.T) {
	snap, err := TakeSnapshot(t.TempDir(), []string{".xylem/HARNESS.md", ".xylem.yml"})
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	if len(snap.Files) != 0 {
		t.Fatalf("len(Files) = %d, want 0", len(snap.Files))
	}
}

func TestSmoke_S10_TakeSnapshotMatchesAndHashes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".xylem/HARNESS.md", "harness content")
	writeFile(t, dir, ".xylem.yml", "config content")
	writeFile(t, dir, "src/main.go", "package main")

	snap, err := TakeSnapshot(dir, []string{".xylem/HARNESS.md", ".xylem.yml"})
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	if len(snap.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(snap.Files))
	}

	byPath := make(map[string]FileHash, len(snap.Files))
	for _, file := range snap.Files {
		byPath[file.Path] = file
		if file.Hash == "" {
			t.Fatalf("hash for %q is empty", file.Path)
		}
	}

	if _, ok := byPath[".xylem/HARNESS.md"]; !ok {
		t.Fatal(`snapshot missing ".xylem/HARNESS.md"`)
	}
	if got, want := byPath[".xylem/HARNESS.md"].Hash, sha256hex("harness content"); got != want {
		t.Fatalf(`hash for ".xylem/HARNESS.md" = %q, want %q`, got, want)
	}
	if _, ok := byPath[".xylem.yml"]; !ok {
		t.Fatal(`snapshot missing ".xylem.yml"`)
	}
	if got, want := byPath[".xylem.yml"].Hash, sha256hex("config content"); got != want {
		t.Fatalf(`hash for ".xylem.yml" = %q, want %q`, got, want)
	}
	if _, ok := byPath["src/main.go"]; ok {
		t.Fatal(`snapshot unexpectedly contains "src/main.go"`)
	}
}

func TestSmoke_S11_TakeSnapshotDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".xylem.yml", "stable content")

	snap1, err := TakeSnapshot(dir, []string{".xylem.yml"})
	if err != nil {
		t.Fatalf("first TakeSnapshot() error = %v", err)
	}

	snap2, err := TakeSnapshot(dir, []string{".xylem.yml"})
	if err != nil {
		t.Fatalf("second TakeSnapshot() error = %v", err)
	}

	if !reflect.DeepEqual(snap1, snap2) {
		t.Fatalf("snapshots differ: first=%#v second=%#v", snap1, snap2)
	}
}

func TestSmoke_S12_TakeSnapshotSortedByPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".xylem/workflows/implement-feature.yaml", "feature")
	writeFile(t, dir, ".xylem/workflows/fix-bug.yaml", "bug")
	writeFile(t, dir, ".xylem.yml", "config")

	snap, err := TakeSnapshot(dir, []string{".xylem/workflows/*.yaml", ".xylem.yml"})
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}

	for i := 1; i < len(snap.Files); i++ {
		if snap.Files[i-1].Path > snap.Files[i].Path {
			t.Fatalf("files not sorted: %q before %q", snap.Files[i-1].Path, snap.Files[i].Path)
		}
	}
}

func TestSmoke_S13_CompareIdenticalSnapshots(t *testing.T) {
	snap := Snapshot{
		Files: []FileHash{
			{Path: ".xylem.yml", Hash: "abc123"},
		},
	}

	violations := Compare(snap, snap)
	if len(violations) != 0 {
		t.Fatalf("len(violations) = %d, want 0", len(violations))
	}
}

func TestSmoke_S14_CompareDetectsModified(t *testing.T) {
	before := Snapshot{Files: []FileHash{{Path: ".xylem.yml", Hash: "aaa"}}}
	after := Snapshot{Files: []FileHash{{Path: ".xylem.yml", Hash: "bbb"}}}

	violations := Compare(before, after)
	if len(violations) != 1 {
		t.Fatalf("len(violations) = %d, want 1", len(violations))
	}
	if got := violations[0].Path; got != ".xylem.yml" {
		t.Fatalf("path = %q, want %q", got, ".xylem.yml")
	}
	if got := violations[0].Before; got != "aaa" {
		t.Fatalf("before = %q, want %q", got, "aaa")
	}
	if got := violations[0].After; got != "bbb" {
		t.Fatalf("after = %q, want %q", got, "bbb")
	}
}

func TestSmoke_S15_CompareDetectsDeleted(t *testing.T) {
	before := Snapshot{Files: []FileHash{{Path: ".xylem/HARNESS.md", Hash: "ddd"}}}

	violations := Compare(before, Snapshot{})
	if len(violations) != 1 {
		t.Fatalf("len(violations) = %d, want 1", len(violations))
	}
	if got := violations[0].Path; got != ".xylem/HARNESS.md" {
		t.Fatalf("path = %q, want %q", got, ".xylem/HARNESS.md")
	}
	if got := violations[0].Before; got != "ddd" {
		t.Fatalf("before = %q, want %q", got, "ddd")
	}
	if got := violations[0].After; got != "deleted" {
		t.Fatalf("after = %q, want %q", got, "deleted")
	}
}

func TestSmoke_S16_CompareDetectsCreated(t *testing.T) {
	after := Snapshot{Files: []FileHash{{Path: ".xylem/workflows/new.yaml", Hash: "eee"}}}

	violations := Compare(Snapshot{}, after)
	if len(violations) != 1 {
		t.Fatalf("len(violations) = %d, want 1", len(violations))
	}
	if got := violations[0].Path; got != ".xylem/workflows/new.yaml" {
		t.Fatalf("path = %q, want %q", got, ".xylem/workflows/new.yaml")
	}
	if got := violations[0].Before; got != "absent" {
		t.Fatalf("before = %q, want %q", got, "absent")
	}
	if got := violations[0].After; got != "eee" {
		t.Fatalf("after = %q, want %q", got, "eee")
	}
}

func TestTakeSnapshotDeduplicatesOverlappingPatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".xylem.yml", "config")

	snap, err := TakeSnapshot(dir, []string{".xylem.yml", "*"})
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	if len(snap.Files) != 1 {
		t.Fatalf("len(Files) = %d, want 1", len(snap.Files))
	}
	if snap.Files[0].Path != ".xylem.yml" {
		t.Fatalf("path = %q, want %q", snap.Files[0].Path, ".xylem.yml")
	}
}

func TestTakeSnapshotWithNilPatternsReturnsEmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".xylem.yml", "config")

	snap, err := TakeSnapshot(dir, nil)
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	if len(snap.Files) != 0 {
		t.Fatalf("len(Files) = %d, want 0", len(snap.Files))
	}
}

func TestTakeSnapshotSkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".xylem/workflows/fix-bug.yaml", "bug")

	snap, err := TakeSnapshot(dir, []string{".xylem/*", ".xylem/workflows/*.yaml"})
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}

	want := Snapshot{
		Files: []FileHash{
			{Path: ".xylem/workflows/fix-bug.yaml", Hash: sha256hex("bug")},
		},
	}
	if !reflect.DeepEqual(snap, want) {
		t.Fatalf("TakeSnapshot() = %#v, want %#v", snap, want)
	}
}

func TestTakeSnapshotPropagatesReadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".xylem.yml")
	if err := os.WriteFile(path, []byte("config"), 0o000); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	defer func() {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("Chmod() cleanup error = %v", err)
		}
	}()

	_, err := TakeSnapshot(dir, []string{".xylem.yml"})
	if err == nil {
		t.Fatal("TakeSnapshot() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "hash file") {
		t.Fatalf("TakeSnapshot() error = %v, want wrapped hash error", err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("TakeSnapshot() error = %v, want os.ErrPermission", err)
	}
}

func TestTakeSnapshotRejectsInvalidGlobPattern(t *testing.T) {
	dir := t.TempDir()

	_, err := TakeSnapshot(dir, []string{"["})
	if err == nil {
		t.Fatal("TakeSnapshot() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), `glob pattern "["`) {
		t.Fatalf("TakeSnapshot() error = %v, want wrapped glob pattern error", err)
	}
	if !errors.Is(err, filepath.ErrBadPattern) {
		t.Fatalf("TakeSnapshot() error = %v, want filepath.ErrBadPattern", err)
	}
}

func TestTakeSnapshotRejectsUnsafePatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		{name: "parent segment", pattern: "../outside.txt"},
		{name: "absolute path", pattern: filepath.Join(string(filepath.Separator), "tmp", "outside.txt")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TakeSnapshot(t.TempDir(), []string{tt.pattern})
			if err == nil {
				t.Fatal("TakeSnapshot() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("validate pattern %q", tt.pattern)) {
				t.Fatalf("TakeSnapshot() error = %v, want wrapped validation error", err)
			}
		})
	}
}

func TestTakeSnapshotRejectsEscapingMatches(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Dir(dir)
	outsideName := filepath.Base(dir) + "-outside.txt"
	outsidePath := filepath.Join(parent, outsideName)
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	defer os.Remove(outsidePath)

	_, err := TakeSnapshot(dir, []string{"../" + outsideName})
	if err == nil {
		t.Fatal("TakeSnapshot() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "validate pattern") {
		t.Fatalf("TakeSnapshot() error = %v, want validation error", err)
	}
}

func TestTakeSnapshotRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	link := filepath.Join(dir, ".xylem.yml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := TakeSnapshot(dir, []string{".xylem.yml"})
	if err == nil {
		t.Fatal("TakeSnapshot() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), `lstat "`) {
		t.Fatalf("TakeSnapshot() error = %v, want lstat context", err)
	}
	if !errors.Is(err, errSymlinkNotSupported) {
		t.Fatalf("TakeSnapshot() error = %v, want errSymlinkNotSupported", err)
	}
}

func TestCompareReturnsSortedViolations(t *testing.T) {
	before := Snapshot{
		Files: []FileHash{
			{Path: ".xylem/workflows/fix-bug.yaml", Hash: "aaa"},
			{Path: ".xylem.yml", Hash: "bbb"},
		},
	}
	after := Snapshot{
		Files: []FileHash{
			{Path: ".xylem.yml", Hash: "ccc"},
			{Path: ".xylem/HARNESS.md", Hash: "ddd"},
		},
	}

	violations := Compare(before, after)
	want := []Violation{
		{Path: ".xylem.yml", Before: "bbb", After: "ccc"},
		{Path: ".xylem/HARNESS.md", Before: "absent", After: "ddd"},
		{Path: ".xylem/workflows/fix-bug.yaml", Before: "aaa", After: "deleted"},
	}
	if !reflect.DeepEqual(violations, want) {
		t.Fatalf("Compare() = %#v, want %#v", violations, want)
	}
}
