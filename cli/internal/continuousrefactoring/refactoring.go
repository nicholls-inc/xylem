package continuousrefactoring

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

const (
	DefaultLOCThreshold    = 120
	DefaultMaxIssuesPerRun = 2
)

var DefaultSourceDirs = []string{
	"cli/cmd/xylem",
	"cli/internal",
}

var DefaultFileExtensions = []string{
	".go",
}

var DefaultExcludePatterns = []string{
	"**/*_test.go",
	"**/testdata/**",
	".xylem/**",
}

type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

type Mode string

const (
	ModeSemantic Mode = "semantic"
	ModeFileDiet Mode = "file-diet"
)

type Manifest struct {
	Version          int                 `json:"version"`
	GeneratedAt      string              `json:"generated_at"`
	Repo             string              `json:"repo"`
	RepoRoot         string              `json:"repo_root"`
	SourceName       string              `json:"source_name"`
	SourceDirs       []string            `json:"source_dirs"`
	FileExtensions   []string            `json:"file_extensions"`
	LOCThreshold     int                 `json:"loc_threshold"`
	MaxIssuesPerRun  int                 `json:"max_issues_per_run"`
	ExcludePatterns  []string            `json:"exclude_patterns"`
	SemanticFindings []SemanticCandidate `json:"semantic_findings"`
	FileFindings     []FileCandidate     `json:"file_findings"`
}

