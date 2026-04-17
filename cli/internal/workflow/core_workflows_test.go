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

func coreProfileWorkflowPath(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller() failed")

	return filepath.Join(filepath.Dir(file), "..", "profiles", "core", "workflows", name)
}

// TestCoreWorkflowGates_UseValidationTestTemplate verifies that the command gates
// in fix-bug and implement-feature use the {{ .Validation.Test }} template rather
// than the hard-coded "make test" command that broke 100% of vessels (issue #562).
func TestCoreWorkflowGates_UseValidationTestTemplate(t *testing.T) {
	t.Parallel()

	type phaseExpect struct {
		name    string
		retries int
	}

	cases := []struct {
		workflow string
		phases   []phaseExpect
	}{
		{
			workflow: "fix-bug.yaml",
			phases: []phaseExpect{
				{name: "implement", retries: 2},
				{name: "verify", retries: 1},
			},
		},
		{
			workflow: "implement-feature.yaml",
			phases: []phaseExpect{
				{name: "implement", retries: 2},
				{name: "verify", retries: 1},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.workflow, func(t *testing.T) {
			t.Parallel()

			workflowPath := coreProfileWorkflowPath(t, tc.workflow)
			data, err := os.ReadFile(workflowPath)
			require.NoError(t, err, "ReadFile(%q)", workflowPath)

			var wf Workflow
			require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

			for _, pc := range tc.phases {
				pc := pc
				t.Run(pc.name, func(t *testing.T) {
					t.Parallel()

					phase := findPhaseByName(t, wf.Phases, pc.name)
					require.NotNil(t, phase.Gate, "%s phase %q missing gate", tc.workflow, pc.name)

					// Regression guard: must not fall back to the hard-coded make test command.
					assert.NotContains(t, phase.Gate.Run, "make test",
						"%s phase %q gate must not use 'make test'", tc.workflow, pc.name)

					// Fix present: gate must use the per-repo Validation.Test template.
					assert.Contains(t, phase.Gate.Run, `{{ if .Validation.Test }}`,
						"%s phase %q gate must use .Validation.Test template", tc.workflow, pc.name)

					// Fallback guard: misconfiguration must fail loudly, not silently pass.
					assert.Contains(t, phase.Gate.Run, "exit 1",
						"%s phase %q gate must include fallback exit 1", tc.workflow, pc.name)

					// Retries: a zero value would silently disable the gate.
					assert.Equal(t, pc.retries, phase.Gate.Retries,
						"%s phase %q gate retries", tc.workflow, pc.name)
				})
			}
		})
	}
}
