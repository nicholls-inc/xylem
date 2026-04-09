package lessons

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

type prClientStub struct {
	prs []OpenPullRequest
	err error
}

func (s *prClientStub) ListOpenPullRequests(_ context.Context, _ string) ([]OpenPullRequest, error) {
	return s.prs, s.err
}

func TestGenerateClustersRecurringFailuresIntoSingleLesson(t *testing.T) {
	stateDir := t.TempDir()
	harnessPath := filepath.Join(stateDir, "HARNESS.md")
	if err := os.WriteFile(harnessPath, []byte("# Harness\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(HARNESS.md) error = %v", err)
	}

	base := time.Date(2026, time.April, 9, 6, 0, 0, 0, time.UTC)
	writeFailedRun(t, stateDir, "failed-a", base, "tests still fail on retry")
	writeFailedRun(t, stateDir, "failed-b", base.Add(time.Hour), "tests still fail on retry")

	result, err := Generate(context.Background(), stateDir, Options{
		Repo:           "owner/repo",
		HarnessPath:    harnessPath,
		LookbackWindow: 30 * 24 * time.Hour,
		MinSamples:     2,
		Now:            base.Add(2 * time.Hour),
	}, &prClientStub{})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(result.Report.Lessons) != 1 {
		t.Fatalf("len(Report.Lessons) = %d, want 1", len(result.Report.Lessons))
	}
	lesson := result.Report.Lessons[0]
	if lesson.Samples != 2 {
		t.Fatalf("lesson.Samples = %d, want 2", lesson.Samples)
	}
	if !strings.Contains(lesson.NegativeConstraint, "tests still fail on retry") {
		t.Fatalf("NegativeConstraint = %q, want normalized signal", lesson.NegativeConstraint)
	}
	if len(result.Report.Proposals) != 1 {
		t.Fatalf("len(Report.Proposals) = %d, want 1", len(result.Report.Proposals))
	}
	if !strings.Contains(result.Report.Proposals[0].HarnessPatch, "xylem-lesson:"+lesson.Fingerprint) {
		t.Fatalf("HarnessPatch = %q, want lesson fingerprint marker", result.Report.Proposals[0].HarnessPatch)
	}
}

func TestGenerateSkipsLessonWhenEquivalentOpenPRExists(t *testing.T) {
	stateDir := t.TempDir()
	harnessPath := filepath.Join(stateDir, "HARNESS.md")
	if err := os.WriteFile(harnessPath, []byte("# Harness\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(HARNESS.md) error = %v", err)
	}

	base := time.Date(2026, time.April, 9, 6, 0, 0, 0, time.UTC)
	writeFailedRun(t, stateDir, "failed-a", base, "tests still fail on retry")
	writeFailedRun(t, stateDir, "failed-b", base.Add(time.Hour), "tests still fail on retry")

	fingerprint := fingerprintFor("lessons", "verify", "evidence", normalizeSignal("tests still fail on retry"))
	result, err := Generate(context.Background(), stateDir, Options{
		Repo:           "owner/repo",
		HarnessPath:    harnessPath,
		LookbackWindow: 30 * 24 * time.Hour,
		MinSamples:     2,
		Now:            base.Add(2 * time.Hour),
	}, &prClientStub{prs: []OpenPullRequest{{
		Number:     42,
		Title:      "[lessons] lessons-verify institutional memory updates",
		Body:       "contains " + fingerprint,
		HeadBranch: "chore/lessons-lessons-verify-" + fingerprint[:8],
	}}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(result.Report.Lessons) != 0 {
		t.Fatalf("len(Report.Lessons) = %d, want 0", len(result.Report.Lessons))
	}
	if len(result.Report.Skipped) != 1 {
		t.Fatalf("len(Report.Skipped) = %d, want 1", len(result.Report.Skipped))
	}
	if result.Report.Skipped[0].Reason != "equivalent open PR already exists" {
		t.Fatalf("Skipped reason = %q, want open-PR dedupe", result.Report.Skipped[0].Reason)
	}
}

func writeFailedRun(t *testing.T, stateDir, vesselID string, endedAt time.Time, claim string) {
	t.Helper()
	startedAt := endedAt.Add(-2 * time.Minute)
	manifest := &evidence.Manifest{
		VesselID: vesselID,
		Workflow: "lessons",
		Claims: []evidence.Claim{{
			Claim:     claim,
			Phase:     "verify",
			Passed:    false,
			Timestamp: endedAt,
		}},
		CreatedAt: endedAt,
	}
	if err := evidence.SaveManifest(stateDir, vesselID, manifest); err != nil {
		t.Fatalf("SaveManifest() error = %v", err)
	}
	summary := &runner.VesselSummary{
		VesselID:             vesselID,
		Source:               "schedule",
		Workflow:             "lessons",
		State:                "failed",
		StartedAt:            startedAt,
		EndedAt:              endedAt,
		DurationMS:           endedAt.Sub(startedAt).Milliseconds(),
		EvidenceManifestPath: filepath.ToSlash(filepath.Join("phases", vesselID, "evidence-manifest.json")),
		ReviewArtifacts: &runner.ReviewArtifacts{
			EvidenceManifest: filepath.ToSlash(filepath.Join("phases", vesselID, "evidence-manifest.json")),
		},
		Phases: []runner.PhaseSummary{{
			Name:   "verify",
			Type:   "prompt",
			Status: "failed",
			Error:  claim,
		}},
		Note: "fixture",
	}
	if err := runner.SaveVesselSummary(stateDir, summary); err != nil {
		t.Fatalf("SaveVesselSummary() error = %v", err)
	}
}
