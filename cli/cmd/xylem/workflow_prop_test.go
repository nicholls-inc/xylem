package main

import (
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

func TestPropApplyProposedWorkflowPlanDeleteOnlyRemovesNamedWorkflows(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		rootDir, err := os.MkdirTemp("", "xylem-workflow-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(rootDir)

		workflowsDir := filepath.Join(rootDir, ".xylem", "workflows")
		if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
			rt.Fatalf("MkdirAll(%q) error = %v", workflowsDir, err)
		}

		candidates := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
		workflowPaths := make([]string, 0, len(candidates))
		deleted := make(map[string]bool, len(candidates))
		keptCount := 0
		for _, name := range candidates {
			ext := rapid.SampledFrom([]string{".yaml", ".yml"}).Draw(rt, "ext-"+name)
			path := filepath.Join(workflowsDir, name+ext)
			workflowPaths = append(workflowPaths, path)
			if rapid.Bool().Draw(rt, "delete-"+name) {
				deleted[workflowPlanPathForDiskPath(workflowsDir, path)] = true
				continue
			}
			keptCount++
		}

		changes := make([]adaptPlanChange, 0, len(deleted)+1)
		for path := range deleted {
			changes = append(changes, adaptPlanChange{
				Path:      path,
				Op:        "delete",
				Rationale: "remove inapplicable workflow",
			})
		}
		changes = append(changes, adaptPlanChange{
			Path:      "docs/README.md",
			Op:        "patch",
			Rationale: "unrelated doc update",
		})

		filtered, err := applyProposedWorkflowPlan(workflowsDir, workflowPaths, adaptPlan{
			SchemaVersion:  1,
			Detected:       adaptPlanDetected{},
			PlannedChanges: changes,
			Skipped:        []adaptPlanSkipped{},
		})
		if err != nil {
			rt.Fatalf("applyProposedWorkflowPlan() error = %v", err)
		}
		if len(filtered) != keptCount {
			rt.Fatalf("len(filtered) = %d, want %d", len(filtered), keptCount)
		}
		for _, path := range filtered {
			if deleted[workflowPlanPathForDiskPath(workflowsDir, path)] {
				rt.Fatalf("filtered workflows unexpectedly retained deleted path %q", path)
			}
		}
	})
}
