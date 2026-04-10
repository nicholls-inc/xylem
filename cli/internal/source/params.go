package source

import (
	"encoding/json"
	"fmt"
	"strings"
)

const TaskParamsMetaKey = "task_params_json"

func EncodeTaskParams(params map[string]any) (string, error) {
	if len(params) == 0 {
		return "", nil
	}

	data, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal task params: %w", err)
	}
	return string(data), nil
}

func DecodeTaskParams(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("unmarshal task params: %w", err)
	}
	if len(params) == 0 {
		return nil, nil
	}
	return params, nil
}
