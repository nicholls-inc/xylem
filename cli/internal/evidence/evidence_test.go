package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSmoke_S1_LevelValidAcceptsAllNamedLevelsIncludingUntyped(t *testing.T) {
	t.Parallel()

	levels := []Level{
		Proved,
		MechanicallyChecked,
		BehaviorallyChecked,
		ObservedInSitu,
		Untyped,
	}

	for _, level := range levels {
		level := level
		t.Run(level.String(), func(t *testing.T) {
			if !level.Valid() {
				t.Fatalf("Valid() = false for %q, want true", level)
			}
		})
	}
}

func TestSmoke_S2_LevelValidRejectsArbitraryStrings(t *testing.T) {
	t.Parallel()

	levels := []Level{
		"high",
		"none",
		"PROVED",
		"mechanically-checked",
	}

	for _, level := range levels {
		level := level
		t.Run(string(level), func(t *testing.T) {
			if level.Valid() {
				t.Fatalf("Valid() = true for %q, want false", level)
			}
		})
	}
}

func TestSmoke_S3_LevelRankOrderingProvedIsStrongestUntypedIsWeakest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level Level
		want  int
	}{
		{level: Proved, want: 4},
		{level: MechanicallyChecked, want: 3},
		{level: BehaviorallyChecked, want: 2},
		{level: ObservedInSitu, want: 1},
		{level: Untyped, want: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.level.String(), func(t *testing.T) {
			if got := tt.level.Rank(); got != tt.want {
				t.Fatalf("Rank() = %d, want %d", got, tt.want)
			}
		})
	}

	if Proved.Rank() <= MechanicallyChecked.Rank() ||
		MechanicallyChecked.Rank() <= BehaviorallyChecked.Rank() ||
		BehaviorallyChecked.Rank() <= ObservedInSitu.Rank() ||
		ObservedInSitu.Rank() <= Untyped.Rank() {
		t.Fatal("rank ordering did not match expected descending strength")
	}
}

func TestSmoke_S4_ManifestBuildSummaryCountsTotalPassedAndFailedClaims(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		Claims: []Claim{
			{Claim: "claim 1", Passed: true},
			{Claim: "claim 2", Passed: true},
			{Claim: "claim 3", Passed: false},
		},
	}

	manifest.BuildSummary()

	if manifest.Summary.Total != 3 {
		t.Fatalf("Total = %d, want 3", manifest.Summary.Total)
	}
	if manifest.Summary.Passed != 2 {
		t.Fatalf("Passed = %d, want 2", manifest.Summary.Passed)
	}
	if manifest.Summary.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", manifest.Summary.Failed)
	}
}

func TestSmoke_S5_ManifestBuildSummaryGroupsClaimsByLevel(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		Claims: []Claim{
			{Claim: "claim 1", Level: BehaviorallyChecked, Passed: true},
			{Claim: "claim 2", Level: BehaviorallyChecked, Passed: false},
			{Claim: "claim 3", Level: MechanicallyChecked, Passed: true},
			{Claim: "claim 4", Level: Untyped, Passed: true},
		},
	}

	manifest.BuildSummary()

	if got := manifest.Summary.ByLevel[BehaviorallyChecked]; got != 2 {
		t.Fatalf("ByLevel[%q] = %d, want 2", BehaviorallyChecked, got)
	}
	if got := manifest.Summary.ByLevel[MechanicallyChecked]; got != 1 {
		t.Fatalf("ByLevel[%q] = %d, want 1", MechanicallyChecked, got)
	}
	if got := manifest.Summary.ByLevel[Untyped]; got != 1 {
		t.Fatalf("ByLevel[%q] = %d, want 1", Untyped, got)
	}

	if _, ok := manifest.Summary.ByLevel[Proved]; ok {
		t.Fatalf("ByLevel[%q] should be absent", Proved)
	}
	if _, ok := manifest.Summary.ByLevel[ObservedInSitu]; ok {
		t.Fatalf("ByLevel[%q] should be absent", ObservedInSitu)
	}
}

func TestSmoke_S6_ManifestStrongestLevelReturnsHighestRankedPassingClaimLevel(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		Claims: []Claim{
			{Claim: "observed", Level: ObservedInSitu, Passed: true},
			{Claim: "behavioral", Level: BehaviorallyChecked, Passed: true},
			{Claim: "mechanical", Level: MechanicallyChecked, Passed: true},
		},
	}

	if got := manifest.StrongestLevel(); got != MechanicallyChecked {
		t.Fatalf("StrongestLevel() = %q, want %q", got, MechanicallyChecked)
	}
}

func TestSmoke_S7_ManifestStrongestLevelReturnsUntypedWhenNoClaimsPassed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		manifest Manifest
	}{
		{
			name: "all failed",
			manifest: Manifest{
				Claims: []Claim{
					{Claim: "claim 1", Level: Proved, Passed: false},
					{Claim: "claim 2", Level: MechanicallyChecked, Passed: false},
				},
			},
		},
		{
			name:     "empty",
			manifest: Manifest{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.manifest.StrongestLevel(); got != Untyped {
				t.Fatalf("StrongestLevel() = %q, want %q", got, Untyped)
			}
		})
	}
}

