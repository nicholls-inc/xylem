package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/dtu"
)

const dtuDefaultStateDir = ".xylem"

var dtuShimNames = []string{"gh", "git", "claude", "copilot"}

var (
	dtuLoadLiveVerificationSuite = dtu.LoadLiveVerificationSuite
	dtuLoadDivergenceRegistry    = dtu.LoadDivergenceRegistry
	dtuLoadAttributionPolicy     = dtu.LoadAttributionPolicy
	dtuRunLiveVerification       = dtu.RunLiveVerification
	dtuExecutablePath            = os.Executable
)

type dtuOptions struct {
	ManifestPath string
	UniverseID   string
	StateDir     string
	ShimDir      string
	WorkDir      string
}

func newDtuCmd() *cobra.Command {
	opts := &dtuOptions{}
	cmd := &cobra.Command{
		Use:   "dtu",
		Short: "Manage Digital Twin Universe state and runtime",
	}

	cmd.PersistentFlags().StringVar(&opts.ManifestPath, "manifest", "", "DTU manifest YAML path")
	cmd.PersistentFlags().StringVar(&opts.UniverseID, "universe", "", "DTU universe ID (defaults to manifest name)")
	cmd.PersistentFlags().StringVar(&opts.StateDir, "state-dir", "", "DTU/xylem state directory (defaults to xylem state_dir)")
	cmd.PersistentFlags().StringVar(&opts.ShimDir, "shim-dir", "", "Directory containing DTU shim binaries")
	cmd.PersistentFlags().StringVar(&opts.WorkDir, "workdir", "", "Working directory for materialized DTU artifacts")

	cmd.AddCommand(
		newDtuLoadCmd(opts),
		newDtuMaterializeCmd(opts),
		newDtuEnvCmd(opts),
		newDtuRunCmd(opts),
		newDtuVerifyCmd(opts),
	)

	return cmd
}

func newDtuLoadCmd(opts *dtuOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "load [manifest]",
		Short: "Load a DTU manifest and write normalized DTU state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveDTUOptions(opts, firstNonEmpty(opts.ManifestPath, argsFirst(args)))
			if err != nil {
				return err
			}
			if err := saveDTUState(resolved); err != nil {
				return err
			}
			cmd.Printf("Initialized DTU universe %q at %s\n", resolved.UniverseID, resolved.Store.Path())
			return nil
		},
	}
}

func newDtuMaterializeCmd(opts *dtuOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "materialize [manifest]",
		Short: "Materialize DTU state and runtime directories",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveDTUOptions(opts, firstNonEmpty(opts.ManifestPath, argsFirst(args)))
			if err != nil {
				return err
			}
			if err := saveDTUState(resolved); err != nil {
				return err
			}
			if err := materializeDTURuntime(resolved); err != nil {
				return err
			}
			cmd.Printf("Materialized DTU universe %q\n", resolved.UniverseID)
			cmd.Printf("State file: %s\n", resolved.Store.Path())
			cmd.Printf("Runtime dir: %s\n", resolved.RuntimeDir)
			cmd.Printf("Work dir: %s\n", resolved.WorkDir)
			cmd.Printf("Shim dir: %s\n", resolved.ShimDir)
			return nil
		},
	}
}

