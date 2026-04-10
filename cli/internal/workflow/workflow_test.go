package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func writeWorkflowFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write workflow file: %v", err)
	}

	return path
}

func createPromptFile(t *testing.T, dir, name string) {
	t.Helper()

	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("create prompt dir: %v", err)
	}
	if err := os.WriteFile(full, []byte("prompt content"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}

	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %q", want, err.Error())
	}
}

// chdirTemp changes the working directory to dir for the duration of the test.
func chdirTemp(t *testing.T, dir string) {
	t.Helper()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %q: %v", dir, err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

func tierPtr(s string) *string {
	return &s
}

func TestSmoke_S1_WorkflowTierRoundTripPreservesNilAndEmptyStringDistinction(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")
	createPromptFile(t, dir, "prompts/plan.md")
	createPromptFile(t, dir, "prompts/apply.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
tier: high
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: plan
    prompt_file: prompts/plan.md
    max_turns: 10
    tier: ""
  - name: apply
    prompt_file: prompts/apply.md
    max_turns: 10
    tier: low
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, tierPtr("high"), got.Tier)
	require.Nil(t, got.Phases[0].Tier)
	assert.Equal(t, tierPtr(""), got.Phases[1].Tier)
	assert.Equal(t, tierPtr("low"), got.Phases[2].Tier)

	data, err := yaml.Marshal(got)
	require.NoError(t, err)

	var roundTripped Workflow
	require.NoError(t, yaml.Unmarshal(data, &roundTripped))
	require.Equal(t, got.Tier, roundTripped.Tier)
	require.Nil(t, roundTripped.Phases[0].Tier)
	assert.Equal(t, got.Phases[1].Tier, roundTripped.Phases[1].Tier)
	assert.Equal(t, got.Phases[2].Tier, roundTripped.Phases[2].Tier)
}

func TestSmoke_S11_GateWithoutEvidenceMetadataLoadsCleanlyWithNilEvidenceField(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, got.Phases[0].Gate)
	assert.Nil(t, got.Phases[0].Gate.Evidence)
}

func TestSmoke_S12_GateWithValidEvidenceMetadataParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
      evidence:
        claim: "All tests pass"
        level: behaviorally_checked
        checker: "go test"
        trust_boundary: "Package-level only"
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, got.Phases[0].Gate)
	require.NotNil(t, got.Phases[0].Gate.Evidence)

	e := got.Phases[0].Gate.Evidence
	assert.Equal(t, "All tests pass", e.Claim)
	assert.Equal(t, "behaviorally_checked", e.Level)
	assert.Equal(t, "go test", e.Checker)
	assert.Equal(t, "Package-level only", e.TrustBoundary)
}

func TestSmoke_S13_GateWithInvalidEvidenceLevelIsRejectedByValidateGate(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
      evidence:
        claim: "All tests pass"
        level: high
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `high`)
	assert.Contains(t, err.Error(), `gate evidence level "high" is not valid`)
	assert.Contains(t, err.Error(), "must be proved, mechanically_checked, behaviorally_checked, or observed_in_situ")
}

