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

func TestSmoke_S8_CutReleaseWorkflowUsesAdminMergeAndNoopGuards(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller() failed")

	workflowPath := filepath.Join(filepath.Dir(file), "..", "..", "..", ".xylem", "workflows", "cut-release.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)
	require.Len(t, wf.Phases, 3)

	assert.Equal(t, "check", wf.Phases[0].Name)
	require.NotNil(t, wf.Phases[0].NoOp)
	assert.Equal(t, "XYLEM_NOOP", wf.Phases[0].NoOp.Match)
	assert.Contains(t, wf.Phases[0].Run, `autorelease: pending`)

	assert.Equal(t, "verify", wf.Phases[1].Name)
	require.NotNil(t, wf.Phases[1].NoOp)
	assert.Equal(t, "XYLEM_NOOP", wf.Phases[1].NoOp.Match)
	assert.Contains(t, wf.Phases[1].Run, `block-release`)
	assert.Contains(t, wf.Phases[1].Run, `statusCheckRollup`)
	assert.Contains(t, wf.Phases[1].Run, `'"conclusion":"SUCCESS"'`)

	assert.Equal(t, "merge", wf.Phases[2].Name)
	assert.Equal(t, "command", wf.Phases[2].Type)
	assert.Contains(t, wf.Phases[2].Run, "--admin")
	assert.Contains(t, wf.Phases[2].Run, "{{.Repo.Slug}}")
}
