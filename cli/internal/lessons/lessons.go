package lessons

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
	"github.com/nicholls-inc/xylem/cli/internal/review"
)

const (
	defaultLookbackWindow        = 30 * 24 * time.Hour
	defaultMinSamples            = 2
	defaultOutputDir             = "reviews"
	defaultMaxEvidence           = 5
	defaultMaxLessonsPerProposal = 3
	reportJSONName               = "lessons.json"
	reportMarkdownName           = "lessons.md"
	runPhaseName                 = "[run]"
)

var whitespace = regexp.MustCompile(`\s+`)
var noisyToken = regexp.MustCompile(`[\d/_.:-]+`)
var slugSafe = regexp.MustCompile(`[^a-z0-9]+`)

type PRClient interface {
	ListOpenPullRequests(ctx context.Context, repo string) ([]OpenPullRequest, error)
}

type OpenPullRequest struct {
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	HeadBranch string   `json:"head_branch"`
	Labels     []string `json:"labels,omitempty"`
}

type Options struct {
	Repo                  string
	HarnessPath           string
	OutputDir             string
	LookbackWindow        time.Duration
	MinSamples            int
	MaxEvidence           int
	MaxLessonsPerProposal int
	Now                   time.Time
}

type Result struct {
	Report       *Report
	JSONPath     string
	MarkdownPath string
	Markdown     string
	OutputDir    string
}

type Report struct {
	GeneratedAt        time.Time       `json:"generated_at"`
	Repo               string          `json:"repo,omitempty"`
	HarnessPath        string          `json:"harness_path"`
	LookbackHours      int             `json:"lookback_hours"`
	MinSamples         int             `json:"min_samples"`
	TotalRunsObserved  int             `json:"total_runs_observed"`
	FailedRunsReviewed int             `json:"failed_runs_reviewed"`
	Lessons            []Lesson        `json:"lessons"`
	Proposals          []Proposal      `json:"proposals"`
	Skipped            []SkippedLesson `json:"skipped,omitempty"`
	Warnings           []string        `json:"warnings,omitempty"`
}

type Lesson struct {
	Fingerprint        string        `json:"fingerprint"`
	Theme              string        `json:"theme"`
	Source             string        `json:"source"`
	Workflow           string        `json:"workflow"`
	Phase              string        `json:"phase"`
	SignalKind         string        `json:"signal_kind"`
	Signal             string        `json:"signal"`
	RecoveryClass      string        `json:"recovery_class,omitempty"`
	RecoveryAction     string        `json:"recovery_action,omitempty"`
	FollowUpRoute      string        `json:"follow_up_route,omitempty"`
	Samples            int           `json:"samples"`
	NegativeConstraint string        `json:"negative_constraint"`
	Rationale          string        `json:"rationale"`
	Example            string        `json:"example"`
	Evidence           []EvidenceRef `json:"evidence"`
	ProposedMarkdown   string        `json:"proposed_markdown"`
}

type EvidenceRef struct {
	VesselID     string `json:"vessel_id"`
	EndedAt      string `json:"ended_at"`
	ArtifactPath string `json:"artifact_path,omitempty"`
	SummaryPath  string `json:"summary_path"`
}

type Proposal struct {
	Theme              string   `json:"theme"`
	Branch             string   `json:"branch"`
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	HarnessPath        string   `json:"harness_path"`
	LessonFingerprints []string `json:"lesson_fingerprints"`
	HarnessPatch       string   `json:"harness_patch"`
	Status             string   `json:"status,omitempty"`
	PRNumber           int      `json:"pr_number,omitempty"`
	PRURL              string   `json:"pr_url,omitempty"`
}

type SkippedLesson struct {
	Fingerprint string `json:"fingerprint"`
	Reason      string `json:"reason"`
}

type observation struct {
	theme          string
	source         string
	workflow       string
	phase          string
	signalKind     string
	signal         string
	recoveryClass  string
	recoveryAction string
	followUpRoute  string
	example        string
	artifactPath   string
	vesselID       string
	endedAt        time.Time
}

