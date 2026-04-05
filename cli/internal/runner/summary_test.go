package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/reporter"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

func TestSaveVesselSummaryWritesPrettyPrintedJSON(t *testing.T) {
	stateDir := t.TempDir()
	summary := &VesselSummary{
		VesselID: "vessel-abc123",
		Source:   "manual",
		State:    "completed",
		Phases:   []PhaseSummary{},
	}

	if err := SaveVesselSummary(stateDir, summary); err != nil {
		t.Fatalf("SaveVesselSummary() error = %v", err)
	}

	path := filepath.Join(stateDir, "phases", "vessel-abc123", summaryFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read summary file: %v", err)
	}
	if !strings.Contains(string(data), "\n  \"") {
		t.Fatalf("summary.json is not pretty printed: %s", string(data))
	}

	var got VesselSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal summary.json: %v", err)
	}
	if got.Note != summaryDisclaimer {
		t.Fatalf("Note = %q, want %q", got.Note, summaryDisclaimer)
	}
}

func TestVesselRunStateBuildSummaryIncludesBudgetLimits(t *testing.T) {
	startedAt := time.Now().Add(-2 * time.Minute).UTC()
	cfg := &config.Config{
		Cost: config.CostConfig{
			Budget: &config.BudgetConfig{
				MaxCostUSD: 1.0,
				MaxTokens:  50000,
			},
		},
	}
	vrs := newVesselRunState(cfg, queue.Vessel{
		ID:       "vessel-budget",
		Source:   "manual",
		Workflow: "fix-bug",
	}, startedAt)
	vrs.addPhase(PhaseSummary{
		Name:            "implement",
		Status:          "completed",
		InputTokensEst:  100,
		OutputTokensEst: 50,
		CostUSDEst:      0.25,
	})

	summary := vrs.buildSummary("completed", startedAt.Add(30*time.Second))
	if got, want := summary.TotalInputTokensEst, 100; got != want {
		t.Fatalf("TotalInputTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalOutputTokensEst, 50; got != want {
		t.Fatalf("TotalOutputTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalTokensEst, 150; got != want {
		t.Fatalf("TotalTokensEst = %d, want %d", got, want)
	}
	if got, want := summary.TotalCostUSDEst, 0.25; got != want {
		t.Fatalf("TotalCostUSDEst = %v, want %v", got, want)
	}
	if summary.BudgetMaxCostUSD == nil || *summary.BudgetMaxCostUSD != 1.0 {
		t.Fatalf("BudgetMaxCostUSD = %#v, want 1.0", summary.BudgetMaxCostUSD)
	}
	if summary.BudgetMaxTokens == nil || *summary.BudgetMaxTokens != 50000 {
		t.Fatalf("BudgetMaxTokens = %#v, want 50000", summary.BudgetMaxTokens)
	}
}

func TestBuildGateClaimUsesEvidenceMetadata(t *testing.T) {
	recordedAt := time.Date(2026, time.March, 31, 12, 0, 0, 0, time.UTC)
	artifactPath := phaseArtifactRelativePath("vessel-1", "implement")
	claim := buildGateClaim(workflow.Phase{
		Name: "implement",
		Gate: &workflow.Gate{
			Run: "go test ./...",
			Evidence: &workflow.GateEvidence{
				Claim:         "All tests pass",
				Level:         "behaviorally_checked",
				Checker:       "go test",
				TrustBoundary: "Package-level only",
			},
		},
	}, true, artifactPath, recordedAt)

	if claim.Claim != "All tests pass" {
		t.Fatalf("Claim = %q, want %q", claim.Claim, "All tests pass")
	}
	if claim.Level != evidence.BehaviorallyChecked {
		t.Fatalf("Level = %q, want %q", claim.Level, evidence.BehaviorallyChecked)
	}
	if claim.Checker != "go test" {
		t.Fatalf("Checker = %q, want %q", claim.Checker, "go test")
	}
	if claim.TrustBoundary != "Package-level only" {
		t.Fatalf("TrustBoundary = %q, want %q", claim.TrustBoundary, "Package-level only")
	}
	if !claim.Passed {
		t.Fatal("Passed = false, want true")
	}
	if claim.ArtifactPath != artifactPath {
		t.Fatalf("ArtifactPath = %q, want %q", claim.ArtifactPath, artifactPath)
	}
	if claim.Phase != "implement" {
		t.Fatalf("Phase = %q, want %q", claim.Phase, "implement")
	}
	if !claim.Timestamp.Equal(recordedAt) {
		t.Fatalf("Timestamp = %s, want %s", claim.Timestamp, recordedAt)
	}
}

func TestBuildGateClaimUsesDefaultsWithoutEvidence(t *testing.T) {
	recordedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	artifactPath := phaseArtifactRelativePath("vessel-1", "implement")
	claim := buildGateClaim(workflow.Phase{
		Name: "implement",
		Gate: &workflow.Gate{Run: "cd cli && go test ./..."},
	}, true, artifactPath, recordedAt)

	if claim.Level != evidence.Untyped {
		t.Fatalf("Level = %q, want %q", claim.Level, evidence.Untyped)
	}
	if claim.TrustBoundary != "No trust boundary declared" {
		t.Fatalf("TrustBoundary = %q, want %q", claim.TrustBoundary, "No trust boundary declared")
	}
	if !strings.Contains(claim.Claim, "implement") {
		t.Fatalf("Claim = %q, want phase name", claim.Claim)
	}
	if claim.Checker != "cd cli && go test ./..." {
		t.Fatalf("Checker = %q, want gate run command", claim.Checker)
	}
	if !claim.Passed {
		t.Fatal("Passed = false, want true")
	}
	if claim.ArtifactPath != artifactPath {
		t.Fatalf("ArtifactPath = %q, want %q", claim.ArtifactPath, artifactPath)
	}
	if claim.Phase != "implement" {
		t.Fatalf("Phase = %q, want %q", claim.Phase, "implement")
	}
	if !claim.Timestamp.Equal(recordedAt) {
		t.Fatalf("Timestamp = %s, want %s", claim.Timestamp, recordedAt)
	}
}

func TestDrainPromptOnlyWritesSummaryArtifact(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makePromptVessel(1, "Prompt-only workflow summary smoke"))

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Prompt-only workflow summary smoke": []byte("Prompt-only completion output for summary telemetry"),
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	summary := loadSummary(t, cfg.StateDir, "prompt-1")
	if summary.State != "completed" {
		t.Fatalf("State = %q, want completed", summary.State)
	}
	if len(summary.Phases) != 0 {
		t.Fatalf("len(Phases) = %d, want 0", len(summary.Phases))
	}
	if summary.TotalTokensEst <= 0 {
		t.Fatalf("TotalTokensEst = %d, want > 0", summary.TotalTokensEst)
	}
	if summary.EvidenceManifestPath != "" {
		t.Fatalf("EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
	}

	manifestPath := filepath.Join(cfg.StateDir, "phases", "prompt-1", "evidence-manifest.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("expected no evidence manifest, got err=%v", err)
	}
}

func TestDrainWritesFailureSummaryAndEvidenceManifest(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(1, "artifact-failure"))

	writeWorkflowFile(t, dir, "artifact-failure", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the issue",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make analyze\"\n      evidence:\n        claim: \"Analyze gate passed\"\n        level: behaviorally_checked\n        checker: \"make analyze\"\n        trust_boundary: \"Analysis scope only\"",
		},
		{
			name:          "implement",
			promptContent: "Implement the fix",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make implement\"\n      retries: 0",
		},
	})

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the issue": []byte("analysis output"),
			"Implement the fix": []byte("implementation output"),
		},
		gateCallResults: []gateCallResult{
			{output: []byte("analyze gate ok"), err: nil},
			{output: []byte("implement gate failed"), err: &mockExitError{code: 1}},
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", result.Failed)
	}

	summary := loadSummary(t, cfg.StateDir, "issue-1")
	if summary.State != "failed" {
		t.Fatalf("State = %q, want failed", summary.State)
	}
	if len(summary.Phases) != 2 {
		t.Fatalf("len(Phases) = %d, want 2", len(summary.Phases))
	}
	if summary.Phases[0].Status != "completed" {
		t.Fatalf("Phases[0].Status = %q, want completed", summary.Phases[0].Status)
	}
	if summary.Phases[1].Status != "failed" {
		t.Fatalf("Phases[1].Status = %q, want failed", summary.Phases[1].Status)
	}
	if summary.EvidenceManifestPath != evidenceManifestRelativePath("issue-1") {
		t.Fatalf("EvidenceManifestPath = %q, want %q", summary.EvidenceManifestPath, evidenceManifestRelativePath("issue-1"))
	}

	manifest, err := evidence.LoadManifest(cfg.StateDir, "issue-1")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(manifest.Claims) != 1 {
		t.Fatalf("len(Claims) = %d, want 1", len(manifest.Claims))
	}
	if manifest.Claims[0].Phase != "analyze" {
		t.Fatalf("Claims[0].Phase = %q, want analyze", manifest.Claims[0].Phase)
	}
	for _, claim := range manifest.Claims {
		if claim.Phase == "implement" {
			t.Fatalf("unexpected claim for failed phase: %+v", claim)
		}
	}
}

