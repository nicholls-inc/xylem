package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/evaluator"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

type LoadedRun struct {
	Summary      runner.VesselSummary
	Evidence     *evidence.Manifest
	CostReport   *cost.CostReport
	BudgetAlerts []cost.BudgetAlert
	EvalReport   *evaluator.LoopResult
}

func LoadRuns(stateDir string, lookbackRuns int) ([]LoadedRun, int, []string, error) {
	phaseDir := filepath.Join(stateDir, "phases")
	entries, err := os.ReadDir(phaseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil, nil
		}
		return nil, 0, nil, fmt.Errorf("load runs: read phase dir: %w", err)
	}

	type summaryRecord struct {
		dirName string
		summary runner.VesselSummary
	}

	summaries := make([]summaryRecord, 0, len(entries))
	warnings := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		summary, err := loadSummaryFile(filepath.Join(phaseDir, entry.Name(), "summary.json"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("%s: %v", entry.Name(), err))
			continue
		}
		summaries = append(summaries, summaryRecord{dirName: entry.Name(), summary: *summary})
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].summary.EndedAt.After(summaries[j].summary.EndedAt)
	})

	total := len(summaries)
	if lookbackRuns > 0 && len(summaries) > lookbackRuns {
		summaries = summaries[:lookbackRuns]
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].summary.EndedAt.Before(summaries[j].summary.EndedAt)
	})

	runs := make([]LoadedRun, 0, len(summaries))
	for _, record := range summaries {
		run := LoadedRun{Summary: record.summary}

		if manifestPath := resolveArtifactPath(stateDir, record.summary.EvidenceManifestPath, reviewArtifactValue(record.summary.ReviewArtifacts, func(a *runner.ReviewArtifacts) string {
			return a.EvidenceManifest
		})); manifestPath != "" {
			manifest, warning := loadOptionalManifest(stateDir, record.summary.VesselID, manifestPath)
			run.Evidence = manifest
			if warning != "" {
				warnings = append(warnings, warning)
			}
		}

		if costPath := resolveArtifactPath(stateDir, record.summary.CostReportPath, reviewArtifactValue(record.summary.ReviewArtifacts, func(a *runner.ReviewArtifacts) string {
			return a.CostReport
		})); costPath != "" {
			report, warning := loadOptionalCostReport(costPath)
			run.CostReport = report
			if warning != "" {
				warnings = append(warnings, warning)
			}
		}

		if alertsPath := resolveArtifactPath(stateDir, record.summary.BudgetAlertsPath, reviewArtifactValue(record.summary.ReviewArtifacts, func(a *runner.ReviewArtifacts) string {
			return a.BudgetAlerts
		})); alertsPath != "" {
			alerts, warning := loadOptionalBudgetAlerts(alertsPath)
			run.BudgetAlerts = alerts
			if warning != "" {
				warnings = append(warnings, warning)
			}
		}

		if evalPath := resolveArtifactPath(stateDir, record.summary.EvalReportPath, reviewArtifactValue(record.summary.ReviewArtifacts, func(a *runner.ReviewArtifacts) string {
			return a.EvalReport
		})); evalPath != "" {
			report, warning := loadOptionalEvalReport(evalPath)
			run.EvalReport = report
			if warning != "" {
				warnings = append(warnings, warning)
			}
		}

		runs = append(runs, run)
	}

	return runs, total, warnings, nil
}

func CountAvailableRuns(stateDir string) (int, error) {
	runs, total, _, err := LoadRuns(stateDir, 0)
	if err != nil {
		return 0, err
	}
	if runs == nil {
		return total, nil
	}
	return total, nil
}

func loadSummaryFile(path string) (*runner.VesselSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read summary: %w", err)
	}
	var summary runner.VesselSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, fmt.Errorf("unmarshal summary: %w", err)
	}
	return &summary, nil
}

func resolveArtifactPath(stateDir, legacyPath, reviewPath string) string {
	rel := strings.TrimSpace(reviewPath)
	if rel == "" {
		rel = strings.TrimSpace(legacyPath)
	}
	if rel == "" {
		return ""
	}
	return filepath.Join(stateDir, filepath.FromSlash(rel))
}

func reviewArtifactValue(artifacts *runner.ReviewArtifacts, selectPath func(*runner.ReviewArtifacts) string) string {
	if artifacts == nil {
		return ""
	}
	return selectPath(artifacts)
}

func loadOptionalManifest(stateDir, vesselID, path string) (*evidence.Manifest, string) {
	manifest, err := evidence.LoadManifest(stateDir, vesselID)
	if err == nil {
		return manifest, ""
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, ""
	}
	return nil, fmt.Sprintf("%s: %v", filepath.Base(path), err)
}

func loadOptionalCostReport(path string) (*cost.CostReport, string) {
	report, err := cost.LoadReport(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return report, ""
	}
	return nil, fmt.Sprintf("%s: %v", filepath.Base(path), err)
}

func loadOptionalBudgetAlerts(path string) ([]cost.BudgetAlert, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ""
		}
		return nil, fmt.Sprintf("%s: %v", filepath.Base(path), err)
	}
	var alerts []cost.BudgetAlert
	if err := json.Unmarshal(data, &alerts); err != nil {
		return nil, fmt.Sprintf("%s: unmarshal budget alerts: %v", filepath.Base(path), err)
	}
	return alerts, ""
}

func loadOptionalEvalReport(path string) (*evaluator.LoopResult, string) {
	report, err := evaluator.LoadReport(filepath.Dir(path))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return report, ""
	}
	return nil, fmt.Sprintf("%s: %v", filepath.Base(path), err)
}
