package runner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/surface"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
	"pgregory.net/rapid"
)

func TestProp_FilterAdditiveProtectedSurfaceViolationsDropsOnlyAdditions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		allowAdditive := rapid.Bool().Draw(t, "allowAdditive")
		count := rapid.IntRange(0, 12).Draw(t, "count")

		violations := make([]surface.Violation, 0, count)
		want := make([]surface.Violation, 0, count)
		for i := range count {
			before := rapid.SampledFrom([]string{
				"absent",
				"deleted",
				"aaaaaaaaaaaaaaaa",
				"bbbbbbbbbbbbbbbb",
			}).Draw(t, fmt.Sprintf("before-%d", i))
			violation := surface.Violation{
				Path:   fmt.Sprintf(".xylem/generated/%d.yaml", i),
				Before: before,
				After: rapid.SampledFrom([]string{
					"deleted",
					"1111111111111111",
					"2222222222222222",
				}).Draw(t, fmt.Sprintf("after-%d", i)),
			}
			violations = append(violations, violation)
			if !allowAdditive || violation.Before != "absent" {
				want = append(want, violation)
			}
		}

		got := filterAdditiveProtectedSurfaceViolations(violations, allowAdditive)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("filterAdditiveProtectedSurfaceViolations() = %#v, want %#v", got, want)
		}
	})
}

func TestProp_IssueBodyMentionsProtectedPathHonorsTokenBoundaries(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pathSuffix := rapid.StringMatching(`[a-z0-9._/-]{1,24}`).Draw(t, "pathSuffix")
		path := ".xylem/" + pathSuffix
		validBoundary := rapid.SampledFrom([]string{"", " ", "\n", "`", "(", "[", ":", "="}).Draw(t, "validBoundary")
		invalidBoundary := rapid.SampledFrom([]string{"a", "0", ".", "/", "_", "-"}).Draw(t, "invalidBoundary")

		if !issueBodyMentionsProtectedPath(validBoundary+path+validBoundary, path) {
			t.Fatalf("issueBodyMentionsProtectedPath() = false, want true for bounded path %q", path)
		}
		if issueBodyMentionsProtectedPath(invalidBoundary+path+validBoundary, path) {
			t.Fatalf("issueBodyMentionsProtectedPath() = true, want false for prefixed path %q", path)
		}
		if issueBodyMentionsProtectedPath(validBoundary+path+invalidBoundary, path) {
			t.Fatalf("issueBodyMentionsProtectedPath() = true, want false for suffixed path %q", path)
		}
	})
}

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