type clusterKey struct {
	theme          string
	source         string
	workflow       string
	phase          string
	signalKind     string
	signal         string
	recoveryClass  string
	recoveryAction string
	followUpRoute  string
}

type cluster struct {
	observation observation
	items       []observation
}

func Generate(ctx context.Context, stateDir string, opts Options, prClient PRClient) (*Result, error) {
	if strings.TrimSpace(opts.HarnessPath) == "" {
		opts.HarnessPath = filepath.Join(".xylem", "HARNESS.md")
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		opts.OutputDir = defaultOutputDir
	}
	if opts.LookbackWindow <= 0 {
		opts.LookbackWindow = defaultLookbackWindow
	}
	if opts.MinSamples <= 0 {
		opts.MinSamples = defaultMinSamples
	}
	if opts.MaxEvidence <= 0 {
		opts.MaxEvidence = defaultMaxEvidence
	}
	if opts.MaxLessonsPerProposal <= 0 {
		opts.MaxLessonsPerProposal = defaultMaxLessonsPerProposal
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}

	runs, totalRuns, warnings, err := review.LoadRuns(stateDir, 0)
	if err != nil {
		return nil, fmt.Errorf("generate lessons: %w", err)
	}

	harnessData, err := os.ReadFile(opts.HarnessPath)
	if err != nil {
		return nil, fmt.Errorf("generate lessons: read harness: %w", err)
	}
	openPRs, err := listOpenPullRequests(ctx, prClient, opts.Repo)
	if err != nil {
		return nil, fmt.Errorf("generate lessons: list open pull requests: %w", err)
	}

	filtered := filterFailedRuns(runs, opts.Now.Add(-opts.LookbackWindow))
	clusters := buildClusters(filtered)
	lessons, skipped := synthesizeLessons(clusters, string(harnessData), openPRs, opts)
	proposals := buildProposals(lessons, opts)

	report := &Report{
		GeneratedAt:        opts.Now,
		Repo:               opts.Repo,
		HarnessPath:        opts.HarnessPath,
		LookbackHours:      int(opts.LookbackWindow / time.Hour),
		MinSamples:         opts.MinSamples,
		TotalRunsObserved:  totalRuns,
		FailedRunsReviewed: len(filtered),
		Lessons:            lessons,
		Proposals:          proposals,
		Skipped:            skipped,
		Warnings:           warnings,
	}

	outputDir := config.RuntimePath(stateDir, opts.OutputDir)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("generate lessons: create output dir: %w", err)
	}
	jsonPath := filepath.Join(outputDir, reportJSONName)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("generate lessons: marshal report: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("generate lessons: write json report: %w", err)
	}
	markdown := renderMarkdown(report)
	markdownPath := filepath.Join(outputDir, reportMarkdownName)
	if err := os.WriteFile(markdownPath, []byte(markdown), 0o644); err != nil {
		return nil, fmt.Errorf("generate lessons: write markdown report: %w", err)
	}

	return &Result{
		Report:       report,
		JSONPath:     jsonPath,
		MarkdownPath: markdownPath,
		Markdown:     markdown,
		OutputDir:    opts.OutputDir,
	}, nil
}

func WriteResult(result *Result) error {
	if result == nil || result.Report == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(result.JSONPath), 0o755); err != nil {
		return fmt.Errorf("write lessons result: create json dir: %w", err)
	}
	data, err := json.MarshalIndent(result.Report, "", "  ")
	if err != nil {
		return fmt.Errorf("write lessons result: marshal report: %w", err)
	}
	if err := os.WriteFile(result.JSONPath, data, 0o644); err != nil {
		return fmt.Errorf("write lessons result: write json report: %w", err)
	}
	result.Markdown = renderMarkdown(result.Report)
	if err := os.MkdirAll(filepath.Dir(result.MarkdownPath), 0o755); err != nil {
		return fmt.Errorf("write lessons result: create markdown dir: %w", err)
	}
	if err := os.WriteFile(result.MarkdownPath, []byte(result.Markdown), 0o644); err != nil {
		return fmt.Errorf("write lessons result: write markdown report: %w", err)
	}
	return nil
}

