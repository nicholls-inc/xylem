package gapreport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const DefaultTrackingIssueTitle = "[sota-gap] Weekly tracking"

type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

type FiledIssue struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Number  int    `json:"number,omitempty"`
	URL     string `json:"url,omitempty"`
	Created bool   `json:"created"`
}

type FileResult struct {
	Created  []FiledIssue `json:"created"`
	Existing []FiledIssue `json:"existing"`
}

func FileIssues(ctx context.Context, runner CommandRunner, repo string, delta *Delta, limit int, prefix string, labels []string) (*FileResult, error) {
	if runner == nil {
		return nil, fmt.Errorf("runner is required")
	}
	if delta == nil {
		return nil, fmt.Errorf("delta is required")
	}
	if limit <= 0 {
		limit = 3
	}
	openIssues, err := loadOpenIssues(ctx, runner, repo, prefix)
	if err != nil {
		return nil, err
	}

	result := &FileResult{}
	candidates := append([]CapabilityDelta(nil), delta.NewGaps...)
	sortCapabilityDeltas(candidates)
	for _, candidate := range candidates {
		if len(result.Created)+len(result.Existing) >= limit {
			break
		}
		title := issueTitle(prefix, candidate)
		if existing, ok := openIssues[title]; ok {
			result.Existing = append(result.Existing, FiledIssue{
				Key:     candidate.Key,
				Title:   title,
				Number:  existing.Number,
				URL:     existing.URL,
				Created: false,
			})
			continue
		}
		body := issueBody(candidate)
		args := []string{"issue", "create", "--repo", repo, "--title", title, "--body", body}
		for _, label := range labels {
			args = append(args, "--label", label)
		}
		out, err := runner.RunOutput(ctx, "gh", args...)
		if err != nil {
			return nil, fmt.Errorf("create issue %q: %w", title, err)
		}
		urlValue := strings.TrimSpace(string(out))
		number, parseErr := issueNumberFromURL(urlValue)
		if parseErr != nil {
			return nil, fmt.Errorf("parse created issue url %q: %w", urlValue, parseErr)
		}
		result.Created = append(result.Created, FiledIssue{
			Key:     candidate.Key,
			Title:   title,
			Number:  number,
			URL:     urlValue,
			Created: true,
		})
		openIssues[title] = issueSummary{Number: number, URL: urlValue}
	}
	return result, nil
}

func EnsureTrackingIssue(ctx context.Context, runner CommandRunner, repo string, title string) (FiledIssue, error) {
	if runner == nil {
		return FiledIssue{}, fmt.Errorf("runner is required")
	}
	if strings.TrimSpace(title) == "" {
		title = DefaultTrackingIssueTitle
	}
	openIssues, err := loadOpenIssues(ctx, runner, repo, title)
	if err != nil {
		return FiledIssue{}, err
	}
	if existing, ok := openIssues[title]; ok {
		return FiledIssue{Title: title, Number: existing.Number, URL: existing.URL, Created: false}, nil
	}
	body := "Weekly SoTA self-gap-analysis summaries land here so operators can track whether xylem is converging on the harness reference docs."
	out, err := runner.RunOutput(ctx, "gh", "issue", "create", "--repo", repo, "--title", title, "--body", body)
	if err != nil {
		return FiledIssue{}, fmt.Errorf("create tracking issue %q: %w", title, err)
	}
	urlValue := strings.TrimSpace(string(out))
	number, err := issueNumberFromURL(urlValue)
	if err != nil {
		return FiledIssue{}, fmt.Errorf("parse tracking issue url %q: %w", urlValue, err)
	}
	return FiledIssue{Title: title, Number: number, URL: urlValue, Created: true}, nil
}

