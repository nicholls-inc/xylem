package hardening

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/review"
	workflowpkg "github.com/nicholls-inc/xylem/cli/internal/workflow"
)

const (
	InventoryVersion = 1
	ScoreVersion     = 1

	ClassificationDeterministic = "deterministic"
	ClassificationFuzzy         = "fuzzy"
	ClassificationMixed         = "mixed"

	DefaultLedgerPath = "docs/hardening-ledger.md"
	defaultIssueLabel = "[harden]"
	historyWindow     = 30 * 24 * time.Hour
)

var hardRulePattern = regexp.MustCompile(`(?i)\b(if|else|otherwise|must|only|exact|required|valid values)\b`)

var structuredOutputMarkers = []string{
	"json",
	"yaml",
	"schema",
	"structured output",
	"write exactly one json file",
	"write a json file",
	"must validate",
	"result:",
	"issues_created:",
	"report_status:",
	"follow_up:",
}

var patternMatchingMarkers = []string{
	"label",
	"labels",
	"git",
	"branch",
	"pull request",
	"grep",
	"regex",
	"file contents",
	"workflow yaml",
	"queue.jsonl",
	".xylem/phases",
	"summary.json",
	"gh issue",
	"status label",
}

type Inventory struct {
	Version     int                 `json:"version"`
	GeneratedAt string              `json:"generated_at"`
	RepoRoot    string              `json:"repo_root"`
	WorkflowDir string              `json:"workflow_dir"`
	Workflows   []WorkflowInventory `json:"workflows"`
}

type WorkflowInventory struct {
	Name        string           `json:"name"`
	Path        string           `json:"path"`
	Description string           `json:"description,omitempty"`
	Class       string           `json:"class,omitempty"`
	Phases      []PhaseInventory `json:"phases"`
}

type PhaseInventory struct {
	ID                   string   `json:"id"`
	DisplayName          string   `json:"display_name"`
	Workflow             string   `json:"workflow"`
	WorkflowPath         string   `json:"workflow_path"`
	Phase                string   `json:"phase"`
	Type                 string   `json:"type"`
	PromptPath           string   `json:"prompt_path,omitempty"`
	PromptExcerpt        string   `json:"prompt_excerpt,omitempty"`
	PromptLines          int      `json:"prompt_lines,omitempty"`
	Classification       string   `json:"classification"`
	StructuredSignals    []string `json:"structured_signals,omitempty"`
	PatternSignals       []string `json:"pattern_signals,omitempty"`
	DeterministicSignals []string `json:"deterministic_signals,omitempty"`
	HardRuleCount        int      `json:"hard_rule_count,omitempty"`
}

type ScoreReport struct {
	Version       int              `json:"version"`
	GeneratedAt   string           `json:"generated_at"`
	RepoRoot      string           `json:"repo_root"`
	WindowStart   string           `json:"window_start"`
	WindowEnd     string           `json:"window_end"`
	Candidates    []CandidateScore `json:"candidates"`
	TopCandidates []CandidateScore `json:"top_candidates"`
	Warnings      []string         `json:"warnings,omitempty"`
}

type CandidateScore struct {
	ID             string        `json:"id"`
	DisplayName    string        `json:"display_name"`
	Workflow       string        `json:"workflow"`
	WorkflowPath   string        `json:"workflow_path"`
	Phase          string        `json:"phase"`
	Type           string        `json:"type"`
	PromptPath     string        `json:"prompt_path,omitempty"`
	PromptExcerpt  string        `json:"prompt_excerpt,omitempty"`
	Classification string        `json:"classification"`
	Score          int           `json:"score"`
	Criteria       ScoreCriteria `json:"criteria"`
	Reasons        []string      `json:"reasons"`
}

type ScoreCriteria struct {
	StructuredOutput      bool `json:"structured_output"`
	PatternMatching       bool `json:"pattern_matching"`
	FailureCount30d       int  `json:"failure_count_30d"`
	HardRuleCount         int  `json:"hard_rule_count"`
	GoReplacementFeasible bool `json:"go_replacement_feasible"`
}

type Proposal struct {
	PhaseID             string   `json:"phase_id"`
	Workflow            string   `json:"workflow"`
	Phase               string   `json:"phase"`
	Title               string   `json:"title"`
	Body                string   `json:"body"`
	CLISignature        string   `json:"cli_signature"`
	PackageLocation     string   `json:"package_location"`
	EstimatedComplexity string   `json:"estimated_complexity"`
	TestCases           []string `json:"test_cases"`
	Score               int      `json:"score,omitempty"`
}