func TestSmoke_S8_SaveManifestWritesJSONToTheExpectedPath(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	manifest := &Manifest{
		VesselID:  "vessel-abc123",
		Workflow:  "fix-bug",
		CreatedAt: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC),
		Claims: []Claim{
			{
				Claim:     "tests pass",
				Level:     BehaviorallyChecked,
				Passed:    true,
				Timestamp: time.Date(2026, time.January, 1, 12, 1, 0, 0, time.UTC),
			},
		},
	}

	if err := SaveManifest(stateDir, "vessel-abc123", manifest); err != nil {
		t.Fatalf("SaveManifest() error = %v", err)
	}

	path := filepath.Join(stateDir, "phases", "vessel-abc123", "evidence-manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal(raw) error = %v", err)
	}
	for _, key := range []string{"vessel_id", "claims", "summary"} {
		_, ok := raw[key]
		if !ok {
			t.Fatalf("expected JSON key %q to be present", key)
		}
	}

	var got Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(manifest) error = %v", err)
	}
	if got.VesselID != "vessel-abc123" {
		t.Fatalf("VesselID = %q, want %q", got.VesselID, "vessel-abc123")
	}
}

func TestSmoke_S9_SaveManifestCallsBuildSummaryBeforeWriting(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	manifest := &Manifest{
		VesselID: "vessel-xyz",
		Workflow: "fix-bug",
		Claims: []Claim{
			{Claim: "passed", Passed: true, Level: BehaviorallyChecked},
			{Claim: "failed", Passed: false, Level: MechanicallyChecked},
		},
	}

	if err := SaveManifest(stateDir, "vessel-xyz", manifest); err != nil {
		t.Fatalf("SaveManifest() error = %v", err)
	}

	got, err := LoadManifest(stateDir, "vessel-xyz")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}

	if got.Summary.Total != 2 {
		t.Fatalf("Total = %d, want 2", got.Summary.Total)
	}
	if got.Summary.Passed != 1 {
		t.Fatalf("Passed = %d, want 1", got.Summary.Passed)
	}
	if got.Summary.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", got.Summary.Failed)
	}
}

func TestSmoke_S10_LoadManifestRoundTripsCorrectly(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	manifest := &Manifest{
		VesselID:  "vessel-roundtrip",
		Workflow:  "fix-bug",
		CreatedAt: time.Date(2026, time.January, 2, 9, 30, 0, 0, time.UTC),
		Claims: []Claim{
			{
				Claim:         "tests pass",
				Level:         BehaviorallyChecked,
				Checker:       "go test ./...",
				TrustBoundary: "Package-level only",
				ArtifactPath:  "cli/internal/evidence/evidence_test.go",
				Phase:         "verify",
				Passed:        true,
				Timestamp:     time.Date(2026, time.January, 2, 9, 31, 0, 0, time.UTC),
			},
			{
				Claim:         "binary builds",
				Level:         MechanicallyChecked,
				Checker:       "go build ./cmd/xylem",
				TrustBoundary: "Build succeeds locally",
				ArtifactPath:  "cli/cmd/xylem",
				Phase:         "build",
				Passed:        false,
				Timestamp:     time.Date(2026, time.January, 2, 9, 32, 0, 0, time.UTC),
			},
		},
	}

	if err := SaveManifest(stateDir, manifest.VesselID, manifest); err != nil {
		t.Fatalf("SaveManifest() error = %v", err)
	}

	got, err := LoadManifest(stateDir, manifest.VesselID)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}

	if got.VesselID != manifest.VesselID {
		t.Fatalf("VesselID = %q, want %q", got.VesselID, manifest.VesselID)
	}
	if got.Workflow != manifest.Workflow {
		t.Fatalf("Workflow = %q, want %q", got.Workflow, manifest.Workflow)
	}
	if !got.CreatedAt.Equal(manifest.CreatedAt) {
		t.Fatalf("CreatedAt = %s, want %s", got.CreatedAt, manifest.CreatedAt)
	}
	if len(got.Claims) != len(manifest.Claims) {
		t.Fatalf("len(Claims) = %d, want %d", len(got.Claims), len(manifest.Claims))
	}

	for i := range got.Claims {
		assertClaimEqual(t, got.Claims[i], manifest.Claims[i])
	}
	if !reflect.DeepEqual(got.Summary, manifest.Summary) {
		t.Fatalf("Summary = %#v, want %#v", got.Summary, manifest.Summary)
	}
}

func assertClaimEqual(t *testing.T, got, want Claim) {
	t.Helper()

	if got.Claim != want.Claim {
		t.Fatalf("Claim = %q, want %q", got.Claim, want.Claim)
	}
	if got.Level != want.Level {
		t.Fatalf("Level = %q, want %q", got.Level, want.Level)
	}
	if got.Checker != want.Checker {
		t.Fatalf("Checker = %q, want %q", got.Checker, want.Checker)
	}
	if got.TrustBoundary != want.TrustBoundary {
		t.Fatalf("TrustBoundary = %q, want %q", got.TrustBoundary, want.TrustBoundary)
	}
	if got.ArtifactPath != want.ArtifactPath {
		t.Fatalf("ArtifactPath = %q, want %q", got.ArtifactPath, want.ArtifactPath)
	}
	if got.Phase != want.Phase {
		t.Fatalf("Phase = %q, want %q", got.Phase, want.Phase)
	}
	if got.Passed != want.Passed {
		t.Fatalf("Passed = %t, want %t", got.Passed, want.Passed)
	}
	if !got.Timestamp.Equal(want.Timestamp) {
		t.Fatalf("Timestamp = %s, want %s", got.Timestamp, want.Timestamp)
	}
}