func filterFailedRuns(runs []review.LoadedRun, cutoff time.Time) []review.LoadedRun {
	filtered := make([]review.LoadedRun, 0, len(runs))
	for _, run := range runs {
		if run.Summary.EndedAt.Before(cutoff) {
			continue
		}
		switch run.Summary.State {
		case "failed", "timed_out":
			filtered = append(filtered, run)
		}
	}
	return filtered
}

func buildClusters(runs []review.LoadedRun) map[clusterKey]*cluster {
	clusters := make(map[clusterKey]*cluster)
	for _, run := range runs {
		for _, obs := range extractObservations(run) {
			key := clusterKey{
				theme:          obs.theme,
				source:         obs.source,
				workflow:       obs.workflow,
				phase:          obs.phase,
				signalKind:     obs.signalKind,
				signal:         obs.signal,
				recoveryClass:  obs.recoveryClass,
				recoveryAction: obs.recoveryAction,
				followUpRoute:  obs.followUpRoute,
			}
			group, ok := clusters[key]
			if !ok {
				group = &cluster{observation: obs}
				clusters[key] = group
			}
			group.items = append(group.items, obs)
		}
	}
	return clusters
}

func synthesizeLessons(groups map[clusterKey]*cluster, harness string, openPRs []OpenPullRequest, opts Options) ([]Lesson, []SkippedLesson) {
	lessons := make([]Lesson, 0, len(groups))
	skipped := make([]SkippedLesson, 0)
	for _, group := range groups {
		if len(group.items) < opts.MinSamples {
			continue
		}
		lesson := lessonFromCluster(group, opts.MaxEvidence)
		switch {
		case strings.Contains(harness, lesson.Fingerprint) || strings.Contains(harness, lesson.NegativeConstraint):
			skipped = append(skipped, SkippedLesson{Fingerprint: lesson.Fingerprint, Reason: "already present in HARNESS.md"})
		case openPRContains(openPRs, lesson):
			skipped = append(skipped, SkippedLesson{Fingerprint: lesson.Fingerprint, Reason: "equivalent open PR already exists"})
		default:
			lessons = append(lessons, lesson)
		}
	}
	sort.Slice(lessons, func(i, j int) bool {
		if lessons[i].Theme != lessons[j].Theme {
			return lessons[i].Theme < lessons[j].Theme
		}
		if lessons[i].Samples != lessons[j].Samples {
			return lessons[i].Samples > lessons[j].Samples
		}
		return lessons[i].Fingerprint < lessons[j].Fingerprint
	})
	sort.Slice(skipped, func(i, j int) bool {
		return skipped[i].Fingerprint < skipped[j].Fingerprint
	})
	return lessons, skipped
}

func lessonFromCluster(group *cluster, maxEvidence int) Lesson {
	obs := group.observation
	fingerprint := fingerprintFor(obs.workflow, obs.phase, obs.signalKind, obs.signal)
	evidenceRefs := make([]EvidenceRef, 0, min(len(group.items), maxEvidence))
	for _, item := range group.items {
		if len(evidenceRefs) >= maxEvidence {
			break
		}
		evidenceRefs = append(evidenceRefs, EvidenceRef{
			VesselID:     item.vesselID,
			EndedAt:      item.endedAt.UTC().Format(time.RFC3339),
			ArtifactPath: item.artifactPath,
			SummaryPath:  filepath.ToSlash(filepath.Join("phases", item.vesselID, "summary.json")),
		})
	}
	rule := negativeConstraint(obs)
	rationale := fmt.Sprintf("This failure pattern recurred in %d failed vessels for `%s` and should be encoded as institutional memory instead of rediscovered in later runs.", len(group.items), obs.workflow)
	example := obs.example
	if example == "" {
		example = obs.signal
	}
	markdown := renderHarnessEntry(fingerprint, rule, rationale, example, evidenceRefs)
	return Lesson{
		Fingerprint:        fingerprint,
		Theme:              obs.theme,
		Source:             obs.source,
		Workflow:           obs.workflow,
		Phase:              obs.phase,
		SignalKind:         obs.signalKind,
		Signal:             obs.signal,
		RecoveryClass:      obs.recoveryClass,
		RecoveryAction:     obs.recoveryAction,
		FollowUpRoute:      obs.followUpRoute,
		Samples:            len(group.items),
		NegativeConstraint: rule,
		Rationale:          rationale,
		Example:            example,
		Evidence:           evidenceRefs,
		ProposedMarkdown:   markdown,
	}
}

