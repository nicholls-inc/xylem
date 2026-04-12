package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	workflowpkg "github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/spf13/cobra"
)

type adaptPlan struct {
	SchemaVersion  int                `json:"schema_version"`
	Detected       adaptPlanDetected  `json:"detected"`
	PlannedChanges []adaptPlanChange  `json:"planned_changes"`
	Skipped        []adaptPlanSkipped `json:"skipped"`
}

type adaptPlanDetected struct {
	Languages   []string `json:"languages"`
	BuildTools  []string `json:"build_tools"`
	TestRunners []string `json:"test_runners"`
	Linters     []string `json:"linters"`
	HasFrontend bool     `json:"has_frontend"`
	HasDatabase bool     `json:"has_database"`
	EntryPoints []string `json:"entry_points"`
}

type adaptPlanChange struct {
	Path        string `json:"path"`
	Op          string `json:"op"`
	Rationale   string `json:"rationale"`
	DiffSummary string `json:"diff_summary,omitempty"`
}

type adaptPlanSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type rawAdaptPlan struct {
	SchemaVersion  *int                `json:"schema_version"`
	Detected       *adaptPlanDetected  `json:"detected"`
	PlannedChanges *[]adaptPlanChange  `json:"planned_changes"`
	Skipped        *[]adaptPlanSkipped `json:"skipped"`
}

func newWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Inspect and validate workflows",
	}

	cmd.AddCommand(newWorkflowValidateCmd())
	return cmd
}

func newWorkflowValidateCmd() *cobra.Command {
	var proposedPlanPath string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate workflow definitions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdWorkflowValidate(cmd.OutOrStdout(), deps.cfg.StateDir, proposedPlanPath)
		},
	}

	cmd.Flags().StringVar(&proposedPlanPath, "proposed", "", "Validate workflows after applying an adapt-plan.json in memory")
	return cmd
}

