package simplicity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultWindowDays          = 7
	DefaultMaxPRs              = 3
	DefaultMinConfidence       = 0.8
	DefaultMinDuplicateLines   = 10
	DefaultMinDuplicateTargets = 3
)

var DefaultExcludeGlobs = []string{
	"**/*_test.go",
	".xylem/workflows/**",
	".xylem/prompts/**",
}

type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ChangeManifest struct {
	Version     int           `json:"version"`
	GeneratedAt string        `json:"generated_at"`
	RepoRoot    string        `json:"repo_root"`
	WindowDays  int           `json:"window_days"`
	Since       string        `json:"since"`
	Files       []ChangedFile `json:"files"`
}

type ChangedFile struct {
	Path string `json:"path"`
}

type Finding struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Title           string   `json:"title"`
	Summary         string   `json:"summary"`
	Paths           []string `json:"paths"`
	Confidence      float64  `json:"confidence"`
	DuplicateLines  int      `json:"duplicate_lines,omitempty"`
	LocationCount   int      `json:"location_count,omitempty"`
	Implementation  string   `json:"implementation,omitempty"`
	PullRequestBody string   `json:"pull_request_body,omitempty"`
}

type FindingsFile struct {
	Version     int       `json:"version"`
	GeneratedAt string    `json:"generated_at"`
	Findings    []Finding `json:"findings"`
}

type PlanOptions struct {
	MaxPRs                int      `json:"max_prs"`
	MinConfidence         float64  `json:"min_confidence"`
	MinDuplicateLines     int      `json:"min_duplicate_lines"`
	MinDuplicateLocations int      `json:"min_duplicate_locations"`
	ExcludeGlobs          []string `json:"exclude_globs,omitempty"`
}

type PRPlan struct {
	Version     int         `json:"version"`
	GeneratedAt string      `json:"generated_at"`
	Repo        string      `json:"repo"`
	BaseBranch  string      `json:"base_branch"`
	Options     PlanOptions `json:"options"`
	Selected    []PlannedPR `json:"selected"`
	Skipped     []SkippedPR `json:"skipped"`
}

