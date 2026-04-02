package dtu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type stubVerificationRunner struct {
	results map[string]stubVerificationResult
}

type stubVerificationResult struct {
	result VerificationCommandResult
	err    error
}

func (s stubVerificationRunner) Run(_ context.Context, invocation VerificationInvocation) (VerificationCommandResult, error) {
	key := invocation.Command + "\x00" + strings.Join(invocation.Args, "\x00")
	result, ok := s.results[key]
	if !ok {
		return VerificationCommandResult{}, fmt.Errorf("unexpected invocation %s %v", invocation.Command, invocation.Args)
	}
	return result.result, result.err
}

func key(command string, args ...string) string {
	return command + "\x00" + strings.Join(args, "\x00")
}

func verificationBinaryPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "cmd", "xylem"))
}

func TestRunLiveVerificationDifferentialMismatchUsesRegistryAndPolicy(t *testing.T) {
	t.Parallel()

	suite, err := LoadLiveVerificationSuite(verificationAssetPath(t, "live-verification.yaml"))
	if err != nil {
		t.Fatalf("LoadLiveVerificationSuite() error = %v", err)
	}
	registry, err := LoadDivergenceRegistry(verificationAssetPath(t, "divergence-registry.yaml"))
	if err != nil {
		t.Fatalf("LoadDivergenceRegistry() error = %v", err)
	}
	policy, err := LoadAttributionPolicy(verificationAssetPath(t, "attribution-policy.yaml"))
	if err != nil {
		t.Fatalf("LoadAttributionPolicy() error = %v", err)
	}

	stateDir := t.TempDir()
	workDir := t.TempDir()
	report, err := RunLiveVerification(context.Background(), suite, registry, policy, VerificationRunOptions{
		StateDir:        stateDir,
		WorkDir:         workDir,
		XylemExecutable: verificationBinaryPath(t),
		Environment:     os.Environ(),
		EnvLookup: func(key string) (string, bool) {
			switch key {
			case "XYLEM_DTU_LIVE_GH_DIFFERENTIAL":
				return "1", true
			default:
				return "", false
			}
		},
		Runner: stubVerificationRunner{results: map[string]stubVerificationResult{
			key("gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number", "title", "body", "url", "labels", "--limit", "20", "--label", "bug"): {
				result: VerificationCommandResult{Stdout: `[{"number":1,"title":"flaky gate","body":"wait for plan approval","url":"https://example.test/issues/1","labels":[{"name":"bug"}]}]`},
			},
			key(verificationBinaryPath(t), "shim-dispatch", "gh", "search", "issues", "--repo", "owner/repo", "--state", "open", "--json", "number", "title", "body", "url", "labels", "--limit", "20", "--label", "bug"): {
				result: VerificationCommandResult{Stdout: `[{"number":2,"title":"flaky gate","body":"wait for plan approval","url":"https://example.test/issues/1","labels":[{"name":"bug"}]}]`},
			},
			key("gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number", "title", "body", "url", "labels", "headRefName", "--limit", "20"): {
				result: VerificationCommandResult{Stdout: `[]`},
			},
			key(verificationBinaryPath(t), "shim-dispatch", "gh", "pr", "list", "--repo", "owner/repo", "--state", "open", "--json", "number", "title", "body", "url", "labels", "headRefName", "--limit", "20"): {
				result: VerificationCommandResult{Stdout: `[]`},
			},
			key("gh", "issue", "view", "1", "--repo", "owner/repo", "--json", "labels"): {
				result: VerificationCommandResult{Stdout: `{"labels":[{"name":"bug"}]}`},
			},
			key(verificationBinaryPath(t), "shim-dispatch", "gh", "issue", "view", "1", "--repo", "owner/repo", "--json", "labels"): {
				result: VerificationCommandResult{Stdout: `{"labels":[{"name":"bug"}]}`},
			},
		}},
		Now: func() time.Time {
			return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("RunLiveVerification() error = %v", err)
	}
	if report.Summary.DifferentialRun != 3 {
		t.Fatalf("DifferentialRun = %d, want 3", report.Summary.DifferentialRun)
	}
	if report.Summary.Mismatches != 1 {
		t.Fatalf("Mismatches = %d, want 1", report.Summary.Mismatches)
	}
	first := report.Differential[0]
	if first.Status != VerificationStatusMismatch {
		t.Fatalf("Status = %q, want %q", first.Status, VerificationStatusMismatch)
	}
	if first.AttributionRule == nil || first.AttributionRule.Classification != AttributionClassificationFidelityBug {
		t.Fatalf("AttributionRule = %#v, want fidelity_bug", first.AttributionRule)
	}
	if len(first.Divergences) != 0 {
		t.Fatalf("Divergences = %v, want none for unmatched registry entry", first.Divergences)
	}
	if first.Live == nil || first.Twin == nil || first.Live.CanonicalJSON == first.Twin.CanonicalJSON {
		t.Fatalf("expected differing normalized outputs, got live=%#v twin=%#v", first.Live, first.Twin)
	}
}

func TestRunLiveVerificationCanaryDriftUsesRegistry(t *testing.T) {
	t.Parallel()

	suite, err := LoadLiveVerificationSuite(verificationAssetPath(t, "live-verification.yaml"))
	if err != nil {
		t.Fatalf("LoadLiveVerificationSuite() error = %v", err)
	}
	registry, err := LoadDivergenceRegistry(verificationAssetPath(t, "divergence-registry.yaml"))
	if err != nil {
		t.Fatalf("LoadDivergenceRegistry() error = %v", err)
	}
	policy, err := LoadAttributionPolicy(verificationAssetPath(t, "attribution-policy.yaml"))
	if err != nil {
		t.Fatalf("LoadAttributionPolicy() error = %v", err)
	}

	report, err := RunLiveVerification(context.Background(), suite, registry, policy, VerificationRunOptions{
		StateDir:        t.TempDir(),
		WorkDir:         t.TempDir(),
		XylemExecutable: verificationBinaryPath(t),
		Environment:     os.Environ(),
		EnvLookup: func(key string) (string, bool) {
			if key == "XYLEM_DTU_LIVE_CANARY" {
				return "1", true
			}
			return "", false
		},
		Runner: stubVerificationRunner{results: map[string]stubVerificationResult{
			key("gh", "repo", "view", "--json", "defaultBranchRef"): {
				result: VerificationCommandResult{Stdout: `{"defaultBranchRef":{"name":"main"}}`},
			},
			key("claude", "-p", "smoke test", "--max-turns", "1"): {
				result: VerificationCommandResult{Stderr: "provider offline", ExitCode: 1},
			},
			key("copilot", "-p", "smoke test", "-s"): {
				result: VerificationCommandResult{Stdout: "ok"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("RunLiveVerification() error = %v", err)
	}
	if report.Summary.CanariesRun != 3 {
		t.Fatalf("CanariesRun = %d, want 3", report.Summary.CanariesRun)
	}
	if report.Summary.Drifts != 1 {
		t.Fatalf("Drifts = %d, want 1", report.Summary.Drifts)
	}
	if report.Canaries[1].Status != VerificationStatusDrift {
		t.Fatalf("Status = %q, want %q", report.Canaries[1].Status, VerificationStatusDrift)
	}
	if len(report.Canaries[1].Divergences) != 1 {
		t.Fatalf("claude canary divergences = %v, want 1", report.Canaries[1].Divergences)
	}
	if report.Canaries[1].AttributionRule == nil || report.Canaries[1].AttributionRule.Classification != AttributionClassificationMissingFidelity {
		t.Fatalf("AttributionRule = %#v, want missing_fidelity", report.Canaries[1].AttributionRule)
	}
}

func TestSanitizedLiveEnvironmentRemovesDTUKeysAndShimDirFromPath(t *testing.T) {
	t.Parallel()

	env := []string{
		"PATH=/shim/bin" + string(os.PathListSeparator) + "/usr/bin",
		EnvShimDir + "=/shim/bin",
		EnvStateDir + "=/tmp/state",
		"HOME=/Users/test",
	}
	got := sanitizedLiveEnvironment(env)
	if _, ok := envValue(got, EnvShimDir); ok {
		t.Fatal("expected shim dir env to be removed")
	}
	if _, ok := envValue(got, EnvStateDir); ok {
		t.Fatal("expected DTU state env to be removed")
	}
	pathValue, ok := envValue(got, "PATH")
	if !ok {
		t.Fatal("expected PATH to remain")
	}
	if strings.Contains(pathValue, "/shim/bin") {
		t.Fatalf("PATH = %q, expected shim dir removed", pathValue)
	}
}