func cmdWorkflowValidate(stdout io.Writer, stateDir, proposedPlanPath string) error {
	var plan *adaptPlan
	if proposedPlanPath != "" {
		loadedPlan, err := loadAdaptPlan(proposedPlanPath)
		if err != nil {
			return fmt.Errorf("load proposed plan: %w", err)
		}
		plan = &loadedPlan
	}

	workflowsDir := filepath.Join(stateDir, "workflows")
	count, err := validateWorkflowDir(workflowsDir, plan)
	if err != nil {
		return fmt.Errorf("validate workflows: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "Validated %d workflow(s).\n", count); err != nil {
		return fmt.Errorf("write workflow validation summary: %w", err)
	}
	return nil
}

func validateWorkflowDir(workflowsDir string, plan *adaptPlan) (int, error) {
	workflowPaths, err := discoverWorkflowPaths(workflowsDir)
	if err != nil {
		return 0, err
	}
	if plan != nil {
		workflowPaths, err = applyProposedWorkflowPlan(workflowsDir, workflowPaths, *plan)
		if err != nil {
			return 0, err
		}
	}

	for _, path := range workflowPaths {
		if _, _, err := workflowpkg.LoadWithDigest(path); err != nil {
			return 0, err
		}
	}
	return len(workflowPaths), nil
}

func discoverWorkflowPaths(workflowsDir string) ([]string, error) {
	info, err := os.Stat(workflowsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat workflows dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workflows path %q is not a directory", workflowsDir)
	}

	paths := make([]string, 0)
	if err := filepath.WalkDir(workflowsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk workflows dir: %w", err)
	}

	sort.Strings(paths)
	return paths, nil
}

func applyProposedWorkflowPlan(workflowsDir string, workflowPaths []string, plan adaptPlan) ([]string, error) {
	deleted := make(map[string]struct{})
	for _, change := range plan.PlannedChanges {
		if !adaptPlanTargetsWorkflow(change.Path) {
			continue
		}
		switch change.Op {
		case "delete":
			deleted[change.Path] = struct{}{}
		case "patch", "replace", "create":
			return nil, fmt.Errorf("planned change %q with op %q cannot be validated with --proposed because adapt-plan.json does not include workflow contents", change.Path, change.Op)
		default:
			return nil, fmt.Errorf("planned change %q has unsupported op %q", change.Path, change.Op)
		}
	}

	filtered := make([]string, 0, len(workflowPaths))
	for _, path := range workflowPaths {
		if _, skip := deleted[workflowPlanPathForDiskPath(workflowsDir, path)]; skip {
			continue
		}
		filtered = append(filtered, path)
	}
	return filtered, nil
}

func workflowPlanPathForDiskPath(workflowsDir, path string) string {
	rel, err := filepath.Rel(workflowsDir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(filepath.Join(".xylem", "workflows", rel))
}

func adaptPlanTargetsWorkflow(path string) bool {
	return strings.HasPrefix(path, ".xylem/workflows/") && isWorkflowFilePath(path)
}

func isWorkflowFilePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func loadAdaptPlan(path string) (adaptPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return adaptPlan{}, fmt.Errorf("read adapt plan %q: %w", path, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var raw rawAdaptPlan
	if err := dec.Decode(&raw); err != nil {
		return adaptPlan{}, fmt.Errorf("decode adapt plan %q: %w", path, err)
	}
	if err := ensureSingleJSONObject(dec, path); err != nil {
		return adaptPlan{}, err
	}

	plan, err := normalizeAdaptPlan(raw)
	if err != nil {
		return adaptPlan{}, fmt.Errorf("validate adapt plan %q: %w", path, err)
	}
	return plan, nil
}

func ensureSingleJSONObject(dec *json.Decoder, path string) error {
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode adapt plan %q: unexpected trailing data", path)
		}
		return fmt.Errorf("decode adapt plan %q: %w", path, err)
	}
	return nil
}

func normalizeAdaptPlan(raw rawAdaptPlan) (adaptPlan, error) {
	switch {
	case raw.SchemaVersion == nil:
		return adaptPlan{}, fmt.Errorf(`"schema_version" is required`)
	case *raw.SchemaVersion != 1:
		return adaptPlan{}, fmt.Errorf(`"schema_version" must equal 1`)
	case raw.Detected == nil:
		return adaptPlan{}, fmt.Errorf(`"detected" is required`)
	case raw.PlannedChanges == nil:
		return adaptPlan{}, fmt.Errorf(`"planned_changes" is required`)
	case raw.Skipped == nil:
		return adaptPlan{}, fmt.Errorf(`"skipped" is required`)
	}

	planned := append([]adaptPlanChange(nil), (*raw.PlannedChanges)...)
	for i := range planned {
		path, err := normalizeAdaptPlanPath(planned[i].Path)
		if err != nil {
			return adaptPlan{}, fmt.Errorf(`"planned_changes[%d].path" %w`, i, err)
		}
		if err := validateAdaptPlanChange(path, planned[i]); err != nil {
			return adaptPlan{}, fmt.Errorf(`"planned_changes[%d]" %w`, i, err)
		}
		planned[i].Path = path
	}

	skipped := append([]adaptPlanSkipped(nil), (*raw.Skipped)...)
	for i := range skipped {
		path, err := normalizeAdaptPlanPath(skipped[i].Path)
		if err != nil {
			return adaptPlan{}, fmt.Errorf(`"skipped[%d].path" %w`, i, err)
		}
		if strings.TrimSpace(skipped[i].Reason) == "" {
			return adaptPlan{}, fmt.Errorf(`"skipped[%d].reason" must not be empty`, i)
		}
		if !isAllowedAdaptPlanPath(path) {
			return adaptPlan{}, fmt.Errorf(`"skipped[%d].path" %q is outside the adapt-plan allowlist`, i, path)
		}
		skipped[i].Path = path
	}

	return adaptPlan{
		SchemaVersion:  *raw.SchemaVersion,
		Detected:       *raw.Detected,
		PlannedChanges: planned,
		Skipped:        skipped,
	}, nil
}

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

func validateAdaptPlanChange(path string, change adaptPlanChange) error {
	switch change.Op {
	case "patch", "replace", "create", "delete":
	default:
		return fmt.Errorf(`has invalid op %q`, change.Op)
	}
	if !isAllowedAdaptPlanPath(path) {
		return fmt.Errorf(`path %q is outside the adapt-plan allowlist`, path)
	}
	if strings.TrimSpace(change.Rationale) == "" {
		return fmt.Errorf(`must include a non-empty rationale`)
	}
	if change.Op == "delete" && (!adaptPlanTargetsWorkflow(path) || !strings.HasPrefix(path, ".xylem/workflows/")) {
		return fmt.Errorf(`delete is only allowed for workflow YAML files under ".xylem/workflows/"`)
	}
	return nil
}

func isAllowedAdaptPlanPath(path string) bool {
	return path == ".xylem.yml" ||
		path == "AGENTS.md" ||
		strings.HasPrefix(path, ".xylem/") ||
		strings.HasPrefix(path, "docs/")
}