func buildProposals(lessons []Lesson, opts Options) []Proposal {
	if len(lessons) == 0 {
		return nil
	}
	grouped := make(map[string][]Lesson)
	themes := make([]string, 0)
	for _, lesson := range lessons {
		if _, ok := grouped[lesson.Theme]; !ok {
			themes = append(themes, lesson.Theme)
		}
		grouped[lesson.Theme] = append(grouped[lesson.Theme], lesson)
	}
	sort.Strings(themes)

	proposals := make([]Proposal, 0)
	for _, theme := range themes {
		items := grouped[theme]
		for start := 0; start < len(items); start += opts.MaxLessonsPerProposal {
			end := start + opts.MaxLessonsPerProposal
			if end > len(items) {
				end = len(items)
			}
			chunk := items[start:end]
			fingerprints := make([]string, 0, len(chunk))
			entries := make([]string, 0, len(chunk))
			for _, lesson := range chunk {
				fingerprints = append(fingerprints, lesson.Fingerprint)
				entries = append(entries, lesson.ProposedMarkdown)
			}
			branch := fmt.Sprintf("chore/lessons-%s-%s", slug(theme), chunk[0].Fingerprint[:8])
			title := fmt.Sprintf("[lessons] %s institutional memory updates", theme)
			body := renderProposalBody(theme, chunk)
			proposals = append(proposals, Proposal{
				Theme:              theme,
				Branch:             branch,
				Title:              title,
				Body:               body,
				HarnessPath:        opts.HarnessPath,
				LessonFingerprints: fingerprints,
				HarnessPatch:       strings.Join(entries, "\n\n"),
				Status:             "pending",
			})
		}
	}
	return proposals
}

func extractObservations(run review.LoadedRun) []observation {
	observations := make([]observation, 0)
	seen := make(map[string]bool)
	add := func(phase, kind, signal, example, artifactPath string) {
		normalized := normalizeSignal(signal)
		if normalized == "" {
			return
		}
		if phase == "" {
			phase = runPhaseName
		}
		seenKey := phase + "\x00" + normalized
		if seen[seenKey] {
			return
		}
		seen[seenKey] = true
		observations = append(observations, observation{
			theme:          themeFor(run.Summary.Workflow, phase),
			source:         run.Summary.Source,
			workflow:       run.Summary.Workflow,
			phase:          phase,
			signalKind:     kind,
			signal:         normalized,
			recoveryClass:  recoveryClass(run),
			recoveryAction: recoveryAction(run),
			followUpRoute:  recoveryFollowUpRoute(run),
			example:        truncate(example, 180),
			artifactPath:   artifactPath,
			vesselID:       run.Summary.VesselID,
			endedAt:        run.Summary.EndedAt,
		})
	}

	if run.Evidence != nil {
		for _, claim := range run.Evidence.Claims {
			if claim.Passed {
				continue
			}
			add(claim.Phase, "evidence", claim.Claim, claim.Claim, claim.ArtifactPath)
		}
	}
	for _, phase := range run.Summary.Phases {
		if phase.Status == "completed" || phase.Status == "no-op" {
			continue
		}
		signal := phase.Error
		if signal == "" && phase.GatePassed != nil && !*phase.GatePassed {
			signal = "gate failed"
		}
		if signal == "" {
			signal = phase.Status
		}
		add(phase.Name, "phase", signal, signal, filepath.ToSlash(filepath.Join("phases", run.Summary.VesselID, phase.Name+".output")))
	}
	if run.EvalReport != nil && evalHasIssues(run.EvalReport) {
		signal := firstEvalSignal(run.EvalReport)
		add(runPhaseName, "evaluation", signal, signal, filepath.ToSlash(filepath.Join("phases", run.Summary.VesselID, "quality-report.json")))
	}
	if len(observations) == 0 {
		add(runPhaseName, "run", run.Summary.State, run.Summary.State, "")
	}
	return observations
}