type PlannedPR struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Branch         string   `json:"branch"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Summary        string   `json:"summary"`
	Paths          []string `json:"paths"`
	Confidence     float64  `json:"confidence"`
	DuplicateLines int      `json:"duplicate_lines,omitempty"`
	LocationCount  int      `json:"location_count,omitempty"`
	Implementation string   `json:"implementation,omitempty"`
}

type SkippedPR struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type OpenResult struct {
	Version     int              `json:"version"`
	GeneratedAt string           `json:"generated_at"`
	Repo        string           `json:"repo"`
	Created     []OpenedPRResult `json:"created"`
	Skipped     []OpenedPRResult `json:"skipped"`
}

type OpenedPRResult struct {
	ID     string `json:"id"`
	Branch string `json:"branch"`
	URL    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func ScanRecentChanges(ctx context.Context, runner CommandRunner, repoRoot string, now time.Time, windowDays int) (*ChangeManifest, error) {
	if runner == nil {
		return nil, fmt.Errorf("runner is required")
	}
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil, fmt.Errorf("repo_root is required")
	}
	if windowDays <= 0 {
		return nil, fmt.Errorf("window_days must be greater than 0")
	}

	since := now.UTC().Add(-time.Duration(windowDays) * 24 * time.Hour)
	out, err := runner.RunOutput(
		ctx,
		"git",
		"-C", repoRoot,
		"log",
		"--name-only",
		"--pretty=format:",
		"--diff-filter=ACMRTUXB",
		"--since", since.Format(time.RFC3339),
		"HEAD",
	)
	if err != nil {
		return nil, fmt.Errorf("git log changed files: %w", err)
	}

	files, err := parseChangedFiles(string(out), repoRoot)
	if err != nil {
		return nil, err
	}
	return &ChangeManifest{
		Version:     1,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		RepoRoot:    filepath.Clean(repoRoot),
		WindowDays:  windowDays,
		Since:       since.Format(time.RFC3339),
		Files:       files,
	}, nil
}

func parseChangedFiles(raw, repoRoot string) ([]ChangedFile, error) {
	seen := make(map[string]struct{})
	files := make([]ChangedFile, 0)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := filepath.ToSlash(strings.TrimSpace(line))
		if trimmed == "" {
			continue
		}
		clean := strings.TrimPrefix(filepath.Clean(trimmed), "./")
		if clean == "." {
			continue
		}
		info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(clean)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat changed file %q: %w", clean, err)
		}
		if info.IsDir() {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		files = append(files, ChangedFile{Path: clean})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func LoadFindings(path string) (*FindingsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read findings %q: %w", path, err)
	}
	var findings FindingsFile
	if err := json.Unmarshal(data, &findings); err != nil {
		return nil, fmt.Errorf("parse findings %q: %w", path, err)
	}
	if err := findings.Validate(); err != nil {
		return nil, fmt.Errorf("validate findings %q: %w", path, err)
	}
	return &findings, nil
}

func (f *FindingsFile) Validate() error {
	if f == nil {
		return fmt.Errorf("findings file must not be nil")
	}
	if f.Version <= 0 {
		return fmt.Errorf("version must be greater than 0")
	}
	if strings.TrimSpace(f.GeneratedAt) == "" {
		return fmt.Errorf("generated_at is required")
	}
	if _, err := time.Parse(time.RFC3339, f.GeneratedAt); err != nil {
		return fmt.Errorf("generated_at must be RFC3339: %w", err)
	}
	seen := make(map[string]struct{}, len(f.Findings))
	for i := range f.Findings {
		if err := f.Findings[i].Validate(); err != nil {
			return fmt.Errorf("findings[%d]: %w", i, err)
		}
		if _, ok := seen[f.Findings[i].ID]; ok {
			return fmt.Errorf("duplicate finding id %q", f.Findings[i].ID)
		}
		seen[f.Findings[i].ID] = struct{}{}
	}
	return nil
}

func (f *Finding) Validate() error {
	if strings.TrimSpace(f.ID) == "" {
		return fmt.Errorf("id is required")
	}
	switch f.Kind {
	case "simplification", "duplication":
	default:
		return fmt.Errorf("kind %q is invalid", f.Kind)
	}
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(f.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	if len(f.Paths) == 0 {
		return fmt.Errorf("paths must not be empty")
	}
	seen := make(map[string]struct{}, len(f.Paths))
	for _, path := range f.Paths {
		path = normalizePath(path)
		if path == "" {
			return fmt.Errorf("paths must not contain empty entries")
		}
		if _, ok := seen[path]; ok {
			return fmt.Errorf("paths must not contain duplicates")
		}
		seen[path] = struct{}{}
	}
	if f.Kind == "duplication" {
		if f.DuplicateLines < 0 {
			return fmt.Errorf("duplicate_lines must be non-negative")
		}
		if f.LocationCount < 0 {
			return fmt.Errorf("location_count must be non-negative")
		}
	}
	return nil
}

func BuildPlan(repo, baseBranch string, options PlanOptions, simplifications, duplicates *FindingsFile, now time.Time) (*PRPlan, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, fmt.Errorf("repo is required")
	}
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		return nil, fmt.Errorf("base_branch is required")
	}
	if err := validatePlanOptions(options); err != nil {
		return nil, err
	}
	if simplifications == nil || duplicates == nil {
		return nil, fmt.Errorf("simplification and duplication findings are required")
	}
	if err := simplifications.Validate(); err != nil {
		return nil, fmt.Errorf("simplifications: %w", err)
	}
	if err := duplicates.Validate(); err != nil {
		return nil, fmt.Errorf("duplications: %w", err)
	}

	candidates := make([]scoredFinding, 0, len(simplifications.Findings)+len(duplicates.Findings))
	skipped := make([]SkippedPR, 0)
	for _, finding := range append(append([]Finding(nil), simplifications.Findings...), duplicates.Findings...) {
		reason := skipReason(finding, options)
		if reason != "" {
			skipped = append(skipped, SkippedPR{ID: finding.ID, Reason: reason})
			continue
		}
		candidates = append(candidates, scoredFinding{
			Finding: finding,
			Score:   scoreFinding(finding),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].ID < candidates[j].ID
	})

	selected := make([]PlannedPR, 0, min(options.MaxPRs, len(candidates)))
	seenSelectedIDs := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seenSelectedIDs[candidate.ID]; ok {
			skipped = append(skipped, SkippedPR{ID: candidate.ID, Reason: "duplicate finding id"})
			continue
		}
		if len(selected) >= options.MaxPRs {
			skipped = append(skipped, SkippedPR{ID: candidate.ID, Reason: "exceeds max_prs"})
			continue
		}
		selected = append(selected, planEntry(candidate.Finding, len(selected)+1))
		seenSelectedIDs[candidate.ID] = struct{}{}
	}
	sort.Slice(skipped, func(i, j int) bool {
		if skipped[i].Reason != skipped[j].Reason {
			return skipped[i].Reason < skipped[j].Reason
		}
		return skipped[i].ID < skipped[j].ID
	})

	return &PRPlan{
		Version:     1,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Repo:        repo,
		BaseBranch:  baseBranch,
		Options:     options,
		Selected:    selected,
		Skipped:     skipped,
	}, nil
}

func (p *PRPlan) Validate() error {
	if p == nil {
		return fmt.Errorf("plan must not be nil")
	}
	if p.Version <= 0 {
		return fmt.Errorf("version must be greater than 0")
	}
	if strings.TrimSpace(p.GeneratedAt) == "" {
		return fmt.Errorf("generated_at is required")
	}
	if _, err := time.Parse(time.RFC3339, p.GeneratedAt); err != nil {
		return fmt.Errorf("generated_at must be RFC3339: %w", err)
	}
	if strings.TrimSpace(p.Repo) == "" {
		return fmt.Errorf("repo is required")
	}
	if strings.TrimSpace(p.BaseBranch) == "" {
		return fmt.Errorf("base_branch is required")
	}
	if err := validatePlanOptions(p.Options); err != nil {
		return err
	}
	if len(p.Selected) > p.Options.MaxPRs {
		return fmt.Errorf("selected entries exceed max_prs")
	}
	seenIDs := make(map[string]struct{}, len(p.Selected))
	seenBranches := make(map[string]struct{}, len(p.Selected))
	for i := range p.Selected {
		if err := p.Selected[i].Validate(); err != nil {
			return fmt.Errorf("selected[%d]: %w", i, err)
		}
		if _, ok := seenIDs[p.Selected[i].ID]; ok {
			return fmt.Errorf("duplicate selected id %q", p.Selected[i].ID)
		}
		if _, ok := seenBranches[p.Selected[i].Branch]; ok {
			return fmt.Errorf("duplicate selected branch %q", p.Selected[i].Branch)
		}
		seenIDs[p.Selected[i].ID] = struct{}{}
		seenBranches[p.Selected[i].Branch] = struct{}{}
	}
	return nil
}

func (p *PlannedPR) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("id is required")
	}
	switch p.Kind {
	case "simplification", "duplication":
	default:
		return fmt.Errorf("kind %q is invalid", p.Kind)
	}
	if strings.TrimSpace(p.Branch) == "" {
		return fmt.Errorf("branch is required")
	}
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(p.Body) == "" {
		return fmt.Errorf("body is required")
	}
	if strings.TrimSpace(p.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	if len(p.Paths) == 0 {
		return fmt.Errorf("paths must not be empty")
	}
	return nil
}

func OpenPRs(ctx context.Context, runner CommandRunner, plan *PRPlan, now time.Time) (*OpenResult, error) {
	if runner == nil {
		return nil, fmt.Errorf("runner is required")
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	result := &OpenResult{
		Version:     1,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Repo:        plan.Repo,
	}
	for _, entry := range plan.Selected {
		existingURL, err := lookupOpenPR(ctx, runner, plan.Repo, entry.Branch)
		if err != nil {
			return nil, fmt.Errorf("lookup open PR for %s: %w", entry.Branch, err)
		}
		if existingURL != "" {
			result.Skipped = append(result.Skipped, OpenedPRResult{
				ID:     entry.ID,
				Branch: entry.Branch,
				URL:    existingURL,
				Reason: "already open",
			})
			continue
		}
		out, err := runner.RunOutput(
			ctx,
			"gh", "pr", "create",
			"--repo", plan.Repo,
			"--head", entry.Branch,
			"--base", plan.BaseBranch,
			"--title", entry.Title,
			"--body", entry.Body,
		)
		if err != nil {
			return nil, fmt.Errorf("create PR for %s: %w", entry.Branch, err)
		}
		result.Created = append(result.Created, OpenedPRResult{
			ID:     entry.ID,
			Branch: entry.Branch,
			URL:    strings.TrimSpace(string(out)),
		})
	}
	return result, nil
}

func LoadPlan(path string) (*PRPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan %q: %w", path, err)
	}
	var plan PRPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("parse plan %q: %w", path, err)
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("validate plan %q: %w", path, err)
	}
	return &plan, nil
}

func WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %q: %w", path, err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json for %q: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write json %q: %w", path, err)
	}
	return nil
}

type scoredFinding struct {
	Finding
	Score float64
}

func validatePlanOptions(options PlanOptions) error {
	if options.MaxPRs <= 0 {
		return fmt.Errorf("max_prs must be greater than 0")
	}
	if options.MinConfidence < 0 || options.MinConfidence > 1 {
		return fmt.Errorf("min_confidence must be between 0 and 1")
	}
	if options.MinDuplicateLines < 0 {
		return fmt.Errorf("min_duplicate_lines must be non-negative")
	}
	if options.MinDuplicateLocations < 0 {
		return fmt.Errorf("min_duplicate_locations must be non-negative")
	}
	return nil
}

func skipReason(f Finding, options PlanOptions) string {
	if f.Confidence < options.MinConfidence {
		return "below min_confidence"
	}
	if matchesAnyPath(f.Paths, options.ExcludeGlobs) {
		return "matches excluded path"
	}
	if f.Kind == "duplication" {
		if f.DuplicateLines < options.MinDuplicateLines {
			return "below min_duplicate_lines"
		}
		if f.LocationCount < options.MinDuplicateLocations {
			return "below min_duplicate_locations"
		}
	}
	return ""
}

func matchesAnyPath(paths, globs []string) bool {
	for _, path := range paths {
		normalized := normalizePath(path)
		for _, glob := range globs {
			matched, err := doublestarMatch(glob, normalized)
			if err == nil && matched {
				return true
			}
		}
	}
	return false
}

func doublestarMatch(pattern, target string) (bool, error) {
	pattern = normalizePath(pattern)
	target = normalizePath(target)
	if !strings.Contains(pattern, "**") {
		return path.Match(pattern, target)
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		for candidate := target; ; {
			matched, err := path.Match(suffix, candidate)
			if matched || err != nil {
				return matched, err
			}
			slash := strings.IndexByte(candidate, '/')
			if slash < 0 {
				return false, nil
			}
			candidate = candidate[slash+1:]
		}
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return target == prefix || strings.HasPrefix(target, prefix+"/"), nil
	}
	return path.Match(strings.ReplaceAll(pattern, "**", "*"), target)
}

func scoreFinding(f Finding) float64 {
	score := f.Confidence * 100
	if f.Kind == "duplication" {
		score += float64(f.DuplicateLines) + float64(f.LocationCount*10)
		return score
	}
	score += float64(len(f.Paths) * 5)
	return score
}

func planEntry(f Finding, ordinal int) PlannedPR {
	paths := make([]string, 0, len(f.Paths))
	for _, path := range f.Paths {
		paths = append(paths, normalizePath(path))
	}
	sort.Strings(paths)

	body := strings.TrimSpace(f.PullRequestBody)
	if body == "" {
		body = fmt.Sprintf("## Summary\n- %s\n\n## Paths\n%s", f.Summary, bulletList(paths))
	}
	return PlannedPR{
		ID:             f.ID,
		Kind:           f.Kind,
		Branch:         fmt.Sprintf("continuous-simplicity/%02d-%s", ordinal, sanitizeBranchComponent(f.ID)),
		Title:          f.Title,
		Body:           body,
		Summary:        f.Summary,
		Paths:          paths,
		Confidence:     f.Confidence,
		DuplicateLines: f.DuplicateLines,
		LocationCount:  f.LocationCount,
		Implementation: strings.TrimSpace(f.Implementation),
	}
}

func lookupOpenPR(ctx context.Context, runner CommandRunner, repo, branch string) (string, error) {
	out, err := runner.RunOutput(
		ctx,
		"gh", "pr", "list",
		"--repo", repo,
		"--head", branch,
		"--state", "open",
		"--json", "url",
		"--limit", "1",
	)
	if err != nil {
		return "", err
	}
	var prs []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return "", fmt.Errorf("parse gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return "", nil
	}
	return strings.TrimSpace(prs[0].URL), nil
}

func normalizePath(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(strings.TrimSpace(path))), "./")
}

func sanitizeBranchComponent(raw string) string {
	replacer := strings.NewReplacer("/", "-", "_", "-", " ", "-", ".", "-")
	value := strings.ToLower(strings.TrimSpace(replacer.Replace(raw)))
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(out.String(), "-")
	if result == "" {
		return "candidate"
	}
	return result
}

func bulletList(paths []string) string {
	lines := make([]string, 0, len(paths))
	for _, path := range paths {
		lines = append(lines, "- `"+path+"`")
	}
	return strings.Join(lines, "\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
