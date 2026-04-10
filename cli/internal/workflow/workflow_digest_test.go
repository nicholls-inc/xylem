package workflow

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadWithDigestReturnsLoadedWorkflowDigest(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompts", "fix-bug", "analyze.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(promptPath), 0o755))
	require.NoError(t, os.WriteFile(promptPath, []byte("analyze"), 0o644))

	workflowPath := filepath.Join(dir, "fix-bug.yaml")
	workflowYAML := "name: fix-bug\nphases:\n  - name: analyze\n    prompt_file: " + promptPath + "\n    max_turns: 1\n"
	require.NoError(t, os.WriteFile(workflowPath, []byte(workflowYAML), 0o644))
	wantDigest := fmt.Sprintf("wf-%x", sha256.Sum256([]byte(workflowYAML)))

	wf, digest, err := LoadWithDigest(workflowPath)
	require.NoError(t, err)
	require.NotNil(t, wf)
	assert.Equal(t, "fix-bug", wf.Name)
	assert.Equal(t, wantDigest, digest)
}

func TestLoadWithDigestReturnsReadError(t *testing.T) {
	_, _, err := LoadWithDigest(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read workflow file")
}
