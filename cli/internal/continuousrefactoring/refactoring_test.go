package continuousrefactoring

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

func TestInspectRejectsSourceDirOutsideRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := &config.Config{
		Repo: "owner/repo",
		Sources: map[string]config.SourceConfig{
			"continuous-refactoring-semantic": {
				Type:           "schedule",
				Repo:           "owner/repo",
				Workflow:       "continuous-refactoring",
				SourceDirs:     []string{"../outside"},
				FileExtensions: []string{".go"},
			},
		},
	}

	_, err := Inspect(cfg, repoRoot, "continuous-refactoring-semantic", time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Inspect() error = nil, want source dir rejection")
	}
	if !strings.Contains(err.Error(), `source dir "../outside" escapes repo root`) {
		t.Fatalf("Inspect() error = %q, want source dir escape error", err)
	}
}

func TestResolveSourceDirAcceptsNestedRepoPath(t *testing.T) {
	repoRoot := t.TempDir()

	got, err := resolveSourceDir(repoRoot, filepath.Join("cli", "internal"))
	if err != nil {
		t.Fatalf("resolveSourceDir() error = %v", err)
	}

	want := filepath.Join(repoRoot, "cli", "internal")
	if evalWant, evalErr := filepath.EvalSymlinks(repoRoot); evalErr == nil {
		want = filepath.Join(evalWant, "cli", "internal")
	}
	if got != want {
		t.Fatalf("resolveSourceDir() = %q, want %q", got, want)
	}
}
