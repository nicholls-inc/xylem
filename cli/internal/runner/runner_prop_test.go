package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
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

func TestProp_PromptOnlyUsageAccumulatesEstimatedTotals(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prompt := rapid.StringMatching(`[a-zA-Z0-9 ]{0,120}`).Draw(t, "prompt")
		output := rapid.StringMatching(`[a-zA-Z0-9 ]{0,240}`).Draw(t, "output")

		cfg := &config.Config{}
		setPricedModel(cfg)

		vrs := newVesselRunState(cfg, queue.Vessel{
			ID:     "prompt-1",
			Source: "manual",
			Prompt: prompt,
		}, time.Now().UTC())
		inputTokens, outputTokens, costUSDEst := vrs.recordPromptOnlyUsage("claude-sonnet-4", prompt, output, time.Now().UTC())

		summary := vrs.buildSummary("completed", time.Now().UTC())
		wantInputTokens := cost.EstimateTokens(prompt)
		if inputTokens != wantInputTokens {
			t.Fatalf("inputTokens = %d, want %d", inputTokens, wantInputTokens)
		}
		wantOutputTokens := cost.EstimateTokens(output)
		if outputTokens != wantOutputTokens {
			t.Fatalf("outputTokens = %d, want %d", outputTokens, wantOutputTokens)
		}
		wantCostUSDEst := cost.EstimateCost(wantInputTokens, wantOutputTokens, cost.LookupPricing("claude-sonnet-4"))
		if costUSDEst != wantCostUSDEst {
			t.Fatalf("costUSDEst = %f, want %f", costUSDEst, wantCostUSDEst)
		}
		if summary.TotalInputTokensEst != inputTokens {
			t.Fatalf("summary.TotalInputTokensEst = %d, want %d", summary.TotalInputTokensEst, inputTokens)
		}
		if summary.TotalOutputTokensEst != outputTokens {
			t.Fatalf("summary.TotalOutputTokensEst = %d, want %d", summary.TotalOutputTokensEst, outputTokens)
		}
		if summary.TotalTokensEst != inputTokens+outputTokens {
			t.Fatalf("summary.TotalTokensEst = %d, want %d", summary.TotalTokensEst, inputTokens+outputTokens)
		}
		if summary.TotalCostUSDEst != costUSDEst {
			t.Fatalf("summary.TotalCostUSDEst = %f, want %f", summary.TotalCostUSDEst, costUSDEst)
		}
		if len(summary.Phases) != 0 {
			t.Fatalf("len(summary.Phases) = %d, want 0", len(summary.Phases))
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

func TestProp_RestoreMissingProtectedSurfacesFromRootRepairsMissingFiles(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sourceRoot, err := os.MkdirTemp("", "runner-surface-source-*")
		if err != nil {
			t.Fatalf("MkdirTemp(sourceRoot) error = %v", err)
		}
		defer os.RemoveAll(sourceRoot)

		worktreePath, err := os.MkdirTemp("", "runner-surface-worktree-*")
		if err != nil {
			t.Fatalf("MkdirTemp(worktreePath) error = %v", err)
		}
		defer os.RemoveAll(worktreePath)

		files := map[string]string{
			".xylem.yml":                        rapid.StringMatching(`[a-zA-Z0-9 _:\n-]{1,48}`).Draw(t, "config"),
			".xylem/HARNESS.md":                 rapid.StringMatching(`[a-zA-Z0-9 _:\n-]{1,48}`).Draw(t, "harness"),
			".xylem/workflows/fix-bug.yaml":     rapid.StringMatching(`[a-zA-Z0-9 _:\n-]{1,48}`).Draw(t, "workflow"),
			".xylem/prompts/fix-bug/analyze.md": rapid.StringMatching(`[a-zA-Z0-9 _:\n-]{1,48}`).Draw(t, "prompt"),
		}
		patterns := []string{
			".xylem.yml",
			".xylem/HARNESS.md",
			".xylem/workflows/*.yaml",
			".xylem/prompts/*/*.md",
		}

		expectedRestored := 0
		for path, content := range files {
			srcPath := filepath.Join(sourceRoot, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
				t.Fatalf("MkdirAll(%s) error = %v", srcPath, err)
			}
			if err := os.WriteFile(srcPath, []byte(content), 0o644); err != nil {
				t.Fatalf("WriteFile(%s) error = %v", srcPath, err)
			}

			if rapid.Bool().Draw(t, "missing-"+path) {
				expectedRestored++
				continue
			}

			dstPath := filepath.Join(worktreePath, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				t.Fatalf("MkdirAll(%s) error = %v", dstPath, err)
			}
			if err := os.WriteFile(dstPath, []byte(content), 0o644); err != nil {
				t.Fatalf("WriteFile(%s) error = %v", dstPath, err)
			}
		}

		restored, err := restoreMissingProtectedSurfacesFromRoot(worktreePath, sourceRoot, patterns)
		if err != nil {
			t.Fatalf("restoreMissingProtectedSurfacesFromRoot() error = %v", err)
		}
		if restored != expectedRestored {
			t.Fatalf("restored = %d, want %d", restored, expectedRestored)
		}

		sourceSnapshot, err := surface.TakeSnapshot(sourceRoot, patterns)
		if err != nil {
			t.Fatalf("TakeSnapshot(sourceRoot) error = %v", err)
		}
		worktreeSnapshot, err := surface.TakeSnapshot(worktreePath, patterns)
		if err != nil {
			t.Fatalf("TakeSnapshot(worktreePath) error = %v", err)
		}
		if diff := surface.Compare(sourceSnapshot, worktreeSnapshot); len(diff) != 0 {
			t.Fatalf("restored snapshot diff = %+v, want none", diff)
		}
	})
}

