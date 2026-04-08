package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"pgregory.net/rapid"
)

func TestProp_VesselRunStateBuildSummaryTotalsMatchAccumulatedPhases(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		phaseCount := rapid.IntRange(0, 20).Draw(t, "phaseCount")
		startedAt := time.Unix(1_700_000_000, 0).UTC()
		vrs := newVesselRunState(nil, queue.Vessel{
			ID:       "vessel-prop",
			Source:   "manual",
			Workflow: "fix-bug",
		}, startedAt)

		wantInput := 0
		wantOutput := 0
		wantCost := 0.0
		for i := range phaseCount {
			input := rapid.IntRange(0, 5_000).Draw(t, "input")
			output := rapid.IntRange(0, 5_000).Draw(t, "output")
			costUSDEst := rapid.Float64Range(0, 5).Draw(t, "cost")

			vrs.addPhase(PhaseSummary{
				Name:            "phase-" + strconv.Itoa(i),
				Status:          "completed",
				InputTokensEst:  input,
				OutputTokensEst: output,
				CostUSDEst:      costUSDEst,
			})
			wantInput += input
			wantOutput += output
			wantCost += costUSDEst
		}

		summary := vrs.buildSummary("completed", startedAt.Add(5*time.Second))
		if summary.TotalInputTokensEst != wantInput {
			t.Fatalf("TotalInputTokensEst = %d, want %d", summary.TotalInputTokensEst, wantInput)
		}
		if summary.TotalOutputTokensEst != wantOutput {
			t.Fatalf("TotalOutputTokensEst = %d, want %d", summary.TotalOutputTokensEst, wantOutput)
		}
		if summary.TotalTokensEst != wantInput+wantOutput {
			t.Fatalf("TotalTokensEst = %d, want %d", summary.TotalTokensEst, wantInput+wantOutput)
		}
		if summary.TotalCostUSDEst != wantCost {
			t.Fatalf("TotalCostUSDEst = %v, want %v", summary.TotalCostUSDEst, wantCost)
		}
		if len(summary.Phases) != phaseCount {
			t.Fatalf("len(summary.Phases) = %d, want %d", len(summary.Phases), phaseCount)
		}
	})
}

func TestProp_SaveVesselSummaryNeverWritesNullPhases(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		stateDir, err := os.MkdirTemp("", "summary-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(stateDir)
		vesselID := rapid.StringMatching(`[A-Za-z0-9_-]{1,24}`).Draw(t, "vesselID")

		summary := &VesselSummary{
			VesselID: vesselID,
			Source:   "manual",
			State:    "completed",
		}
		if rapid.Bool().Draw(t, "withEmptySlice") {
			summary.Phases = []PhaseSummary{}
		}

		if err := SaveVesselSummary(stateDir, summary); err != nil {
			t.Fatalf("SaveVesselSummary() error = %v", err)
		}

		data, err := os.ReadFile(filepath.Join(stateDir, "phases", vesselID, summaryFileName))
		if err != nil {
			t.Fatalf("read summary: %v", err)
		}
		if strings.Contains(string(data), `"phases": null`) {
			t.Fatalf("summary.json = %s, want phases array instead of null", string(data))
		}

		var got VesselSummary
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		if got.Phases == nil {
			t.Fatal("Phases = nil, want empty slice after round trip")
		}
	})
}