func TestLoadWorkflowGateRejectsUntypedEvidenceLevelKeyword(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
      evidence:
        claim: "All tests pass"
        level: untyped
`)

	_, err := Load(path)
	requireErrorContains(t, err, `gate evidence level "untyped" is not valid`)
	requireErrorContains(t, err, "must be proved, mechanically_checked, behaviorally_checked, or observed_in_situ")
}

func TestSmoke_S14_GateWithPartialEvidenceClaimAndLevelOnlyParsesWithoutError(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
      evidence:
        claim: "Tests pass"
        level: mechanically_checked
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, got.Phases[0].Gate)
	require.NotNil(t, got.Phases[0].Gate.Evidence)

	e := got.Phases[0].Gate.Evidence
	assert.Equal(t, "", e.Checker)
	assert.Equal(t, "", e.TrustBoundary)
}

func TestSmoke_S15_PhaseEvaluatorConfigParsesAndValidates(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    evaluator:
      max_retries: 2
      pass_threshold: 0.8
      criteria:
        - name: coverage
          weight: 0.6
          threshold: 0.8
        - name: correctness
          weight: 0.4
          threshold: 0.9
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, got.Phases[0].Evaluator)

	eval := got.Phases[0].Evaluator
	assert.Equal(t, 2, eval.MaxRetries)
	assert.Equal(t, 0.8, eval.PassThreshold)
	require.Len(t, eval.Criteria, 2)
	assert.Equal(t, "coverage", eval.Criteria[0].Name)
	assert.Equal(t, 0.6, eval.Criteria[0].Weight)
	assert.Equal(t, 0.8, eval.Criteria[0].Threshold)
	assert.Equal(t, "correctness", eval.Criteria[1].Name)
	assert.Equal(t, 0.4, eval.Criteria[1].Weight)
	assert.Equal(t, 0.9, eval.Criteria[1].Threshold)
}

func TestSmoke_S16_PhaseEvaluatorRejectsCriteriaWeightSumsOutsideTolerance(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    evaluator:
      criteria:
        - name: coverage
          weight: 0.7
          threshold: 0.8
        - name: correctness
          weight: 0.2
          threshold: 0.9
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `phase "analyze": evaluator: criteria weights must sum to 1.0 +/- 0.01`)
}

func TestSmoke_S17_PhaseEvaluatorRejectsThresholdsOutsideUnitInterval(t *testing.T) {
	tests := []struct {
		name          string
		evaluatorYAML string
		wantErr       string
	}{
		{
			name: "criterion threshold out of range",
			evaluatorYAML: `    evaluator:
      criteria:
        - name: coverage
          weight: 1.0
          threshold: 1.1
`,
			wantErr: `phase "analyze": evaluator: criterion "coverage" threshold must be in [0, 1]`,
		},
		{
			name: "pass threshold out of range",
			evaluatorYAML: `    evaluator:
      pass_threshold: 1.1
      criteria:
        - name: coverage
          weight: 1.0
          threshold: 0.8
`,
			wantErr: `phase "analyze": evaluator: pass_threshold must be in [0, 1]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)
			createPromptFile(t, dir, "prompts/analyze.md")

			path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`+tt.evaluatorYAML)

			_, err := Load(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSmoke_S18_PhaseEvaluatorRejectsNegativeMaxRetries(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    evaluator:
      max_retries: -1
      criteria:
        - name: coverage
          weight: 1.0
          threshold: 0.8
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `phase "analyze": evaluator: max_retries must be non-negative`)
}

func TestSmoke_S19_WorkflowWithoutEvaluatorStillLoadsWithNilEvaluator(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.Len(t, got.Phases, 1)
	assert.Nil(t, got.Phases[0].Evaluator)
	assert.Equal(t, "analyze", got.Phases[0].Name)
	assert.Equal(t, "prompts/analyze.md", got.Phases[0].PromptFile)
	assert.Equal(t, 10, got.Phases[0].MaxTurns)
}

func TestLoadWorkflowEvaluatorRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name          string
		evaluatorYAML string
		wantErr       string
	}{
		{
			name: "negative criterion weight",
			evaluatorYAML: `    evaluator:
      criteria:
        - name: coverage
          weight: -0.1
          threshold: 0.8
        - name: correctness
          weight: 1.1
          threshold: 0.9
`,
			wantErr: `phase "analyze": evaluator: criterion "coverage" weight must be non-negative`,
		},
		{
			name: "duplicate criterion name",
			evaluatorYAML: `    evaluator:
      criteria:
        - name: coverage
          weight: 0.5
          threshold: 0.8
        - name: coverage
          weight: 0.5
          threshold: 0.9
`,
			wantErr: `phase "analyze": evaluator: duplicate criterion name "coverage"`,
		},
		{
			name: "blank criterion name",
			evaluatorYAML: `    evaluator:
      criteria:
        - name: ""
          weight: 1.0
          threshold: 0.8
`,
			wantErr: `phase "analyze": evaluator: criteria[0].name is required`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)
			createPromptFile(t, dir, "prompts/analyze.md")

			path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`+tt.evaluatorYAML)

			_, err := Load(path)
			requireErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadWorkflowAllowAdditiveProtectedWritesDefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	got, err := Load(path)
	require.NoError(t, err)
	assert.False(t, got.AllowAdditiveProtectedWrites)
	assert.Equal(t, policy.Delivery, got.Class)
}

func TestLoadWorkflowAllowAdditiveProtectedWritesParsesTrue(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
allow_additive_protected_writes: true
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	got, err := Load(path)
	require.NoError(t, err)
	assert.True(t, got.AllowAdditiveProtectedWrites)
	assert.Equal(t, policy.HarnessMaintenance, got.Class)
}

