package workflow

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	implementHarnessFetchMain  = "git fetch origin main"
	implementHarnessFetchHead  = `git fetch origin main "$current_branch"`
	implementHarnessMergeBase  = "git merge-base --is-ancestor origin/main HEAD"
	implementHarnessRemoteRef  = `git rev-parse "origin/$current_branch"`
	implementHarnessGoVet      = "go vet ./..."
	implementHarnessGoBuild    = "go build ./cmd/xylem"
	implementHarnessGoTest     = "go test ./..."
	implementHarnessPRCreate   = "gh pr create"
	implementHarnessRepoFlag   = "--repo nicholls-inc/xylem"
	implementHarnessPRLabel    = `--label "harness-impl"`
	implementHarnessMergeLabel = `--label "ready-to-merge"`
)

func checkedInWorkflowPath(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller() failed")

	return filepath.Join(filepath.Dir(file), "..", "..", "..", ".xylem", "workflows", name)
}

func checkedInPromptPath(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller() failed")

	return filepath.Join(filepath.Dir(file), "..", "..", "..", ".xylem", "prompts", "implement-harness", name)
}

func findPhaseByName(t *testing.T, phases []Phase, name string) *Phase {
	t.Helper()

	for i := range phases {
		if phases[i].Name == name {
			return &phases[i]
		}
	}

	t.Fatalf("phase %q not found", name)
	return nil
}

func commandContainsInOrder(command string, parts ...string) bool {
	searchStart := 0
	for _, part := range parts {
		idx := strings.Index(command[searchStart:], part)
		if idx == -1 {
			return false
		}
		searchStart += idx + len(part)
	}

	return true
}

func TestSmoke_S2_ImplementHarnessWorkflowChecksRebasedBranchBeforePRDraftCompletes(t *testing.T) {
	t.Parallel()

	workflowPath := checkedInWorkflowPath(t, "implement-harness.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	prDraft := findPhaseByName(t, wf.Phases, "pr_draft")
	require.NotNil(t, prDraft.Gate, "workflow %q missing pr_draft gate", wf.Name)
	assert.Equal(t, "command", prDraft.Gate.Type)
	assert.Contains(t, prDraft.Gate.Run, implementHarnessFetchHead)
	assert.Contains(t, prDraft.Gate.Run, implementHarnessMergeBase)
	assert.Contains(t, prDraft.Gate.Run, implementHarnessRemoteRef)
	assert.Contains(t, prDraft.Gate.Run, `ERROR: branch is behind origin/main; rebase before pushing`)
	assert.Contains(t, prDraft.Gate.Run, `ERROR: remote branch does not match HEAD; push the rebased branch before continuing`)
	assert.True(t, commandContainsInOrder(
		prDraft.Gate.Run,
		implementHarnessFetchHead,
		implementHarnessMergeBase,
		implementHarnessRemoteRef,
		implementHarnessGoVet,
		implementHarnessGoBuild,
		implementHarnessGoTest,
	))
}

func TestSmoke_S3_ImplementHarnessWorkflowChecksMainFreshnessBeforePRCreate(t *testing.T) {
	t.Parallel()

	workflowPath := checkedInWorkflowPath(t, "implement-harness.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	prCreate := findPhaseByName(t, wf.Phases, "pr_create")
	assert.Equal(t, "command", prCreate.Type)
	assert.Contains(t, prCreate.Run, implementHarnessFetchMain)
	assert.Contains(t, prCreate.Run, implementHarnessMergeBase)
	assert.Contains(t, prCreate.Run, implementHarnessRepoFlag)
	assert.Contains(t, prCreate.Run, implementHarnessPRLabel)
	assert.Contains(t, prCreate.Run, implementHarnessMergeLabel)
	assert.Contains(t, prCreate.Run, `ERROR: branch is behind origin/main; rebase before creating the PR`)
	assert.True(t, commandContainsInOrder(
		prCreate.Run,
		implementHarnessFetchMain,
		implementHarnessMergeBase,
		implementHarnessPRCreate,
		implementHarnessPRLabel,
		implementHarnessMergeLabel,
	))
}

func TestSmoke_S4_ImplementHarnessPRDraftPromptRequiresRebaseRetestAndForcePush(t *testing.T) {
	t.Parallel()

	promptPath := checkedInPromptPath(t, "pr_draft.md")
	data, err := os.ReadFile(promptPath)
	require.NoError(t, err, "ReadFile(%q)", promptPath)

	prompt := string(data)
	assert.Contains(t, prompt, "`git fetch origin main && git rebase origin/main`")
	assert.Contains(t, prompt, "`go vet ./... && go build ./cmd/xylem && go test ./...`")
	assert.Contains(t, prompt, "`git push --force-with-lease`")
	assert.True(t, commandContainsInOrder(
		prompt,
		"git fetch origin main",
		"git rebase origin/main",
		"go vet ./...",
		"go build ./cmd/xylem",
		"go test ./...",
		"git push --force-with-lease",
	))
}

func TestSmoke_S5_AnalyzePhaseMaxTurnsIs50(t *testing.T) {
	t.Parallel()

	workflowPath := checkedInWorkflowPath(t, "implement-harness.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	analyze := findPhaseByName(t, wf.Phases, "analyze")
	assert.Equal(t, 50, analyze.MaxTurns, "analyze phase max_turns should be 50")
}

func TestSmoke_S6_PlanPhaseMaxTurnsIs50(t *testing.T) {
	t.Parallel()

	workflowPath := checkedInWorkflowPath(t, "implement-harness.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	plan := findPhaseByName(t, wf.Phases, "plan")
	assert.Equal(t, 50, plan.MaxTurns, "plan phase max_turns should be 50")
}

func TestSmoke_S7_OtherPhaseMaxTurnsAreUnchanged(t *testing.T) {
	t.Parallel()

	workflowPath := checkedInWorkflowPath(t, "implement-harness.yaml")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "ReadFile(%q)", workflowPath)

	var wf Workflow
	require.NoError(t, yaml.Unmarshal(data, &wf), "yaml.Unmarshal(%q)", workflowPath)

	cases := []struct {
		name      string
		wantTurns int
	}{
		{"implement", 80},
		{"verify", 80},
		{"test_critic", 50},
		{"smoke", 60},
		{"pr_draft", 50},
	}
	for _, tc := range cases {
		phase := findPhaseByName(t, wf.Phases, tc.name)
		assert.Equal(t, tc.wantTurns, phase.MaxTurns, "phase %q max_turns", tc.name)
	}
}