func PostSummary(ctx context.Context, runner CommandRunner, repo string, trackingIssue int, delta *Delta, filed *FileResult, report string) error {
	if runner == nil {
		return fmt.Errorf("runner is required")
	}
	if delta == nil {
		return fmt.Errorf("delta is required")
	}
	var lines []string
	lines = append(lines, "**xylem — weekly SoTA gap analysis**")
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("- Current status counts: wired=%d, dormant=%d, not-implemented=%d",
		delta.Current.Counts[StatusWired], delta.Current.Counts[StatusDormant], delta.Current.Counts[StatusNotImplemented]))
	lines = append(lines, fmt.Sprintf("- Improvements since last snapshot: %d", len(delta.Improvements)))
	lines = append(lines, fmt.Sprintf("- New gaps this run: %d", len(delta.NewGaps)))
	if filed != nil && len(filed.Created) > 0 {
		lines = append(lines, "- Filed issues:")
		for _, item := range filed.Created {
			lines = append(lines, fmt.Sprintf("  - %s (#%d)", item.Title, item.Number))
		}
	}
	if filed != nil && len(filed.Existing) > 0 {
		lines = append(lines, "- Existing open gap issues already covered:")
		for _, item := range filed.Existing {
			lines = append(lines, fmt.Sprintf("  - %s (#%d)", item.Title, item.Number))
		}
	}
	if trimmed := strings.TrimSpace(report); trimmed != "" {
		lines = append(lines, "", "<details>", "<summary>Gap report excerpt</summary>", "", trimmed, "", "</details>")
	}
	body := strings.Join(lines, "\n")
	if _, err := runner.RunOutput(ctx, "gh", "issue", "comment", strconv.Itoa(trackingIssue), "--repo", repo, "--body", body); err != nil {
		return fmt.Errorf("post tracking summary comment: %w", err)
	}
	return nil
}

func ShouldFail(delta *Delta, filed *FileResult) bool {
	if delta == nil {
		return false
	}
	filedCount := 0
	if filed != nil {
		filedCount = len(filed.Created)
	}
	return filedCount == 0 && ImprovementCount(delta) == 0
}

type issueSummary struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

func loadOpenIssues(ctx context.Context, runner CommandRunner, repo string, search string) (map[string]issueSummary, error) {
	args := []string{"search", "issues", "--repo", repo, "--state", "open", "--json", "number,title,url", "--limit", "100"}
	if strings.TrimSpace(search) != "" {
		args = append(args, "--search", search)
	}
	out, err := runner.RunOutput(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("search open issues in %s: %w", repo, err)
	}
	var issues []issueSummary
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parse issue search output: %w", err)
	}
	index := make(map[string]issueSummary, len(issues))
	for _, item := range issues {
		index[item.Title] = item
	}
	return index, nil
}

func issueTitle(prefix string, capability CapabilityDelta) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "[sota-gap]"
	}
	return fmt.Sprintf("%s %s", prefix, capability.Name)
}

func issueBody(capability CapabilityDelta) string {
	var lines []string
	lines = append(lines, capability.Summary, "")
	lines = append(lines, fmt.Sprintf("Current status: `%s`", capability.CurrentStatus))
	if capability.PreviousStatus != "" {
		lines = append(lines, fmt.Sprintf("Previous status: `%s`", capability.PreviousStatus))
	}
	lines = append(lines, fmt.Sprintf("Priority: `%d`", capability.Priority), "")
	lines = append(lines, "## Spec sections")
	for _, section := range capability.SpecSections {
		lines = append(lines, fmt.Sprintf("- %s", section))
	}
	lines = append(lines, "", "## Current code evidence")
	for _, evidence := range capability.CodeEvidence {
		location := fmt.Sprintf("`%s:%d`", evidence.Path, evidence.LineStart)
		if evidence.LineEnd > 0 && evidence.LineEnd != evidence.LineStart {
			location = fmt.Sprintf("`%s:%d-%d`", evidence.Path, evidence.LineStart, evidence.LineEnd)
		}
		lines = append(lines, fmt.Sprintf("- %s — %s", location, evidence.Summary))
	}
	if strings.TrimSpace(capability.Recommendation) != "" {
		lines = append(lines, "", "## Suggested direction", capability.Recommendation)
	}
	return strings.Join(lines, "\n")
}

func issueNumberFromURL(raw string) (int, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	number, err := strconv.Atoi(path.Base(parsed.Path))
	if err != nil {
		return 0, err
	}
	return number, nil
}
