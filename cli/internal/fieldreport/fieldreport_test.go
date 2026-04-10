package fieldreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/recovery"
	"github.com/nicholls-inc/xylem/cli/internal/review"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeRuns(n int) []review.LoadedRun {
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	runs := make([]review.LoadedRun, n)
	for i := range runs {
		gatePassed := true
		runs[i] = review.LoadedRun{
			Summary: runner.VesselSummary{
				VesselID:             "",
				Source:               "bugs",
				Workflow:             "fix-bug",
				State:                "completed",
				StartedAt:            base.Add(time.Duration(i) * time.Hour),
				EndedAt:              base.Add(time.Duration(i)*time.Hour + 30*time.Minute),
				DurationMS:           1800000,
				TotalTokensEst:       10000 + i*1000,
				TotalCostUSDEst:      0.50 + float64(i)*0.10,
				TotalInputTokensEst:  7000 + i*700,
				TotalOutputTokensEst: 3000 + i*300,
				Phases: []runner.PhaseSummary{
					{Name: "analyze", Type: "prompt", Status: "completed", DurationMS: 900000, GatePassed: &gatePassed},
					{Name: "implement", Type: "prompt", Status: "completed", DurationMS: 900000},
				},
			},
		}
	}
	return runs
}

func writeSummaries(t *testing.T, stateDir string, runs []review.LoadedRun) {
	t.Helper()
	for i, run := range runs {
		run.Summary.VesselID = vesselID(i)
		dir := filepath.Join(stateDir, "phases", run.Summary.VesselID)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		data, err := json.MarshalIndent(run.Summary, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), data, 0o644))

		if run.Recovery != nil {
			recDir := filepath.Join(stateDir, "phases", run.Summary.VesselID)
			recData, err := json.MarshalIndent(run.Recovery, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(recDir, "failure-review.json"), recData, 0o644))
		}
	}
}

func vesselID(i int) string {
	return "vessel-" + string(rune('a'+i))
}

func TestGenerate_InsufficientData(t *testing.T) {
	stateDir := t.TempDir()
	runs := makeRuns(3)
	writeSummaries(t, stateDir, runs)

	_, err := Generate(stateDir, Options{})
	assert.ErrorIs(t, err, ErrInsufficientData)
}

func TestGenerate_BasicReport(t *testing.T) {
	stateDir := t.TempDir()
	runs := makeRuns(6)
	// Make one failed
	runs[3].Summary.State = "failed"
	runs[3].Summary.Workflow = "implement-feature"
	// Make one timed out
	runs[4].Summary.State = "timed_out"
	runs[4].Summary.BudgetExceeded = true
	writeSummaries(t, stateDir, runs)

	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	report, err := Generate(stateDir, Options{
		XylemVersion:   "abc123",
		ProfileVersion: 1,
		Now:            now,
	})
	require.NoError(t, err)
	require.NotNil(t, report)

	assert.Equal(t, SchemaVersion, report.Version)
	assert.Equal(t, now, report.GeneratedAt)
	assert.Equal(t, "abc123", report.XylemVersion)
	assert.Equal(t, 1, report.ProfileVersion)
	assert.Equal(t, 6, report.TotalVessels)
	assert.Len(t, report.ReportID, 16)

	// Fleet digest: runs[0,1,2,5]=healthy, runs[3]=failed(unhealthy), runs[4]=timed_out(unhealthy)
	assert.Equal(t, 4, report.Fleet.Healthy)
	assert.Equal(t, 0, report.Fleet.Degraded)
	assert.Equal(t, 2, report.Fleet.Unhealthy)

	// Workflow digests: runs[0,1,2,4,5]=fix-bug, runs[3]=implement-feature
	assert.Len(t, report.Workflows, 2)
	for _, wf := range report.Workflows {
		switch wf.Workflow {
		case "fix-bug":
			assert.Equal(t, 5, wf.Total)
			assert.Equal(t, 4, wf.Completed)
			assert.Equal(t, 0, wf.Failed)
			assert.Equal(t, 1, wf.TimedOut)
		case "implement-feature":
			assert.Equal(t, 1, wf.Total)
			assert.Equal(t, 0, wf.Completed)
			assert.Equal(t, 1, wf.Failed)
		}
	}

	// No extended fields
	assert.Empty(t, report.HashedRepoID)
	assert.Nil(t, report.FailurePatterns)
}

