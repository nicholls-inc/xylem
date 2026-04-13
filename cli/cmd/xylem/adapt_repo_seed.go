package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/profiles"
)

const (
	adaptRepoIssueTitle     = "[xylem] adapt harness to this repository"
	adaptRepoIssueBody      = "xylem created this issue so the first adaptation cycle can produce a reviewable harness-update PR for this repository.\n\nThe seeded workflow must only touch xylem control-plane files, validate the proposed harness changes, and open a PR for review."
	adaptRepoSeedLabel      = "xylem-adapt-repo"
	adaptRepoReadyLabel     = "ready-for-work"
	adaptRepoSeededByDaemon = "daemon"
	adaptRepoSeededByInit   = "init"
)

var adaptRepoIssueNumberPattern = regexp.MustCompile(`/issues/(\d+)$`)

type adaptRepoSeedRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type adaptRepoIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type adaptRepoSeedMarker struct {
	SeededAt       string `json:"seeded_at"`
	IssueNumber    int    `json:"issue_number,omitempty"`
	IssueURL       string `json:"issue_url,omitempty"`
	ProfileVersion int    `json:"profile_version"`
	SeededBy       string `json:"seeded_by"`
}

func ensureAdaptRepoSeeded(ctx context.Context, cfg *config.Config, runner adaptRepoSeedRunner, seededBy string) (*adaptRepoSeedMarker, error) {
	if cfg == nil {
		return nil, fmt.Errorf("ensure adapt-repo seeded: config is required")
	}
	if runner == nil {
		return nil, fmt.Errorf("ensure adapt-repo seeded: command runner is required")
	}

	markerPath := adaptRepoSeedMarkerPath(cfg.StateDir)
	if _, err := os.Stat(markerPath); err == nil {
		marker, readErr := readAdaptRepoSeedMarker(markerPath)
		if readErr != nil {
			return nil, fmt.Errorf("ensure adapt-repo seeded: %w", readErr)
		}
		return marker, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("ensure adapt-repo seeded: stat marker: %w", err)
	}

	repo := primaryGitHubRepo(cfg)
	if repo == "" {
		return nil, nil
	}

	issue, err := findExistingAdaptRepoIssue(ctx, runner, repo)
	if err != nil {
		return nil, fmt.Errorf("ensure adapt-repo seeded: %w", err)
	}
	if issue == nil {
		issue, err = createAdaptRepoIssue(ctx, runner, repo)
		if err != nil {
			return nil, fmt.Errorf("ensure adapt-repo seeded: %w", err)
		}
	}

	version, ok := profiles.Version("core")
	if !ok {
		return nil, fmt.Errorf("ensure adapt-repo seeded: core profile version is unknown")
	}

	marker := &adaptRepoSeedMarker{
		SeededAt:       daemonNow().UTC().Format(timeFormatRFC3339),
		ProfileVersion: version,
		SeededBy:       seededBy,
	}
	if issue != nil {
		marker.IssueNumber = issue.Number
		marker.IssueURL = issue.URL
	}

	if err := writeAdaptRepoSeedMarker(markerPath, marker); err != nil {
		return nil, fmt.Errorf("ensure adapt-repo seeded: %w", err)
	}
	return marker, nil
}

func adaptRepoSeedMarkerPath(stateDir string) string {
	return filepath.Join(stateDir, "state", "bootstrap", "adapt-repo-seeded.json")
}

func readAdaptRepoSeedMarker(path string) (*adaptRepoSeedMarker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read adapt-repo seed marker %q: %w", path, err)
	}

	var marker adaptRepoSeedMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("parse adapt-repo seed marker %q: %w", path, err)
	}
	return &marker, nil
}

func writeAdaptRepoSeedMarker(path string, marker *adaptRepoSeedMarker) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create adapt-repo seed marker directory: %w", err)
	}

	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal adapt-repo seed marker: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write adapt-repo seed marker %q: %w", path, err)
	}
	return nil
}

func primaryGitHubRepo(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if strings.TrimSpace(cfg.Repo) != "" {
		return strings.TrimSpace(cfg.Repo)
	}

	names := make([]string, 0, len(cfg.Sources))
	for name := range cfg.Sources {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		src := cfg.Sources[name]
		switch src.Type {
		case "github", "github-pr", "github-pr-events", "github-merge", "scheduled":
			if repo := strings.TrimSpace(src.Repo); repo != "" {
				return repo
			}
		}
	}
	return ""
}

func findExistingAdaptRepoIssue(ctx context.Context, runner adaptRepoSeedRunner, repo string) (*adaptRepoIssue, error) {
	for _, state := range []string{"open", "closed"} {
		issue, err := findAdaptRepoIssueByState(ctx, runner, repo, state)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			return issue, nil
		}
	}
	return nil, nil
}

func findAdaptRepoIssueByState(ctx context.Context, runner adaptRepoSeedRunner, repo, state string) (*adaptRepoIssue, error) {
	out, err := runner.Run(ctx, "gh", "issue", "list",
		"--repo", repo,
		"--state", state,
		"--label", adaptRepoSeedLabel,
		"--json", "number,title,url",
		"--limit", "100",
	)
	if err != nil {
		return nil, fmt.Errorf("list adapt-repo seed issues in %s state: %w", state, err)
	}

	var issues []adaptRepoIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parse adapt-repo seed issue list output for %s state: %w", state, err)
	}
	for _, issue := range issues {
		if issue.Title == adaptRepoIssueTitle {
			return &issue, nil
		}
	}
	return nil, nil
}

func createAdaptRepoIssue(ctx context.Context, runner adaptRepoSeedRunner, repo string) (*adaptRepoIssue, error) {
	out, err := runner.Run(ctx, "gh", "issue", "create",
		"--repo", repo,
		"--title", adaptRepoIssueTitle,
		"--body", adaptRepoIssueBody,
		"--label", adaptRepoSeedLabel,
		"--label", adaptRepoReadyLabel,
	)
	if err != nil {
		return nil, fmt.Errorf("create adapt-repo seed issue: %w", err)
	}

	url := strings.TrimSpace(string(out))
	if url == "" {
		return nil, fmt.Errorf("create adapt-repo seed issue: gh returned an empty URL")
	}

	number, err := parseAdaptRepoIssueNumber(url)
	if err != nil {
		return nil, fmt.Errorf("create adapt-repo seed issue: %w", err)
	}

	return &adaptRepoIssue{
		Number: number,
		Title:  adaptRepoIssueTitle,
		URL:    url,
	}, nil
}

func parseAdaptRepoIssueNumber(url string) (int, error) {
	match := adaptRepoIssueNumberPattern.FindStringSubmatch(strings.TrimSpace(url))
	if match == nil {
		return 0, fmt.Errorf("parse adapt-repo issue number from %q", url)
	}

	var number int
	if _, err := fmt.Sscanf(match[1], "%d", &number); err != nil {
		return 0, fmt.Errorf("scan adapt-repo issue number from %q: %w", url, err)
	}
	return number, nil
}