func TestLoadWorkflowAllowCanonicalProtectedWrites(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "defaults false",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			want: false,
		},
		{
			name: "parses true",
			yaml: `name: test-workflow
allow_canonical_protected_writes: true
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)
			createPromptFile(t, dir, "prompts/analyze.md")

			path := writeWorkflowFile(t, dir, "test-workflow", tt.yaml)

			got, err := Load(path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.AllowCanonicalProtectedWrites)
			if tt.want {
				assert.Equal(t, policy.HarnessMaintenance, got.Class)
				return
			}
			assert.Equal(t, policy.Delivery, got.Class)
		})
	}
}

func TestLoadWorkflowClassParsesExplicitValue(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
class: ops
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	got, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, policy.Ops, got.Class)
}

func TestSmoke_S4_WorkflowClassDefaultsToDeliveryWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, policy.Delivery, got.Class)
	assert.False(t, got.AllowAdditiveProtectedWrites)
	assert.False(t, got.AllowCanonicalProtectedWrites)
}

func TestSmoke_S5_LegacyProtectedWriteFlagsPromoteHarnessMaintenance(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
allow_canonical_protected_writes: true
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, policy.HarnessMaintenance, got.Class)
	assert.False(t, got.AllowAdditiveProtectedWrites)
	assert.True(t, got.AllowCanonicalProtectedWrites)
}

func TestSmoke_S6_ExplicitOpsClassLoadsWithoutLegacyFlags(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
class: ops
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, policy.Ops, got.Class)
	assert.False(t, got.AllowAdditiveProtectedWrites)
	assert.False(t, got.AllowCanonicalProtectedWrites)
}

func TestLoadWorkflowClassRejectsUnknownValue(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
class: runtime
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	_, err := Load(path)
	requireErrorContains(t, err, `"class" is invalid: unknown workflow class "runtime"`)
}

func TestSmoke_S7_InconsistentClassAndLegacyFlagsReturnStructuredError(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
class: delivery
allow_additive_protected_writes: true
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"class" "delivery" conflicts with legacy protected write flags`)
	assert.Contains(t, err.Error(), "delivery requires both legacy flags to be false")
}

func TestLoadWorkflowClassRejectsLegacyFlagConflict(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "delivery conflicts with true legacy flag",
			yaml: `name: test-workflow
class: delivery
allow_additive_protected_writes: true
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			wantErr: `"class" "delivery" conflicts with legacy protected write flags: delivery requires both legacy flags to be false`,
		},
		{
			name: "harness maintenance conflicts with false legacy flags",
			yaml: `name: test-workflow
class: harness-maintenance
allow_additive_protected_writes: false
allow_canonical_protected_writes: false
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			wantErr: `"class" "harness-maintenance" conflicts with legacy protected write flags: harness-maintenance requires at least one legacy flag to be true`,
		},
		{
			name: "ops cannot be combined with legacy flags",
			yaml: `name: test-workflow
class: ops
allow_additive_protected_writes: false
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			wantErr: `"class" "ops" conflicts with legacy protected write flags: ops cannot be combined with legacy protected write flags`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)
			createPromptFile(t, dir, "prompts/analyze.md")

			path := writeWorkflowFile(t, dir, "test-workflow", tt.yaml)
			_, err := Load(path)
			requireErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadWorkflowClassAllowsConsistentLegacyFlags(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		wantClass     policy.Class
		wantAdditive  bool
		wantCanonical bool
	}{
		{
			name: "delivery with explicit additive false legacy flag",
			yaml: `name: test-workflow
class: delivery
allow_additive_protected_writes: false
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			wantClass:     policy.Delivery,
			wantAdditive:  false,
			wantCanonical: false,
		},
		{
			name: "delivery with both explicit false legacy flags",
			yaml: `name: test-workflow
class: delivery
allow_additive_protected_writes: false
allow_canonical_protected_writes: false
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			wantClass:     policy.Delivery,
			wantAdditive:  false,
			wantCanonical: false,
		},
		{
			name: "harness maintenance with additive legacy flag",
			yaml: `name: test-workflow
class: harness-maintenance
allow_additive_protected_writes: true
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			wantClass:     policy.HarnessMaintenance,
			wantAdditive:  true,
			wantCanonical: false,
		},
		{
			name: "harness maintenance with canonical legacy flag",
			yaml: `name: test-workflow
class: harness-maintenance
allow_canonical_protected_writes: true
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			wantClass:     policy.HarnessMaintenance,
			wantAdditive:  false,
			wantCanonical: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)
			createPromptFile(t, dir, "prompts/analyze.md")

			path := writeWorkflowFile(t, dir, "test-workflow", tt.yaml)
			got, err := Load(path)
			require.NoError(t, err)
			assert.Equal(t, tt.wantClass, got.Class)
			assert.Equal(t, tt.wantAdditive, got.AllowAdditiveProtectedWrites)
			assert.Equal(t, tt.wantCanonical, got.AllowCanonicalProtectedWrites)
		})
	}
}

func TestLoadWorkflowGateWithEvidenceAndEmptyLevel(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: "go test ./..."
      evidence:
        claim: "Analyzer ran"
        checker: "go test"
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Phases[0].Gate == nil || got.Phases[0].Gate.Evidence == nil {
		t.Fatal("Gate.Evidence = nil, want evidence metadata")
	}

	if got.Phases[0].Gate.Evidence.Level != "" {
		t.Fatalf("Evidence.Level = %q, want empty string", got.Phases[0].Gate.Evidence.Level)
	}
}

func TestLoadWorkflowClassParsesHarnessMaintenance(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/plan.md")

	path := writeWorkflowFile(t, dir, "adapt-repo", `name: adapt-repo
class: harness-maintenance
phases:
  - name: plan
    prompt_file: prompts/plan.md
    max_turns: 10
`)

	got, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ClassHarnessMaintenance, got.Class)
}

func TestLoadWorkflowLiveHTTPGateParsesAndValidates(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: live
      retries: 1
      live:
        mode: http
        timeout: "30s"
        http:
          base_url: "http://127.0.0.1:3000"
          steps:
            - name: health
              url: /health
              expect_status: 200
              max_latency: "2s"
              expect_body_regex: '"status":"ok"'
              expect_headers:
                - name: Content-Type
                  regex: 'application/json'
              expect_json:
                - path: $.status
                  equals: ok
`)

	got, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, got.Phases[0].Gate)
	require.NotNil(t, got.Phases[0].Gate.Live)
	assert.Equal(t, "http", got.Phases[0].Gate.Live.Mode)
	require.Len(t, got.Phases[0].Gate.Live.HTTP.Steps, 1)
	assert.Equal(t, "/health", got.Phases[0].Gate.Live.HTTP.Steps[0].URL)
}

