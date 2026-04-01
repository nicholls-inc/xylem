package evidence

import (
	"os"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func genLevel() *rapid.Generator[Level] {
	return rapid.SampledFrom([]Level{
		Proved,
		MechanicallyChecked,
		BehaviorallyChecked,
		ObservedInSitu,
		Untyped,
	})
}

func genInvalidLevel() *rapid.Generator[Level] {
	return rapid.Custom(func(t *rapid.T) Level {
		for {
			level := Level(rapid.StringMatching(`[A-Za-z][A-Za-z_-]{0,23}`).Draw(t, "invalid_level"))
			if !level.Valid() {
				return level
			}
		}
	})
}

func genClaim() *rapid.Generator[Claim] {
	return rapid.Custom(func(t *rapid.T) Claim {
		return Claim{
			Claim:         rapid.StringMatching(`[a-z]{3,20}( [a-z]{3,20}){0,3}`).Draw(t, "claim"),
			Level:         genLevel().Draw(t, "level"),
			Checker:       rapid.StringMatching(`[a-z]{2,12}([ ./-][a-z0-9]{1,12}){0,3}`).Draw(t, "checker"),
			TrustBoundary: rapid.StringMatching(`[A-Za-z]{3,16}( [A-Za-z]{2,16}){0,4}`).Draw(t, "trust_boundary"),
			ArtifactPath:  rapid.StringMatching(`[a-z]{2,12}(/[a-z]{2,12}){0,3}\.[a-z]{2,4}`).Draw(t, "artifact_path"),
			Phase:         rapid.StringMatching(`[a-z][a-z0-9-]{1,15}`).Draw(t, "phase"),
			Passed:        rapid.Bool().Draw(t, "passed"),
			Timestamp: time.Unix(
				int64(rapid.IntRange(0, 2_000_000_000).Draw(t, "timestamp_secs")),
				int64(rapid.IntRange(0, 999_999_999).Draw(t, "timestamp_nanos")),
			).UTC(),
		}
	})
}

func genManifest() *rapid.Generator[Manifest] {
	return rapid.Custom(func(t *rapid.T) Manifest {
		return Manifest{
			VesselID:  rapid.StringMatching(`[a-z0-9][a-z0-9-]{1,15}`).Draw(t, "vessel_id"),
			Workflow:  rapid.StringMatching(`[a-z][a-z-]{1,15}`).Draw(t, "workflow"),
			Claims:    rapid.SliceOfN(genClaim(), 0, 50).Draw(t, "claims"),
			CreatedAt: time.Unix(int64(rapid.IntRange(0, 2_000_000_000).Draw(t, "created_at_secs")), 0).UTC(),
		}
	})
}

func TestPropLevelValidAcceptsAllValid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		level := genLevel().Draw(t, "level")
		if !level.Valid() {
			t.Fatalf("Valid() = false for %q, want true", level)
		}
	})
}

func TestPropLevelValidRejectsInvalid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		level := genInvalidLevel().Draw(t, "level")
		if level.Valid() {
			t.Fatalf("Valid() = true for %q, want false", level)
		}
	})
}

func TestPropRankMatchesSpecification(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genLevel().Draw(t, "a")
		b := genLevel().Draw(t, "b")

		if got := a.Rank(); got != expectedRank(a) {
			t.Fatalf("%q.Rank() = %d, want %d", a, got, expectedRank(a))
		}
		if got := b.Rank(); got != expectedRank(b) {
			t.Fatalf("%q.Rank() = %d, want %d", b, got, expectedRank(b))
		}
		if a != b && a.Rank() == b.Rank() {
			t.Fatalf("distinct levels %q and %q should not share rank %d", a, b, a.Rank())
		}
	})
}

func TestPropBuildSummaryTotalEqualsPassedPlusFailed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		manifest := genManifest().Draw(t, "manifest")
		manifest.BuildSummary()

		if manifest.Summary.Total != manifest.Summary.Passed+manifest.Summary.Failed {
			t.Fatalf("Total = %d, want %d", manifest.Summary.Total, manifest.Summary.Passed+manifest.Summary.Failed)
		}
	})
}

func TestPropBuildSummaryByLevelSumsToTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		manifest := genManifest().Draw(t, "manifest")
		manifest.BuildSummary()

		var total int
		for _, count := range manifest.Summary.ByLevel {
			total += count
		}
		if total != manifest.Summary.Total {
			t.Fatalf("sum(ByLevel) = %d, want %d", total, manifest.Summary.Total)
		}
	})
}

func TestPropBuildSummaryIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		manifest := genManifest().Draw(t, "manifest")
		manifest.BuildSummary()
		first := manifest.Summary
		manifest.BuildSummary()

		if !reflect.DeepEqual(manifest.Summary, first) {
			t.Fatalf("BuildSummary() not idempotent: got %#v, want %#v", manifest.Summary, first)
		}
	})
}

func TestPropStrongestLevelNeverExceedsMaxPassing(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		manifest := genManifest().Draw(t, "manifest")

		maxPassing := Untyped
		foundPassing := false
		for _, claim := range manifest.Claims {
			if !claim.Passed {
				continue
			}
			foundPassing = true
			if claim.Level.Rank() > maxPassing.Rank() {
				maxPassing = claim.Level
			}
		}

		strongest := manifest.StrongestLevel()
		if strongest.Rank() > maxPassing.Rank() {
			t.Fatalf("StrongestLevel() rank = %d, max passing rank = %d", strongest.Rank(), maxPassing.Rank())
		}
		if foundPassing && strongest != maxPassing {
			t.Fatalf("StrongestLevel() = %q, want %q", strongest, maxPassing)
		}
		if !foundPassing && strongest != Untyped {
			t.Fatalf("StrongestLevel() = %q, want %q", strongest, Untyped)
		}
	})
}

func expectedRank(level Level) int {
	switch level {
	case Proved:
		return 4
	case MechanicallyChecked:
		return 3
	case BehaviorallyChecked:
		return 2
	case ObservedInSitu:
		return 1
	case Untyped:
		return 0
	default:
		return -1
	}
}

func TestPropSaveLoadRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		manifest := genManifest().Draw(t, "manifest")

		stateDir, err := os.MkdirTemp("", "evidence-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(stateDir)

		if err := SaveManifest(stateDir, manifest.VesselID, &manifest); err != nil {
			t.Fatalf("SaveManifest() error = %v", err)
		}

		loaded, err := LoadManifest(stateDir, manifest.VesselID)
		if err != nil {
			t.Fatalf("LoadManifest() error = %v", err)
		}

		if loaded.VesselID != manifest.VesselID {
			t.Fatalf("VesselID = %q, want %q", loaded.VesselID, manifest.VesselID)
		}
		if loaded.Workflow != manifest.Workflow {
			t.Fatalf("Workflow = %q, want %q", loaded.Workflow, manifest.Workflow)
		}
		if !loaded.CreatedAt.Equal(manifest.CreatedAt) {
			t.Fatalf("CreatedAt = %s, want %s", loaded.CreatedAt, manifest.CreatedAt)
		}
		if len(loaded.Claims) != len(manifest.Claims) {
			t.Fatalf("len(Claims) = %d, want %d", len(loaded.Claims), len(manifest.Claims))
		}

		for i := range loaded.Claims {
			got := loaded.Claims[i]
			want := manifest.Claims[i]
			if got.Claim != want.Claim {
				t.Fatalf("Claims[%d].Claim = %q, want %q", i, got.Claim, want.Claim)
			}
			if got.Level != want.Level {
				t.Fatalf("Claims[%d].Level = %q, want %q", i, got.Level, want.Level)
			}
			if got.Checker != want.Checker {
				t.Fatalf("Claims[%d].Checker = %q, want %q", i, got.Checker, want.Checker)
			}
			if got.TrustBoundary != want.TrustBoundary {
				t.Fatalf("Claims[%d].TrustBoundary = %q, want %q", i, got.TrustBoundary, want.TrustBoundary)
			}
			if got.ArtifactPath != want.ArtifactPath {
				t.Fatalf("Claims[%d].ArtifactPath = %q, want %q", i, got.ArtifactPath, want.ArtifactPath)
			}
			if got.Phase != want.Phase {
				t.Fatalf("Claims[%d].Phase = %q, want %q", i, got.Phase, want.Phase)
			}
			if got.Passed != want.Passed {
				t.Fatalf("Claims[%d].Passed = %t, want %t", i, got.Passed, want.Passed)
			}
			if !got.Timestamp.Equal(want.Timestamp) {
				t.Fatalf("Claims[%d].Timestamp = %s, want %s", i, got.Timestamp, want.Timestamp)
			}
		}

		if !reflect.DeepEqual(loaded.Summary, manifest.Summary) {
			t.Fatalf("Summary = %#v, want %#v", loaded.Summary, manifest.Summary)
		}
	})
}