func newDtuEnvCmd(opts *dtuOptions) *cobra.Command {
	var shell bool
	cmd := &cobra.Command{
		Use:   "env [manifest]",
		Short: "Print DTU environment exports for xylem and shims",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveDTUOptions(opts, firstNonEmpty(opts.ManifestPath, argsFirst(args)))
			if err != nil {
				return err
			}
			if err := saveDTUState(resolved); err != nil {
				return err
			}
			if err := materializeDTURuntime(resolved); err != nil {
				return err
			}

			for _, entry := range resolved.env(resolved.ShimDir) {
				if shell {
					key, value, _ := strings.Cut(entry, "=")
					cmd.Printf("export %s=%s\n", key, shellEscape(value))
					continue
				}
				cmd.Println(entry)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&shell, "shell", true, "Print shell export statements")
	return cmd
}

func newDtuRunCmd(opts *dtuOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "run --manifest <path> -- <xylem args...>",
		Short: "Run xylem under a materialized DTU environment",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("xylem arguments are required after --")
			}
			resolved, err := resolveDTUOptions(opts, opts.ManifestPath)
			if err != nil {
				return err
			}
			if err := saveDTUState(resolved); err != nil {
				return err
			}
			if err := materializeDTURuntime(resolved); err != nil {
				return err
			}

			binary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve xylem executable: %w", err)
			}
			runner := &realCmdRunner{}
			if err := runner.RunProcessWithEnv(cmd.Context(), resolved.WorkDir, resolved.env(resolved.ShimDir), binary, args...); err != nil {
				return fmt.Errorf("run xylem in dtu environment: %w", err)
			}
			return nil
		},
	}
}

func newDtuVerifyCmd(opts *dtuOptions) *cobra.Command {
	var (
		suitePath      string
		registryPath   string
		policyPath     string
		verificationWD string
	)

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Run env-gated live DTU differential checks and canaries",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("verify does not accept positional arguments")
			}

			resolved, err := resolveDTUOptions(opts, firstNonEmpty(opts.ManifestPath, verificationDefaultManifestPath()))
			if err != nil {
				return err
			}
			if err := saveDTUState(resolved); err != nil {
				return err
			}
			if err := materializeDTURuntime(resolved); err != nil {
				return err
			}

			suite, err := dtuLoadLiveVerificationSuite(filepath.Clean(suitePath))
			if err != nil {
				return err
			}
			registry, err := dtuLoadDivergenceRegistry(filepath.Clean(registryPath))
			if err != nil {
				return err
			}
			policy, err := dtuLoadAttributionPolicy(filepath.Clean(policyPath))
			if err != nil {
				return err
			}
			binary, err := dtuExecutablePath()
			if err != nil {
				return fmt.Errorf("resolve xylem executable: %w", err)
			}
			workDir := strings.TrimSpace(verificationWD)
			if workDir == "" {
				workDir = resolved.WorkDir
			}
			report, err := dtuRunLiveVerification(cmd.Context(), suite, registry, policy, dtu.VerificationRunOptions{
				StateDir:        resolved.StateDir,
				WorkDir:         workDir,
				XylemExecutable: binary,
				Environment:     os.Environ(),
				EnvLookup:       os.LookupEnv,
			})
			if err != nil {
				return err
			}

			printDTUVerificationReport(cmd, report)
			if report.HasFailures() {
				return fmt.Errorf("DTU verification failed: %d mismatch(es), %d drift alarm(s), %d error(s)", report.Summary.Mismatches, report.Summary.Drifts, report.Summary.Errors)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&suitePath, "suite", verificationDefaultAssetPath("live-verification.yaml"), "Live verification suite YAML path")
	cmd.Flags().StringVar(&registryPath, "divergence-registry", verificationDefaultAssetPath("divergence-registry.yaml"), "Divergence registry YAML path")
	cmd.Flags().StringVar(&policyPath, "attribution-policy", verificationDefaultAssetPath("attribution-policy.yaml"), "Attribution policy YAML path")
	cmd.Flags().StringVar(&verificationWD, "verify-workdir", "", "Working directory for live verification commands")
	return cmd
}

type resolvedDTUOptions struct {
	ManifestPath string
	UniverseID   string
	StateDir     string
	WorkDir      string
	ShimDir      string
	RuntimeDir   string
	Store        *dtu.Store
	State        *dtu.State
}