func TestLoadWorkflowLiveGateRejectsMissingModeSpecificConfig(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: live
      live:
        mode: browser
`)

	_, err := Load(path)
	requireErrorContains(t, err, `live.browser is required`)
}

func TestLoadWorkflowLiveGateRejectsInvalidCommandAssertDuration(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: live
      live:
        mode: command+assert
        command_assert:
          run: "cat status.json"
          timeout: "not-a-duration"
          expect_json:
            - path: $.status
              equals: ok
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid live.command_assert.timeout`)
}

func TestLoadWorkflowLiveGateRejectsInvalidCommandAssertJSONPath(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "test-workflow", `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: live
      live:
        mode: command+assert
        command_assert:
          run: "cat status.json"
          timeout: "30s"
          expect_json:
            - path: status
              equals: ok
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `live.command_assert.expect_json[0].path must start with '$'`)
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name         string
		workflowName string // filename stem; defaults to "fix-bug"
		yaml         string
		prompts      []string // prompt files to create relative to repo root (cwd)
		wantErr      string   // empty means no error expected
		checkFunc    func(t *testing.T, s *Workflow)
	}{
		{
			name:         "valid workflow file",
			workflowName: "fix-bug",
			yaml: `name: fix-bug
description: Fix a bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
`,
			prompts: []string{"prompts/analyze.md", "prompts/implement.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if s.Name != "fix-bug" {
					t.Fatalf("Name = %q, want fix-bug", s.Name)
				}
				if s.Description != "Fix a bug" {
					t.Fatalf("Description = %q, want 'Fix a bug'", s.Description)
				}
				if len(s.Phases) != 2 {
					t.Fatalf("len(Phases) = %d, want 2", len(s.Phases))
				}
				if s.Phases[0].Name != "analyze" {
					t.Fatalf("Phases[0].Name = %q, want analyze", s.Phases[0].Name)
				}
				if s.Phases[1].MaxTurns != 20 {
					t.Fatalf("Phases[1].MaxTurns = %d, want 20", s.Phases[1].MaxTurns)
				}
				if s.Class != policy.Delivery {
					t.Fatalf("Class = %q, want %q", s.Class, policy.Delivery)
				}
			},
		},
		{
			name:         "valid workflow with all features",
			workflowName: "deploy",
			yaml: `name: deploy
description: Deploy with gates
allow_additive_protected_writes: true
allow_canonical_protected_writes: true
phases:
  - name: build
    prompt_file: prompts/build.md
    max_turns: 15
    allowed_tools: Bash,Read
    gate:
      type: command
      run: make test
      retries: 2
      retry_delay: "5s"
  - name: review
    prompt_file: prompts/review.md
    max_turns: 5
    gate:
      type: label
      wait_for: approved
      timeout: "12h"
      poll_interval: "30s"
`,
			prompts: []string{"prompts/build.md", "prompts/review.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if s.Phases[0].Gate.Type != "command" {
					t.Fatalf("gate type = %q, want command", s.Phases[0].Gate.Type)
				}
				if !s.AllowAdditiveProtectedWrites {
					t.Fatal("AllowAdditiveProtectedWrites = false, want true")
				}
				if !s.AllowCanonicalProtectedWrites {
					t.Fatal("AllowCanonicalProtectedWrites = false, want true")
				}
				if s.Class != policy.HarnessMaintenance {
					t.Fatalf("Class = %q, want %q", s.Class, policy.HarnessMaintenance)
				}
				if s.Phases[0].Gate.Retries != 2 {
					t.Fatalf("gate retries = %d, want 2", s.Phases[0].Gate.Retries)
				}
				if *s.Phases[0].AllowedTools != "Bash,Read" {
					t.Fatalf("AllowedTools = %q, want Bash,Read", *s.Phases[0].AllowedTools)
				}
				if s.Phases[1].Gate.Type != "label" {
					t.Fatalf("gate type = %q, want label", s.Phases[1].Gate.Type)
				}
				if s.Phases[1].Gate.WaitFor != "approved" {
					t.Fatalf("gate wait_for = %q, want approved", s.Phases[1].Gate.WaitFor)
				}
			},
		},
		{
			name:         "valid workflow with model",
			workflowName: "fix-bug",
			yaml: `name: fix-bug
description: Fix a bug
model: claude-sonnet-4-20250514
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
    model: claude-opus-4-20250514
`,
			prompts: []string{"prompts/analyze.md", "prompts/implement.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if s.Model == nil || *s.Model != "claude-sonnet-4-20250514" {
					t.Fatalf("Workflow.Model = %v, want claude-sonnet-4-20250514", s.Model)
				}
				if s.Phases[0].Model != nil {
					t.Fatalf("Phases[0].Model = %v, want nil", s.Phases[0].Model)
				}
				if s.Phases[1].Model == nil || *s.Phases[1].Model != "claude-opus-4-20250514" {
					t.Fatalf("Phases[1].Model = %v, want claude-opus-4-20250514", s.Phases[1].Model)
				}
			},
		},
		{
			name:         "missing phases key",
			workflowName: "test-workflow",
			yaml:         "name: test-workflow\n",
			wantErr:      `"phases" is required`,
		},
		{
			name:         "empty name",
			workflowName: "test-workflow",
			yaml: `phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `"name" is required`,
		},
		{
			name:         "duplicate phase names",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: implement
    prompt_file: prompts/a.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/b.md
    max_turns: 10
`,
			prompts: []string{"prompts/a.md", "prompts/b.md"},
			wantErr: `duplicate phase name "implement"`,
		},
		{
			name:         "missing prompt_file",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    max_turns: 10
`,
			wantErr: "prompt_file is required",
		},
		{
			name:         "non-existent prompt_file",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/missing.md
    max_turns: 10
`,
			wantErr: "prompt_file not found: prompts/missing.md",
		},
		{
			name:         "invalid gate type",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: webhook
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `type must be "command", "label", or "live"`,
		},
		{
			name:         "command gate missing run",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "run is required for command gate",
		},
		{
			name:         "label gate missing wait_for",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: label
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "wait_for is required for label gate",
		},
		{
			name:         "invalid duration string",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    gate:
      type: command
      run: make test
      retry_delay: not-a-duration
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `invalid retry_delay "not-a-duration"`,
		},
		{
			name:         "max_turns zero",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 0
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "max_turns must be greater than 0",
		},
		{
			name:         "allowed_tools empty string",
			workflowName: "test-workflow",
			yaml: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    allowed_tools: ""
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "allowed_tools must not be empty when specified",
		},
		{
			name:         "name does not match filename",
			workflowName: "fix-bug",
			yaml: `name: wrong-name
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `workflow name "wrong-name" does not match filename "fix-bug.yaml"`,
		},
		{
			name: "valid depends_on",
			yaml: `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
    depends_on:
      - analyze
`,
			prompts: []string{"prompts/analyze.md", "prompts/implement.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if len(s.Phases[1].DependsOn) != 1 || s.Phases[1].DependsOn[0] != "analyze" {
					t.Fatalf("DependsOn = %v, want [analyze]", s.Phases[1].DependsOn)
				}
				if !s.HasDependencies() {
					t.Fatal("HasDependencies() = false, want true")
				}
			},
		},
		{
			name: "depends_on self-reference",
			yaml: `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    depends_on:
      - analyze
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: "depends_on contains self-reference",
		},
		{
			name: "depends_on unknown phase",
			yaml: `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    depends_on:
      - nonexistent
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `depends_on references unknown phase "nonexistent"`,
		},
		{
			name: "depends_on duplicate entry",
			yaml: `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
    depends_on:
      - analyze
      - analyze
`,
			prompts: []string{"prompts/analyze.md", "prompts/implement.md"},
			wantErr: `depends_on contains duplicate entry "analyze"`,
		},
		{
			name: "depends_on cycle",
			yaml: `name: fix-bug
phases:
  - name: alpha
    prompt_file: prompts/alpha.md
    max_turns: 10
    depends_on:
      - beta
  - name: beta
    prompt_file: prompts/beta.md
    max_turns: 10
    depends_on:
      - alpha
`,
			prompts: []string{"prompts/alpha.md", "prompts/beta.md"},
			wantErr: "depends_on creates a cycle",
		},
		{
			name: "parallel phases with shared dependency",
			yaml: `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: implement_a
    prompt_file: prompts/implement_a.md
    max_turns: 20
    depends_on:
      - analyze
  - name: implement_b
    prompt_file: prompts/implement_b.md
    max_turns: 20
    depends_on:
      - analyze
  - name: merge
    prompt_file: prompts/merge.md
    max_turns: 5
    depends_on:
      - implement_a
      - implement_b
`,
			prompts: []string{"prompts/analyze.md", "prompts/implement_a.md", "prompts/implement_b.md", "prompts/merge.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if len(s.Phases) != 4 {
					t.Fatalf("len(Phases) = %d, want 4", len(s.Phases))
				}
				if len(s.Phases[3].DependsOn) != 2 {
					t.Fatalf("merge DependsOn = %v, want [implement_a, implement_b]", s.Phases[3].DependsOn)
				}
			},
		},
		{
			name: "no depends_on means HasDependencies false",
			yaml: `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
`,
			prompts: []string{"prompts/analyze.md", "prompts/implement.md"},
			checkFunc: func(t *testing.T, s *Workflow) {
				t.Helper()
				if s.HasDependencies() {
					t.Fatal("HasDependencies() = true, want false")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)

			for _, p := range tt.prompts {
				createPromptFile(t, dir, p)
			}

			workflowName := tt.workflowName
			if workflowName == "" {
				workflowName = "fix-bug"
			}
			path := writeWorkflowFile(t, dir, workflowName, tt.yaml)
			s, err := Load(path)

			if tt.wantErr != "" {
				requireErrorContains(t, err, tt.wantErr)
				return
			}

			if err != nil {
				t.Fatalf("Load returned unexpected error: %v", err)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, s)
			}
		})
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeWorkflowFile(t, dir, "workflow", "name: [broken\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for malformed yaml")
	}
}

