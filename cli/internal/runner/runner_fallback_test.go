package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fallbackPhaseCall struct {
	Command string
	Args    []string
	Env     []string
	Prompt  string
}

type fallbackCmdRunner struct {
	calls []fallbackPhaseCall
}

func (f *fallbackCmdRunner) RunOutput(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return []byte{}, nil
}

func (f *fallbackCmdRunner) RunProcess(_ context.Context, _ string, _ string, _ ...string) error {
	return nil
}

func (f *fallbackCmdRunner) RunPhase(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return f.RunPhaseWithEnv(ctx, dir, nil, stdin, name, args...)
}

func (f *fallbackCmdRunner) RunPhaseWithEnv(_ context.Context, _ string, extraEnv []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	prompt := ""
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		prompt = string(data)
	}
	f.calls = append(f.calls, fallbackPhaseCall{
		Command: name,
		Args:    append([]string(nil), args...),
		Env:     append([]string(nil), extraEnv...),
		Prompt:  prompt,
	})
	switch name {
	case "claude-fallback":
		return nil, errors.New("Credit balance is too low")
	case "claude-hard-fail":
		return nil, errors.New("exit status 1")
	default:
		return []byte("implemented"), nil
	}
}

func TestRunPhaseWithProviderFallbackFallsBackAfterRateLimit(t *testing.T) {
	t.Setenv("XYLEM_DTU_STATE_PATH", setupDTUClock(t))
	tracer, rec := newTestTracer(t)

	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 1,
		MaxTurns:    50,
		Timeout:     "30s",
		StateDir:    filepath.Join(dir, ".xylem"),
		Providers: map[string]config.ProviderConfig{
			"primary": {
				Kind:    "claude",
				Command: "claude-fallback",
				Tiers:   map[string]string{"med": "claude-med"},
				Env: map[string]string{
					"ANTHROPIC_API_KEY": "anthropic-secret",
				},
			},
			"secondary": {
				Kind:    "copilot",
				Command: "copilot-success",
				Tiers:   map[string]string{"med": "gpt-med"},
				Env: map[string]string{
					"GITHUB_TOKEN": "copilot-secret",
				},
			},
		},
		LLMRouting: config.LLMRoutingConfig{
			DefaultTier: "med",
			Tiers: map[string]config.TierRouting{
				"med": {Providers: []string{"primary", "secondary"}},
			},
		},
		Harness: config.HarnessConfig{
			ProtectedSurfaces: config.ProtectedSurfacesConfig{
				Paths: []string{
					".xylem/HARNESS.md",
					".xylem.yml",
					".xylem/workflows/*.yaml",
					".xylem/prompts/*/*.md",
				},
			},
		},
		Sources: map[string]config.SourceConfig{
			"github": {
				Type:    "github",
				Repo:    "owner/repo",
				Exclude: []string{"wontfix"},
				Tasks:   map[string]config.Task{"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(1, "fix-bug")
	vessel.Tier = "med"
	_, _ = q.Enqueue(vessel)

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "implement", promptContent: "Fix the bug", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(oldWd)

	cmdRunner := &fallbackCmdRunner{}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}
	r.Tracer = tracer

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Completed)
	require.Len(t, cmdRunner.calls, 5)

	last := cmdRunner.calls[len(cmdRunner.calls)-1]
	assert.Equal(t, "copilot-success", last.Command)
	assert.Contains(t, last.Args, "--model")
	assert.True(t, containsArgSequence(last.Args, "--model", "gpt-med"))
	assert.Contains(t, last.Env, "GITHUB_TOKEN=copilot-secret")
	assert.NotContains(t, last.Env, "ANTHROPIC_API_KEY=anthropic-secret")
	assert.True(t, containsArgSequence(last.Args, "-p", "Fix the bug"))

	first := cmdRunner.calls[0]
	assert.Equal(t, "claude-fallback", first.Command)
	assert.True(t, containsArgSequence(first.Args, "--model", "claude-med"))
	assert.Contains(t, first.Env, "ANTHROPIC_API_KEY=anthropic-secret")
	assert.NotContains(t, first.Env, "GITHUB_TOKEN=copilot-secret")
	assert.Equal(t, "Fix the bug", first.Prompt)

	attrs := spanAttrMap(endedSpanByName(t, rec, "phase:implement"))
	assert.Equal(t, "secondary", attrs["xylem.phase.provider"])
	assert.Equal(t, "gpt-med", attrs["xylem.phase.model"])
	assert.Equal(t, "secondary", attrs["llm.provider"])
	assert.Equal(t, "med", attrs["llm.tier"])
}

func TestRunPhaseWithProviderFallbackDoesNotMaskRealFailures(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Concurrency: 1,
		MaxTurns:    50,
		Timeout:     "30s",
		StateDir:    filepath.Join(dir, ".xylem"),
		Providers: map[string]config.ProviderConfig{
			"primary": {
				Kind:    "claude",
				Command: "claude-hard-fail",
				Tiers:   map[string]string{"med": "claude-med"},
			},
			"secondary": {
				Kind:    "copilot",
				Command: "copilot-success",
				Tiers:   map[string]string{"med": "gpt-med"},
			},
		},
		LLMRouting: config.LLMRoutingConfig{
			DefaultTier: "med",
			Tiers: map[string]config.TierRouting{
				"med": {Providers: []string{"primary", "secondary"}},
			},
		},
		Harness: config.HarnessConfig{
			ProtectedSurfaces: config.ProtectedSurfacesConfig{
				Paths: []string{
					".xylem/HARNESS.md",
					".xylem.yml",
					".xylem/workflows/*.yaml",
					".xylem/prompts/*/*.md",
				},
			},
		},
		Sources: map[string]config.SourceConfig{
			"github": {
				Type:    "github",
				Repo:    "owner/repo",
				Exclude: []string{"wontfix"},
				Tasks:   map[string]config.Task{"fix-bugs": {Labels: []string{"bug"}, Workflow: "fix-bug"}},
			},
		},
	}
	q := queue.New(filepath.Join(dir, "queue.jsonl"))
	vessel := makeVessel(1, "fix-bug")
	vessel.Tier = "med"
	_, _ = q.Enqueue(vessel)

	writeWorkflowFile(t, dir, "fix-bug", []testPhase{
		{name: "implement", promptContent: "Fix the bug", maxTurns: 10},
	})

	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(oldWd)

	cmdRunner := &fallbackCmdRunner{}
	r := New(cfg, q, &mockWorktree{}, cmdRunner)
	r.Sources = map[string]source.Source{"github-issue": makeGitHubSource()}

	result, err := r.DrainAndWait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Len(t, cmdRunner.calls, 1)
	assert.Equal(t, "claude-hard-fail", cmdRunner.calls[0].Command)

	vessels, listErr := q.List()
	require.NoError(t, listErr)
	require.Len(t, vessels, 1)
	assert.Equal(t, queue.StateFailed, vessels[0].State)
	assert.True(t, strings.Contains(vessels[0].Error, "exit status 1"))
}
