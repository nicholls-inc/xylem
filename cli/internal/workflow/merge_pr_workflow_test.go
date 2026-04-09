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

func TestSmoke_S1_MergePRWorkflowUsesAutoFlag(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller() failed")

	workflowPath := filepath.Join(filepath.Dir(file), "..", "..", "..", ".xylem", "workflows", "merge-pr.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	var mergePhase *Phase
	for i := range wf.Phases {
		if wf.Phases[i].Name == "merge" {
			mergePhase = &wf.Phases[i]
			break
		}
	}
	require.NotNil(t, mergePhase, "workflow %q missing merge phase", wf.Name)
	assert.Equal(t, "command", mergePhase.Type)
	assert.Contains(t, mergePhase.Run, "--auto")
	assert.NotContains(t, mergePhase.Run, "--admin")
}
