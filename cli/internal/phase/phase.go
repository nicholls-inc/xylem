package phase

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
	"github.com/nicholls-inc/xylem/cli/internal/memory"
)

// Truncation limits (constants, not configurable).
const (
	MaxPreviousOutputLen = 16000
	MaxGateResultLen     = 8000
	MaxIssueBodyLen      = 32000
	MaxReporterOutputLen = 64000
	MaxEvalFeedbackLen   = 8000
	MaxEvalOutputLen     = 16000
	MaxEvalCriteriaLen   = 8000
	TruncationSuffix     = "\n\n[... output truncated at %d characters]"
	minSummaryTokens     = 8
)

// TemplateData holds all data available to phase prompt templates.
type TemplateData struct {
	Date  string
	Issue IssueData
	Phase PhaseData
	// PreviousOutputs maps phase name to output text.
	PreviousOutputs map[string]string
	// PreviousOutputOrder is the compaction order to apply to previous phase
	// outputs. When empty, lexical key order is used.
	PreviousOutputOrder []string
	GateResult          string // most recent gate command output
	Evaluation          EvaluationData
	Vessel              VesselData
	Repo                RepoData
	Source              SourceData
	Validation          ValidationData
	// EpisodicContext holds prior episodic entries for this vessel, most
	// recent first. Populated by the runner for phase index > 0. Template
	// authors should guard with {{if .EpisodicContext}}.
	EpisodicContext []memory.EpisodicEntry
	// DaemonBinary is the absolute path to the running xylem daemon binary.
	// Use this in command phases that need to call back into xylem without
	// requiring a built binary in the worktree.
	DaemonBinary string
}

type RenderOptions struct {
	ContextBudget int
	Preamble      string
}

// IssueData describes the issue being worked on.
type IssueData struct {
	URL    string
	Title  string
	Body   string
	Labels []string
	Number int
}

// PhaseData identifies the current phase.
type PhaseData struct {
	Name  string
	Index int
}

// VesselData identifies the vessel (work item) being processed.
type VesselData struct {
	ID     string
	Ref    string
	Source string
	Meta   map[string]string
}

// RepoData describes repository-level template metadata for the vessel.
type RepoData struct {
	Slug          string
	DefaultBranch string
}

// SourceData describes the configured source that produced the vessel.
type SourceData struct {
	Name string
	Repo string
}

// ValidationData describes optional repo-specific validation commands.
type ValidationData struct {
	Format string
	Lint   string
	Build  string
	Test   string
}

// EvaluationData describes evaluator loop context for the current phase.
type EvaluationData struct {
	Iteration int
	Feedback  string
	Output    string
	Criteria  string
}

// TruncateOutput truncates s to maxLen characters, appending a suffix if truncated.
func TruncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf(TruncationSuffix, maxLen)
}

// prepareData returns a copy of data with all fields truncated to their limits.
// It does not mutate the caller's data.
func prepareData(data TemplateData) TemplateData {
	out := cloneTemplateData(data)

	if out.PreviousOutputs != nil {
		for k, v := range out.PreviousOutputs {
			out.PreviousOutputs[k] = TruncateOutput(v, MaxPreviousOutputLen)
		}
	}

	out.GateResult = TruncateOutput(data.GateResult, MaxGateResultLen)
	out.Issue.Body = TruncateOutput(data.Issue.Body, MaxIssueBodyLen)
	out.Evaluation.Feedback = TruncateOutput(data.Evaluation.Feedback, MaxEvalFeedbackLen)
	out.Evaluation.Output = TruncateOutput(data.Evaluation.Output, MaxEvalOutputLen)
	out.Evaluation.Criteria = TruncateOutput(data.Evaluation.Criteria, MaxEvalCriteriaLen)

	return out
}

// RenderPrompt parses templateContent as a Go text/template and executes it
// with the provided data. Fields are truncated to their respective limits
// before rendering.
func RenderPrompt(templateContent string, data TemplateData) (string, error) {
	return renderPrompt(templateContent, data, RenderOptions{})
}

// RenderPromptWithOptions renders a phase prompt with optional context-budget
// compaction.
func RenderPromptWithOptions(templateContent string, data TemplateData, opts RenderOptions) (string, error) {
	return renderPrompt(templateContent, data, opts)
}

func renderPrompt(templateContent string, data TemplateData, opts RenderOptions) (string, error) {
	tmpl, err := template.New("phase").Option("missingkey=zero").Parse(templateContent)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	prepared := prepareData(data)
	rendered, err := executeTemplate(tmpl, prepared)
	if err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	if opts.ContextBudget <= 0 {
		return rendered, nil
	}
	if renderedFitsBudget(rendered, opts) {
		return rendered, nil
	}
	compacted, err := compactRenderedPrompt(tmpl, prepared, opts)
	if err != nil {
		return "", err
	}
	return compacted, nil
}

