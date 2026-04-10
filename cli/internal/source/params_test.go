package source

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeTaskParamsProducesJSONObject(t *testing.T) {
	t.Parallel()

	params := map[string]any{
		"mode":               "file_diet",
		"loc_threshold":      500,
		"source_dirs":        []any{"cli", "internal"},
		"max_issues_per_run": 1,
	}

	encoded, err := EncodeTaskParams(params)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &decoded))
	assert.Equal(t, "file_diet", decoded["mode"])
	assert.Equal(t, float64(500), decoded["loc_threshold"])
	assert.Equal(t, float64(1), decoded["max_issues_per_run"])
	assert.Equal(t, []any{"cli", "internal"}, decoded["source_dirs"])
}

func TestDecodeTaskParamsParsesJSONObject(t *testing.T) {
	t.Parallel()

	decoded, err := DecodeTaskParams(`{"mode":"semantic_refactor","max_issues_per_run":3,"source_dirs":["cli"]}`)
	require.NoError(t, err)
	assert.Equal(t, "semantic_refactor", decoded["mode"])
	assert.Equal(t, float64(3), decoded["max_issues_per_run"])
	assert.Equal(t, []any{"cli"}, decoded["source_dirs"])
}

func TestDecodeTaskParamsBlankReturnsNil(t *testing.T) {
	t.Parallel()

	decoded, err := DecodeTaskParams("   ")
	require.NoError(t, err)
	assert.Nil(t, decoded)
}

func TestDecodeTaskParamsRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	decoded, err := DecodeTaskParams("{not-json")
	require.Error(t, err)
	assert.Nil(t, decoded)
	assert.Contains(t, err.Error(), "unmarshal task params")
}
