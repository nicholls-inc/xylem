package dtu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// VerificationCaseKind identifies whether a verification case is differential
// or a live-only canary.
type VerificationCaseKind string

const (
	VerificationCaseKindDifferential VerificationCaseKind = "differential"
	VerificationCaseKindCanary       VerificationCaseKind = "canary"
)

// VerificationStatus captures the outcome of a verification case.
type VerificationStatus string

const (
	VerificationStatusMatched  VerificationStatus = "matched"
	VerificationStatusPassed   VerificationStatus = "passed"
	VerificationStatusMismatch VerificationStatus = "mismatch"
	VerificationStatusDrift    VerificationStatus = "drift"
	VerificationStatusError    VerificationStatus = "error"
)

// VerificationInvocation describes one command execution performed during DTU
// verification.
type VerificationInvocation struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
}

// VerificationCommandRunner executes a verification invocation and returns the
// observed process result.
type VerificationCommandRunner interface {
	Run(context.Context, VerificationInvocation) (VerificationCommandResult, error)
}

// VerificationExecution captures one live or twin command execution.
type VerificationExecution struct {
	Command       string                    `json:"command"`
	Args          []string                  `json:"args,omitempty"`
	Result        VerificationCommandResult `json:"result"`
	Normalized    any                       `json:"normalized,omitempty"`
	CanonicalJSON string                    `json:"canonical_json,omitempty"`
	Error         string                    `json:"error,omitempty"`
}

// VerificationCaseReport captures the outcome for one enabled verification case.
type VerificationCaseReport struct {
	Kind            VerificationCaseKind   `json:"kind"`
	Name            string                 `json:"name"`
	Boundary        Boundary               `json:"boundary"`
	EnabledEnv      string                 `json:"enabled_env"`
	Command         string                 `json:"command"`
	Args            []string               `json:"args,omitempty"`
	Normalizer      string                 `json:"normalizer,omitempty"`
	Fixture         string                 `json:"fixture,omitempty"`
	Status          VerificationStatus     `json:"status"`
	Message         string                 `json:"message,omitempty"`
	Live            *VerificationExecution `json:"live,omitempty"`
	Twin            *VerificationExecution `json:"twin,omitempty"`
	Divergences     []Divergence           `json:"divergences,omitempty"`
	AttributionRule *AttributionRule       `json:"attribution_rule,omitempty"`
}

// VerificationSummary captures aggregate verification counts.
type VerificationSummary struct {
	DifferentialRun int `json:"differential_run"`
	CanariesRun     int `json:"canaries_run"`
	Matches         int `json:"matches"`
	Passes          int `json:"passes"`
	Mismatches      int `json:"mismatches"`
	Drifts          int `json:"drifts"`
	Errors          int `json:"errors"`
}

// VerificationReport is the aggregate result of running a live verification suite.
type VerificationReport struct {
	SuiteVersion string                   `json:"suite_version,omitempty"`
	Differential []VerificationCaseReport `json:"differential,omitempty"`
	Canaries     []VerificationCaseReport `json:"canaries,omitempty"`
	Summary      VerificationSummary      `json:"summary"`
}

// HasFailures reports whether the verification run observed any mismatch, drift,
// or execution/normalization error.
func (r *VerificationReport) HasFailures() bool {
	if r == nil {
		return false
	}
	return r.Summary.Mismatches > 0 || r.Summary.Drifts > 0 || r.Summary.Errors > 0
}

// VerificationRunOptions configures executable DTU verification.
type VerificationRunOptions struct {
	StateDir        string
	WorkDir         string
	XylemExecutable string
	Environment     []string
	EnvLookup       func(string) (string, bool)
	Runner          VerificationCommandRunner
	Now             func() time.Time
}

