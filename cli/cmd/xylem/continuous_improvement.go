package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/continuousimprovement"
)

var selectContinuousImprovement = continuousimprovement.SelectAndPersist

func newContinuousImprovementCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "continuous-improvement",
		Short: "Deterministic helpers for scheduled continuous improvement runs",
	}
	cmd.AddCommand(newContinuousImprovementSelectCmd())
	return cmd
}

func newContinuousImprovementSelectCmd() *cobra.Command {
	var statePath string
	var selectionPath string
	var nowRaw string
	cmd := &cobra.Command{
		Use:   "select",
		Short: "Choose and persist the next continuous improvement focus area",
		RunE: func(cmd *cobra.Command, args []string) error {
			var now time.Time
			if strings.TrimSpace(nowRaw) != "" {
				parsed, err := time.Parse(time.RFC3339, nowRaw)
				if err != nil {
					return fmt.Errorf("parse --now: %w", err)
				}
				now = parsed
			}
			selection, err := cmdContinuousImprovementSelect(statePath, selectionPath, now)
			if err != nil {
				return err
			}
			fmt.Print(continuousimprovement.RenderMarkdown(selection))
			return nil
		},
	}
	cmd.Flags().StringVar(&statePath, "state", "", "Path to the continuous-improvement rotation state JSON")
	cmd.Flags().StringVar(&selectionPath, "selection", "", "Path to write the current selection JSON")
	cmd.Flags().StringVar(&nowRaw, "now", "", "Optional RFC3339 timestamp override for deterministic runs")
	return cmd
}

func cmdContinuousImprovementSelect(statePath, selectionPath string, now time.Time) (*continuousimprovement.Selection, error) {
	if deps == nil || deps.cfg == nil {
		return nil, fmt.Errorf("continuous-improvement select requires loaded config")
	}
	if strings.TrimSpace(statePath) == "" {
		statePath = filepath.Join(deps.cfg.StateDir, "state", "continuous-improvement", "state.json")
	}
	if strings.TrimSpace(selectionPath) == "" {
		selectionPath = filepath.Join(deps.cfg.StateDir, "state", "continuous-improvement", "current-selection.json")
	}
	selection, err := selectContinuousImprovement(continuousimprovement.Options{
		Repo:          detectLessonsRepo(deps.cfg),
		Now:           now,
		StatePath:     statePath,
		SelectionPath: selectionPath,
	})
	if err != nil {
		return nil, fmt.Errorf("select continuous improvement focus: %w", err)
	}
	return selection, nil
}