func TestDrainOrchestratedWritesSummaryManifestAndReporterEvidence(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(42, "artifact-orchestrated"))

	writeWorkflowFile(t, dir, "artifact-orchestrated", []testPhase{
		{
			name:          "analyze",
			promptContent: "Analyze the bug",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make analyze\"\n      evidence:\n        claim: \"Analyze gate passed\"\n        level: behaviorally_checked\n        checker: \"make analyze\"\n        trust_boundary: \"Analysis wave only\"",
		},
		{
			name:          "implement",
			promptContent: "Implement the bugfix",
			maxTurns:      5,
			gate:          "      type: command\n      run: \"make implement\"\n      evidence:\n        claim: \"Implement gate passed\"\n        level: mechanically_checked\n        checker: \"make implement\"\n        trust_boundary: \"Implementation wave only\"",
		},
		{
			name:          "finalize",
			promptContent: "Finalize the work",
			maxTurns:      3,
			dependsOn:     []string{"analyze", "implement"},
		},
	})

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the bug":      []byte("analysis output"),
			"Implement the bugfix": []byte("implementation output"),
			"Finalize the work":    []byte("final output"),
		},
		gateCallResults: []gateCallResult{
			{output: []byte("analyze gate ok"), err: nil},
			{output: []byte("implement gate ok"), err: nil},
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	r.Reporter = &reporter.Reporter{Runner: cmdRunner, Repo: "owner/repo"}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	summary := loadSummary(t, cfg.StateDir, "issue-42")
	if summary.State != "completed" {
		t.Fatalf("State = %q, want completed", summary.State)
	}
	if len(summary.Phases) != 3 {
		t.Fatalf("len(Phases) = %d, want 3", len(summary.Phases))
	}
	if summary.EvidenceManifestPath != evidenceManifestRelativePath("issue-42") {
		t.Fatalf("EvidenceManifestPath = %q, want %q", summary.EvidenceManifestPath, evidenceManifestRelativePath("issue-42"))
	}

	manifest, err := evidence.LoadManifest(cfg.StateDir, "issue-42")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(manifest.Claims) != 2 {
		t.Fatalf("len(Claims) = %d, want 2", len(manifest.Claims))
	}
	if got := manifest.Claims[0].Phase; got != "analyze" {
		t.Fatalf("Claims[0].Phase = %q, want analyze", got)
	}
	if got := manifest.Claims[1].Phase; got != "implement" {
		t.Fatalf("Claims[1].Phase = %q, want implement", got)
	}

	if !strings.Contains(cmdRunner.lastBody, "### Verification evidence") {
		t.Fatalf("expected evidence section in completion comment, got: %s", cmdRunner.lastBody)
	}
	if !strings.Contains(cmdRunner.lastBody, "Analyze gate passed") {
		t.Fatalf("expected analyze claim in completion comment, got: %s", cmdRunner.lastBody)
	}
}