func evalHasIssues(result *evaluator.LoopResult) bool {
	if result == nil || result.FinalResult == nil {
		return false
	}
	return !result.FinalResult.Pass || len(result.FinalResult.Feedback) > 0 || len(result.FinalResult.Score.Issues) > 0
}

func firstEvalSignal(result *evaluator.LoopResult) string {
	if result == nil || result.FinalResult == nil {
		return "evaluation failed"
	}
	if len(result.FinalResult.Feedback) > 0 {
		return result.FinalResult.Feedback[0].Description
	}
	if len(result.FinalResult.Score.Issues) > 0 {
		return result.FinalResult.Score.Issues[0].Description
	}
	if !result.FinalResult.Pass {
		return "evaluation failed"
	}
	return "evaluation failed"
}

func negativeConstraint(obs observation) string {
	switch obs.signalKind {
	case "evidence":
		return fmt.Sprintf("Do not mark `%s` as complete while phase `%s` is still failing the evidence claim `%s`.", obs.workflow, obs.phase, obs.signal)
	case "evaluation":
		return fmt.Sprintf("Do not ship `%s` output when the evaluator still reports `%s`.", obs.workflow, obs.signal)
	case "phase":
		return fmt.Sprintf("Do not finish `%s` work with phase `%s` still failing due to `%s`.", obs.workflow, obs.phase, obs.signal)
	default:
		return fmt.Sprintf("Do not rerun `%s` without addressing the recurring failure `%s` first.", obs.workflow, obs.signal)
	}
}

func renderHarnessEntry(fingerprint, rule, rationale, example string, refs []EvidenceRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s <!-- xylem-lesson:%s -->\n", rule, fingerprint)
	fmt.Fprintf(&b, "- Rationale: %s\n", rationale)
	fmt.Fprintf(&b, "- Example symptom: %s\n", example)
	if len(refs) > 0 {
		b.WriteString("- Evidence:\n")
		for _, ref := range refs {
			if ref.ArtifactPath != "" {
				fmt.Fprintf(&b, "  - `%s` (%s) — `%s`\n", ref.VesselID, ref.EndedAt, ref.ArtifactPath)
				continue
			}
			fmt.Fprintf(&b, "  - `%s` (%s) — `%s`\n", ref.VesselID, ref.EndedAt, ref.SummaryPath)
		}
	}
	return b.String()
}

