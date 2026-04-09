package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func adaptRepoSearchCall(repo string) string {
	return "gh search issues --repo " + repo + " --state all --json number,title,url --limit 100 --search " + adaptRepoIssueTitle
}

func adaptRepoCreateCall(repo string) string {
	return "gh issue create --repo " + repo + " --title " + adaptRepoIssueTitle + " --body " + adaptRepoIssueBody + " --label " + adaptRepoSeedLabel + " --label " + adaptRepoReadyLabel
}

type seedRunnerStub struct {
	calls   [][]string
	outputs map[string][]byte
}

func (s *seedRunnerStub) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	s.calls = append(s.calls, call)
	if s.outputs == nil {
		return nil, nil
	}
	return s.outputs[strings.Join(call, " ")], nil
}

func TestSmoke_S3_DaemonSeedingCreatesIssueAndMarkerOnFreshRepo(t *testing.T) {
	cfg := &config.Config{
		StateDir: t.TempDir(),
		Sources: map[string]config.SourceConfig{
			"bugs": {
				Type: "github",
				Repo: "owner/repo",
			},
		},
	}
	runner := &seedRunnerStub{
		outputs: map[string][]byte{
			adaptRepoSearchCall("owner/repo"): []byte("[]"),
			adaptRepoCreateCall("owner/repo"): []byte("https://github.com/owner/repo/issues/34\n"),
		},
	}

	marker, err := ensureAdaptRepoSeeded(context.Background(), cfg, runner, adaptRepoSeededByDaemon)
	require.NoError(t, err)
	require.NotNil(t, marker)
	assert.Equal(t, 34, marker.IssueNumber)
	assert.Equal(t, "https://github.com/owner/repo/issues/34", marker.IssueURL)
	assert.Equal(t, adaptRepoSeededByDaemon, marker.SeededBy)
	assert.Equal(t, 1, marker.ProfileVersion)

	written, err := readAdaptRepoSeedMarker(filepath.Join(cfg.StateDir, "state", "bootstrap", "adapt-repo-seeded.json"))
	require.NoError(t, err)
	assert.Equal(t, marker, written)
	assert.Len(t, runner.calls, 2)
}

func TestSmoke_S4_DaemonSeedingDedupesMatchingClosedIssueByTitle(t *testing.T) {
	cfg := &config.Config{
		StateDir: t.TempDir(),
		Sources: map[string]config.SourceConfig{
			"adapt-repo": {
				Type: "github",
				Repo: "owner/repo",
			},
		},
	}
	runner := &seedRunnerStub{
		outputs: map[string][]byte{
			adaptRepoSearchCall("owner/repo"): []byte(`[{"number":21,"title":"[xylem] adapt harness to this repository","url":"https://github.com/owner/repo/issues/21"}]`),
		},
	}

	marker, err := ensureAdaptRepoSeeded(context.Background(), cfg, runner, adaptRepoSeededByDaemon)
	require.NoError(t, err)
	require.NotNil(t, marker)
	assert.Equal(t, 21, marker.IssueNumber)
	assert.Equal(t, "https://github.com/owner/repo/issues/21", marker.IssueURL)
	assert.Equal(t, adaptRepoSeededByDaemon, marker.SeededBy)

	written, err := readAdaptRepoSeedMarker(filepath.Join(cfg.StateDir, "state", "bootstrap", "adapt-repo-seeded.json"))
	require.NoError(t, err)
	assert.Equal(t, marker, written)
	assert.Len(t, runner.calls, 1)
}

func TestSmoke_S5_AdaptRepoSeedMarkerPreventsReseedingOnSubsequentBoots(t *testing.T) {
	cfg := &config.Config{
		StateDir: t.TempDir(),
		Sources: map[string]config.SourceConfig{
			"adapt-repo": {
				Type: "github",
				Repo: "owner/repo",
			},
		},
	}
	runner := &seedRunnerStub{
		outputs: map[string][]byte{
			adaptRepoSearchCall("owner/repo"): []byte("[]"),
			adaptRepoCreateCall("owner/repo"): []byte("https://github.com/owner/repo/issues/34\n"),
		},
	}

	marker, err := ensureAdaptRepoSeeded(context.Background(), cfg, runner, adaptRepoSeededByDaemon)
	require.NoError(t, err)
	require.NotNil(t, marker)
	assert.Len(t, runner.calls, 2)

	markerAgain, err := ensureAdaptRepoSeeded(context.Background(), cfg, runner, adaptRepoSeededByDaemon)
	require.NoError(t, err)
	require.NotNil(t, markerAgain)
	assert.Equal(t, marker, markerAgain)
	assert.Len(t, runner.calls, 2)
}

func TestEnsureAdaptRepoSeededSkipsConfigsWithoutGitHubRepo(t *testing.T) {
	cfg := &config.Config{
		StateDir: t.TempDir(),
		Sources: map[string]config.SourceConfig{
			"doctor": {
				Type:     "schedule",
				Cadence:  "1h",
				Workflow: "doctor",
			},
		},
	}
	runner := &seedRunnerStub{}

	marker, err := ensureAdaptRepoSeeded(context.Background(), cfg, runner, adaptRepoSeededByDaemon)
	require.NoError(t, err)
	assert.Nil(t, marker)
	assert.Empty(t, runner.calls)
}
