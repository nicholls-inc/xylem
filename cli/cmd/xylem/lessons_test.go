package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	lessonspkg "github.com/nicholls-inc/xylem/cli/internal/lessons"
)

type lessonsRunnerStub struct {
	processCalls []string
	phaseCalls   []string
}

func (s *lessonsRunnerStub) RunOutput(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func (s *lessonsRunnerStub) RunProcess(_ context.Context, dir string, name string, args ...string) error {
	s.processCalls = append(s.processCalls, dir+"::"+name+" "+strings.Join(args, " "))
	return nil
}

func (s *lessonsRunnerStub) RunPhase(_ context.Context, dir string, _ io.Reader, name string, args ...string) ([]byte, error) {
	call := dir + "::" + name + " " + strings.Join(args, " ")
	s.phaseCalls = append(s.phaseCalls, call)
	if strings.Contains(call, "pr list") {
		return []byte(`[{"number":17,"url":"https://github.com/owner/repo/pull/17"}]`), nil
	}
	return []byte("https://github.com/owner/repo/pull/17\n"), nil
}

type lessonsWorktreeStub struct {
	path    string
	creates []string
	removes []string
}

func (s *lessonsWorktreeStub) Create(_ context.Context, branchName string) (string, error) {
	s.creates = append(s.creates, branchName)
	return s.path, nil
}

func (s *lessonsWorktreeStub) Remove(_ context.Context, worktreePath string) error {
	s.removes = append(s.removes, worktreePath)
	return nil
}

func TestCmdLessonsPrintsGeneratedReport(t *testing.T) {
	cfg := &config.Config{StateDir: t.TempDir()}
	original := generateLessons
	t.Cleanup(func() { generateLessons = original })

	generateLessons = func(_ context.Context, cfg *config.Config, _ lessonsRunner) (*lessonspkg.Result, error) {
		return &lessonspkg.Result{
			JSONPath:     cfg.StateDir + "/reviews/lessons.json",
			MarkdownPath: cfg.StateDir + "/reviews/lessons.md",
			Markdown:     "# Lessons synthesis\n",
		}, nil
	}

	out := captureStdout(func() {
		if err := cmdLessons(cfg, nil, &lessonsRunnerStub{}); err != nil {
			t.Fatalf("cmdLessons() error = %v", err)
		}
	})
	if !strings.Contains(out, "lessons.json") {
		t.Fatalf("output = %q, want json path", out)
	}
	if !strings.Contains(out, "# Lessons synthesis") {
		t.Fatalf("output = %q, want markdown body", out)
	}
}

func TestCmdLessonsCreatesPullRequestsForGeneratedProposals(t *testing.T) {
	stateDir := t.TempDir()
	worktreePath := filepath.Join(stateDir, "worktree")
	if err := os.MkdirAll(filepath.Join(worktreePath, ".xylem"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree .xylem) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, ".xylem", "HARNESS.md"), []byte("# Harness\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(HARNESS.md) error = %v", err)
	}

	cfg := &config.Config{
		StateDir: stateDir,
		Repo:     "owner/repo",
	}
	original := generateLessons
	t.Cleanup(func() { generateLessons = original })
	generateLessons = func(_ context.Context, cfg *config.Config, _ lessonsRunner) (*lessonspkg.Result, error) {
		return &lessonspkg.Result{
			Report: &lessonspkg.Report{
				Proposals: []lessonspkg.Proposal{{
					Theme:              "lessons-verify",
					Branch:             "chore/lessons-lessons-verify-abc12345",
					Title:              "[lessons] lessons-verify institutional memory updates",
					Body:               "body",
					HarnessPath:        ".xylem/HARNESS.md",
					LessonFingerprints: []string{"lesson-abc123"},
					HarnessPatch:       "### Do not ship broken tests <!-- xylem-lesson:lesson-abc123 -->\n- Rationale: keep the tests green\n- Example symptom: tests still fail\n",
					Status:             "pending",
				}},
			},
			JSONPath:     filepath.Join(cfg.StateDir, "reviews", "lessons.json"),
			MarkdownPath: filepath.Join(cfg.StateDir, "reviews", "lessons.md"),
			Markdown:     "# Lessons synthesis\n",
		}, nil
	}

	wt := &lessonsWorktreeStub{path: worktreePath}
	runner := &lessonsRunnerStub{}
	if err := cmdLessons(cfg, wt, runner); err != nil {
		t.Fatalf("cmdLessons() error = %v", err)
	}

	harnessData, err := os.ReadFile(filepath.Join(worktreePath, ".xylem", "HARNESS.md"))
	if err != nil {
		t.Fatalf("ReadFile(HARNESS.md) error = %v", err)
	}
	if !strings.Contains(string(harnessData), "xylem-lesson:lesson-abc123") {
		t.Fatalf("HARNESS.md = %q, want appended lesson patch", string(harnessData))
	}
	if len(runner.processCalls) != 3 {
		t.Fatalf("len(processCalls) = %d, want 3", len(runner.processCalls))
	}
	if len(runner.phaseCalls) != 2 {
		t.Fatalf("len(phaseCalls) = %d, want 2", len(runner.phaseCalls))
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "reviews", "lessons.json"))
	if err != nil {
		t.Fatalf("ReadFile(lessons.json) error = %v", err)
	}
	var report lessonspkg.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("Unmarshal(lessons.json) error = %v", err)
	}
	if got := report.Proposals[0].Status; got != "created" {
		t.Fatalf("proposal status = %q, want created", got)
	}
	if got := report.Proposals[0].PRURL; got != "https://github.com/owner/repo/pull/17" {
		t.Fatalf("proposal pr_url = %q, want created PR URL", got)
	}
}
