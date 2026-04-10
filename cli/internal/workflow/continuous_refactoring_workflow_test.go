package workflow

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSmoke_S8_ContinuousRefactoringWorkflowUsesTemplateParamsAndDedupeCommandPhases(t *testing.T) {
	t.Parallel()

	workflowPath := checkedInWorkflowPath(t, "continuous-refactoring.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	assert.Equal(t, "continuous-refactoring", wf.Name)

	refactorDedupe := findPhaseByName(t, wf.Phases, "dedupe_refactor")
	assert.Equal(t, "command", refactorDedupe.Type)
	require.NotNil(t, refactorDedupe.NoOp)
	assert.Equal(t, "XYLEM_NOOP", refactorDedupe.NoOp.Match)
	assert.Contains(t, refactorDedupe.Run, `{{index .Source.Params "mode"}}`)
	assert.Contains(t, refactorDedupe.Run, `--label refactor`)
	assert.Contains(t, refactorDedupe.Run, `"[refactor]" in:title`)

	fileDietDedupe := findPhaseByName(t, wf.Phases, "dedupe_file_diet")
	assert.Equal(t, "command", fileDietDedupe.Type)
	require.NotNil(t, fileDietDedupe.NoOp)
	assert.Equal(t, "XYLEM_NOOP", fileDietDedupe.NoOp.Match)
	assert.Contains(t, fileDietDedupe.Run, `{{index .Source.Params "mode"}}`)
	assert.Contains(t, fileDietDedupe.Run, `{{index .PreviousOutputs "split_plan"}}`)
	assert.Contains(t, fileDietDedupe.Run, `--label file-diet`)
	assert.Contains(t, fileDietDedupe.Run, `[file-diet] $target_file`)
	assert.Contains(t, fileDietDedupe.Run, `in:title`)
}

func TestSmoke_S9_ContinuousRefactoringPromptsReferenceConfiguredParamsLabelsAndIssueCreationRules(t *testing.T) {
	t.Parallel()

	analyzePromptPath := checkedInWorkflowPromptPath(t, "continuous-refactoring", "analyze.md")
	analyzePrompt, err := os.ReadFile(analyzePromptPath)
	require.NoError(t, err, "ReadFile(%q)", analyzePromptPath)
	assert.Contains(t, string(analyzePrompt), `{{index .Source.Params "source_dirs"}}`)
	assert.Contains(t, string(analyzePrompt), `{{index .Source.Params "file_extensions"}}`)
	assert.Contains(t, string(analyzePrompt), `{{index .Source.Params "exclude_patterns"}}`)
	assert.Contains(t, string(analyzePrompt), `{{index .Source.Params "max_issues_per_run"}}`)

	proposePromptPath := checkedInWorkflowPromptPath(t, "continuous-refactoring", "propose.md")
	proposePrompt, err := os.ReadFile(proposePromptPath)
	require.NoError(t, err, "ReadFile(%q)", proposePromptPath)
	assert.Contains(t, string(proposePrompt), "Title: [refactor]")
	assert.Contains(t, string(proposePrompt), "Labels: refactor, enhancement, ready-for-work")
	assert.Contains(t, string(proposePrompt), `{{index .Source.Params "max_issues_per_run"}}`)

	createRefactorIssuePromptPath := checkedInWorkflowPromptPath(t, "continuous-refactoring", "create_refactor_issue.md")
	createRefactorIssuePrompt, err := os.ReadFile(createRefactorIssuePromptPath)
	require.NoError(t, err, "ReadFile(%q)", createRefactorIssuePromptPath)
	assert.Contains(t, string(createRefactorIssuePrompt), "gh issue create --repo {{.Repo.Slug}}")
	assert.Contains(t, string(createRefactorIssuePrompt), "Apply labels `refactor`, `enhancement`, and `ready-for-work`.")
	assert.Contains(t, string(createRefactorIssuePrompt), `{{index .Source.Params "max_issues_per_run"}}`)

	measurePromptPath := checkedInWorkflowPromptPath(t, "continuous-refactoring", "measure.md")
	measurePrompt, err := os.ReadFile(measurePromptPath)
	require.NoError(t, err, "ReadFile(%q)", measurePromptPath)
	assert.Contains(t, string(measurePrompt), `{{index .Source.Params "source_dirs"}}`)
	assert.Contains(t, string(measurePrompt), `{{index .Source.Params "file_extensions"}}`)
	assert.Contains(t, string(measurePrompt), `{{index .Source.Params "exclude_patterns"}}`)
	assert.Contains(t, string(measurePrompt), `{{index .Source.Params "loc_threshold"}}`)

	splitPlanPromptPath := checkedInWorkflowPromptPath(t, "continuous-refactoring", "split_plan.md")
	splitPlanPrompt, err := os.ReadFile(splitPlanPromptPath)
	require.NoError(t, err, "ReadFile(%q)", splitPlanPromptPath)
	assert.Contains(t, string(splitPlanPrompt), "Title: [file-diet]")
	assert.Contains(t, string(splitPlanPrompt), "Target file:")
	assert.Contains(t, string(splitPlanPrompt), "Labels: file-diet, enhancement, ready-for-work")

	fileIssuePromptPath := checkedInWorkflowPromptPath(t, "continuous-refactoring", "file_issue.md")
	fileIssuePrompt, err := os.ReadFile(fileIssuePromptPath)
	require.NoError(t, err, "ReadFile(%q)", fileIssuePromptPath)
	assert.Contains(t, string(fileIssuePrompt), "gh issue create --repo {{.Repo.Slug}}")
	assert.Contains(t, string(fileIssuePrompt), "Create exactly one issue from the split-plan draft.")
	assert.Contains(t, string(fileIssuePrompt), "Apply labels `file-diet`, `enhancement`, and `ready-for-work`.")
}
