package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type workflowHealthTestRunner struct {
	calls        [][]string
	issueList    []contextWeightIssue
	createCount  int
	createTitles []string
	createBodies []string
}

func (r *workflowHealthTestRunner) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if name != "gh" || len(args) < 2 {
		return []byte{}, nil
	}
	switch {
	case args[0] == "issue" && args[1] == "list":
		return json.Marshal(r.issueList)
	case args[0] == "issue" && args[1] == "create":
		r.createCount++
		r.createTitles = append(r.createTitles, valueAfterFlag(args, "--title"))
		r.createBodies = append(r.createBodies, valueAfterFlag(args, "--body"))
		return []byte("https://github.com/owner/repo/issues/" + strconv.Itoa(90+r.createCount)), nil
	default:
		return []byte{}, nil
	}
}

func TestSmoke_S1_CollectAnalyzeReportAndEscalateRepeatedFailures(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 12, 0, 0, 0, time.UTC)
	failedGate := false

	for i := range 3 {
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:  "build-failed-" + string(rune('a'+i)),
			source:    "github-issue",
			workflow:  "build",
			state:     "failed",
			startedAt: now.Add(-time.Duration(24-i) * time.Hour),
			endedAt:   now.Add(-time.Duration(24-i)*time.Hour + 5*time.Minute),
			phases: []runner.PhaseSummary{{
				Name:       "implement",
				Type:       "prompt",
				Status:     "failed",
				GateType:   "command",
				GatePassed: &failedGate,
				CostUSDEst: 0.40,
			}},
			totalCost: 0.40,
			recoveryArtifact: recovery.Build(recovery.Input{
				VesselID:    "build-failed-" + string(rune('a'+i)),
				Source:      "github-issue",
				Workflow:    "build",
				State:       queue.StateFailed,
				FailedPhase: "implement",
				Error:       "tests failed",
				Meta: map[string]string{
					recovery.MetaRetryOutcome: "suppressed",
				},
				CreatedAt: now,
			}),
		})
	}

	writeRunArtifacts(t, stateDir, runFixture{
		vesselID:  "build-previous",
		source:    "github-issue",
		workflow:  "build",
		state:     "completed",
		startedAt: now.Add(-10 * 24 * time.Hour),
		endedAt:   now.Add(-10*24*time.Hour + 4*time.Minute),
		phases: []runner.PhaseSummary{{
			Name:       "implement",
			Type:       "prompt",
			Status:     "completed",
			CostUSDEst: 0.10,
		}},
		totalCost: 0.10,
	})
	writeRunArtifacts(t, stateDir, runFixture{
		vesselID:  "stable-run",
		source:    "github-issue",
		workflow:  "stable",
		state:     "completed",
		startedAt: now.Add(-2 * time.Hour),
		endedAt:   now.Add(-90 * time.Minute),
		phases: []runner.PhaseSummary{{
			Name:       "implement",
			Type:       "prompt",
			Status:     "completed",
			CostUSDEst: 0.05,
		}},
		totalCost: 0.05,
	})

	q := queue.New(filepath.Join(stateDir, "queue.jsonl"))
	for _, vessel := range []queue.Vessel{
		{ID: "build-failed-a", Source: "github-issue", Workflow: "build", State: queue.StateFailed, CreatedAt: now.Add(-24 * time.Hour)},
		{ID: "stable-run", Source: "github-issue", Workflow: "stable", State: queue.StateCompleted, CreatedAt: now.Add(-2 * time.Hour)},
	} {
		_, err := q.Enqueue(vessel)
		require.NoError(t, err)
	}

	result, err := GenerateWorkflowHealthReport(stateDir, WorkflowHealthOptions{
		LookbackRuns:        20,
		Window:              7 * 24 * time.Hour,
		OutputDir:           "reviews",
		Now:                 now,
		EscalationThreshold: 2,
	})
	require.NoError(t, err)

	assert.Equal(t, 4, result.Report.ReviewedRuns)
	assert.Equal(t, 2, result.Report.CurrentVessels)
	assert.Equal(t, 1, result.Report.Fleet.Healthy)
	assert.Equal(t, 1, result.Report.Fleet.Unhealthy)

	require.NotEmpty(t, result.Report.AnomalyCounts)
	assert.Equal(t, "gate_failed", result.Report.AnomalyCounts[0].Name)
	assert.Equal(t, 3, result.Report.AnomalyCounts[0].Count)

	require.NotEmpty(t, result.Report.RetryOutcomes)
	assert.Equal(t, "suppressed", result.Report.RetryOutcomes[0].Name)
	assert.Equal(t, 3, result.Report.RetryOutcomes[0].Count)

	require.NotEmpty(t, result.Report.Workflows)
	assert.Equal(t, "build", result.Report.Workflows[0].Workflow)
	assert.Equal(t, 3, result.Report.Workflows[0].Failed)
	assert.Greater(t, result.Report.Workflows[0].CostDeltaUSDEst, 0.0)

	require.Len(t, result.Report.EscalationFindings, 1)
	finding := result.Report.EscalationFindings[0]
	assert.Equal(t, "build", finding.Workflow)
	assert.Equal(t, 3, finding.Count)
	assert.Contains(t, finding.Pattern, "gate_failed")
	assert.Contains(t, finding.Pattern, "implement")
	assert.NotEmpty(t, finding.Recommendations)

	assert.Contains(t, result.Markdown, "## Escalation candidates")
	assert.Contains(t, result.Markdown, "build")
	_, err = os.Stat(filepath.Join(stateDir, "reviews", workflowHealthReportJSONName))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(stateDir, "reviews", workflowHealthReportMarkdownName))
	require.NoError(t, err)
}