func resolveDTUOptions(opts *dtuOptions, manifestPath string) (*resolvedDTUOptions, error) {
	manifestPath = strings.TrimSpace(manifestPath)

	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		var err error
		stateDir, err = configuredStateDir(viper.GetString("config"))
		if err != nil {
			return nil, err
		}
	}
	if stateDir == "" {
		stateDir = dtuDefaultStateDir
	}
	stateDir, err := filepath.Abs(filepath.Clean(stateDir))
	if err != nil {
		return nil, fmt.Errorf("resolve state dir %q: %w", stateDir, err)
	}

	universeID := strings.TrimSpace(opts.UniverseID)
	var (
		absManifest string
		store       *dtu.Store
		state       *dtu.State
	)
	if manifestPath != "" {
		absManifest, err = filepath.Abs(filepath.Clean(manifestPath))
		if err != nil {
			return nil, fmt.Errorf("resolve manifest path %q: %w", manifestPath, err)
		}

		manifest, err := dtu.LoadManifest(absManifest)
		if err != nil {
			return nil, err
		}
		if universeID == "" {
			universeID = defaultUniverseID(manifest.Metadata.Name)
		}
		store, err = dtu.NewStore(stateDir, universeID)
		if err != nil {
			return nil, err
		}
		state, err = dtu.NewState(universeID, manifest, absManifest, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	} else {
		if universeID == "" {
			return nil, fmt.Errorf("manifest path or --universe is required")
		}
		store, err = dtu.NewStore(stateDir, universeID)
		if err != nil {
			return nil, err
		}
		state, err = store.Load()
		if err != nil {
			return nil, fmt.Errorf("load DTU state: %w", err)
		}
		absManifest = state.ManifestPath
	}

	runtimeDir := filepath.Join(stateDir, "dtu", universeID)
	workDir := strings.TrimSpace(opts.WorkDir)
	if workDir == "" {
		workDir = filepath.Join(runtimeDir, "workdir")
	}
	workDir, err = filepath.Abs(filepath.Clean(workDir))
	if err != nil {
		return nil, fmt.Errorf("resolve workdir %q: %w", workDir, err)
	}

	shimDir := strings.TrimSpace(opts.ShimDir)
	if shimDir == "" {
		shimDir = dtu.DefaultShimDir(stateDir)
	}
	shimDir, err = filepath.Abs(filepath.Clean(shimDir))
	if err != nil {
		return nil, fmt.Errorf("resolve shim dir %q: %w", shimDir, err)
	}

	return &resolvedDTUOptions{
		ManifestPath: absManifest,
		UniverseID:   universeID,
		StateDir:     stateDir,
		WorkDir:      workDir,
		ShimDir:      shimDir,
		RuntimeDir:   runtimeDir,
		Store:        store,
		State:        state,
	}, nil
}

func saveDTUState(resolved *resolvedDTUOptions) error {
	if err := resolved.Store.Save(resolved.State); err != nil {
		return fmt.Errorf("save DTU state: %w", err)
	}
	return nil
}

func materializeDTURuntime(resolved *resolvedDTUOptions) error {
	for _, dir := range []string{resolved.RuntimeDir, resolved.WorkDir, resolved.ShimDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create runtime dir %q: %w", dir, err)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve xylem executable: %w", err)
	}
	if err := installDTUShimWrappers(resolved.ShimDir, binary); err != nil {
		return fmt.Errorf("install DTU shim wrappers: %w", err)
	}
	return nil
}

func installDTUShimWrappers(shimDir, binary string) error {
	binary, err := filepath.Abs(filepath.Clean(binary))
	if err != nil {
		return fmt.Errorf("resolve xylem executable path %q: %w", binary, err)
	}
	for _, shim := range dtuShimNames {
		path := filepath.Join(shimDir, shimWrapperFilename(shim))
		content := shimWrapperContent(binary, shim)
		if err := os.WriteFile(path, []byte(content), shimWrapperMode()); err != nil {
			return fmt.Errorf("write shim wrapper %q: %w", path, err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(path, shimWrapperMode()); err != nil {
				return fmt.Errorf("chmod shim wrapper %q: %w", path, err)
			}
		}
	}
	return nil
}

func shimWrapperFilename(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".cmd"
	}
	return name
}

