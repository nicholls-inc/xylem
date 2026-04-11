package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/hardening"
)

func newHardenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "harden",
		Aliases: []string{"hardening-report"},
		Short:   "Deterministic helpers for recurring hardening audits",
	}
	cmd.AddCommand(
		newHardenInventoryCmd(),
		newHardenScoreCmd(),
		newHardenFileIssuesCmd(),
		newHardenTrackCmd(),
	)
	return cmd
}

func newHardenInventoryCmd() *cobra.Command {
	var repoRoot, workflowDir, outputPath, nowRaw string
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Inventory workflow phases and classify their hardening shape",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(outputPath) == "" {
				outputPath = filepath.Join(defaultStateDirPath(), "state", "hardening-audit", "inventory.json")
			}
			now, err := parseOptionalNow(nowRaw)
			if err != nil {
				return err
			}
			inventory, err := cmdHardenInventory(repoRoot, workflowDir, outputPath, now)
			if err != nil {
				return err
			}
			fmt.Printf("Wrote %s (%d workflows, %d phases)\n", outputPath, len(inventory.Workflows), countInventoryPhases(inventory))
			return nil
		},
	}
	cmd.Flags().StringVar(&repoRoot, "repo-root", ".", "Repository root containing .xylem/")
	cmd.Flags().StringVar(&workflowDir, "workflow-dir", ".xylem/workflows", "Directory containing workflow YAML files")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write the inventory JSON")
	cmd.Flags().StringVar(&nowRaw, "now", "", "Optional RFC3339 timestamp override for deterministic runs")
	return cmd
}

func newHardenScoreCmd() *cobra.Command {
	var repoRoot, inventoryPath, stateDir, outputPath, nowRaw string
	cmd := &cobra.Command{
		Use:   "score",
		Short: "Score fuzzy and mixed phases for deterministic hardening candidacy",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(outputPath) == "" {
				outputPath = filepath.Join(defaultStateDirPath(), "state", "hardening-audit", "scores.json")
			}
			now, err := parseOptionalNow(nowRaw)
			if err != nil {
				return err
			}
			report, err := cmdHardenScore(repoRoot, inventoryPath, stateDir, outputPath, now)
			if err != nil {
				return err
			}
			fmt.Printf("Wrote %s (%d candidates, %d top candidates)\n", outputPath, len(report.Candidates), len(report.TopCandidates))
			return nil
		},
	}
	cmd.Flags().StringVar(&repoRoot, "repo-root", ".", "Repository root containing .xylem/")
	cmd.Flags().StringVar(&inventoryPath, "inventory", "", "Path to the inventory JSON")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "State directory containing phases/<vessel>/summary.json")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write the scores JSON")
	cmd.Flags().StringVar(&nowRaw, "now", "", "Optional RFC3339 timestamp override for deterministic runs")
	cmd.MarkFlagRequired("inventory") //nolint:errcheck
	return cmd
}

