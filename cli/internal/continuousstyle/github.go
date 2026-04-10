package continuousstyle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const DefaultTrackingIssueTitle = "[continuous-style] Tracking"

type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

type FiledIssue struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Number  int    `json:"number,omitempty"`
	URL     string `json:"url,omitempty"`
	Created bool   `json:"created"`
}

type FileResult struct {
	Created  []FiledIssue `json:"created"`
	Existing []FiledIssue `json:"existing"`
}

func FileIssues(ctx context.Context, runner CommandRunner, repo string, report *Report, limit int, prefix string, labels []string) (*FileResult, error) {
	if runner == nil {
		return nil, fmt.Errorf("runner is required")
	}
	if report == nil {
		return nil, fmt.Errorf("report is required")
	}
	if limit <= 0 {
		limit = 3
	}
	openIssues, err := loadOpenIssues(ctx, runner, repo, prefix)
	if err != nil {
		return nil, err
	}

	result := &FileResult{}
	for _, finding := range SortedFindings(report) {
		if len(result.Created) >= limit {
			break
		}
		title := issueTitle(prefix, finding)
		if existing, ok := openIssues[title]; ok {
			result.Existing = append(result.Existing, FiledIssue{
				ID:      finding.ID,
				Title:   title,
				Number:  existing.Number,
				URL:     existing.URL,
				Created: false,
			})
			continue
		}
		body := issueBody(report.TargetSurface, finding)
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
			ID:      finding.ID,
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
	body := "Scheduled continuous-style summaries land here so operators can track terminal-output polish opportunities across xylem's CLI surfaces."
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

func PostSummary(ctx context.Context, runner CommandRunner, repo string, trackingIssue int, report *Report, filed *FileResult, summary string) error {
	if runner == nil {
		return fmt.Errorf("runner is required")
	}
	if report == nil {
		return fmt.Errorf("report is required")
	}
	lines := []string{
		"**xylem — continuous style analysis**",
		"",
		fmt.Sprintf("- Target surface: %s", report.TargetSurface),
		fmt.Sprintf("- Findings this run: %d", len(report.Findings)),
	}
	top := SortedFindings(report)
	if len(top) > 0 {
		lines = append(lines, "- Highest-priority findings:")
		for _, finding := range top[:min(3, len(top))] {
			lines = append(lines, fmt.Sprintf("  - %s (`%s`, priority=%d)", finding.Title, finding.Category, finding.Priority))
		}
	}
	if filed != nil && len(filed.Created) > 0 {
		lines = append(lines, "- Filed issues:")
		for _, item := range filed.Created {
			lines = append(lines, fmt.Sprintf("  - %s (#%d)", item.Title, item.Number))
		}
	}
	if filed != nil && len(filed.Existing) > 0 {
		lines = append(lines, "- Existing open style issues already covering these findings:")
		for _, item := range filed.Existing {
			lines = append(lines, fmt.Sprintf("  - %s (#%d)", item.Title, item.Number))
		}
	}
	if trimmed := strings.TrimSpace(summary); trimmed != "" {
		lines = append(lines, "", "<details>", "<summary>Analysis report</summary>", "", trimmed, "", "</details>")
	}
	body := strings.Join(lines, "\n")
	if _, err := runner.RunOutput(ctx, "gh", "issue", "comment", strconv.Itoa(trackingIssue), "--repo", repo, "--body", body); err != nil {
		return fmt.Errorf("post tracking summary comment: %w", err)
	}
	return nil
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

func issueTitle(prefix string, finding Finding) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "[continuous-style]"
	}
	return fmt.Sprintf("%s %s", prefix, finding.Title)
}

func issueBody(targetSurface string, finding Finding) string {
	lines := []string{
		finding.Summary,
		"",
		fmt.Sprintf("Category: `%s`", finding.Category),
		fmt.Sprintf("Priority: `%d`", finding.Priority),
		fmt.Sprintf("Target surface: `%s`", targetSurface),
		"",
		"## Affected paths",
	}
	for _, path := range finding.Paths {
		lines = append(lines, fmt.Sprintf("- `%s`", path))
	}
	lines = append(lines, "", "## Current code evidence")
	for _, evidence := range finding.Evidence {
		location := fmt.Sprintf("`%s:%d`", evidence.Path, evidence.LineStart)
		if evidence.LineEnd > 0 && evidence.LineEnd != evidence.LineStart {
			location = fmt.Sprintf("`%s:%d-%d`", evidence.Path, evidence.LineStart, evidence.LineEnd)
		}
		lines = append(lines, fmt.Sprintf("- %s — %s", location, evidence.Summary))
	}
	lines = append(lines, "", "## Suggested direction", finding.Recommendation)
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