// RunLiveVerification executes the enabled differential and canary cases in a
// live verification suite.
func RunLiveVerification(ctx context.Context, suite *LiveVerificationSuite, registry *DivergenceRegistry, policy *AttributionPolicy, opts VerificationRunOptions) (*VerificationReport, error) {
	if suite == nil {
		return nil, fmt.Errorf("live verification suite must not be nil")
	}
	if strings.TrimSpace(opts.StateDir) == "" {
		return nil, fmt.Errorf("verification state dir is required")
	}
	if opts.Runner == nil {
		opts.Runner = execVerificationRunner{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.EnvLookup == nil {
		opts.EnvLookup = os.LookupEnv
	}
	if len(opts.Environment) == 0 {
		opts.Environment = os.Environ()
	}
	if len(suite.EnabledDifferential(opts.EnvLookup)) > 0 && strings.TrimSpace(opts.XylemExecutable) == "" {
		return nil, fmt.Errorf("xylem executable path is required for differential verification")
	}

	report := &VerificationReport{SuiteVersion: suite.Version}
	for _, verificationCase := range suite.EnabledDifferential(opts.EnvLookup) {
		caseReport := runDifferentialVerificationCase(ctx, suite, verificationCase, registry, policy, opts)
		report.Differential = append(report.Differential, caseReport)
		report.Summary.DifferentialRun++
		tallyVerificationCase(&report.Summary, caseReport.Status)
	}
	for _, verificationCase := range suite.EnabledCanaries(opts.EnvLookup) {
		caseReport := runCanaryVerificationCase(ctx, verificationCase, registry, policy, opts)
		report.Canaries = append(report.Canaries, caseReport)
		report.Summary.CanariesRun++
		tallyVerificationCase(&report.Summary, caseReport.Status)
	}
	return report, nil
}

func runDifferentialVerificationCase(ctx context.Context, suite *LiveVerificationSuite, verificationCase LiveVerificationCase, registry *DivergenceRegistry, policy *AttributionPolicy, opts VerificationRunOptions) VerificationCaseReport {
	report := newVerificationCaseReport(VerificationCaseKindDifferential, verificationCase)

	liveExec := executeVerificationCommand(ctx, opts.Runner, VerificationInvocation{
		Command: verificationCase.Command,
		Args:    append([]string(nil), verificationCase.Args...),
		Dir:     opts.WorkDir,
		Env:     sanitizedLiveEnvironment(opts.Environment),
	})
	report.Live = &liveExec

	fixturePath, err := suite.ResolveFixturePath(verificationCase.Fixture)
	if err != nil {
		report.Status = VerificationStatusError
		report.Message = err.Error()
		report.Divergences = relatedDivergences(registry, VerificationCaseKindDifferential, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationTwinBug)
		return report
	}
	report.Fixture = fixturePath

	twinExec, err := executeTwinVerificationCase(ctx, verificationCase, fixturePath, opts)
	if err != nil {
		report.Status = VerificationStatusError
		report.Message = err.Error()
		report.Twin = &VerificationExecution{
			Command: opts.XylemExecutable,
			Args:    []string{"shim-dispatch", verificationCase.Command},
			Error:   err.Error(),
		}
		report.Divergences = relatedDivergences(registry, VerificationCaseKindDifferential, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationTwinBug)
		return report
	}
	report.Twin = &twinExec

	if report.Live.Error != "" {
		report.Status = VerificationStatusError
		report.Message = report.Live.Error
		report.Divergences = relatedDivergences(registry, VerificationCaseKindDifferential, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationMissingFidelity)
		return report
	}
	if report.Twin.Error != "" {
		report.Status = VerificationStatusError
		report.Message = report.Twin.Error
		report.Divergences = relatedDivergences(registry, VerificationCaseKindDifferential, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationTwinBug)
		return report
	}

	liveNormalized, liveJSON, err := NormalizeVerificationResult(verificationCase.Normalizer, report.Live.Result)
	if err != nil {
		report.Status = VerificationStatusError
		report.Message = fmt.Sprintf("normalize live result: %v", err)
		report.Live.Error = report.Message
		report.Divergences = relatedDivergences(registry, VerificationCaseKindDifferential, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationMissingFidelity)
		return report
	}
	report.Live.Normalized = liveNormalized
	report.Live.CanonicalJSON = liveJSON

	twinNormalized, twinJSON, err := NormalizeVerificationResult(verificationCase.Normalizer, report.Twin.Result)
	if err != nil {
		report.Status = VerificationStatusError
		report.Message = fmt.Sprintf("normalize twin result: %v", err)
		report.Twin.Error = report.Message
		report.Divergences = relatedDivergences(registry, VerificationCaseKindDifferential, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationTwinBug)
		return report
	}
	report.Twin.Normalized = twinNormalized
	report.Twin.CanonicalJSON = twinJSON

	if liveJSON != twinJSON {
		report.Status = VerificationStatusMismatch
		report.Message = "live and twin normalized outputs differ"
		report.Divergences = relatedDivergences(registry, VerificationCaseKindDifferential, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationFidelityBug)
		return report
	}

	report.Status = VerificationStatusMatched
	if report.Live.Result.ExitCode == report.Twin.Result.ExitCode {
		report.Message = fmt.Sprintf("matched normalized output with exit code %d", report.Live.Result.ExitCode)
	} else {
		report.Message = fmt.Sprintf("matched normalized output despite raw exit-code difference live=%d twin=%d", report.Live.Result.ExitCode, report.Twin.Result.ExitCode)
	}
	return report
}

func runCanaryVerificationCase(ctx context.Context, verificationCase LiveVerificationCase, registry *DivergenceRegistry, policy *AttributionPolicy, opts VerificationRunOptions) VerificationCaseReport {
	report := newVerificationCaseReport(VerificationCaseKindCanary, verificationCase)

	liveExec := executeVerificationCommand(ctx, opts.Runner, VerificationInvocation{
		Command: verificationCase.Command,
		Args:    append([]string(nil), verificationCase.Args...),
		Dir:     opts.WorkDir,
		Env:     sanitizedLiveEnvironment(opts.Environment),
	})
	report.Live = &liveExec

	if report.Live.Error != "" {
		report.Status = VerificationStatusError
		report.Message = report.Live.Error
		report.Divergences = relatedDivergences(registry, VerificationCaseKindCanary, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationMissingFidelity)
		return report
	}

	if strings.TrimSpace(verificationCase.Normalizer) != "" {
		normalized, canonicalJSON, err := NormalizeVerificationResult(verificationCase.Normalizer, report.Live.Result)
		if err != nil {
			report.Status = VerificationStatusError
			report.Message = fmt.Sprintf("normalize live result: %v", err)
			report.Live.Error = report.Message
			report.Divergences = relatedDivergences(registry, VerificationCaseKindCanary, verificationCase)
			report.AttributionRule = selectAttributionRule(policy, AttributionClassificationMissingFidelity)
			return report
		}
		report.Live.Normalized = normalized
		report.Live.CanonicalJSON = canonicalJSON
	}

	if report.Live.Result.ExitCode != 0 {
		report.Status = VerificationStatusDrift
		report.Message = fmt.Sprintf("live canary exited with code %d", report.Live.Result.ExitCode)
		report.Divergences = relatedDivergences(registry, VerificationCaseKindCanary, verificationCase)
		report.AttributionRule = selectAttributionRule(policy, AttributionClassificationMissingFidelity)
		return report
	}

	report.Status = VerificationStatusPassed
	report.Message = "live canary passed"
	return report
}

func newVerificationCaseReport(kind VerificationCaseKind, verificationCase LiveVerificationCase) VerificationCaseReport {
	return VerificationCaseReport{
		Kind:       kind,
		Name:       verificationCase.Name,
		Boundary:   verificationCase.Boundary,
		EnabledEnv: verificationCase.EnabledEnv,
		Command:    verificationCase.Command,
		Args:       append([]string(nil), verificationCase.Args...),
		Normalizer: verificationCase.Normalizer,
		Fixture:    verificationCase.Fixture,
	}
}

func executeTwinVerificationCase(ctx context.Context, verificationCase LiveVerificationCase, fixturePath string, opts VerificationRunOptions) (VerificationExecution, error) {
	manifest, err := LoadManifest(fixturePath)
	if err != nil {
		return VerificationExecution{}, fmt.Errorf("load DTU fixture %q: %w", fixturePath, err)
	}
	universeID := verificationUniverseID(verificationCase.Name)
	store, err := NewStore(opts.StateDir, universeID)
	if err != nil {
		return VerificationExecution{}, fmt.Errorf("create DTU verification store: %w", err)
	}
	state, err := NewState(universeID, manifest, fixturePath, opts.Now().UTC())
	if err != nil {
		return VerificationExecution{}, fmt.Errorf("create DTU verification state: %w", err)
	}
	if err := store.Save(state); err != nil {
		return VerificationExecution{}, fmt.Errorf("save DTU verification state: %w", err)
	}

	env := append(sanitizedLiveEnvironment(opts.Environment),
		EnvUniverseID+"="+universeID,
		EnvStatePath+"="+store.Path(),
		EnvStateDir+"="+opts.StateDir,
		EnvManifestPath+"="+fixturePath,
		EnvEventLogPath+"="+store.EventLogPath(),
		EnvWorkDir+"="+opts.WorkDir,
	)
	return executeVerificationCommand(ctx, opts.Runner, VerificationInvocation{
		Command: opts.XylemExecutable,
		Args:    append([]string{"shim-dispatch", verificationCase.Command}, verificationCase.Args...),
		Dir:     opts.WorkDir,
		Env:     env,
	}), nil
}

func executeVerificationCommand(ctx context.Context, runner VerificationCommandRunner, invocation VerificationInvocation) VerificationExecution {
	result, err := runner.Run(ctx, invocation)
	execution := VerificationExecution{
		Command: invocation.Command,
		Args:    append([]string(nil), invocation.Args...),
		Result:  result,
	}
	if err != nil {
		execution.Error = err.Error()
	}
	return execution
}

func relatedDivergences(registry *DivergenceRegistry, kind VerificationCaseKind, verificationCase LiveVerificationCase) []Divergence {
	if registry == nil || len(registry.Divergences) == 0 {
		return nil
	}
	commandLine := renderVerificationCommand(verificationCase.Command, verificationCase.Args)
	liveReference := string(kind) + ":" + verificationCase.Name
	var matches []Divergence
	for _, divergence := range registry.Divergences {
		if divergence.Boundary != verificationCase.Boundary {
			continue
		}
		if strings.TrimSpace(divergence.CommandOrCase) == verificationCase.Name ||
			strings.TrimSpace(divergence.CommandOrCase) == commandLine ||
			strings.TrimSpace(divergence.LiveReference) == liveReference {
			matches = append(matches, divergence)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	return matches
}

func selectAttributionRule(policy *AttributionPolicy, classification AttributionClassification) *AttributionRule {
	if policy == nil {
		return nil
	}
	for i := range policy.Rules {
		if policy.Rules[i].Classification == classification {
			rule := policy.Rules[i]
			return &rule
		}
	}
	return nil
}

func tallyVerificationCase(summary *VerificationSummary, status VerificationStatus) {
	switch status {
	case VerificationStatusMatched:
		summary.Matches++
	case VerificationStatusPassed:
		summary.Passes++
	case VerificationStatusMismatch:
		summary.Mismatches++
	case VerificationStatusDrift:
		summary.Drifts++
	case VerificationStatusError:
		summary.Errors++
	}
}

type execVerificationRunner struct{}

func (execVerificationRunner) Run(ctx context.Context, invocation VerificationInvocation) (VerificationCommandResult, error) {
	cmd := exec.CommandContext(ctx, invocation.Command, invocation.Args...)
	cmd.Dir = invocation.Dir
	if len(invocation.Env) > 0 {
		cmd.Env = append([]string(nil), invocation.Env...)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := VerificationCommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("run verification command %q: %w", renderVerificationCommand(invocation.Command, invocation.Args), err)
}

func sanitizedLiveEnvironment(env []string) []string {
	if len(env) == 0 {
		return nil
	}
	dtuKeys := map[string]struct{}{
		EnvUniverseID:   {},
		EnvStatePath:    {},
		EnvStateDir:     {},
		EnvManifestPath: {},
		EnvEventLogPath: {},
		EnvWorkDir:      {},
		EnvShimDir:      {},
		EnvPhase:        {},
		EnvScript:       {},
		EnvAttempt:      {},
		EnvFault:        {},
	}
	shimDir := ""
	if value, ok := envValue(env, EnvShimDir); ok {
		shimDir = strings.TrimSpace(value)
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, blocked := dtuKeys[key]; blocked {
			continue
		}
		if key == "PATH" && shimDir != "" {
			value = stripPathEntry(value, shimDir)
		}
		out = append(out, key+"="+value)
	}
	return out
}

func envValue(env []string, key string) (string, bool) {
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && k == key {
			return v, true
		}
	}
	return "", false
}

func stripPathEntry(pathValue, blocked string) string {
	if strings.TrimSpace(blocked) == "" {
		return pathValue
	}
	parts := strings.Split(pathValue, string(os.PathListSeparator))
	filtered := parts[:0]
	for _, part := range parts {
		if filepath.Clean(part) == filepath.Clean(blocked) {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, string(os.PathListSeparator))
}

func renderVerificationCommand(command string, args []string) string {
	if len(args) == 0 {
		return strings.TrimSpace(command)
	}
	return strings.TrimSpace(command + " " + strings.Join(args, " "))
}

var verificationUniverseIDPattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func verificationUniverseID(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.ReplaceAll(name, " ", "-")
	name = verificationUniverseIDPattern.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		name = "default"
	}
	return "verify-" + name
}
