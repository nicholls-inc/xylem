package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	bootstrap "github.com/nicholls-inc/xylem/cli/internal/bootstrap"
)

type analyzeRepoOutput struct {
	Root        string            `json:"root"`
	Timestamp   string            `json:"timestamp"`
	Languages   []string          `json:"languages"`
	Frameworks  []string          `json:"frameworks"`
	BuildTools  []string          `json:"build_tools"`
	EntryPoints []string          `json:"entry_points"`
	Dimensions  map[string]string `json:"dimensions"`
}

type auditLegibilityOutput struct {
	Root       string                 `json:"root"`
	Timestamp  string                 `json:"timestamp"`
	Overall    float64                `json:"overall"`
	Dimensions []auditDimensionOutput `json:"dimensions"`
}

type auditDimensionOutput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Weight      float64  `json:"weight"`
	Score       float64  `json:"score"`
	Gaps        []string `json:"gaps"`
	AutoFixable bool     `json:"auto_fixable"`
}

func newBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Analyze repository bootstrap surfaces",
	}

	cmd.AddCommand(
		newBootstrapAnalyzeRepoCmd(),
		newBootstrapAuditLegibilityCmd(),
	)

	return cmd
}

func newBootstrapAnalyzeRepoCmd() *cobra.Command {
	var rootPath string
	var outputPath string

	cmd := &cobra.Command{
		Use:   "analyze-repo",
		Short: "Analyze repository languages, tooling, and entry points",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveBootstrapRoot(rootPath)
			if err != nil {
				return err
			}

			profile, err := bootstrap.AnalyzeRepo(root)
			if err != nil {
				return fmt.Errorf("analyze repo: %w", err)
			}

			out := analyzeRepoOutput{
				Root:        root,
				Timestamp:   profile.AnalyzedAt.UTC().Format(timeFormatRFC3339),
				Languages:   normalizeLanguages(profile.Languages),
				Frameworks:  normalizeFrameworks(profile.Frameworks),
				BuildTools:  normalizeBuildTools(profile.BuildTools),
				EntryPoints: normalizeEntryPoints(profile.EntryPoints),
				Dimensions:  describeDimensions(bootstrap.DefaultDimensions()),
			}

			return writeBootstrapOutput(cmd.OutOrStdout(), outputPath, out)
		},
	}

	cmd.Flags().StringVar(&rootPath, "root", ".", "Repository root to analyze")
	cmd.Flags().StringVar(&outputPath, "output", "", "Write JSON to a file instead of stdout")
	return cmd
}

func newBootstrapAuditLegibilityCmd() *cobra.Command {
	var rootPath string
	var outputPath string

	cmd := &cobra.Command{
		Use:   "audit-legibility",
		Short: "Audit repository legibility and bootstrap readiness",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveBootstrapRoot(rootPath)
			if err != nil {
				return err
			}

			profile, err := bootstrap.AnalyzeRepo(root)
			if err != nil {
				return fmt.Errorf("analyze repo for audit: %w", err)
			}

			report, err := bootstrap.AuditLegibility(root, profile)
			if err != nil {
				return fmt.Errorf("audit legibility: %w", err)
			}

			out := auditLegibilityOutput{
				Root:      root,
				Timestamp: report.AuditedAt.UTC().Format(timeFormatRFC3339),
				Overall:   report.Overall,
			}
			for _, dim := range report.Dimensions {
				out.Dimensions = append(out.Dimensions, auditDimensionOutput{
					Name:        dim.Dimension.Name,
					Description: dim.Dimension.Description,
					Weight:      dim.Dimension.Weight,
					Score:       dim.Score,
					Gaps:        append([]string(nil), dim.Gaps...),
					AutoFixable: dim.AutoFixable,
				})
			}

			return writeBootstrapOutput(cmd.OutOrStdout(), outputPath, out)
		},
	}

	cmd.Flags().StringVar(&rootPath, "root", ".", "Repository root to audit")
	cmd.Flags().StringVar(&outputPath, "output", "", "Write JSON to a file instead of stdout")
	return cmd
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"

func resolveBootstrapRoot(rootPath string) (string, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", rootPath, err)
	}
	return absRoot, nil
}

func writeBootstrapOutput(stdout io.Writer, outputPath string, payload interface{}) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bootstrap output: %w", err)
	}
	data = append(data, '\n')

	if outputPath == "" || outputPath == "-" {
		if _, err := stdout.Write(data); err != nil {
			return fmt.Errorf("write bootstrap output: %w", err)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write output %q: %w", outputPath, err)
	}
	return nil
}

func normalizeLanguages(languages []bootstrap.Language) []string {
	result := make([]string, 0, len(languages))
	for _, lang := range languages {
		result = append(result, normalizeBootstrapName(lang.Name))
	}
	return uniqueSortedStrings(result)
}

func normalizeFrameworks(frameworks []bootstrap.Framework) []string {
	result := make([]string, 0, len(frameworks))
	for _, framework := range frameworks {
		result = append(result, normalizeBootstrapName(framework.Name))
	}
	return uniqueSortedStrings(result)
}

func normalizeBuildTools(buildTools []bootstrap.BuildTool) []string {
	result := make([]string, 0, len(buildTools))
	for _, buildTool := range buildTools {
		result = append(result, normalizeBootstrapName(buildTool.Name))
	}
	return uniqueSortedStrings(result)
}

func normalizeEntryPoints(entryPoints []bootstrap.EntryPoint) []string {
	result := make([]string, 0, len(entryPoints))
	for _, entryPoint := range entryPoints {
		switch {
		case entryPoint.Path != "":
			result = append(result, filepath.ToSlash(entryPoint.Path))
		case entryPoint.Name != "":
			result = append(result, entryPoint.Name)
		}
	}
	return uniqueSortedStrings(result)
}

func describeDimensions(dimensions []bootstrap.Dimension) map[string]string {
	descriptions := make(map[string]string, len(dimensions))
	for _, dim := range dimensions {
		descriptions[dimensionKey(dim.Name)] = dim.Description
	}
	return descriptions
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeBootstrapName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func dimensionKey(name string) string {
	switch name {
	case "Bootstrap Self-Sufficiency":
		return "self_sufficiency"
	case "Task Entry Points":
		return "task_entry_points"
	case "Validation Harness":
		return "validation_harness"
	case "Linting/Formatting":
		return "linting_formatting"
	case "Codebase Map":
		return "codebase_map"
	case "Doc Structure":
		return "doc_structure"
	case "Decision Records":
		return "decision_records"
	default:
		replacer := strings.NewReplacer("/", " ", "-", " ")
		return strings.Join(strings.Fields(strings.ToLower(replacer.Replace(name))), "_")
	}
}