func TestLoadWorkflowWithNoOp(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "fix-bug", `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    noop:
      match: XYLEM_NOOP
`)

	wf, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Phases[0].NoOp == nil {
		t.Fatal("expected noop config to be loaded")
	}
	if wf.Phases[0].NoOp.Match != "XYLEM_NOOP" {
		t.Fatalf("expected noop match XYLEM_NOOP, got %q", wf.Phases[0].NoOp.Match)
	}
}

func TestLoadWorkflowNoOpRequiresMatch(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "fix-bug", `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    noop:
      match: ""
`)

	_, err := Load(path)
	requireErrorContains(t, err, `phase "analyze": noop: match is required`)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name             string
		workflowFileName string // filename stem for the workflow file (used for name validation)
		wf               Workflow
		prompts          []string // prompt files to create relative to cwd
		wantErr          string
		check            func(*testing.T, Workflow)
	}{
		{
			name:             "valid minimal workflow",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:             "invalid workflow class",
			workflowFileName: "test",
			wf: Workflow{
				Name:  "test",
				Class: Class("wildcard"),
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `"class" is invalid: unknown workflow class "wildcard"`,
		},
		{
			name:             "empty name",
			workflowFileName: "test",
			wf: Workflow{
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `"name" is required`,
		},
		{
			name:             "invalid class",
			workflowFileName: "test",
			wf: Workflow{
				Name:  "test",
				Class: "runtime",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `"class" is invalid: unknown workflow class "runtime"`,
		},
		{
			name:             "class is normalized",
			workflowFileName: "test",
			wf: Workflow{
				Name:  "test",
				Class: " delivery ",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			check: func(t *testing.T, s Workflow) {
				t.Helper()
				if s.Class != policy.Delivery {
					t.Fatalf("Class = %q, want %q", s.Class, policy.Delivery)
				}
			},
		},
		{
			name:             "no phases",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
			},
			wantErr: `"phases" is required`,
		},
		{
			name:             "phase with empty name",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "each phase must have a non-empty name",
		},
		{
			name:             "duplicate phase names",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "build", PromptFile: "a.md", MaxTurns: 5},
					{Name: "build", PromptFile: "b.md", MaxTurns: 5},
				},
			},
			prompts: []string{"a.md", "b.md"},
			wantErr: `duplicate phase name "build"`,
		},
		{
			name:             "missing prompt_file value",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "", MaxTurns: 5},
				},
			},
			wantErr: "prompt_file is required",
		},
		{
			name:             "non-existent prompt_file",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "does-not-exist.md", MaxTurns: 5},
				},
			},
			wantErr: "prompt_file not found: does-not-exist.md",
		},
		{
			name:             "max_turns zero",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 0},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "max_turns must be greater than 0",
		},
		{
			name:             "max_turns negative",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: -1},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "max_turns must be greater than 0",
		},
		{
			name:             "invalid gate type",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "webhook"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `type must be "command", "label", or "live"`,
		},
		{
			name:             "command gate missing run",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "command"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "run is required for command gate",
		},
		{
			name:             "label gate missing wait_for",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "label"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "wait_for is required for label gate",
		},
		{
			name:             "invalid retry_delay duration",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "command", Run: "make test", RetryDelay: "bad"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `invalid retry_delay "bad"`,
		},
		{
			name:             "invalid timeout duration",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "label", WaitFor: "approved", Timeout: "forever"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `invalid timeout "forever"`,
		},
		{
			name:             "invalid poll_interval duration",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{Type: "label", WaitFor: "approved", PollInterval: "nope"},
					},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `invalid poll_interval "nope"`,
		},
		{
			name:             "allowed_tools empty string",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5, AllowedTools: strPtr("")},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "allowed_tools must not be empty when specified",
		},
		{
			name:             "allowed_tools nil is valid",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5, AllowedTools: nil},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:             "allowed_tools with value is valid",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5, AllowedTools: strPtr("Bash,Read")},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:             "valid command gate with all fields",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{
							Type:       "command",
							Run:        "go test ./...",
							Retries:    3,
							RetryDelay: "10s",
						},
					},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:             "valid label gate with all fields",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{
						Name: "step1", PromptFile: "prompt.md", MaxTurns: 5,
						Gate: &Gate{
							Type:         "label",
							WaitFor:      "approved",
							Timeout:      "24h",
							PollInterval: "60s",
						},
					},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:             "name does not match filename",
			workflowFileName: "test",
			wf: Workflow{
				Name: "wrong-name",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `workflow name "wrong-name" does not match filename "test.yaml"`,
		},
		{
			name:             "phase name with hyphens is rejected",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "create-issues", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `phase name "create-issues" is invalid; must start with a lowercase letter and contain only lowercase letters, digits, and underscores`,
		},
		{
			name:             "phase name with uppercase is rejected",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "CreateIssues", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `phase name "CreateIssues" is invalid`,
		},
		{
			name:             "phase name with underscores is accepted",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "create_issues", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:             "phase name starting with digit is rejected",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "2step", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `phase name "2step" is invalid`,
		},
		{
			name:             "phase name with digits is accepted",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step2", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:             "command phase valid",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "build", Type: "command", Run: "echo hello"},
				},
			},
		},
		{
			name:             "command phase missing run",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "build", Type: "command"},
				},
			},
			wantErr: "run is required",
		},
		{
			name:             "command phase empty run",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "build", Type: "command", Run: "   "},
				},
			},
			wantErr: "run is required",
		},
		{
			name:             "unknown phase type",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "build", Type: "webhook"},
				},
			},
			wantErr: "type must be",
		},
		{
			name:             "command phase with gate",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "build", Type: "command", Run: "make build", Gate: &Gate{Type: "command", Run: "make test"}},
				},
			},
		},
		{
			name:             "prompt phase default",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5},
				},
			},
			prompts: []string{"prompt.md"},
		},
		{
			name:             "depends_on duplicate entry",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "analyze", PromptFile: "a.md", MaxTurns: 5},
					{Name: "implement", PromptFile: "b.md", MaxTurns: 5, DependsOn: []string{"analyze", "analyze"}},
				},
			},
			prompts: []string{"a.md", "b.md"},
			wantErr: `depends_on contains duplicate entry "analyze"`,
		},
		{
			name:             "depends_on self-reference",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5, DependsOn: []string{"step1"}},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: "depends_on contains self-reference",
		},
		{
			name:             "depends_on unknown phase",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "step1", PromptFile: "prompt.md", MaxTurns: 5, DependsOn: []string{"missing"}},
				},
			},
			prompts: []string{"prompt.md"},
			wantErr: `depends_on references unknown phase "missing"`,
		},
		{
			name:             "valid depends_on",
			workflowFileName: "test",
			wf: Workflow{
				Name: "test",
				Phases: []Phase{
					{Name: "analyze", PromptFile: "a.md", MaxTurns: 5},
					{Name: "implement", PromptFile: "b.md", MaxTurns: 5, DependsOn: []string{"analyze"}},
				},
			},
			prompts: []string{"a.md", "b.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)

			for _, p := range tt.prompts {
				createPromptFile(t, dir, p)
			}

			workflowFileName := tt.workflowFileName
			if workflowFileName == "" {
				workflowFileName = "test"
			}
			workflowFilePath := filepath.Join(dir, workflowFileName+".yaml")

			err := tt.wf.Validate(workflowFilePath)

			if tt.wantErr != "" {
				requireErrorContains(t, err, tt.wantErr)
				return
			}

			if err != nil {
				t.Fatalf("Validate returned unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, tt.wf)
			}
		})
	}
}

