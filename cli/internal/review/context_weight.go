package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ContextWeightAuditWorkflow       = "context-weight-audit"
	contextWeightReportJSONName      = "context-weight-audit.json"
	contextWeightReportMarkdownName  = "context-weight-audit.md"
	contextWeightIssueStateName      = "context-weight-audit-issues.json"
	contextWeightFindingMarkerPrefix = "<!-- xylem:context-weight-fingerprint="
	contextWeightOutlierRatio        = 2
	contextWeightMaxOffendingPhases  = 3
)

type ContextWeightOptions struct {
	LookbackRuns int
	MinSamples   int
	OutputDir    string
	Now          time.Time
}

type ContextWeightResult struct {
	Report       *ContextWeightReport
	JSONPath     string
	MarkdownPath string
	Markdown     string
	Published    []PublishedIssue
}

type ContextWeightReport struct {
	GeneratedAt       time.Time              `json:"generated_at"`
	LookbackRuns      int                    `json:"lookback_runs"`
	MinSamples        int                    `json:"min_samples"`
	TotalRunsObserved int                    `json:"total_runs_observed"`
	ReviewedRuns      int                    `json:"reviewed_runs"`
	Baseline          ContextWeightBaseline  `json:"baseline"`
	Findings          []ContextWeightFinding `json:"findings,omitempty"`
	Warnings          []string               `json:"warnings,omitempty"`
}

type ContextWeightBaseline struct {
	WorkflowInputTokens  int `json:"workflow_input_tokens"`
	WorkflowOutputTokens int `json:"workflow_output_tokens"`
}

type ContextWeightFinding struct {
	Fingerprint               string               `json:"fingerprint"`
	Source                    string               `json:"source"`
	Workflow                  string               `json:"workflow"`
	Samples                   int                  `json:"samples"`
	AverageInputTokens        int                  `json:"average_input_tokens"`
	AverageOutputTokens       int                  `json:"average_output_tokens"`
	RepeatedHighFootprintRuns int                  `json:"repeated_high_footprint_runs"`
	LargestPhases             []ContextWeightPhase `json:"largest_phases,omitempty"`
	Reasons                   []string             `json:"reasons,omitempty"`
	Remediations              []string             `json:"remediations,omitempty"`
}

type ContextWeightPhase struct {
	Name                string `json:"name"`
	Type                string `json:"type"`
	Samples             int    `json:"samples"`
	AverageInputTokens  int    `json:"average_input_tokens"`
	AverageOutputTokens int    `json:"average_output_tokens"`
}

type PublishedIssue struct {
	Fingerprint string `json:"fingerprint"`
	IssueNumber int    `json:"issue_number"`
	Title       string `json:"title"`
	Created     bool   `json:"created"`
}

type contextWeightWorkflowAccumulator struct {
	source      string
	workflow    string
	samples     int
	totalInput  int
	totalOutput int
	runs        []contextWeightRun
	phases      map[string]*contextWeightPhaseAccumulator
}

type contextWeightRun struct {
	input  int
	output int
}

type contextWeightPhaseAccumulator struct {
	name        string
	phaseType   string
	samples     int
	totalInput  int
	totalOutput int
}

type contextWeightIssueState struct {
	Findings map[string]contextWeightIssueRecord `json:"findings,omitempty"`
}

type contextWeightIssueRecord struct {
	IssueNumber     int       `json:"issue_number"`
	Title           string    `json:"title"`
	FirstReportedAt time.Time `json:"first_reported_at"`
	LastObservedAt  time.Time `json:"last_observed_at"`
}

type contextWeightIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

type issueRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

