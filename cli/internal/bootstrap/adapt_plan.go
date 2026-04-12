package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "embed"
)

//go:embed schemas/adapt-plan.schema.json
var AdaptPlanSchema []byte

// AdaptPlan is the schema_version=1 structure written by the adapt-repo plan
// phase to .xylem/state/bootstrap/adapt-plan.json and consumed by
// xylem config validate --proposed and xylem workflow validate --proposed.
type AdaptPlan struct {
	SchemaVersion  int                `json:"schema_version"`
	Detected       AdaptPlanDetected  `json:"detected"`
	PlannedChanges []AdaptPlanChange  `json:"planned_changes"`
	Skipped        []AdaptPlanSkipped `json:"skipped"`
}

// AdaptPlanDetected holds the repository characteristics detected during analysis.
type AdaptPlanDetected struct {
	Languages   []string `json:"languages"`
	BuildTools  []string `json:"build_tools"`
	TestRunners []string `json:"test_runners"`
	Linters     []string `json:"linters"`
	HasFrontend bool     `json:"has_frontend"`
	HasDatabase bool     `json:"has_database"`
	EntryPoints []string `json:"entry_points"`
}

// AdaptPlanChange describes a single file operation in the plan.
type AdaptPlanChange struct {
	Path        string `json:"path"`
	Op          string `json:"op"`
	Rationale   string `json:"rationale"`
	DiffSummary string `json:"diff_summary,omitempty"`
}

// AdaptPlanSkipped describes a file that was considered but not planned for change.
type AdaptPlanSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// rawAdaptPlan mirrors AdaptPlan with pointer fields so the reader can
// distinguish missing keys from zero values.
type rawAdaptPlan struct {
	SchemaVersion  *int                `json:"schema_version"`
	Detected       *AdaptPlanDetected  `json:"detected"`
	PlannedChanges *[]AdaptPlanChange  `json:"planned_changes"`
	Skipped        *[]AdaptPlanSkipped `json:"skipped"`
}

// ReadAdaptPlan reads and validates an adapt-plan.json from path.
// It rejects unknown fields, missing required fields, and trailing JSON data.
func ReadAdaptPlan(path string) (*AdaptPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read adapt plan %q: %w", path, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var raw rawAdaptPlan
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode adapt plan %q: %w", path, err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode adapt plan %q: unexpected trailing data", path)
		}
		return nil, fmt.Errorf("decode adapt plan %q: %w", path, err)
	}

	plan, err := normalizeRawAdaptPlan(raw)
	if err != nil {
		return nil, fmt.Errorf("validate adapt plan %q: %w", path, err)
	}
	return &plan, nil
}

// WriteAdaptPlan serialises plan to path, creating parent directories as needed.
func WriteAdaptPlan(path string, plan *AdaptPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dirs for %q: %w", path, err)
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal adapt plan: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write adapt plan %q: %w", path, err)
	}
	return nil
}

// Validate checks the plan for semantic correctness:
//   - schema_version must equal 1
//   - each planned change: path relative and in allowlist, valid op, non-empty rationale
//   - delete op only allowed for workflow YAML files under .xylem/workflows/
//   - each skipped entry: path relative and in allowlist, non-empty reason
func (p *AdaptPlan) Validate() error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf(`"schema_version" must equal 1`)
	}
	for i, change := range p.PlannedChanges {
		path, err := normalizeAdaptPlanPath(change.Path)
		if err != nil {
			return fmt.Errorf(`"planned_changes[%d].path" %w`, i, err)
		}
		if err := validateAdaptPlanChange(path, change); err != nil {
			return fmt.Errorf(`"planned_changes[%d]" %w`, i, err)
		}
	}
	for i, skipped := range p.Skipped {
		path, err := normalizeAdaptPlanPath(skipped.Path)
		if err != nil {
			return fmt.Errorf(`"skipped[%d].path" %w`, i, err)
		}
		if strings.TrimSpace(skipped.Reason) == "" {
			return fmt.Errorf(`"skipped[%d].reason" must not be empty`, i)
		}
		if !IsAllowedAdaptPlanPath(path) {
			return fmt.Errorf(`"skipped[%d].path" %q is outside the adapt-plan allowlist`, i, path)
		}
	}
	return nil
}

