package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/bootstrap"
	workflowpkg "github.com/nicholls-inc/xylem/cli/internal/workflow"
	"github.com/spf13/cobra"
)

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
	var plan *bootstrap.AdaptPlan
	if proposedPlanPath != "" {
		var err error
		plan, err = bootstrap.ReadAdaptPlan(proposedPlanPath)
		if err != nil {
			return fmt.Errorf("load proposed plan: %w", err)
		}
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

func validateWorkflowDir(workflowsDir string, plan *bootstrap.AdaptPlan) (int, error) {
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

func applyProposedWorkflowPlan(workflowsDir string, workflowPaths []string, plan bootstrap.AdaptPlan) ([]string, error) {
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
