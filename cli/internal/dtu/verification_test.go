package dtu

import (
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func verificationAssetPath(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

func TestLoadDivergenceRegistry(t *testing.T) {
	t.Parallel()

	registry, err := LoadDivergenceRegistry(verificationAssetPath(t, "divergence-registry.yaml"))
	if err != nil {
		t.Fatalf("LoadDivergenceRegistry() error = %v", err)
	}
	if registry.Version != formatVersion {
		t.Fatalf("Version = %q, want %q", registry.Version, formatVersion)
	}
	if len(registry.Divergences) != 3 {
		t.Fatalf("len(Divergences) = %d, want 3", len(registry.Divergences))
	}
	got := []Boundary{
		registry.Divergences[0].Boundary,
		registry.Divergences[1].Boundary,
		registry.Divergences[2].Boundary,
	}
	want := []Boundary{BoundaryGH, BoundaryGit, BoundaryClaude}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("divergence boundaries = %v, want %v", got, want)
	}
}

func TestLoadAttributionPolicy(t *testing.T) {
	t.Parallel()

	policy, err := LoadAttributionPolicy(verificationAssetPath(t, "attribution-policy.yaml"))
	if err != nil {
		t.Fatalf("LoadAttributionPolicy() error = %v", err)
	}
	if len(policy.Rules) != 4 {
		t.Fatalf("len(Rules) = %d, want 4", len(policy.Rules))
	}
	if policy.Rules[0].Classification != AttributionClassificationXylemBug {
		t.Fatalf("Rules[0].Classification = %q, want %q", policy.Rules[0].Classification, AttributionClassificationXylemBug)
	}
}

func TestLoadLiveVerificationSuite(t *testing.T) {
	t.Parallel()

	suite, err := LoadLiveVerificationSuite(verificationAssetPath(t, "live-verification.yaml"))
	if err != nil {
		t.Fatalf("LoadLiveVerificationSuite() error = %v", err)
	}
	if len(suite.Differential) != 6 {
		t.Fatalf("len(Differential) = %d, want 6", len(suite.Differential))
	}
	if len(suite.Canaries) != 4 {
		t.Fatalf("len(Canaries) = %d, want 4", len(suite.Canaries))
	}
}

func TestLiveVerificationSuiteEnabledCases(t *testing.T) {
	t.Parallel()

	suite, err := LoadLiveVerificationSuite(verificationAssetPath(t, "live-verification.yaml"))
	if err != nil {
		t.Fatalf("LoadLiveVerificationSuite() error = %v", err)
	}

	lookup := func(key string) (string, bool) {
		enabled := map[string]string{
			"XYLEM_DTU_LIVE_GH_DIFFERENTIAL":       "1",
			"XYLEM_DTU_LIVE_GIT_DIFFERENTIAL":      "1",
			"XYLEM_DTU_LIVE_PROVIDER_DIFFERENTIAL": "1",
			"XYLEM_DTU_LIVE_CANARY":                "true",
		}
		value, ok := enabled[key]
		return value, ok
	}

	differential := suite.EnabledDifferential(lookup)
	if len(differential) != 6 {
		t.Fatalf("len(EnabledDifferential) = %d, want 6", len(differential))
	}
	canaries := suite.EnabledCanaries(lookup)
	if len(canaries) != 4 {
		t.Fatalf("len(EnabledCanaries) = %d, want 4", len(canaries))
	}
}

func TestNormalizeVerificationResultGitLSRemoteHeads(t *testing.T) {
	t.Parallel()

	normalized, canonicalJSON, err := NormalizeVerificationResult("git_ls_remote_heads", VerificationCommandResult{
		Stdout: "cafebabe\trefs/heads/release\n" +
			"deadbeef\trefs/heads/main\n",
	})
	if err != nil {
		t.Fatalf("NormalizeVerificationResult() error = %v", err)
	}
	want := []string{"refs/heads/main", "refs/heads/release"}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized = %#v, want %#v", normalized, want)
	}
	if canonicalJSON != `["refs/heads/main","refs/heads/release"]` {
		t.Fatalf("canonicalJSON = %q, want sorted head refs", canonicalJSON)
	}
}

func TestNormalizeVerificationResultGitSymbolicRef(t *testing.T) {
	t.Parallel()

	normalized, canonicalJSON, err := NormalizeVerificationResult("git_symbolic_ref", VerificationCommandResult{
		Stdout: " refs/remotes/origin/main \n",
	})
	if err != nil {
		t.Fatalf("NormalizeVerificationResult() error = %v", err)
	}
	if normalized != "refs/remotes/origin/main" {
		t.Fatalf("normalized = %#v, want %q", normalized, "refs/remotes/origin/main")
	}
	if canonicalJSON != `"refs/remotes/origin/main"` {
		t.Fatalf("canonicalJSON = %q, want trimmed symbolic ref", canonicalJSON)
	}
}

func TestNormalizeVerificationResultProviderProcessShape(t *testing.T) {
	t.Parallel()

	normalized, canonicalJSON, err := NormalizeVerificationResult("provider_process_shape", VerificationCommandResult{
		Stdout:   "ok\n",
		Stderr:   "",
		ExitCode: 0,
	})
	if err != nil {
		t.Fatalf("NormalizeVerificationResult() error = %v", err)
	}
	want := normalizedProviderProcessShape{
		HasStdout: true,
		HasStderr: false,
		ExitCode:  0,
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized = %#v, want %#v", normalized, want)
	}
	if canonicalJSON != `{"has_stdout":true,"has_stderr":false,"exit_code":0}` {
		t.Fatalf("canonicalJSON = %q, want provider shape JSON", canonicalJSON)
	}
}

func TestLiveVerificationProviderFixturesMatchDifferentials(t *testing.T) {
	t.Parallel()

	const fixedNow = "2026-01-02T03:04:05Z"
	testCases := []struct {
		name     string
		fixture  string
		inv      ProviderInvocation
		wantName string
	}{
		{
			name:    "copilot verification fixture",
			fixture: "github-pr-copilot.yaml",
			inv: ProviderInvocation{
				Provider:     ProviderCopilot,
				Prompt:       "HARNESS HEADER\n\nReview this change",
				AllowedTools: []string{"Read,Write"},
			},
			wantName: "live-verification-review",
		},
		{
			name:    "claude verification fixture",
			fixture: "issue-happy-path.yaml",
			inv: ProviderInvocation{
				Provider: ProviderClaude,
				Prompt:   "smoke test",
			},
			wantName: "live-verification-smoke",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manifestPath := verificationAssetPath(t, tc.fixture)
			manifest, err := LoadManifest(manifestPath)
			if err != nil {
				t.Fatalf("LoadManifest(%q) error = %v", manifestPath, err)
			}
			state, err := NewState("verification-fixture", manifest, manifestPath, mustParseVerificationTime(t, fixedNow))
			if err != nil {
				t.Fatalf("NewState(%q) error = %v", manifest.Metadata.Name, err)
			}

			script, err := state.SelectProviderScript(tc.inv)
			if err != nil {
				t.Fatalf("SelectProviderScript() error = %v", err)
			}
			if script.Name != tc.wantName {
				t.Fatalf("script.Name = %q, want %q", script.Name, tc.wantName)
			}
		})
	}
}

func mustParseVerificationTime(t *testing.T, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", value, err)
	}
	return parsed
}
