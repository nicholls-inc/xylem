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
  json     the intermediate graph as indented JSON (includes the
           trigger reverse index and orphaned workflow list)

Subcommands:
  state-machine  render the trigger → workflow flow as a Mermaid
                 stateDiagram-v2 with two coupled state machines
                 (issue labels and PR lifecycle), so cross-workflow loops
                 and dead ends are visible at a glance`,
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

	cmd.AddCommand(newVisualizeStateMachineCmd())
	return cmd
}

func newVisualizeStateMachineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "state-machine",
		Aliases: []string{"sm"},
		Short:   "Render triggers and workflows as an abstract state machine",
		Long: `Render the xylem configuration as a Mermaid stateDiagram-v2 made up of
two coupled state machines:

  * Issue labels    — states are sorted sets of GitHub issue/PR labels;
                      transitions are workflow runs fired by github /
                      github-pr sources. Per-task status_labels become
                      outbound transitions, so loops and dead ends (e.g. a
                      workflow that writes a label another source uses as a
                      trigger) are visible as cycles in the diagram.
  * PR lifecycle    — synthetic states for review_submitted, checks_failed,
                      commented, and merged events, with author allow/deny
                      lists embedded in the display label.

Missing workflows render with a dashed outline; orphan workflow files (YAML
present under the workflows directory but not referenced by any task) render
as isolated running(W) nodes with no incoming edge, so gaps and dead code
show up before any audit rule runs.

The output is plain Mermaid text and can be pasted into a ` + "`" + "`" + "`" + `mermaid fenced
block on GitHub/VSCode or piped through the mermaid CLI.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			output, _ := cmd.Flags().GetString("output")
			workflowsDir, _ := cmd.Flags().GetString("workflows-dir")
			if workflowsDir == "" {
				// Inherit from the parent command when the user doesn't
				// override the flag on the subcommand.
				workflowsDir, _ = cmd.InheritedFlags().GetString("workflows-dir")
			}
			return cmdVisualizeStateMachine(deps.cfg, workflowsDir, output)
		},
	}
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

	warnGraphGaps(g, workflowsDir)
	return nil
}

// cmdVisualizeStateMachine implements `xylem visualize state-machine`. It
// reuses visualize.Build so the reverse index, missing workflows, and orphan
// detection all run exactly once against the same graph.
func cmdVisualizeStateMachine(cfg *config.Config, workflowsDir, output string) error {
	g, err := visualize.Build(cfg, workflowsDir)
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
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

	if err := visualize.RenderStateMachine(g, w); err != nil {
		return fmt.Errorf("render state-machine: %w", err)
	}

	warnGraphGaps(g, workflowsDir)
	return nil
}

// warnGraphGaps writes the advisory "missing" and "orphan" workflow lines
// to stderr. This is the minimum audit surface promised by the plan:
// advisory only, never fails the command, but makes gaps visible without
// the operator needing to run a separate audit subcommand.
func warnGraphGaps(g *visualize.Graph, workflowsDir string) {
	if len(g.MissingWorkflows) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d workflow(s) referenced but not found under %s:\n", len(g.MissingWorkflows), workflowsDir)
		for _, name := range g.MissingWorkflows {
			fmt.Fprintf(os.Stderr, "  - missing: %s\n", name)
		}
	}
	if len(g.OrphanedWorkflows) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d workflow file(s) under %s are not referenced by any task:\n", len(g.OrphanedWorkflows), workflowsDir)
		for _, name := range g.OrphanedWorkflows {
			fmt.Fprintf(os.Stderr, "  - orphan: %s\n", name)
		}
	}
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
