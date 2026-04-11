package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
	"github.com/nicholls-inc/xylem/cli/internal/phase"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/signal"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

type phaseLLMExecution struct {
	output           []byte
	promptForCost    string
	provider         string
	model            string
	evaluationReport *PhaseEvaluationReport
}

type phaseLoopGenerator struct {
	r                 *Runner
	vessel            queue.Vessel
	wf                *workflow.Workflow
	phaseDef          workflow.Phase
	promptTemplate    string
	phaseIdx          int
	previousOutputs   map[string]string
	issueData         phase.IssueData
	gateResult        string
	harnessContent    string
	worktreePath      string
	phasesDir         string
	srcCfg            *config.SourceConfig
	retryAttempt      int
	criteriaText      string
	traceEvents       *[]signal.TraceEvent
	iteration         int
	lastOutput        []byte
	lastPromptForCost string
	lastProvider      string
	lastModel         string
}

func (g *phaseLoopGenerator) ID() string {
	return g.phaseDef.Name + "-generator"
}

func (g *phaseLoopGenerator) Generate(ctx context.Context, _ string, feedback []evaluator.Issue) (string, error) {
	g.iteration++
	td := g.r.buildTemplateData(
		g.vessel,
		g.issueData,
		g.phaseDef.Name,
		g.phaseIdx,
		g.previousOutputs,
		g.gateResult,
		phase.EvaluationData{
			Iteration: g.iteration,
			Feedback:  formatEvaluatorFeedback(feedback),
			Criteria:  g.criteriaText,
		},
	)
	rendered, err := phase.RenderPrompt(g.promptTemplate, td)
	if err != nil {
		g.appendTrace("generation", false, "")
		return "", fmt.Errorf("render prompt for phase %s: %w", g.phaseDef.Name, err)
	}
	if wErr := os.WriteFile(filepath.Join(g.phasesDir, g.phaseDef.Name+".prompt"), []byte(rendered), 0o644); wErr != nil {
		log.Printf("warn: write prompt artifact for phase %s: %v", g.phaseDef.Name, wErr)
	}
	output, promptForCost, provider, model, err := g.r.runPromptInvocation(ctx, g.vessel, g.worktreePath, g.srcCfg, g.wf, &g.phaseDef, g.harnessContent, rendered, evaluatorLoopAttempt(g.retryAttempt, g.iteration))
	if err != nil {
		g.appendTrace("generation", false, "")
		return "", err
	}
	g.lastOutput = output
	g.lastPromptForCost = promptForCost
	g.lastProvider = provider
	g.lastModel = model
	g.appendTrace("generation", true, string(output))
	return string(output), nil
}

func (g *phaseLoopGenerator) appendTrace(eventType string, success bool, content string) {
	if g.traceEvents == nil {
		return
	}
	*g.traceEvents = append(*g.traceEvents, signal.TraceEvent{
		Type:       eventType,
		Timestamp:  g.r.runtimeNow().UTC(),
		Success:    success,
		TokensUsed: cost.EstimateTokens(content),
		Content:    content,
	})
}

type phaseLoopEvaluator struct {
	r               *Runner
	vessel          queue.Vessel
	wf              *workflow.Workflow
	phaseDef        workflow.Phase
	evaluatorDef    workflow.Phase
	evalTemplate    string
	phaseIdx        int
	previousOutputs map[string]string
	issueData       phase.IssueData
	gateResult      string
	harnessContent  string
	worktreePath    string
	phasesDir       string
	srcCfg          *config.SourceConfig
	vrs             *vesselRunState
	traceEvents     *[]signal.TraceEvent
	retryAttempt    int
	iteration       int
}

func (e *phaseLoopEvaluator) ID() string {
	return e.phaseDef.Name + "-evaluator"
}