// IsAllowedAdaptPlanPath reports whether path is within the adapt-plan surface allowlist.
func IsAllowedAdaptPlanPath(path string) bool {
	return path == ".xylem.yml" ||
		path == "AGENTS.md" ||
		strings.HasPrefix(path, ".xylem/") ||
		strings.HasPrefix(path, "docs/")
}

// normalizeRawAdaptPlan converts a raw decoded plan into a validated AdaptPlan.
func normalizeRawAdaptPlan(raw rawAdaptPlan) (AdaptPlan, error) {
	switch {
	case raw.SchemaVersion == nil:
		return AdaptPlan{}, fmt.Errorf(`"schema_version" is required`)
	case *raw.SchemaVersion != 1:
		return AdaptPlan{}, fmt.Errorf(`"schema_version" must equal 1`)
	case raw.Detected == nil:
		return AdaptPlan{}, fmt.Errorf(`"detected" is required`)
	case raw.PlannedChanges == nil:
		return AdaptPlan{}, fmt.Errorf(`"planned_changes" is required`)
	case raw.Skipped == nil:
		return AdaptPlan{}, fmt.Errorf(`"skipped" is required`)
	}

	planned := append([]AdaptPlanChange{}, (*raw.PlannedChanges)...)
	for i := range planned {
		path, err := normalizeAdaptPlanPath(planned[i].Path)
		if err != nil {
			return AdaptPlan{}, fmt.Errorf(`"planned_changes[%d].path" %w`, i, err)
		}
		if err := validateAdaptPlanChange(path, planned[i]); err != nil {
			return AdaptPlan{}, fmt.Errorf(`"planned_changes[%d]" %w`, i, err)
		}
		planned[i].Path = path
	}

	skipped := append([]AdaptPlanSkipped{}, (*raw.Skipped)...)
	for i := range skipped {
		path, err := normalizeAdaptPlanPath(skipped[i].Path)
		if err != nil {
			return AdaptPlan{}, fmt.Errorf(`"skipped[%d].path" %w`, i, err)
		}
		if strings.TrimSpace(skipped[i].Reason) == "" {
			return AdaptPlan{}, fmt.Errorf(`"skipped[%d].reason" must not be empty`, i)
		}
		if !IsAllowedAdaptPlanPath(path) {
			return AdaptPlan{}, fmt.Errorf(`"skipped[%d].path" %q is outside the adapt-plan allowlist`, i, path)
		}
		skipped[i].Path = path
	}

	return AdaptPlan{
		SchemaVersion:  *raw.SchemaVersion,
		Detected:       *raw.Detected,
		PlannedChanges: planned,
		Skipped:        skipped,
	}, nil
}

// normalizeAdaptPlanPath cleans path and validates it is relative and within the root.
func normalizeAdaptPlanPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("must be relative")
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("must stay within the repository root")
	}
	return cleaned, nil
}

// validateAdaptPlanChange validates a single planned change after path normalisation.
func validateAdaptPlanChange(path string, change AdaptPlanChange) error {
	switch change.Op {
	case "patch", "replace", "create", "delete":
	default:
		return fmt.Errorf(`has invalid op %q`, change.Op)
	}
	if !IsAllowedAdaptPlanPath(path) {
		return fmt.Errorf(`path %q is outside the adapt-plan allowlist`, path)
	}
	if strings.TrimSpace(change.Rationale) == "" {
		return fmt.Errorf(`must include a non-empty rationale`)
	}
	if change.Op == "delete" {
		if !strings.HasPrefix(path, ".xylem/workflows/") {
			return fmt.Errorf(`delete is only allowed for workflow YAML files under ".xylem/workflows/"`)
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return fmt.Errorf(`delete is only allowed for workflow YAML files under ".xylem/workflows/"`)
		}
	}
	return nil
}