func executeTemplate(tmpl *template.Template, data TemplateData) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type mutableField struct {
	name     string
	priority int
	get      func(*TemplateData) string
	set      func(*TemplateData, string)
}

func compactRenderedPrompt(tmpl *template.Template, data TemplateData, opts RenderOptions) (string, error) {
	available := availableBodyBudget(opts)
	compacted := cloneTemplateData(data)
	fields := mutableFields(compacted)

	base := cloneTemplateData(compacted)
	clearMutableFields(&base, fields)
	baseRendered, err := executeTemplate(tmpl, base)
	if err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	fieldBudget := max(0, available-ctxmgr.EstimateTokens(baseRendered))
	if fieldBudget > 0 {
		window := &ctxmgr.Window{
			MaxTokens: fieldBudget,
			Segments:  make([]ctxmgr.Segment, 0, len(fields)),
		}
		for _, field := range fields {
			content := field.get(&compacted)
			if content == "" {
				continue
			}
			window.Segments = append(window.Segments, ctxmgr.Segment{
				Name:     field.name,
				Content:  content,
				Tokens:   ctxmgr.EstimateTokens(content),
				Priority: field.priority,
			})
		}

		kept := make(map[string]struct{}, len(window.Segments))
		for _, seg := range ctxmgr.CompactToFit(window, ctxmgr.CompactionConfig{
			Threshold:       ctxmgr.DefaultCompactionThreshold,
			PreserveDurable: true,
		}).Segments {
			kept[seg.Name] = struct{}{}
		}
		for _, field := range fields {
			if _, ok := kept[field.name]; !ok {
				field.set(&compacted, "")
			}
		}
	}

	rendered, err := executeTemplate(tmpl, compacted)
	if err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	for i := 0; i < len(fields)*4 && !renderedFitsBudget(rendered, opts); i++ {
		field := largestMutableField(compacted, fields)
		if field == nil {
			break
		}
		current := field.get(&compacted)
		target := ctxmgr.EstimateTokens(current) / 2
		if target >= ctxmgr.EstimateTokens(current) {
			target = ctxmgr.EstimateTokens(current) - 1
		}
		next := summarizeText(current, target)
		if next == current {
			next = ""
		}
		field.set(&compacted, next)
		rendered, err = executeTemplate(tmpl, compacted)
		if err != nil {
			return "", fmt.Errorf("execute template: %w", err)
		}
	}
	if renderedFitsBudget(rendered, opts) {
		return rendered, nil
	}
	return compactBodyToFit(rendered, opts.ContextBudget, preambleBudgetPrefix(opts.Preamble)), nil
}

// ApplyContextBudget compacts a fully assembled prompt to fit within the
// configured token budget, preserving opts.Preamble verbatim when present.
func ApplyContextBudget(prompt string, opts RenderOptions) string {
	if opts.ContextBudget <= 0 {
		return prompt
	}
	prefix := preservedPrefix(prompt, opts.Preamble)
	body := prompt[len(prefix):]
	return prefix + compactBodyToFit(body, opts.ContextBudget, prefix)
}

func availableBodyBudget(opts RenderOptions) int {
	return max(0, opts.ContextBudget-ctxmgr.EstimateTokens(preambleBudgetPrefix(opts.Preamble)))
}

func renderedFitsBudget(rendered string, opts RenderOptions) bool {
	return ctxmgr.EstimateTokens(preambleBudgetPrefix(opts.Preamble)+rendered) <= opts.ContextBudget
}

func preambleBudgetPrefix(preamble string) string {
	if preamble == "" {
		return ""
	}
	return preamble + "\n\n"
}

func preservedPrefix(prompt, preamble string) string {
	if preamble == "" {
		return ""
	}
	if strings.HasPrefix(prompt, preamble+"\n\n") {
		return preamble + "\n\n"
	}
	if strings.HasPrefix(prompt, preamble) {
		return preamble
	}
	return ""
}