func TestProp_PhasePolicyIntentsStayUniqueAndClassifyHighRiskActions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "runner-policy-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		cfg := makeTestConfig(dir, 1)
		fallbackRepo := rapid.StringMatching(`[a-z0-9][a-z0-9-]{0,11}/[a-z0-9][a-z0-9-]{0,11}`).Draw(t, "fallbackRepo")
		r := New(cfg, nil, nil, nil)
		r.Sources = map[string]source.Source{
			"github-issue": &source.GitHub{Repo: fallbackRepo},
		}

		isCommand := rapid.Bool().Draw(t, "isCommand")
		commitRepeats := rapid.IntRange(0, 3).Draw(t, "commitRepeats")
		pushRepeats := rapid.IntRange(0, 3).Draw(t, "pushRepeats")
		prRepeats := rapid.IntRange(0, 3).Draw(t, "prRepeats")
		explicitRepo := rapid.Bool().Draw(t, "explicitRepo")
		pushBranch := rapid.StringMatching(`[a-z0-9][a-z0-9-]{0,15}`).Draw(t, "pushBranch")
		prRepo := rapid.StringMatching(`[a-z0-9][a-z0-9-]{0,11}/[a-z0-9][a-z0-9-]{0,11}`).Draw(t, "prRepo")

		var parts []string
		for range commitRepeats {
			parts = append(parts, "commit all changes")
		}
		for range pushRepeats {
			parts = append(parts, "git push origin "+pushBranch)
		}
		for range prRepeats {
			prCommand := "gh pr create"
			if explicitRepo {
				prCommand += " --repo " + prRepo
			}
			parts = append(parts, prCommand)
		}
		classificationText := strings.Join(parts, " && ")

		p := workflow.Phase{Name: "apply"}
		renderedPrompt := classificationText
		renderedCommand := ""
		wantBaseAction := "phase_execute"
		if isCommand {
			p.Type = "command"
			renderedCommand = classificationText
			renderedPrompt = ""
			wantBaseAction = "external_command"
		}

		vessel := queue.Vessel{
			ID:       "issue-1",
			Source:   "github-issue",
			Workflow: "fix-bug",
		}

		intents := r.phasePolicyIntents(vessel, p, renderedCommand, renderedPrompt)
		counts := make(map[string]int)
		resources := make(map[string]string)
		for _, intent := range intents {
			key := intent.Action + "\x00" + intent.Resource
			counts[key]++
			if counts[key] > 1 {
				t.Fatalf("duplicate intent generated for %q/%q: %+v", intent.Action, intent.Resource, intents)
			}
			resources[intent.Action] = intent.Resource
		}

		if counts[wantBaseAction+"\x00"+p.Name] != 1 {
			t.Fatalf("base intent count = %d, want 1 (intents=%+v)", counts[wantBaseAction+"\x00"+p.Name], intents)
		}

		if commitRepeats == 0 {
			if _, ok := resources["git_commit"]; ok {
				t.Fatalf("unexpected git_commit intent in %+v", intents)
			}
		} else if resources["git_commit"] != "*" {
			t.Fatalf("git_commit resource = %q, want *", resources["git_commit"])
		}

		if pushRepeats == 0 {
			if _, ok := resources["git_push"]; ok {
				t.Fatalf("unexpected git_push intent in %+v", intents)
			}
		} else if resources["git_push"] != pushBranch {
			t.Fatalf("git_push resource = %q, want %q", resources["git_push"], pushBranch)
		}

		if prRepeats == 0 {
			if _, ok := resources["pr_create"]; ok {
				t.Fatalf("unexpected pr_create intent in %+v", intents)
			}
			return
		}

		wantPRRepo := fallbackRepo
		if explicitRepo {
			wantPRRepo = prRepo
		}
		if resources["pr_create"] != wantPRRepo {
			t.Fatalf("pr_create resource = %q, want %q", resources["pr_create"], wantPRRepo)
		}
	})
}