type FiledIssue struct {
	PhaseID string `json:"phase_id"`
	Title   string `json:"title"`
	Number  int    `json:"number,omitempty"`
	URL     string `json:"url,omitempty"`
	Created bool   `json:"created"`
}

type FileResult struct {
	Created  []FiledIssue `json:"created"`
	Existing []FiledIssue `json:"existing"`
}

type CommandRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

type issueSummary struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

func GenerateInventory(repoRoot, workflowDir string, now time.Time) (*Inventory, error) {
	absRepoRoot, err := resolveRepoRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	absWorkflowDir, err := resolveWithinRoot(absRepoRoot, workflowDir)
	if err != nil {
		return nil, fmt.Errorf("resolve workflow dir: %w", err)
	}
	matches, err := filepath.Glob(filepath.Join(absWorkflowDir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob workflow files: %w", err)
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no workflow yaml files found in %s", absWorkflowDir)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	inventory := &Inventory{
		Version:     InventoryVersion,
		GeneratedAt: now.Format(time.RFC3339),
		RepoRoot:    absRepoRoot,
		WorkflowDir: filepath.ToSlash(mustRelative(absRepoRoot, absWorkflowDir)),
		Workflows:   make([]WorkflowInventory, 0, len(matches)),
	}

	originalWD, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working dir: %w", err)
	}
	if err := os.Chdir(absRepoRoot); err != nil {
		return nil, fmt.Errorf("chdir repo root: %w", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	for _, workflowPath := range matches {
		wf, err := workflowpkg.Load(workflowPath)
		if err != nil {
			return nil, fmt.Errorf("inventory workflow %s: %w", filepath.Base(workflowPath), err)
		}
		item := WorkflowInventory{
			Name:        wf.Name,
			Path:        filepath.ToSlash(mustRelative(absRepoRoot, workflowPath)),
			Description: wf.Description,
			Class:       string(wf.Class),
			Phases:      make([]PhaseInventory, 0, len(wf.Phases)),
		}
		for _, phase := range wf.Phases {
			item.Phases = append(item.Phases, buildPhaseInventory(absRepoRoot, workflowPath, wf.Name, phase))
		}
		inventory.Workflows = append(inventory.Workflows, item)
	}

	return inventory, nil
}

func ScoreInventory(inventory *Inventory, stateDir string, now time.Time) (*ScoreReport, error) {
	if inventory == nil {
		return nil, fmt.Errorf("inventory is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	windowStart := now.Add(-historyWindow)
	absStateDir, err := resolveStateDir(inventory.RepoRoot, stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve state dir: %w", err)
	}

	runs, _, warnings, err := review.LoadRuns(absStateDir, 0)
	if err != nil {
		return nil, fmt.Errorf("load run history: %w", err)
	}
	failures := countPhaseFailures(runs, windowStart, now)

	report := &ScoreReport{
		Version:       ScoreVersion,
		GeneratedAt:   now.Format(time.RFC3339),
		RepoRoot:      inventory.RepoRoot,
		WindowStart:   windowStart.Format(time.RFC3339),
		WindowEnd:     now.Format(time.RFC3339),
		Candidates:    []CandidateScore{},
		TopCandidates: []CandidateScore{},
		Warnings:      append([]string(nil), warnings...),
	}

	for _, workflowItem := range inventory.Workflows {
		for _, phase := range workflowItem.Phases {
			if phase.Classification == ClassificationDeterministic {
				continue
			}
			candidate := scorePhase(phase, failures[phase.ID])
			report.Candidates = append(report.Candidates, candidate)
		}
	}
	sort.Slice(report.Candidates, func(i, j int) bool {
		if report.Candidates[i].Score != report.Candidates[j].Score {
			return report.Candidates[i].Score > report.Candidates[j].Score
		}
		if report.Candidates[i].Criteria.FailureCount30d != report.Candidates[j].Criteria.FailureCount30d {
			return report.Candidates[i].Criteria.FailureCount30d > report.Candidates[j].Criteria.FailureCount30d
		}
		if report.Candidates[i].Criteria.HardRuleCount != report.Candidates[j].Criteria.HardRuleCount {
			return report.Candidates[i].Criteria.HardRuleCount > report.Candidates[j].Criteria.HardRuleCount
		}
		return report.Candidates[i].ID < report.Candidates[j].ID
	})

	topCount := min(3, len(report.Candidates))
	report.TopCandidates = append(report.TopCandidates, report.Candidates[:topCount]...)
	return report, nil
}

func LoadInventory(path string) (*Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read inventory %q: %w", path, err)
	}
	var inventory Inventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		return nil, fmt.Errorf("parse inventory %q: %w", path, err)
	}
	return &inventory, nil
}

func LoadProposals(path string) ([]Proposal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read proposals %q: %w", path, err)
	}
	var proposals []Proposal
	if err := json.Unmarshal(data, &proposals); err != nil {
		return nil, fmt.Errorf("parse proposals %q: %w", path, err)
	}
	seen := make(map[string]struct{}, len(proposals))
	for i := range proposals {
		if err := validateProposal(proposals[i]); err != nil {
			return nil, fmt.Errorf("proposals[%d]: %w", i, err)
		}
		if _, ok := seen[proposals[i].Title]; ok {
			return nil, fmt.Errorf("duplicate proposal title %q", proposals[i].Title)
		}
		seen[proposals[i].Title] = struct{}{}
	}
	return proposals, nil
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

func FileIssues(ctx context.Context, runner CommandRunner, repo string, proposals []Proposal, labels []string) (*FileResult, error) {
	if runner == nil {
		return nil, fmt.Errorf("runner is required")
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, fmt.Errorf("repo is required")
	}
	if len(labels) == 0 {
		labels = []string{"enhancement", "ready-for-work"}
	}
	openIssues, err := loadOpenIssues(ctx, runner, repo, defaultIssueLabel)
	if err != nil {
		return nil, err
	}
	result := &FileResult{}
	for _, proposal := range proposals {
		if existing, ok := openIssues[proposal.Title]; ok {
			result.Existing = append(result.Existing, FiledIssue{
				PhaseID: proposal.PhaseID,
				Title:   proposal.Title,
				Number:  existing.Number,
				URL:     existing.URL,
				Created: false,
			})
			continue
		}
		args := []string{"issue", "create", "--repo", repo, "--title", proposal.Title, "--body", proposal.Body}
		for _, label := range labels {
			args = append(args, "--label", label)
		}
		out, err := runner.RunOutput(ctx, "gh", args...)
		if err != nil {
			return nil, fmt.Errorf("create issue %q: %w", proposal.Title, err)
		}
		rawURL := strings.TrimSpace(string(out))
		number, err := issueNumberFromURL(rawURL)
		if err != nil {
			return nil, fmt.Errorf("parse created issue url %q: %w", rawURL, err)
		}
		result.Created = append(result.Created, FiledIssue{
			PhaseID: proposal.PhaseID,
			Title:   proposal.Title,
			Number:  number,
			URL:     rawURL,
			Created: true,
		})
		openIssues[proposal.Title] = issueSummary{Number: number, Title: proposal.Title, URL: rawURL}
	}
	return result, nil
}

func AppendLedger(repoRoot, ledgerPath string, proposals []Proposal, filed *FileResult, now time.Time) error {
	absRepoRoot, err := resolveRepoRoot(repoRoot)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if strings.TrimSpace(ledgerPath) == "" {
		ledgerPath = DefaultLedgerPath
	}
	absLedgerPath, err := resolveWithinRoot(absRepoRoot, ledgerPath)
	if err != nil {
		return fmt.Errorf("resolve ledger path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absLedgerPath), 0o755); err != nil {
		return fmt.Errorf("create ledger dir: %w", err)
	}

	var existing strings.Builder
	if data, err := os.ReadFile(absLedgerPath); err == nil {
		existing.Write(data)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read ledger: %w", err)
	} else {
		existing.WriteString("# Hardening Ledger\n\n")
		existing.WriteString("This file records scheduled hardening-audit runs and the workflow phases they proposed to harden.\n")
	}

	if !strings.HasSuffix(existing.String(), "\n") {
		existing.WriteString("\n")
	}
	existing.WriteString("\n")
	existing.WriteString(fmt.Sprintf("## %s\n\n", now.Format(time.RFC3339)))

	if len(proposals) == 0 {
		existing.WriteString("- No fuzzy or mixed phases were promoted to issue proposals in this run.\n")
	} else {
		statusByTitle := make(map[string]FiledIssue, len(proposals))
		if filed != nil {
			for _, item := range filed.Created {
				statusByTitle[item.Title] = item
			}
			for _, item := range filed.Existing {
				statusByTitle[item.Title] = item
			}
		}
		for _, proposal := range proposals {
			status := "recorded"
			if item, ok := statusByTitle[proposal.Title]; ok {
				switch {
				case item.Created:
					status = fmt.Sprintf("opened issue #%d", item.Number)
				case item.Number > 0:
					status = fmt.Sprintf("matched existing issue #%d", item.Number)
				}
			}
			existing.WriteString(fmt.Sprintf("- `%s` — %s\n", proposal.PhaseID, status))
			existing.WriteString(fmt.Sprintf("  - Proposed CLI: `%s`\n", strings.TrimSpace(proposal.CLISignature)))
			existing.WriteString(fmt.Sprintf("  - Package: `%s`\n", strings.TrimSpace(proposal.PackageLocation)))
			existing.WriteString(fmt.Sprintf("  - Estimated complexity: `%s`\n", strings.TrimSpace(proposal.EstimatedComplexity)))
			for _, testCase := range proposal.TestCases {
				existing.WriteString(fmt.Sprintf("  - Test case: %s\n", strings.TrimSpace(testCase)))
			}
		}
	}

	if err := os.WriteFile(absLedgerPath, []byte(existing.String()), 0o644); err != nil {
		return fmt.Errorf("write ledger: %w", err)
	}
	return nil
}

func buildPhaseInventory(repoRoot, workflowPath, workflowName string, phase workflowpkg.Phase) PhaseInventory {
	phaseType := strings.TrimSpace(phase.Type)
	if phaseType == "" {
		phaseType = "prompt"
	}
	item := PhaseInventory{
		ID:           workflowName + "/" + phase.Name,
		DisplayName:  workflowName + "/" + phase.Name,
		Workflow:     workflowName,
		WorkflowPath: filepath.ToSlash(mustRelative(repoRoot, workflowPath)),
		Phase:        phase.Name,
		Type:         phaseType,
	}
	if phaseType == "command" {
		item.Classification = ClassificationDeterministic
		item.DeterministicSignals = []string{"command-phase"}
		return item
	}

	promptPath := strings.TrimSpace(phase.PromptFile)
	item.PromptPath = filepath.ToSlash(promptPath)
	content := readPrompt(repoRoot, promptPath)
	item.PromptExcerpt = promptExcerpt(content)
	item.PromptLines = lineCount(content)
	item.StructuredSignals = detectMarkers(content, structuredOutputMarkers)
	item.PatternSignals = detectMarkers(content, patternMatchingMarkers)
	item.HardRuleCount = countHardRules(content)

	switch {
	case len(item.StructuredSignals) > 0 || len(item.PatternSignals) > 0 || item.HardRuleCount >= 3:
		item.Classification = ClassificationMixed
	default:
		item.Classification = ClassificationFuzzy
	}
	return item
}

func scorePhase(phase PhaseInventory, failureCount int) CandidateScore {
	criteria := ScoreCriteria{
		StructuredOutput:      len(phase.StructuredSignals) > 0,
		PatternMatching:       len(phase.PatternSignals) > 0,
		FailureCount30d:       failureCount,
		HardRuleCount:         phase.HardRuleCount,
		GoReplacementFeasible: phase.PromptLines > 0 && phase.PromptLines <= 100 && (len(phase.StructuredSignals) > 0 || len(phase.PatternSignals) > 0 || phase.HardRuleCount >= 3),
	}

	score := 0
	reasons := make([]string, 0, 6)
	if criteria.StructuredOutput {
		score += 3
		reasons = append(reasons, "phase already emits or requests structured output that could be schema-validated")
	}
	if criteria.PatternMatching {
		score += 2
		reasons = append(reasons, "prompt mostly reads labels, git state, or file patterns that map well to deterministic CLI logic")
	}
	if criteria.FailureCount30d >= 2 {
		score += 3 + min(2, criteria.FailureCount30d-2)
		reasons = append(reasons, fmt.Sprintf("phase failed %d times in the last 30 days", criteria.FailureCount30d))
	}
	if criteria.HardRuleCount >= 3 {
		score += 2
		reasons = append(reasons, fmt.Sprintf("prompt already encodes %d explicit rules or branches", criteria.HardRuleCount))
	} else if criteria.HardRuleCount > 0 {
		score++
		reasons = append(reasons, fmt.Sprintf("prompt already encodes %d explicit rules or branches", criteria.HardRuleCount))
	}
	if criteria.GoReplacementFeasible {
		score += 2
		reasons = append(reasons, "scope looks small enough for a focused Go helper")
	}
	if phase.Classification == ClassificationMixed {
		score++
		reasons = append(reasons, "phase is mixed rather than purely fuzzy, so it is already close to deterministic")
	}

	return CandidateScore{
		ID:             phase.ID,
		DisplayName:    phase.DisplayName,
		Workflow:       phase.Workflow,
		WorkflowPath:   phase.WorkflowPath,
		Phase:          phase.Phase,
		Type:           phase.Type,
		PromptPath:     phase.PromptPath,
		PromptExcerpt:  phase.PromptExcerpt,
		Classification: phase.Classification,
		Score:          score,
		Criteria:       criteria,
		Reasons:        reasons,
	}
}

func countPhaseFailures(runs []review.LoadedRun, windowStart, windowEnd time.Time) map[string]int {
	counts := make(map[string]int)
	for _, run := range runs {
		endedAt := run.Summary.EndedAt.UTC()
		if endedAt.Before(windowStart) || endedAt.After(windowEnd) {
			continue
		}
		for _, phase := range run.Summary.Phases {
			if phase.Status != "failed" {
				continue
			}
			counts[run.Summary.Workflow+"/"+phase.Name]++
		}
	}
	return counts
}

func readPrompt(repoRoot, promptPath string) string {
	if strings.TrimSpace(promptPath) == "" {
		return ""
	}
	absPath := promptPath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(repoRoot, filepath.FromSlash(promptPath))
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	return string(data)
}

func promptExcerpt(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	trimmed := make([]string, 0, min(12, len(lines)))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		trimmed = append(trimmed, line)
		if len(trimmed) == 12 {
			break
		}
	}
	excerpt := strings.Join(trimmed, "\n")
	if len(excerpt) > 900 {
		excerpt = excerpt[:900] + "..."
	}
	return excerpt
}

func lineCount(content string) int {
	if strings.TrimSpace(content) == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

func detectMarkers(content string, markers []string) []string {
	content = strings.ToLower(content)
	var hits []string
	for _, marker := range markers {
		if strings.Contains(content, marker) {
			hits = append(hits, marker)
		}
	}
	return hits
}

func countHardRules(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if hardRulePattern.MatchString(line) {
			count++
		}
	}
	return count
}

func validateProposal(proposal Proposal) error {
	if strings.TrimSpace(proposal.PhaseID) == "" {
		return fmt.Errorf("phase_id is required")
	}
	if strings.TrimSpace(proposal.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if !strings.HasPrefix(strings.TrimSpace(proposal.Title), defaultIssueLabel+" ") {
		return fmt.Errorf("title must start with %q", defaultIssueLabel+" ")
	}
	if strings.TrimSpace(proposal.Body) == "" {
		return fmt.Errorf("body is required")
	}
	if strings.TrimSpace(proposal.CLISignature) == "" {
		return fmt.Errorf("cli_signature is required")
	}
	if strings.TrimSpace(proposal.PackageLocation) == "" {
		return fmt.Errorf("package_location is required")
	}
	if strings.TrimSpace(proposal.EstimatedComplexity) == "" {
		return fmt.Errorf("estimated_complexity is required")
	}
	if len(proposal.TestCases) == 0 {
		return fmt.Errorf("test_cases must not be empty")
	}
	return nil
}

func loadOpenIssues(ctx context.Context, runner CommandRunner, repo, search string) (map[string]issueSummary, error) {
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

func resolveRepoRoot(repoRoot string) (string, error) {
	if strings.TrimSpace(repoRoot) == "" {
		repoRoot = "."
	}
	absPath, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repo root %q: %w", repoRoot, err)
	}
	return absPath, nil
}

func resolveStateDir(repoRoot, stateDir string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		stateDir = ".xylem"
	}
	return resolveWithinRoot(repoRoot, stateDir)
}

func resolveWithinRoot(repoRoot, target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return repoRoot, nil
	}
	absTarget := target
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(repoRoot, filepath.FromSlash(target))
	}
	absTarget = filepath.Clean(absTarget)
	rel, err := filepath.Rel(repoRoot, absTarget)
	if err != nil {
		return "", fmt.Errorf("rel path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes repo root %q", target, repoRoot)
	}
	return absTarget, nil
}

func mustRelative(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