func (e *phaseLoopEvaluator) Evaluate(ctx context.Context, output string, criteria []evaluator.Criterion) (*evaluator.EvalResult, error) {
	e.iteration++
	td := e.r.buildTemplateData(
		e.vessel,
		e.issueData,
		e.phaseDef.Name,
		e.phaseIdx,
		e.previousOutputs,
		e.gateResult,
		phase.EvaluationData{
			Iteration: e.iteration,
			Output:    output,
			Criteria:  formatEvaluatorCriteria(criteria),
		},
	)
	rendered, err := phase.RenderPrompt(e.evalTemplate, td)
	if err != nil {
		e.appendTrace(false, "")
		return nil, fmt.Errorf("render evaluator prompt for phase %s: %w", e.phaseDef.Name, err)
	}
	if wErr := os.WriteFile(filepath.Join(e.phasesDir, e.phaseDef.Name+".evaluator.prompt"), []byte(rendered), 0o644); wErr != nil {
		log.Printf("warn: write evaluator prompt artifact for phase %s: %v", e.phaseDef.Name, wErr)
	}
	rawOutput, promptForCost, _, model, err := e.r.runPromptInvocation(ctx, e.vessel, e.worktreePath, e.srcCfg, e.wf, &e.evaluatorDef, e.harnessContent, rendered, evaluatorLoopAttempt(e.retryAttempt, e.iteration))
	if err != nil {
		e.appendTrace(false, "")
		return nil, err
	}
	if wErr := os.WriteFile(filepath.Join(e.phasesDir, e.phaseDef.Name+".evaluator.output"), rawOutput, 0o644); wErr != nil {
		log.Printf("warn: write evaluator output artifact for phase %s: %v", e.phaseDef.Name, wErr)
	}
	if e.vrs != nil {
		e.vrs.recordEvaluationUsage(model, promptForCost, string(rawOutput), e.r.runtimeNow().UTC())
	}
	result, err := parseEvalResultOutput(rawOutput)
	if err != nil {
		e.appendTrace(false, string(rawOutput))
		return nil, fmt.Errorf("parse evaluator output for phase %s: %w", e.phaseDef.Name, err)
	}
	if len(result.Feedback) == 0 && len(result.Score.Issues) > 0 {
		result.Feedback = append([]evaluator.Issue(nil), result.Score.Issues...)
	}
	e.appendTrace(true, string(rawOutput))
	return result, nil
}

func (e *phaseLoopEvaluator) appendTrace(success bool, content string) {
	if e.traceEvents == nil {
		return
	}
	*e.traceEvents = append(*e.traceEvents, signal.TraceEvent{
		Type:       "evaluation",
		Timestamp:  e.r.runtimeNow().UTC(),
		Success:    success,
		TokensUsed: cost.EstimateTokens(content),
		Content:    content,
	})
}

func (r *Runner) runPromptInvocation(ctx context.Context, vessel queue.Vessel, worktreePath string, srcCfg *config.SourceConfig, wf *workflow.Workflow, p *workflow.Phase, harnessContent, rendered string, attempt int) ([]byte, string, string, string, error) {
	tier, providerChain := resolvePhaseProviderChain(r.Config, srcCfg, vessel, wf, p)
	provider := ""
	model := ""
	output, provider, model, err := r.runPhaseWithProviderFallback(ctx, vessel.ID, p.Name, worktreePath, providerChain, func(provider string) (providerInvocation, error) {
		cmd, args, phaseStdin, resolvedModel, err := buildProviderPhaseArgs(r.Config, srcCfg, wf, p, harnessContent, provider, tier, rendered, attempt)
		if err != nil {
			return providerInvocation{}, err
		}
		stdinContent := ""
		if phaseStdin != nil {
			stdinContent = rendered
		}
		return providerInvocation{
			Provider:     provider,
			Model:        resolvedModel,
			Env:          providerEnvForName(r.Config, provider),
			Command:      cmd,
			Args:         args,
			StdinContent: stdinContent,
		}, nil
	})
	promptForCost := rendered
	if harnessContent != "" {
		promptForCost = harnessContent + "\n\n" + rendered
	}
	return output, promptForCost, provider, model, err
}

