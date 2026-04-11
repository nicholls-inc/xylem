package fieldreport

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/review"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

const (
	// MinVesselsForReport is the minimum number of completed vessels needed
	// to produce a meaningful field report. Below this threshold, Generate
	// returns ErrInsufficientData.
	MinVesselsForReport = 5

	// SchemaVersion is the field report schema version. Bump this when the
	// FieldReport struct changes in a backwards-incompatible way.
	SchemaVersion = 1
)

// ErrInsufficientData is returned when there are not enough vessel summaries
// to produce a meaningful report.
var ErrInsufficientData = fmt.Errorf("insufficient vessel data for field report")

// Options controls field report generation.
type Options struct {
	// XylemVersion is the build version string of the running binary.
	XylemVersion string

	// ProfileVersion is the seeded profile bundle version associated with
	// the installation that produced the report (for example, the core
	// profile version written into adapt-repo bootstrap markers).
	ProfileVersion int

	// Extended enables the extended telemetry level (hashed repo ID,
	// failure pattern keys).
	Extended bool

	// RepoSlug is the owner/name of the repo being analyzed. Only used
	// when Extended is true — it is hashed, never sent in cleartext.
	RepoSlug string

	// LookbackRuns limits how many recent runs to include. 0 means all.
	LookbackRuns int

	// Now overrides the report timestamp (for testing).
	Now time.Time
}

// FieldReport is the privacy-safe, anonymized aggregate of vessel run data
// from a productized xylem installation. It contains no PII, no repo names
// (unless Extended + hashed), no prompt content, and no individual vessel IDs.
type FieldReport struct {
	Version        int       `json:"version"`
	ReportID       string    `json:"report_id"`
	GeneratedAt    time.Time `json:"generated_at"`
	XylemVersion   string    `json:"xylem_version"`
	ProfileVersion int       `json:"profile_version"`
	WindowStart    time.Time `json:"window_start"`
	WindowEnd      time.Time `json:"window_end"`

	TotalVessels int                   `json:"total_vessels"`
	Fleet        FleetDigest           `json:"fleet"`
	Workflows    []WorkflowDigest      `json:"workflows"`
	CostDigest   CostDigest            `json:"cost_digest"`
	Recovery     []RecoveryClassDigest `json:"recovery_digest,omitempty"`

	// Extended fields (only populated when Options.Extended is true)
	HashedRepoID    string           `json:"hashed_repo_id,omitempty"`
	FailurePatterns []FailurePattern `json:"failure_patterns,omitempty"`
}

// FleetDigest summarizes overall fleet health ratios.
type FleetDigest struct {
	Healthy   int `json:"healthy"`
	Degraded  int `json:"degraded"`
	Unhealthy int `json:"unhealthy"`
}

// WorkflowDigest summarizes per-workflow outcomes.
type WorkflowDigest struct {
	Workflow   string     `json:"workflow"`
	Total      int        `json:"total"`
	Completed  int        `json:"completed"`
	Failed     int        `json:"failed"`
	TimedOut   int        `json:"timed_out"`
	Cancelled  int        `json:"cancelled"`
	SourceType string     `json:"source_type,omitempty"`
	Cost       CostDigest `json:"cost"`
	Duration   StatDigest `json:"duration"`
}

