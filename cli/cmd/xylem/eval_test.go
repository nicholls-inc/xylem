package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewEvalCmd_Subcommands(t *testing.T) {
	cmd := newEvalCmd()

	subNames := map[string]bool{}
	for _, sub := range cmd.Commands() {
		subNames[sub.Name()] = true
	}

	for _, want := range []string{"run", "baseline", "compare"} {
		if !subNames[want] {
			t.Errorf("eval subcommand %q not registered", want)
		}
	}
}

func TestNewEvalRunCmd_Flags(t *testing.T) {
	cmd := newEvalRunCmd()

	if cmd.Flag("scenario") == nil {
		t.Error("missing --scenario flag")
	}
	if cmd.Flag("all") == nil {
		t.Error("missing --all flag")
	}
	if cmd.Flag("eval-dir") == nil {
		t.Error("missing --eval-dir flag")
	}
}

func TestNewEvalCompareCmd_Flags(t *testing.T) {
	cmd := newEvalCompareCmd()

	if cmd.Flag("regression-threshold") == nil {
		t.Error("missing --regression-threshold flag")
	}
}

func TestResolveEvalTargets_SingleScenario(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := filepath.Join(dir, "scenarios")
	scenarioDir := filepath.Join(scenariosDir, "my-scenario")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}

	targets, err := resolveEvalTargets(dir, "my-scenario", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if filepath.Base(targets[0]) != "my-scenario" {
		t.Errorf("expected my-scenario, got %s", filepath.Base(targets[0]))
	}
}

func TestResolveEvalTargets_All(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := filepath.Join(dir, "scenarios")
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := os.MkdirAll(filepath.Join(scenariosDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	targets, err := resolveEvalTargets(dir, "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 3 {
		t.Errorf("expected 3 targets, got %d", len(targets))
	}
}

func TestResolveEvalTargets_NoScenarioNoAll(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scenarios"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := resolveEvalTargets(dir, "", false)
	if err == nil {
		t.Error("expected error when neither --scenario nor --all is set")
	}
}

func TestResolveEvalTargets_MissingScenario(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scenarios"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := resolveEvalTargets(dir, "nonexistent", false)
	if err == nil {
		t.Error("expected error for nonexistent scenario")
	}
}

func TestRewardFromFile(t *testing.T) {
	dir := t.TempDir()

	// Missing file returns -1
	if got := rewardFromFile(dir); got != -1 {
		t.Errorf("expected -1 for missing file, got %v", got)
	}

	// Valid file
	if err := os.WriteFile(filepath.Join(dir, "reward.txt"), []byte("0.8750\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rewardFromFile(dir); got != 0.875 {
		t.Errorf("expected 0.875, got %v", got)
	}
}