func TestProp_CancelledTransitionRequiresCancelledLatestState(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cancelled := rapid.Bool().Draw(t, "cancelled")
		wrapErr := rapid.Bool().Draw(t, "wrapErr")

		dir, err := os.MkdirTemp("", "runner-cancel-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		cfg := makeTestConfig(dir, 1)
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		_, err = q.Enqueue(queue.Vessel{
			ID:        "issue-1",
			Source:    "manual",
			State:     queue.StatePending,
			CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
		if cancelled {
			if err := q.Cancel("issue-1"); err != nil {
				t.Fatalf("Cancel() error = %v", err)
			}
		}

		r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{})
		transitionErr := error(queue.ErrInvalidTransition)
		if wrapErr {
			transitionErr = fmt.Errorf("persist update: %w", transitionErr)
		}

		if got := r.cancelledTransition("issue-1", transitionErr); got != cancelled {
			t.Fatalf("cancelledTransition() = %t, want %t", got, cancelled)
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

func TestProp_WorkflowSnapshotDigestMatchesSHA256(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		data := []byte(rapid.StringMatching(`[\x09\x0a\x0d\x20-\x7e]{0,256}`).Draw(t, "data"))
		sum := sha256.Sum256(data)
		want := fmt.Sprintf("sha256:%x", sum)
		if got := workflowSnapshotDigest(data); got != want {
			t.Fatalf("workflowSnapshotDigest() = %q, want %q", got, want)
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

func TestProp_ValidateIssueDataForWorkflowRequiresIssueNumberForCommandTemplates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		phaseUsesIssue := rapid.Bool().Draw(t, "phaseUsesIssue")
		gateUsesIssue := rapid.Bool().Draw(t, "gateUsesIssue")
		phaseType := rapid.SampledFrom([]string{"", "command"}).Draw(t, "phaseType")
		issueNumber := rapid.IntRange(0, 3).Draw(t, "issueNumber")

		phaseRun := "echo ready"
		if phaseUsesIssue {
			phaseRun = "gh pr merge {{.Issue.Number}}"
		}

		var gateCfg *workflow.Gate
		if rapid.Bool().Draw(t, "hasGate") {
			gateRun := "echo gate"
			if gateUsesIssue {
				gateRun = "gh pr view {{.Issue.Number}} --json mergeable"
			}
			gateCfg = &workflow.Gate{
				Type: "command",
				Run:  gateRun,
			}
		}

		p := workflow.Phase{
			Name:       "resolve",
			Type:       phaseType,
			Run:        phaseRun,
			PromptFile: ".xylem/prompts/resolve-conflicts/resolve.md",
			MaxTurns:   10,
			Gate:       gateCfg,
		}
		wf := &workflow.Workflow{Phases: []workflow.Phase{p}}
		err := validateIssueDataForWorkflow(queue.Vessel{ID: "issue-1"}, phase.IssueData{Number: issueNumber}, wf)

		wantErr := issueNumber == 0 &&
			((phaseType == "command" && phaseUsesIssue) || (gateCfg != nil && gateUsesIssue))
		if wantErr && err == nil {
			t.Fatalf("validateIssueDataForWorkflow() error = nil, want error for phaseType=%q phaseUsesIssue=%t gateUsesIssue=%t", phaseType, phaseUsesIssue, gateUsesIssue)
		}
		if !wantErr && err != nil {
			t.Fatalf("validateIssueDataForWorkflow() error = %v, want nil for phaseType=%q phaseUsesIssue=%t gateUsesIssue=%t issueNumber=%d", err, phaseType, phaseUsesIssue, gateUsesIssue, issueNumber)
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

func TestProp_InFlightAccountingMatchesLaunchedWork(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		concurrency := rapid.IntRange(1, 4).Draw(t, "concurrency")
		occupied := rapid.IntRange(0, concurrency-1).Draw(t, "occupied")
		pendingCount := rapid.IntRange(0, 8).Draw(t, "pending")

		dir, err := os.MkdirTemp("", "runner-inflight-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		cfg := makeTestConfig(dir, concurrency)
		cfg.StateDir = filepath.Join(dir, ".xylem")
		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		for i := 1; i <= pendingCount; i++ {
			if _, err := q.Enqueue(makeVessel(i, "fix-bug")); err != nil {
				t.Fatalf("Enqueue(%d) error = %v", i, err)
			}
		}
		writeSinglePhaseWorkflow(t, dir, "fix-bug")

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd() error = %v", err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("Chdir(%q) error = %v", dir, err)
		}
		defer os.Chdir(oldWd)

		releaseLaunched := make(chan struct{})
		r := New(cfg, q, &mockWorktree{}, &mockCmdRunner{
			phaseOutputs: map[string][]byte{
				"Analyze": []byte("analysis complete"),
			},
			runPhaseHook: func(_ string, _ string, _ string, _ ...string) ([]byte, error, bool) {
				<-releaseLaunched
				return []byte("analysis complete"), nil, true
			},
		})
		r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

		heldDone := make(chan struct{})
		for range occupied {
			r.sem <- struct{}{}
			r.inFlight.Add(1)
			r.wg.Add(1)
			go func() {
				<-heldDone
				<-r.sem
				r.inFlight.Add(-1)
				r.wg.Done()
			}()
		}

		result, err := r.Drain(context.Background())
		if err != nil {
			t.Fatalf("Drain() error = %v", err)
		}

		available := concurrency - occupied
		wantLaunched := pendingCount
		if wantLaunched > available {
			wantLaunched = available
		}
		if result.Launched != wantLaunched {
			t.Fatalf("Drain().Launched = %d, want %d (pending=%d, available=%d)", result.Launched, wantLaunched, pendingCount, available)
		}
		if got := r.InFlightCount(); got != occupied+result.Launched {
			t.Fatalf("InFlightCount() = %d, want %d", got, occupied+result.Launched)
		}

		close(releaseLaunched)
		close(heldDone)
		waited := r.Wait()
		if got := r.InFlightCount(); got != 0 {
			t.Fatalf("InFlightCount() after Wait = %d, want 0", got)
		}
		if waited.Completed != result.Launched {
			t.Fatalf("Wait().Completed = %d, want %d", waited.Completed, result.Launched)
		}
	})
}
