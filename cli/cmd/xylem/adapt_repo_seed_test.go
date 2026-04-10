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

func adaptRepoSearchCallForState(repo, state string) string {
	return "gh search issues --repo " + repo + " --state " + state + " --json number,title,url --limit 100 --search " + adaptRepoIssueTitle
}

func adaptRepoCreateCall(repo string) string {
	return "gh issue create --repo " + repo + " --title " + adaptRepoIssueTitle + " --body " + adaptRepoIssueBody + " --label " + adaptRepoSeedLabel + " --label " + adaptRepoReadyLabel
}

type seedRunnerStub struct {
	calls   [][]string
	outputs map[string][]byte
	errors  map[string]error
}

func (s *seedRunnerStub) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	s.calls = append(s.calls, call)
	key := strings.Join(call, " ")
	if s.errors != nil {
		if err := s.errors[key]; err != nil {
			return nil, err
		}
	}
	if s.outputs == nil {
		return nil, nil
	}
	return s.outputs[key], nil
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
			adaptRepoSearchCallForState("owner/repo", "open"):   []byte("[]"),
			adaptRepoSearchCallForState("owner/repo", "closed"): []byte("[]"),
			adaptRepoCreateCall("owner/repo"):                   []byte("https://github.com/owner/repo/issues/34\n"),
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
	assert.Len(t, runner.calls, 3)
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
			adaptRepoSearchCallForState("owner/repo", "open"):   []byte("[]"),
			adaptRepoSearchCallForState("owner/repo", "closed"): []byte(`[{"number":21,"title":"[xylem] adapt harness to this repository","url":"https://github.com/owner/repo/issues/21"}]`),
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
	assert.Len(t, runner.calls, 2)
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
			adaptRepoSearchCallForState("owner/repo", "open"):   []byte("[]"),
			adaptRepoSearchCallForState("owner/repo", "closed"): []byte("[]"),
			adaptRepoCreateCall("owner/repo"):                   []byte("https://github.com/owner/repo/issues/34\n"),
		},
	}

	marker, err := ensureAdaptRepoSeeded(context.Background(), cfg, runner, adaptRepoSeededByDaemon)
	require.NoError(t, err)
	require.NotNil(t, marker)
	assert.Len(t, runner.calls, 3)

	markerAgain, err := ensureAdaptRepoSeeded(context.Background(), cfg, runner, adaptRepoSeededByDaemon)
	require.NoError(t, err)
	require.NotNil(t, markerAgain)
	assert.Equal(t, marker, markerAgain)
	assert.Len(t, runner.calls, 3)
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

func TestFindExistingAdaptRepoIssueStopsAfterOpenMatch(t *testing.T) {
	runner := &seedRunnerStub{
		outputs: map[string][]byte{
			adaptRepoSearchCallForState("owner/repo", "open"): []byte(`[{"number":13,"title":"[xylem] adapt harness to this repository","url":"https://github.com/owner/repo/issues/13"}]`),
		},
	}

	issue, err := findExistingAdaptRepoIssue(context.Background(), runner, "owner/repo")
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, 13, issue.Number)
	assert.Len(t, runner.calls, 1)
}
