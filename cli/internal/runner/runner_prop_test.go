package runner

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/surface"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	"pgregory.net/rapid"
)

func TestProp_BudgetEnforcementNeverLeaksAcrossVessels(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		outputLen := rapid.IntRange(20, 800).Draw(t, "outputLen")
		output := strings.Repeat("o", outputLen)
		phaseDef := workflow.Phase{Name: "implement"}
		model := "claude-sonnet-4"
		prompt := "Implement issue"
		individualTokens := cost.EstimateTokens(prompt) + cost.EstimateTokens(output)

		dir, err := os.MkdirTemp("", "runner-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		cfg := makeTestConfig(dir, 1)
		setPricedModel(cfg)
		setBudget(cfg, 10.0, individualTokens+1)

		vrsA := newVesselRunState(cfg, queue.Vessel{ID: "issue-1", Source: "manual", Workflow: "prop"}, time.Now().UTC())
		if _, _, _ = vrsA.recordPhaseTokens(phaseDef, model, prompt, output, time.Now().UTC()); vrsA.costTracker.BudgetExceeded() {
			t.Fatal("vrsA exceeded budget unexpectedly")
		}

		vrsB := newVesselRunState(cfg, queue.Vessel{ID: "issue-2", Source: "manual", Workflow: "prop"}, time.Now().UTC())
		inputTokens, outputTokens, costUSDEst := vrsB.recordPhaseTokens(phaseDef, model, prompt, output, time.Now().UTC())
		if vrsB.costTracker.BudgetExceeded() {
			t.Fatal("vrsB exceeded budget due to leaked state")
		}
		vrsB.addPhase(vrsB.phaseSummary(cfg, nil, nil, phaseDef, "", inputTokens, outputTokens, costUSDEst, time.Second, "completed", nil, ""))

		summaryB := vrsB.buildSummary("completed", time.Now().UTC())
		if summaryB.BudgetExceeded {
			t.Fatal("summaryB.BudgetExceeded = true, want false")
		}
		if want := inputTokens + outputTokens; summaryB.TotalTokensEst != want {
			t.Fatalf("summaryB.TotalTokensEst = %d, want %d", summaryB.TotalTokensEst, want)
		}
	})
}

func TestProp_PromptOnlyCostAlwaysNonNegative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prompt := rapid.StringMatching(`[a-zA-Z0-9 ]{1,120}`).Draw(t, "prompt")
		output := rapid.StringMatching(`[a-zA-Z0-9 ]{1,240}`).Draw(t, "output")

		cfg := &config.Config{}
		setPricedModel(cfg)

		vrs := newVesselRunState(cfg, queue.Vessel{
			ID:     "prompt-1",
			Source: "manual",
			Prompt: prompt,
		}, time.Now().UTC())
		vrs.recordPromptOnlyUsage("claude-sonnet-4", prompt, output, time.Now().UTC())

		summary := vrs.buildSummary("completed", time.Now().UTC())
		if summary.TotalCostUSDEst < 0 {
			t.Fatalf("summary.TotalCostUSDEst = %f, want >= 0", summary.TotalCostUSDEst)
		}
	})
}

func TestProp_BudgetExceededIsMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		records := rapid.SliceOfN(rapid.Float64Range(0.0, 1.0), 1, 20).Draw(t, "costs")
		tracker := cost.NewTracker(&cost.Budget{CostLimitUSD: 1.0})
		exceeded := false

		for i, recordCost := range records {
			err := tracker.Record(cost.UsageRecord{
				MissionID:    "mission-1",
				AgentRole:    cost.RoleGenerator,
				Purpose:      cost.PurposeReasoning,
				Model:        "claude-sonnet-4",
				InputTokens:  1,
				OutputTokens: 1,
				CostUSD:      recordCost,
				Timestamp:    time.Unix(int64(i+1), 0).UTC(),
			})
			if err != nil {
				t.Fatalf("Record() error = %v", err)
			}

			if tracker.BudgetExceeded() {
				exceeded = true
			}
			if exceeded && !tracker.BudgetExceeded() {
				t.Fatal("BudgetExceeded reverted to false")
			}
		}
	})
}

func TestProp_FormatViolationsIncludesEveryViolation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		count := rapid.IntRange(0, 8).Draw(t, "count")
		violations := make([]surface.Violation, 0, count)
		wantParts := make([]string, 0, count)
		for i := range count {
			violation := surface.Violation{
				Path:   rapid.StringMatching(`[a-z0-9./_-]{1,32}`).Draw(t, "path"+string(rune('a'+i))),
				Before: rapid.StringMatching(`(absent|deleted|[0-9a-f]{1,16})`).Draw(t, "before"+string(rune('a'+i))),
				After:  rapid.StringMatching(`(absent|deleted|[0-9a-f]{1,16})`).Draw(t, "after"+string(rune('a'+i))),
			}
			violations = append(violations, violation)
			wantParts = append(wantParts, fmt.Sprintf("%s (before: %s, after: %s)", violation.Path, violation.Before, violation.After))
		}

		formatted := formatViolations(violations)
		if count == 0 {
			if formatted != "" {
				t.Fatalf("formatViolations(nil) = %q, want empty string", formatted)
			}
			return
		}

		want := strings.Join(wantParts, "; ")
		if formatted != want {
			t.Fatalf("formatViolations() = %q, want %q", formatted, want)
		}
	})
}