func TestDrainWorkflowWithoutGateOmitsEvidenceFromCompletionComment(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, 1)
	cfg.StateDir = filepath.Join(dir, ".xylem")

	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	_, _ = q.Enqueue(makeVessel(7, "artifact-no-evidence"))

	writeWorkflowFile(t, dir, "artifact-no-evidence", []testPhase{
		{name: "analyze", promptContent: "Analyze the issue", maxTurns: 5},
		{name: "implement", promptContent: "Implement the fix", maxTurns: 5},
	})

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmdRunner := &mockCmdRunner{
		phaseOutputs: map[string][]byte{
			"Analyze the issue": []byte("analysis output"),
			"Implement the fix": []byte("implementation output"),
		},
	}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{
		"github-issue": makeGitHubSource(),
	}
	r.Reporter = &reporter.Reporter{Runner: cmdRunner, Repo: "owner/repo"}

	result, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", result.Completed)
	}

	summary := loadSummary(t, cfg.StateDir, "issue-7")
	if summary.State != "completed" {
		t.Fatalf("State = %q, want completed", summary.State)
	}
	if len(summary.Phases) != 2 {
		t.Fatalf("len(Phases) = %d, want 2", len(summary.Phases))
	}
	if summary.EvidenceManifestPath != "" {
		t.Fatalf("EvidenceManifestPath = %q, want empty string", summary.EvidenceManifestPath)
	}

	manifestPath := filepath.Join(cfg.StateDir, "phases", "issue-7", "evidence-manifest.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("expected no evidence manifest, got err=%v", err)
	}

	if !strings.Contains(cmdRunner.lastBody, "**xylem — all phases completed**") {
		t.Fatalf("expected completion header in comment, got: %s", cmdRunner.lastBody)
	}
	if strings.Contains(cmdRunner.lastBody, "### Verification evidence") {
		t.Fatalf("expected no evidence section in completion comment, got: %s", cmdRunner.lastBody)
	}
}

func loadSummary(t *testing.T, stateDir, vesselID string) VesselSummary {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(stateDir, "phases", vesselID, summaryFileName))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}

	var summary VesselSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}

	return summary
}
