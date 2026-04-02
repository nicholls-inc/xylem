package dtu

import (
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
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
	if len(suite.Differential) != 4 {
		t.Fatalf("len(Differential) = %d, want 4", len(suite.Differential))
	}
	if len(suite.Canaries) != 3 {
		t.Fatalf("len(Canaries) = %d, want 3", len(suite.Canaries))
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
			"XYLEM_DTU_LIVE_PROVIDER_DIFFERENTIAL": "",
			"XYLEM_DTU_LIVE_CANARY":                "true",
		}
		value, ok := enabled[key]
		return value, ok
	}

	differential := suite.EnabledDifferential(lookup)
	if len(differential) != 3 {
		t.Fatalf("len(EnabledDifferential) = %d, want 3", len(differential))
	}
	canaries := suite.EnabledCanaries(lookup)
	if len(canaries) != 3 {
		t.Fatalf("len(EnabledCanaries) = %d, want 3", len(canaries))
	}
}
