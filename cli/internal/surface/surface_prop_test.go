package surface

import (
	"os"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

var knownPaths = []string{
	".xylem.yml",
	".xylem/HARNESS.md",
	".xylem/workflows/fix-bug.yaml",
	".xylem/workflows/implement-feature.yaml",
	"docs/ignored.txt",
	"src/main.go",
}

var knownPatterns = []string{
	".xylem.yml",
	".xylem/HARNESS.md",
	".xylem/workflows/*.yaml",
	".xylem/*",
	"docs/*",
	"src/*",
}

func genFileHash(t *rapid.T) FileHash {
	return FileHash{
		Path: rapid.StringMatching(`[a-z0-9./_-]{1,32}`).Draw(t, "path"),
		Hash: rapid.StringMatching(`[0-9a-f]{64}`).Draw(t, "hash"),
	}
}

func genSnapshot(t *rapid.T) Snapshot {
	count := rapid.IntRange(0, 10).Draw(t, "count")
	byPath := make(map[string]FileHash, count)
	for range count {
		fh := genFileHash(t)
		byPath[fh.Path] = fh
	}

	files := make([]FileHash, 0, len(byPath))
	for _, fh := range byPath {
		files = append(files, fh)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	return Snapshot{Files: files}
}

func genFileContent(t *rapid.T) string {
	return rapid.StringMatching(`[ -~]{0,64}`).Draw(t, "content")
}

func genFilesystemFixture(t *rapid.T) (map[string]string, []string) {
	files := make(map[string]string)
	for _, path := range knownPaths {
		if rapid.Bool().Draw(t, "includeFile"+path) {
			files[path] = genFileContent(t)
		}
	}

	patterns := make([]string, 0, len(knownPatterns))
	for _, pattern := range knownPatterns {
		if rapid.Bool().Draw(t, "includePattern"+pattern) {
			patterns = append(patterns, pattern)
		}
	}

	return files, patterns
}

func TestProp_TakeSnapshotAlwaysSorted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "surface-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		files, patterns := genFilesystemFixture(t)
		for relPath, content := range files {
			if err := writeFileAt(dir, relPath, content); err != nil {
				t.Fatalf("writeFileAt() error = %v", err)
			}
		}

		snap, err := TakeSnapshot(dir, patterns)
		if err != nil {
			t.Fatalf("TakeSnapshot() error = %v", err)
		}

		for i := 1; i < len(snap.Files); i++ {
			if snap.Files[i-1].Path > snap.Files[i].Path {
				t.Fatalf("files not sorted: %q before %q", snap.Files[i-1].Path, snap.Files[i].Path)
			}
		}
	})
}

func TestProp_TakeSnapshotDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "surface-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		files, patterns := genFilesystemFixture(t)
		for relPath, content := range files {
			if err := writeFileAt(dir, relPath, content); err != nil {
				t.Fatalf("writeFileAt() error = %v", err)
			}
		}

		first, err := TakeSnapshot(dir, patterns)
		if err != nil {
			t.Fatalf("first TakeSnapshot() error = %v", err)
		}
		second, err := TakeSnapshot(dir, patterns)
		if err != nil {
			t.Fatalf("second TakeSnapshot() error = %v", err)
		}

		if len(first.Files) != len(second.Files) {
			t.Fatalf("snapshot lengths differ: %d != %d", len(first.Files), len(second.Files))
		}
		for i := range first.Files {
			if first.Files[i] != second.Files[i] {
				t.Fatalf("snapshot entry %d differs: %#v != %#v", i, first.Files[i], second.Files[i])
			}
		}
	})
}

func TestProp_CompareIdenticalIsEmpty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		snap := genSnapshot(t)
		violations := Compare(snap, snap)
		if len(violations) != 0 {
			t.Fatalf("len(violations) = %d, want 0", len(violations))
		}
	})
}

func TestProp_CompareDetectsAllDeletions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		before := genSnapshot(t)
		violations := Compare(before, Snapshot{})
		if len(violations) != len(before.Files) {
			t.Fatalf("len(violations) = %d, want %d", len(violations), len(before.Files))
		}
		for i, violation := range violations {
			if violation.Path != before.Files[i].Path {
				t.Fatalf("violation %d path = %q, want %q", i, violation.Path, before.Files[i].Path)
			}
			if violation.Before != before.Files[i].Hash {
				t.Fatalf("violation %d before = %q, want %q", i, violation.Before, before.Files[i].Hash)
			}
			if violation.After != "deleted" {
				t.Fatalf("violation %d after = %q, want %q", i, violation.After, "deleted")
			}
		}
	})
}

func TestProp_CompareDetectsAllCreations(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		after := genSnapshot(t)
		violations := Compare(Snapshot{}, after)
		if len(violations) != len(after.Files) {
			t.Fatalf("len(violations) = %d, want %d", len(violations), len(after.Files))
		}
		for i, violation := range violations {
			if violation.Path != after.Files[i].Path {
				t.Fatalf("violation %d path = %q, want %q", i, violation.Path, after.Files[i].Path)
			}
			if violation.Before != "absent" {
				t.Fatalf("violation %d before = %q, want %q", i, violation.Before, "absent")
			}
			if violation.After != after.Files[i].Hash {
				t.Fatalf("violation %d after = %q, want %q", i, violation.After, after.Files[i].Hash)
			}
		}
	})
}

func TestProp_CompareSymmetry(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genSnapshot(t)
		b := genSnapshot(t)

		ab := Compare(a, b)
		ba := Compare(b, a)

		abByPath := make(map[string]Violation, len(ab))
		for _, violation := range ab {
			abByPath[violation.Path] = violation
		}

		baByPath := make(map[string]Violation, len(ba))
		for _, violation := range ba {
			baByPath[violation.Path] = violation
		}

		for path, forward := range abByPath {
			reverse, ok := baByPath[path]
			if !ok {
				t.Fatalf("missing reverse violation for path %q", path)
			}

			switch {
			case forward.After == "deleted":
				if reverse.Before != "absent" || reverse.After != forward.Before {
					t.Fatalf("deletion symmetry mismatch for %q: forward=%#v reverse=%#v", path, forward, reverse)
				}
			case forward.Before == "absent":
				if reverse.After != "deleted" || reverse.Before != forward.After {
					t.Fatalf("creation symmetry mismatch for %q: forward=%#v reverse=%#v", path, forward, reverse)
				}
			default:
				if reverse.Before != forward.After || reverse.After != forward.Before {
					t.Fatalf("modification symmetry mismatch for %q: forward=%#v reverse=%#v", path, forward, reverse)
				}
			}
		}

		if len(abByPath) != len(baByPath) {
			t.Fatalf("violation set sizes differ: %d != %d", len(abByPath), len(baByPath))
		}
	})
}

func TestProp_CompareResultsStaySorted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		violations := Compare(genSnapshot(t), genSnapshot(t))
		for i := 1; i < len(violations); i++ {
			if strings.Compare(violations[i-1].Path, violations[i].Path) > 0 {
				t.Fatalf("violations not sorted: %q before %q", violations[i-1].Path, violations[i].Path)
			}
		}
	})
}
