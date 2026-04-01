package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const manifestFileName = "evidence-manifest.json"

// Level describes the strength of a verification claim.
type Level string

const (
	Proved              Level = "proved"
	MechanicallyChecked Level = "mechanically_checked"
	BehaviorallyChecked Level = "behaviorally_checked"
	ObservedInSitu      Level = "observed_in_situ"
	Untyped             Level = ""
)

// Valid reports whether l is one of the recognized evidence levels.
func (l Level) Valid() bool {
	switch l {
	case Proved, MechanicallyChecked, BehaviorallyChecked, ObservedInSitu, Untyped:
		return true
	default:
		return false
	}
}

// Rank returns the strength ordering for an evidence level.
func (l Level) Rank() int {
	switch l {
	case Proved:
		return 4
	case MechanicallyChecked:
		return 3
	case BehaviorallyChecked:
		return 2
	case ObservedInSitu:
		return 1
	default:
		return 0
	}
}

// String returns the serialized level string, or "untyped" for the zero value.
func (l Level) String() string {
	if l == Untyped {
		return "untyped"
	}
	return string(l)
}

// Claim captures a single verification claim and its provenance metadata.
type Claim struct {
	Claim         string    `json:"claim"`
	Level         Level     `json:"level"`
	Checker       string    `json:"checker,omitempty"`
	TrustBoundary string    `json:"trust_boundary,omitempty"`
	ArtifactPath  string    `json:"artifact_path,omitempty"`
	Phase         string    `json:"phase,omitempty"`
	Passed        bool      `json:"passed"`
	Timestamp     time.Time `json:"timestamp"`
}

// Manifest records the evidence collected for a vessel execution.
type Manifest struct {
	VesselID  string          `json:"vessel_id"`
	Workflow  string          `json:"workflow"`
	Claims    []Claim         `json:"claims"`
	CreatedAt time.Time       `json:"created_at"`
	Summary   ManifestSummary `json:"summary"`
}

// ManifestSummary provides aggregate counts over a manifest's claims.
type ManifestSummary struct {
	Total   int           `json:"total"`
	Passed  int           `json:"passed"`
	Failed  int           `json:"failed"`
	ByLevel map[Level]int `json:"by_level"`
}

// BuildSummary rebuilds the manifest summary from the claim list.
func (m *Manifest) BuildSummary() {
	summary := ManifestSummary{
		ByLevel: make(map[Level]int),
	}

	for _, claim := range m.Claims {
		summary.Total++
		if claim.Passed {
			summary.Passed++
		} else {
			summary.Failed++
		}
		summary.ByLevel[claim.Level]++
	}

	m.Summary = summary
}

// StrongestLevel returns the highest-ranked level among passing claims.
func (m *Manifest) StrongestLevel() Level {
	strongest := Untyped
	for _, claim := range m.Claims {
		if !claim.Passed {
			continue
		}
		if claim.Level.Rank() > strongest.Rank() {
			strongest = claim.Level
		}
	}
	return strongest
}

// SaveManifest writes a manifest to <stateDir>/phases/<vesselID>/evidence-manifest.json.
func SaveManifest(stateDir, vesselID string, manifest *Manifest) error {
	path := filepath.Join(stateDir, "phases", vesselID, manifestFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save manifest: create dir: %w", err)
	}

	manifest.BuildSummary()

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("save manifest: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save manifest: write: %w", err)
	}
	return nil
}

// LoadManifest reads a manifest from <stateDir>/phases/<vesselID>/evidence-manifest.json.
func LoadManifest(stateDir, vesselID string) (*Manifest, error) {
	path := filepath.Join(stateDir, "phases", vesselID, manifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load manifest: read: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("load manifest: unmarshal: %w", err)
	}
	return &manifest, nil
}