func TestGenerate_ExtendedMode(t *testing.T) {
	stateDir := t.TempDir()
	runs := makeRuns(6)
	runs[2].Summary.State = "failed"
	runs[3].Summary.State = "timed_out"
	writeSummaries(t, stateDir, runs)

	report, err := Generate(stateDir, Options{
		Extended: true,
		RepoSlug: "myorg/myrepo",
		Now:      time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	assert.Len(t, report.HashedRepoID, 16)
	assert.NotEqual(t, "myorg/myrepo", report.HashedRepoID)
	assert.NotEmpty(t, report.FailurePatterns)
}

func TestGenerate_PrivacyProjection(t *testing.T) {
	stateDir := t.TempDir()
	runs := makeRuns(6)
	// Add identifiable data to summaries
	for i := range runs {
		runs[i].Summary.Ref = "issue-42-fix-auth-bug"
		runs[i].Summary.Source = "my-org-bugs"
	}
	writeSummaries(t, stateDir, runs)

	report, err := Generate(stateDir, Options{
		Now: time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	data, err := json.Marshal(report)
	require.NoError(t, err)
	jsonStr := string(data)

	// These should NOT appear in the report
	assert.NotContains(t, jsonStr, "issue-42")
	assert.NotContains(t, jsonStr, "fix-auth-bug")
	assert.NotContains(t, jsonStr, "my-org-bugs")
	assert.NotContains(t, jsonStr, "vessel-")
}

func TestSave(t *testing.T) {
	stateDir := t.TempDir()
	report := &FieldReport{
		Version:      SchemaVersion,
		ReportID:     "testreport12345",
		GeneratedAt:  time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
		TotalVessels: 10,
	}

	path, err := Save(stateDir, report)
	require.NoError(t, err)
	assert.Contains(t, path, "2026-04-10.json")

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var loaded FieldReport
	require.NoError(t, json.Unmarshal(data, &loaded))
	assert.Equal(t, report.ReportID, loaded.ReportID)
	assert.Equal(t, 10, loaded.TotalVessels)
}

func TestHashRepoID_Deterministic(t *testing.T) {
	a := hashRepoID("myorg/myrepo")
	b := hashRepoID("myorg/myrepo")
	assert.Equal(t, a, b)
	assert.Len(t, a, 16)

	c := hashRepoID("other/repo")
	assert.NotEqual(t, a, c)
}

func TestComputeRecoveryDigest(t *testing.T) {
	runs := makeRuns(6)
	runs[0].Recovery = &recovery.Artifact{RecoveryClass: "transient"}
	runs[1].Recovery = &recovery.Artifact{RecoveryClass: "transient"}
	runs[2].Recovery = &recovery.Artifact{RecoveryClass: "structural"}

	digests := computeRecoveryDigest(runs)
	require.Len(t, digests, 2)
	assert.Equal(t, "transient", digests[0].Class)
	assert.Equal(t, 2, digests[0].Count)
	assert.Equal(t, "structural", digests[1].Class)
	assert.Equal(t, 1, digests[1].Count)
}

func TestStatHelpers(t *testing.T) {
	vals := []int{10, 20, 30, 40, 50}
	assert.Equal(t, 150, sumInts(vals))
	assert.Equal(t, 30, meanInt(vals))
	assert.Equal(t, 30, medianInt(vals))
	assert.Equal(t, 50, percentileInt(vals, 0.95))

	floats := []float64{1.0, 2.0, 3.0, 4.0, 5.0}
	assert.InDelta(t, 15.0, sumFloats(floats), 0.001)
	assert.InDelta(t, 3.0, meanFloat(floats), 0.001)
	assert.InDelta(t, 3.0, medianFloat(floats), 0.001)

	// Empty
	assert.Equal(t, 0, meanInt(nil))
	assert.Equal(t, 0, medianInt(nil))
	assert.Equal(t, 0, percentileInt(nil, 0.95))
}
