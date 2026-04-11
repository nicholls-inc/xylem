package workflow

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func checkedInWorkflowPromptPath(t *testing.T, workflowName, promptName string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller() failed")

	return filepath.Join(filepath.Dir(file), "..", "..", "..", ".xylem", "prompts", workflowName, promptName)
}

func TestSmoke_S5_FixPRChecksWorkflowUsesValidationTemplateCommands(t *testing.T) {
	t.Parallel()

	workflowPath := checkedInWorkflowPath(t, "fix-pr-checks.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	fixPhase := findPhaseByName(t, wf.Phases, "fix")
	require.NotNil(t, fixPhase.Gate, "workflow %q missing fix gate", wf.Name)
	assert.Equal(t, "command", fixPhase.Gate.Type)
	assert.Contains(t, fixPhase.Gate.Run, "set -e")
	assert.Contains(t, fixPhase.Gate.Run, "{{ .Validation.Format }}")
	assert.Contains(t, fixPhase.Gate.Run, "{{ .Validation.Lint }}")
	assert.Contains(t, fixPhase.Gate.Run, "{{ .Validation.Build }}")
	assert.Contains(t, fixPhase.Gate.Run, "{{ .Validation.Test }}")
	assert.NotContains(t, fixPhase.Gate.Run, "go build ./cmd/xylem")
}

func TestSmoke_S6_ResolveConflictsWorkflowUsesRepoAndValidationTemplates(t *testing.T) {
	t.Parallel()

	workflowPath := checkedInWorkflowPath(t, "resolve-conflicts.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	mergeMainPhase := findPhaseByName(t, wf.Phases, "merge_main")
	assert.Equal(t, "command", mergeMainPhase.Type)
	require.NotNil(t, mergeMainPhase.NoOp, "workflow %q missing merge_main noop", wf.Name)
	assert.Equal(t, "XYLEM_NOOP", mergeMainPhase.NoOp.Match)
	assert.Contains(t, mergeMainPhase.Run, "head_branch=\"$(gh pr view {{ .Issue.Number }} --repo {{ .Repo.Slug }} --json headRefName --jq '.headRefName')\"")
	assert.Contains(t, mergeMainPhase.Run, "git fetch origin \"$head_branch\" \"{{ .Repo.DefaultBranch }}\"")
	assert.Contains(t, mergeMainPhase.Run, "git branch -D \"$head_branch\" >/dev/null 2>&1 || true")
	assert.Contains(t, mergeMainPhase.Run, "gh pr checkout {{ .Issue.Number }} --repo {{ .Repo.Slug }}")
	assert.Contains(t, mergeMainPhase.Run, "git reset --hard \"origin/$head_branch\"")
	assert.Contains(t, mergeMainPhase.Run, "git merge origin/{{ .Repo.DefaultBranch }} --no-commit --no-ff")
	assert.Contains(t, mergeMainPhase.Run, "git diff --name-only --diff-filter=U")
	assert.True(t, commandContainsInOrder(
		mergeMainPhase.Run,
		"head_branch=\"$(gh pr view {{ .Issue.Number }} --repo {{ .Repo.Slug }} --json headRefName --jq '.headRefName')\"",
		"git fetch origin \"$head_branch\" \"{{ .Repo.DefaultBranch }}\"",
		"git branch -D \"$head_branch\" >/dev/null 2>&1 || true",
		"gh pr checkout {{ .Issue.Number }} --repo {{ .Repo.Slug }}",
		"git reset --hard \"origin/$head_branch\"",
		"git merge origin/{{ .Repo.DefaultBranch }} --no-commit --no-ff",
	))

	resolvePhase := findPhaseByName(t, wf.Phases, "resolve")
	require.NotNil(t, resolvePhase.Gate, "workflow %q missing resolve gate", wf.Name)
	assert.Equal(t, "command", resolvePhase.Gate.Type)
	assert.Contains(t, resolvePhase.Gate.Run, "{{ .Repo.Slug }}")
	assert.Contains(t, resolvePhase.Gate.Run, "{{ .Repo.DefaultBranch }}")
	assert.Contains(t, resolvePhase.Gate.Run, "{{ .Validation.Format }}")
	assert.Contains(t, resolvePhase.Gate.Run, "{{ .Validation.Lint }}")
	assert.Contains(t, resolvePhase.Gate.Run, "{{ .Validation.Build }}")
	assert.Contains(t, resolvePhase.Gate.Run, "{{ .Validation.Test }}")
	assert.NotContains(t, resolvePhase.Gate.Run, "go build ./cmd/xylem")
	assert.NotContains(t, resolvePhase.Gate.Run, "--repo nicholls-inc/xylem")
}

func TestSmoke_S7_ResolveConflictsPromptsRelyOnDeterministicMergePhase(t *testing.T) {
	t.Parallel()

	analyzePromptPath := checkedInWorkflowPromptPath(t, "resolve-conflicts", "analyze.md")
	analyzePrompt, err := os.ReadFile(analyzePromptPath)
	require.NoError(t, err, "ReadFile(%q)", analyzePromptPath)
	assert.Contains(t, string(analyzePrompt), "{{.PreviousOutputs.merge_main}}")
	assert.Contains(t, string(analyzePrompt), "Do not rerun the merge.")
	assert.NotContains(t, string(analyzePrompt), "Run `gh pr checkout")
	assert.NotContains(t, string(analyzePrompt), "Run `git fetch origin main && git merge origin/main --no-commit`")

	resolvePromptPath := checkedInWorkflowPromptPath(t, "resolve-conflicts", "resolve.md")
	resolvePrompt, err := os.ReadFile(resolvePromptPath)
	require.NoError(t, err, "ReadFile(%q)", resolvePromptPath)
	assert.Contains(t, string(resolvePrompt), "{{.PreviousOutputs.merge_main}}")
	assert.Contains(t, string(resolvePrompt), "do not rely on self-reporting that a merge happened")
	assert.Contains(t, string(resolvePrompt), "complete the in-progress merge before validation")
	assert.Contains(t, string(resolvePrompt), "git commit --no-edit")

	pushPromptPath := checkedInWorkflowPromptPath(t, "resolve-conflicts", "push.md")
	pushPrompt, err := os.ReadFile(pushPromptPath)
	require.NoError(t, err, "ReadFile(%q)", pushPromptPath)
	assert.Contains(t, string(pushPrompt), "Push the already-completed merge resolution")
	assert.NotContains(t, string(pushPrompt), "git add -A && git commit")
}