func (r *Runner) runPhaseEvaluationLoop(ctx context.Context, vessel queue.Vessel, wf *workflow.Workflow, phaseIdx int, previousOutputs map[string]string, issueData phase.IssueData, gateResult, harnessContent, worktreePath, phasesDir, promptTemplate string, vrs *vesselRunState, retryAttempt int) (*phaseLLMExecution, error) {
	p := wf.Phases[phaseIdx]
	if p.Evaluator == nil {
		return nil, fmt.Errorf("phase %s has no evaluator configuration", p.Name)
	}

	traceEvents := evaluationSeedEvents(previousOutputs, gateResult, r.runtimeNow())
	seedSignals := signal.Compute(traceEvents, signal.DefaultConfig())
	intensity := evaluator.SelectIntensity(resolveEvaluationComplexity(p, p.Evaluator), seedSignals.HealthString())
	srcCfg := r.sourceConfigFromMeta(vessel)
	gen := &phaseLoopGenerator{
		r:               r,
		vessel:          vessel,
		wf:              wf,
		phaseDef:        p,
		promptTemplate:  promptTemplate,
		phaseIdx:        phaseIdx,
		previousOutputs: previousOutputs,
		issueData:       issueData,
		gateResult:      gateResult,
		harnessContent:  harnessContent,
		worktreePath:    worktreePath,
		phasesDir:       phasesDir,
		srcCfg:          srcCfg,
		retryAttempt:    retryAttempt,
		criteriaText:    formatEvaluatorCriteria(p.Evaluator.Criteria),
		traceEvents:     &traceEvents,
	}
	evalPhase := evaluatorPhaseFromConfig(p, p.Evaluator)
	evalTemplateBytes, err := os.ReadFile(p.Evaluator.PromptFile)
	if err != nil {
		return nil, fmt.Errorf("read evaluator prompt file %s: %w", p.Evaluator.PromptFile, err)
	}
	ev := &phaseLoopEvaluator{
		r:               r,
		vessel:          vessel,
		wf:              wf,
		phaseDef:        p,
		evaluatorDef:    evalPhase,
		evalTemplate:    string(evalTemplateBytes),
		phaseIdx:        phaseIdx,
		previousOutputs: previousOutputs,
		issueData:       issueData,
		gateResult:      gateResult,
		harnessContent:  harnessContent,
		worktreePath:    worktreePath,
		phasesDir:       phasesDir,
		srcCfg:          srcCfg,
		vrs:             vrs,
		traceEvents:     &traceEvents,
		retryAttempt:    retryAttempt,
	}
	loop, err := evaluator.NewLoop(gen, ev, evaluator.EvalConfig{
		Criteria:      append([]evaluator.Criterion(nil), p.Evaluator.Criteria...),
		MaxIterations: p.Evaluator.MaxIterations,
		PassThreshold: p.Evaluator.PassThreshold,
	})
	if err != nil {
		return nil, fmt.Errorf("create evaluator loop for phase %s: %w", p.Name, err)
	}
	result, err := loop.RunWithIntensity(ctx, promptTemplate, intensity)
	if err != nil {
		return nil, fmt.Errorf("run evaluator loop for phase %s: %w", p.Name, err)
	}
	if len(gen.lastOutput) == 0 {
		return nil, fmt.Errorf("run evaluator loop for phase %s: generator did not produce output", p.Name)
	}
	signals := signal.Compute(traceEvents, signal.DefaultConfig())
	report := &PhaseEvaluationReport{
		Phase:       p.Name,
		Intensity:   intensity.String(),
		Signals:     signals,
		Criteria:    append([]evaluator.Criterion(nil), p.Evaluator.Criteria...),
		Iterations:  result.Iterations,
		Converged:   result.Converged,
		History:     append([]evaluator.EvalResult(nil), result.History...),
		FinalResult: cloneEvalResult(result.FinalResult),
	}
	exec := &phaseLLMExecution{
		output:           append([]byte(nil), gen.lastOutput...),
		promptForCost:    gen.lastPromptForCost,
		provider:         gen.lastProvider,
		model:            gen.lastModel,
		evaluationReport: report,
	}
	if result.FinalResult == nil {
		return exec, fmt.Errorf("phase %s evaluator returned no final result", p.Name)
	}
	if !result.Converged || !result.FinalResult.Pass {
		return exec, fmt.Errorf("phase %s evaluator did not pass after %d iterations", p.Name, result.Iterations)
	}
	return exec, nil
}

func cloneEvalResult(result *evaluator.EvalResult) *evaluator.EvalResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Feedback = append([]evaluator.Issue(nil), result.Feedback...)
	clone.Score.Issues = append([]evaluator.Issue(nil), result.Score.Issues...)
	if result.Score.Criteria != nil {
		clone.Score.Criteria = make(map[string]float64, len(result.Score.Criteria))
		for key, value := range result.Score.Criteria {
			clone.Score.Criteria[key] = value
		}
	}
	return &clone
}

