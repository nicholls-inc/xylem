package releasecadence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runnerStub struct {
	calls     [][]string
	responses map[string][]byte
}

func (r *runnerStub) RunOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if out, ok := r.responses[strings.Join(call, "\x00")]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unexpected call: %v", call)
}

func (r *runnerStub) hasCallPrefix(want ...string) bool {
	for _, call := range r.calls {
		if len(call) < len(want) {
			continue
		}
		match := true
		for i := range want {
			if call[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func TestMatchesReleasePR(t *testing.T) {
	t.Parallel()

	assert.True(t, MatchesReleasePR("release-please--branches--main", "Release Please"))
	assert.True(t, MatchesReleasePR("some-branch", "release please: bump version"))
	assert.False(t, MatchesReleasePR("feat/issue-42-42", "Regular change"))
}

func TestApplyReadyLabelNoReleasePR(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)
	runner := &runnerStub{
		responses: map[string][]byte{
			commandKey("gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt,labels"): []byte("[]"),
		},
	}

	result, err := ApplyReadyLabel(context.Background(), runner, Options{Repo: "owner/repo", Now: now})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ActionNoop, result.Action)
	assert.Contains(t, result.Reason, "no open release-please PR")
	assert.Len(t, runner.calls, 1)
}

func TestApplyReadyLabelSkipsYoungPR(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)
	runner := &runnerStub{
		responses: map[string][]byte{
			commandKey("gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt,labels"): mustJSON(t, []map[string]any{{
				"number":      21,
				"title":       "Release Please",
				"url":         "https://example/pr/21",
				"headRefName": "release-please--branches--main",
				"createdAt":   now.Add(-2 * 24 * time.Hour),
				"labels":      []map[string]any{},
			}}),
		},
	}

	result, err := ApplyReadyLabel(context.Background(), runner, Options{Repo: "owner/repo", Now: now})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ActionNoop, result.Action)
	assert.Equal(t, 21, result.PRNumber)
	assert.Contains(t, result.Reason, "only")
	assert.False(t, runner.hasCallPrefix("gh", "pr", "view", "21", "--repo", "owner/repo"))
}

func TestApplyReadyLabelSkipsAlreadyReadyPR(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)
	runner := &runnerStub{
		responses: map[string][]byte{
			commandKey("gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt,labels"): mustJSON(t, []map[string]any{{
				"number":      21,
				"title":       "Release Please",
				"url":         "https://example/pr/21",
				"headRefName": "release-please--branches--main",
				"createdAt":   now.Add(-10 * 24 * time.Hour),
				"labels":      []map[string]any{{"name": DefaultReadyLabel}},
			}}),
		},
	}

	result, err := ApplyReadyLabel(context.Background(), runner, Options{Repo: "owner/repo", Now: now})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ActionNoop, result.Action)
	assert.Contains(t, result.Reason, DefaultReadyLabel)
	assert.False(t, runner.hasCallPrefix("gh", "api", "--method", "POST"))
}

func TestApplyReadyLabelAddsReadyLabelWhenMature(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)
	runner := &runnerStub{
		responses: map[string][]byte{
			commandKey("gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--limit", "100", "--json", "number,title,url,headRefName,createdAt,labels"): mustJSON(t, []map[string]any{{
				"number":      21,
				"title":       "Release Please",
				"url":         "https://example/pr/21",
				"headRefName": "release-please--branches--main",
				"createdAt":   now.Add(-10 * 24 * time.Hour),
				"labels":      []map[string]any{},
			}}),
			commandKey("gh", "pr", "view", "21", "--repo", "owner/repo", "--json", "commits"): mustJSON(t, map[string]any{
				"commits": []map[string]any{{"oid": "a"}, {"oid": "b"}, {"oid": "c"}, {"oid": "d"}, {"oid": "e"}},
			}),
			commandKey("gh", "pr", "edit", "21", "--repo", "owner/repo", "--add-label", DefaultReadyLabel): []byte(""),
		},
	}

	result, err := ApplyReadyLabel(context.Background(), runner, Options{Repo: "owner/repo", Now: now})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ActionLabeled, result.Action)
	assert.Equal(t, 21, result.PRNumber)
	assert.Equal(t, 5, result.CommitCount)
	assert.Equal(t, DefaultReadyLabel, result.ReadyLabel)
	assert.True(t, runner.hasCallPrefix("gh", "pr", "edit", "21", "--repo", "owner/repo", "--add-label", DefaultReadyLabel))
}
