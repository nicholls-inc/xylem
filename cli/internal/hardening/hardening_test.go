package hardening

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRunner struct {
	calls   [][]string
	outputs map[string][]byte
}

func (m *mockRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	m.calls = append(m.calls, call)
	if m.outputs == nil {
		return nil, nil
	}
	return m.outputs[strings.Join(call, " ")], nil
}

func TestGenerateInventoryClassifiesPromptAndCommandPhases(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "prompts", "hardening-audit"), 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "prompts", "hardening-audit", "rank.md"), []byte(
		"Read `.xylem/state/hardening-audit/scores.json` and write exactly one JSON file.\nIf the score is low, otherwise explain why.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "prompts", "hardening-audit", "review.md"), []byte(
		"Review the repository and explain whether the maintenance direction still feels right.\n"), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "workflows", "hardening-audit.yaml"), []byte(`
name: hardening-audit
class: harness-maintenance
phases:
  - name: inventory
    type: command
    run: echo inventory
  - name: rank
    prompt_file: .xylem/prompts/hardening-audit/rank.md
    max_turns: 20
  - name: review
    prompt_file: .xylem/prompts/hardening-audit/review.md
    max_turns: 20
`), 0o644))

	inventory, err := GenerateInventory(repoRoot, ".xylem/workflows", time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, inventory.Workflows, 1)
	require.Len(t, inventory.Workflows[0].Phases, 3)

	commandPhase := inventory.Workflows[0].Phases[0]
	assert.Equal(t, ClassificationDeterministic, commandPhase.Classification)

	mixedPhase := inventory.Workflows[0].Phases[1]
	assert.Equal(t, ClassificationMixed, mixedPhase.Classification)
	assert.NotEmpty(t, mixedPhase.StructuredSignals)
	assert.NotZero(t, mixedPhase.HardRuleCount)

	fuzzyPhase := inventory.Workflows[0].Phases[2]
	assert.Equal(t, ClassificationFuzzy, fuzzyPhase.Classification)
	assert.Empty(t, fuzzyPhase.StructuredSignals)
	assert.Empty(t, fuzzyPhase.PatternSignals)
}

func TestGenerateInventoryFailsWhenWorkflowValidationFails(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".xylem", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".xylem", "workflows", "broken.yaml"), []byte(`
name: broken
phases:
  - name: analyze
    prompt_file: .xylem/prompts/missing.md
    max_turns: 20
`), 0o644))

	_, err := GenerateInventory(repoRoot, ".xylem/workflows", time.Time{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt_file not found")
}

func TestScoreInventoryUsesRecentFailuresAndRanksTopCandidates(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := filepath.Join(repoRoot, ".xylem")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "phases", "issue-1"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "phases", "issue-2"), 0o755))

	writeSummary := func(vesselID string, endedAt time.Time) {
		t.Helper()
		summary := &runner.VesselSummary{
			VesselID:   vesselID,
			Workflow:   "hardening-audit",
			State:      "failed",
			StartedAt:  endedAt.Add(-5 * time.Minute),
			EndedAt:    endedAt,
			DurationMS: 300000,
			Phases: []runner.PhaseSummary{
				{Name: "rank", Type: "prompt", Status: "failed"},
			},
		}
		require.NoError(t, runner.SaveVesselSummary(stateDir, summary))
	}
	writeSummary("issue-1", time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC))
	writeSummary("issue-2", time.Date(2026, time.April, 7, 10, 0, 0, 0, time.UTC))

	inventory := &Inventory{
		Version:     InventoryVersion,
		GeneratedAt: "2026-04-10T00:00:00Z",
		RepoRoot:    repoRoot,
		WorkflowDir: ".xylem/workflows",
		Workflows: []WorkflowInventory{{
			Name: "hardening-audit",
			Path: ".xylem/workflows/hardening-audit.yaml",
			Phases: []PhaseInventory{
				{
					ID:             "hardening-audit/rank",
					DisplayName:    "hardening-audit/rank",
					Workflow:       "hardening-audit",
					WorkflowPath:   ".xylem/workflows/hardening-audit.yaml",
					Phase:          "rank",
					Type:           "prompt",
					PromptPath:     ".xylem/prompts/hardening-audit/rank.md",
					PromptExcerpt:  "Write exactly one JSON file and inspect git labels.",
					PromptLines:    12,
					Classification: ClassificationMixed,
					StructuredSignals: []string{
						"json",
					},
					PatternSignals: []string{
						"label",
						"git",
					},
					HardRuleCount: 4,
				},
				{
					ID:             "hardening-audit/review",
					DisplayName:    "hardening-audit/review",
					Workflow:       "hardening-audit",
					WorkflowPath:   ".xylem/workflows/hardening-audit.yaml",
					Phase:          "review",
					Type:           "prompt",
					PromptPath:     ".xylem/prompts/hardening-audit/review.md",
					PromptExcerpt:  "Review overall quality.",
					PromptLines:    20,
					Classification: ClassificationFuzzy,
				},
			},
		}},
	}

	report, err := ScoreInventory(inventory, ".xylem", time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, report.Candidates, 2)
	require.NotEmpty(t, report.TopCandidates)
	assert.Equal(t, "hardening-audit/rank", report.TopCandidates[0].ID)
	assert.Equal(t, 2, report.TopCandidates[0].Criteria.FailureCount30d)
	assert.True(t, report.TopCandidates[0].Criteria.GoReplacementFeasible)
	assert.Greater(t, report.TopCandidates[0].Score, report.Candidates[1].Score)
}

