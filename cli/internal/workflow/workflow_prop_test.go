package workflow

import (
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"gopkg.in/yaml.v3"
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

func genRecognizedWorkflowClass() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		string(ClassDelivery),
		string(ClassHarnessMaintenance),
		string(ClassOps),
	})
}

func genInvalidWorkflowClass() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		for {
			class := rapid.StringMatching(`[a-z][a-z-]{0,31}`).Draw(t, "class")
			if err := validateClass(Class(class)); err != nil {
				return class
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

func TestPropValidateClassAcceptsRecognizedClasses(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		class := genRecognizedWorkflowClass().Draw(t, "class")
		if err := validateClass(Class(class)); err != nil {
			t.Fatalf("validateClass(%q) error = %v", class, err)
		}
	})
}

func TestPropValidateClassRejectsUnknownClasses(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		class := genInvalidWorkflowClass().Draw(t, "class")
		err := validateClass(Class(class))
		if err == nil {
			t.Fatalf("validateClass(%q) error = nil", class)
		}
		if !strings.Contains(err.Error(), class) {
			t.Fatalf("validateClass(%q) error = %q, want mention of class", class, err.Error())
		}
	})
}

func TestPropWorkflowTierYAMLRoundTripPreservesPointerSemantics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		optionalTier := func(label string) *string {
			kind := rapid.SampledFrom([]string{"nil", "empty", "value"}).Draw(t, label+"-kind")
			switch kind {
			case "nil":
				return nil
			case "empty":
				value := ""
				return &value
			default:
				value := rapid.StringMatching(`[a-z][a-z0-9-]{0,7}`).Draw(t, label+"-value")
				return &value
			}
		}

		wf := Workflow{
			Name:   rapid.StringMatching(`[a-z][a-z0-9-]{0,7}`).Draw(t, "workflow-name"),
			Tier:   optionalTier("workflow-tier"),
			Phases: []Phase{{Name: "analyze", Tier: optionalTier("phase-tier")}},
		}

		data, err := yaml.Marshal(wf)
		if err != nil {
			t.Fatalf("yaml.Marshal() error = %v", err)
		}

		var roundTripped Workflow
		if err := yaml.Unmarshal(data, &roundTripped); err != nil {
			t.Fatalf("yaml.Unmarshal() error = %v", err)
		}

		assertOptional := func(name string, want, got *string) {
			switch {
			case want == nil && got != nil:
				t.Fatalf("%s = %q, want nil", name, *got)
			case want != nil && got == nil:
				t.Fatalf("%s = nil, want %q", name, *want)
			case want != nil && got != nil && *want != *got:
				t.Fatalf("%s = %q, want %q", name, *got, *want)
			}
		}

		assertOptional("workflow tier", wf.Tier, roundTripped.Tier)
		assertOptional("phase tier", wf.Phases[0].Tier, roundTripped.Phases[0].Tier)
	})
}
