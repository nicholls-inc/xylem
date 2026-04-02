package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/dtu"
)

func writeDTUManifest(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "universe.yaml")
	content := `metadata:
  name: Sample Universe
repositories:
  - owner: nicholls-inc
    name: xylem
    default_branch: main
    labels:
      - name: ready
    issues:
      - number: 1
        title: Fix it
        labels: [ready]
providers:
  scripts:
    - name: analyze
      provider: claude
      stdout: ok
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func TestDTUSubcommandRegistration(t *testing.T) {
	cmd := newRootCmd()
	var dtuCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "dtu" {
			dtuCmd = sub
			break
		}
	}
	if dtuCmd == nil {
		t.Fatal("expected dtu subcommand to be registered")
	}

	names := make(map[string]bool)
	for _, sub := range dtuCmd.Commands() {
		names[sub.Name()] = true
	}
	for _, name := range []string{"load", "materialize", "env", "run", "verify"} {
		if !names[name] {
			t.Errorf("expected dtu subcommand %q", name)
		}
	}
}

func TestDTULoadAndMaterializeHappyPath(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeDTUManifest(t, dir)
	stateDir := filepath.Join(dir, ".xylem")

	opts := &dtuOptions{ManifestPath: manifestPath, StateDir: stateDir}
	resolved, err := resolveDTUOptions(opts, manifestPath)
	if err != nil {
		t.Fatalf("resolveDTUOptions() error = %v", err)
	}
	if resolved.UniverseID != "sample-universe" {
		t.Fatalf("UniverseID = %q, want sample-universe", resolved.UniverseID)
	}

	if err := saveDTUState(resolved); err != nil {
		t.Fatalf("saveDTUState() error = %v", err)
	}
	if err := materializeDTURuntime(resolved); err != nil {
		t.Fatalf("materializeDTURuntime() error = %v", err)
	}

	store, err := dtu.NewStore(stateDir, resolved.UniverseID)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if state.ManifestPath != resolved.ManifestPath {
		t.Fatalf("ManifestPath = %q, want %q", state.ManifestPath, resolved.ManifestPath)
	}

	for _, path := range []string{resolved.RuntimeDir, resolved.WorkDir, resolved.ShimDir, store.Path(), store.EventLogPath()} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable() error = %v", err)
	}
	for _, shim := range dtuShimNames {
		path := filepath.Join(resolved.ShimDir, shimWrapperFilename(shim))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected shim wrapper %s to exist: %v", path, err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", path, err)
		}
		if got, want := string(content), shimWrapperContent(binary, shim); got != want {
			t.Fatalf("shim wrapper %q = %q, want %q", shim, got, want)
		}
		if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
			t.Fatalf("shim wrapper %q mode = %v, want executable", shim, info.Mode())
		}
	}

	env := resolved.env(resolved.ShimDir)
	wantPairs := map[string]string{
		dtu.EnvUniverseID:   resolved.UniverseID,
		dtu.EnvStatePath:    store.Path(),
		dtu.EnvStateDir:     resolved.StateDir,
		dtu.EnvManifestPath: resolved.ManifestPath,
		dtu.EnvEventLogPath: store.EventLogPath(),
		dtu.EnvShimDir:      resolved.ShimDir,
	}
	for key, want := range wantPairs {
		found := false
		for _, entry := range env {
			k, v, ok := strings.Cut(entry, "=")
			if ok && k == key {
				found = true
				if v != want {
					t.Fatalf("%s = %q, want %q", key, v, want)
				}
			}
		}
		if !found {
			t.Fatalf("expected env %s", key)
		}
	}

	pathEntry := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			pathEntry = strings.TrimPrefix(entry, "PATH=")
			break
		}
	}
	if pathEntry == "" {
		t.Fatal("expected PATH entry")
	}
	if first := strings.Split(pathEntry, string(os.PathListSeparator))[0]; first != resolved.ShimDir {
		t.Fatalf("PATH prefix = %q, want %q", first, resolved.ShimDir)
	}
}

func TestShimDispatchCommandRequiresShimName(t *testing.T) {
	cmd := newShimDispatchCmd()
	if err := cmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "shim name is required") {
		t.Fatalf("RunE() error = %v, want shim name is required", err)
	}
}

func TestDtuEnvCommandPrintsShellExports(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeDTUManifest(t, dir)
	stateDir := filepath.Join(dir, ".xylem")

	cmd := newDtuEnvCmd(&dtuOptions{ManifestPath: manifestPath, StateDir: stateDir})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--shell"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	text := out.String()
	for _, expected := range []string{
		"export " + dtu.EnvStatePath + "=",
		"export " + dtu.EnvStateDir + "=",
		"export " + dtu.EnvUniverseID + "=",
		"export " + dtu.EnvEventLogPath + "=",
		"export PATH=",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in output, got %q", expected, text)
		}
	}
}

func TestDtuVerifyCommandPrintsReportAndFailsOnMismatch(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeDTUManifest(t, dir)
	stateDir := filepath.Join(dir, ".xylem")

	originalSuite := dtuLoadLiveVerificationSuite
	originalRegistry := dtuLoadDivergenceRegistry
	originalPolicy := dtuLoadAttributionPolicy
	originalRun := dtuRunLiveVerification
	originalExecutable := dtuExecutablePath
	defer func() {
		dtuLoadLiveVerificationSuite = originalSuite
		dtuLoadDivergenceRegistry = originalRegistry
		dtuLoadAttributionPolicy = originalPolicy
		dtuRunLiveVerification = originalRun
		dtuExecutablePath = originalExecutable
	}()

	dtuLoadLiveVerificationSuite = func(path string) (*dtu.LiveVerificationSuite, error) {
		return &dtu.LiveVerificationSuite{Version: "v1"}, nil
	}
	dtuLoadDivergenceRegistry = func(path string) (*dtu.DivergenceRegistry, error) {
		return &dtu.DivergenceRegistry{Version: "v1"}, nil
	}
	dtuLoadAttributionPolicy = func(path string) (*dtu.AttributionPolicy, error) {
		return &dtu.AttributionPolicy{Version: "v1"}, nil
	}
	dtuExecutablePath = func() (string, error) {
		return "/path/to/xylem", nil
	}
	dtuRunLiveVerification = func(ctx context.Context, suite *dtu.LiveVerificationSuite, registry *dtu.DivergenceRegistry, policy *dtu.AttributionPolicy, opts dtu.VerificationRunOptions) (*dtu.VerificationReport, error) {
		return &dtu.VerificationReport{
			Summary: dtu.VerificationSummary{
				DifferentialRun: 1,
				Mismatches:      1,
			},
			Differential: []dtu.VerificationCaseReport{
				{
					Name:    "gh-search-issues-open-bug",
					Status:  dtu.VerificationStatusMismatch,
					Message: "live and twin normalized outputs differ",
					Live:    &dtu.VerificationExecution{CanonicalJSON: `[{"number":1}]`},
					Twin:    &dtu.VerificationExecution{CanonicalJSON: `[{"number":2}]`},
					AttributionRule: &dtu.AttributionRule{
						Classification: dtu.AttributionClassificationFidelityBug,
						NextStep:       "Correct the boundary contract in the twin, add a divergence entry if intentional, and rerun the scenario.",
					},
				},
			},
		}, nil
	}

	cmd := newDtuVerifyCmd(&dtuOptions{ManifestPath: manifestPath, StateDir: stateDir})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "DTU verification failed") {
		t.Fatalf("Execute() error = %v, want DTU verification failed", err)
	}

	text := out.String()
	for _, expected := range []string{
		"DTU verification summary:",
		"[mismatch] gh-search-issues-open-bug: live and twin normalized outputs differ",
		"live normalized: [{\"number\":1}]",
		"twin normalized: [{\"number\":2}]",
		"attribution: fidelity_bug -> Correct the boundary contract in the twin, add a divergence entry if intentional, and rerun the scenario.",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in output, got %q", expected, text)
		}
	}
}
