package main

import (
	"context"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/releasecadence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type releaseCadenceRunnerStub struct{}

func (releaseCadenceRunnerStub) RunOutput(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func TestCmdReleaseCadenceLabelReadyUsesConfigRepo(t *testing.T) {
	setupTestDeps(t)
	original := applyReleaseCadenceReadyLabel
	t.Cleanup(func() {
		applyReleaseCadenceReadyLabel = original
	})

	var got releasecadence.Options
	applyReleaseCadenceReadyLabel = func(_ context.Context, _ releasecadence.CommandRunner, opts releasecadence.Options) (*releasecadence.Result, error) {
		got = opts
		return &releasecadence.Result{Action: releasecadence.ActionNoop, Reason: "already satisfied"}, nil
	}

	now := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)
	result, err := cmdReleaseCadenceLabelReady(context.Background(), "", releasecadence.DefaultReadyLabel, releasecadence.DefaultOptOutLabel, releasecadence.DefaultMinAge, releasecadence.DefaultMinCommits, now, releaseCadenceRunnerStub{})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, releasecadence.ActionNoop, result.Action)
	assert.Equal(t, "owner/repo", got.Repo)
	assert.Equal(t, now, got.Now)
}
