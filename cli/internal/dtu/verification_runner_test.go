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

func reportByName(t *testing.T, reports []VerificationCaseReport, name string) VerificationCaseReport {
	t.Helper()

	for _, report := range reports {
		if report.Name == name {
			return report
		}
	}
	t.Fatalf("verification report %q not found in %#v", name, reports)
	return VerificationCaseReport{}
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
	first := reportByName(t, report.Differential, "gh-search-issues-open-bug")
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

func TestRunLiveVerificationGitAndProviderDifferentialsMatch(t *testing.T) {
	t.Parallel()

	suite, err := LoadLiveVerificationSuite(verificationAssetPath(t, "live-verification.yaml"))
	if err != nil {
		t.Fatalf("LoadLiveVerificationSuite() error = %v", err)
	}

	report, err := RunLiveVerification(context.Background(), suite, nil, nil, VerificationRunOptions{
		StateDir:        t.TempDir(),
		WorkDir:         t.TempDir(),
		XylemExecutable: verificationBinaryPath(t),
		Environment:     os.Environ(),
		EnvLookup: func(key string) (string, bool) {
			switch key {
			case "XYLEM_DTU_LIVE_GIT_DIFFERENTIAL", "XYLEM_DTU_LIVE_PROVIDER_DIFFERENTIAL":
				return "1", true
			default:
				return "", false
			}
		},
		Runner: stubVerificationRunner{results: map[string]stubVerificationResult{
			key("git", "ls-remote", "--heads", "origin", "main"): {
				result: VerificationCommandResult{Stdout: "deadbeef\trefs/heads/main\n"},
			},
			key(verificationBinaryPath(t), "shim-dispatch", "git", "ls-remote", "--heads", "origin", "main"): {
				result: VerificationCommandResult{Stdout: "cafebabe\trefs/heads/main\n"},
			},
			key("copilot", "-p", "HARNESS HEADER\n\nReview this change", "-s", "--available-tools", "Read,Write", "--allow-all-tools"): {
				result: VerificationCommandResult{Stdout: "live copilot response\n"},
			},
			key(verificationBinaryPath(t), "shim-dispatch", "copilot", "-p", "HARNESS HEADER\n\nReview this change", "-s", "--available-tools", "Read,Write", "--allow-all-tools"): {
				result: VerificationCommandResult{Stdout: "fixture copilot response\n"},
			},
			key("claude", "-p", "smoke test", "--max-turns", "1"): {
				result: VerificationCommandResult{Stdout: "live claude response\n"},
			},
			key(verificationBinaryPath(t), "shim-dispatch", "claude", "-p", "smoke test", "--max-turns", "1"): {
				result: VerificationCommandResult{Stdout: "fixture claude response\n"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("RunLiveVerification() error = %v", err)
	}
	if report.Summary.DifferentialRun != 3 {
		t.Fatalf("DifferentialRun = %d, want 3", report.Summary.DifferentialRun)
	}
	if report.Summary.Matches != 3 {
		t.Fatalf("Matches = %d, want 3", report.Summary.Matches)
	}

	gitCase := reportByName(t, report.Differential, "git-ls-remote-main")
	if gitCase.Status != VerificationStatusMatched {
		t.Fatalf("git Status = %q, want %q", gitCase.Status, VerificationStatusMatched)
	}
	if gitCase.Live == nil || gitCase.Twin == nil {
		t.Fatalf("git case missing executions: %#v", gitCase)
	}
	if gitCase.Live.CanonicalJSON != `["refs/heads/main"]` || gitCase.Twin.CanonicalJSON != `["refs/heads/main"]` {
		t.Fatalf("git canonical JSON = live %q twin %q, want main ref only", gitCase.Live.CanonicalJSON, gitCase.Twin.CanonicalJSON)
	}

	copilotCase := reportByName(t, report.Differential, "provider-headless-shape")
	if copilotCase.Status != VerificationStatusMatched {
		t.Fatalf("copilot Status = %q, want %q", copilotCase.Status, VerificationStatusMatched)
	}
	if copilotCase.Live == nil || copilotCase.Twin == nil || copilotCase.Live.CanonicalJSON != copilotCase.Twin.CanonicalJSON {
		t.Fatalf("copilot canonical JSON mismatch: live=%#v twin=%#v", copilotCase.Live, copilotCase.Twin)
	}

	claudeCase := reportByName(t, report.Differential, "claude-headless-shape")
	if claudeCase.Status != VerificationStatusMatched {
		t.Fatalf("claude Status = %q, want %q", claudeCase.Status, VerificationStatusMatched)
	}
	if claudeCase.Live == nil || claudeCase.Twin == nil || claudeCase.Live.CanonicalJSON != claudeCase.Twin.CanonicalJSON {
		t.Fatalf("claude canonical JSON mismatch: live=%#v twin=%#v", claudeCase.Live, claudeCase.Twin)
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
			key("git", "symbolic-ref", "refs/remotes/origin/HEAD"): {
				result: VerificationCommandResult{Stdout: "refs/remotes/origin/main\n"},
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
	if report.Summary.CanariesRun != 4 {
		t.Fatalf("CanariesRun = %d, want 4", report.Summary.CanariesRun)
	}
	if report.Summary.Drifts != 1 {
		t.Fatalf("Drifts = %d, want 1", report.Summary.Drifts)
	}
	claudeCanary := reportByName(t, report.Canaries, "claude-headless-smoke")
	if claudeCanary.Status != VerificationStatusDrift {
		t.Fatalf("Status = %q, want %q", claudeCanary.Status, VerificationStatusDrift)
	}
	if len(claudeCanary.Divergences) != 1 {
		t.Fatalf("claude canary divergences = %v, want 1", claudeCanary.Divergences)
	}
	if claudeCanary.AttributionRule == nil || claudeCanary.AttributionRule.Classification != AttributionClassificationMissingFidelity {
		t.Fatalf("AttributionRule = %#v, want missing_fidelity", claudeCanary.AttributionRule)
	}

	gitCanary := reportByName(t, report.Canaries, "git-origin-head-symbolic-ref")
	if gitCanary.Status != VerificationStatusPassed {
		t.Fatalf("git canary Status = %q, want %q", gitCanary.Status, VerificationStatusPassed)
	}
	if gitCanary.Live == nil || gitCanary.Live.CanonicalJSON != `"refs/remotes/origin/main"` {
		t.Fatalf("git canary canonical JSON = %#v, want origin/main ref", gitCanary.Live)
	}
}

func TestSanitizedLiveEnvironmentRemovesDTUKeysAndShimDirFromPath(t *testing.T) {
	t.Parallel()

	env := []string{
		"PATH=/shim/bin" + string(os.PathListSeparator) + "/usr/bin",
		EnvShimDir + "=/shim/bin",
		EnvStateDir + "=/tmp/state",
		EnvWorkDir + "=/workdir",
		"HOME=/Users/test",
	}
	got := sanitizedLiveEnvironment(env)
	if _, ok := envValue(got, EnvShimDir); ok {
		t.Fatal("expected shim dir env to be removed")
	}
	if _, ok := envValue(got, EnvStateDir); ok {
		t.Fatal("expected DTU state env to be removed")
	}
	if _, ok := envValue(got, EnvWorkDir); ok {
		t.Fatal("expected DTU workdir env to be removed")
	}
	pathValue, ok := envValue(got, "PATH")
	if !ok {
		t.Fatal("expected PATH to remain")
	}
	if strings.Contains(pathValue, "/shim/bin") {
		t.Fatalf("PATH = %q, expected shim dir removed", pathValue)
	}
}
