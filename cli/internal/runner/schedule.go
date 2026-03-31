package runner

import (
	"fmt"

	"github.com/nicholls-inc/xylem/cli/internal/orchestrator"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

// phaseGraph holds the orchestrator topology and computed execution waves
// for a workflow. Waves are groups of phase indices that can execute concurrently;
// within each wave, all dependencies are satisfied by prior waves.
type phaseGraph struct {
	orch  *orchestrator.Orchestrator
	waves [][]int // each element is a list of phase indices
	// deps maps each phase index to the set of phase names it depends on.
	// When the workflow has no explicit depends_on, this encodes the implicit
	// sequential chain (each phase depends on its predecessor).
	deps map[int]map[string]struct{}
	// parallel is true when the workflow declares explicit depends_on on any phase,
	// meaning waves may contain more than one phase.
	parallel bool
}

// buildPhaseGraph creates an orchestrator topology from a workflow and computes
// execution waves via level-based topological sort (Kahn's algorithm).
//
// When no phase declares depends_on, phases are chained sequentially and
// parallel is false. When any phase declares depends_on, the explicit
// dependency graph is used and parallel is true.
func buildPhaseGraph(wf *workflow.Workflow) (*phaseGraph, error) {
	n := len(wf.Phases)
	nameToIdx := make(map[string]int, n)
	for i, p := range wf.Phases {
		nameToIdx[p.Name] = i
	}

	hasExplicit := wf.HasDependencies()

	// Build successor list and in-degree for topological sort.
	successors := make([][]int, n)
	inDegree := make([]int, n)
	deps := make(map[int]map[string]struct{}, n)

	if hasExplicit {
		for i, p := range wf.Phases {
			if len(p.DependsOn) > 0 {
				deps[i] = make(map[string]struct{}, len(p.DependsOn))
				for _, dep := range p.DependsOn {
					j := nameToIdx[dep]
					successors[j] = append(successors[j], i)
					inDegree[i]++
					deps[i][dep] = struct{}{}
				}
			}
		}
	} else {
		// Implicit sequential chain: phase i depends on phase i-1.
		for i := 1; i < n; i++ {
			successors[i-1] = append(successors[i-1], i)
			inDegree[i] = 1
			deps[i] = map[string]struct{}{wf.Phases[i-1].Name: {}}
		}
	}

	// Kahn's algorithm: level-based topological sort.
	var waves [][]int
	done := make([]bool, n)

	for {
		var wave []int
		for i := 0; i < n; i++ {
			if !done[i] && inDegree[i] == 0 {
				wave = append(wave, i)
			}
		}
		if len(wave) == 0 {
			break
		}
		waves = append(waves, wave)
		for _, i := range wave {
			done[i] = true
			for _, j := range successors[i] {
				inDegree[j]--
			}
		}
	}

	// Build orchestrator topology.
	orch := orchestrator.NewOrchestrator(orchestrator.OrchestratorConfig{
		FailurePolicy: "fail-fast",
	})

	for _, p := range wf.Phases {
		if err := orch.AddAgent(p.Name, p.Name); err != nil {
			return nil, fmt.Errorf("add phase %q to orchestrator: %w", p.Name, err)
		}
	}

	for _, p := range wf.Phases {
		for _, dep := range p.DependsOn {
			if err := orch.AddEdge(dep, p.Name, "depends"); err != nil {
				return nil, fmt.Errorf("add dependency edge %s -> %s: %w", dep, p.Name, err)
			}
		}
	}
	// For implicit sequential, add edges too.
	if !hasExplicit && n > 1 {
		for i := 0; i < n-1; i++ {
			if err := orch.AddEdge(wf.Phases[i].Name, wf.Phases[i+1].Name, "sequential"); err != nil {
				return nil, fmt.Errorf("add sequential edge: %w", err)
			}
		}
	}

	return &phaseGraph{
		orch:     orch,
		waves:    waves,
		deps:     deps,
		parallel: hasExplicit,
	}, nil
}

// dependencyOutputs returns the subset of allOutputs that the phase at phaseIdx
// is allowed to see. When the workflow uses explicit depends_on (parallel mode),
// only outputs from declared dependencies are included — this is the context
// firewall. In sequential mode, all previous outputs are visible for backward
// compatibility.
func (g *phaseGraph) dependencyOutputs(phaseIdx int, allOutputs map[string]string) map[string]string {
	if !g.parallel {
		// Sequential mode: all outputs visible (backward compat).
		out := make(map[string]string, len(allOutputs))
		for k, v := range allOutputs {
			out[k] = v
		}
		return out
	}

	depSet := g.deps[phaseIdx]
	if len(depSet) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(depSet))
	for dep := range depSet {
		if v, ok := allOutputs[dep]; ok {
			out[dep] = v
		}
	}
	return out
}