func GenerateContextWeightAudit(stateDir string, opts ContextWeightOptions) (*ContextWeightResult, error) {
	if opts.LookbackRuns <= 0 {
		opts.LookbackRuns = defaultLookbackRuns
	}
	if opts.MinSamples <= 0 {
		opts.MinSamples = defaultMinSamples
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		opts.OutputDir = defaultOutputDir
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}

	runs, totalRuns, warnings, err := loadRuns(stateDir, opts.LookbackRuns)
	if err != nil {
		return nil, fmt.Errorf("generate context-weight audit: %w", err)
	}

	findings, baseline := buildContextWeightFindings(runs, opts.MinSamples)
	report := &ContextWeightReport{
		GeneratedAt:       opts.Now,
		LookbackRuns:      opts.LookbackRuns,
		MinSamples:        opts.MinSamples,
		TotalRunsObserved: totalRuns,
		ReviewedRuns:      len(runs),
		Baseline:          baseline,
		Findings:          findings,
		Warnings:          append([]string(nil), warnings...),
	}

	outputDir := filepath.Join(stateDir, opts.OutputDir)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("generate context-weight audit: create output dir: %w", err)
	}

	jsonPath := filepath.Join(outputDir, contextWeightReportJSONName)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("generate context-weight audit: marshal report: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("generate context-weight audit: write json report: %w", err)
	}

	markdown := renderContextWeightMarkdown(report)
	markdownPath := filepath.Join(outputDir, contextWeightReportMarkdownName)
	if err := os.WriteFile(markdownPath, []byte(markdown), 0o644); err != nil {
		return nil, fmt.Errorf("generate context-weight audit: write markdown report: %w", err)
	}

	return &ContextWeightResult{
		Report:       report,
		JSONPath:     jsonPath,
		MarkdownPath: markdownPath,
		Markdown:     markdown,
	}, nil
}

func RunContextWeightAudit(ctx context.Context, stateDir, repo string, runner issueRunner, opts ContextWeightOptions) (*ContextWeightResult, error) {
	result, err := GenerateContextWeightAudit(stateDir, opts)
	if err != nil {
		return nil, err
	}
	published, err := PublishContextWeightIssues(ctx, stateDir, repo, runner, result.Report, opts.OutputDir, opts.Now)
	if err != nil {
		return nil, err
	}
	result.Published = published
	return result, nil
}