func TestSmoke_S2_PublishWeeklyIssuesOnlyOncePerWeek(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 12, 0, 0, 0, time.UTC)
	failedGate := false

	for i := range 2 {
		writeRunArtifacts(t, stateDir, runFixture{
			vesselID:  "build-repeat-" + string(rune('a'+i)),
			source:    "github-issue",
			workflow:  "build",
			state:     "failed",
			startedAt: now.Add(-time.Duration(i+1) * time.Hour),
			endedAt:   now.Add(-time.Duration(i+1)*time.Hour + 2*time.Minute),
			phases: []runner.PhaseSummary{{
				Name:       "implement",
				Type:       "prompt",
				Status:     "failed",
				GateType:   "command",
				GatePassed: &failedGate,
			}},
		})
	}

	runner := &workflowHealthTestRunner{}
	first, err := RunWorkflowHealthReport(context.Background(), stateDir, "owner/repo", runner, WorkflowHealthOptions{
		LookbackRuns:        10,
		OutputDir:           "reviews",
		Now:                 now,
		EscalationThreshold: 2,
	})
	require.NoError(t, err)
	require.Len(t, first.Published, 2)
	assert.True(t, first.Published[0].Created)
	assert.True(t, first.Published[1].Created)
	assert.Equal(t, 2, runner.createCount)
	assert.Equal(t, workflowHealthReportTitle, runner.createTitles[0])
	assert.Equal(t, workflowHealthEscalationTitle, runner.createTitles[1])
	assert.Contains(t, runner.createBodies[0], workflowHealthReportMarkerPrefix+workflowHealthWeekKey(now))
	assert.Contains(t, runner.createBodies[1], workflowHealthEscalationMarkerPrefix+workflowHealthWeekKey(now))

	second, err := RunWorkflowHealthReport(context.Background(), stateDir, "owner/repo", runner, WorkflowHealthOptions{
		LookbackRuns:        10,
		OutputDir:           "reviews",
		Now:                 now.Add(2 * time.Hour),
		EscalationThreshold: 2,
	})
	require.NoError(t, err)
	require.Len(t, second.Published, 2)
	assert.False(t, second.Published[0].Created)
	assert.False(t, second.Published[1].Created)
	assert.Equal(t, 2, runner.createCount)

	stateJSON, err := os.ReadFile(filepath.Join(stateDir, "reviews", workflowHealthIssueStateName))
	require.NoError(t, err)
	assert.Contains(t, string(stateJSON), workflowHealthWeekKey(now))
}

func TestSmoke_S3_WriteReportWhenWindowHasNoRecentRuns(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.April, 10, 12, 0, 0, 0, time.UTC)

	writeRunArtifacts(t, stateDir, runFixture{
		vesselID:  "old-run",
		source:    "github-issue",
		workflow:  "build",
		state:     "completed",
		startedAt: now.Add(-20 * 24 * time.Hour),
		endedAt:   now.Add(-20*24*time.Hour + time.Minute),
	})

	result, err := GenerateWorkflowHealthReport(stateDir, WorkflowHealthOptions{
		LookbackRuns: 10,
		Window:       7 * 24 * time.Hour,
		OutputDir:    "reviews",
		Now:          now,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, result.Report.ReviewedRuns)
	assert.Empty(t, result.Report.EscalationFindings)
	assert.Contains(t, result.Markdown, "No completed or failed vessel runs landed in the reporting window")
	assert.NotContains(t, strings.Join(result.Report.Warnings, "\n"), "error")
}