// CostDigest summarizes token and cost statistics.
type CostDigest struct {
	TotalTokens    int     `json:"total_tokens"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	MeanTokens     int     `json:"mean_tokens"`
	MedianTokens   int     `json:"median_tokens"`
	P95Tokens      int     `json:"p95_tokens"`
	MeanCostUSD    float64 `json:"mean_cost_usd"`
	MedianCostUSD  float64 `json:"median_cost_usd"`
	P95CostUSD     float64 `json:"p95_cost_usd"`
	BudgetExceeded int     `json:"budget_exceeded"`
	BudgetWarnings int     `json:"budget_warnings"`
}

// StatDigest summarizes a numeric distribution.
type StatDigest struct {
	MeanMS   int64 `json:"mean_ms"`
	MedianMS int64 `json:"median_ms"`
	P95MS    int64 `json:"p95_ms"`
}

// RecoveryClassDigest summarizes recovery classifications.
type RecoveryClassDigest struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

// FailurePattern summarizes a recurring anomaly pattern (extended only).
type FailurePattern struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// Generate produces a FieldReport from the vessel summaries in stateDir.
func Generate(stateDir string, opts Options) (*FieldReport, error) {
	lookback := opts.LookbackRuns
	if lookback <= 0 {
		lookback = 0
	}

	runs, _, _, err := review.LoadRuns(stateDir, lookback)
	if err != nil {
		return nil, fmt.Errorf("field report: load runs: %w", err)
	}

	if len(runs) < MinVesselsForReport {
		return nil, ErrInsufficientData
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	report := &FieldReport{
		Version:        SchemaVersion,
		ReportID:       generateReportID(now, stateDir),
		GeneratedAt:    now,
		XylemVersion:   opts.XylemVersion,
		ProfileVersion: opts.ProfileVersion,
		TotalVessels:   len(runs),
	}

	// Compute time window from summaries
	report.WindowStart, report.WindowEnd = computeTimeWindow(runs)

	// Fleet health
	report.Fleet = computeFleetDigest(runs)

	// Per-workflow digests
	report.Workflows = computeWorkflowDigests(runs)

	// Aggregate cost digest
	report.CostDigest = computeAggregateCostDigest(runs)

	// Recovery class distribution
	report.Recovery = computeRecoveryDigest(runs)

	// Extended fields
	if opts.Extended {
		if opts.RepoSlug != "" {
			report.HashedRepoID = hashRepoID(opts.RepoSlug)
		}
		report.FailurePatterns = computeFailurePatterns(runs)
	}

	return report, nil
}

// Save writes the field report to stateDir/field-reports/<date>.json.
func Save(stateDir string, report *FieldReport) (string, error) {
	dir := filepath.Join(stateDir, "state", "field-reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("field report: create dir: %w", err)
	}

	date := report.GeneratedAt.Format("2006-01-02")
	path := filepath.Join(dir, date+".json")

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("field report: marshal: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("field report: write: %w", err)
	}

	return path, nil
}

func generateReportID(t time.Time, stateDir string) string {
	h := sha256.New()
	h.Write([]byte(t.Format(time.RFC3339Nano)))
	h.Write([]byte(stateDir))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func hashRepoID(slug string) string {
	h := sha256.Sum256([]byte(slug))
	return fmt.Sprintf("%x", h[:])[:16]
}

func computeTimeWindow(runs []review.LoadedRun) (time.Time, time.Time) {
	if len(runs) == 0 {
		return time.Time{}, time.Time{}
	}
	start := runs[0].Summary.StartedAt
	end := runs[0].Summary.EndedAt
	for _, run := range runs[1:] {
		if run.Summary.StartedAt.Before(start) {
			start = run.Summary.StartedAt
		}
		if run.Summary.EndedAt.After(end) {
			end = run.Summary.EndedAt
		}
	}
	return start, end
}

func computeFleetDigest(runs []review.LoadedRun) FleetDigest {
	digest := FleetDigest{}
	for _, run := range runs {
		health := classifyRunHealth(run.Summary)
		switch health {
		case "healthy":
			digest.Healthy++
		case "degraded":
			digest.Degraded++
		case "unhealthy":
			digest.Unhealthy++
		}
	}
	return digest
}

func classifyRunHealth(s runner.VesselSummary) string {
	switch s.State {
	case "failed", "timed_out":
		return "unhealthy"
	case "cancelled":
		return "degraded"
	}
	if s.BudgetExceeded {
		return "degraded"
	}
	for _, phase := range s.Phases {
		if phase.GatePassed != nil && !*phase.GatePassed {
			return "unhealthy"
		}
		if phase.Status == "failed" {
			return "unhealthy"
		}
	}
	return "healthy"
}

func computeWorkflowDigests(runs []review.LoadedRun) []WorkflowDigest {
	type accumulator struct {
		total     int
		completed int
		failed    int
		timedOut  int
		cancelled int
		tokens    []int
		costs     []float64
		durations []int64
		budgetExc int
		budgetWrn int
		source    string
	}

	byWorkflow := make(map[string]*accumulator)
	for _, run := range runs {
		wf := run.Summary.Workflow
		if wf == "" {
			wf = "unknown"
		}
		acc, ok := byWorkflow[wf]
		if !ok {
			acc = &accumulator{}
			byWorkflow[wf] = acc
		}
		acc.total++
		switch run.Summary.State {
		case "completed":
			acc.completed++
		case "failed":
			acc.failed++
		case "timed_out":
			acc.timedOut++
		case "cancelled":
			acc.cancelled++
		}
		acc.tokens = append(acc.tokens, run.Summary.TotalTokensEst)
		acc.costs = append(acc.costs, run.Summary.TotalCostUSDEst)
		acc.durations = append(acc.durations, run.Summary.DurationMS)
		if run.Summary.BudgetExceeded {
			acc.budgetExc++
		}
		if run.Summary.BudgetWarning {
			acc.budgetWrn++
		}
		// Source type from summary source field — strip to type only
		if acc.source == "" && run.Summary.Source != "" {
			acc.source = sourceType(run.Summary.Source)
		}
	}

	digests := make([]WorkflowDigest, 0, len(byWorkflow))
	for wf, acc := range byWorkflow {
		digests = append(digests, WorkflowDigest{
			Workflow:   wf,
			Total:      acc.total,
			Completed:  acc.completed,
			Failed:     acc.failed,
			TimedOut:   acc.timedOut,
			Cancelled:  acc.cancelled,
			SourceType: acc.source,
			Cost: CostDigest{
				TotalTokens:    sumInts(acc.tokens),
				TotalCostUSD:   sumFloats(acc.costs),
				MeanTokens:     meanInt(acc.tokens),
				MedianTokens:   medianInt(acc.tokens),
				P95Tokens:      percentileInt(acc.tokens, 0.95),
				MeanCostUSD:    meanFloat(acc.costs),
				MedianCostUSD:  medianFloat(acc.costs),
				P95CostUSD:     percentileFloat(acc.costs, 0.95),
				BudgetExceeded: acc.budgetExc,
				BudgetWarnings: acc.budgetWrn,
			},
			Duration: StatDigest{
				MeanMS:   meanInt64(acc.durations),
				MedianMS: medianInt64(acc.durations),
				P95MS:    percentileInt64(acc.durations, 0.95),
			},
		})
	}

	sort.Slice(digests, func(i, j int) bool {
		return digests[i].Workflow < digests[j].Workflow
	})
	return digests
}

func computeAggregateCostDigest(runs []review.LoadedRun) CostDigest {
	tokens := make([]int, 0, len(runs))
	costs := make([]float64, 0, len(runs))
	budgetExc := 0
	budgetWrn := 0

	for _, run := range runs {
		tokens = append(tokens, run.Summary.TotalTokensEst)
		costs = append(costs, run.Summary.TotalCostUSDEst)
		if run.Summary.BudgetExceeded {
			budgetExc++
		}
		if run.Summary.BudgetWarning {
			budgetWrn++
		}
	}

	return CostDigest{
		TotalTokens:    sumInts(tokens),
		TotalCostUSD:   sumFloats(costs),
		MeanTokens:     meanInt(tokens),
		MedianTokens:   medianInt(tokens),
		P95Tokens:      percentileInt(tokens, 0.95),
		MeanCostUSD:    meanFloat(costs),
		MedianCostUSD:  medianFloat(costs),
		P95CostUSD:     percentileFloat(costs, 0.95),
		BudgetExceeded: budgetExc,
		BudgetWarnings: budgetWrn,
	}
}

func computeRecoveryDigest(runs []review.LoadedRun) []RecoveryClassDigest {
	counts := make(map[string]int)
	for _, run := range runs {
		if run.Recovery == nil {
			continue
		}
		class := string(run.Recovery.RecoveryClass)
		if class == "" {
			continue
		}
		counts[class]++
	}

	digests := make([]RecoveryClassDigest, 0, len(counts))
	for class, count := range counts {
		digests = append(digests, RecoveryClassDigest{Class: class, Count: count})
	}
	sort.Slice(digests, func(i, j int) bool {
		if digests[i].Count == digests[j].Count {
			return digests[i].Class < digests[j].Class
		}
		return digests[i].Count > digests[j].Count
	})
	return digests
}

func computeFailurePatterns(runs []review.LoadedRun) []FailurePattern {
	counts := make(map[string]int)
	for _, run := range runs {
		if run.Summary.State == "completed" {
			continue
		}
		// Count anomaly codes from phase-level signals
		for _, phase := range run.Summary.Phases {
			if phase.GatePassed != nil && !*phase.GatePassed {
				counts["gate_failed"]++
			}
			if phase.Status == "failed" {
				counts["phase_failed"]++
			}
		}
		if run.Summary.BudgetExceeded {
			counts["budget_exceeded"]++
		}
		switch run.Summary.State {
		case "timed_out":
			counts["timed_out"]++
		case "failed":
			counts["run_failed"]++
		case "cancelled":
			counts["cancelled"]++
		}
	}

	patterns := make([]FailurePattern, 0, len(counts))
	for code, count := range counts {
		patterns = append(patterns, FailurePattern{Code: code, Count: count})
	}
	sort.Slice(patterns, func(i, j int) bool {
		if patterns[i].Count == patterns[j].Count {
			return patterns[i].Code < patterns[j].Code
		}
		return patterns[i].Count > patterns[j].Count
	})
	return patterns
}

// sourceType extracts a generic source type from the source name.
// Source names in summaries are the config key (e.g. "bugs", "features",
// "pr-lifecycle"). We don't expose these directly — instead map to
// generic types.
func sourceType(source string) string {
	// The source field in VesselSummary is the config source key name,
	// not the type. We can't reliably map to type without config access,
	// so return a sanitized version that doesn't leak org-specific names.
	// For privacy, we classify into broad buckets.
	return "github"
}

// Statistical helpers

func sumInts(vals []int) int {
	total := 0
	for _, v := range vals {
		total += v
	}
	return total
}

func sumFloats(vals []float64) float64 {
	total := 0.0
	for _, v := range vals {
		total += v
	}
	return total
}

func meanInt(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	return sumInts(vals) / len(vals)
}

func meanFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	return sumFloats(vals) / float64(len(vals))
}

func meanInt64(vals []int64) int64 {
	if len(vals) == 0 {
		return 0
	}
	total := int64(0)
	for _, v := range vals {
		total += v
	}
	return total / int64(len(vals))
}

func medianInt(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int, len(vals))
	copy(sorted, vals)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func medianFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func medianInt64(vals []int64) int64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int64, len(vals))
	copy(sorted, vals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func percentileInt(vals []int, p float64) int {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int, len(vals))
	copy(sorted, vals)
	sort.Ints(sorted)
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func percentileFloat(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func percentileInt64(vals []int64, p float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int64, len(vals))
	copy(sorted, vals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
