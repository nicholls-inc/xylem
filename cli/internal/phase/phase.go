package phase

import (
	"bytes"
	"fmt"
	"text/template"
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
)

// TemplateData holds all data available to phase prompt templates.
type TemplateData struct {
	Date            string
	Issue           IssueData
	Phase           PhaseData
	PreviousOutputs map[string]string // phase name → output text
	GateResult      string            // most recent gate command output
	Evaluation      EvaluationData
	Vessel          VesselData
	Repo            RepoData
	Source          SourceData
	Validation      ValidationData
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
	out := data

	// Deep copy and truncate PreviousOutputs.
	if data.PreviousOutputs != nil {
		out.PreviousOutputs = make(map[string]string, len(data.PreviousOutputs))
		for k, v := range data.PreviousOutputs {
			out.PreviousOutputs[k] = TruncateOutput(v, MaxPreviousOutputLen)
		}
	}

	if data.Vessel.Meta != nil {
		out.Vessel.Meta = make(map[string]string, len(data.Vessel.Meta))
		for k, v := range data.Vessel.Meta {
			out.Vessel.Meta[k] = v
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
	tmpl, err := template.New("phase").Option("missingkey=zero").Parse(templateContent)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	prepared := prepareData(data)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, prepared); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}
