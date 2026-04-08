package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/visualize"
)

func newVisualizeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "visualize",
		Aliases: []string{"viz"},
		Short:   "Visualize triggers and workflows from .xylem.yml",
		Long: `Render the sources, triggers, and workflows configured in .xylem.yml
as a diagram. Workflows referenced by a task are loaded from
.xylem/workflows/<name>.yaml and their phases and gates are included.

Supported output formats:
  mermaid  flowchart text suitable for GitHub/VSCode markdown (default)
  dot      Graphviz digraph text for ` + "`dot -Tpng`" + `
  json     the intermediate graph as indented JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("output")
			workflowsDir, _ := cmd.Flags().GetString("workflows-dir")
			return cmdVisualize(deps.cfg, workflowsDir, format, output)
		},
	}
	cmd.Flags().StringP("format", "f", "mermaid", "Output format: mermaid|dot|json")
	cmd.Flags().StringP("output", "o", "", "Write to file instead of stdout")
	cmd.Flags().String("workflows-dir", filepath.Join(".xylem", "workflows"), "Directory containing workflow YAML files")
	return cmd
}

func cmdVisualize(cfg *config.Config, workflowsDir, format, output string) error {
	g, err := visualize.Build(cfg, workflowsDir)
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}

	render, err := pickRenderer(format)
	if err != nil {
		return err
	}

	var w io.Writer = os.Stdout
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	if err := render(g, w); err != nil {
		return fmt.Errorf("render %s: %w", format, err)
	}

	// Surface missing workflows so the diagram isn't silently incomplete.
	if len(g.MissingWorkflows) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d workflow(s) referenced but not found under %s:\n", len(g.MissingWorkflows), workflowsDir)
		for _, name := range g.MissingWorkflows {
			fmt.Fprintf(os.Stderr, "  - %s\n", name)
		}
	}

	return nil
}

func pickRenderer(format string) (func(*visualize.Graph, io.Writer) error, error) {
	switch format {
	case "", "mermaid":
		return visualize.RenderMermaid, nil
	case "dot":
		return visualize.RenderDOT, nil
	case "json":
		return visualize.RenderJSON, nil
	default:
		return nil, fmt.Errorf("unknown format %q (supported: mermaid, dot, json)", format)
	}
}