func evaluatorPhaseFromConfig(base workflow.Phase, cfg *workflow.PhaseEvaluator) workflow.Phase {
	return workflow.Phase{
		Name:         base.Name + "_evaluator",
		PromptFile:   cfg.PromptFile,
		MaxTurns:     cfg.MaxTurns,
		LLM:          cfg.LLM,
		Model:        cfg.Model,
		Tier:         cfg.Tier,
		AllowedTools: cfg.AllowedTools,
	}
}

func formatEvaluatorFeedback(feedback []evaluator.Issue) string {
	if len(feedback) == 0 {
		return ""
	}
	var b strings.Builder
	for i, issue := range feedback {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, issue.Severity.String(), strings.TrimSpace(issue.Description))
		if loc := strings.TrimSpace(issue.Location); loc != "" {
			fmt.Fprintf(&b, " @ %s", loc)
		}
		if suggestion := strings.TrimSpace(issue.Suggestion); suggestion != "" {
			fmt.Fprintf(&b, "\n   Suggestion: %s", suggestion)
		}
	}
	return b.String()
}

func formatEvaluatorCriteria(criteria []evaluator.Criterion) string {
	if len(criteria) == 0 {
		return ""
	}
	var b strings.Builder
	for i, criterion := range criteria {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s (weight=%.2f, threshold=%.2f)", i+1, criterion.Name, criterion.Weight, criterion.Threshold)
		if desc := strings.TrimSpace(criterion.Description); desc != "" {
			fmt.Fprintf(&b, ": %s", desc)
		}
	}
	return b.String()
}

func evaluationSeedEvents(previousOutputs map[string]string, gateResult string, now time.Time) []signal.TraceEvent {
	keys := make([]string, 0, len(previousOutputs))
	for key := range previousOutputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	now = now.UTC()
	events := make([]signal.TraceEvent, 0, len(keys)+1)
	for i, key := range keys {
		events = append(events, signal.TraceEvent{
			Type:       "generation",
			Timestamp:  now.Add(time.Duration(i) * time.Millisecond),
			Success:    true,
			TokensUsed: cost.EstimateTokens(previousOutputs[key]),
			Content:    previousOutputs[key],
		})
	}
	if strings.TrimSpace(gateResult) != "" {
		events = append(events, signal.TraceEvent{
			Type:       "gate_feedback",
			Timestamp:  now.Add(time.Duration(len(events)) * time.Millisecond),
			Success:    true,
			TokensUsed: cost.EstimateTokens(gateResult),
			Content:    gateResult,
		})
	}
	return events
}

func evaluatorLoopAttempt(retryAttempt, iteration int) int {
	if retryAttempt < 1 {
		retryAttempt = 1
	}
	if iteration < 1 {
		iteration = 1
	}
	return retryAttempt*1000 + iteration
}

func resolveEvaluationComplexity(p workflow.Phase, cfg *workflow.PhaseEvaluator) string {
	raw := ""
	switch {
	case cfg != nil && cfg.Tier != nil:
		raw = strings.ToLower(strings.TrimSpace(*cfg.Tier))
	case p.Tier != nil:
		raw = strings.ToLower(strings.TrimSpace(*p.Tier))
	}
	switch raw {
	case "low", "trivial":
		return "low"
	case "high", "critical":
		return "high"
	default:
		return "medium"
	}
}

func parseEvalResultOutput(raw []byte) (*evaluator.EvalResult, error) {
	var result evaluator.EvalResult
	if err := json.Unmarshal(raw, &result); err == nil {
		return &result, nil
	}
	text := strings.TrimSpace(string(raw))
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(text[start:end+1]), &result); err == nil {
			return &result, nil
		}
	}
	return nil, fmt.Errorf("evaluator output was not valid JSON")
}

func applyPhaseEvaluationSummary(summary PhaseSummary, report *PhaseEvaluationReport) PhaseSummary {
	if report == nil {
		return summary
	}
	summary.EvalIterations = report.Iterations
	summary.EvalConverged = report.Converged
	summary.EvalIntensity = report.Intensity
	return summary
}
