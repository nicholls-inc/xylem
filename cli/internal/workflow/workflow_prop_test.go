package workflow

import (
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/policy"
	"pgregory.net/rapid"
)

func genRecognizedGateEvidenceLevel() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		string(evidence.Proved),
		string(evidence.MechanicallyChecked),
		string(evidence.BehaviorallyChecked),
		string(evidence.ObservedInSitu),
	})
}

func genInvalidGateEvidenceLevel() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		for {
			level := rapid.StringMatching(`[A-Za-z][A-Za-z_-]{0,31}`).Draw(t, "level")
			if parsed := evidence.Level(level); !parsed.Valid() {
				return level
			}
		}
	})
}

func TestPropValidateGateAcceptsRecognizedEvidenceLevels(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		level := genRecognizedGateEvidenceLevel().Draw(t, "level")
		err := validateGate("analyze", &Gate{
			Type: "command",
			Run:  "go test ./...",
			Evidence: &GateEvidence{
				Level: level,
			},
		})
		if err != nil {
			t.Fatalf("validateGate() error = %v for level %q", err, level)
		}
	})
}

func TestPropValidateGateRejectsUnknownEvidenceLevels(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		level := genInvalidGateEvidenceLevel().Draw(t, "level")
		err := validateGate("analyze", &Gate{
			Type: "command",
			Run:  "go test ./...",
			Evidence: &GateEvidence{
				Level: level,
			},
		})
		if err == nil {
			t.Fatalf("validateGate() error = nil for invalid level %q", level)
		}
		if !strings.Contains(err.Error(), level) {
			t.Fatalf("validateGate() error = %q, want it to mention %q", err.Error(), level)
		}
	})
}

func TestPropValidateGateAllowsEmptyEvidenceLevel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		err := validateGate("analyze", &Gate{
			Type: "command",
			Run:  "go test ./...",
			Evidence: &GateEvidence{
				Claim:         rapid.StringMatching(`[A-Za-z][A-Za-z ]{0,31}`).Draw(t, "claim"),
				Checker:       rapid.StringMatching(`[a-z][a-z0-9 ./-]{0,31}`).Draw(t, "checker"),
				TrustBoundary: rapid.StringMatching(`[A-Za-z][A-Za-z ]{0,63}`).Draw(t, "trust_boundary"),
			},
		})
		if err != nil {
			t.Fatalf("validateGate() error = %v for empty level", err)
		}
	})
}

func TestPropNormalizeClassMigratesLegacyProtectedWriteFlags(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		allowAdditive := rapid.Bool().Draw(t, "allowAdditive")
		allowCanonical := rapid.Bool().Draw(t, "allowCanonical")
		wf := &Workflow{
			AllowAdditiveProtectedWrites:  allowAdditive,
			AllowCanonicalProtectedWrites: allowCanonical,
		}

		wf.normalizeClass()

		want := policy.Delivery
		if allowAdditive || allowCanonical {
			want = policy.HarnessMaintenance
		}
		if wf.Class != string(want) {
			t.Fatalf("normalizeClass() = %q, want %q", wf.Class, want)
		}
	})
}