func PublishContextWeightIssues(ctx context.Context, stateDir, repo string, runner issueRunner, report *ContextWeightReport, outputDir string, now time.Time) ([]PublishedIssue, error) {
	if report == nil {
		return nil, nil
	}
	if strings.TrimSpace(outputDir) == "" {
		outputDir = defaultOutputDir
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	statePath := filepath.Join(stateDir, outputDir, contextWeightIssueStateName)
	state, err := loadContextWeightIssueState(statePath)
	if err != nil {
		return nil, err
	}

	openByFingerprint := map[string]contextWeightIssue{}
	if runner != nil && strings.TrimSpace(repo) != "" && len(report.Findings) > 0 {
		openByFingerprint, err = loadOpenContextWeightIssues(ctx, runner, repo)
		if err != nil {
			return nil, err
		}
	}

	published := make([]PublishedIssue, 0, len(report.Findings))
	for _, finding := range report.Findings {
		title := contextWeightIssueTitle(finding)
		record, ok := state.Findings[finding.Fingerprint]
		switch {
		case ok:
			record.Title = title
			record.LastObservedAt = now
			state.Findings[finding.Fingerprint] = record
			published = append(published, PublishedIssue{
				Fingerprint: finding.Fingerprint,
				IssueNumber: record.IssueNumber,
				Title:       record.Title,
				Created:     false,
			})
			continue
		case openByFingerprint[finding.Fingerprint].Number > 0:
			issue := openByFingerprint[finding.Fingerprint]
			state.Findings[finding.Fingerprint] = contextWeightIssueRecord{
				IssueNumber:     issue.Number,
				Title:           issue.Title,
				FirstReportedAt: now,
				LastObservedAt:  now,
			}
			published = append(published, PublishedIssue{
				Fingerprint: finding.Fingerprint,
				IssueNumber: issue.Number,
				Title:       issue.Title,
				Created:     false,
			})
			continue
		}

		if runner == nil || strings.TrimSpace(repo) == "" {
			continue
		}

		body := renderContextWeightIssueBody(finding, report.Baseline)
		out, err := runner.RunOutput(ctx, "gh", "issue", "create", "--repo", repo, "--title", title, "--body", body)
		if err != nil {
			return nil, fmt.Errorf("publish context-weight issue for %s: %w", finding.Workflow, err)
		}
		issueNumber, err := parseIssueNumberFromCreateOutput(string(out))
		if err != nil {
			return nil, fmt.Errorf("publish context-weight issue for %s: %w", finding.Workflow, err)
		}
		state.Findings[finding.Fingerprint] = contextWeightIssueRecord{
			IssueNumber:     issueNumber,
			Title:           title,
			FirstReportedAt: now,
			LastObservedAt:  now,
		}
		published = append(published, PublishedIssue{
			Fingerprint: finding.Fingerprint,
			IssueNumber: issueNumber,
			Title:       title,
			Created:     true,
		})
	}

	if err := saveContextWeightIssueState(statePath, state); err != nil {
		return nil, err
	}
	return published, nil
}

func buildContextWeightFindings(runs []loadedRun, minSamples int) ([]ContextWeightFinding, ContextWeightBaseline) {
	workflows := make(map[string]*contextWeightWorkflowAccumulator)
	for _, run := range runs {
		if run.Summary.TotalInputTokensEst == 0 && run.Summary.TotalOutputTokensEst == 0 {
			continue
		}
		key := run.Summary.Source + "\x00" + run.Summary.Workflow
		group := workflows[key]
		if group == nil {
			group = &contextWeightWorkflowAccumulator{
				source:   run.Summary.Source,
				workflow: run.Summary.Workflow,
				phases:   make(map[string]*contextWeightPhaseAccumulator),
			}
			workflows[key] = group
		}
		group.samples++
		group.totalInput += run.Summary.TotalInputTokensEst
		group.totalOutput += run.Summary.TotalOutputTokensEst
		group.runs = append(group.runs, contextWeightRun{
			input:  run.Summary.TotalInputTokensEst,
			output: run.Summary.TotalOutputTokensEst,
		})
		for _, phase := range run.Summary.Phases {
			if phase.Type != "prompt" {
				continue
			}
			phaseKey := phase.Name + "\x00" + phase.Type
			phaseGroup := group.phases[phaseKey]
			if phaseGroup == nil {
				phaseGroup = &contextWeightPhaseAccumulator{
					name:      phase.Name,
					phaseType: phase.Type,
				}
				group.phases[phaseKey] = phaseGroup
			}
			phaseGroup.samples++
			phaseGroup.totalInput += phase.InputTokensEst
			phaseGroup.totalOutput += phase.OutputTokensEst
		}
	}

	inputAverages := make([]int, 0, len(workflows))
	outputAverages := make([]int, 0, len(workflows))
	for _, group := range workflows {
		if group.samples < minSamples {
			continue
		}
		inputAverages = append(inputAverages, group.totalInput/group.samples)
		outputAverages = append(outputAverages, group.totalOutput/group.samples)
	}
	baseline := ContextWeightBaseline{
		WorkflowInputTokens:  medianInt(inputAverages),
		WorkflowOutputTokens: medianInt(outputAverages),
	}

	findings := make([]ContextWeightFinding, 0)
	for _, group := range workflows {
		if group.samples < minSamples {
			continue
		}
		avgInput := group.totalInput / group.samples
		avgOutput := group.totalOutput / group.samples
		reasons := make([]string, 0, 3)
		if baseline.WorkflowInputTokens > 0 && avgInput >= baseline.WorkflowInputTokens*contextWeightOutlierRatio {
			reasons = append(reasons, fmt.Sprintf(
				"average input tokens %d are %.1fx the repo baseline %d",
				avgInput,
				float64(avgInput)/float64(baseline.WorkflowInputTokens),
				baseline.WorkflowInputTokens,
			))
		}
		if baseline.WorkflowOutputTokens > 0 && avgOutput >= baseline.WorkflowOutputTokens*contextWeightOutlierRatio {
			reasons = append(reasons, fmt.Sprintf(
				"average output tokens %d are %.1fx the repo baseline %d",
				avgOutput,
				float64(avgOutput)/float64(baseline.WorkflowOutputTokens),
				baseline.WorkflowOutputTokens,
			))
		}
		highRuns := countHighFootprintRuns(group.runs, baseline)
		if highRuns >= minSamples {
			reasons = append(reasons, fmt.Sprintf(
				"%d of %d recent runs stayed above the %dx repo baseline",
				highRuns,
				group.samples,
				contextWeightOutlierRatio,
			))
		}
		if len(reasons) == 0 {
			continue
		}

		largestPhases := topContextWeightPhases(group.phases)
		finding := ContextWeightFinding{
			Source:                    group.source,
			Workflow:                  group.workflow,
			Samples:                   group.samples,
			AverageInputTokens:        avgInput,
			AverageOutputTokens:       avgOutput,
			RepeatedHighFootprintRuns: highRuns,
			LargestPhases:             largestPhases,
			Reasons:                   reasons,
			Remediations:              contextWeightRemediations(group.workflow, largestPhases),
		}
		finding.Fingerprint = contextWeightFindingFingerprint(finding)
		findings = append(findings, finding)
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].AverageInputTokens != findings[j].AverageInputTokens {
			return findings[i].AverageInputTokens > findings[j].AverageInputTokens
		}
		if findings[i].AverageOutputTokens != findings[j].AverageOutputTokens {
			return findings[i].AverageOutputTokens > findings[j].AverageOutputTokens
		}
		return findings[i].Workflow < findings[j].Workflow
	})
	return findings, baseline
}