func newHardenFileIssuesCmd() *cobra.Command {
	var proposalsPath, outputPath, repo string
	var labels []string
	cmd := &cobra.Command{
		Use:   "file-issues",
		Short: "Create or dedupe hardening issues from ranked proposals",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(outputPath) == "" {
				outputPath = filepath.Join(defaultStateDirPath(), "state", "hardening-audit", "filed-issues.json")
			}
			result, err := cmdHardenFileIssues(repo, proposalsPath, outputPath, labels)
			if err != nil {
				return err
			}
			fmt.Printf("Wrote %s (%d created, %d existing)\n", outputPath, len(result.Created), len(result.Existing))
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository slug (owner/name); defaults to repo from config")
	cmd.Flags().StringVar(&proposalsPath, "proposals", "", "Path to the ranked proposals JSON")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write the filed-issues JSON")
	cmd.Flags().StringSliceVar(&labels, "labels", []string{"enhancement", "ready-for-work"}, "Labels to apply to new issues")
	cmd.MarkFlagRequired("proposals") //nolint:errcheck
	return cmd
}

func newHardenTrackCmd() *cobra.Command {
	var repoRoot, proposalsPath, filedPath, ledgerPath, nowRaw string
	cmd := &cobra.Command{
		Use:   "track",
		Short: "Append this hardening audit run to docs/hardening-ledger.md",
		RunE: func(cmd *cobra.Command, args []string) error {
			now, err := parseOptionalNow(nowRaw)
			if err != nil {
				return err
			}
			if err := cmdHardenTrack(repoRoot, proposalsPath, filedPath, ledgerPath, now); err != nil {
				return err
			}
			fmt.Printf("Updated %s\n", ledgerPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&repoRoot, "repo-root", ".", "Repository root containing docs/")
	cmd.Flags().StringVar(&proposalsPath, "proposals", "", "Path to the ranked proposals JSON")
	cmd.Flags().StringVar(&filedPath, "filed", "", "Path to the filed-issues JSON")
	cmd.Flags().StringVar(&ledgerPath, "ledger", hardening.DefaultLedgerPath, "Ledger markdown path relative to the repo root")
	cmd.Flags().StringVar(&nowRaw, "now", "", "Optional RFC3339 timestamp override for deterministic runs")
	cmd.MarkFlagRequired("proposals") //nolint:errcheck
	cmd.MarkFlagRequired("filed")     //nolint:errcheck
	return cmd
}

func cmdHardenInventory(repoRoot, workflowDir, outputPath string, now time.Time) (*hardening.Inventory, error) {
	repoRoot = defaultRepoRoot(repoRoot)
	if strings.TrimSpace(outputPath) == "" {
		outputPath = filepath.Join(defaultStateDirPath(), "state", "hardening-audit", "inventory.json")
	}
	inventory, err := hardening.GenerateInventory(repoRoot, workflowDir, now)
	if err != nil {
		return nil, fmt.Errorf("generate inventory: %w", err)
	}
	if err := hardening.WriteJSON(outputPath, inventory); err != nil {
		return nil, err
	}
	return inventory, nil
}

func cmdHardenScore(repoRoot, inventoryPath, stateDir, outputPath string, now time.Time) (*hardening.ScoreReport, error) {
	if strings.TrimSpace(outputPath) == "" {
		outputPath = filepath.Join(defaultStateDirPath(), "state", "hardening-audit", "scores.json")
	}
	if strings.TrimSpace(stateDir) == "" {
		stateDir = defaultStateDirPath()
	}
	inventory, err := hardening.LoadInventory(inventoryPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(repoRoot) != "" {
		inventory.RepoRoot = defaultRepoRoot(repoRoot)
	}
	report, err := hardening.ScoreInventory(inventory, stateDir, now)
	if err != nil {
		return nil, fmt.Errorf("score inventory: %w", err)
	}
	if err := hardening.WriteJSON(outputPath, report); err != nil {
		return nil, err
	}
	return report, nil
}

func cmdHardenFileIssues(repo, proposalsPath, outputPath string, labels []string) (*hardening.FileResult, error) {
	if strings.TrimSpace(outputPath) == "" {
		outputPath = filepath.Join(defaultStateDirPath(), "state", "hardening-audit", "filed-issues.json")
	}
	if strings.TrimSpace(repo) == "" {
		if deps == nil || deps.cfg == nil || strings.TrimSpace(deps.cfg.Repo) == "" {
			return nil, fmt.Errorf("file-issues requires --repo or loaded config repo")
		}
		repo = deps.cfg.Repo
	}
	proposals, err := hardening.LoadProposals(proposalsPath)
	if err != nil {
		return nil, err
	}
	result, err := hardening.FileIssues(context.Background(), &realCmdRunner{}, repo, proposals, labels)
	if err != nil {
		return nil, err
	}
	if err := hardening.WriteJSON(outputPath, result); err != nil {
		return nil, err
	}
	return result, nil
}

func cmdHardenTrack(repoRoot, proposalsPath, filedPath, ledgerPath string, now time.Time) error {
	repoRoot = defaultRepoRoot(repoRoot)
	proposals, err := hardening.LoadProposals(proposalsPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filedPath)
	if err != nil {
		return fmt.Errorf("read filed issues %q: %w", filedPath, err)
	}
	var filed hardening.FileResult
	if err := json.Unmarshal(data, &filed); err != nil {
		return fmt.Errorf("parse filed issues %q: %w", filedPath, err)
	}
	if err := hardening.AppendLedger(repoRoot, ledgerPath, proposals, &filed, now); err != nil {
		return err
	}
	return nil
}

func parseOptionalNow(nowRaw string) (time.Time, error) {
	if strings.TrimSpace(nowRaw) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, nowRaw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse --now: %w", err)
	}
	return parsed, nil
}

func defaultRepoRoot(repoRoot string) string {
	if strings.TrimSpace(repoRoot) != "" {
		return repoRoot
	}
	return "."
}

func defaultStateDirPath() string {
	if deps != nil && deps.cfg != nil && strings.TrimSpace(deps.cfg.StateDir) != "" {
		return deps.cfg.StateDir
	}
	return ".xylem"
}

func countInventoryPhases(inventory *hardening.Inventory) int {
	if inventory == nil {
		return 0
	}
	total := 0
	for _, workflow := range inventory.Workflows {
		total += len(workflow.Phases)
	}
	return total
}