func mutableFields(data TemplateData) []mutableField {
	fields := make([]mutableField, 0, 5+len(data.PreviousOutputs))
	order := orderedPreviousOutputKeys(data)
	for idx, key := range order {
		key := key
		fields = append(fields, mutableField{
			name:     "previous:" + key,
			priority: idx + 1,
			get: func(td *TemplateData) string {
				if td.PreviousOutputs == nil {
					return ""
				}
				return td.PreviousOutputs[key]
			},
			set: func(td *TemplateData, value string) {
				if td.PreviousOutputs == nil {
					if value == "" {
						return
					}
					td.PreviousOutputs = map[string]string{}
				}
				td.PreviousOutputs[key] = value
			},
		})
	}
	fields = append(fields,
		mutableField{
			name:     "issue-body",
			priority: 1000,
			get:      func(td *TemplateData) string { return td.Issue.Body },
			set:      func(td *TemplateData, value string) { td.Issue.Body = value },
		},
		mutableField{
			name:     "gate-result",
			priority: 900,
			get:      func(td *TemplateData) string { return td.GateResult },
			set:      func(td *TemplateData, value string) { td.GateResult = value },
		},
		mutableField{
			name:     "eval-feedback",
			priority: 850,
			get:      func(td *TemplateData) string { return td.Evaluation.Feedback },
			set:      func(td *TemplateData, value string) { td.Evaluation.Feedback = value },
		},
		mutableField{
			name:     "eval-output",
			priority: 840,
			get:      func(td *TemplateData) string { return td.Evaluation.Output },
			set:      func(td *TemplateData, value string) { td.Evaluation.Output = value },
		},
		mutableField{
			name:     "eval-criteria",
			priority: 830,
			get:      func(td *TemplateData) string { return td.Evaluation.Criteria },
			set:      func(td *TemplateData, value string) { td.Evaluation.Criteria = value },
		},
	)
	return fields
}

func orderedPreviousOutputKeys(data TemplateData) []string {
	if len(data.PreviousOutputs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(data.PreviousOutputs))
	seen := make(map[string]struct{}, len(data.PreviousOutputs))
	for _, key := range data.PreviousOutputOrder {
		if _, ok := data.PreviousOutputs[key]; !ok {
			continue
		}
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	extras := make([]string, 0, len(data.PreviousOutputs)-len(keys))
	for key := range data.PreviousOutputs {
		if _, ok := seen[key]; ok {
			continue
		}
		extras = append(extras, key)
	}
	sort.Strings(extras)
	return append(keys, extras...)
}

func clearMutableFields(data *TemplateData, fields []mutableField) {
	for _, field := range fields {
		field.set(data, "")
	}
}

func largestMutableField(data TemplateData, fields []mutableField) *mutableField {
	var selected *mutableField
	maxTokens := 0
	for i := range fields {
		tokens := ctxmgr.EstimateTokens(fields[i].get(&data))
		if tokens > maxTokens {
			maxTokens = tokens
			selected = &fields[i]
		}
	}
	return selected
}

func summarizeText(content string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	if ctxmgr.EstimateTokens(content) <= maxTokens {
		return content
	}
	maxChars := maxTokens * 4
	if maxChars <= 0 {
		return ""
	}
	notice := fmt.Sprintf("[context compacted to fit %d tokens]\n", maxTokens)
	if len(notice) >= maxChars {
		return notice[:maxChars]
	}
	remaining := maxChars - len(notice)
	if remaining <= 0 {
		return notice
	}
	if remaining <= 5 {
		return notice + content[:remaining]
	}

	ellipsis := "\n...\n"
	payload := remaining - len(ellipsis)
	if payload <= 0 {
		return notice + content[:remaining]
	}
	if ctxmgr.EstimateTokens(content) <= minSummaryTokens {
		if len(content) > payload {
			return notice + content[:payload]
		}
		return notice + content
	}
	head := payload * 3 / 4
	tail := payload - head
	if head > len(content) {
		head = len(content)
	}
	if tail > len(content)-head {
		tail = len(content) - head
	}
	return notice + content[:head] + ellipsis + content[len(content)-tail:]
}

func compactBodyToFit(body string, budget int, fixedPrefix string) string {
	if ctxmgr.EstimateTokens(fixedPrefix+body) <= budget {
		return body
	}
	for bodyBudget := max(0, budget-ctxmgr.EstimateTokens(fixedPrefix)); bodyBudget >= 0; bodyBudget-- {
		candidate := summarizeText(body, bodyBudget)
		for len(candidate) > 0 && ctxmgr.EstimateTokens(fixedPrefix+candidate) > budget {
			candidate = candidate[:len(candidate)-1]
		}
		if ctxmgr.EstimateTokens(fixedPrefix+candidate) <= budget {
			return candidate
		}
	}
	return ""
}

func cloneTemplateData(data TemplateData) TemplateData {
	out := data
	if data.PreviousOutputs != nil {
		out.PreviousOutputs = make(map[string]string, len(data.PreviousOutputs))
		for key, value := range data.PreviousOutputs {
			out.PreviousOutputs[key] = value
		}
	}
	if data.Vessel.Meta != nil {
		out.Vessel.Meta = make(map[string]string, len(data.Vessel.Meta))
		for key, value := range data.Vessel.Meta {
			out.Vessel.Meta[key] = value
		}
	}
	if data.Issue.Labels != nil {
		out.Issue.Labels = append([]string(nil), data.Issue.Labels...)
	}
	if data.PreviousOutputOrder != nil {
		out.PreviousOutputOrder = append([]string(nil), data.PreviousOutputOrder...)
	}
	return out
}