func countHighFootprintRuns(runs []contextWeightRun, baseline ContextWeightBaseline) int {
	count := 0
	for _, run := range runs {
		highInput := baseline.WorkflowInputTokens > 0 && run.input >= baseline.WorkflowInputTokens*contextWeightOutlierRatio
		highOutput := baseline.WorkflowOutputTokens > 0 && run.output >= baseline.WorkflowOutputTokens*contextWeightOutlierRatio
		if highInput || highOutput {
			count++
		}
	}
	return count
}

func topContextWeightPhases(phases map[string]*contextWeightPhaseAccumulator) []ContextWeightPhase {
	if len(phases) == 0 {
		return nil
	}
	out := make([]ContextWeightPhase, 0, len(phases))
	for _, phase := range phases {
		if phase.samples == 0 {
			continue
		}
		out = append(out, ContextWeightPhase{
			Name:                phase.name,
			Type:                phase.phaseType,
			Samples:             phase.samples,
			AverageInputTokens:  phase.totalInput / phase.samples,
			AverageOutputTokens: phase.totalOutput / phase.samples,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AverageInputTokens != out[j].AverageInputTokens {
			return out[i].AverageInputTokens > out[j].AverageInputTokens
		}
		if out[i].AverageOutputTokens != out[j].AverageOutputTokens {
			return out[i].AverageOutputTokens > out[j].AverageOutputTokens
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > contextWeightMaxOffendingPhases {
		out = out[:contextWeightMaxOffendingPhases]
	}
	return out
}

func contextWeightRemediations(workflow string, phases []ContextWeightPhase) []string {
	remediations := []string{
		"Trim large prompt templates or static context in the heaviest phases before adding more workflow logic.",
		"Split the workflow with an explicit context reset or handoff if the same run keeps carrying too much prior output forward.",
		fmt.Sprintf("Keep the workflow aligned with #60 by prioritizing ctxmgr-backed compaction or handoff for `%s` instead of assuming runner integration already exists.", workflow),
	}
	if len(phases) == 0 {
		return remediations
	}
	names := make([]string, 0, len(phases))
	for _, phase := range phases {
		names = append(names, phase.Name)
	}
	remediations[0] = fmt.Sprintf("Trim large prompt templates or static context in the heaviest phases (%s) before adding more workflow logic.", strings.Join(names, ", "))
	return remediations
}

func contextWeightFindingFingerprint(finding ContextWeightFinding) string {
	signals := make([]string, 0, 3)
	for _, reason := range finding.Reasons {
		switch {
		case strings.Contains(reason, "input tokens"):
			signals = append(signals, "input")
		case strings.Contains(reason, "output tokens"):
			signals = append(signals, "output")
		case strings.Contains(reason, "recent runs"):
			signals = append(signals, "persistent")
		}
	}
	phaseNames := make([]string, 0, len(finding.LargestPhases))
	for _, phase := range finding.LargestPhases {
		phaseNames = append(phaseNames, phase.Name)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		finding.Source,
		finding.Workflow,
		strings.Join(signals, ","),
		strings.Join(phaseNames, ","),
	}, "\n")))
	return fmt.Sprintf("%x", sum[:8])
}

func renderContextWeightMarkdown(report *ContextWeightReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Context-weight audit\n\n")
	fmt.Fprintf(&b, "- Generated: %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Reviewed runs: %d of %d available\n", report.ReviewedRuns, report.TotalRunsObserved)
	fmt.Fprintf(&b, "- Baseline: median workflow input=%d tokens, median workflow output=%d tokens\n\n",
		report.Baseline.WorkflowInputTokens,
		report.Baseline.WorkflowOutputTokens,
	)
	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "## Warnings\n\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
		fmt.Fprintf(&b, "\n")
	}
	if len(report.Findings) == 0 {
		fmt.Fprintf(&b, "No persistent context-weight outliers were detected.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "## Findings\n\n")
	for _, finding := range report.Findings {
		fmt.Fprintf(&b, "### `%s / %s`\n\n", finding.Source, finding.Workflow)
		fmt.Fprintf(&b, "- Samples: %d\n", finding.Samples)
		fmt.Fprintf(&b, "- Average input tokens: %d\n", finding.AverageInputTokens)
		fmt.Fprintf(&b, "- Average output tokens: %d\n", finding.AverageOutputTokens)
		fmt.Fprintf(&b, "- Repeated high-footprint runs: %d\n", finding.RepeatedHighFootprintRuns)
		if len(finding.Reasons) > 0 {
			fmt.Fprintf(&b, "- Evidence: %s\n", strings.Join(finding.Reasons, "; "))
		}
		if len(finding.LargestPhases) > 0 {
			fmt.Fprintf(&b, "- Largest phases:\n")
			for _, phase := range finding.LargestPhases {
				fmt.Fprintf(&b, "  - `%s` (%s): avg input=%d, avg output=%d over %d sample(s)\n",
					phase.Name, phase.Type, phase.AverageInputTokens, phase.AverageOutputTokens, phase.Samples)
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

func contextWeightIssueTitle(finding ContextWeightFinding) string {
	return fmt.Sprintf("harness: context-weight audit for %s", finding.Workflow)
}

func renderContextWeightIssueBody(finding ContextWeightFinding, baseline ContextWeightBaseline) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s -->\n", contextWeightFindingMarkerPrefix, finding.Fingerprint)
	fmt.Fprintf(&b, "## Context-weight finding\n\n")
	fmt.Fprintf(&b, "- Source: `%s`\n", finding.Source)
	fmt.Fprintf(&b, "- Workflow: `%s`\n", finding.Workflow)
	fmt.Fprintf(&b, "- Samples reviewed: %d\n", finding.Samples)
	fmt.Fprintf(&b, "- Average estimated input tokens: %d (repo median baseline %d)\n", finding.AverageInputTokens, baseline.WorkflowInputTokens)
	fmt.Fprintf(&b, "- Average estimated output tokens: %d (repo median baseline %d)\n", finding.AverageOutputTokens, baseline.WorkflowOutputTokens)
	fmt.Fprintf(&b, "- Repeated high-footprint runs: %d\n\n", finding.RepeatedHighFootprintRuns)

	fmt.Fprintf(&b, "This issue was generated from persisted `.xylem/phases/*/summary.json` artifacts. It measures the current runner prompt footprint and should stay aligned with #60 rather than assuming `ctxmgr` is already wired into prompt assembly.\n\n")

	if len(finding.Reasons) > 0 {
		fmt.Fprintf(&b, "## Evidence\n\n")
		for _, reason := range finding.Reasons {
			fmt.Fprintf(&b, "- %s\n", reason)
		}
		fmt.Fprintf(&b, "\n")
	}
	if len(finding.LargestPhases) > 0 {
		fmt.Fprintf(&b, "## Largest offending phases\n\n")
		fmt.Fprintf(&b, "| Phase | Type | Avg input tokens | Avg output tokens | Samples |\n")
		fmt.Fprintf(&b, "|-------|------|------------------|-------------------|---------|\n")
		for _, phase := range finding.LargestPhases {
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %d |\n",
				phase.Name, phase.Type, phase.AverageInputTokens, phase.AverageOutputTokens, phase.Samples)
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, "## Suggested remediations\n\n")
	for _, remediation := range finding.Remediations {
		fmt.Fprintf(&b, "- %s\n", remediation)
	}
	return b.String()
}

func loadOpenContextWeightIssues(ctx context.Context, runner issueRunner, repo string) (map[string]contextWeightIssue, error) {
	out, err := runner.RunOutput(ctx, "gh", "issue", "list", "--repo", repo, "--state", "open", "--limit", "100", "--json", "number,title,body")
	if err != nil {
		return nil, fmt.Errorf("load open context-weight issues: %w", err)
	}
	var issues []contextWeightIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("load open context-weight issues: parse gh issue list output: %w", err)
	}
	byFingerprint := make(map[string]contextWeightIssue, len(issues))
	for _, issue := range issues {
		fingerprint := parseContextWeightMarker(issue.Body)
		if fingerprint == "" {
			continue
		}
		byFingerprint[fingerprint] = issue
	}
	return byFingerprint, nil
}

func parseContextWeightMarker(body string) string {
	start := strings.Index(body, contextWeightFindingMarkerPrefix)
	if start == -1 {
		return ""
	}
	start += len(contextWeightFindingMarkerPrefix)
	end := strings.Index(body[start:], " -->")
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(body[start : start+end])
}

func parseIssueNumberFromCreateOutput(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("parse created issue: empty output")
	}
	if n, err := strconv.Atoi(trimmed); err == nil {
		return n, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return 0, fmt.Errorf("parse created issue url %q: %w", trimmed, err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) == 0 {
		return 0, fmt.Errorf("parse created issue url %q: missing path segments", trimmed)
	}
	issueNum, err := strconv.Atoi(segments[len(segments)-1])
	if err != nil {
		return 0, fmt.Errorf("parse created issue url %q: %w", trimmed, err)
	}
	return issueNum, nil
}

func loadContextWeightIssueState(path string) (*contextWeightIssueState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &contextWeightIssueState{Findings: make(map[string]contextWeightIssueRecord)}, nil
		}
		return nil, fmt.Errorf("load context-weight issue state: %w", err)
	}
	var state contextWeightIssueState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("load context-weight issue state: unmarshal: %w", err)
	}
	if state.Findings == nil {
		state.Findings = make(map[string]contextWeightIssueRecord)
	}
	return &state, nil
}

func saveContextWeightIssueState(path string, state *contextWeightIssueState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save context-weight issue state: create dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("save context-weight issue state: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save context-weight issue state: write: %w", err)
	}
	return nil
}

func medianInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