type SemanticCandidate struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Function       string `json:"function"`
	Receiver       string `json:"receiver,omitempty"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	LOC            int    `json:"loc"`
	StatementCount int    `json:"statement_count"`
}

type FileCandidate struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	LOC  int    `json:"loc"`
}

type OpenResult struct {
	Version     int                 `json:"version"`
	GeneratedAt string              `json:"generated_at"`
	Repo        string              `json:"repo"`
	SourceName  string              `json:"source_name"`
	Mode        Mode                `json:"mode"`
	Created     []OpenedIssueResult `json:"created"`
	Skipped     []OpenedIssueResult `json:"skipped"`
}

type OpenedIssueResult struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type existingIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

type issueCandidate struct {
	ID    string
	Title string
	Body  string
}

func Inspect(cfg *config.Config, repoRoot, sourceName string, now time.Time) (*Manifest, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	srcCfg, ok := cfg.Sources[sourceName]
	if !ok {
		return nil, fmt.Errorf("source %q not found in config", sourceName)
	}
	resolvedRoot, err := canonicalizeRoot(strings.TrimSpace(repoRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}
	settings := normalizeSettings(srcCfg)
	files, err := collectCandidateFiles(resolvedRoot, settings)
	if err != nil {
		return nil, err
	}

	manifest := &Manifest{
		Version:          1,
		GeneratedAt:      now.UTC().Format(time.RFC3339),
		Repo:             resolveRepo(cfg, srcCfg),
		RepoRoot:         resolvedRoot,
		SourceName:       sourceName,
		SourceDirs:       append([]string(nil), settings.SourceDirs...),
		FileExtensions:   append([]string(nil), settings.OrderedExts...),
		LOCThreshold:     settings.LOCThreshold,
		MaxIssuesPerRun:  settings.MaxIssuesPerRun,
		ExcludePatterns:  append([]string(nil), settings.ExcludePatterns...),
		SemanticFindings: collectSemanticCandidates(resolvedRoot, settings.LOCThreshold, files),
		FileFindings:     collectFileCandidates(settings.LOCThreshold, files),
	}
	return manifest, nil
}

func OpenIssues(ctx context.Context, runner CommandRunner, manifest *Manifest, mode Mode, now time.Time) (*OpenResult, error) {
	if runner == nil {
		return nil, fmt.Errorf("runner is required")
	}
	if manifest == nil {
		return nil, fmt.Errorf("manifest is required")
	}
	if strings.TrimSpace(manifest.Repo) == "" {
		return nil, fmt.Errorf("manifest repo is required")
	}
	if mode != ModeSemantic && mode != ModeFileDiet {
		return nil, fmt.Errorf("unsupported mode %q", mode)
	}

	candidates := issueCandidatesForMode(manifest, mode)
	existing, err := loadExistingIssues(ctx, runner, manifest.Repo)
	if err != nil {
		return nil, err
	}

	result := &OpenResult{
		Version:     1,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Repo:        manifest.Repo,
		SourceName:  manifest.SourceName,
		Mode:        mode,
	}
	for _, candidate := range candidates {
		if match, ok := findExistingIssue(existing, candidate); ok {
			result.Skipped = append(result.Skipped, OpenedIssueResult{
				ID:     candidate.ID,
				Title:  candidate.Title,
				Number: match.Number,
				Reason: "already tracked",
			})
			continue
		}
		out, err := runner.RunOutput(ctx, "gh", "issue", "create",
			"--repo", manifest.Repo,
			"--title", candidate.Title,
			"--body", candidate.Body,
		)
		if err != nil {
			return nil, fmt.Errorf("create issue %q: %w", candidate.ID, err)
		}
		url := strings.TrimSpace(string(out))
		result.Created = append(result.Created, OpenedIssueResult{
			ID:    candidate.ID,
			Title: candidate.Title,
			URL:   url,
		})
	}
	return result, nil
}

func LoadManifest(path string) (*Manifest, error) {
	var manifest Manifest
	if err := loadJSON(path, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func loadJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

type normalizedSettings struct {
	SourceDirs      []string
	FileExtensions  map[string]struct{}
	OrderedExts     []string
	LOCThreshold    int
	MaxIssuesPerRun int
	ExcludePatterns []string
}

func normalizeSettings(src config.SourceConfig) normalizedSettings {
	sourceDirs := append([]string(nil), src.SourceDirs...)
	if len(sourceDirs) == 0 {
		sourceDirs = append([]string(nil), DefaultSourceDirs...)
	}
	fileExtensions := append([]string(nil), src.FileExtensions...)
	if len(fileExtensions) == 0 {
		fileExtensions = append([]string(nil), DefaultFileExtensions...)
	}
	extSet := make(map[string]struct{}, len(fileExtensions))
	orderedExts := make([]string, 0, len(fileExtensions))
	for _, ext := range fileExtensions {
		trimmed := strings.TrimSpace(ext)
		if trimmed == "" {
			continue
		}
		if _, ok := extSet[trimmed]; ok {
			continue
		}
		extSet[trimmed] = struct{}{}
		orderedExts = append(orderedExts, trimmed)
	}
	locThreshold := src.LOCThreshold
	if locThreshold <= 0 {
		locThreshold = DefaultLOCThreshold
	}
	maxIssues := src.MaxIssuesPerRun
	if maxIssues <= 0 {
		maxIssues = DefaultMaxIssuesPerRun
	}
	excludePatterns := append([]string(nil), src.ExcludePatterns...)
	if len(excludePatterns) == 0 {
		excludePatterns = append([]string(nil), DefaultExcludePatterns...)
	}
	return normalizedSettings{
		SourceDirs:      sourceDirs,
		FileExtensions:  extSet,
		OrderedExts:     orderedExts,
		LOCThreshold:    locThreshold,
		MaxIssuesPerRun: maxIssues,
		ExcludePatterns: excludePatterns,
	}
}

type scannedFile struct {
	RelativePath string
	AbsolutePath string
	Content      []byte
	LOC          int
}

func collectCandidateFiles(repoRoot string, settings normalizedSettings) ([]scannedFile, error) {
	seen := make(map[string]struct{})
	files := make([]scannedFile, 0)
	for _, sourceDir := range settings.SourceDirs {
		root, err := resolveSourceDir(repoRoot, sourceDir)
		if err != nil {
			return nil, err
		}
		if err := filepath.WalkDir(root, func(filePath string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return fmt.Errorf("walk %s: %w", sourceDir, walkErr)
			}
			if d.IsDir() {
				return nil
			}
			ext := filepath.Ext(filePath)
			if _, ok := settings.FileExtensions[ext]; !ok {
				return nil
			}
			relPath, err := filepath.Rel(repoRoot, filePath)
			if err != nil {
				return fmt.Errorf("relative path for %s: %w", filePath, err)
			}
			normalized := normalizePath(relPath)
			if matchesAnyPattern(normalized, settings.ExcludePatterns) {
				return nil
			}
			if _, ok := seen[normalized]; ok {
				return nil
			}
			seen[normalized] = struct{}{}
			content, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("read %s: %w", normalized, err)
			}
			files = append(files, scannedFile{
				RelativePath: normalized,
				AbsolutePath: filePath,
				Content:      content,
				LOC:          countLOC(content),
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	return files, nil
}

func resolveSourceDir(repoRoot, sourceDir string) (string, error) {
	canonicalRepoRoot, err := canonicalizeRoot(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repo root for source dir %q: %w", sourceDir, err)
	}

	root := filepath.Join(canonicalRepoRoot, sourceDir)
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve source dir %q: %w", sourceDir, err)
	}
	if evalRoot, evalErr := filepath.EvalSymlinks(resolvedRoot); evalErr == nil {
		resolvedRoot = evalRoot
	} else if !os.IsNotExist(evalErr) {
		return "", fmt.Errorf("evaluate source dir %q: %w", sourceDir, evalErr)
	}
	rel, err := filepath.Rel(canonicalRepoRoot, resolvedRoot)
	if err != nil {
		return "", fmt.Errorf("relativize source dir %q: %w", sourceDir, err)
	}
	parentPrefix := ".." + string(filepath.Separator)
	if rel == ".." || strings.HasPrefix(rel, parentPrefix) {
		return "", fmt.Errorf("source dir %q escapes repo root", sourceDir)
	}
	return resolvedRoot, nil
}

func canonicalizeRoot(root string) (string, error) {
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if evalRoot, evalErr := filepath.EvalSymlinks(resolvedRoot); evalErr == nil {
		resolvedRoot = evalRoot
	} else if !os.IsNotExist(evalErr) {
		return "", evalErr
	}
	return resolvedRoot, nil
}

func collectSemanticCandidates(repoRoot string, locThreshold int, files []scannedFile) []SemanticCandidate {
	candidates := make([]SemanticCandidate, 0)
	for _, file := range files {
		if filepath.Ext(file.AbsolutePath) != ".go" {
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file.AbsolutePath, file.Content, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			start := fset.Position(fn.Pos()).Line
			end := fset.Position(fn.End()).Line
			loc := end - start + 1
			if loc < locThreshold {
				continue
			}
			receiver := receiverName(fn)
			candidates = append(candidates, SemanticCandidate{
				ID:             semanticID(file.RelativePath, receiver, fn.Name.Name),
				Path:           file.RelativePath,
				Function:       fn.Name.Name,
				Receiver:       receiver,
				StartLine:      start,
				EndLine:        end,
				LOC:            loc,
				StatementCount: countStatements(fn.Body),
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftScore := candidates[i].LOC*10 + candidates[i].StatementCount
		rightScore := candidates[j].LOC*10 + candidates[j].StatementCount
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		if candidates[i].Path != candidates[j].Path {
			return candidates[i].Path < candidates[j].Path
		}
		if candidates[i].Receiver != candidates[j].Receiver {
			return candidates[i].Receiver < candidates[j].Receiver
		}
		return candidates[i].Function < candidates[j].Function
	})
	return candidates
}

func collectFileCandidates(locThreshold int, files []scannedFile) []FileCandidate {
	candidates := make([]FileCandidate, 0, len(files))
	for _, file := range files {
		if file.LOC < locThreshold {
			continue
		}
		candidates = append(candidates, FileCandidate{
			ID:   fileID(file.RelativePath),
			Path: file.RelativePath,
			LOC:  file.LOC,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].LOC != candidates[j].LOC {
			return candidates[i].LOC > candidates[j].LOC
		}
		return candidates[i].Path < candidates[j].Path
	})
	return candidates
}

func issueCandidatesForMode(manifest *Manifest, mode Mode) []issueCandidate {
	limit := manifest.MaxIssuesPerRun
	if limit <= 0 {
		limit = DefaultMaxIssuesPerRun
	}

	switch mode {
	case ModeSemantic:
		candidates := make([]issueCandidate, 0, min(limit, len(manifest.SemanticFindings)))
		for _, finding := range manifest.SemanticFindings {
			candidates = append(candidates, issueCandidate{
				ID:    finding.ID,
				Title: fmt.Sprintf("[refactor] split %s in %s", qualifiedFunctionName(finding), finding.Path),
				Body: strings.TrimSpace(fmt.Sprintf(`
