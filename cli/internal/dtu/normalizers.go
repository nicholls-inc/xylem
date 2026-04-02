package dtu

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// VerificationCommandResult captures stdout/stderr/exit code from one
// verification command invocation.
type VerificationCommandResult struct {
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// VerificationNormalizer converts raw command output into a stable contract
// shape suitable for differential comparisons.
type VerificationNormalizer func(VerificationCommandResult) (any, error)

type normalizedIssueListItem struct {
	Number int      `json:"number"`
	Title  string   `json:"title,omitempty"`
	Body   string   `json:"body,omitempty"`
	URL    string   `json:"url,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type normalizedPRListItem struct {
	Number      int      `json:"number"`
	Title       string   `json:"title,omitempty"`
	Body        string   `json:"body,omitempty"`
	URL         string   `json:"url,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	HeadRefName string   `json:"head_ref_name,omitempty"`
}

type normalizedProviderProcess struct {
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code"`
}

type normalizedProviderProcessShape struct {
	HasStdout bool `json:"has_stdout"`
	HasStderr bool `json:"has_stderr"`
	ExitCode  int  `json:"exit_code"`
}

var verificationNormalizers = map[string]VerificationNormalizer{
	"git_ls_remote_heads":         normalizeGitLSRemoteHeads,
	"git_symbolic_ref":            normalizeGitSymbolicRef,
	"issue_list":                  normalizeIssueList,
	"labels_only":                 normalizeLabelsOnly,
	"pr_list":                     normalizePRList,
	"provider_process_shape":      normalizeProviderProcessShape,
	"provider_stdout_stderr_exit": normalizeProviderStdoutStderrExit,
}

// LookupVerificationNormalizer returns the named normalizer, if available.
func LookupVerificationNormalizer(name string) (VerificationNormalizer, bool) {
	normalizer, ok := verificationNormalizers[strings.TrimSpace(name)]
	return normalizer, ok
}

// NormalizeVerificationResult applies a declared normalizer and returns both
// the structured value and its canonical JSON form.
func NormalizeVerificationResult(normalizerName string, result VerificationCommandResult) (any, string, error) {
	normalizer, ok := LookupVerificationNormalizer(normalizerName)
	if !ok {
		return nil, "", fmt.Errorf("unknown verification normalizer %q", normalizerName)
	}
	normalized, err := normalizer(result)
	if err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", fmt.Errorf("marshal normalized verification result: %w", err)
	}
	return normalized, string(data), nil
}

func normalizeIssueList(result VerificationCommandResult) (any, error) {
	raw, err := decodeJSONArray(result.Stdout, "issue_list")
	if err != nil {
		return nil, err
	}

	items := make([]normalizedIssueListItem, 0, len(raw))
	for i, entry := range raw {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("issue_list item %d must be an object", i)
		}
		items = append(items, normalizedIssueListItem{
			Number: intFromJSON(object["number"]),
			Title:  stringFromJSON(object["title"]),
			Body:   stringFromJSON(object["body"]),
			URL:    stringFromJSON(object["url"]),
			Labels: labelNamesFromJSON(object["labels"]),
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Number != items[j].Number {
			return items[i].Number < items[j].Number
		}
		return items[i].Title < items[j].Title
	})
	return items, nil
}

func normalizePRList(result VerificationCommandResult) (any, error) {
	raw, err := decodeJSONArray(result.Stdout, "pr_list")
	if err != nil {
		return nil, err
	}

	items := make([]normalizedPRListItem, 0, len(raw))
	for i, entry := range raw {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("pr_list item %d must be an object", i)
		}
		items = append(items, normalizedPRListItem{
			Number:      intFromJSON(object["number"]),
			Title:       stringFromJSON(object["title"]),
			Body:        stringFromJSON(object["body"]),
			URL:         stringFromJSON(object["url"]),
			Labels:      labelNamesFromJSON(object["labels"]),
			HeadRefName: stringFromJSON(object["headRefName"]),
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Number != items[j].Number {
			return items[i].Number < items[j].Number
		}
		return items[i].Title < items[j].Title
	})
	return items, nil
}

func normalizeLabelsOnly(result VerificationCommandResult) (any, error) {
	value, err := decodeJSONValue(result.Stdout, "labels_only")
	if err != nil {
		return nil, err
	}

	switch typed := value.(type) {
	case map[string]any:
		if labels, ok := typed["labels"]; ok {
			return labelNamesFromJSON(labels), nil
		}
		return nil, fmt.Errorf("labels_only payload must contain a labels field")
	case []any:
		return labelNamesFromJSON(typed), nil
	default:
		return nil, fmt.Errorf("labels_only payload must be an object or array")
	}
}

func normalizeProviderStdoutStderrExit(result VerificationCommandResult) (any, error) {
	return normalizedProviderProcess{
		Stdout:   strings.TrimSpace(result.Stdout),
		Stderr:   strings.TrimSpace(result.Stderr),
		ExitCode: result.ExitCode,
	}, nil
}

func normalizeProviderProcessShape(result VerificationCommandResult) (any, error) {
	return normalizedProviderProcessShape{
		HasStdout: strings.TrimSpace(result.Stdout) != "",
		HasStderr: strings.TrimSpace(result.Stderr) != "",
		ExitCode:  result.ExitCode,
	}, nil
}

func normalizeGitLSRemoteHeads(result VerificationCommandResult) (any, error) {
	lines := trimmedOutputLines(result.Stdout)
	if len(lines) == 0 {
		return []string{}, nil
	}

	refs := make([]string, 0, len(lines))
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("git_ls_remote_heads line %d must contain <sha> <ref>", i+1)
		}
		ref := strings.TrimSpace(fields[1])
		if !strings.HasPrefix(ref, "refs/heads/") {
			return nil, fmt.Errorf("git_ls_remote_heads line %d must contain refs/heads/*, got %q", i+1, ref)
		}
		refs = append(refs, ref)
	}
	return normalizeStrings(refs), nil
}

func normalizeGitSymbolicRef(result VerificationCommandResult) (any, error) {
	ref := strings.TrimSpace(result.Stdout)
	if ref == "" {
		return nil, fmt.Errorf("git_symbolic_ref output must not be empty")
	}
	if !strings.HasPrefix(ref, "refs/") {
		return nil, fmt.Errorf("git_symbolic_ref output must start with refs/, got %q", ref)
	}
	return ref, nil
}

func decodeJSONArray(stdout string, normalizerName string) ([]any, error) {
	value, err := decodeJSONValue(stdout, normalizerName)
	if err != nil {
		return nil, err
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s output must be a JSON array", normalizerName)
	}
	return array, nil
}

func decodeJSONValue(stdout string, normalizerName string) (any, error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil, fmt.Errorf("%s output must not be empty", normalizerName)
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, fmt.Errorf("decode %s output: %w", normalizerName, err)
	}
	return value, nil
}

func trimmedOutputLines(stdout string) []string {
	if strings.TrimSpace(stdout) == "" {
		return nil
	}

	lines := strings.Split(stdout, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func labelNamesFromJSON(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		return normalizeStrings(typed)
	case []any:
		names := make([]string, 0, len(typed))
		for _, entry := range typed {
			switch label := entry.(type) {
			case string:
				names = append(names, label)
			case map[string]any:
				names = append(names, stringFromJSON(label["name"]))
			}
		}
		return normalizeStrings(names)
	default:
		return nil
	}
}

func stringFromJSON(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func intFromJSON(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		number, _ := typed.Int64()
		return int(number)
	default:
		return 0
	}
}
