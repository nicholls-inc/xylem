package runner

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

func TestBuildPhaseGraph_Sequential(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "test",
		Phases: []workflow.Phase{
			{Name: "a"},
			{Name: "b"},
			{Name: "c"},
		},
	}

	g, err := buildPhaseGraph(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if g.parallel {
		t.Error("expected parallel=false for sequential workflow")
	}

	// Sequential: 3 waves, one phase each.
	if len(g.waves) != 3 {
		t.Fatalf("expected 3 waves, got %d", len(g.waves))
	}
	for i, wave := range g.waves {
		if len(wave) != 1 || wave[0] != i {
			t.Errorf("wave %d = %v, want [%d]", i, wave, i)
		}
	}
}

func TestBuildPhaseGraph_DiamondDependency(t *testing.T) {
	// Diamond: A -> B, A -> C, B+C -> D
	wf := &workflow.Workflow{
		Name: "test",
		Phases: []workflow.Phase{
			{Name: "a"},
			{Name: "b", DependsOn: []string{"a"}},
			{Name: "c", DependsOn: []string{"a"}},
			{Name: "d", DependsOn: []string{"b", "c"}},
		},
	}

	g, err := buildPhaseGraph(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !g.parallel {
		t.Error("expected parallel=true for workflow with depends_on")
	}

	// Wave 0: [a], Wave 1: [b, c], Wave 2: [d]
	if len(g.waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %v", len(g.waves), g.waves)
	}
	if len(g.waves[0]) != 1 || g.waves[0][0] != 0 {
		t.Errorf("wave 0 = %v, want [0]", g.waves[0])
	}
	if len(g.waves[1]) != 2 {
		t.Errorf("wave 1 = %v, want 2 phases", g.waves[1])
	}
	if len(g.waves[2]) != 1 || g.waves[2][0] != 3 {
		t.Errorf("wave 2 = %v, want [3]", g.waves[2])
	}
}

func TestBuildPhaseGraph_FullyParallel(t *testing.T) {
	// All phases have no dependencies — they should all be in wave 0.
	wf := &workflow.Workflow{
		Name: "test",
		Phases: []workflow.Phase{
			{Name: "a", DependsOn: []string{}},
			{Name: "b", DependsOn: []string{}},
			{Name: "c", DependsOn: []string{}},
		},
	}

	// HasDependencies is false when all DependsOn are empty slices.
	// This means sequential mode (backward compat).
	if wf.HasDependencies() {
		t.Fatal("HasDependencies() should be false for empty DependsOn slices")
	}
}

func TestBuildPhaseGraph_SinglePhase(t *testing.T) {
	wf := &workflow.Workflow{
		Name:   "test",
		Phases: []workflow.Phase{{Name: "only"}},
	}

	g, err := buildPhaseGraph(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(g.waves) != 1 || len(g.waves[0]) != 1 {
		t.Errorf("expected 1 wave with 1 phase, got %v", g.waves)
	}
}

func TestDependencyOutputs_Sequential(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "test",
		Phases: []workflow.Phase{
			{Name: "a"},
			{Name: "b"},
		},
	}

	g, err := buildPhaseGraph(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	allOutputs := map[string]string{
		"a": "output-a",
	}

	// In sequential mode, phase b sees all outputs.
	out := g.dependencyOutputs(1, allOutputs)
	if out["a"] != "output-a" {
		t.Errorf("sequential mode: expected output-a, got %q", out["a"])
	}
}

func TestDependencyOutputs_ContextFirewall(t *testing.T) {
	// Diamond: A -> B, A -> C, B+C -> D
	wf := &workflow.Workflow{
		Name: "test",
		Phases: []workflow.Phase{
			{Name: "a"},
			{Name: "b", DependsOn: []string{"a"}},
			{Name: "c", DependsOn: []string{"a"}},
			{Name: "d", DependsOn: []string{"b", "c"}},
		},
	}

	g, err := buildPhaseGraph(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	allOutputs := map[string]string{
		"a": "output-a",
		"b": "output-b",
		"c": "output-c",
	}

	// Phase b (index 1) should only see output from a.
	outB := g.dependencyOutputs(1, allOutputs)
	if len(outB) != 1 || outB["a"] != "output-a" {
		t.Errorf("phase b outputs = %v, want {a: output-a}", outB)
	}

	// Phase d (index 3) should only see outputs from b and c (not a).
	outD := g.dependencyOutputs(3, allOutputs)
	if len(outD) != 2 {
		t.Errorf("phase d outputs = %v, want 2 entries", outD)
	}
	if outD["b"] != "output-b" || outD["c"] != "output-c" {
		t.Errorf("phase d outputs = %v, want {b: output-b, c: output-c}", outD)
	}
	if _, hasA := outD["a"]; hasA {
		t.Error("phase d should NOT see output from a (context firewall)")
	}

	// Phase a (index 0) should see nothing (no deps).
	outA := g.dependencyOutputs(0, allOutputs)
	if len(outA) != 0 {
		t.Errorf("phase a outputs = %v, want empty", outA)
	}
}