<!-- xylem:continuous-refactoring id:%s -->
## Why this function is a refactor target
- Path: %s
- Function: %s
- Lines: %d-%d (%d LOC)
- Statements: %d
- Scope: %s

## Suggested outcome
- Extract smaller helpers so the function falls below the configured %d LOC threshold.
- Preserve current behavior and public APIs.
- Add or update focused tests if the refactor exposes missing coverage seams.
`, finding.ID, finding.Path, qualifiedFunctionName(finding), finding.StartLine, finding.EndLine, finding.LOC, finding.StatementCount, manifest.SourceName, manifest.LOCThreshold)),
			})
			if len(candidates) == limit {
				break
			}
		}
		return candidates
	case ModeFileDiet:
		candidates := make([]issueCandidate, 0, min(limit, len(manifest.FileFindings)))
		for _, finding := range manifest.FileFindings {
			candidates = append(candidates, issueCandidate{
				ID:    finding.ID,
				Title: fmt.Sprintf("[file-diet] simplify %s", finding.Path),
				Body: strings.TrimSpace(fmt.Sprintf(`
<!-- xylem:continuous-refactoring id:%s -->
## Why this file needs a diet pass
- Path: %s
- Current size: %d LOC
- Configured threshold: %d LOC
- Scope: %s

## Suggested outcome
- Split the file into smaller focused units or helpers.
- Remove low-value indirection and keep behavior unchanged.
- Leave the resulting structure easier to navigate than the current monolith.
`, finding.ID, finding.Path, finding.LOC, manifest.LOCThreshold, manifest.SourceName)),
			})
			if len(candidates) == limit {
				break
			}
		}
		return candidates
	default:
		return nil
	}
}

func loadExistingIssues(ctx context.Context, runner CommandRunner, repo string) ([]existingIssue, error) {
	openIssues, err := readIssues(ctx, runner, repo, "open")
	if err != nil {
		return nil, err
	}
	closedIssues, err := readIssues(ctx, runner, repo, "closed")
	if err != nil {
		return nil, err
	}
	return append(openIssues, closedIssues...), nil
}

func readIssues(ctx context.Context, runner CommandRunner, repo, state string) ([]existingIssue, error) {
	out, err := runner.RunOutput(ctx, "gh", "issue", "list",
		"--repo", repo,
		"--state", state,
		"--json", "number,title,body",
		"--limit", "100",
	)
	if err != nil {
		return nil, fmt.Errorf("list %s issues: %w", state, err)
	}
	var issues []existingIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parse %s issues: %w", state, err)
	}
	return issues, nil
}

func findExistingIssue(issues []existingIssue, candidate issueCandidate) (existingIssue, bool) {
	marker := issueMarker(candidate.ID)
	for _, issue := range issues {
		if strings.Contains(issue.Body, marker) || strings.TrimSpace(issue.Title) == strings.TrimSpace(candidate.Title) {
			return issue, true
		}
	}
	return existingIssue{}, false
}

func issueMarker(id string) string {
	return fmt.Sprintf("xylem:continuous-refactoring id:%s", id)
}

func resolveRepo(cfg *config.Config, src config.SourceConfig) string {
	if repo := strings.TrimSpace(src.Repo); repo != "" {
		return repo
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.Repo)
	}
	return ""
}

func countStatements(body *ast.BlockStmt) int {
	count := 0
	ast.Inspect(body, func(node ast.Node) bool {
		if _, ok := node.(ast.Stmt); ok {
			count++
		}
		return true
	})
	return count
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch expr := fn.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.StarExpr:
		if ident, ok := expr.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return ""
}

func qualifiedFunctionName(candidate SemanticCandidate) string {
	if candidate.Receiver == "" {
		return candidate.Function
	}
	return candidate.Receiver + "." + candidate.Function
}

func semanticID(relPath, receiver, functionName string) string {
	base := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	parts := []string{"semantic", normalizeToken(base)}
	if receiver != "" {
		parts = append(parts, normalizeToken(receiver))
	}
	parts = append(parts, normalizeToken(functionName))
	return strings.Join(parts, "-")
}

func fileID(relPath string) string {
	return "file-diet-" + normalizeToken(strings.TrimSuffix(relPath, filepath.Ext(relPath)))
}

func normalizeToken(value string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "_", "-", ".", "-", "*", "-", " ", "-")
	cleaned := replacer.Replace(strings.ToLower(value))
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "item"
	}
	return cleaned
}

func countLOC(content []byte) int {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		count++
	}
	return count
}

func matchesAnyPattern(target string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := doublestarMatch(pattern, target)
		if err == nil && matched {
			return true
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

func normalizePath(value string) string {
	return filepath.ToSlash(filepath.Clean(value))
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