func TestLoadWorkflowWithModel(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)

	createPromptFile(t, dir, "prompts/analyze.md")
	createPromptFile(t, dir, "prompts/implement.md")

	path := writeWorkflowFile(t, dir, "fix-bug", `name: fix-bug
description: Fix a bug
model: claude-sonnet-4-20250514
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
    model: claude-opus-4-20250514
  - name: implement
    prompt_file: prompts/implement.md
    max_turns: 20
`)

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if s.Model == nil || *s.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("Workflow.Model = %v, want claude-sonnet-4-20250514", s.Model)
	}
	if s.Phases[0].Model == nil || *s.Phases[0].Model != "claude-opus-4-20250514" {
		t.Fatalf("Phases[0].Model = %v, want claude-opus-4-20250514", s.Phases[0].Model)
	}
	if s.Phases[1].Model != nil {
		t.Fatalf("Phases[1].Model = %v, want nil", s.Phases[1].Model)
	}
}

func TestLoadWorkflowWithoutModel(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)

	createPromptFile(t, dir, "prompts/analyze.md")

	path := writeWorkflowFile(t, dir, "fix-bug", `name: fix-bug
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 10
`)

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if s.Model != nil {
		t.Fatalf("Workflow.Model = %v, want nil", s.Model)
	}
	if s.Phases[0].Model != nil {
		t.Fatalf("Phases[0].Model = %v, want nil", s.Phases[0].Model)
	}
}