func renderProposalBody(theme string, lessons []Lesson) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Institutional memory updates for %s\n\n", theme)
	b.WriteString("This PR proposes evidence-backed `Do Not` guidance derived from recurring failed vessels.\n\n")
	for _, lesson := range lessons {
		fmt.Fprintf(&b, "- `%s` (%d samples", lesson.Fingerprint, lesson.Samples)
		if lesson.RecoveryClass != "" || lesson.RecoveryAction != "" {
			fmt.Fprintf(&b, ", class=%s, action=%s", lesson.RecoveryClass, lesson.RecoveryAction)
		}
		b.WriteString(")\n")
	}
	b.WriteString("\n### Proposed HARNESS.md additions\n\n")
	for _, lesson := range lessons {
		b.WriteString(lesson.ProposedMarkdown)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func renderMarkdown(report *Report) string {
	var b strings.Builder
	b.WriteString("# Lessons synthesis\n\n")
	fmt.Fprintf(&b, "- Generated: %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Failed runs reviewed: %d of %d\n", report.FailedRunsReviewed, report.TotalRunsObserved)
	fmt.Fprintf(&b, "- Proposed lessons: %d\n", len(report.Lessons))
	fmt.Fprintf(&b, "- Proposed PR slices: %d\n\n", len(report.Proposals))
	if len(report.Warnings) > 0 {
		b.WriteString("## Warnings\n\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
		b.WriteString("\n")
	}
	if len(report.Lessons) == 0 {
		b.WriteString("No recurring failed-run patterns met the synthesis threshold.\n")
		return b.String()
	}
	b.WriteString("## Lessons\n\n")
	for _, lesson := range report.Lessons {
		fmt.Fprintf(&b, "- `%s` — %s", lesson.Fingerprint, lesson.NegativeConstraint)
		if lesson.RecoveryClass != "" || lesson.RecoveryAction != "" {
			fmt.Fprintf(&b, " _(class=%s, action=%s)_", lesson.RecoveryClass, lesson.RecoveryAction)
		}
		if lesson.FollowUpRoute != "" {
			fmt.Fprintf(&b, " _(route=%s)_", lesson.FollowUpRoute)
		}
		b.WriteString("\n")
	}
	if len(report.Proposals) > 0 {
		b.WriteString("\n## Proposal slices\n\n")
		for _, proposal := range report.Proposals {
			if proposal.PRURL != "" {
				fmt.Fprintf(&b, "- `%s` → `%s` (%s)\n", proposal.Title, proposal.Branch, proposal.PRURL)
				continue
			}
			if proposal.Status != "" {
				fmt.Fprintf(&b, "- `%s` → `%s` (%s)\n", proposal.Title, proposal.Branch, proposal.Status)
				continue
			}
			fmt.Fprintf(&b, "- `%s` → `%s`\n", proposal.Title, proposal.Branch)
		}
	}
	if len(report.Skipped) > 0 {
		b.WriteString("\n## Skipped\n\n")
		for _, skipped := range report.Skipped {
			fmt.Fprintf(&b, "- `%s` — %s\n", skipped.Fingerprint, skipped.Reason)
		}
	}
	return b.String()
}

func normalizeSignal(signal string) string {
	signal = strings.ToLower(strings.TrimSpace(signal))
	signal = noisyToken.ReplaceAllString(signal, " ")
	signal = whitespace.ReplaceAllString(signal, " ")
	signal = strings.Trim(signal, " -.,:;")
	return truncate(signal, 96)
}

func themeFor(workflow, phase string) string {
	if phase == "" || phase == runPhaseName {
		return workflow
	}
	return workflow + "-" + phase
}

func fingerprintFor(parts ...string) string {
	joined := strings.Join(parts, "\x00")
	sum := fmt.Sprintf("%x", hash(joined))
	return "lesson-" + sum[:12]
}

func hash(s string) [32]byte {
	return sha256Sum([]byte(s))
}

func openPRContains(prs []OpenPullRequest, lesson Lesson) bool {
	for _, pr := range prs {
		body := pr.Title + "\n" + pr.Body + "\n" + pr.HeadBranch
		if strings.Contains(body, lesson.Fingerprint) || strings.Contains(body, lesson.NegativeConstraint) {
			return true
		}
	}
	return false
}

func listOpenPullRequests(ctx context.Context, client PRClient, repo string) ([]OpenPullRequest, error) {
	if client == nil || strings.TrimSpace(repo) == "" {
		return nil, nil
	}
	return client.ListOpenPullRequests(ctx, repo)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max])
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugSafe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "lessons"
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}

func recoveryClass(run review.LoadedRun) string {
	if run.Recovery == nil {
		return ""
	}
	return string(run.Recovery.RecoveryClass)
}

func recoveryAction(run review.LoadedRun) string {
	if run.Recovery == nil {
		return ""
	}
	return string(run.Recovery.RecoveryAction)
}

func recoveryFollowUpRoute(run review.LoadedRun) string {
	if run.Recovery == nil {
		return ""
	}
	return run.Recovery.FollowUpRoute
}
