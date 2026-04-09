package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const manifestFileName = "evidence-manifest.json"

var safePathComponent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

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

// String returns the human-readable serialized name for a level.
func (l Level) String() string {
	if l == Untyped {
		return "untyped"
	}
	return string(l)
}

// MarshalText encodes a level for use in text-based formats and map keys.
func (l Level) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

// UnmarshalText decodes a level from its serialized text form.
func (l *Level) UnmarshalText(text []byte) error {
	level, err := parseLevel(string(text))
	if err != nil {
		return fmt.Errorf("unmarshal Level: %w", err)
	}
	*l = level
	return nil
}

// MarshalJSON encodes a level using its serialized string form.
func (l Level) MarshalJSON() ([]byte, error) {
	return json.Marshal(l.String())
}

// UnmarshalJSON decodes a level from its serialized string form.
func (l *Level) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("unmarshal Level: %w", err)
	}
	return l.UnmarshalText([]byte(s))
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

// Artifact captures a persisted evidence attachment.
type Artifact struct {
	Path        string `json:"path"`
	MediaType   string `json:"media_type,omitempty"`
	Description string `json:"description,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
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

func parseLevel(s string) (Level, error) {
	switch Level(s) {
	case Untyped:
		return Untyped, nil
	case Proved:
		return Proved, nil
	case MechanicallyChecked:
		return MechanicallyChecked, nil
	case BehaviorallyChecked:
		return BehaviorallyChecked, nil
	case ObservedInSitu:
		return ObservedInSitu, nil
	case Level("untyped"):
		return Untyped, nil
	default:
		return "", fmt.Errorf("invalid level %q", s)
	}
}

func validatePathComponent(component string) error {
	if component == "" {
		return fmt.Errorf("path component must not be empty")
	}
	if strings.Contains(component, "..") {
		return fmt.Errorf("path component must not contain %q", "..")
	}
	if !safePathComponent.MatchString(component) {
		return fmt.Errorf("path component %q contains invalid characters (allowed: a-zA-Z0-9._-)", component)
	}
	return nil
}

func validateArtifactRelativePath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("artifact path must not be empty")
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean == "." || clean == "/" {
		return "", fmt.Errorf("artifact path must not resolve to current directory")
	}
	if strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("artifact path must be relative")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("artifact path must not escape the evidence directory")
	}
	for _, component := range strings.Split(clean, "/") {
		if err := validatePathComponent(component); err != nil {
			return "", fmt.Errorf("artifact path component %q: %w", component, err)
		}
	}
	return clean, nil
}

func validateManifestClaims(manifest *Manifest) error {
	for i, claim := range manifest.Claims {
		if !claim.Level.Valid() {
			return fmt.Errorf("claim %d has invalid level %q", i, claim.Level)
		}
	}
	return nil
}

// SaveManifest writes a manifest to <stateDir>/phases/<vesselID>/evidence-manifest.json.
func SaveManifest(stateDir, vesselID string, manifest *Manifest) error {
	if manifest == nil {
		return fmt.Errorf("save manifest: manifest must not be nil")
	}
	if manifest.VesselID == "" {
		manifest.VesselID = vesselID
	} else if vesselID != "" && manifest.VesselID != vesselID {
		return fmt.Errorf("save manifest: vessel ID mismatch: param %q manifest %q", vesselID, manifest.VesselID)
	}
	if err := validatePathComponent(manifest.VesselID); err != nil {
		return fmt.Errorf("save manifest: invalid vessel ID: %w", err)
	}
	if err := validateManifestClaims(manifest); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	path := filepath.Join(stateDir, "phases", manifest.VesselID, manifestFileName)
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
	if err := validatePathComponent(vesselID); err != nil {
		return nil, fmt.Errorf("load manifest: invalid vessel ID: %w", err)
	}

	path := filepath.Join(stateDir, "phases", vesselID, manifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load manifest: read: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("load manifest: unmarshal: %w", err)
	}
	if manifest.VesselID != vesselID {
		return nil, fmt.Errorf("load manifest: vessel ID mismatch: path %q manifest %q", vesselID, manifest.VesselID)
	}
	if err := validateManifestClaims(&manifest); err != nil {
		return nil, fmt.Errorf("load manifest: %w", err)
	}
	manifest.BuildSummary()

	return &manifest, nil
}

// SaveArtifact writes an attachment to <stateDir>/phases/<vesselID>/evidence/<name>.
func SaveArtifact(stateDir, vesselID, name string, data []byte, mediaType, description string) (Artifact, error) {
	if err := validatePathComponent(vesselID); err != nil {
		return Artifact{}, fmt.Errorf("save artifact: invalid vessel ID: %w", err)
	}
	cleanName, err := validateArtifactRelativePath(name)
	if err != nil {
		return Artifact{}, fmt.Errorf("save artifact: %w", err)
	}

	path := filepath.Join(stateDir, "phases", vesselID, "evidence", filepath.FromSlash(cleanName))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, fmt.Errorf("save artifact: create dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Artifact{}, fmt.Errorf("save artifact: write: %w", err)
	}

	return Artifact{
		Path:        filepath.ToSlash(filepath.Join("phases", vesselID, "evidence", cleanName)),
		MediaType:   mediaType,
		Description: description,
		SizeBytes:   int64(len(data)),
	}, nil
}