func strPtr(s string) *string {
	return &s
}

func TestWorkflowLLMField(t *testing.T) {
	tests := []struct {
		name         string
		workflowYAML string
		prompts      []string
		wantErr      string
		checkFunc    func(t *testing.T, wf *Workflow)
	}{
		{
			name: "workflow llm claude",
			workflowYAML: `name: test-workflow
llm: claude
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.LLM == nil || *wf.LLM != "claude" {
					t.Fatalf("Workflow.LLM = %v, want claude", wf.LLM)
				}
			},
		},
		{
			name: "workflow llm copilot",
			workflowYAML: `name: test-workflow
llm: copilot
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.LLM == nil || *wf.LLM != "copilot" {
					t.Fatalf("Workflow.LLM = %v, want copilot", wf.LLM)
				}
			},
		},
		{
			name: "workflow llm absent means nil",
			workflowYAML: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.LLM != nil {
					t.Fatalf("Workflow.LLM = %v, want nil", wf.LLM)
				}
			},
		},
		{
			name: "workflow llm invalid",
			workflowYAML: `name: test-workflow
llm: gpt4
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `workflow: llm must be "claude" or "copilot"`,
		},
		{
			name: "phase llm copilot",
			workflowYAML: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
    llm: copilot
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.Phases[0].LLM == nil || *wf.Phases[0].LLM != "copilot" {
					t.Fatalf("Phase.LLM = %v, want copilot", wf.Phases[0].LLM)
				}
			},
		},
		{
			name: "phase llm invalid",
			workflowYAML: `name: test-workflow
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
    llm: openai
`,
			prompts: []string{"prompts/analyze.md"},
			wantErr: `phase "analyze": llm must be "claude" or "copilot"`,
		},
		{
			name: "workflow and phase llm with model",
			workflowYAML: `name: test-workflow
llm: copilot
model: gpt-4o
phases:
  - name: analyze
    prompt_file: prompts/analyze.md
    max_turns: 5
    llm: claude
    model: claude-sonnet-4-5
`,
			prompts: []string{"prompts/analyze.md"},
			checkFunc: func(t *testing.T, wf *Workflow) {
				t.Helper()
				if wf.LLM == nil || *wf.LLM != "copilot" {
					t.Fatalf("Workflow.LLM = %v, want copilot", wf.LLM)
				}
				if wf.Model == nil || *wf.Model != "gpt-4o" {
					t.Fatalf("Workflow.Model = %v, want gpt-4o", wf.Model)
				}
				if wf.Phases[0].LLM == nil || *wf.Phases[0].LLM != "claude" {
					t.Fatalf("Phase.LLM = %v, want claude", wf.Phases[0].LLM)
				}
				if wf.Phases[0].Model == nil || *wf.Phases[0].Model != "claude-sonnet-4-5" {
					t.Fatalf("Phase.Model = %v, want claude-sonnet-4-5", wf.Phases[0].Model)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdirTemp(t, dir)

			for _, p := range tt.prompts {
				createPromptFile(t, dir, p)
			}

			path := writeWorkflowFile(t, dir, "test-workflow", tt.workflowYAML)
			wf, err := Load(path)

			if tt.wantErr != "" {
				requireErrorContains(t, err, tt.wantErr)
				return
			}

			if err != nil {
				t.Fatalf("Load returned unexpected error: %v", err)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, wf)
			}
		})
	}
}
