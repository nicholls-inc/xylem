package workflow

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

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