func TestFileIssuesCreatesAndDedupes(t *testing.T) {
	searchOut, err := json.Marshal([]issueSummary{{
		Number: 7,
		Title:  "[harden] hardening-audit/review",
		URL:    "https://github.com/owner/repo/issues/7",
	}})
	require.NoError(t, err)

	runner := &mockRunner{
		outputs: map[string][]byte{
			"gh search issues --repo owner/repo --state open --json number,title,url --limit 100 --search [harden]":                          searchOut,
			"gh issue create --repo owner/repo --title [harden] hardening-audit/rank --body body --label enhancement --label ready-for-work": []byte("https://github.com/owner/repo/issues/9\n"),
		},
	}

	result, err := FileIssues(context.Background(), runner, "owner/repo", []Proposal{
		{PhaseID: "hardening-audit/rank", Title: "[harden] hardening-audit/rank", Body: "body", CLISignature: "xylem harden score", PackageLocation: "cli/internal/hardening", EstimatedComplexity: "medium", TestCases: []string{"writes scores"}},
		{PhaseID: "hardening-audit/review", Title: "[harden] hardening-audit/review", Body: "body", CLISignature: "xylem harden inventory", PackageLocation: "cli/internal/hardening", EstimatedComplexity: "small", TestCases: []string{"writes inventory"}},
	}, []string{"enhancement", "ready-for-work"})
	require.NoError(t, err)

	require.Len(t, result.Created, 1)
	assert.Equal(t, 9, result.Created[0].Number)
	require.Len(t, result.Existing, 1)
	assert.Equal(t, 7, result.Existing[0].Number)
}

func TestAppendLedgerCreatesMarkdownHistory(t *testing.T) {
	repoRoot := t.TempDir()
	err := AppendLedger(repoRoot, DefaultLedgerPath, []Proposal{{
		PhaseID:             "hardening-audit/rank",
		Title:               "[harden] hardening-audit/rank",
		CLISignature:        "xylem harden score",
		PackageLocation:     "cli/internal/hardening",
		EstimatedComplexity: "medium",
		TestCases:           []string{"writes scores.json", "counts recent failures"},
	}}, &FileResult{
		Created: []FiledIssue{{
			PhaseID: "hardening-audit/rank",
			Title:   "[harden] hardening-audit/rank",
			Number:  42,
			URL:     "https://github.com/owner/repo/issues/42",
			Created: true,
		}},
	}, time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(repoRoot, DefaultLedgerPath))
	require.NoError(t, err)
	assert.Contains(t, string(data), "# Hardening Ledger")
	assert.Contains(t, string(data), "2026-04-10T00:00:00Z")
	assert.Contains(t, string(data), "opened issue #42")
	assert.Contains(t, string(data), "xylem harden score")
}