func shimWrapperContent(binary, shim string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("@echo off\r\n\"%s\" shim-dispatch %s %%*\r\n", escapeCmdLiteral(binary), shim)
	}
	return fmt.Sprintf("#!/bin/sh\nexec %s shim-dispatch %s \"$@\"\n", shellEscape(binary), shellEscape(shim))
}

func shimWrapperMode() os.FileMode {
	if runtime.GOOS == "windows" {
		return 0o644
	}
	return 0o755
}

func escapeCmdLiteral(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func (r *resolvedDTUOptions) env(pathPrefix string) []string {
	pathValue := os.Getenv("PATH")
	if strings.TrimSpace(pathPrefix) != "" {
		pathValue = prependPath(pathPrefix, pathValue)
	}
	return []string{
		dtu.EnvUniverseID + "=" + r.UniverseID,
		dtu.EnvStatePath + "=" + r.Store.Path(),
		dtu.EnvStateDir + "=" + r.StateDir,
		dtu.EnvManifestPath + "=" + r.ManifestPath,
		dtu.EnvEventLogPath + "=" + r.Store.EventLogPath(),
		dtu.EnvShimDir + "=" + r.ShimDir,
		"PATH=" + pathValue,
	}
}

func configuredStateDir(configPath string) (string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return "", nil
	}
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat config %q: %w", configPath, err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(cfg.StateDir), nil
}

func prependPath(prefix, pathValue string) string {
	prefix = strings.TrimSpace(prefix)
	pathValue = strings.TrimSpace(pathValue)
	if prefix == "" {
		return pathValue
	}
	if pathValue == "" {
		return prefix
	}
	return prefix + string(os.PathListSeparator) + pathValue
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func argsFirst(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

var invalidUniverseChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func defaultUniverseID(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.ReplaceAll(name, " ", "-")
	name = invalidUniverseChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		return "default"
	}
	return name
}

func shellEscape(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func verificationDefaultAssetPath(name string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return filepath.Join("cli", "internal", "dtu", "testdata", name)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "internal", "dtu", "testdata", name))
}

func verificationDefaultManifestPath() string {
	return verificationDefaultAssetPath("issue-label-gate.yaml")
}

func printDTUVerificationReport(cmd *cobra.Command, report *dtu.VerificationReport) {
	if report == nil {
		return
	}
	cmd.Printf("DTU verification summary: %d differential, %d canary, %d matched, %d passed, %d mismatches, %d drift, %d errors\n",
		report.Summary.DifferentialRun,
		report.Summary.CanariesRun,
		report.Summary.Matches,
		report.Summary.Passes,
		report.Summary.Mismatches,
		report.Summary.Drifts,
		report.Summary.Errors,
	)
	for _, section := range []struct {
		label string
		cases []dtu.VerificationCaseReport
	}{
		{label: "differential", cases: report.Differential},
		{label: "canary", cases: report.Canaries},
	} {
		for _, item := range section.cases {
			cmd.Printf("[%s] %s: %s\n", item.Status, item.Name, item.Message)
			if item.Live != nil && item.Live.Error != "" {
				cmd.Printf("  live error: %s\n", item.Live.Error)
			}
			if item.Twin != nil && item.Twin.Error != "" {
				cmd.Printf("  twin error: %s\n", item.Twin.Error)
			}
			if item.Status == dtu.VerificationStatusMismatch && item.Live != nil && item.Twin != nil {
				cmd.Printf("  live normalized: %s\n", item.Live.CanonicalJSON)
				cmd.Printf("  twin normalized: %s\n", item.Twin.CanonicalJSON)
			}
			if len(item.Divergences) > 0 {
				for _, divergence := range item.Divergences {
					cmd.Printf("  divergence: %s (%s) - %s\n", divergence.CommandOrCase, divergence.Status, divergence.Rationale)
				}
			}
			if item.AttributionRule != nil {
				cmd.Printf("  attribution: %s -> %s\n", item.AttributionRule.Classification, item.AttributionRule.NextStep)
			}
		}
	}
}
